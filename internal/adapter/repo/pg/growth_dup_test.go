// growth_dup_test.go — exercises the duplicate-replay path of AwardExpTx
// (an existing growth_events row pre-dates Now → INSERT was a no-op via
// ON CONFLICT DO NOTHING, and the repo must return Duplicate=true).
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// dupQuerier routes:
//   - FOR UPDATE → companion row
//   - selectExistingGrowthEventSQL → an existing event row with an older awarded_at
//   - selectGrowthDailyCounterSQL → empty counter
type dupQuerier struct {
	stubQuerier
	famRow           stubRow
	existingEventRow stubRow
}

func (q *dupQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, "QR:"+sql)
	switch {
	case contains(sql, "FOR UPDATE"):
		return q.famRow
	case contains(sql, "WHERE companion_id     = $1\n\t   AND source"):
		return q.existingEventRow
	case contains(sql, "companion_growth_daily_counters"):
		// returns zero — no rows.
		return stubRow{fn: nil}
	}
	return nil
}

func (q *dupQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, "Q:"+sql)
	return nil, nil
}

type dupTxRunner struct {
	q *dupQuerier
}

func (t *dupTxRunner) RunInTx(ctx context.Context, fn func(context.Context, Querier) error) error {
	return fn(ctx, t.q)
}

func TestAwardExpTx_DuplicateReplayReturnsDuplicateFlag(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	older := now.Add(-1 * time.Hour) // existing row pre-dates this call
	q := &dupQuerier{
		famRow: stubRow{
			fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 1, 48),
		},
		existingEventRow: stubRow{
			fn: growthEventScan("e0", "atom_session", 3, 51, older),
		},
	}
	repo := NewGrowthRepo(&dupTxRunner{q: q})

	out, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "fam-1",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "idem-e0",
		Now:            now,
		ManaTier:       "standard",
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if !out.Duplicate {
		t.Errorf("Duplicate = false; want true (replay path)")
	}
	if out.ClampedDelta != 0 {
		t.Errorf("ClampedDelta = %d; want 0 on replay", out.ClampedDelta)
	}
	if out.GrowthEvent == nil || out.GrowthEvent.GrowthEventID != "e0" {
		t.Errorf("expected existing growth_event returned")
	}
}

func TestAwardExpTx_CompanionMissingMapsToNotFound(t *testing.T) {
	q := &dupQuerier{
		famRow:           stubRow{fn: nil}, // ErrNoRows
		existingEventRow: stubRow{fn: nil},
	}
	repo := NewGrowthRepo(&dupTxRunner{q: q})
	_, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k",
		Now: time.Now().UTC(),
	})
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("err = %v; want ErrCompanionNotFound", err)
	}
}

func TestAwardExpTx_TenantMismatchMapsToNotFound(t *testing.T) {
	q := &dupQuerier{
		famRow: stubRow{
			fn: rowFixture("fam-1", "OTHER", pgUserGCID, 1, 0),
		},
		existingEventRow: stubRow{fn: nil},
	}
	repo := NewGrowthRepo(&dupTxRunner{q: q})
	_, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k",
		Now: time.Now().UTC(),
	})
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("err = %v; want ErrCompanionNotFound", err)
	}
}

// freshAwardQuerier simulates a FRESH AwardExpTx: the growth_event INSERT
// writes a row (RowsAffected==1, NOT an ON CONFLICT no-op), and
// selectExistingGrowthEventSQL reads it back with an awarded_at TRUNCATED to
// microseconds — exactly as real PostgreSQL timestamptz does. The pre-fix code
// compared existing.AwardedAt against the nanosecond-precision in.Now and
// misclassified this fresh insert as a replay, silently skipping the counter
// UPSERT + companion_instances UPDATE (every EXP award was dropped).
type freshAwardQuerier struct {
	stubQuerier
	famRow           stubRow
	existingEventRow stubRow
}

func (q *freshAwardQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, sql)
	// Every Exec (incl. the growth_event INSERT) reports a row written —
	// i.e. NOT an ON CONFLICT no-op, so this is a genuine fresh award.
	return rls.CommandTag{RowsAffected: 1}, nil
}

func (q *freshAwardQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, "QR:"+sql)
	switch {
	case contains(sql, "FOR UPDATE"):
		return q.famRow
	case contains(sql, "WHERE companion_id     = $1\n\t   AND source"):
		return q.existingEventRow
	case contains(sql, "companion_growth_daily_counters"):
		return stubRow{fn: nil}
	}
	return nil
}

func (q *freshAwardQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return nil, nil
}

type freshTxRunner struct{ q *freshAwardQuerier }

func (t *freshTxRunner) RunInTx(ctx context.Context, fn func(context.Context, Querier) error) error {
	return fn(ctx, t.q)
}

// TestAwardExpTx_FreshInsertAdvancesInstanceDespiteTimestampTruncation guards
// the regression where a freshly-inserted growth_event was misread as a replay
// because PG truncates timestamptz to microseconds. With a nanosecond-precision
// in.Now and a microsecond-truncated read-back, the fix (idempotency via the
// INSERT's RowsAffected, not awarded_at equality) must still take the fresh
// path: the counter UPSERT + companion_instances growth UPDATE both fire.
func TestAwardExpTx_FreshInsertAdvancesInstanceDespiteTimestampTruncation(t *testing.T) {
	// in.Now carries sub-microsecond nanoseconds (…123456789).
	now := time.Date(2026, 5, 13, 12, 0, 0, 123456789, time.UTC)
	// PG round-trips awarded_at truncated to microseconds (…123456000).
	truncated := time.Date(2026, 5, 13, 12, 0, 0, 123456000, time.UTC)
	q := &freshAwardQuerier{
		famRow:           stubRow{fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 2, 100)},
		existingEventRow: stubRow{fn: growthEventScan("e-fresh", "atom_session", 3, 103, truncated)},
	}
	repo := NewGrowthRepo(&freshTxRunner{q: q})

	out, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "fam-1",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "idem-fresh",
		Now:            now,
		ManaTier:       "standard",
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if out.Duplicate {
		t.Fatalf("Duplicate = true; want false — a fresh insert must NOT be misread as a replay")
	}
	if out.ClampedDelta != 3 {
		t.Errorf("ClampedDelta = %d; want 3 (fresh award)", out.ClampedDelta)
	}
	var sawInstanceUpdate, sawCounterUpsert bool
	for _, c := range q.execCalls {
		if contains(c, "effective_llm_tier_cached") && contains(c, "growth_exp") {
			sawInstanceUpdate = true
		}
		if contains(c, "companion_growth_daily_counters") && contains(c, "ON CONFLICT") {
			sawCounterUpsert = true
		}
	}
	if !sawInstanceUpdate {
		t.Errorf("companion_instances growth UPDATE was not issued — EXP would never advance")
	}
	if !sawCounterUpsert {
		t.Errorf("daily-counter UPSERT was not issued")
	}
}
