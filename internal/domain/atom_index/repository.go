package atom_index

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical not-found sentinel for Get. BOTH adapters
// (pg + in-memory) map their storage-level no-rows condition to this error so
// callers can distinguish "unprojected atom" (expected — skip/404) from a real
// storage failure (unexpected — fail loud). Per the no-debt/fail-loud
// directive 2026-06-10.
var ErrNotFound = errors.New("atom_index: not found")

// Repo is the atom_index projection port. chora-consumption owns this skinny
// local projection of chora_creation atoms (fed by chora.creation.atom.created.v1)
// — cross-DB queries to chora_creation are FORBIDDEN (ddd-enforcement).
//
// All methods take a context so the pg-backed adapter can establish the
// per-request RLS tenant session (rls.ApplySession reads tenant_id from ctx).
// The in-memory adapter ignores ctx. Callers MUST set the tenant on ctx
// (tracing.WithTenantID) before invoking — for Pub/Sub subscribers the tenant
// comes from the event envelope/payload, not request middleware.
type Repo interface {
	// Save upserts the projection by AtomID.
	Save(ctx context.Context, a *AtomIndex) error
	// Get returns the projection by AtomID, or ErrNotFound. Excludes
	// soft-deleted entries.
	Get(ctx context.Context, atomID string) (*AtomIndex, error)
	// ListByCourse returns all live atoms for a (tenant, course) pair.
	// Order is unspecified — callers sort for stable ordering.
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*AtomIndex, error)

	// MarkPublished applies the atom.published flip for one atom: it sets ONLY
	// status + the MCQ answer key (atomType / correctOptionID / answerCount) +
	// the cognitive level (WS-C3 — blank never clobbers a known level),
	// preserving the projected title / topic_tags / course_id. The answer key
	// comes from the PUBLISHED event because the atom.created row's key is
	// empty/unreliable. status is non-downgrading (never reverts an already-
	// published row). Tenant-scoped (RLS). CHO-1968.
	MarkPublished(ctx context.Context, atomID string, st Status, atomType, correctOptionID string, answerCount int, cognitiveLevel string) error

	// SearchForLearner returns up to `limit` live (non-soft-deleted) atoms for
	// a tenant, ordered most-recently-published first, optionally biased toward
	// a topic. This is the read surface behind the Consumption gRPC
	// RecommendAtomsForLearner RPC the AI Kernel Content Recommender crew calls
	// for REAL RAG retrieval (replacing the recommender's atom-stub source).
	//
	// topicHint semantics:
	//   - "" (empty)        → the most-recently-published atoms for the tenant.
	//   - non-empty         → atoms whose topic_tags overlap the hint
	//                         (case-insensitive substring match on any tag) are
	//                         returned first; if FEWER than `limit` match, the
	//                         result is topped-up with the next most-recent
	//                         non-matching atoms so the caller always gets the
	//                         best `limit` candidates available.
	//
	// limit <= 0 is treated as the adapter default; the adapter clamps an
	// over-large limit. Cross-DB queries FORBIDDEN — this reads only the local
	// atom_index projection (ddd-enforcement). Tenant-scoped (RLS).
	SearchForLearner(ctx context.Context, tenantID, topicHint string, limit int) ([]*AtomIndex, error)
}
