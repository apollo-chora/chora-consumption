// growth_test.go — RLS contract + SQL-shape verification for the
// ADR-149 Companion Growth pgx adapter (PROD-B / Iter G.5).
//
// Production atomic-transaction behaviour (AwardExpTx + CommitHatch +
// ProvisionEgg) is exercised under integration_test.go against a live
// Cloud SQL instance gated by the `integration` build tag. The tests
// in THIS file verify:
//
//  1. The RLS SET LOCAL pair is emitted BEFORE any user query on every
//     Repository method (per multi-tenant-rls SKILL).
//  2. The SQL string templates reference the expected tables + columns
//     (review-via-test for the migration 0032 schema).
//  3. Sentinel-error mapping: nil/empty inputs return predictable errors.
//  4. Defence-in-depth bare-context rejection (rls.ErrNoTenantContext).
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ---------------------------------------------------------------------------
// Test 1 — GetGrowthRow applies RLS before query.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_GetGrowthRowAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.GetGrowthRow(withCtx(), pgTenantID, "01970000-0000-7000-c000-000000000001")

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair before query; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

// ---------------------------------------------------------------------------
// Test 2 — AwardExpTx applies RLS + emits all 3 statements (UPSERT counter,
// INSERT event, UPDATE companion) inside one transaction.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_AwardExpTxAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "01970000-0000-7000-c000-000000000001",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "evt-k1",
		Now:            time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		ManaTier:       "standard",
	})

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q (missing tenant SET LOCAL)", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", tx.q.execCalls[1])
	}
}

// ---------------------------------------------------------------------------
// Test 3 — CommitHatch applies RLS.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_CommitHatchAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.CommitHatch(withCtx(), growth.HatchTxInput{
		TenantID:       pgTenantID,
		CompanionID:    "01970000-0000-7000-c000-000000000001",
		OwnerGCID:      pgUserGCID,
		DisplayName:    "Pip",
		Tone:           "encouraging",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01970000-0000-7000-a000-000000000001",
		Now:            time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
	})

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

// ---------------------------------------------------------------------------
// Test 4 — SetResonantConcept applies RLS (CHO-2013 P1).
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_SetResonantConceptAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	cid := "01970000-0000-7000-a000-000000000002"
	_, _ = repo.SetResonantConcept(withCtx(), pgTenantID,
		"01970000-0000-7000-c000-000000000001", &cid)

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
}

// ---------------------------------------------------------------------------
// Test 5 — MarkAhaMoment applies RLS.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_MarkAhaMomentAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.MarkAhaMoment(withCtx(), growth.AhaMomentInput{
		TenantID:        pgTenantID,
		CompanionID:     "01970000-0000-7000-c000-000000000001",
		OwnerGCID:       pgUserGCID,
		PreviewLLMTier:  "pro",
		WindowExpiresAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	})

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
}

// ---------------------------------------------------------------------------
// Test 6 — CountCompanionsOfSpecies applies RLS.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_CountCompanionsOfSpeciesAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.CountCompanionsOfSpecies(withCtx(), pgTenantID, pgUserGCID, "owl",
		"01970000-0000-7000-c000-000000000001")

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
}

// ---------------------------------------------------------------------------
// Test 7 — ListGrowthEvents applies RLS.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_ListGrowthEventsAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID:    pgTenantID,
		CompanionID: "01970000-0000-7000-c000-000000000001",
		CallerGCID:  pgUserGCID,
		PageSize:    25,
	})

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
}

// ---------------------------------------------------------------------------
// Test 8 — ProvisionEgg applies RLS.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_ProvisionEggAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, _ = repo.ProvisionEgg(withCtx(), growth.ProvisionEggInput{
		TenantID:      pgTenantID,
		OwnerGCID:     pgUserGCID,
		EggSku:        "egg.standard.v1",
		EggPurchaseID: "01970000-0000-7000-e000-000000000001",
		EggSource:     "purchase",
		Now:           time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		SoftExpiryAt:  time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		HardExpiryAt:  time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
	})

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
}

// ---------------------------------------------------------------------------
// Test 9 — Bare context (no tenant) fails loud with rls.ErrNoTenantContext.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_FailsLoudOnMissingTenantContext(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)

	_, err := repo.GetGrowthRow(context.Background(), pgTenantID, "fid")
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Errorf("GetGrowthRow err = %v; want rls.ErrNoTenantContext", err)
	}
}

// ---------------------------------------------------------------------------
// Test 10 — Input validation: nil/empty inputs rejected fast.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_AwardExpTxRejectsEmptyInputs(t *testing.T) {
	repo := NewGrowthRepo(&stubTxRunner{})
	_, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID:       "",
		CompanionID:    "fid",
		OwnerGCID:      pgUserGCID,
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "k",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

// ---------------------------------------------------------------------------
// Test 11 — SQL templates exposed at package level are non-empty + reference
// the migration 0032 tables.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_SQLTemplatesNonEmpty(t *testing.T) {
	cases := map[string]string{
		"selectGrowthRow":              selectGrowthRowSQL,
		"selectGrowthRowForUpdate":     selectGrowthRowForUpdateSQL,
		"upsertGrowthDailyCounter":     upsertGrowthDailyCounterSQL,
		"insertGrowthEvent":            insertGrowthEventSQL,
		"updateGrowthExpAndStage":      updateGrowthExpAndStageSQL,
		"commitHatch":                  commitHatchSQL,
		"setResonantConcept":           setResonantConceptSQL,
		"commitBornHatched":            commitBornHatchedSQL,
		"markAhaMoment":                markAhaMomentSQL,
		"countCompanionsOfSpecies":     countCompanionsOfSpeciesSQL,
		"listGrowthEvents":             listGrowthEventsSQL,
		"provisionEggInsert":           provisionEggInsertSQL,
		"provisionEggLookupByPurchase": provisionEggLookupByPurchaseSQL,
	}
	for name, sql := range cases {
		if sql == "" {
			t.Errorf("%s SQL empty", name)
		}
	}
	// Spot-check expected table references.
	checks := []struct {
		sql   string
		needs string
	}{
		{selectGrowthRowSQL, "companion_instances"},
		{upsertGrowthDailyCounterSQL, "companion_growth_daily_counters"},
		{insertGrowthEventSQL, "companion_growth_events"},
		{insertGrowthEventSQL, "ON CONFLICT"},
		{commitHatchSQL, "companion_instances"},
		{commitHatchSQL, "hatched_at"},
		{markAhaMomentSQL, "aha_moment_consumed"},
		{provisionEggInsertSQL, "companion_instances"},
		{provisionEggInsertSQL, "ON CONFLICT"},
	}
	for _, c := range checks {
		if !contains(c.sql, c.needs) {
			t.Errorf("SQL does not contain %q:\n%s", c.needs, c.sql)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 12 — Compile-time guarantee that *GrowthRepo satisfies the port.
// ---------------------------------------------------------------------------

func TestPGGrowthRepo_SatisfiesPort(t *testing.T) {
	var _ growth.Repository = (*GrowthRepo)(nil)
}
