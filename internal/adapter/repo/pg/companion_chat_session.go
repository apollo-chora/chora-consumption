// companion_chat_session.go — Postgres adapter for the ADR-154 Companion
// conversational chat session repository port (companion.ChatSessionRepository).
//
// Schema mirrors migrations/0035_familiar_chat_sessions.up.sql (objects renamed live by 0110_companion_rename):
//
//	companion_chat_sessions
//	  id                  UUID PRIMARY KEY
//	  tenant_id           UUID NOT NULL
//	  owner_gcid          UUID NOT NULL
//	  companion_id         UUID NOT NULL
//	  engine_session_id   TEXT NOT NULL
//	  mana_charged_total  INTEGER NOT NULL DEFAULT 0
//	  turn_count          INTEGER NOT NULL DEFAULT 0
//	  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
//	  last_used_at        TIMESTAMPTZ NOT NULL DEFAULT now()
//	  closed_at           TIMESTAMPTZ
//
// Per multi-tenant-rls SKILL: every read/write runs rls.ApplySession to
// set chora.tenant_id + chora.user_gcid session GUCs inside the transaction
// (defence-in-depth complementing per-database domain isolation).
//
// All operations are tenant-scoped by ctx (rls.ApplySession reads tenant_id
// off ctx via tracing.WithTenantID). Callers MUST set tenant_id on ctx
// BEFORE invoking these methods — otherwise the methods return
// rls.ErrNoTenantContext from ApplySession.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---------------------------------------------------------------------------
// CompanionChatSessionRepo
// ---------------------------------------------------------------------------

// CompanionChatSessionRepo is the Postgres-backed implementation of
// companion.ChatSessionRepository.
type CompanionChatSessionRepo struct {
	tx TxRunner
}

// NewCompanionChatSessionRepo constructs the repo around a TxRunner.
// Production wires a PgxTxRunner backed by a pgxpool.Pool; tests inject
// chatStubTxRunner.
func NewCompanionChatSessionRepo(tx TxRunner) *CompanionChatSessionRepo {
	return &CompanionChatSessionRepo{tx: tx}
}

// Compile-time guarantee that *CompanionChatSessionRepo satisfies the port.
var _ companion.ChatSessionRepository = (*CompanionChatSessionRepo)(nil)

// ---------------------------------------------------------------------------
// Get — single OPEN session for (owner_gcid, companion_id)
// ---------------------------------------------------------------------------

// Get returns the single open session for the pair, or
// companion.ErrChatSessionNotFound when no row matches. The unique partial
// index `(owner_gcid, companion_id) WHERE closed_at IS NULL` guarantees
// at-most-one match.
func (r *CompanionChatSessionRepo) Get(ctx context.Context, ownerGCID, companionID string) (*companion.ChatSession, error) {
	if strings.TrimSpace(ownerGCID) == "" {
		return nil, errors.New("pg: owner_gcid required")
	}
	if strings.TrimSpace(companionID) == "" {
		return nil, errors.New("pg: companion_id required")
	}
	var out *companion.ChatSession
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, selectCompanionChatSessionOpenSQL, ownerGCID, companionID)
		if row == nil {
			return companion.ErrChatSessionNotFound
		}
		var sess companion.ChatSession
		if err := row.Scan(
			&sess.ID,
			&sess.TenantID,
			&sess.OwnerGCID,
			&sess.CompanionID,
			&sess.EngineSessionID,
			&sess.ManaChargedTotal,
			&sess.TurnCount,
			&sess.CreatedAt,
			&sess.LastUsedAt,
			&sess.ClosedAt,
		); err != nil {
			if errors.Is(err, ErrNoRows) {
				return companion.ErrChatSessionNotFound
			}
			return fmt.Errorf("pg: scan chat_session: %w", err)
		}
		out = &sess
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, companion.ErrChatSessionNotFound
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Save — UPSERT
// ---------------------------------------------------------------------------

// Save upserts the session row. On first call (fresh session) INSERTs; on
// subsequent calls UPDATEs the counters + last_used_at + closed_at.
// Idempotent against the PRIMARY KEY.
func (r *CompanionChatSessionRepo) Save(ctx context.Context, sess companion.ChatSession) error {
	if strings.TrimSpace(sess.ID) == "" {
		return errors.New("pg: chat_session.id required")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, upsertCompanionChatSessionSQL,
			sess.ID,
			sess.TenantID,
			sess.OwnerGCID,
			sess.CompanionID,
			sess.EngineSessionID,
			sess.ManaChargedTotal,
			sess.TurnCount,
			sess.CreatedAt,
			sess.LastUsedAt,
			sess.ClosedAt,
		); err != nil {
			return fmt.Errorf("pg: upsert chat_session: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// MarkClosed — soft-delete
// ---------------------------------------------------------------------------

// MarkClosed sets closed_at = now() on the named session row. Used when the
// engine reports session-not-found and we rotate to a fresh engine_session_id.
// Idempotent — already-closed rows are unchanged.
func (r *CompanionChatSessionRepo) MarkClosed(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("pg: session_id required")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, markClosedCompanionChatSessionSQL, sessionID); err != nil {
			return fmt.Errorf("pg: mark closed chat_session: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// SQL templates (review-via-test)
// ---------------------------------------------------------------------------

// selectCompanionChatSessionOpenSQL fetches the single OPEN session for a
// (owner_gcid, companion_id) pair. The unique partial index
// idx_companion_chat_sessions_open_per_user guarantees at-most-one row.
// RLS policy `tenant_isolation` filters tenant scope.
const selectCompanionChatSessionOpenSQL = `
SELECT id,
       tenant_id,
       owner_gcid,
       companion_id,
       engine_session_id,
       mana_charged_total,
       turn_count,
       created_at,
       last_used_at,
       closed_at
  FROM companion_chat_sessions
 WHERE owner_gcid  = $1
   AND companion_id = $2
   AND closed_at IS NULL
 LIMIT 1`

// upsertCompanionChatSessionSQL writes the session row. ON CONFLICT(id)
// updates the mutable counters + timestamps; tenant + owner + companion +
// engine_session_id are stamped at creation and remain immutable.
const upsertCompanionChatSessionSQL = `
INSERT INTO companion_chat_sessions
       (id, tenant_id, owner_gcid, companion_id, engine_session_id,
        mana_charged_total, turn_count,
        created_at, last_used_at, closed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    ON CONFLICT (id) DO UPDATE
   SET mana_charged_total = EXCLUDED.mana_charged_total,
       turn_count         = EXCLUDED.turn_count,
       last_used_at       = EXCLUDED.last_used_at,
       closed_at          = EXCLUDED.closed_at`

// markClosedCompanionChatSessionSQL soft-deletes the session — sets
// closed_at = now() only when it is still NULL (idempotent on retry).
const markClosedCompanionChatSessionSQL = `
UPDATE companion_chat_sessions
   SET closed_at = now()
 WHERE id = $1
   AND closed_at IS NULL`
