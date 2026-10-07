// ports.go — driven ports (interfaces) + DTOs for the Growth-Edge aggregate.
// Adapters live in internal/adapter/* and depend on this package; the domain
// NEVER depends on them (hexagonal dependency direction).
package learner_weakness

import (
	"context"
	"time"
)

// UpsertInput is one piece of weakness evidence to fold into a learner's Growth
// Edges. It is consumed by New (mint a fresh edge) and Merge (fold into a
// dedup-matched existing edge); the Repository.Upsert orchestrates which.
type UpsertInput struct {
	TenantID        string
	LearnerGCID     string
	ConceptKey      string // optional; derived from ConceptLabel when blank
	ConceptLabel    string
	Embedding       []float32 // 1024-d text-embedding-004, precomputed by the caller
	TopicID         string    // optional nearest-TopicNode resolution
	TargetConceptID string    // ADR-238: resolved on-map ConceptNode id (goal-scoped nearest-match at ingest). "" = UNMATCHED / unresolved.
	Category        string    // optional coarse bucket
	Tags            []string
	Strength        float64 // 0..1 shakiness
	Source          Source
	Descriptor      Descriptor
	Now             time.Time
}

// UpsertResult reports the outcome of an Upsert.
type UpsertResult struct {
	ID     string // the resulting Growth-Edge row id (existing on merge, new on insert)
	Merged bool   // true = folded into an existing edge; false = new sibling row
}

// ListSort enumerates the accepted list orderings (mirrors the OpenAPI enum).
type ListSort string

const (
	SortStrengthDesc      ListSort = "strength_desc"
	SortLastEvidencedDesc ListSort = "last_evidenced_desc"
	SortFirstSeenDesc     ListSort = "first_seen_desc"
)

// ListQuery is the filter/sort/paginate request for a learner's Growth Edges.
type ListQuery struct {
	TenantID     string
	LearnerGCID  string
	Category     string   // "" = no category filter
	Tags         []string // OR-within; "" entries ignored
	TopicID      string   // "" = no topic filter
	MinStrength  float64  // 0 = no floor
	IncludeGrown bool     // default false excludes mastered edges
	Sort         ListSort // default SortStrengthDesc
	PageSize     int      // <=0 defaults; capped
	PageToken    string   // opaque cursor from a prior page ("" = first page)
}

// ListResult is a page of Growth Edges.
type ListResult struct {
	Items         []LearnerWeakness
	NextPageToken string // "" on the last page
}

// Repository is the persistence port for the Growth-Edge aggregate. Every method
// is per-GCID + tenant-scoped (RLS applied in the adapter). Cross-DB queries are
// forbidden — this only ever touches chora_consumption.
type Repository interface {
	// Upsert applies embedding dedup: if an active edge for (tenant, gcid) is
	// within DedupCosineDistanceMax cosine distance of in.Embedding it is folded
	// via Merge (Merged=true); otherwise a new sibling edge is inserted.
	Upsert(ctx context.Context, in UpsertInput) (UpsertResult, error)
	// List returns a filtered, sorted, paginated page of the learner's edges.
	List(ctx context.Context, q ListQuery) (ListResult, error)
	// ListAll returns EVERY live edge matching the query's filters
	// (category/topic/min_strength/tags/include_grown), sorted by q.Sort, with
	// NO pagination — it ignores q.PageSize + q.PageToken. The read-time rollup
	// (RollupPage) collapses near-duplicate edges across the WHOLE set before the
	// learner-facing list endpoint paginates, so it must see every row; raw-page
	// pagination would split a rollup bucket across pages and duplicate its
	// representative. Per-learner edge counts are small (dozens); the adapter
	// applies a high safety bound. Tenant scoping rides the ctx (RLS).
	ListAll(ctx context.Context, q ListQuery) ([]LearnerWeakness, error)
	// Get returns one edge by id, scoped to the learner; nil + nil-error when absent.
	Get(ctx context.Context, learnerGCID, id string) (*LearnerWeakness, error)
	// SoftDelete dismisses an edge (idempotent).
	SoftDelete(ctx context.Context, learnerGCID, id string, now time.Time) error
	// RecoverByConceptKey applies the W3-derived recovery path: load the live
	// edge whose concept_key matches (normalised), lower its strength via
	// LearnerWeakness.Recover (only-lower semantics; may flip status to grown),
	// and persist. Returns the edges that crossed active→grown on this call —
	// 0 or 1 (empty when no matching edge exists, the strength would not lower
	// it, or it recovered-but-did-not-grow / was already grown) so the caller
	// can publish weakness.grown.v1 (ADR-196). Tenant scoping rides the ctx (RLS).
	RecoverByConceptKey(ctx context.Context, learnerGCID, conceptKey string, strength float64, now time.Time) ([]GrownEdge, error)
	// RecoverByDrillAtomID applies the W4 drill-completion recovery path: for
	// every live edge whose cached_drill_atom_ids contains atomID, lower its
	// strength via LearnerWeakness.Recover (only-lower; may flip to grown) and
	// persist. This is the path that closes the upload→practice→grow loop for
	// EXPLICIT edges, whose analyser-minted concept_key never equals a drilled
	// atom's broad primary topic (so RecoverByConceptKey can never move them).
	// Returns the edges that crossed active→grown on this call (0..N; empty when
	// none matched, the strength would not lower them, or they recovered-but-did-
	// not-grow / were already grown) so the caller can publish weakness.grown.v1
	// (ADR-196). Tenant scoping rides the ctx.
	RecoverByDrillAtomID(ctx context.Context, learnerGCID, atomID string, strength float64, now time.Time) ([]GrownEdge, error)
	// SetCachedDrillAtoms replaces the edge's W4 drill-atom cache (semantic
	// matches resolved via creation's ContentRetrieval.SearchEmbeddings).
	// Idempotent; no-op on a dismissed edge. Tenant scoping rides the ctx.
	SetCachedDrillAtoms(ctx context.Context, learnerGCID, id string, atomIDs []string, now time.Time) error
}

// Embedder embeds a concept label into the 1024-d text-embedding-004 space used
// for dedup, nearest-topic resolution, and semantic drill-atom retrieval. The
// adapter wraps the same direct-Vertex client as the Companion memory path (the
// gateway has no embeddings RPC).
type Embedder interface {
	Embed(ctx context.Context, text, tenantID string) ([]float32, error)
}
