// memory.go — domain port for the F4 per-Companion pgvector RAG memory feature.
//
// Each conversational turn is embedded (via the Embedder port — see
// embedder.go) and persisted as a memory row; the next turn embeds the
// incoming message and RECALLs the nearest prior memories by cosine distance.
// Scope is per-Companion (`scope_key = 'companion:{companion_id}'`) mirroring the
// deployed agent's app_name prefix — NOT a per-user-shared store (each learner
// owns N specialised Companions 1:N per ADR-147 / multi-companion design).
//
// IMPORTANT (hexagonal + feedback_companion_vs_agent): this package is the PURE
// domain layer. It declares the CompanionMemory port the chat handler depends
// on — it MUST NOT import any Vertex AI / pgvector / SDK code, build URLs, or
// carry endpoint config. The concrete adapter (a pgvector-backed repo in the
// pg adapter package) lands separately; the precomputed 768-d embedding is
// supplied by the caller (the Embedder port produced it).
package companion

import (
	"context"
	"time"
)

// MemoryTypeResearch is the memory_type of a note written by a Seeker skill's
// web_research sink — the ONLY memory_type that is web-grounded, and therefore
// the only one that can owe the learner an attribution.
//
// ⚠ companion_mind declares this same literal for its read model. The two bounded
// packages deliberately do not import each other, so the constant is stated twice
// and the equality is GUARDED by a test in the pg adapter (which imports both).
// They name the same persisted value; a silent divergence would make the recall
// path stop recognising the very notes the view path renders.
const MemoryTypeResearch = "research"

// MemoryRow is one recalled per-Companion memory.
type MemoryRow struct {
	ID          string
	MemoryType  string
	ContentText string
	CreatedAt   time.Time
	Distance    float64 // cosine distance (lower = nearer); 0 when not scored
	// Provenance — where a GROUNDED note came from, read back from source_metadata
	// (mig 0094). nil for every non-grounded memory_type, AND for a research note
	// written before 0094 (genuinely unrecorded — see IsGrounded).
	//
	// CHO-2192: without this, a note the Companion recalls to answer with could be
	// restated as a bare assertion — the sources were on disk the whole time and
	// the recall query simply never asked for them.
	Provenance *MemoryProvenance
}

// IsGrounded reports whether this memory came off the live web, and therefore
// owes the learner a source.
//
// It keys on memory_type, NOT on whether Provenance happens to be present — and
// that distinction is the whole of the pre-0094 case. A research note written
// before mig 0094 IS grounded; it simply has no recoverable sources. Keying on
// Provenance != nil would silently reclassify it as ungrounded and show the
// learner nothing — and our silence would be indistinguishable from "no web
// search happened", which is the exact untraceable-assertion bug this exists to
// prevent. Grounded-but-unrecorded is a thing we must be able to SAY.
func (m MemoryRow) IsGrounded() bool { return m.MemoryType == MemoryTypeResearch }

// MemoryCitation is one DURABLE grounded source behind a memory note (ADR-231 D4).
//
// ⚠ It deliberately has NO url. The gateway's citation uri is a Google
// grounding-api-redirect link that EXPIRES (~30 days), so a note that persisted
// it would rot to dead links — worse than persisting nothing, because a dead
// link still LOOKS like a working source. domain+title+snippet never expire, so
// they are the record. The uri stays a live-response convenience only.
type MemoryCitation struct {
	Domain  string `json:"domain,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// MemoryProvenance is where a GROUNDED memory note came from: the web-search
// queries the model actually issued, plus the durable citations. It is persisted
// as the `source_metadata` JSONB column (mig 0094) — deliberately NOT folded into
// ContentText, which is what gets vector-embedded (provenance in the prose would
// poison recall and would read to the learner as knowledge, which it is not).
//
// nil for every non-grounded note (chat_turn / recap / ceremony) ⇒ the column
// stays NULL rather than carrying an empty husk.
type MemoryProvenance struct {
	WebSearchQueries []string         `json:"web_search_queries,omitempty"`
	Citations        []MemoryCitation `json:"citations,omitempty"`
}

// IsEmpty reports whether there is nothing to attribute — nil-safe, and TOTAL
// over the contract: a nil Provenance and an empty husk are the same fact.
//
// The wire rule everywhere downstream is "attribution is emitted IF AND ONLY IF
// there is provenance". One shape for "nothing to attribute" means the FE has
// exactly one branch for it and can never render an attribution block with
// nothing in it — which would imply a web search that never happened.
func (p *MemoryProvenance) IsEmpty() bool {
	return p == nil || (len(p.WebSearchQueries) == 0 && len(p.Citations) == 0)
}

// RecordMemoryInput is one memory to persist (embedding precomputed by the Embedder port).
type RecordMemoryInput struct {
	TenantID        string
	OwnerGCID       string
	CompanionID     string
	MemoryType      string // "" defaults to "chat_turn"
	ContentText     string
	Embedding       []float32 // 768-d
	ModelID         string
	SourceTurnID    string // optional
	SourceSessionID string // optional
	Now             time.Time
	TTL             time.Duration // 0 = no expiry
	// Provenance — grounded notes only (memory_type="research"): where the note
	// came from (ADR-231 D4). nil ⇒ source_metadata NULL.
	Provenance *MemoryProvenance
}

// CompanionMemory is the nil-safe per-Companion RAG memory port (pgvector-backed).
// A nil CompanionMemory means memory is disabled — callers skip Recall/Record and
// chat is unchanged. Recall/Record errors are non-fatal to the chat turn (soft-fail).
type CompanionMemory interface {
	Recall(ctx context.Context, companionID string, queryEmbedding []float32, topK int) ([]MemoryRow, error)
	Record(ctx context.Context, in RecordMemoryInput) error
}

// MemoryRetirer purges a companion's retained memories at retire (CHO-2096):
// soft-deletes every live companion_memory_recall row for (tenant, companion) so
// a retired companion remembers nothing about the learner's goals — the
// irreversibility the retire confirm promises. Soft-delete, never hard-delete
// (ddd-enforcement #4; true erasure stays with the closure saga's
// crypto-shred). Kept narrow (separate from CompanionMemory) so chat's
// recall/record stubs are untouched. Returns the purged row count.
type MemoryRetirer interface {
	SoftDeleteByCompanion(ctx context.Context, tenantID, companionID string) (int64, error)
}

// MemoryScopeKey is the canonical per-Companion scope key (mirrors the agent's app_name prefix).
func MemoryScopeKey(companionID string) string { return "companion:" + companionID }
