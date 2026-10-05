// growth_edge_context.go — W7 (Epic-1b): build the `learner_weakness` session
// context for the Companion chat turn. Mirrors the F4 memory recall: fetch the
// learner's top Growth Edges (shakiest first), distil them into a compact JSON
// envelope, and let the agent's InstructionProvider weave them into the
// per-turn system prompt. Soft-fail throughout — the edges are supplementary
// context, never load-bearing for the turn.
package http

import (
	"context"
	"encoding/json"
	"log"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// chatGrowthEdgeTopN bounds how many edges ride one chat turn (prompt budget).
const chatGrowthEdgeTopN = 5

// chatGrowthEdge is one distilled edge in the learner_weakness envelope. The
// wire contract lives here (producer); the agent-side reader keeps its own
// local parse target (mirrors the companion_memory envelope split).
type chatGrowthEdge struct {
	Label           string   `json:"label"`
	ConceptKey      string   `json:"concept_key"`
	Strength        float64  `json:"strength"`
	Summary         string   `json:"summary,omitempty"`
	SuggestedAngles []string `json:"suggested_angles,omitempty"`
}

type chatGrowthEdgeEnvelope struct {
	GrowthEdges []chatGrowthEdge `json:"growth_edges"`
}

// chatLearnerWeaknessJSON returns the learner_weakness envelope JSON for the
// turn, or "" when the repo is unwired, the read fails (logged, non-fatal), or
// the learner has no active edges.
func (s *Server) chatLearnerWeaknessJSON(ctx context.Context, tenantID, gcid string) string {
	if s.LearnerWeakness == nil {
		return ""
	}
	res, err := s.LearnerWeakness.List(ctx, lw.ListQuery{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Sort:        lw.SortStrengthDesc,
		PageSize:    chatGrowthEdgeTopN,
	})
	if err != nil {
		log.Printf("consumption: companion chat growth-edge read failed (non-fatal): %v", err)
		return ""
	}
	if len(res.Items) == 0 {
		return ""
	}
	env := chatGrowthEdgeEnvelope{GrowthEdges: make([]chatGrowthEdge, 0, len(res.Items))}
	for _, e := range res.Items {
		env.GrowthEdges = append(env.GrowthEdges, chatGrowthEdge{
			Label:           e.ConceptLabel,
			ConceptKey:      e.ConceptKey,
			Strength:        e.Strength,
			Summary:         e.Descriptor.Summary,
			SuggestedAngles: e.Descriptor.SuggestedAngles,
		})
	}
	b, err := json.Marshal(env)
	if err != nil {
		return "" // unreachable for this shape; defensive
	}
	return string(b)
}
