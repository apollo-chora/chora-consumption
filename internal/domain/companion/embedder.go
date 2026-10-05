// embedder.go — OPTIONAL text-embedding port for the F4 per-Companion
// pgvector RAG memory feature.
//
// The chat handler embeds (a) the incoming learner message to RECALL the
// nearest prior memories and (b) the assembled turn (message + reply) to
// RECORD a new memory. Both go through this port. The concrete adapter is a
// direct Vertex AI Embeddings API client (text-embedding-004, 768-d) in the
// clients package — chora-model-gateway exposes only a text-generation
// `Invoke` RPC, so embeddings cannot route through it.
//
// IMPORTANT (hexagonal + feedback_companion_vs_agent): this package is the
// PURE domain layer. It declares the port the handler depends on — it MUST
// NOT import any Vertex AI SDK, build URLs, or carry endpoint config.
//
// Contract for callers (nil-safe — mirrors MemorySummaryResolver):
//   - A nil Embedder means embeddings are not configured → the handler skips
//     BOTH recall and record; chat is unchanged (memory disabled gracefully).
//   - An Embed error (Vertex unreachable / quota / timeout) MUST be treated as
//     non-fatal — memory is supplementary, never load-bearing for a chat turn.
//     Callers log + continue (recall yields nothing / record is skipped).
package companion

import "context"

// EmbedTaskType selects the Vertex embedding task type. text-embedding-004
// tunes the vector for the downstream use: a stored memory is a DOCUMENT, the
// per-turn lookup is a QUERY. Using the matched pair improves recall quality.
type EmbedTaskType string

const (
	// EmbedTaskQuery is used when embedding the learner's incoming message to
	// retrieve nearest memories.
	EmbedTaskQuery EmbedTaskType = "RETRIEVAL_QUERY"
	// EmbedTaskDocument is used when embedding an assembled turn to persist it.
	EmbedTaskDocument EmbedTaskType = "RETRIEVAL_DOCUMENT"
)

// EmbedInput is one text to embed.
type EmbedInput struct {
	// Text is the content to embed (REQUIRED).
	Text string
	// TaskType selects RETRIEVAL_QUERY vs RETRIEVAL_DOCUMENT. Empty defaults to
	// RETRIEVAL_DOCUMENT (the safe, symmetric default).
	TaskType EmbedTaskType
	// TenantID is carried for future per-tenant cost attribution; it is not
	// required by the embedding call itself.
	TenantID string
}

// Embedder produces a dense vector embedding for a text. The returned slice is
// the model's native dimensionality (768 for text-embedding-004) and is bound
// directly into the pgvector(768) column by the CompanionMemory adapter.
//
// A nil Embedder disables F4 memory (see package doc).
type Embedder interface {
	Embed(ctx context.Context, in EmbedInput) ([]float32, error)
}
