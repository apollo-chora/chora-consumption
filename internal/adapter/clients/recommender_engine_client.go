// Package clients — Recommender engine client wrapper.
//
// RecommenderEngineClient invokes the deployed Vertex AI Agent Engine
// (us-central1 per ADR-148) that hosts the Content Recommender crew
// (P1 single-agent). Used by /companion/daily-dose to surface AI-picked
// atoms alongside the deterministic SM-2 composition (Phyllis Step 8b).
//
// Per Iter 3 smoke (CHO-1527) the recommender's tool invocation rate is
// ~33% — the Iter 3.5 followup prompt tweak will bring this to ~100%.
// Until then this client treats text-only responses as a non-fatal
// "engine produced narrative but no picks" outcome (NarrativeOnly=true,
// AtomIDs empty). The daily-dose composer surfaces the narrative
// separately so the UI can distinguish "ai picked" vs "ai narrated".
package clients

import (
	"context"
)

// RecommenderEnginePort is the abstraction over the Recommender engine.
type RecommenderEnginePort interface {
	Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error)
}

// RecommendRequest carries the per-learner state. The Recommender crew
// reads tenant_id + user_gcid + mana_tier + learner_persona from
// session.State via the manaplugin + tieredmodelplugin contract.
type RecommendRequest struct {
	TenantID       string
	UserGCID       string
	ManaTier       string
	LearnerPersona string // curious-explorer | cert-focused | social-leader
	UserPrompt     string // e.g. "Recommend 2 atoms for my daily dose."
	// TopicHint, when set, is written into session.State["topic_hint"] so the
	// recommender's InstructionProvider weaves it into the [CONTEXT] block and
	// the model passes it to recommend_atoms_for_learner. Optional — the tool
	// accepts an absent topic (defaults to "general"). Empty ⇒ the key is
	// omitted from state (the composer renders the "<any>" sentinel).
	TopicHint string
	// Candidates, when non-empty, are the REAL atom candidates chora-consumption
	// pre-fetched from atom_index in-process (CHO-1662 session-state-injection
	// RAG). They are marshalled to a JSON STRING and written into
	// session.State["atom_candidates"], which the recommender's
	// recommend_atoms_for_learner tool reads back and returns. This sidesteps the
	// mesh: the sidecar-less crew CANNOT dial back to consumption's STRICT-mTLS
	// :9090, and consumption already owns atom_index + initiates this call.
	// Empty ⇒ the key is omitted (the tool falls back to its stub searcher).
	Candidates []AtomCandidate
}

// AtomCandidate is one pre-fetched atom_index row injected into the recommender
// session state for ranking. Field tags MUST match the recommender tool's Atom
// shape (atom_id/title/snippet) so the JSON round-trips across the language-
// agnostic session-state seam.
type AtomCandidate struct {
	AtomID  string `json:"atom_id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// RecommendResponse carries the engine's outcome. When the model invoked
// the recommend_atoms_for_learner tool, AtomIDs is populated. Otherwise
// NarrativeOnly is true and Narrative holds the model's free-text
// response.
type RecommendResponse struct {
	AtomIDs       []string // tool-call result; nil when tool not invoked
	NarrativeOnly bool     // true when the model returned text instead of a tool call
	Narrative     string   // the model's free-text response (for UI surfacing)
	Model         string
	OutputTokens  int
	FinishReason  string
}
