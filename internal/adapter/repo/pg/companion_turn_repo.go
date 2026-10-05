// companion_turn_repo.go - ADR-254 D4/D8: the pg TurnStore.
//
// Schema mirrors migrations/0111_companion_turns.up.sql (companion_turns, RLS
// ENABLE+FORCE on chora.tenant_id). Every call runs inside a tenant-scoped
// transaction (rls.ApplySession reads the tenant from the context: the HTTP
// request context for Begin/Get/Timeout, the result envelope's tenant for
// Complete).
//
// Begin is the load-bearing method: the turn row and the outbox REQUEST row are
// written in ONE transaction through the ambient querier (PgxTxRunner.RunAmbient
// + TxOutbox.PublishGrownInTx, the CHO-2129 same-tx idiom), so a turn is never
// "accepted" without its request being durably queued, and a request is never
// queued without the row the waiter polls.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// AmbientTxRunner is what Begin needs: a transaction whose querier is ambient on
// the context, so the outbox publish closure (TxOutbox) joins it.
type AmbientTxRunner interface {
	TxRunner
	RunAmbient(ctx context.Context, fn func(ctx context.Context) error) error
}

// CompanionTurnRepo is the pg-backed companion.TurnStore.
type CompanionTurnRepo struct {
	tx AmbientTxRunner
}

// NewCompanionTurnRepo constructs the repo over a PgxTxRunner.
func NewCompanionTurnRepo(tx AmbientTxRunner) *CompanionTurnRepo {
	return &CompanionTurnRepo{tx: tx}
}

var _ companion.TurnStore = (*CompanionTurnRepo)(nil)

const insertCompanionTurnSQL = `
INSERT INTO companion_turns
    (turn_id, tenant_id, owner_gcid, companion_id, conversation_id, kind, lane,
     status, request, requested_at, deadline_at, created_at, updated_at)
VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7,
        'accepted', $8::jsonb, $9, $10, $9, $9)
ON CONFLICT (turn_id) DO NOTHING`

const selectCompanionTurnSQL = `
SELECT turn_id, tenant_id, owner_gcid,
       COALESCE(companion_id::text, ''), COALESCE(conversation_id::text, ''),
       kind, lane, status, request, result,
       COALESCE(error_code, ''), COALESCE(workflow_id, ''), COALESCE(generated_by_model_id, ''),
       requested_at, deadline_at, completed_at,
       COALESCE(settled_by, ''), late_completion_at
  FROM companion_turns
 WHERE turn_id = $1`

const selectCompanionTurnForUpdateSQL = selectCompanionTurnSQL + ` FOR UPDATE`

const updateCompanionTurnTerminalSQL = `
UPDATE companion_turns
   SET status = $2,
       result = $3::jsonb,
       error_code = NULLIF($4, ''),
       workflow_id = NULLIF($5, ''),
       generated_by_model_id = NULLIF($6, ''),
       completed_at = $7,
       settled_by = $8,
       updated_at = now()
 WHERE turn_id = $1`

const timeoutCompanionTurnSQL = `
UPDATE companion_turns
   SET status = 'timeout', completed_at = $2, settled_by = 'timeout', updated_at = now()
 WHERE turn_id = $1 AND status = 'accepted'`

// recordLateCompanionTurnSQL persists the EVIDENCE of a result that arrived
// after the turn was already terminal. AUDIT-G5.
//
// It deliberately does NOT touch status, result or completed_at: the caller was
// answered with a timeout and rewriting that would destroy the very history the
// audit reads. `late_completion_at IS NULL` makes it idempotent under Pub/Sub
// redelivery, so a retried completion cannot walk the recorded time forward.
const recordLateCompanionTurnSQL = `
UPDATE companion_turns
   SET late_completion_at = $2,
       generated_by_model_id = COALESCE(NULLIF($3, ''), generated_by_model_id),
       updated_at = now()
 WHERE turn_id = $1 AND status <> 'accepted' AND late_completion_at IS NULL`

// Begin inserts the ACCEPTED turn row and runs publish in the same transaction.
// A duplicate turn_id is ErrTurnExists (nothing published, nothing written).
func (r *CompanionTurnRepo) Begin(ctx context.Context, turn *companion.Turn, publish func(ctx context.Context) error) error {
	if turn == nil {
		return errors.New("pg: companion turn required")
	}
	if publish == nil {
		return errors.New("pg: companion turn publish closure required")
	}
	if strings.TrimSpace(turn.TurnID) == "" || strings.TrimSpace(turn.TenantID) == "" {
		return fmt.Errorf("%w: turn_id + tenant_id required", companion.ErrTurnInvalid)
	}
	return r.tx.RunAmbient(ctx, func(ctx context.Context) error {
		q, ok := AmbientQuerier(ctx)
		if !ok {
			return errors.New("pg: companion turn Begin requires the ambient querier")
		}
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, insertCompanionTurnSQL,
			turn.TurnID, turn.TenantID, turn.OwnerGCID, turn.CompanionID, turn.ConversationID,
			string(turn.Kind), string(turn.Lane), string(turn.Request), turn.RequestedAt.UTC(), turn.DeadlineAt.UTC(),
		)
		if err != nil {
			return fmt.Errorf("pg: insert companion_turn %s: %w", turn.TurnID, err)
		}
		if tag.RowsAffected == 0 {
			return fmt.Errorf("%w: %s", companion.ErrTurnExists, turn.TurnID)
		}
		if err := publish(ctx); err != nil {
			return fmt.Errorf("pg: companion turn %s request publish: %w", turn.TurnID, err)
		}
		return nil
	})
}

// Get reads one turn (RLS scopes the tenant; tenantID is checked again on the
// row so a cross-tenant id never resolves).
func (r *CompanionTurnRepo) Get(ctx context.Context, tenantID, turnID string) (*companion.Turn, error) {
	if strings.TrimSpace(turnID) == "" {
		return nil, fmt.Errorf("%w: turn_id required", companion.ErrTurnInvalid)
	}
	var out *companion.Turn
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		t, err := scanCompanionTurn(q.QueryRow(ctx, selectCompanionTurnSQL, turnID))
		if err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out.TenantID != tenantID {
		return nil, companion.ErrTurnNotFound
	}
	return out, nil
}

// Complete applies the wire result exactly once (SELECT ... FOR UPDATE, domain
// Complete, UPDATE). A terminal turn reports applied=false with a nil error
// (idempotent redelivery); an unknown turn is ErrTurnNotFound.
func (r *CompanionTurnRepo) Complete(ctx context.Context, tenantID, turnID string, res companion.TurnResult) (bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return false, fmt.Errorf("%w: turn_id required", companion.ErrTurnInvalid)
	}
	applied := false
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		t, err := scanCompanionTurn(q.QueryRow(ctx, selectCompanionTurnForUpdateSQL, turnID))
		if err != nil {
			return err
		}
		if t.TenantID != tenantID {
			return companion.ErrTurnNotFound
		}
		if err := t.Complete(res); err != nil {
			if errors.Is(err, companion.ErrTurnAlreadyTerminal) {
				// AUDIT-G5. This branch used to `return nil`: the answer was
				// ACKed and discarded while turn.go's docstring claimed the
				// store "records it as late". It did not. A turn reaped at its
				// deadline and answered a second later read as one that was
				// never answered at all, which is a different fact and the one
				// the lane gate needs.
				//
				// Still ACK (return nil): a late answer is not a delivery
				// failure and must not be redelivered forever. But record it
				// first, and never flip the status back.
				if !t.RecordLateCompletion(res, time.Now()) {
					return nil // already recorded: a redelivery, nothing new to keep
				}
				if _, lerr := q.Exec(ctx, recordLateCompanionTurnSQL,
					t.TurnID, t.LateCompletionAt, t.GeneratedByModelID,
				); lerr != nil {
					return fmt.Errorf("pg: record late completion for companion_turn %s: %w", turnID, lerr)
				}
				return nil
			}
			return err
		}
		if _, err := q.Exec(ctx, updateCompanionTurnTerminalSQL,
			t.TurnID, string(t.Status), string(t.Result), t.ErrorCode, t.WorkflowID, t.GeneratedByModelID, t.CompletedAt, string(t.SettledBy),
		); err != nil {
			return fmt.Errorf("pg: complete companion_turn %s: %w", turnID, err)
		}
		applied = true
		return nil
	})
	return applied, err
}

// Timeout marks a still-accepted turn as timed out; applied=false when the
// result beat the reaper (or the turn was already terminal).
func (r *CompanionTurnRepo) Timeout(ctx context.Context, tenantID, turnID string, now time.Time) (bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return false, fmt.Errorf("%w: turn_id required", companion.ErrTurnInvalid)
	}
	applied := false
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		t, err := scanCompanionTurn(q.QueryRow(ctx, selectCompanionTurnSQL, turnID))
		if err != nil {
			return err
		}
		if t.TenantID != tenantID {
			return companion.ErrTurnNotFound
		}
		tag, err := q.Exec(ctx, timeoutCompanionTurnSQL, turnID, now.UTC())
		if err != nil {
			return fmt.Errorf("pg: timeout companion_turn %s: %w", turnID, err)
		}
		applied = tag.RowsAffected == 1
		return nil
	})
	return applied, err
}

func scanCompanionTurn(row Row) (*companion.Turn, error) {
	if row == nil {
		return nil, companion.ErrTurnNotFound
	}
	var (
		t           companion.Turn
		kind, lane  string
		status      string
		request     []byte
		result      []byte
		completedAt *time.Time
		settledBy   string
		lateAt      *time.Time
	)
	if err := row.Scan(
		&t.TurnID, &t.TenantID, &t.OwnerGCID, &t.CompanionID, &t.ConversationID,
		&kind, &lane, &status, &request, &result,
		&t.ErrorCode, &t.WorkflowID, &t.GeneratedByModelID,
		&t.RequestedAt, &t.DeadlineAt, &completedAt,
		&settledBy, &lateAt,
	); err != nil {
		if errors.Is(err, ErrNoRows) {
			return nil, companion.ErrTurnNotFound
		}
		return nil, fmt.Errorf("pg: scan companion_turn: %w", err)
	}
	t.Kind = companion.TurnKind(kind)
	t.Lane = companion.TurnLane(lane)
	// Read back so RecordLateCompletion can tell a genuine late arrival from a
	// redelivery of one already recorded.
	t.SettledBy = companion.TurnSettledBy(settledBy)
	t.LateCompletionAt = lateAt
	t.Status = companion.TurnStatus(status)
	t.Request = json.RawMessage(request)
	if len(result) > 0 {
		t.Result = json.RawMessage(result)
	}
	t.CompletedAt = completedAt
	return &t, nil
}
