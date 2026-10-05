// repository.go — the LearningPath repository port (hexagonal).
//
// R3 (durability): the EXT-scope LearningPath was served only by an in-memory
// adapter, so a bootstrapped path was lost on pod restart and invisible to
// other replicas (the /v1/me/learning-paths read 404'd after any restart).
// This port lets the composition root swap in the pg-backed adapter
// (durable + replica-consistent) while tests/dev keep the in-memory one.
//
// All methods take a context: the pg adapter reads the RLS tenant session
// from it (set by the HTTP middleware on reads and by the Pub/Sub push
// dispatcher — tracing.WithTenantID — on the cross-domain writes). The
// in-memory adapter ignores ctx but keeps the signature for parity (mirrors
// atom_index.Repo).
package learning_path

import "context"

// Repo is the persistence port for the course-bound LearningPath aggregate.
type Repo interface {
	// Save upserts a LearningPath by PathID.
	Save(ctx context.Context, p *LearningPath) error
	// Get returns the path by id, or a not-found error.
	Get(ctx context.Context, pathID string) (*LearningPath, error)
	// GetByCourseAndGCID returns the single path for one (tenant, course,
	// learner) trio — the durable idempotency key for enrollment bootstrap.
	GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*LearningPath, error)
	// ListByLearner returns all non-deleted paths for a learner. limit<=0 = unbounded.
	ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*LearningPath, error)
	// ListByLearnerWithAtom returns the learner's paths containing atomID
	// (used by the atom_session.completed advance subscriber).
	ListByLearnerWithAtom(ctx context.Context, tenantID, gcid, atomID string) ([]*LearningPath, error)
	// ListByCourse returns all course-bound paths for a (tenant, course) pair
	// across learners (used for the R2 retroactive atom append).
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*LearningPath, error)

	// GetBySourceCollection returns the single study-list path derived from one
	// (tenant, owner, source collection) trio — the get-or-create probe for the
	// collection→study-list conversion (ADR-233 D2/D4). Backed by the partial
	// unique index idx_learning_paths_source_collection_owner (migration 0093).
	//
	// Re-converting the same collection MUST find this path and additively
	// re-sync it (D4: a source may never retract atoms from a derived path),
	// never mint a second one.
	GetBySourceCollection(ctx context.Context, tenantID, ownerGCID, collectionID string) (*LearningPath, error)

	// GetByStudyListEventID returns the path anchored to one conversion event,
	// or a not-found error. This is the DURABLE delivery-dedupe anchor for the
	// converted_to_study_list subscriber — a redelivered push carries the same
	// study_list_event_id and must be a no-op ack.
	//
	// Deliberately NOT tenant-scoped in its arguments: the anchor is globally
	// unique (partial unique index on study_list_event_id) and the caller has
	// already stamped the RLS tenant onto ctx, so the row is still tenant-fenced
	// by the policy — passing a tenant here would add a redundant predicate
	// without adding safety.
	GetByStudyListEventID(ctx context.Context, studyListEventID string) (*LearningPath, error)
}
