// confirm.go — CHO-2040 (owner ruling R8-5): pure core of the ceremony confirm
// hook (POST /v1/me/companions/{id}/ceremony/edge-scout/confirm). The FE
// orchestrates: CHO-2038 POST first (mint the ticked learning edges on the
// map), then this hook with the SAME response body. Effect (c):
//
//   - remediate ticks → ONE chora.consumption.weakness.analyzed.v1 event via
//     the outbox, with output_selection.familiar_coaching=true so the LIVE
//     analyzed subscriber both upserts the LearnerWeakness aggregate (source
//     = explicit) and writes the per-Companion RAG coaching memory
//     (weakness_companion_rag.go) — ceremony ticks feed the Companion.
//   - explore ticks → ONE plain ceremony memory-note on the companion via the
//     existing memory-note sink (memory_type='ceremony').
//
// This file is the handler-independent logic: validation, the
// remediate/explore split, the DETERMINISTIC synthetic upload id (the dedup
// carrier — outbox idempotency_key + the subscriber's (tenant|gcid|upload_id)
// inbox key both derive from it, so a re-confirm can never double-feed), the
// ceremony strength/confidence defaults, and the factual explore note.
package edgescout

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// ConfirmEdge is one ticked ceremony edge echoed back from the CHO-2038 POST
// response: the minted ConceptNode id, its title, and the learner's labelled
// intent.
type ConfirmEdge struct {
	ConceptID string
	Title     string
	Intent    Intent
}

// MaxConfirmTitleLen bounds a ticked edge title (the CHO-2038 mint accepted
// it already; this guards the echo against tampering/bloat).
const MaxConfirmTitleLen = 200

// Ceremony remediate ticks are LEARNER-DECLARED weaknesses, not
// evidence-graded diagnoses. The published edge carries:
//
//   - strength 0.6 — moderately shaky: strong enough to surface in the dose /
//     Growth-Edges views, deliberately below a graded-catastrophic ~0.9 (the
//     LearnerWeakness upsert takes max(existing, new), so a harder-evidenced
//     row is never watered down by a ceremony tick).
//   - confidence 1.0 — the learner explicitly ticked it; there is no analyser
//     uncertainty to model.
const (
	CeremonyRemediateStrength   = 0.6
	CeremonyRemediateConfidence = 1.0
)

// confirmUploadIDPrefix namespaces the deterministic id derivation (a
// different ceremony surface reusing the same triple can never collide).
const confirmUploadIDPrefix = "ceremony-edge-scout-confirm"

// ValidateConfirmEdges enforces the R8-5 request contract: 1..MaxCandidates
// edges; every edge carries a non-blank concept id, a non-blank title within
// MaxConfirmTitleLen, and a valid remediate|explore intent; concept ids do
// not repeat. UUID-SHAPE checking stays in the HTTP layer (its uuidShaped
// helper); title→concept-key normalisation checking stays where the key is
// derived. Returns a descriptive error for the 400 body.
func ValidateConfirmEdges(edges []ConfirmEdge) error {
	if len(edges) == 0 {
		return fmt.Errorf("edges required (at least one ticked edge)")
	}
	if len(edges) > MaxCandidates {
		return fmt.Errorf("too many edges: %d > the ceremony cap %d", len(edges), MaxCandidates)
	}
	seen := make(map[string]bool, len(edges))
	for i, e := range edges {
		cid := strings.TrimSpace(e.ConceptID)
		if cid == "" {
			return fmt.Errorf("edges[%d]: concept_id required", i)
		}
		if seen[cid] {
			return fmt.Errorf("edges[%d]: duplicate concept_id %s", i, cid)
		}
		seen[cid] = true
		title := strings.TrimSpace(e.Title)
		if title == "" {
			return fmt.Errorf("edges[%d]: title required", i)
		}
		if len(title) > MaxConfirmTitleLen {
			return fmt.Errorf("edges[%d]: title exceeds %d chars", i, MaxConfirmTitleLen)
		}
		if !e.Intent.Valid() {
			return fmt.Errorf("edges[%d]: intent %q invalid (want remediate|explore)", i, e.Intent)
		}
	}
	return nil
}

// SplitConfirm partitions the ticked edges by intent, preserving order.
func SplitConfirm(edges []ConfirmEdge) (remediate, explore []ConfirmEdge) {
	for _, e := range edges {
		switch e.Intent {
		case IntentRemediate:
			remediate = append(remediate, e)
		case IntentExplore:
			explore = append(explore, e)
		}
	}
	return remediate, explore
}

// ConfirmUploadID derives the DETERMINISTIC synthetic upload id for the
// weakness.analyzed event from (tenant, goal, SORTED remediate concept ids).
// Properties the rest of the pipeline depends on:
//
//   - Deterministic + order-insensitive ⇒ a re-confirm of the same tick set
//     yields the same id, so the outbox idempotency_key dedups the emission
//     and the analyzed subscriber's (tenant|gcid|upload_id) inbox key dedups
//     redelivery — no double-feed (R8-5 idempotency requirement).
//   - UUID-SHAPED (RFC 4122 v8 layout over a SHA-256 prefix) ⇒ the analyzed
//     subscriber's JobCompleter UPDATE compares it against the UUID-typed
//     weakness_doc_uploads.id: a non-UUID string would 22P02 the whole event
//     into a NACK loop; a well-formed v8 UUID simply matches no row (no such
//     upload job — a harmless no-op by MarkCompleted's UPDATE semantics).
func ConfirmUploadID(tenantID, goalID string, remediateConceptIDs []string) string {
	ids := append([]string(nil), remediateConceptIDs...)
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(confirmUploadIDPrefix + "|" + tenantID + "|" + goalID + "|" + strings.Join(ids, ",")))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x80 // version 8 (custom deterministic derivation)
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ComposeExploreNote renders the ONE plain, factual ceremony memory-note for
// the explore ticks. No persona, no invention — just what the learner chose
// and toward what. An empty anchor (rootless goal, unresolvable title) falls
// back to "their goal".
func ComposeExploreNote(titles []string, goalAnchor string) string {
	kept := make([]string, 0, len(titles))
	for _, t := range titles {
		if t = strings.TrimSpace(t); t != "" {
			kept = append(kept, t)
		}
	}
	anchor := strings.TrimSpace(goalAnchor)
	if anchor == "" {
		anchor = "their goal"
	} else {
		anchor = "the goal \"" + anchor + "\""
	}
	return "At the Companion binding ceremony the learner chose to explore " +
		strings.Join(kept, ", ") + " toward " + anchor + "."
}
