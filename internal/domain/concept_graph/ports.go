package conceptgraph

import (
	"context"
	"time"
)

// ConceptNodeRepository persists the learner-owned ConceptNode aggregate.
// Reads/writes are scoped by (tenantID, learnerGCID) as defence-in-depth on top
// of RLS. GetByID returns (nil, nil) when nothing live matches.
type ConceptNodeRepository interface {
	Create(ctx context.Context, c *ConceptNode) error
	GetByID(ctx context.Context, tenantID, learnerGCID, conceptID string) (*ConceptNode, error)
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*ConceptNode, error)
	Update(ctx context.Context, c *ConceptNode) error
}

// EdgeRepository persists the learner-owned Edge aggregate. ListByConcept
// returns edges where the concept is the source OR target — its hex-face
// relations, pre-partition (see PartitionByHexFace).
type EdgeRepository interface {
	Create(ctx context.Context, e *Edge) error
	GetByID(ctx context.Context, tenantID, learnerGCID, edgeID string) (*Edge, error)
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*Edge, error)
	ListByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string) ([]*Edge, error)
	Update(ctx context.Context, e *Edge) error
	// SoftDeleteByConcept soft-deletes every LIVE edge incident to conceptID
	// (source OR target), scoped to (tenant, learner), and returns the count.
	// Idempotent: a re-run over an already-clean concept touches 0 rows. This is
	// the concept.deleted.v1 cascade primitive — per ADR-212 edges are a separate
	// aggregate cleaned via the delete EVENT, never an inline cross-aggregate
	// write from the concept delete handler (CHO-2324).
	SoftDeleteByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string, now time.Time) (int64, error)
}

// SuggestionRepository persists the Companion Suggestion aggregate (ADR-212
// WS-4). Reads/writes are scoped by (tenantID, learnerGCID) as defence-in-depth
// on top of RLS. GetByID / GetBySourceEvent return (nil, nil) when nothing live
// matches. ListPending returns the learner's undecided suggestions (their
// curation inbox), newest first.
type SuggestionRepository interface {
	Create(ctx context.Context, s *Suggestion) error
	// CreateBatch persists all suggestions in ONE transaction (all-or-nothing) —
	// used by the emitted-event subscriber so a partial-batch failure rolls back
	// entirely and Pub/Sub redelivery re-ingests cleanly (no duplicate rows). A
	// learner also sees a whole suggestion batch appear atomically.
	CreateBatch(ctx context.Context, ss []*Suggestion) error
	GetByID(ctx context.Context, tenantID, learnerGCID, suggestionID string) (*Suggestion, error)
	GetBySourceEvent(ctx context.Context, tenantID, learnerGCID, sourceEventID string) (*Suggestion, error)
	ListPending(ctx context.Context, tenantID, learnerGCID string) ([]*Suggestion, error)
	// ListPendingForFocal returns the learner's undecided suggestions SCOPED to a
	// focal concept: rows whose focal_concept_id matches focalConceptID OR are
	// whole-map (NULL). This keeps a stale prior-generate batch (a different
	// focal) from leaking onto other nodes while whole-map suggestions still show
	// everywhere. focalConceptID must be a non-empty UUID (the handler routes an
	// empty focal to ListPending for back-compat). Newest first.
	ListPendingForFocal(ctx context.Context, tenantID, learnerGCID, focalConceptID string) ([]*Suggestion, error)
	Update(ctx context.Context, s *Suggestion) error
}

// SuggestionAccepter atomically materialises an accepted suggestion: within ONE
// chora_consumption transaction it persists the minted ConceptNode OR Edge
// (from Suggestion.Accept) AND flips the Suggestion to accepted. Exactly one of
// (node, edge) is non-nil, per the suggestion's kind. Kept a separate port
// (mirrors ReRootApplier) because it spans two aggregate tables in one tx.
type SuggestionAccepter interface {
	ApplyAccept(ctx context.Context, s *Suggestion, node *ConceptNode, edge *Edge) error
}

// LearningEdgeApplier persists a batch of minted ceremony learning-edges (each a
// ConceptNode + its hierarchy Edge under the goal root, from SelectLearningEdges)
// in ONE chora_consumption transaction (CHO-2038). Mirrors SuggestionAccepter's
// one-tx-many-statements contract — the whole ceremony selection lands
// all-or-nothing so the learner never sees a half-applied map.
type LearningEdgeApplier interface {
	ApplyLearningEdges(ctx context.Context, minted []MintedLearningEdge) error
}
