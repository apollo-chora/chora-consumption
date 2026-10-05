// companion_ceremony_confirm_handler.go — CHO-2040 (owner ruling R8-5): the
// ceremony confirm hook, effect (c).
//
//	POST /v1/me/companions/{id}/ceremony/edge-scout/confirm
//	{"goal_id":"<uuid>","edges":[{"concept_id":"<uuid>","title":"...","intent":"remediate"|"explore"}]}
//
// The FE orchestrates the two writes: it POSTs the ticked edges to the
// CHO-2038 mint-by-title endpoint FIRST (KG-owned; mints the ConceptNodes on
// the learner's map) and then echoes that response here. This hook owns the
// COMPANION-side effects of the ceremony ticks:
//
//   - REMEDIATE ticks → ONE chora.consumption.weakness.analyzed.v1 event via
//     the SAME transactional outbox every consumption publisher uses. The
//     LIVE analyzed subscriber (weakness_analyzed_subscriber.go) upserts each
//     tick into the LearnerWeakness aggregate (source=explicit) and — because
//     the event carries output_selection.familiar_coaching=true — writes the
//     distilled diagnosis into the learner's per-Companion pgvector memory
//     (weakness_companion_rag.go). That is the point: ceremony ticks feed the
//     Companion's RAG.
//   - EXPLORE ticks → ONE plain ceremony memory-note on the companion via the
//     existing memory-note sink (the recap_scribe path: Embedder.Embed +
//     CompanionMemory.Record), memory_type='ceremony' (the column is free-form
//     VARCHAR(32); existing types are chat_turn/recap/weakness_diagnosis).
//
// Idempotency: the synthetic upload_id is DETERMINISTIC over (tenant, goal,
// sorted remediate concept_ids) — the outbox dedups on the derived
// idempotency_key (unique partial index) and the analyzed subscriber's inbox
// dedups on (tenant|gcid|upload_id), so a re-confirm never double-feeds.
//
// Failure semantics (loud; partial success NAMED): effects run publish →
// note. A publish failure aborts before the note (500 CONFIRM_PUBLISH_FAILED,
// nothing happened). A note failure AFTER the publish returns 500
// CONFIRM_NOTE_FAILED whose message names the already-published remediate
// count — the FE must not re-drive blindly (a re-confirm is safe for the
// event, which dedups, and retries only the note).
//
// No mana: confirm runs NO LLM turn — nothing is stamped, nothing debits.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// topicWeaknessAnalyzedPublish is the canonical topic the confirm hook
// publishes remediate ticks on (the same one this service consumes).
const topicWeaknessAnalyzedPublish = "chora.consumption.weakness.analyzed.v1"

// ceremonyModelUsed is the D2-transparency attribution stamped on the
// published event: the edges are LEARNER-CONFIRMED selections, not an
// analyser's output (the propose turn's tokens were metered separately).
const ceremonyModelUsed = "learner_selection"

// ceremonyConfirmEdgeReq is one ticked edge on the wire (the CHO-2038 POST
// response echoed back).
type ceremonyConfirmEdgeReq struct {
	ConceptID string `json:"concept_id"`
	Title     string `json:"title"`
	Intent    string `json:"intent"`
}

// ceremonyConfirmReq is the inbound body.
type ceremonyConfirmReq struct {
	GoalID string                   `json:"goal_id"`
	Edges  []ceremonyConfirmEdgeReq `json:"edges"`
}

// ceremonyConfirmResp is the wire response: how many remediate ticks rode the
// published event and how many explore ticks the ceremony note covers.
type ceremonyConfirmResp struct {
	Published int `json:"published"`
	Noted     int `json:"noted"`
}

// ceremonyEdgeScoutConfirm handles POST /v1/me/companions/{id}/ceremony/edge-scout/confirm.
func (s *Server) ceremonyEdgeScoutConfirm(w http.ResponseWriter, r *http.Request, companionID string) {
	// Wiring gate (fail-soft at boot, fail-loud per request). Goals gates
	// ownership; Publisher carries the remediate event. The note sink
	// (Embedder + CompanionMemory) is gated AFTER decode — only a body with
	// explore ticks needs it (remediate-only confirms work without it).
	if s.Goals == nil || s.Publisher == nil {
		writeError(w, http.StatusServiceUnavailable, "CEREMONY_CONFIRM_NOT_WIRED",
			"ceremony confirm ports not wired (goals/publisher) — check cmd/server wiring")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req ceremonyConfirmReq
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", derr.Error())
		return
	}
	goalID := strings.TrimSpace(req.GoalID)
	if goalID == "" || !uuidShaped(goalID) {
		writeError(w, http.StatusBadRequest, "INVALID_GOAL_REF",
			"goal_id required and must be a UUID")
		return
	}

	// ---- Edge validation: domain shape rules (count/title/intent/dupes) +
	// the transport-level UUID shape + the remediate concept-key derivation
	// (an unkeyable remediate title would be dropped SILENTLY by the
	// subscriber's defensive skip — reject it loudly here instead).
	edges := make([]edgescout.ConfirmEdge, 0, len(req.Edges))
	for i, e := range req.Edges {
		cid := strings.TrimSpace(e.ConceptID)
		if cid != "" && !uuidShaped(cid) {
			writeError(w, http.StatusBadRequest, "INVALID_EDGES",
				fmt.Sprintf("edges[%d]: concept_id must be a UUID", i))
			return
		}
		edges = append(edges, edgescout.ConfirmEdge{
			ConceptID: cid,
			Title:     strings.TrimSpace(e.Title),
			Intent:    edgescout.Intent(strings.TrimSpace(e.Intent)),
		})
	}
	if verr := edgescout.ValidateConfirmEdges(edges); verr != nil {
		writeError(w, http.StatusBadRequest, "INVALID_EDGES", verr.Error())
		return
	}
	remediate, explore := edgescout.SplitConfirm(edges)
	for _, e := range remediate {
		if lw.NormalizeConceptKey(e.Title) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_EDGES",
				fmt.Sprintf("remediate title %q does not normalise to a concept key", e.Title))
			return
		}
	}
	// Note-sink gate BEFORE any side effect — a half-wired server must never
	// half-confirm (publish without being able to note).
	if len(explore) > 0 && (s.Embedder == nil || s.CompanionMemory == nil) {
		writeError(w, http.StatusServiceUnavailable, "CEREMONY_CONFIRM_NOT_WIRED",
			"explore ticks need the companion memory sink (embedder/memory) — check cmd/server wiring")
		return
	}

	ctx := repoCtx(r, tenantID, gcid)
	if _, err := s.loadOwnedInstance(ctx, tenantID, gcid, companionID); err != nil {
		s.writeSkillError(w, err)
		return
	}

	// Goal gate — owned by the caller, READ-ONLY (mirrors the propose runner
	// + the CHO-2038 handler's no-leak rule: mismatch = 404, never 403).
	g, gerr := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, "GOAL_READ_FAILED", gerr.Error())
		return
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	// ---- Goal anchor (root-concept title, else the ConceptSet). Rides both
	// the remediate descriptors and the explore note, so it resolves BEFORE
	// any side effect; a read failure is a clean loud 500 (nothing happened).
	anchor := ""
	if g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" && s.ConceptNodes != nil {
		node, nerr := s.ConceptNodes.GetByID(ctx, tenantID, gcid, strings.TrimSpace(*g.RootConceptID))
		if nerr != nil {
			writeError(w, http.StatusInternalServerError, "CONFIRM_CONCEPT_READ_FAILED", nerr.Error())
			return
		}
		if node != nil {
			anchor = strings.TrimSpace(node.Title)
		}
	}
	if anchor == "" {
		anchor = strings.TrimSpace(strings.Join(g.ConceptSet, ", "))
	}

	// ---- Effect 1: remediate ticks → weakness.analyzed.v1 via the outbox.
	if len(remediate) > 0 {
		if perr := s.publishCeremonyRemediate(r, tenantID, gcid, goalID, anchor, remediate); perr != nil {
			writeError(w, http.StatusInternalServerError, "CONFIRM_PUBLISH_FAILED", perr.Error())
			return
		}
	}

	// ---- Effect 2: explore ticks → ONE ceremony memory-note. LOUD on
	// failure, NAMING the partial success (the event above already went out).
	if len(explore) > 0 {
		if nerr := s.recordCeremonyExploreNote(ctx, tenantID, gcid, companionID, anchor, explore); nerr != nil {
			writeError(w, http.StatusInternalServerError, "CONFIRM_NOTE_FAILED",
				fmt.Sprintf("%d remediate tick(s) already published to weakness.analyzed.v1; the explore ceremony note failed: %v (re-confirm is safe — the event dedups)", len(remediate), nerr))
			return
		}
	}

	writeJSON(w, http.StatusOK, ceremonyConfirmResp{
		Published: len(remediate),
		Noted:     len(explore),
	})
}

// publishCeremonyRemediate emits the deterministic weakness.analyzed.v1 event
// carrying the remediate ticks. ErrDuplicateIdempotencyKey from the outbox is
// SUCCESS — the prior emission of this exact tick set is already durably
// queued (re-confirm semantics).
func (s *Server) publishCeremonyRemediate(r *http.Request, tenantID, gcid, goalID, anchor string, remediate []edgescout.ConfirmEdge) error {
	conceptIDs := make([]string, 0, len(remediate))
	for _, e := range remediate {
		conceptIDs = append(conceptIDs, e.ConceptID)
	}
	uploadID := edgescout.ConfirmUploadID(tenantID, goalID, conceptIDs)

	// Factual per-edge descriptor — the analyzed subscriber persists it into
	// the LearnerWeakness descriptor AND the Companion-RAG hook surfaces its
	// "summary" line to the companion ("label — summary").
	goalName := anchor
	if goalName == "" {
		goalName = "their goal"
	}
	descriptor, derr := json.Marshal(map[string]any{
		"summary": "the learner flagged this to remediate at the Companion binding ceremony toward " + goalName,
	})
	if derr != nil {
		return fmt.Errorf("marshal ceremony descriptor: %w", derr)
	}
	edgePayloads := make([]map[string]any, 0, len(remediate))
	for _, e := range remediate {
		edgePayloads = append(edgePayloads, map[string]any{
			"concept_label":   e.Title,
			"concept_key":     lw.NormalizeConceptKey(e.Title),
			"tags":            []string{"ceremony"},
			"confidence":      edgescout.CeremonyRemediateConfidence,
			"strength":        edgescout.CeremonyRemediateStrength,
			"descriptor_json": string(descriptor),
		})
	}

	// Mandatory-field envelope (event_id UUIDv7 / idempotency_key / tenant /
	// gcid / occurred+published / traceparent / source_* / schema_version) —
	// events.NewEnvelope stamps them; the deterministic idempotency key is
	// derived from the deterministic upload id.
	tp := tracing.EnsureTraceparent(r.Header.Get("traceparent"))
	ts := r.Header.Get("tracestate")
	env := events.NewEnvelope(tenantID, gcid, tp, ts, "ceremony_edge_scout_confirm."+uploadID)
	payload := map[string]any{
		"upload_id":    uploadID,
		"tenant_id":    tenantID,
		"learner_gcid": gcid,
		"edges":        edgePayloads,
		"model_used":   ceremonyModelUsed,
		"analyzed_at":  env.OccurredAt,
		// familiar_coaching=true is the load-bearing flag: the analyzed
		// subscriber gates the per-Companion RAG write on it (ADR-205 D5 /
		// ADR-173) — ceremony ticks feed the Companion.
		"output_selection": map[string]any{
			"focused_dose":      false,
			"familiar_coaching": true,
			"practice_test":     false,
			"study_aids":        false,
		},
		// goal_id is NOT a schema field (the encoder ignores it); it labels
		// the outbox row's aggregate_id for audit/replay queries.
		"goal_id": goalID,
	}
	if perr := s.Publisher.Publish(topicWeaknessAnalyzedPublish, env, payload); perr != nil {
		if errors.Is(perr, outbox.ErrDuplicateIdempotencyKey) {
			return nil // already durably queued — the re-confirm contract
		}
		return perr
	}
	return nil
}

// recordCeremonyExploreNote persists the ONE plain ceremony memory-note for
// the explore ticks via the existing memory-note sink (the recap_scribe
// path). memory_type='ceremony'.
func (s *Server) recordCeremonyExploreNote(ctx context.Context, tenantID, gcid, companionID, anchor string, explore []edgescout.ConfirmEdge) error {
	titles := make([]string, 0, len(explore))
	for _, e := range explore {
		titles = append(titles, e.Title)
	}
	content := edgescout.ComposeExploreNote(titles, anchor)
	vec, eerr := s.Embedder.Embed(ctx, companion.EmbedInput{
		Text: content, TaskType: companion.EmbedTaskDocument, TenantID: tenantID,
	})
	if eerr != nil {
		return fmt.Errorf("embed ceremony note: %w", eerr)
	}
	if rerr := s.CompanionMemory.Record(ctx, companion.RecordMemoryInput{
		TenantID:    tenantID,
		OwnerGCID:   gcid,
		CompanionID: companionID,
		MemoryType:  "ceremony",
		ContentText: content,
		Embedding:   vec,
		ModelID:     s.CompanionEmbeddingModelID,
		Now:         time.Now().UTC(),
	}); rerr != nil {
		return fmt.Errorf("record ceremony note: %w", rerr)
	}
	return nil
}
