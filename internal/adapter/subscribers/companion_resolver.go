// companion_resolver.go — repo-backed CompanionResolver.
//
// CHO-2012 (ADR-218 D7): EXP attribution resolves through GOAL ATTACHMENT.
// The active companion for (tenant, gcid) is:
//
//  1. goal-scoped (ResolveForGoal, when the upstream event carries a goal
//     context): that goal's Goal.AttachedCompanionID;
//  2. otherwise the learner's EXPLICIT ACTIVE COMPANION — the companion on
//     the most recently touched attached goal (summon = Goal.AttachCompanion,
//     ADR-212 D5; updated_at is the freshest attachment signal the schema
//     carries — a `companion_attached_at` column is a candidate refinement
//     owned by the KG track);
//  3. terminal fallback: the owner's FIRST non-deleted roster instance
//     (the pre-P0 rule) so EXP is NEVER dropped for learners who have not
//     summoned onto a map yet.
//
// F-I1.4 (CHO-2088, ADR-228 incubation): a pre-hatch Stage-0 egg accrues XP
// ONLY through an EXACT goal attachment (rule 1). It is EXCLUDED from BOTH
// fallbacks (rules 2 + 3) — an unbound egg accrues nothing, and an egg bound
// to goal H never soaks an unrelated goal's event via the active-companion
// fallback. The resolver reads the ROSTER (which carries growth stage) so it
// can tell eggs from hatched Companions; a missing growth projection is
// treated as hatched (fail-open — EXP must flow, we only ever exclude a
// CONFIRMED egg).
//
// The theme-primary vs global-trickle split stays editor-tunable and is NOT
// implemented here (GQ-26): 100% of the award goes to the resolved companion,
// exactly as before — attribution changes, the amount does not.
//
// Per ddd-enforcement HARD RULE: both reads stay WITHIN chora_consumption
// (companion_instances + goals) — no cross-DB read. Errors from either read
// PROPAGATE (Pub/Sub NACK → redelivery) — never a silent EXP drop or a
// silent mis-attribution.
package subscribers

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// preHatchStage is Stage 0 (Egg) on the ADR-149 growth axis — a Companion
// that has not yet hatched.
const preHatchStage = 0

// CompanionLister is the interface-segregated read slice of
// companion.InstanceRepository the resolver needs. It reads the ROSTER
// (created_at ASC) so each candidate carries its growth stage — F-I1.4
// needs the stage to keep pre-hatch eggs out of the attribution fallbacks.
// *pg.CompanionInstanceRepo (production) and *inmem.CompanionInstanceRepo
// (dev/tests) both satisfy it via ListRosterByOwner.
type CompanionLister interface {
	ListRosterByOwner(ctx context.Context, tenantID, ownerGCID string) ([]*companion.RosterEntry, error)
}

// GoalAttachmentLister is the read slice of goal.Repository the resolver
// needs (live goals, newest-first). *pg.GoalRepo satisfies it.
type GoalAttachmentLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*goal.Goal, error)
}

// RepoCompanionResolver resolves EXP attribution per ADR-218 D7 + ADR-228.
type RepoCompanionResolver struct {
	repo  CompanionLister
	goals GoalAttachmentLister
}

// NewRepoCompanionResolver constructs the resolver. A nil goals lister keeps
// the legacy roster-only semantics (defensive — production wires the goal
// repo; EXP must flow either way).
func NewRepoCompanionResolver(repo CompanionLister, goals GoalAttachmentLister) *RepoCompanionResolver {
	return &RepoCompanionResolver{repo: repo, goals: goals}
}

// ResolveActiveCompanion returns the learner's explicit active companion:
// the (hatched) companion bonded to the most recently touched attached goal,
// falling back to the first non-deleted HATCHED roster instance;
// ErrNoCompanion when the owner has no eligible companion. Pre-hatch eggs are
// excluded from both fallbacks (F-I1.4). Read errors propagate verbatim.
func (r *RepoCompanionResolver) ResolveActiveCompanion(ctx context.Context, tenantID, ownerGCID string) (string, error) {
	// One roster read backs both fallbacks: it carries growth stage so eggs
	// can be filtered out of the active-companion + terminal-fallback paths.
	roster, err := r.repo.ListRosterByOwner(ctx, tenantID, ownerGCID)
	if err != nil {
		return "", err
	}
	hatched := make(map[string]bool, len(roster))
	oldestHatched := ""
	for _, e := range roster {
		if e == nil || e.Instance == nil || isPreHatchEgg(e) {
			continue
		}
		hatched[e.Instance.CompanionID] = true
		if oldestHatched == "" {
			oldestHatched = e.Instance.CompanionID // roster is created_at ASC
		}
	}

	if id, err := r.mostRecentSummon(ctx, tenantID, ownerGCID, hatched); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}
	if oldestHatched != "" {
		return oldestHatched, nil
	}
	return "", ErrNoCompanion
}

// ResolveForGoal returns the companion attached to the SPECIFIC goal when
// bonded, falling through to ResolveActiveCompanion otherwise. The exact
// attachment is the ONLY path that credits a pre-hatch egg (F-I1.4
// bind-to-warm): an egg bound to THIS goal earns on THIS goal's events.
func (r *RepoCompanionResolver) ResolveForGoal(ctx context.Context, tenantID, ownerGCID, goalID string) (string, error) {
	if r.goals != nil && goalID != "" {
		gs, err := r.goals.ListByLearner(ctx, tenantID, ownerGCID)
		if err != nil {
			return "", err
		}
		for _, g := range gs {
			if g != nil && g.GoalID == goalID && g.AttachedCompanionID != nil && *g.AttachedCompanionID != "" {
				return *g.AttachedCompanionID, nil
			}
		}
	}
	return r.ResolveActiveCompanion(ctx, tenantID, ownerGCID)
}

// mostRecentSummon returns the (hatched) companion on the most recently
// updated attached goal, or "" when the learner has no eligible attachment.
// hatched is the set of the owner's non-egg companion IDs — an attachment to
// a pre-hatch egg is skipped (F-I1.4): the egg only earns via an exact goal
// match in ResolveForGoal, never via this active-companion fallback.
func (r *RepoCompanionResolver) mostRecentSummon(ctx context.Context, tenantID, ownerGCID string, hatched map[string]bool) (string, error) {
	if r.goals == nil {
		return "", nil
	}
	gs, err := r.goals.ListByLearner(ctx, tenantID, ownerGCID)
	if err != nil {
		return "", err
	}
	var (
		bestID string
		bestAt time.Time
	)
	for _, g := range gs {
		if g == nil || g.AttachedCompanionID == nil || *g.AttachedCompanionID == "" {
			continue
		}
		if !hatched[*g.AttachedCompanionID] {
			continue // pre-hatch egg (or not on the roster) — never a fallback target
		}
		if bestID == "" || g.UpdatedAt.After(bestAt) {
			bestID = *g.AttachedCompanionID
			bestAt = g.UpdatedAt
		}
	}
	return bestID, nil
}

// isPreHatchEgg reports whether a roster entry is a pre-hatch Stage-0 egg. A
// MISSING growth projection is treated as NOT an egg (fail-open — EXP must
// flow; only a CONFIRMED Stage-0 projection is excluded).
func isPreHatchEgg(e *companion.RosterEntry) bool {
	return e.Growth != nil && e.Growth.Stage == preHatchStage
}

// Compile-time guarantee that *RepoCompanionResolver satisfies the port.
var _ CompanionResolver = (*RepoCompanionResolver)(nil)
