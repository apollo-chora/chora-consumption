package goal

import "context"

// Repository persists the learner-owned Goal aggregate. Every method is
// tenant-scoped via the RLS session GUC (the pg adapter calls rls.ApplySession
// first, mirroring learner_profile / learner_weakness); per-learner scoping is an
// explicit learner_gcid predicate. Cross-DB queries are forbidden
// (ddd-enforcement #1) — the Goal lives wholly in chora_consumption.
type Repository interface {
	// Create persists a new Goal.
	Create(ctx context.Context, g *Goal) error

	// GetByID returns one live Goal (deleted_at IS NULL) by id for the learner, or
	// (nil, nil) when none matches (the adapter does NOT leak across learners; the
	// caller still re-checks tenant/learner before returning it).
	GetByID(ctx context.Context, tenantID, learnerGCID, goalID string) (*Goal, error)

	// ListByLearner returns the learner's live Goals (deleted_at IS NULL),
	// newest-first.
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*Goal, error)

	// Update persists mutable fields of an existing Goal (status, concept_set,
	// north_star_note, chora_target_ref, the attached_companion_id bond,
	// updated_at, deleted_at). Scoped to (id, tenant, learner).
	Update(ctx context.Context, g *Goal) error
}
