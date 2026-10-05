// maps_cooling.go: the cooling counts on the Atlas read (C4, UX Track U §4).
//
// The home's defend_hex card asks one question across every map the learner
// holds: which provinces are going cold. That is a per-goal count, and the Atlas
// card is already the per-goal read, so the count rides there rather than on a
// new route. Two reasons it must:
//
//   - The Atlas card is where a "needs defending" badge belongs anyway, so this
//     is one read model with two consumers rather than two that can disagree.
//   - The Istio authz allowlist for chora-consumption is enumerated PER PATH
//     (chora-infra/k8s/services/chora-consumption/authz-allow-gateway.yaml). A
//     new top-level /v1/me/... route is admitted nowhere until that manifest
//     changes, so it would return 403 in prod while passing every test here.
//
// # What cooling is, per ADR-227, and what it is not
//
// D8 is titled "Decay lives in the climb and gates advancement, NEVER holdings",
// and D15 places the cooling cue on the FRONTIER (claimed, climbing,
// refresh-gated); a province (won) is permanent and "time alone demotes
// nothing". So a cooling hex here is an UNWON node with at least one cleared
// rung whose concept-key retention has decayed below campaign.RefreshThreshold.
//
// This deliberately matches campaignNodeCooling, the cue the MAP read paints,
// including its conservative default that a node with no retention row reads as
// cooling. The home and the map must never disagree about the same hex.
//
// # Counting is not the same as looking
//
// An unwired campaign lane, an unwired retention repo, or a failed read of
// either sets coolingPartial on the envelope instead of serving zeroes. A zero
// would tell the learner nothing is cooling on the strength of a read that never
// happened, and the home would rank the defend card off the page entirely.
package http

import (
	"context"
	"log"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// coolingCalc holds the two learner-wide reads the count needs, taken ONCE per
// request. The naive shape is a retention Get per climbing node, which is a
// per-node round trip across every map the learner owns.
type coolingCalc struct {
	progressByConcept map[string]*campaign.NodeProgress
	retentionByKey    map[string]*topic_retention.TopicScore
	now               time.Time
}

// newCoolingCalc loads the ladder and the retention curve for one Atlas read.
//
// Returns partial=true when either read is unavailable or failed. Partial means
// no card carries a count, never a half-filled Atlas: a count present on some
// cards and silently zero on others is worse than an explicit "not read".
func (s *ExtServer) newCoolingCalc(ctx context.Context, tenantID, gcid string) (*coolingCalc, bool) {
	if s.CampaignProgress == nil {
		return nil, true // campaign lane not wired: no source for the count
	}
	if s.CampaignDose == nil || s.CampaignDose.Retention == nil {
		// A missing ROW is a fact (never reviewed, so cooling). A missing REPO
		// is a read that cannot happen, which is a different thing and must not
		// be dressed as "every hex is cooling".
		return nil, true
	}
	progresses, err := s.CampaignProgress.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		log.Printf("consumption: atlas cooling counts OMITTED, ladder read failed (tenant=%s gcid=%s): %v", tenantID, gcid, err)
		return nil, true
	}
	scores, err := s.CampaignDose.Retention.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		log.Printf("consumption: atlas cooling counts OMITTED, retention read failed (tenant=%s gcid=%s): %v", tenantID, gcid, err)
		return nil, true
	}

	calc := &coolingCalc{
		progressByConcept: make(map[string]*campaign.NodeProgress, len(progresses)),
		retentionByKey:    make(map[string]*topic_retention.TopicScore, len(scores)),
		now:               time.Now().UTC(),
	}
	for _, p := range progresses {
		if p != nil {
			calc.progressByConcept[p.ConceptID] = p
		}
	}
	for _, sc := range scores {
		if sc != nil {
			calc.retentionByKey[sc.TopicID] = sc
		}
	}
	return calc, false
}

// forSubtree counts the cooling hexes inside one goal's sub-tree and names one.
//
// The named hex is the one with the LOWEST concept id among those cooling. The
// pick has to be deterministic: the home renders it into a sentence, and an
// unordered map walk would change which province the learner is told about on
// every reload of the same data.
func (c *coolingCalc) forSubtree(set map[string]bool, byID map[string]*conceptgraph.ConceptNode) (count int, label string) {
	if c == nil {
		return 0, ""
	}
	pickID := ""
	for conceptID := range set {
		p := c.progressByConcept[conceptID]
		if p == nil || p.Won() || p.RungsCleared == 0 {
			// No ladder row is an unstarted node with nothing to refresh; a won
			// node is a province, and ADR-227 D8 keeps decay off holdings.
			continue
		}
		if !c.coolingNode(byID[conceptID]) {
			continue
		}
		count++
		if pickID == "" || conceptID < pickID {
			pickID = conceptID
		}
	}
	if pickID != "" {
		if node := byID[pickID]; node != nil {
			label = node.Title
		}
	}
	return count, label
}

// coolingNode mirrors campaignNodeCooling exactly, reading the batched retention
// map instead of a per-node Get. A node with no concept key, or with no
// retention row, reads as cooling: that is the conservative refresher default
// campaign.DecideServe applies, and the map read paints the same cue.
func (c *coolingCalc) coolingNode(node *conceptgraph.ConceptNode) bool {
	if node == nil || node.ConceptKey == "" {
		return true
	}
	score := c.retentionByKey[node.ConceptKey]
	return score == nil || score.RetentionAt(c.now) < campaign.RefreshThreshold
}

// defensible reports whether a goal can carry a defend card at all.
//
// A retired goal is the archive (C4 ruling 1: retired IS archived), and nobody
// defends a map they have put away. A rootless goal has no sub-tree to defend.
func defensibleGoal(g goal.Goal) bool {
	return g.Status != goal.StatusRetired && g.DeletedAt == nil &&
		g.RootConceptID != nil && *g.RootConceptID != ""
}
