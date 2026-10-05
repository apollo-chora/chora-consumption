// chat_session_repo.go — ADR-154 conversational chat hexagonal port.
//
// ChatSessionRepository is the outbound port the chora-consumption HTTP
// handler depends on. Production wires the pg adapter; tests inject a fake.
package companion

import (
	"context"
	"errors"
)

// ErrChatSessionNotFound is returned by Get when no open session exists
// for the (owner_gcid, companion_id) pair. Handlers translate this to
// "create a new engine session" (NOT a 404 — 404 means the COMPANION is
// not owned by the caller).
var ErrChatSessionNotFound = errors.New("companion: chat_session not found")

// ChatSessionRepository is the outbound port. Implementations:
//
//   - pg.CompanionChatSessionRepo — production pgx-backed.
//   - inmem.CompanionChatSessionRepo — test/dev in-memory (M11 paydown
//     may add when http_test.go arrives).
//
// All operations are tenant-scoped by ctx (rls.ApplySession reads the
// tenant_id off the context via tracing.WithTenantID). Callers MUST set
// tenant_id on ctx BEFORE invoking these methods.
type ChatSessionRepository interface {
	// Get returns the single OPEN session for (owner_gcid, companion_id),
	// or ErrChatSessionNotFound when none exists. The unique partial
	// index `(owner_gcid, companion_id) WHERE closed_at IS NULL` guarantees
	// at-most-one match.
	Get(ctx context.Context, ownerGCID, companionID string) (*ChatSession, error)

	// Save upserts the session row — INSERTs on first turn, UPDATEs the
	// counters + last_used_at + closed_at on subsequent persistence.
	// Idempotent against the row's primary key.
	Save(ctx context.Context, sess ChatSession) error

	// MarkClosed soft-deletes the session by setting closed_at = now.
	// Used when the Vertex AI engine reports `session_not_found` and we
	// rotate to a fresh engine_session_id.
	MarkClosed(ctx context.Context, sessionID string) error
}
