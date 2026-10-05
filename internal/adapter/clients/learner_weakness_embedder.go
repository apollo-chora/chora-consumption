// learner_weakness_embedder.go: adapts the F4 text-embedding client
// (companion.Embedder) to the Growth-Edge learner_weakness.Embedder port.
//
// The Growth-Edge layer embeds concept LABELS for dedup + nearest-topic resolve
// + semantic drill-atom retrieval. Stored concepts use the RETRIEVAL_DOCUMENT
// task type (mirrors how atoms + Companion memories are stored), so a later
// RETRIEVAL_QUERY embedding of a learner question retrieves them by cosine.
package clients

import (
	"context"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// LearnerWeaknessEmbedder wraps any companion.Embedder (the
// GatewayEmbeddingClient in production) to satisfy the
// learner_weakness.Embedder port without coupling the two aggregates' input
// types.
type LearnerWeaknessEmbedder struct {
	inner companion.Embedder
}

// NewLearnerWeaknessEmbedder wraps an existing companion.Embedder.
func NewLearnerWeaknessEmbedder(inner companion.Embedder) *LearnerWeaknessEmbedder {
	return &LearnerWeaknessEmbedder{inner: inner}
}

var _ learner_weakness.Embedder = (*LearnerWeaknessEmbedder)(nil)

// Embed produces the 768-d RETRIEVAL_DOCUMENT embedding for a concept label.
func (e *LearnerWeaknessEmbedder) Embed(ctx context.Context, text, tenantID string) ([]float32, error) {
	return e.inner.Embed(ctx, companion.EmbedInput{
		Text:     text,
		TaskType: companion.EmbedTaskDocument,
		TenantID: tenantID,
	})
}
