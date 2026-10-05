// growth_coverage_test.go — exercises the scan / helper / error-mapping
// paths of growth.go that the basic RLS-shape tests in growth_test.go skip.
//
// These tests do NOT spin up Postgres — they rely on the local Querier/Row
// stubs to drive the code through the various sentinel + helper branches.
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
// scanRow stub — a richer Row implementation that returns canned values for
// the column-by-column Scan call.
// ---------------------------------------------------------------------------

type scanFn func(dest ...any) error

type stubRow struct {
	fn scanFn
}

func (s stubRow) Scan(dest ...any) error {
	if s.fn == nil {
		return ErrNoRows
	}
	return s.fn(dest...)
}

// rowFixture builds a Scan callback that fills the CompanionGrowthRow column
// list in selectGrowthRowSQL order. The pod it describes is UNREVEALED
// (species + revealed_at both NULL — the CHO-2227 mystery invariant).
func rowFixture(companionID, tenantID, ownerGCID string, stage int, exp int) scanFn {
	return func(dest ...any) error {
		// 28 columns total (mirror selectGrowthRowSQL; revealed_at joined at
		// position 11 per CHO-2229, resonant_concept_id at position 10 with
		// visible_kg_neighbors retired, CHO-2013 P1).
		if len(dest) != 28 {
			return errors.New("dest count mismatch")
		}
		*(dest[0].(*string)) = companionID
		*(dest[1].(*string)) = tenantID
		*(dest[2].(*string)) = ownerGCID
		*(dest[3].(*int)) = stage
		// species, shiny_variant, species_rarity, rolled_probability — all NULL
		// (caller passes **string-typed pointers; we leave them at nil).
		*(dest[8].(*int)) = exp
		// resonant_atom_id (9), resonant_concept_id (10) — leave default
		// revealed_at (11), hatched_at (12) — nil
		*(dest[13].(*bool)) = false
		// remaining pointer-targets stay at their zero values (nil).
		_ = dest
		return nil
	}
}

// revealedRowFixture is rowFixture for a REVEALED pod: the CHO-2229 roll
// (species + revealed_at) is persisted; the row can commit.
func revealedRowFixture(companionID, tenantID, ownerGCID string, stage int, exp int, species string) scanFn {
	base := rowFixture(companionID, tenantID, ownerGCID, stage, exp)
	return func(dest ...any) error {
		if err := base(dest...); err != nil {
			return err
		}
		sp := species
		*(dest[4].(**string)) = &sp
		rt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
		*(dest[11].(**time.Time)) = &rt
		return nil
	}
}

// stubQuerier with row-routing for the various query shapes.
type routingQuerier struct {
	stubQuerier
	rows map[string]Row
}

func (r *routingQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	r.stubQuerier.execCalls = append(r.stubQuerier.execCalls, "QR:"+sql)
	for needle, row := range r.rows {
		if contains(sql, needle) {
			return row
		}
	}
	return nil
}

func (r *routingQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	r.stubQuerier.execCalls = append(r.stubQuerier.execCalls, "Q:"+sql)
	return nil, nil
}

// Exec reports a fresh write (RowsAffected==1) so AwardExpTx takes the
// fresh path. AwardExpTx now decides replay-vs-fresh by the growth_event
// INSERT's RowsAffected (ON CONFLICT DO NOTHING → 0); the base stubQuerier's
// 0-default would otherwise be misread as a replay. A genuine replay is
// modelled separately by dupQuerier (which keeps the 0-default).
func (r *routingQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	r.stubQuerier.execCalls = append(r.stubQuerier.execCalls, sql)
	return rls.CommandTag{RowsAffected: 1}, nil
}

type routingTxRunner struct {
	q *routingQuerier
}

func (t *routingTxRunner) RunInTx(ctx context.Context, fn func(context.Context, Querier) error) error {
	if t.q == nil {
		t.q = &routingQuerier{}
	}
	return fn(ctx, t.q)
}

// ---------------------------------------------------------------------------
// Tests — sentinel mapping + scan paths.
// ---------------------------------------------------------------------------

func TestGetGrowthRow_TenantMismatchReturnsNotFound(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FROM companion_instances": stubRow{
				fn: rowFixture("fam-1", "OTHER-TENANT", pgUserGCID, 1, 50),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	_, err := repo.GetGrowthRow(withCtx(), pgTenantID, "fam-1")
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("err = %v; want ErrCompanionNotFound", err)
	}
}

func TestGetGrowthRow_ScanErrorReturnsNotFound(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FROM companion_instances": stubRow{fn: nil}, // ErrNoRows
		},
	}}
	repo := NewGrowthRepo(tx)
	_, err := repo.GetGrowthRow(withCtx(), pgTenantID, "fam-1")
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("err = %v; want ErrCompanionNotFound", err)
	}
}

func TestCommitHatch_NotFoundOnMissingRow(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{}}
	repo := NewGrowthRepo(tx)
	_, err := repo.CommitHatch(withCtx(), growth.HatchTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		DisplayName: "P", Tone: "encouraging",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01970000-0000-7000-a000-000000000001",
		Now:            time.Now(),
	})
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("err = %v; want ErrCompanionNotFound", err)
	}
}

func TestCommitHatch_AlreadyHatchedWhenStageNotZero(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 1, 50),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	_, err := repo.CommitHatch(withCtx(), growth.HatchTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		DisplayName: "P", Tone: "encouraging",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01970000-0000-7000-a000-000000000001",
		Now:            time.Now(),
	})
	if !errors.Is(err, growth.ErrAlreadyHatched) {
		t.Errorf("err = %v; want ErrAlreadyHatched", err)
	}
}

func TestCommitHatch_NotRevealedWhenRollAbsent(t *testing.T) {
	// CHO-2229 commit-only: a stage-0 pod with revealed_at NULL must refuse
	// the commit — the ceremony reveals first.
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 0, 30),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	_, err := repo.CommitHatch(withCtx(), growth.HatchTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		DisplayName: "P", Tone: "encouraging",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01970000-0000-7000-a000-000000000001",
		Now:            time.Now(),
	})
	if !errors.Is(err, growth.ErrNotRevealed) {
		t.Errorf("err = %v; want ErrNotRevealed", err)
	}
}

func TestCommitHatch_HappyPathReturnsStage1(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: revealedRowFixture("fam-1", pgTenantID, pgUserGCID, 0, 30, "owl"),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	out, err := repo.CommitHatch(withCtx(), growth.HatchTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		DisplayName: "Pip", Tone: "encouraging",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01970000-0000-7000-a000-000000000001",
		Now:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CommitHatch: %v", err)
	}
	if out == nil || out.GrowthStage != 1 {
		t.Errorf("GrowthStage = %v, want 1", out)
	}
	if out.Species != "owl" {
		t.Errorf("Species = %q, want owl (the persisted reveal)", out.Species)
	}
}

func TestCommitReveal_PersistsRollOnUnrevealedPod(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 0, 30),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	out, err := repo.CommitReveal(withCtx(), growth.RevealTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Species: "owl", ShinyVariant: true, SpeciesRarity: "common",
		RolledProbability: 35, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CommitReveal: %v", err)
	}
	if out.Duplicate {
		t.Errorf("fresh reveal must not be Duplicate")
	}
	if out.Row.Species != "owl" || out.Row.RevealedAt == nil {
		t.Errorf("reveal must persist the roll, got species=%q revealed=%v", out.Row.Species, out.Row.RevealedAt)
	}
	found := false
	for _, call := range tx.q.execCalls {
		if contains(call, "revealed_at") && contains(call, "UPDATE companion_instances") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the commitRevealSQL UPDATE to run; calls: %v", tx.q.execCalls)
	}
}

func TestCommitReveal_DuplicateReturnsPersistedRoll(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: revealedRowFixture("fam-1", pgTenantID, pgUserGCID, 0, 30, "fox"),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	out, err := repo.CommitReveal(withCtx(), growth.RevealTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Species: "owl", ShinyVariant: false, SpeciesRarity: "common",
		RolledProbability: 35, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CommitReveal duplicate: %v", err)
	}
	if !out.Duplicate {
		t.Fatalf("expected Duplicate=true for an already-revealed pod")
	}
	if out.Row.Species != "fox" {
		t.Errorf("duplicate must return the PERSISTED roll fox, got %q", out.Row.Species)
	}
	for _, call := range tx.q.execCalls {
		if contains(call, "UPDATE companion_instances") && contains(call, "revealed_at        = ") {
			t.Errorf("duplicate reveal must not UPDATE; calls: %v", tx.q.execCalls)
		}
	}
}

func TestCommitReveal_AlreadyHatchedPastStageZero(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 1, 50),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	_, err := repo.CommitReveal(withCtx(), growth.RevealTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Species: "owl", SpeciesRarity: "common", RolledProbability: 35,
		Now: time.Now().UTC(),
	})
	if !errors.Is(err, growth.ErrAlreadyHatched) {
		t.Errorf("err = %v; want ErrAlreadyHatched", err)
	}
}

func TestAwardExpTx_RejectsEmptyTenant(t *testing.T) {
	repo := NewGrowthRepo(&stubTxRunner{})
	_, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID: "", CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

func TestAwardExpTx_RejectsNegativeDelta(t *testing.T) {
	repo := NewGrowthRepo(&stubTxRunner{})
	_, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Source: "atom_session", RequestedDelta: -1, IdempotencyKey: "k",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

func TestAwardExpTx_HappyPathHitsAllStatements(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 1, 48),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	out, err := repo.AwardExpTx(withCtx(), growth.AwardExpTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "evt-1",
		Now: time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC), ManaTier: "standard",
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if out == nil {
		t.Fatal("nil output")
	}
	if !out.TriggeredStageUp {
		t.Errorf("expected stage_up (was at 48, +3 → 51 > 50 Fledgling threshold)")
	}
	if out.NewStage != 2 {
		t.Errorf("NewStage = %d, want 2", out.NewStage)
	}
}

func TestMarkAhaMoment_RejectsEmptyTier(t *testing.T) {
	repo := NewGrowthRepo(&stubTxRunner{})
	_, err := repo.MarkAhaMoment(withCtx(), growth.AhaMomentInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		PreviewLLMTier: "", WindowExpiresAt: time.Now(),
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

func TestMarkAhaMoment_HappyPath(t *testing.T) {
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"FOR UPDATE": stubRow{
				fn: rowFixture("fam-1", pgTenantID, pgUserGCID, 3, 200),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	out, err := repo.MarkAhaMoment(withCtx(), growth.AhaMomentInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		PreviewLLMTier: "pro", WindowExpiresAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("MarkAhaMoment: %v", err)
	}
	if !out.AhaMomentConsumed {
		t.Errorf("AhaMomentConsumed = false, want true")
	}
}

func TestMarkAhaMoment_ConflictWhenAlreadyConsumed(t *testing.T) {
	// rowFixture sets aha_moment_consumed = false; build a custom one.
	row := stubRow{fn: func(dest ...any) error {
		*(dest[0].(*string)) = "fam-1"
		*(dest[1].(*string)) = pgTenantID
		*(dest[2].(*string)) = pgUserGCID
		*(dest[3].(*int)) = 3
		*(dest[8].(*int)) = 200
		*(dest[13].(*bool)) = true // aha_moment_consumed (revealed_at joined at 11, CHO-2229)
		return nil
	}}
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{"FOR UPDATE": row},
	}}
	repo := NewGrowthRepo(tx)
	_, err := repo.MarkAhaMoment(withCtx(), growth.AhaMomentInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		PreviewLLMTier: "pro", WindowExpiresAt: time.Now(),
	})
	if !errors.Is(err, growth.ErrAhaMomentConsumed) {
		t.Errorf("err = %v; want ErrAhaMomentConsumed", err)
	}
}

func TestProvisionEgg_RejectsEmptyInput(t *testing.T) {
	repo := NewGrowthRepo(&stubTxRunner{})
	_, err := repo.ProvisionEgg(withCtx(), growth.ProvisionEggInput{})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

func TestProvisionEgg_IdempotentOnExistingPurchase(t *testing.T) {
	// Lookup returns an existing row → INSERT is skipped + same row returned.
	tx := &routingTxRunner{q: &routingQuerier{
		rows: map[string]Row{
			"egg_purchase_id": stubRow{
				fn: rowFixture("fam-existing", pgTenantID, pgUserGCID, 0, 0),
			},
		},
	}}
	repo := NewGrowthRepo(tx)
	out, err := repo.ProvisionEgg(withCtx(), growth.ProvisionEggInput{
		TenantID: pgTenantID, OwnerGCID: pgUserGCID,
		EggSku: "egg.standard.v1", EggPurchaseID: "p-1", EggSource: "purchase",
	})
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}
	if out.CompanionID != "fam-existing" {
		t.Errorf("CompanionID = %s, want fam-existing", out.CompanionID)
	}
}

func TestCountCompanionsOfSpecies_HitsRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	n, err := repo.CountCompanionsOfSpecies(withCtx(), pgTenantID, pgUserGCID, "owl", "fid")
	if err != nil {
		t.Fatalf("CountCompanionsOfSpecies: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 (stub returns nil row)", n)
	}
}

// OwnerSpeciesSet (owner ruling 2026-08-07) must run inside the RLS session
// like every other read: it decides which species a learner is still allowed to
// roll, so a tenant-leaking read here would narrow a stranger's odds.
func TestOwnerSpeciesSet_HitsRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	owned, err := repo.OwnerSpeciesSet(withCtx(), pgTenantID, pgUserGCID, "fid")
	if err != nil {
		t.Fatalf("OwnerSpeciesSet: %v", err)
	}
	if owned == nil {
		t.Fatal("owned set must never be nil: a nil map read as an exclusion source is indistinguishable from a failed read")
	}
	if len(owned) != 0 {
		t.Errorf("owned = %v, want empty (stub returns no rows)", owned)
	}
	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
}

func TestListGrowthEvents_DefaultsPageSize(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	out, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID: pgTenantID, CompanionID: "fam-1", CallerGCID: pgUserGCID,
		PageSize: 0,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
	if out == nil {
		t.Fatal("nil output")
	}
}

func TestListGrowthEvents_ClampsHugePageSize(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	_, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID: pgTenantID, CompanionID: "fam-1", CallerGCID: pgUserGCID,
		PageSize: 10_000,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
}

func TestListGrowthEvents_AcceptsPageToken(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	tk := encodePageToken(time.Now(), "01970000-0000-7000-d000-000000000001")
	_, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID: pgTenantID, CompanionID: "fam-1", PageToken: tk,
		PageSize: 25,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pure helpers.
// ---------------------------------------------------------------------------

func TestNullStr_RoundTrips(t *testing.T) {
	if nullStr("") != nil {
		t.Error("empty string should encode as nil")
	}
	if got := nullStr("abc"); got != "abc" {
		t.Errorf("nullStr(abc) = %v", got)
	}
}

func TestNullInt_RoundTrips(t *testing.T) {
	if nullInt(0) != nil {
		t.Error("zero int should encode as nil")
	}
	if got := nullInt(7); got != 7 {
		t.Errorf("nullInt(7) = %v", got)
	}
}

func TestPageToken_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 123, time.UTC)
	id := "01970000-0000-7000-d000-000000000001"
	tk := encodePageToken(now, id)
	ts, gotID := decodePageToken(tk)
	if ts.UnixNano() != now.UnixNano() {
		t.Errorf("ts roundtrip = %v, want %v", ts, now)
	}
	if gotID != id {
		t.Errorf("id roundtrip = %s, want %s", gotID, id)
	}
}

func TestPageToken_HandlesMalformed(t *testing.T) {
	cases := []string{"", "garbage", "not-a-number:abc"}
	for _, c := range cases {
		ts, id := decodePageToken(c)
		if !ts.IsZero() || id != "" {
			t.Errorf("decode(%q) = %v, %q; want zero", c, ts, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Failure paths.
// ---------------------------------------------------------------------------

func TestGetGrowthRow_NilContext_FailsLoud(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	_, err := repo.GetGrowthRow(context.Background(), pgTenantID, "fam-1")
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Errorf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestAwardExpTx_NilContext_FailsLoud(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewGrowthRepo(tx)
	_, err := repo.AwardExpTx(context.Background(), growth.AwardExpTxInput{
		TenantID: pgTenantID, CompanionID: "fam-1", OwnerGCID: pgUserGCID,
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k",
		Now: time.Now(),
	})
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Errorf("err = %v; want ErrNoTenantContext", err)
	}
}
