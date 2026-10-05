// companion_ritual_repo.go — Postgres adapter for the CompanionRitual designer
// aggregate (companion.RitualRepository; CHO-2016 G3). Schema mirrors
// migrations/0071_familiar_rituals.up.sql (companion_rituals +
// companion_ritual_revisions).
//
// rls.ApplySession runs inside every tx before the domain SQL. Revisions are
// APPEND-ONLY (INSERT ... ON CONFLICT DO NOTHING; no UPDATE/DELETE path).
// ListByCompanion returns rituals WITHOUT their step revisions (the Grimoire
// list needs only name/trigger/sink/enabled/price) — GetByID hydrates the full
// aggregate incl. revisions for the run path.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const insertRitualSQL = `INSERT INTO companion_rituals
    (ritual_id, tenant_id, companion_id, name, trigger, trigger_config, sink,
     enabled, published_price_units, current_revision, created_at, updated_at)
    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`

const getRitualSQL = `SELECT
    ritual_id, tenant_id, companion_id, name, trigger, trigger_config, sink,
    enabled, published_price_units, current_revision, created_at, updated_at, deleted_at
    FROM companion_rituals
    WHERE tenant_id = $1 AND ritual_id = $2 AND deleted_at IS NULL`

const listRitualsByCompanionSQL = `SELECT
    ritual_id, tenant_id, companion_id, name, trigger, trigger_config, sink,
    enabled, published_price_units, current_revision, created_at, updated_at, deleted_at
    FROM companion_rituals
    WHERE tenant_id = $1 AND companion_id = $2 AND deleted_at IS NULL
    ORDER BY created_at DESC`

const listRitualRevisionsSQL = `SELECT revision_no, steps, armor_verdict, created_at
    FROM companion_ritual_revisions
    WHERE tenant_id = $1 AND ritual_id = $2
    ORDER BY revision_no ASC`

const insertRitualRevisionSQL = `INSERT INTO companion_ritual_revisions
    (revision_id, tenant_id, ritual_id, revision_no, steps, armor_verdict, created_at)
    VALUES ($1,$2,$3,$4,$5,$6,$7)
    ON CONFLICT (ritual_id, revision_no) DO NOTHING`

const bumpRitualRevisionSQL = `UPDATE companion_rituals
    SET current_revision = $3, published_price_units = $4, updated_at = now()
    WHERE tenant_id = $1 AND ritual_id = $2 AND deleted_at IS NULL`

const setRitualEnabledSQL = `UPDATE companion_rituals
    SET enabled = $3, updated_at = now()
    WHERE tenant_id = $1 AND ritual_id = $2 AND deleted_at IS NULL`

const countEnabledRitualsSQL = `SELECT count(*) FROM companion_rituals
    WHERE tenant_id = $1 AND companion_id = $2 AND enabled = TRUE AND deleted_at IS NULL
      AND ($3::uuid IS NULL OR ritual_id <> $3)`

const softDeleteRitualSQL = `UPDATE companion_rituals
    SET deleted_at = now(), enabled = FALSE, updated_at = now()
    WHERE tenant_id = $1 AND ritual_id = $2 AND deleted_at IS NULL`

// CompanionRitualRepo is the pg-backed Ritual aggregate repository.
type CompanionRitualRepo struct {
	tx TxRunner
}

// NewCompanionRitualRepo constructs the repo around a TxRunner.
func NewCompanionRitualRepo(tx TxRunner) *CompanionRitualRepo {
	return &CompanionRitualRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ companion.RitualRepository = (*CompanionRitualRepo)(nil)

// Create inserts a fresh draft ritual.
func (r *CompanionRitualRepo) Create(ctx context.Context, rit *companion.Ritual) error {
	tc, err := marshalTriggerConfig(rit.TriggerConfig)
	if err != nil {
		return err
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, insertRitualSQL,
			rit.RitualID, rit.TenantID, rit.CompanionID, rit.Name, string(rit.Trigger), tc, rit.Sink,
			rit.Enabled, rit.PublishedPriceUnits, rit.CurrentRevision, rit.CreatedAt.UTC(), rit.UpdatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("pg: insert companion_ritual: %w", err)
		}
		return nil
	})
}

// GetByID hydrates a non-deleted ritual with its ordered revisions.
func (r *CompanionRitualRepo) GetByID(ctx context.Context, tenantID, ritualID string) (*companion.Ritual, error) {
	var rit *companion.Ritual
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getRitualSQL, tenantID, ritualID)
		if row == nil {
			return nil // stub Querier → treat as not-found
		}
		loaded, err := scanRitual(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return nil // not found → (nil, nil)
			}
			return err
		}
		revs, err := loadRitualRevisions(ctx, q, tenantID, ritualID)
		if err != nil {
			return err
		}
		loaded.Revisions = revs
		rit = loaded
		return nil
	})
	return rit, err
}

// ListByCompanion returns the companion's non-deleted rituals (WITHOUT revisions).
func (r *CompanionRitualRepo) ListByCompanion(ctx context.Context, tenantID, companionID string) ([]*companion.Ritual, error) {
	var out []*companion.Ritual
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listRitualsByCompanionSQL, tenantID, companionID)
		if err != nil {
			return fmt.Errorf("pg: list companion_rituals: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			rit, err := scanRitual(rows)
			if err != nil {
				return err
			}
			out = append(out, rit)
		}
		return rows.Err()
	})
	return out, err
}

// AppendRevision inserts one append-only revision AND bumps the parent ritual
// (current_revision + published_price_units) atomically in one tx.
func (r *CompanionRitualRepo) AppendRevision(ctx context.Context, tenantID, ritualID string, rev companion.RitualRevision, publishedPriceUnits int) error {
	steps, err := json.Marshal(rev.Steps)
	if err != nil {
		return fmt.Errorf("pg: marshal ritual steps: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, insertRitualRevisionSQL,
			domain.NewUUIDv7(), tenantID, ritualID, rev.RevisionNo, steps, rev.ArmorVerdict, rev.CreatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("pg: insert companion_ritual_revision: %w", err)
		}
		if _, err := q.Exec(ctx, bumpRitualRevisionSQL, tenantID, ritualID, rev.RevisionNo, publishedPriceUnits); err != nil {
			return fmt.Errorf("pg: bump companion_ritual revision: %w", err)
		}
		return nil
	})
}

// SetEnabled persists Enable/Disable.
func (r *CompanionRitualRepo) SetEnabled(ctx context.Context, tenantID, ritualID string, enabled bool) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, setRitualEnabledSQL, tenantID, ritualID, enabled); err != nil {
			return fmt.Errorf("pg: set companion_ritual enabled: %w", err)
		}
		return nil
	})
}

// CountEnabledByCompanion counts enabled non-deleted rituals excluding one.
func (r *CompanionRitualRepo) CountEnabledByCompanion(ctx context.Context, tenantID, companionID, excludeRitualID string) (int, error) {
	var excludeArg any
	if excludeRitualID != "" {
		excludeArg = excludeRitualID
	}
	var n int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, countEnabledRitualsSQL, tenantID, companionID, excludeArg)
		if row == nil {
			return nil
		}
		if err := row.Scan(&n); err != nil {
			return fmt.Errorf("pg: count enabled companion_rituals: %w", err)
		}
		return nil
	})
	return n, err
}

// SoftDelete marks the ritual deleted (idempotent).
func (r *CompanionRitualRepo) SoftDelete(ctx context.Context, tenantID, ritualID string) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, softDeleteRitualSQL, tenantID, ritualID); err != nil {
			return fmt.Errorf("pg: soft-delete companion_ritual: %w", err)
		}
		return nil
	})
}

// loadRitualRevisions reads the ordered revisions for a ritual (within an
// existing tx/Querier).
func loadRitualRevisions(ctx context.Context, q Querier, tenantID, ritualID string) ([]companion.RitualRevision, error) {
	rows, err := q.Query(ctx, listRitualRevisionsSQL, tenantID, ritualID)
	if err != nil {
		return nil, fmt.Errorf("pg: list companion_ritual_revisions: %w", err)
	}
	if rows == nil {
		return nil, nil
	}
	defer rows.Close()
	var out []companion.RitualRevision
	for rows.Next() {
		var rev companion.RitualRevision
		var steps []byte
		if err := rows.Scan(&rev.RevisionNo, &steps, &rev.ArmorVerdict, &rev.CreatedAt); err != nil {
			return nil, fmt.Errorf("pg: scan companion_ritual_revision: %w", err)
		}
		if err := json.Unmarshal(steps, &rev.Steps); err != nil {
			return nil, fmt.Errorf("pg: unmarshal ritual steps: %w", err)
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

// scanRitual scans one companion_rituals row (from a QueryRow or a Query rows
// cursor). Revisions are hydrated separately.
func scanRitual(s interface{ Scan(dest ...any) error }) (*companion.Ritual, error) {
	var (
		rit  companion.Ritual
		trig string
		tc   []byte
	)
	if err := s.Scan(
		&rit.RitualID, &rit.TenantID, &rit.CompanionID, &rit.Name, &trig, &tc, &rit.Sink,
		&rit.Enabled, &rit.PublishedPriceUnits, &rit.CurrentRevision,
		&rit.CreatedAt, &rit.UpdatedAt, &rit.DeletedAt,
	); err != nil {
		return nil, err
	}
	rit.Trigger = companion.RitualTrigger(trig)
	if len(tc) > 0 {
		if err := json.Unmarshal(tc, &rit.TriggerConfig); err != nil {
			return nil, fmt.Errorf("pg: unmarshal ritual trigger_config: %w", err)
		}
	}
	return &rit, nil
}

// marshalTriggerConfig renders the trigger_config map to JSONB bytes,
// defaulting to an empty object.
func marshalTriggerConfig(tc map[string]any) ([]byte, error) {
	if len(tc) == 0 {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(tc)
	if err != nil {
		return nil, fmt.Errorf("pg: marshal ritual trigger_config: %w", err)
	}
	return b, nil
}
