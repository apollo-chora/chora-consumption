// companion_ritual_run_repo.go — Postgres adapter for the Ritual run ledger
// (companion.RunRepository; CHO-2016 G3). Schema mirrors
// migrations/0071_familiar_rituals.up.sql (companion_ritual_runs).
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the domain
// SQL. SweepStaleRunning is TENANT-SCOPED BY CONTEXT: it sweeps only the tenant
// set on ctx (the startup/periodic job sets the tenant; a multi-tenant fleet
// iterates tenants) — RLS FORCE means no cross-tenant read here, and no
// RLS-bypass surface is added (ddd-enforcement §2).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const createRitualRunSQL = `INSERT INTO companion_ritual_runs
    (run_id, tenant_id, companion_id, owner_gcid, ritual_id, ritual_name,
     revision_no, trigger_source, status, mana_charged, reservation_id,
     decision_stamp, step_outputs, paused_companion_id, sink_ref, error,
     started_at, completed_at)
    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`

const updateRitualRunSQL = `UPDATE companion_ritual_runs
    SET status=$2, mana_charged=$3, reservation_id=$4, decision_stamp=$5,
        step_outputs=$6, paused_companion_id=$7, sink_ref=$8, error=$9,
        completed_at=$10
    WHERE run_id=$1`

const sweepStaleRitualRunsSQL = `SELECT
    run_id, tenant_id, companion_id, owner_gcid, ritual_id, ritual_name,
    revision_no, trigger_source, status, mana_charged, reservation_id,
    sink_ref, error, started_at
    FROM companion_ritual_runs
    WHERE status = 'running' AND started_at < $1`

const listRunsByRitualSQL = `SELECT
    run_id, tenant_id, companion_id, owner_gcid, ritual_id, ritual_name,
    revision_no, trigger_source, status, mana_charged, sink_ref, error,
    decision_stamp, step_outputs, paused_companion_id, started_at, completed_at
    FROM companion_ritual_runs
    WHERE tenant_id = $1 AND ritual_id = $2
    ORDER BY started_at DESC
    LIMIT $3`

// getRitualRunSQL reads ONE run. Same column list and order as
// listRunsByRitualSQL so both share a scan shape; keyed on (tenant_id, run_id)
// with the explicit tenant predicate beside RLS, per multi-tenant-rls.
const getRitualRunSQL = `SELECT
    run_id, tenant_id, companion_id, owner_gcid, ritual_id, ritual_name,
    revision_no, trigger_source, status, mana_charged, sink_ref, error,
    decision_stamp, step_outputs, paused_companion_id, started_at, completed_at
    FROM companion_ritual_runs
    WHERE tenant_id = $1 AND run_id = $2`

// CompanionRitualRunRepo is the pg-backed Ritual run ledger.
type CompanionRitualRunRepo struct {
	tx TxRunner
}

// NewCompanionRitualRunRepo constructs the repo around a TxRunner.
func NewCompanionRitualRunRepo(tx TxRunner) *CompanionRitualRunRepo {
	return &CompanionRitualRunRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ companion.RunRepository = (*CompanionRitualRunRepo)(nil)

// marshalStepOutputs renders the S8 step outputs for the JSONB column. A nil
// slice becomes `[]`, NEVER `null`: the column is NOT NULL DEFAULT '[]' and an
// empty array is the honest reading of "this run produced no step outputs".
func marshalStepOutputs(outs []companion.StepOutput) ([]byte, error) {
	if outs == nil {
		outs = []companion.StepOutput{}
	}
	b, err := json.Marshal(outs)
	if err != nil {
		return nil, fmt.Errorf("pg: marshal ritual step outputs: %w", err)
	}
	return b, nil
}

// nullableID renders an optional id for a nullable UUID column: empty becomes
// SQL NULL rather than ”, because paused_companion_id has NO default and NULL
// is the value that means "this run was not paused". An empty string would not
// even cast to uuid.
func nullableID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// CreateRun inserts a new run row (typically status=running, or skipped_budget).
func (r *CompanionRitualRunRepo) CreateRun(ctx context.Context, run *companion.RitualRun) error {
	stamps, err := json.Marshal(run.Stamps)
	if err != nil {
		return fmt.Errorf("pg: marshal ritual run stamps: %w", err)
	}
	outputs, err := marshalStepOutputs(run.StepOutputs)
	if err != nil {
		return err
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, createRitualRunSQL,
			run.RunID, run.TenantID, run.CompanionID, run.OwnerGCID, run.RitualID, run.RitualName,
			run.RevisionNo, run.TriggerSource, string(run.Status), run.ManaCharged, run.ReservationID,
			stamps, outputs, nullableID(run.PausedCompanionID),
			run.SinkRef, run.Error, run.StartedAt.UTC(), utcPtr(run.CompletedAt),
		); err != nil {
			return fmt.Errorf("pg: create companion_ritual_run: %w", err)
		}
		return nil
	})
}

// UpdateRun finalises a run (status + stamps + step outputs + charge + sink_ref
// + completed_at).
//
// ⚠ It OVERWRITES step_outputs, so a caller that has not carried them would
// erase them. Both callers are the runner's own terminals, which always pass the
// run the recorder filled and whose terminate() normalises nil to an empty
// slice; a third caller would have to do the same.
func (r *CompanionRitualRunRepo) UpdateRun(ctx context.Context, run *companion.RitualRun) error {
	stamps, err := json.Marshal(run.Stamps)
	if err != nil {
		return fmt.Errorf("pg: marshal ritual run stamps: %w", err)
	}
	outputs, err := marshalStepOutputs(run.StepOutputs)
	if err != nil {
		return err
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, updateRitualRunSQL,
			run.RunID, string(run.Status), run.ManaCharged, run.ReservationID,
			stamps, outputs, nullableID(run.PausedCompanionID),
			run.SinkRef, run.Error, utcPtr(run.CompletedAt),
		); err != nil {
			return fmt.Errorf("pg: update companion_ritual_run: %w", err)
		}
		return nil
	})
}

// SweepStaleRunning returns runs still 'running' older than olderThan (tenant-
// scoped by ctx). The domain runner refunds + marks each failed via UpdateRun.
func (r *CompanionRitualRunRepo) SweepStaleRunning(ctx context.Context, olderThan time.Time) ([]*companion.RitualRun, error) {
	var out []*companion.RitualRun
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sweepStaleRitualRunsSQL, olderThan.UTC())
		if err != nil {
			return fmt.Errorf("pg: sweep stale ritual runs: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			run := &companion.RitualRun{}
			var status string
			if err := rows.Scan(
				&run.RunID, &run.TenantID, &run.CompanionID, &run.OwnerGCID, &run.RitualID, &run.RitualName,
				&run.RevisionNo, &run.TriggerSource, &status, &run.ManaCharged, &run.ReservationID,
				&run.SinkRef, &run.Error, &run.StartedAt,
			); err != nil {
				return fmt.Errorf("pg: scan stale ritual run: %w", err)
			}
			run.Status = companion.RunStatus(status)
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}

// ListRunsByRitual returns a ritual's runs newest-first (tenant-scoped by ctx +
// explicit predicate). Hydrates decision_stamp for the learner-story / O+
// projection.
func (r *CompanionRitualRunRepo) ListRunsByRitual(ctx context.Context, tenantID, ritualID string, limit int) ([]*companion.RitualRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []*companion.RitualRun
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listRunsByRitualSQL, tenantID, ritualID, limit)
		if err != nil {
			return fmt.Errorf("pg: list ritual runs: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanRitualRun(rows)
			if err != nil {
				return fmt.Errorf("pg: scan ritual run: %w", err)
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}

// GetRun reads ONE run by id (tenant-scoped by ctx + explicit predicate),
// hydrating decision_stamp + step_outputs exactly as the list read does.
//
// Returns companion.ErrRitualRunNotFound on no row, so the handler can answer
// 404 for a missing run and 500 for a store it could not read. Folding the two
// together would render a database outage as "that run does not exist".
func (r *CompanionRitualRunRepo) GetRun(ctx context.Context, tenantID, runID string) (*companion.RitualRun, error) {
	var out *companion.RitualRun
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		run, err := scanRitualRun(q.QueryRow(ctx, getRitualRunSQL, tenantID, runID))
		if err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %q", companion.ErrRitualRunNotFound, runID)
			}
			return err
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanRitualRun materialises one run from a scannable row. Shared by the list
// and the single read so the two cannot drift into hydrating different fields:
// a replay that silently lost step_outputs would render as a run that produced
// nothing, which is exactly the failure S8 exists to prevent.
func scanRitualRun(row Row) (*companion.RitualRun, error) {
	run := &companion.RitualRun{}
	var status string
	var stamps, outputs []byte
	var paused *string
	if err := row.Scan(
		&run.RunID, &run.TenantID, &run.CompanionID, &run.OwnerGCID, &run.RitualID, &run.RitualName,
		&run.RevisionNo, &run.TriggerSource, &status, &run.ManaCharged, &run.SinkRef, &run.Error,
		&stamps, &outputs, &paused, &run.StartedAt, &run.CompletedAt,
	); err != nil {
		return nil, err
	}
	run.Status = companion.RunStatus(status)
	if paused != nil {
		run.PausedCompanionID = *paused
	}
	if len(stamps) > 0 {
		if err := json.Unmarshal(stamps, &run.Stamps); err != nil {
			return nil, fmt.Errorf("pg: unmarshal run stamps: %w", err)
		}
	}
	// Fail LOUD on a malformed column: a swallowed unmarshal would serve a run
	// story with every step summary silently missing, which reads as a run that
	// produced nothing.
	if len(outputs) > 0 {
		if err := json.Unmarshal(outputs, &run.StepOutputs); err != nil {
			return nil, fmt.Errorf("pg: unmarshal run step outputs: %w", err)
		}
	}
	return run, nil
}

// utcPtr normalises a nullable timestamp to UTC (nil stays nil → SQL NULL).
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
