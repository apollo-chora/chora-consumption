// companion_suspension_projection_repo.go: Postgres adapter for the ADR-254
// D11 advisory projection of chora.governance.audit.companion_suspension_
// changed.v1 (companion.SuspensionProjection), over migration 0112's
// companion_suspension_projection table.
//
// ⚠ ADVISORY. chora-model-gateway is the containment control (ADR-252 D1 /
// D6, uncached, fail-closed); this table only lets consumption refuse a chat
// turn honestly before the mana pre-check / ADR-235 claim and render a paused
// status. A stale row here is a UX wobble, never a containment breach.
//
// SQL contract:
//
//	Apply - ONE transaction under the row's own RLS scope:
//	  SET LOCAL chora.tenant_id = <event tenant>   (tenant scope)
//	                            = PlatformScopeTenantGUC (platform scope; the
//	                              policy admits tenant_id IS NULL rows for any
//	                              GUC, but FORCE RLS + ApplySession need a
//	                              UUID-shaped value, and 'platform' would not
//	                              cast: 22P02 on every tenant row).
//	  INSERT ... ON CONFLICT (suspension_id) DO UPDATE ... WHERE the held row
//	  is STRICTLY OLDER (source_version <, or = with an earlier changed_at)
//	  RETURNING source_version.
//	  No row returned => the event is STALE or a DUPLICATE (a state at least as
//	  new is already projected) => applied=false, nil error; the subscriber
//	  ACKs. THIS GUARD IS THE WHOLE POINT: Pub/Sub is at-least-once and not
//	  order-preserving, so without it a late "engaged" could overwrite a newer
//	  "released" (pausing a lifted companion) or the reverse.
//	  Identity columns (scope, tenant_id, skill_key) are write-once: the
//	  emitter never changes them across versions of one row, so the UPDATE arm
//	  deliberately does not touch them.
//	  Idempotency on event_id falls out of the version guard (a redelivered
//	  event carries the same version => not strictly newer => skipped);
//	  last_event_id records which event produced the held state.
//
//	Status - ONE read transaction under the LEARNER's tenant (rls.ApplySession
//	  on ctx; the repo stamps the tenant when the caller's ctx is bare and
//	  refuses a ctx that names a DIFFERENT tenant): SELECT every ENGAGED row
//	  the tenant may see (platform rows + its own), then the pure
//	  companion.DecideSuspension picks the answer. Released rows are kept (the
//	  lift history survives) but never selected.
//
// RLS (migration 0112): ENABLE + FORCE, policy
//
//	tenant_id IS NULL OR tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
//
// so a platform row is visible to every tenant and a tenant row only to its
// own. Every method therefore runs rls.ApplySession BEFORE its statement.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// PlatformScopeTenantGUC is the chora.tenant_id value a PLATFORM-scope write
// runs under. It is the nil UUID observability stamps as envelope.tenant_id
// for platform-scope events; the 0112 policy admits tenant_id IS NULL rows
// whatever the GUC holds, so the value only needs to be UUID-shaped.
const PlatformScopeTenantGUC = "00000000-0000-0000-0000-000000000000"

var (
	// ErrCompanionSuspensionTenantRequired: Status called without a tenant.
	// Fail loud: an empty tenant on a learner read would silently hide every
	// tenant-scope suspension.
	ErrCompanionSuspensionTenantRequired = errors.New("pg: companion suspension status requires a tenant id")
	// ErrCompanionSuspensionTenantMismatch: the ctx already carries a DIFFERENT
	// tenant than the one asked about. Reading tenant A's suspensions under
	// tenant B's GUC would be silently wrong, so refuse.
	ErrCompanionSuspensionTenantMismatch = errors.New("pg: companion suspension status tenant differs from the context tenant")
)

// CompanionSuspensionProjectionRepo implements companion.SuspensionProjection.
type CompanionSuspensionProjectionRepo struct {
	tx TxRunner
}

// NewCompanionSuspensionProjectionRepo constructs the repo around a TxRunner.
// Production wires NewPgxTxRunner(pool); tests inject a stub.
func NewCompanionSuspensionProjectionRepo(tx TxRunner) *CompanionSuspensionProjectionRepo {
	return &CompanionSuspensionProjectionRepo{tx: tx}
}

// Compile-time check.
var _ companion.SuspensionProjection = (*CompanionSuspensionProjectionRepo)(nil)

// ---------------------------------------------------------------------------
// Apply
// ---------------------------------------------------------------------------

// Apply projects one event. See the file-level SQL contract.
func (r *CompanionSuspensionProjectionRepo) Apply(ctx context.Context, ev companion.SuspensionChanged) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	// The write is scoped by the EVENT, not by whatever the caller's context
	// carries (a pull subscriber has no request tenant; a handler's would be
	// the wrong one).
	guc := PlatformScopeTenantGUC
	if ev.Scope == companion.SuspensionScopeTenant {
		guc = ev.TenantID
	}
	ctx = tracing.WithTenantID(ctx, guc)

	applied := false
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var projectedVersion int64
		err := q.QueryRow(ctx, upsertCompanionSuspensionProjectionSQL,
			ev.SuspensionID,
			string(ev.Scope),
			ev.TenantID,
			ev.SkillKey,
			ev.Engaged,
			ev.Reason,
			ev.ActorGCID,
			ev.Version,
			ev.ChangedAt,
			ev.EventID,
		).Scan(&projectedVersion)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				// Stale or duplicate: a state at least as new is already held.
				// Correct outcome, not a fault; the caller ACKs.
				applied = false
				return nil
			}
			return fmt.Errorf("pg: upsert companion_suspension_projection: %w", err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("pg.CompanionSuspensionProjectionRepo.Apply: %w", err)
	}
	return applied, nil
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// Status answers the advisory question for (tenantID, actionCode). See the
// file-level contract; a non-nil error means UNKNOWN to the caller.
func (r *CompanionSuspensionProjectionRepo) Status(ctx context.Context, tenantID, actionCode string) (companion.SuspensionStatus, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return companion.SuspensionStatus{}, ErrCompanionSuspensionTenantRequired
	}
	switch ctxTenant := tracing.TenantIDFromContext(ctx); {
	case ctxTenant == "":
		ctx = tracing.WithTenantID(ctx, tenantID)
	case ctxTenant != tenantID:
		return companion.SuspensionStatus{}, ErrCompanionSuspensionTenantMismatch
	}

	var records []companion.SuspensionRecord
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, selectEngagedCompanionSuspensionsSQL, tenantID)
		if err != nil {
			return fmt.Errorf("pg: select engaged companion suspensions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				rec   companion.SuspensionRecord
				scope string
			)
			if err := rows.Scan(
				&rec.SuspensionID,
				&scope,
				&rec.TenantID,
				&rec.SkillKey,
				&rec.Engaged,
				&rec.Reason,
				&rec.ActorGCID,
				&rec.SourceVersion,
				&rec.ChangedAt,
			); err != nil {
				return fmt.Errorf("pg: scan companion suspension row: %w", err)
			}
			rec.Scope = companion.SuspensionScope(scope)
			records = append(records, rec)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg: iterate companion suspension rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return companion.SuspensionStatus{}, fmt.Errorf("pg.CompanionSuspensionProjectionRepo.Status: %w", err)
	}
	return companion.DecideSuspension(records, tenantID, actionCode), nil
}

// ---------------------------------------------------------------------------
// SQL templates (review-via-test; parse-smoked in
// companion_suspension_projection_prepare_smoke_integration_test.go)
// ---------------------------------------------------------------------------

// upsertCompanionSuspensionProjectionSQL is the monotonic guard: the UPDATE
// arm fires only when the held row is strictly older, so a stale or duplicate
// event matches nothing, RETURNING yields no row, and Apply reports false.
const upsertCompanionSuspensionProjectionSQL = `
INSERT INTO companion_suspension_projection
       (suspension_id, scope, tenant_id, skill_key, engaged, reason, actor_gcid,
        source_version, changed_at, last_event_id, projected_at)
VALUES ($1::uuid, $2, NULLIF($3, '')::uuid, NULLIF($4, ''), $5, $6, $7::uuid,
        $8, $9, $10::uuid, now())
    ON CONFLICT (suspension_id) DO UPDATE
   SET engaged        = EXCLUDED.engaged,
       reason         = EXCLUDED.reason,
       actor_gcid     = EXCLUDED.actor_gcid,
       source_version = EXCLUDED.source_version,
       changed_at     = EXCLUDED.changed_at,
       last_event_id  = EXCLUDED.last_event_id,
       projected_at   = now()
 WHERE companion_suspension_projection.source_version < EXCLUDED.source_version
    OR (companion_suspension_projection.source_version = EXCLUDED.source_version
        AND companion_suspension_projection.changed_at < EXCLUDED.changed_at)
RETURNING source_version`

// selectEngagedCompanionSuspensionsSQL fetches every ENGAGED row the tenant
// may see: platform rows (every tenant) plus its own. RLS already restricts
// the visible set identically; the predicate keeps the read honest on its own.
const selectEngagedCompanionSuspensionsSQL = `
SELECT suspension_id::text,
       scope,
       COALESCE(tenant_id::text, ''),
       COALESCE(skill_key, ''),
       engaged,
       reason,
       actor_gcid::text,
       source_version,
       changed_at
  FROM companion_suspension_projection
 WHERE engaged = TRUE
   AND (scope = 'platform' OR tenant_id = $1::uuid)
 ORDER BY changed_at DESC`
