// dose_kg_pref.go — Postgres adapter for the per-KG daily-dose preference
// (dose_pref.Repository) port. Schema mirrors
// migrations/0070_learner_dose_kg_prefs.up.sql (ADR-224 change #3 / CHO-2045).
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the domain
// query; per-learner scoping is an explicit learner_gcid predicate (the tenant
// is enforced by RLS). Soft-deleted rows are filtered `WHERE deleted_at IS NULL`.
//
// Set is a natural-key UPSERT over the (tenant_id, learner_gcid, map_id) partial
// unique index (WHERE deleted_at IS NULL): a live preference is updated in place;
// otherwise a fresh UUIDv7 row is inserted. The row id + timestamps are minted /
// validated in the pure domain (dose_pref.New) — this adapter only orchestrates —
// and the authoritative row is read back via RETURNING. Mirrors
// learner_weakness.go (RLS + scan-stamp) + retention.go (ON CONFLICT upsert).
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	dp "github.com/apollo-chora/chora-consumption/internal/domain/dose_pref"
)

// DoseKGPrefRepo is the Postgres-backed dose_pref.Repository.
type DoseKGPrefRepo struct {
	tx TxRunner
}

// NewDoseKGPrefRepo constructs the repo around a TxRunner.
func NewDoseKGPrefRepo(tx TxRunner) *DoseKGPrefRepo {
	return &DoseKGPrefRepo{tx: tx}
}

var _ dp.Repository = (*DoseKGPrefRepo)(nil)

// ---------------------------------------------------------------------------
// List — every explicit preference row for a learner (both polarities)
// ---------------------------------------------------------------------------

func (r *DoseKGPrefRepo) List(ctx context.Context, tenantID, gcid string) ([]dp.DoseKGPref, error) {
	out := make([]dp.DoseKGPref, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listDoseKGPrefsSQL, gcid)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanDoseKGPref(rows)
			if err != nil {
				return fmt.Errorf("pg: scan dose_kg_pref: %w", err)
			}
			p.TenantID = tenantID
			p.LearnerGCID = gcid
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// ExcludedMapIDs — the excluded (included=false) live map set for the dose pool
// ---------------------------------------------------------------------------

func (r *DoseKGPrefRepo) ExcludedMapIDs(ctx context.Context, tenantID, gcid string) (map[string]bool, error) {
	out := map[string]bool{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, excludedDoseKGMapIDsSQL, gcid)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			var mapID string
			if err := rows.Scan(&mapID); err != nil {
				return fmt.Errorf("pg: scan excluded map_id: %w", err)
			}
			out[mapID] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Set — natural-key UPSERT (mint UUIDv7 on insert; update in place on conflict)
// ---------------------------------------------------------------------------

func (r *DoseKGPrefRepo) Set(ctx context.Context, tenantID, gcid, mapID string, included bool, now time.Time) (dp.DoseKGPref, error) {
	// Validate + mint (pure, fails fast) — id/timestamps come from the domain.
	fresh, err := dp.New(tenantID, gcid, mapID, included, now)
	if err != nil {
		return dp.DoseKGPref{}, err
	}
	var out dp.DoseKGPref
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, upsertDoseKGPrefSQL,
			fresh.ID, fresh.TenantID, fresh.LearnerGCID, fresh.MapID, fresh.Included,
			fresh.CreatedAt, fresh.UpdatedAt,
		)
		p, err := scanDoseKGPref(row)
		if err != nil {
			return fmt.Errorf("pg: upsert dose_kg_pref: %w", err)
		}
		p.TenantID = fresh.TenantID
		p.LearnerGCID = fresh.LearnerGCID
		out = p
		return nil
	})
	if err != nil {
		return dp.DoseKGPref{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// scan helper
// ---------------------------------------------------------------------------

// scanDoseKGPref reads the shared 5-column projection (id, map_id, included,
// created_at, updated_at) into a domain row. tenant_id + learner_gcid are the
// RLS-implicit / caller scope and are stamped by the calling method, never
// scanned (mirrors scanLearnerWeaknessRows). `scanner` is defined in
// learner_weakness.go and satisfied by both Row and Rows.
func scanDoseKGPref(sc scanner) (dp.DoseKGPref, error) {
	var p dp.DoseKGPref
	if err := sc.Scan(&p.ID, &p.MapID, &p.Included, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return dp.DoseKGPref{}, err
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// SQL templates (review-via-test; PREPARE-smoked under -tags integration)
// ---------------------------------------------------------------------------

// upsertDoseKGPrefSQL upserts one live preference. The conflict arbiter is the
// (tenant_id, learner_gcid, map_id) PARTIAL unique index over live rows — the
// `WHERE deleted_at IS NULL` predicate names it — so a soft-deleted row never
// blocks a fresh preference. RETURNING reads back the authoritative row (the
// existing id + created_at on the update path).
const upsertDoseKGPrefSQL = `
INSERT INTO learner_dose_kg_prefs
       (id, tenant_id, learner_gcid, map_id, included, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (tenant_id, learner_gcid, map_id) WHERE deleted_at IS NULL
DO UPDATE SET included = EXCLUDED.included, updated_at = EXCLUDED.updated_at
RETURNING id, map_id, included, created_at, updated_at`

// listDoseKGPrefsSQL returns every live preference for a learner (both
// polarities), oldest first for a stable order. Tenant scoping rides RLS.
const listDoseKGPrefsSQL = `
SELECT id, map_id, included, created_at, updated_at
  FROM learner_dose_kg_prefs
 WHERE learner_gcid = $1 AND deleted_at IS NULL
 ORDER BY created_at ASC, id ASC`

// excludedDoseKGMapIDsSQL returns the excluded live map_ids for a learner — the
// set the dose composer's pool filter subtracts. Tenant scoping rides RLS.
const excludedDoseKGMapIDsSQL = `
SELECT map_id
  FROM learner_dose_kg_prefs
 WHERE learner_gcid = $1 AND included = false AND deleted_at IS NULL`
