package companiongoalknowledge

import "context"

// Repository is the persistence port for the goal-knowledge cache.
//
// Invalidation is deliberately expressed as List → Invalidate → Upsert rather
// than a bulk UPDATE. The reason-priority table lives in this package and
// nowhere else; a bulk SQL update would have to re-encode those ranks in SQL
// (or carry a priority column), giving the invariant two homes that can drift.
// The fan-out is a learner's goals for one Companion — single digits — so the
// per-row round trip costs nothing worth buying that risk with.
type Repository interface {
	// FindByGoal returns the live cache row for (tenant, learner, companion, goal),
	// or (nil, nil) when none exists yet. Soft-deleted rows are never returned.
	FindByGoal(ctx context.Context, tenantID, learnerGCID, companionID, goalID string) (*GoalKnowledge, error)

	// ListByGoal returns the live rows for (tenant, learner, goal) across every
	// Companion. Fan-out target for goal-scoped invalidation — a weakness growing
	// on a goal stales that goal's reflection for whichever Companion is bound.
	ListByGoal(ctx context.Context, tenantID, learnerGCID, goalID string) ([]*GoalKnowledge, error)

	// ListByCompanion returns the live rows for (tenant, learner, companion) across
	// every goal. Fan-out target for memory-scoped invalidation — a chat turn or a
	// memory eviction changes what the Companion remembers, which bears on every
	// goal it reflects on.
	ListByCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) ([]*GoalKnowledge, error)

	// Upsert persists the row against the live (tenant, learner, companion, goal)
	// unique key.
	//
	// It writes LIVE rows only, and CANNOT express a soft-delete — the upsert
	// arbiter is the live partial unique index, so a row carrying deleted_at
	// matches nothing in it and collides on the primary key instead. Soft-delete
	// is ClosureRepository's job; an adapter must refuse a deleted aggregate here
	// rather than emit SQL that dies at the far end.
	Upsert(ctx context.Context, k *GoalKnowledge) error
}

// ClosureRepository is the soft-delete port: Companion retirement and account
// closure.
//
// It is deliberately SEPARATE from Repository rather than four more methods on
// it. Two reasons, and both are load-bearing:
//
//   - Capability, not convenience. The closure saga and the retirement handler
//     need to tombstone rows; they have no business minting or overwriting a
//     learner's reflection. Handing them the full Repository would grant exactly
//     that. (The concept_suggestion adapter splits SuggestionRepository from
//     SuggestionAccepter for the same reason — one adapter, two narrow ports.)
//   - Soft-delete is a genuinely different write. It is a scoped UPDATE, not an
//     upsert — see the note on Repository.Upsert.
//
// This table holds LLM-written prose ABOUT the learner (remembered sessions,
// strengths, weaknesses), so it is squarely learner-derived content: closure MUST
// reach it, and PII_Closure_Map.yaml lists it. Both methods return the number of
// rows tombstoned, because a purge that silently touches nothing is
// indistinguishable from a successful one otherwise — and on this table that is
// the difference between honouring a closure and quietly retaining it. Callers
// are expected to record the count. NEVER hard-delete.
type ClosureRepository interface {
	// SoftDeleteByCompanion tombstones every live reflection one Companion holds
	// for the learner — Companion retirement. Idempotent: a second call purges
	// nothing and returns 0.
	SoftDeleteByCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) (int64, error)

	// SoftDeleteByLearner tombstones every live reflection the learner holds,
	// across every goal and every Companion — account closure. Idempotent.
	SoftDeleteByLearner(ctx context.Context, tenantID, learnerGCID string) (int64, error)
}
