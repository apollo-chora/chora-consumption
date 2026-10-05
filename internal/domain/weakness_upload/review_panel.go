// review_panel.go — the bounded HITL review panel (ADR-205 D4, CHO-1973).
//
// The graduated (LangGraph) weakness-analyser crew PAUSES at the bounded HITL
// interrupt after diagnosing candidate edges but BEFORE persisting anything, and
// emits chora.consumption.weakness.review_pending.v1 carrying the full panel
// across the domain boundary (cross-DB forbidden). chora-consumption parks the
// upload job in StatusAwaitingReview and stores this panel verbatim in the
// review_payload JSONB column so the A+ FE can poll it and drive the bounded
// accept/reject/merge decision.
//
// The shape is byte-identical to the GET .../uploads/{id} poll DTO `review`
// block (snake_case JSON keys) — the stored JSONB IS what the FE polls — so the
// json tags here serve double duty (JSONB persistence + the wire DTO, which
// embeds *ReviewPanel directly). The orchestrator owns proposed_edge_id; this
// service treats it OPAQUELY (store + echo, never mint).
package weakness_upload

// ReviewPanel is the full bounded HITL review panel surfaced to the learner.
// Slices are non-nil (marshal as [] not null) so the FE always sees an array.
type ReviewPanel struct {
	// Companion fronting the review (nil if the orchestrator left it unresolved).
	Companion *ReviewCompanion `json:"companion,omitempty"`
	// The analyser's proposed edges for bounded accept/reject/merge (may be empty).
	ProposedEdges []ProposedEdge `json:"proposed_edges"`
	// "Add a struggle the analyser missed" bounded picker source (may be empty).
	CandidateStruggles []CandidateStruggle `json:"candidate_struggles"`
	// Selectable Companion-fronted outputs + their mana prices (ADR-205 D5).
	AvailableOutputs []AvailableOutput `json:"available_outputs"`
}

// ReviewCompanion is the Companion persona fronting the review.
type ReviewCompanion struct {
	CompanionID string `json:"companion_id"`
	Name        string `json:"name"`
	Species     string `json:"species,omitempty"`
}

// ProposedEdge is one analyser-proposed Growth Edge for the learner to
// accept / reject / merge. proposed_edge_id is the orchestrator-owned OPAQUE
// handle echoed back in the resume decision (we never mint or interpret it).
type ProposedEdge struct {
	ProposedEdgeID      string   `json:"proposed_edge_id"`
	ConceptLabel        string   `json:"concept_label"`
	Summary             string   `json:"summary"`
	SuggestedAngles     []string `json:"suggested_angles,omitempty"`
	Strength            float32  `json:"strength"`
	SuggestedDifficulty string   `json:"suggested_difficulty"`
}

// CandidateStruggle is one concept the learner MAY add as a struggle the
// analyser missed (bounded picker — never free-form, ADR-205 D4).
type CandidateStruggle struct {
	ConceptKey   string `json:"concept_key"`
	ConceptLabel string `json:"concept_label"`
}

// AvailableOutput is one selectable Companion-fronted output + its mana price
// (ADR-205 D5 / ADR-178). mana_price is 0 for the free outputs.
type AvailableOutput struct {
	Kind            string `json:"kind"`
	ManaPrice       int64  `json:"mana_price"`
	DefaultSelected bool   `json:"default_selected,omitempty"`
}
