// Package conceptgraph is the learner-owned Discovery knowledge graph
// (ADR-212): the learner authors CONCEPTS (not atoms) and typed EDGES between
// them, re-rootable as understanding grows. It supersedes ADR-143's
// machine-authored "hexagonal fog" posture (projection-over-atoms,
// machine-only provenance, exactly-6 neighbours) while KEEPING atoms as the
// content substrate — a ConceptNode ORGANISES 0..N LearningAtoms (by UUID, no
// FK) and may be temporarily empty (the Discovery-scoped atom-centric
// amendment).
//
// SCOPE (WS-1): the ConceptNode + Edge aggregates + their invariants + ports.
// DEFERRED to later workstreams: re-rooting / re-consolidation (WS-2), the
// Companion suggestion engine + provenance events (WS-4), and the hex-fisheye
// FE (WS-5). PartitionByHexFace here is the domain seed of the soft-cap
// overflow drawer (D6), not the renderer.
//
// HEXAGONAL purity: stdlib-only, NO infra imports (mirrors goal + learner_*).
// No time.Now() leak in the aggregate clock — every clock-dependent call takes
// a `now` (constructors fall back to wall-clock only when `Now` is the zero
// value). Per ddd-enforcement: soft-delete only (#5, never hard delete); new
// ids are UUIDv7 (#7); atom + cross-concept references are opaque UUIDs without
// FK (#3, cross-aggregate).
package conceptgraph

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Provenance records HOW a concept or edge entered the learner's map (ADR-212
// D4). Learner sovereignty makes learner-authorship first-class; the LLM "fog"
// is demoted to a suggestion the learner accepts.
type Provenance string

const (
	// ProvenanceLearnerAuthored — the learner created it directly (the default).
	ProvenanceLearnerAuthored Provenance = "learner_authored"
	// ProvenanceCompanionSuggestedAccepted — the Companion proposed it and the
	// learner accepted it (suggestion-then-curate).
	ProvenanceCompanionSuggestedAccepted Provenance = "companion_suggested_accepted"
	// ProvenanceSystemDerived — derived by the platform (e.g. from a Growth-Edge
	// signal, ADR-205) and surfaced for curation.
	ProvenanceSystemDerived Provenance = "system_derived"
)

// Valid reports whether p is a recognised provenance.
func (p Provenance) Valid() bool {
	switch p {
	case ProvenanceLearnerAuthored, ProvenanceCompanionSuggestedAccepted, ProvenanceSystemDerived:
		return true
	}
	return false
}

// Intent labels WHY a ceremony-selected concept is a learning-edge for a goal
// (ADR-212/214, CHO-2038): remediate = a weakness to shore up; explore = an
// adjacent curiosity to pursue. Unspecified is the default for a plain concept
// (not a ceremony learning-edge).
type Intent string

const (
	IntentUnspecified Intent = ""
	IntentRemediate   Intent = "remediate"
	IntentExplore     Intent = "explore"
)

// Valid reports whether i is a recognised intent (unspecified included — the
// default for a plain, non-learning-edge concept).
func (i Intent) Valid() bool {
	switch i {
	case IntentUnspecified, IntentRemediate, IntentExplore:
		return true
	}
	return false
}

// Labelled reports whether i is an explicit learning-edge label (remediate or
// explore). A ceremony learning-edge MUST be Labelled; unspecified is not.
func (i Intent) Labelled() bool {
	return i == IntentRemediate || i == IntentExplore
}

// Sentinel errors (wrapped with %w so the HTTP/gRPC adapter maps each to its
// own status code).
var (
	ErrInvalid        = errors.New("conceptgraph: invalid")
	ErrConceptDeleted = errors.New("conceptgraph: concept is soft-deleted")
	ErrEdgeDeleted    = errors.New("conceptgraph: edge is soft-deleted")
	ErrSelfLoop       = errors.New("conceptgraph: an edge cannot connect a concept to itself")
)

// ConceptNode is a learner-named idea that ORGANISES 0..N LearningAtoms
// (ADR-212 D1). Learner-owned, lives in chora_consumption, and may be
// temporarily empty.
type ConceptNode struct {
	ConceptID   string
	TenantID    string
	LearnerGCID string
	Title       string
	// ConceptKey is the stable normalised slug of the title, set once at mint
	// (CHO-2038 key-at-mint). It is the qgen `target_growth_edges` concept key
	// (proofing R8-7) and shares the learner_weakness concept_key vocabulary.
	// Stable across renames (never re-derived from an edited title).
	ConceptKey string
	AtomRefs   []string // LearningAtom UUIDs; opaque cross-DB refs, no FK (#3); may be empty (D1)
	Provenance Provenance
	Intent     Intent // remediate|explore when a ceremony learning-edge; unspecified otherwise (CHO-2038)
	// SubGoal is the learner's per-node objective (ADR-247 D1): "mastery here
	// means ...". Empty when unset. It drives Cap A question generation and Cap B
	// fog suggestions so both surfaces target what the learner is trying to
	// achieve on this concept. SubGoalProvenance records WHO authored it
	// (learner_authored primary; companion_suggested is a follow-on lane).
	SubGoal           string
	SubGoalProvenance Provenance
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
}

// NewConceptNodeInput is the constructor input for a ConceptNode.
type NewConceptNodeInput struct {
	TenantID    string
	LearnerGCID string
	Title       string
	AtomRefs    []string
	Provenance  Provenance // defaults to learner_authored when empty
	Intent      Intent     // remediate|explore for a ceremony learning-edge; unspecified otherwise
	Now         time.Time
}

// NewConceptNode mints a learner-owned ConceptNode. Title is required (a
// concept is learner-NAMED); AtomRefs may be empty (D1) but each present ref
// must be UUID-shaped (fail-loud, never persist a malformed ref).
func NewConceptNode(in NewConceptNodeInput) (*ConceptNode, error) {
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	learner := strings.TrimSpace(in.LearnerGCID)
	if learner == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: a concept must be named (title required)", ErrInvalid)
	}
	prov := in.Provenance
	if prov == "" {
		prov = ProvenanceLearnerAuthored
	}
	if !prov.Valid() {
		return nil, fmt.Errorf("%w: unknown provenance %q", ErrInvalid, prov)
	}
	if !in.Intent.Valid() {
		return nil, fmt.Errorf("%w: unknown intent %q", ErrInvalid, in.Intent)
	}
	refs, err := normaliseAtomRefs(in.AtomRefs)
	if err != nil {
		return nil, err
	}
	now := resolveNow(in.Now)
	return &ConceptNode{
		ConceptID:   domain.NewUUIDv7(),
		TenantID:    tenant,
		LearnerGCID: learner,
		Title:       title,
		ConceptKey:  normalizeConceptKey(title),
		AtomRefs:    refs,
		Provenance:  prov,
		Intent:      in.Intent,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// normalizeConceptKey slugs a title into the stable concept key: lower-case,
// runs of non-alphanumerics collapse to a single '-', trimmed. Kept stdlib-only
// for hexagonal purity — it MUST stay byte-identical to
// learner_weakness.NormalizeConceptKey (the shared concept_key vocabulary);
// the SQL backfill in migration 0074 mirrors it as
// trim(both '-' from regexp_replace(lower(title),'[^a-z0-9]+','-','g')).
func normalizeConceptKey(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// Rename re-titles a live concept.
func (c *ConceptNode) Rename(title string, now time.Time) error {
	if err := c.ensureLive(); err != nil {
		return err
	}
	t := strings.TrimSpace(title)
	if t == "" {
		return fmt.Errorf("%w: a concept must be named (title required)", ErrInvalid)
	}
	c.Title = t
	c.touch(now)
	return nil
}

// SetSubGoal sets or clears the learner's per-node objective (ADR-247 D1). An
// empty/whitespace text CLEARS the sub-goal and its provenance (the learner
// deletes their objective). A non-empty text stamps prov, defaulting to
// learner_authored when prov is empty. Fails loud on a soft-deleted node
// (mirrors Rename/AttachAtom) or an unknown provenance (never persist a
// malformed provenance).
func (c *ConceptNode) SetSubGoal(text string, prov Provenance, now time.Time) error {
	if err := c.ensureLive(); err != nil {
		return err
	}
	t := strings.TrimSpace(text)
	if t == "" {
		c.SubGoal = ""
		c.SubGoalProvenance = ""
		c.touch(now)
		return nil
	}
	if prov == "" {
		prov = ProvenanceLearnerAuthored
	}
	if !prov.Valid() {
		return fmt.Errorf("%w: unknown sub-goal provenance %q", ErrInvalid, prov)
	}
	c.SubGoal = t
	c.SubGoalProvenance = prov
	c.touch(now)
	return nil
}

// AttachAtom adds an atom UUID reference (idempotent; malformed → ErrInvalid).
func (c *ConceptNode) AttachAtom(atomID string, now time.Time) error {
	if err := c.ensureLive(); err != nil {
		return err
	}
	ref, err := validateAtomRef(atomID)
	if err != nil {
		return err
	}
	for _, existing := range c.AtomRefs {
		if existing == ref {
			return nil // idempotent
		}
	}
	c.AtomRefs = append(c.AtomRefs, ref)
	c.touch(now)
	return nil
}

// DetachAtom removes an atom reference if present (idempotent).
func (c *ConceptNode) DetachAtom(atomID string, now time.Time) error {
	if err := c.ensureLive(); err != nil {
		return err
	}
	ref := strings.TrimSpace(atomID)
	kept := make([]string, 0, len(c.AtomRefs))
	removed := false
	for _, existing := range c.AtomRefs {
		if existing == ref {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	if removed {
		c.AtomRefs = kept
		c.touch(now)
	}
	return nil
}

// SoftDelete tombstones the concept (idempotent; never hard-deletes, #5).
func (c *ConceptNode) SoftDelete(now time.Time) {
	if c.DeletedAt != nil {
		return
	}
	n := resolveNow(now)
	c.DeletedAt = &n
	c.UpdatedAt = n
}

func (c *ConceptNode) ensureLive() error {
	if c.DeletedAt != nil {
		return ErrConceptDeleted
	}
	return nil
}

func (c *ConceptNode) touch(now time.Time) { c.UpdatedAt = resolveNow(now) }

// --- shared helpers (used by concept_node.go + edge.go) ---

// resolveNow returns now.UTC(), falling back to wall-clock when now is zero.
func resolveNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

// normaliseAtomRefs trims + de-duplicates (order-preserved) and validates each
// ref is UUID-shaped. Always returns a non-nil slice (stable wire shape).
func normaliseAtomRefs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		ref, err := validateAtomRef(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out, nil
}

func validateAtomRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if !isUUIDShaped(ref) {
		return "", fmt.Errorf("%w: atom ref %q is not a UUID", ErrInvalid, raw)
	}
	return ref, nil
}

// IsUUIDShaped reports whether s is a canonical hyphenated 8-4-4-4-12 hex UUID.
// Exported so read adapters can guard a CLIENT-SUPPLIED id before it reaches a
// pg uuid comparison: binding a non-UUID into a `col = $N` uuid cast raises
// 22P02, which 500s the read (the 2026-07-22 concept-graph/suggestions incident).
// A canonical UUID is always accepted by Postgres, so a true result never
// mis-routes a valid id.
func IsUUIDShaped(s string) bool { return isUUIDShaped(s) }

// isUUIDShaped reports whether s is a hyphenated 8-4-4-4-12 hex UUID.
func isUUIDShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(r) {
				return false
			}
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
