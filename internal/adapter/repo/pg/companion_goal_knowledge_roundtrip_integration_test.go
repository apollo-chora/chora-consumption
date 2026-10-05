//go:build integration

// companion_goal_knowledge_roundtrip_integration_test.go — CHO-2118 sub-phase B:
// drives the REAL adapter over a REAL pgx connection, because the unit suite
// stubs Exec and therefore cannot see any of the failures that actually kill
// this table in production:
//
//   - binding "" into the UUID columns (generated_by_run_id / root_concept_id)
//     on a never-generated row → 22P02, i.e. the FIRST Upsert of every new
//     (companion, goal) pair;
//   - pgx handing back a zero time instead of nil for a NULL generated_at →
//     "never generated" silently becomes "generated in year one";
//   - an ON CONFLICT arbiter that does not actually match the live partial
//     unique index → a second row per key instead of an update;
//   - a stale row losing synthesis_text on the way through the DB →
//     serve-stale-while-regen degrades to serve-blank-while-regen;
//   - RLS not actually enforcing, with the explicit predicate masking it.
//
// Requires migration 0092 applied. Run against a NOBYPASSRLS role (the
// chora_consumption_app_rw shape) or the RLS phase proves nothing — a superuser
// bypasses FORCE RLS and the leak probe would pass vacuously (it is skipped, not
// silently passed, when the role can bypass).
//
//	export CHORA_TEST_DSN=postgres://...
//	go test -tags integration -run TestIntegration_GoalKnowledge ./internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	companiongoalknowledge "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
)

func TestIntegration_GoalKnowledge_RoundTrip(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewGoalKnowledgeRepo(pg.NewPgxTxRunner(pool))
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	companion, _ := uuid.NewV7()
	goal, _ := uuid.NewV7()
	root, _ := uuid.NewV7()
	rootID := root.String()

	ctxA := tracing.WithGCID(tracing.WithTenantID(ctx, tenantA.String()), learner.String())
	ctxB := tracing.WithGCID(tracing.WithTenantID(ctx, tenantB.String()), learner.String())

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx) //nolint:errcheck // best-effort cleanup
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM companion_goal_knowledge WHERE tenant_id = $1`, tenantA.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	// liveRows counts the live rows for the key under tenant A — the ON CONFLICT
	// proof (an arbiter that misses the partial index inserts a second row).
	liveRows := func(t *testing.T) int {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin count: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // read-only probe
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			t.Fatalf("count guc: %v", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM companion_goal_knowledge
             WHERE tenant_id = $1 AND learner_gcid = $2 AND companion_id = $3 AND goal_id = $4
               AND deleted_at IS NULL`,
			tenantA.String(), learner.String(), companion.String(), goal.String()).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// --- 1. never-generated row: every stamp is NULL (the 22P02 gate) ---------
	k, err := companiongoalknowledge.NewGoalKnowledge(
		tenantA.String(), learner.String(), companion.String(), goal.String(), &rootID)
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	if err := repo.Upsert(ctxA, k); err != nil {
		t.Fatalf("Upsert(never-generated): %v — an empty string bound into a uuid column?", err)
	}

	got, err := repo.FindByGoal(ctxA, tenantA.String(), learner.String(), companion.String(), goal.String())
	if err != nil {
		t.Fatalf("FindByGoal: %v", err)
	}
	if got == nil {
		t.Fatal("FindByGoal returned nil for the row just written")
	}
	// THE null gate, through real pgx rather than a fake row.
	if got.GeneratedAt != nil {
		t.Errorf("GeneratedAt = %v; want nil — a NULL timestamptz must not scan as a zero time", got.GeneratedAt)
	}
	if got.RequestedAt != nil || got.InvalidatedAt != nil {
		t.Errorf("NULL stamps scanned non-nil: req=%v inv=%v", got.RequestedAt, got.InvalidatedAt)
	}
	if got.SynthesisText != "" || got.GeneratedByRunID != "" {
		t.Errorf("NULL text columns must scan empty: text=%q run=%q", got.SynthesisText, got.GeneratedByRunID)
	}
	if got.Status != companiongoalknowledge.StatusPending ||
		got.InvalidationReason != companiongoalknowledge.ReasonNeverGenerated {
		t.Errorf("enums round-trip broken: status=%q reason=%q", got.Status, got.InvalidationReason)
	}
	if got.RootConceptID == nil || *got.RootConceptID != rootID {
		t.Errorf("RootConceptID = %v; want %q", got.RootConceptID, rootID)
	}
	if !got.NeedsSynthesis(time.Now().UTC(), companiongoalknowledge.DefaultRegenFloor,
		companiongoalknowledge.DefaultMaxAge, companiongoalknowledge.DefaultPendingTTL) {
		t.Error("a never-generated row read back from PG must still need synthesis")
	}

	// --- 2. claim → synthesise: the upsert UPDATES, never duplicates ----------
	const text = "You have been steady on fractions, but decimals still trip you up."
	now := time.Now().UTC()
	runID, _ := uuid.NewV7()

	k.MarkRequested(now)
	if err := repo.Upsert(ctxA, k); err != nil {
		t.Fatalf("Upsert(requested): %v", err)
	}
	if err := k.RecordSynthesis(text, runID.String(), "gemini-3-flash", "goal_reflection@v1", "sha256:abc", now); err != nil {
		t.Fatalf("RecordSynthesis: %v", err)
	}
	if err := repo.Upsert(ctxA, k); err != nil {
		t.Fatalf("Upsert(fresh): %v", err)
	}
	if n := liveRows(t); n != 1 {
		t.Fatalf("live rows for the key = %d; want exactly 1 — the ON CONFLICT arbiter "+
			"does not match uq_companion_goal_knowledge_live", n)
	}

	got, err = repo.FindByGoal(ctxA, tenantA.String(), learner.String(), companion.String(), goal.String())
	if err != nil {
		t.Fatalf("FindByGoal(fresh): %v", err)
	}
	if got.SynthesisText != text || !got.IsCacheFresh() {
		t.Errorf("fresh row = %q / fresh=%v", got.SynthesisText, got.IsCacheFresh())
	}
	if got.GeneratedAt == nil || got.GeneratedByRunID != runID.String() || got.PromptVersion != "goal_reflection@v1" {
		t.Errorf("ADR-197 decision stamp did not persist: %+v", got)
	}
	if got.RequestedAt != nil {
		t.Error("requested_at must clear once the synthesis lands (or the claim never releases)")
	}

	// --- 3. invalidate: the stale row KEEPS its text (serve-stale-while-regen) -
	k.Invalidate(companiongoalknowledge.ReasonWeaknessGrown)
	if err := repo.Upsert(ctxA, k); err != nil {
		t.Fatalf("Upsert(stale): %v", err)
	}
	got, err = repo.FindByGoal(ctxA, tenantA.String(), learner.String(), companion.String(), goal.String())
	if err != nil {
		t.Fatalf("FindByGoal(stale): %v", err)
	}
	if got.SynthesisText != text {
		t.Errorf("stale row came back with %q; the last good reflection must survive an "+
			"invalidation or the learner sees a blank card while it regenerates", got.SynthesisText)
	}
	if got.Status != companiongoalknowledge.StatusStale || !got.Servable() || got.IsCacheFresh() {
		t.Errorf("stale row must be servable-but-not-fresh: status=%q servable=%v fresh=%v",
			got.Status, got.Servable(), got.IsCacheFresh())
	}
	if got.InvalidatedAt == nil || got.InvalidationReason != companiongoalknowledge.ReasonWeaknessGrown {
		t.Errorf("invalidation not persisted: at=%v reason=%q", got.InvalidatedAt, got.InvalidationReason)
	}

	// --- 4. the two invalidation fan-outs both find the row -------------------
	byGoal, err := repo.ListByGoal(ctxA, tenantA.String(), learner.String(), goal.String())
	if err != nil {
		t.Fatalf("ListByGoal: %v", err)
	}
	byFam, err := repo.ListByCompanion(ctxA, tenantA.String(), learner.String(), companion.String())
	if err != nil {
		t.Fatalf("ListByCompanion: %v", err)
	}
	if len(byGoal) != 1 || len(byFam) != 1 {
		t.Errorf("fan-out targets = %d by-goal / %d by-companion; want 1 each", len(byGoal), len(byFam))
	}

	// --- 5. RLS leak probe: tenant A's predicate under tenant B's session ------
	// The explicit predicate would hide the row on its own, so the probe asks for
	// tenant A's row while the GUC says tenant B: only the policy can refuse it.
	if bypassesRLS(t, pool) {
		t.Log("SKIP RLS probe: connected role bypasses RLS (superuser/BYPASSRLS) — " +
			"re-run as a NOBYPASSRLS role to gate tenant isolation")
	} else {
		leaked, err := repo.FindByGoal(ctxB, tenantA.String(), learner.String(), companion.String(), goal.String())
		if err != nil {
			t.Fatalf("FindByGoal(cross-tenant): %v", err)
		}
		if leaked != nil {
			t.Errorf("CROSS-TENANT LEAK: tenant B's session read tenant A's reflection: %+v", leaked)
		}
	}

	// --- 6. Upsert REFUSES a soft-deleted aggregate ---------------------------
	// Not a policy choice — a structural one. The ON CONFLICT arbiter is the LIVE
	// partial unique index, so a proposed row carrying deleted_at matches nothing
	// in it; Postgres falls through to a plain INSERT and collides on the
	// aggregate's own id (23505 on the pkey — this test caught exactly that).
	// Refusing turns an opaque pkey violation into a named error.
	deletedAt := time.Now().UTC()
	deletedCopy := *k
	deletedCopy.DeletedAt = &deletedAt
	if err := repo.Upsert(ctxA, &deletedCopy); err == nil {
		t.Error("Upsert(soft-deleted) must fail loud — it cannot express a soft-delete")
	}
	if n := liveRows(t); n != 1 {
		t.Errorf("the refused Upsert must not have touched the table: live rows = %d, want 1", n)
	}

	// --- 7. a soft-delete DOES drop the row from every read -------------------
	// Performed the way a real Companion-retirement / closure sweep would do it: a
	// scoped UPDATE (the softDeleteMemoryByCompanionSQL shape), which is what the
	// port will need when that lane lands. Never a hard delete.
	softDelete := func(t *testing.T) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin soft-delete: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // committed on the happy path
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			t.Fatalf("soft-delete guc: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE companion_goal_knowledge SET deleted_at = now(), updated_at = now()
             WHERE tenant_id = $1 AND learner_gcid = $2 AND companion_id = $3 AND deleted_at IS NULL`,
			tenantA.String(), learner.String(), companion.String()); err != nil {
			t.Fatalf("soft-delete: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit soft-delete: %v", err)
		}
	}
	softDelete(t)

	gone, err := repo.FindByGoal(ctxA, tenantA.String(), learner.String(), companion.String(), goal.String())
	if err != nil {
		t.Fatalf("FindByGoal(soft-deleted): %v", err)
	}
	if gone != nil {
		t.Errorf("a soft-deleted row must not be served: %+v", gone)
	}
	afterGoal, err := repo.ListByGoal(ctxA, tenantA.String(), learner.String(), goal.String())
	if err != nil {
		t.Fatalf("ListByGoal(soft-deleted): %v", err)
	}
	afterFam, err := repo.ListByCompanion(ctxA, tenantA.String(), learner.String(), companion.String())
	if err != nil {
		t.Fatalf("ListByCompanion(soft-deleted): %v", err)
	}
	if len(afterGoal) != 0 || len(afterFam) != 0 {
		t.Errorf("soft-deleted row still in the fan-outs: %d by-goal / %d by-companion", len(afterGoal), len(afterFam))
	}

	// --- 8. resurrection: a fresh aggregate inserts BESIDE the tombstone -------
	// The tombstone is out of the live partial index, so the next Upsert must not
	// conflict with it (and must not resurrect it either — closure is forever).
	revived, err := companiongoalknowledge.NewGoalKnowledge(
		tenantA.String(), learner.String(), companion.String(), goal.String(), &rootID)
	if err != nil {
		t.Fatalf("NewGoalKnowledge(revived): %v", err)
	}
	if err := repo.Upsert(ctxA, revived); err != nil {
		t.Fatalf("Upsert(after soft-delete) must insert a fresh live row beside the tombstone: %v", err)
	}
	back, err := repo.FindByGoal(ctxA, tenantA.String(), learner.String(), companion.String(), goal.String())
	if err != nil {
		t.Fatalf("FindByGoal(revived): %v", err)
	}
	if back == nil || back.ID != revived.ID {
		t.Fatalf("the live row after resurrection = %+v; want the freshly minted aggregate", back)
	}
	if back.SynthesisText != "" || back.Status != companiongoalknowledge.StatusPending {
		t.Errorf("a resurrected row must start clean, not inherit the tombstone's reflection: %+v", back)
	}
}

// TestIntegration_GoalKnowledge_SoftDelete gates the ClosureRepository port
// (Companion retirement + account closure) against a real PostgreSQL.
//
// This table holds LLM-written prose ABOUT the learner, so a purge that silently
// purges nothing is a compliance failure that reports success. The suite proves
// the purge actually lands, is scoped, is idempotent, never hard-deletes, and
// cannot cross a tenant boundary — and, in the negative control below, why
// rls.ApplySession is not optional on a FORCE-RLS table.
func TestIntegration_GoalKnowledge_SoftDelete(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewGoalKnowledgeRepo(pg.NewPgxTxRunner(pool))
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	fam1, _ := uuid.NewV7()
	fam2, _ := uuid.NewV7()
	goal1, _ := uuid.NewV7()
	goal2, _ := uuid.NewV7()
	goal3, _ := uuid.NewV7()

	ctxA := tracing.WithGCID(tracing.WithTenantID(ctx, tenantA.String()), learner.String())
	ctxB := tracing.WithGCID(tracing.WithTenantID(ctx, tenantB.String()), learner.String())

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx) //nolint:errcheck // best-effort cleanup
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM companion_goal_knowledge WHERE tenant_id = $1`, tenantA.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	// countRows reports (live, tombstoned) for the learner under tenant A. The
	// tombstoned count is what proves we never hard-deleted.
	countRows := func(t *testing.T) (live, tombstoned int) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin count: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // read-only probe
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			t.Fatalf("count guc: %v", err)
		}
		if err := tx.QueryRow(ctx, `SELECT
               count(*) FILTER (WHERE deleted_at IS NULL)::int,
               count(*) FILTER (WHERE deleted_at IS NOT NULL)::int
             FROM companion_goal_knowledge WHERE tenant_id = $1 AND learner_gcid = $2`,
			tenantA.String(), learner.String()).Scan(&live, &tombstoned); err != nil {
			t.Fatalf("count: %v", err)
		}
		return live, tombstoned
	}

	seed := func(t *testing.T, companionID, goalID string) {
		t.Helper()
		k, err := companiongoalknowledge.NewGoalKnowledge(
			tenantA.String(), learner.String(), companionID, goalID, nil)
		if err != nil {
			t.Fatalf("seed NewGoalKnowledge: %v", err)
		}
		runID, _ := uuid.NewV7()
		if err := k.RecordSynthesis("You keep circling back to place value.",
			runID.String(), "gemini-3-flash", "goal_reflection@v1", "sha256:seed", time.Now().UTC()); err != nil {
			t.Fatalf("seed RecordSynthesis: %v", err)
		}
		if err := repo.Upsert(ctxA, k); err != nil {
			t.Fatalf("seed Upsert: %v", err)
		}
	}
	seed(t, fam1.String(), goal1.String())
	seed(t, fam1.String(), goal2.String())
	seed(t, fam2.String(), goal3.String())

	if live, dead := countRows(t); live != 3 || dead != 0 {
		t.Fatalf("seed = %d live / %d tombstoned; want 3 / 0", live, dead)
	}

	// --- NEGATIVE CONTROL: the same UPDATE with NO tenant GUC purges NOTHING ---
	//
	// This is why the repo's softDelete calls rls.ApplySession FIRST, and it is
	// not a formality. The table is FORCE RLS with
	//   USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)
	// so a purge that skips the GUC fails one of two ways, decided by whether the
	// pooled connection has ever carried the GUC before:
	//
	//   - fresh connection  → current_setting(...) is NULL → no row is visible →
	//     0 rows tombstoned and NO error. A purge that silently purges nothing,
	//     indistinguishable from "this learner had no cached reflections".
	//   - reused connection → the custom GUC does NOT revert to unset after the
	//     SET LOCAL transaction ends; the placeholder reverts to the EMPTY STRING,
	//     so the policy evaluates ''::uuid → 22P02 "invalid input syntax for type
	//     uuid". Loud, but it reads like a data bug rather than a missing GUC.
	//
	// Neither variant tombstones a single row. So the assertion that matters is
	// not "it errored" or "it returned 0" — it is that the learner's reflections
	// are all still live afterwards.
	if !bypassesRLS(t, pool) {
		func() {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin negative control: %v", err)
			}
			// MUST release the lock even on the error path: this UPDATE takes a
			// RowExclusiveLock, and a leaked transaction would deadlock the cleanup
			// DELETE on another connection.
			defer tx.Rollback(ctx) //nolint:errcheck // always rolled back
			tag, err := tx.Exec(ctx, `UPDATE companion_goal_knowledge
                   SET deleted_at = now(), updated_at = now()
                 WHERE tenant_id = $1 AND learner_gcid = $2 AND companion_id = $3
                   AND deleted_at IS NULL`,
				tenantA.String(), learner.String(), fam1.String())
			if err == nil && tag.RowsAffected() != 0 {
				t.Errorf("an UPDATE with no chora.tenant_id GUC tombstoned %d rows; it must "+
					"tombstone none — the RLS fence has moved", tag.RowsAffected())
			}
		}()
		if live, _ := countRows(t); live != 3 {
			t.Fatalf("negative control purged rows it could not see; live = %d, want 3", live)
		}
	}

	// --- 1. Companion retirement: purge fam1's reflections only ----------------
	n, err := repo.SoftDeleteByCompanion(ctxA, tenantA.String(), learner.String(), fam1.String())
	if err != nil {
		t.Fatalf("SoftDeleteByCompanion: %v", err)
	}
	if n != 2 {
		t.Fatalf("purged %d rows; want 2 (fam1's two goals) — a purge that reports the wrong "+
			"count cannot be audited", n)
	}
	live, dead := countRows(t)
	if live != 1 || dead != 2 {
		t.Errorf("after retirement = %d live / %d tombstoned; want 1 / 2", live, dead)
	}
	// NEVER hard-deleted: the two rows are still physically there, tombstoned.
	if dead != 2 {
		t.Errorf("rows were hard-deleted (tombstoned = %d); soft-delete is a hard invariant", dead)
	}
	// fam1 is gone from every read; fam2 is untouched.
	gone, err := repo.FindByGoal(ctxA, tenantA.String(), learner.String(), fam1.String(), goal1.String())
	if err != nil {
		t.Fatalf("FindByGoal(retired): %v", err)
	}
	if gone != nil {
		t.Errorf("a retired Companion's reflection must not be served: %+v", gone)
	}
	survivors, err := repo.ListByCompanion(ctxA, tenantA.String(), learner.String(), fam2.String())
	if err != nil {
		t.Fatalf("ListByCompanion(fam2): %v", err)
	}
	if len(survivors) != 1 {
		t.Errorf("retiring fam1 purged fam2's reflection too: %d rows left", len(survivors))
	}

	// --- 2. idempotent: retiring again purges nothing --------------------------
	again, err := repo.SoftDeleteByCompanion(ctxA, tenantA.String(), learner.String(), fam1.String())
	if err != nil {
		t.Fatalf("SoftDeleteByCompanion(again): %v", err)
	}
	if again != 0 {
		t.Errorf("second retirement purged %d rows; want 0 (deleted_at IS NULL makes it idempotent)", again)
	}

	// --- 3. RLS: tenant B cannot purge tenant A's rows -------------------------
	if bypassesRLS(t, pool) {
		t.Log("SKIP cross-tenant purge probe: role bypasses RLS — re-run as NOBYPASSRLS")
	} else {
		leaked, err := repo.SoftDeleteByCompanion(ctxB, tenantA.String(), learner.String(), fam2.String())
		if err != nil {
			t.Fatalf("SoftDeleteByCompanion(cross-tenant): %v", err)
		}
		if leaked != 0 {
			t.Errorf("CROSS-TENANT PURGE: tenant B tombstoned %d of tenant A's rows", leaked)
		}
		if live, _ := countRows(t); live != 1 {
			t.Errorf("tenant B's purge destroyed tenant A's data; live = %d, want 1", live)
		}
	}

	// --- 4. account closure sweeps every Companion ----------------------------
	closed, err := repo.SoftDeleteByLearner(ctxA, tenantA.String(), learner.String())
	if err != nil {
		t.Fatalf("SoftDeleteByLearner: %v", err)
	}
	if closed != 1 {
		t.Errorf("closure purged %d rows; want 1 (fam2's remaining reflection)", closed)
	}
	live, dead = countRows(t)
	if live != 0 {
		t.Errorf("closure left %d live reflections about the learner", live)
	}
	if dead != 3 {
		t.Errorf("tombstoned = %d; want all 3 rows retained as tombstones (never hard-deleted)", dead)
	}
}

// bypassesRLS reports whether the connected role sidesteps RLS entirely
// (superuser or BYPASSRLS) — in which case the leak probe would pass vacuously
// and must be skipped loudly rather than counted as a green isolation gate.
func bypassesRLS(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var bypass bool
	if err := pool.QueryRow(context.Background(),
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass); err != nil {
		t.Fatalf("role probe: %v", err)
	}
	return bypass
}
