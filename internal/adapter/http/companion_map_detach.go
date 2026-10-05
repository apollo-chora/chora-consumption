// companion_map_detach.go — map↔roster consistency (CHO-2033 follow-up): when a
// learner retires a roster companion, clear that companion's bond from every map
// (Goal) that designates it (Goal.AttachedCompanionID), so a retired companion
// never lingers on a live map. This is the roster-side counterpart to the
// goals_handler DetachCompanion (the deliberate "dismiss" flow); both mutate
// ONLY the Goal aggregate, within chora_consumption (no cross-DB read).
package http

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// CompanionMapDetacher clears a retired companion's bond from every map (Goal)
// that designates it, returning the number of maps detached. A narrow WRITE
// port kept off the read-only Server.Goals (GoalReader) gate; implemented at
// cmd/server boot over the learner-owned pg goal repo.
type CompanionMapDetacher interface {
	DetachCompanionFromMaps(ctx context.Context, tenantID, learnerGCID, companionID string) (int, error)
}

// goalRepoDetacher satisfies CompanionMapDetacher over the learner-owned Goal
// repo (list-by-learner + Goal.DetachCompanion + Update).
type goalRepoDetacher struct{ goals goal.Repository }

// NewGoalCompanionDetacher wraps the learner-owned Goal repo so the retire path
// can clear a bond without holding the full (mutable) repo on Server.
func NewGoalCompanionDetacher(goals goal.Repository) CompanionMapDetacher {
	return goalRepoDetacher{goals: goals}
}

func (d goalRepoDetacher) DetachCompanionFromMaps(ctx context.Context, tenantID, learnerGCID, companionID string) (int, error) {
	goals, err := d.goals.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	detached := 0
	for _, g := range goals {
		if g == nil || g.AttachedCompanionID == nil || *g.AttachedCompanionID != companionID {
			continue
		}
		if err := g.DetachCompanion(now); err != nil {
			return detached, err
		}
		if err := d.goals.Update(ctx, g); err != nil {
			return detached, err
		}
		detached++
	}
	return detached, nil
}
