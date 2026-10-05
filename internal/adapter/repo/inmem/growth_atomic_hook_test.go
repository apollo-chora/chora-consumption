// growth_atomic_hook_test.go — CHO-2039 (CR §8 R6-3): the in-memory
// GrowthRepo honours the PostAwardInTx transaction contract with the same
// semantics as the pg adapter — a hook error rolls the whole award back
// (row, ledger, counter, idempotency key) and the hook also runs on the
// duplicate-replay repair lane.
package inmem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func seedGrowthRepo(t *testing.T) *inmem.GrowthRepo {
	t.Helper()
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t1", OwnerGCID: "u1",
		GrowthStage: 1, GrowthExp: 48, Species: "owl",
	})
	return repo
}

func hookAward(hook func(context.Context, *growth.AwardExpTxOutput) error) growth.AwardExpTxInput {
	return growth.AwardExpTxInput{
		TenantID: "t1", CompanionID: "fam-1", OwnerGCID: "u1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k1",
		Now: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC), ManaTier: "basic",
		PostAwardInTx: hook,
	}
}

// TestAwardExpTx_HookErrorRollsBackEverything — after a hook failure the
// row is untouched and the SAME award retries fresh (idempotency key and
// daily counter rolled back with it).
func TestAwardExpTx_HookErrorRollsBackEverything(t *testing.T) {
	repo := seedGrowthRepo(t)
	sentinel := errors.New("mint failed")

	_, err := repo.AwardExpTx(context.Background(), hookAward(func(context.Context, *growth.AwardExpTxOutput) error {
		return sentinel
	}))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the hook sentinel", err)
	}
	row, err := repo.GetGrowthRow(context.Background(), "t1", "fam-1")
	if err != nil {
		t.Fatalf("GetGrowthRow: %v", err)
	}
	if row.GrowthExp != 48 || row.GrowthStage != 1 {
		t.Errorf("row = exp %d / stage %d after hook fault, want 48 / 1 (rolled back)", row.GrowthExp, row.GrowthStage)
	}

	// Retry with the SAME key completes FRESH — nothing was consumed.
	out, err := repo.AwardExpTx(context.Background(), hookAward(nil))
	if err != nil {
		t.Fatalf("retry AwardExpTx: %v", err)
	}
	if out.Duplicate {
		t.Error("retry after rollback must be fresh, not a duplicate")
	}
	if out.ClampedDelta != 3 || out.Row.GrowthExp != 51 || out.Row.GrowthStage != 2 {
		t.Errorf("retry out = delta %d / exp %d / stage %d, want 3 / 51 / 2", out.ClampedDelta, out.Row.GrowthExp, out.Row.GrowthStage)
	}
	events, err := repo.ListGrowthEvents(context.Background(), growth.ListGrowthEventsInput{TenantID: "t1", CompanionID: "fam-1"})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
	if len(events.Events) != 1 {
		t.Errorf("ledger events = %d, want exactly 1 (aborted attempt left no row)", len(events.Events))
	}
}

// TestAwardExpTx_HookRunsOnDuplicateReplay — the repair lane: replays hand
// the hook the duplicate snapshot; a hook error aborts the replay.
func TestAwardExpTx_HookRunsOnDuplicateReplay(t *testing.T) {
	repo := seedGrowthRepo(t)
	if _, err := repo.AwardExpTx(context.Background(), hookAward(nil)); err != nil {
		t.Fatalf("first AwardExpTx: %v", err)
	}

	var sawDup bool
	out, err := repo.AwardExpTx(context.Background(), hookAward(func(_ context.Context, o *growth.AwardExpTxOutput) error {
		sawDup = o.Duplicate
		return nil
	}))
	if err != nil {
		t.Fatalf("duplicate AwardExpTx: %v", err)
	}
	if !out.Duplicate || !sawDup {
		t.Errorf("duplicate hook: out.Duplicate=%v, hook saw dup=%v, want true/true", out.Duplicate, sawDup)
	}

	sentinel := errors.New("repair mint failed")
	if _, err := repo.AwardExpTx(context.Background(), hookAward(func(context.Context, *growth.AwardExpTxOutput) error {
		return sentinel
	})); !errors.Is(err, sentinel) {
		t.Errorf("duplicate hook error = %v, want the sentinel surfaced", err)
	}
}
