// ports.go - driven port for the generated-artifact aggregate. Adapters live in
// internal/adapter/* and depend on this package (hexagonal dependency direction).
package growth_edge_output

import "context"

// Repository persists and reads the learner's generated artifacts. Every method
// is tenant-scoped (RLS applied in the adapter; the tenant rides the ctx) with
// learner_gcid as an explicit predicate, matching the sibling weakness tables.
// Cross-DB queries are forbidden: chora_consumption only.
type Repository interface {
	// CreateBatch projects one delivery's artifacts. IDEMPOTENT: Pub/Sub is
	// at-least-once, so a redelivery must not duplicate a learner's artifacts.
	// Returns how many rows were newly inserted (0 on a pure redelivery), which
	// is what lets the subscriber log a duplicate rather than silently ack it.
	CreateBatch(ctx context.Context, outs []Output) (int, error)

	// ListForUpload returns the learner's live artifacts for one upload,
	// filtering soft-deleted rows. An empty slice with a nil error means
	// "generated nothing (or not yet)"; the caller distinguishes those from the
	// upload's own status, never by guessing here.
	ListForUpload(ctx context.Context, tenantID, learnerGCID, uploadID string) ([]Output, error)
}
