// growth_atomic_unlock_test.go — CHO-2039 (CR §8 R6-3): AwardExpTx runs the
// domain PostAwardInTx hook INSIDE the transaction with an ambient Querier,
// on both the fresh and the duplicate-replay branches; a hook error aborts
// the transaction. PgxTxRunner JOINS an ambient Querier instead of opening
// a second transaction, which is what lets the CompanionLoadoutRepo grant
// mints (same chora_consumption DB, same shared TxRunner in cmd/server)
// commit or roll back with the award.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// TestAwardExpTx_FreshPathRunsHookInsideAmbientTx — the fresh-award branch
// invokes the hook after the award writes, handing it the SAME Querier the
// transaction runs on (via the ambient ctx), so nested repo calls join.
func TestAwardExpTx_FreshPathRunsHookInsideAmbientTx(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	q := &freshAwardQuerier{
		famRow:           stubRow{fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 1, 48)},
		existingEventRow: stubRow{fn: growthEventScan("e-fresh", "atom_session", 3, 51, now)},
	}
	repo := NewGrowthRepo(&freshTxRunner{q: q})

	var hookOut *growth.AwardExpTxOutput
	var ambientOK bool
	var ambientSame bool
	out, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "fam-1",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "idem-hook-fresh",
		Now:            now,
		ManaTier:       "standard",
		PostAwardInTx: func(hookCtx context.Context, o *growth.AwardExpTxOutput) error {
			hookOut = o
			var aq Querier
			aq, ambientOK = AmbientQuerier(hookCtx)
			if !ambientOK {
				return nil // asserted below
			}
			ambientSame = aq == Querier(q)
			// A statement issued through the ambient Querier lands on the
			// transaction's statement stream (same tx = atomic with the
			// award writes).
			_, _ = aq.Exec(hookCtx, "PROBE: mint grants inside award tx")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if hookOut == nil {
		t.Fatal("PostAwardInTx was not invoked on the fresh path")
	}
	if hookOut.Duplicate || !hookOut.TriggeredStageUp || hookOut.Row.GrowthStage != 2 {
		t.Errorf("hook out = dup %v / stageUp %v / stage %d, want fresh stage-up to 2",
			hookOut.Duplicate, hookOut.TriggeredStageUp, hookOut.Row.GrowthStage)
	}
	if !ambientOK || !ambientSame {
		t.Errorf("ambient querier (ok=%v, same=%v), want the transaction's own Querier", ambientOK, ambientSame)
	}
	var probeAfterUpdate bool
	var sawUpdate bool
	for _, c := range q.execCalls {
		if contains(c, "growth_exp") && contains(c, "effective_llm_tier_cached") {
			sawUpdate = true
		}
		if contains(c, "PROBE: mint grants inside award tx") {
			probeAfterUpdate = sawUpdate
		}
	}
	if !probeAfterUpdate {
		t.Errorf("hook statement must land on the SAME statement stream AFTER the award writes; calls = %v", q.execCalls)
	}
	if out == nil || !out.TriggeredStageUp {
		t.Errorf("out = %+v, want a fresh stage-up output", out)
	}
}

// TestAwardExpTx_HookErrorAbortsFreshAward — a hook failure propagates out
// of the transaction closure, which is exactly what makes RunInTx roll the
// award writes back (PgxTxRunner rolls back on any fn error).
func TestAwardExpTx_HookErrorAbortsFreshAward(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	q := &freshAwardQuerier{
		famRow:           stubRow{fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 1, 48)},
		existingEventRow: stubRow{fn: growthEventScan("e-fresh", "atom_session", 3, 51, now)},
	}
	repo := NewGrowthRepo(&freshTxRunner{q: q})

	sentinel := errors.New("species-path mint failed")
	out, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "fam-1",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "idem-hook-abort",
		Now:            now,
		ManaTier:       "standard",
		PostAwardInTx: func(context.Context, *growth.AwardExpTxOutput) error {
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the hook sentinel (transaction aborted)", err)
	}
	if out != nil {
		t.Errorf("out = %+v, want nil on an aborted award", out)
	}
}

// TestAwardExpTx_DuplicatePathRunsHook — the replay branch also runs the
// hook (the CHO-2039 repair lane heal-mints atomically with the replay
// transaction) and surfaces its error.
func TestAwardExpTx_DuplicatePathRunsHook(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	older := now.Add(-1 * time.Hour)
	q := &dupQuerier{
		famRow:           stubRow{fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 2, 60)},
		existingEventRow: stubRow{fn: growthEventScan("e0", "atom_session", 3, 51, older)},
	}
	repo := NewGrowthRepo(&dupTxRunner{q: q})

	var hookOut *growth.AwardExpTxOutput
	var ambientOK bool
	out, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "fam-1",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "idem-e0",
		Now:            now,
		ManaTier:       "standard",
		PostAwardInTx: func(hookCtx context.Context, o *growth.AwardExpTxOutput) error {
			hookOut = o
			_, ambientOK = AmbientQuerier(hookCtx)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if hookOut == nil || !hookOut.Duplicate {
		t.Fatalf("hook out = %+v, want the duplicate snapshot", hookOut)
	}
	if hookOut.Row.GrowthStage != 2 {
		t.Errorf("hook row stage = %d, want the CURRENT stage 2 (repair lane unlocks through it)", hookOut.Row.GrowthStage)
	}
	if !ambientOK {
		t.Error("duplicate-path hook must also receive the ambient transaction Querier")
	}
	if out == nil || !out.Duplicate {
		t.Errorf("out = %+v, want Duplicate=true", out)
	}
}

// TestAwardExpTx_DuplicateHookErrorAborts — repair-lane mint failure on the
// replay branch surfaces (NACK → the next redelivery retries the heal).
func TestAwardExpTx_DuplicateHookErrorAborts(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	older := now.Add(-1 * time.Hour)
	q := &dupQuerier{
		famRow:           stubRow{fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 2, 60)},
		existingEventRow: stubRow{fn: growthEventScan("e0", "atom_session", 3, 51, older)},
	}
	repo := NewGrowthRepo(&dupTxRunner{q: q})

	sentinel := errors.New("repair-lane mint failed")
	_, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "fam-1",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "idem-e0",
		Now:            now,
		ManaTier:       "standard",
		PostAwardInTx: func(context.Context, *growth.AwardExpTxOutput) error {
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the repair-lane sentinel", err)
	}
}

// TestPgxTxRunner_JoinsAmbientQuerier — the production TxRunner joins an
// ambient transaction instead of beginning a new one: fn runs against the
// ambient Querier, the (nil) pool is never touched, and fn's error
// propagates to the transaction owner.
func TestPgxTxRunner_JoinsAmbientQuerier(t *testing.T) {
	runner := &PgxTxRunner{} // nil pool — BeginTx would panic if reached
	ambient := &stubQuerier{}
	ctx := WithAmbientQuerier(withCtx(), ambient)

	var got Querier
	sentinel := errors.New("inner failure must reach the tx owner")
	err := runner.RunInTx(ctx, func(_ context.Context, q Querier) error {
		got = q
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want fn's sentinel propagated (owner rolls back)", err)
	}
	if got != Querier(ambient) {
		t.Errorf("fn ran on %T, want the ambient stub querier (joined transaction)", got)
	}
}

// TestAmbientQuerier_AbsentByDefault — a bare ctx carries no ambient
// transaction; top-level repo calls keep their own begin/commit lifecycle.
func TestAmbientQuerier_AbsentByDefault(t *testing.T) {
	if q, ok := AmbientQuerier(context.Background()); ok || q != nil {
		t.Errorf("AmbientQuerier(bare ctx) = (%v, %v), want (nil, false)", q, ok)
	}
}
