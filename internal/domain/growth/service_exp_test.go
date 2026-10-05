// service_exp_test.go — CHO-2012 P0 service-seam tests: ADR-218 D6 (EXP
// rules resolved via the ExpRuler port, in-code map as documented loud
// fallback), D2 (skill_slot_unlocked emission on every stage transition) and
// D3 (species-Path unlocker invoked on stage-up + on duplicate replay so a
// crash between award-commit and unlock heals on redelivery). RED-first.
package growth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// fakeRuler is a configurable ExpRuler port double.
type fakeRuler struct {
	mu    sync.Mutex
	rule  growth.ExpRule
	err   error
	calls int
}

func (f *fakeRuler) ResolveExpRule(_ context.Context, _, _ string) (growth.ExpRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return growth.ExpRule{}, f.err
	}
	return f.rule, nil
}

// fakeUnlocker records the two-phase SkillUnlocker invocations (CHO-2039).
// err fails the MINT phase (the in-transaction seam); publishErr fails the
// post-commit publish phase. minted is what every successful mint returns.
type fakeUnlocker struct {
	mu           sync.Mutex
	calls        []growth.UnlockThroughStageInput // mint invocations
	publishCalls [][]growth.MintedPathGrant
	minted       []growth.MintedPathGrant
	err          error
	publishErr   error
}

func (f *fakeUnlocker) MintThroughStage(_ context.Context, in growth.UnlockThroughStageInput) ([]growth.MintedPathGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.minted, nil
}

func (f *fakeUnlocker) PublishGranted(_ context.Context, _ growth.UnlockThroughStageInput, minted []growth.MintedPathGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return f.publishErr
	}
	f.publishCalls = append(f.publishCalls, minted)
	return nil
}

func seedStage1Row(repo *fakeRepo, exp int) {
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: exp, Species: "owl",
	}
}

func baseAward(source string, delta int) growth.AwardExpInput {
	return growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-1",
		OwnerGCID:      "user-1",
		Source:         source,
		RequestedDelta: delta,
		IdempotencyKey: "idem-1",
		Traceparent:    "00-trace-01",
	}
}

// TestAwardExp_ResolvedDefaultDelta — RequestedDelta 0 means "use the
// resolver's value" (the subscriber stops hardcoding per-source literals).
func TestAwardExp_ResolvedDefaultDelta(t *testing.T) {
	ruler := &fakeRuler{rule: growth.ExpRule{Value: 7, DailyCap: 30, Enabled: true}}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.ExpRules = ruler })
	seedStage1Row(repo, 0)

	resp, err := svc.AwardExp(context.Background(), baseAward("atom_session", 0))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.ClampedDelta != 7 {
		t.Errorf("ClampedDelta = %d, want the resolved value 7", resp.ClampedDelta)
	}
	if ruler.calls != 1 {
		t.Errorf("resolver calls = %d, want 1", ruler.calls)
	}
}

// TestAwardExp_ExplicitDeltaStillHonored — an explicit positive delta
// (atom_session incorrect=1, admin_grant) is used as-is, still cap-clamped.
func TestAwardExp_ExplicitDeltaStillHonored(t *testing.T) {
	ruler := &fakeRuler{rule: growth.ExpRule{Value: 3, DailyCap: 30, Enabled: true}}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.ExpRules = ruler })
	seedStage1Row(repo, 0)

	resp, err := svc.AwardExp(context.Background(), baseAward("atom_session", 1))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.ClampedDelta != 1 {
		t.Errorf("ClampedDelta = %d, want the explicit 1", resp.ClampedDelta)
	}
}

// TestAwardExp_ResolvedCapClamps — the RESOLVER's daily cap (not the
// in-code map's) clamps the award: cap 2 clamps a 3-EXP award to 2.
func TestAwardExp_ResolvedCapClamps(t *testing.T) {
	ruler := &fakeRuler{rule: growth.ExpRule{Value: 3, DailyCap: 2, Enabled: true}}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.ExpRules = ruler })
	seedStage1Row(repo, 0)

	resp, err := svc.AwardExp(context.Background(), baseAward("atom_session", 0))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.ClampedDelta != 2 || !resp.DailyCapHit {
		t.Errorf("got (delta=%d, capHit=%v), want (2, true) from the resolved cap", resp.ClampedDelta, resp.DailyCapHit)
	}
}

// TestAwardExp_DisabledSourceSkips — a disabled source resolves successfully
// and the award is SKIPPED: no ledger write, no events, Skipped=true.
func TestAwardExp_DisabledSourceSkips(t *testing.T) {
	ruler := &fakeRuler{rule: growth.ExpRule{Value: 3, DailyCap: 30, Enabled: false}}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.ExpRules = ruler })
	seedStage1Row(repo, 10)

	resp, err := svc.AwardExp(context.Background(), baseAward("atom_session", 0))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if !resp.Skipped {
		t.Error("Skipped = false, want true for a disabled source")
	}
	if len(repo.events) != 0 {
		t.Errorf("ledger events = %d, want 0 (no write on skip)", len(repo.events))
	}
	if len(ox.topics()) != 0 {
		t.Errorf("outbox topics = %v, want none on skip", ox.topics())
	}
	if resp.Row == nil || resp.Row.GrowthExp != 10 {
		t.Error("skip response must still carry the current row state")
	}
}

// TestAwardExp_ResolverErrorFallsBackLoud — resolver failure uses the
// in-code parity map AND fires the fallback hook (documented, never silent).
func TestAwardExp_ResolverErrorFallsBackLoud(t *testing.T) {
	ruler := &fakeRuler{err: errors.New("identity unreachable")}
	var hookSource string
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) {
		c.ExpRules = ruler
		c.OnExpRuleFallback = func(source string, err error) { hookSource = source }
	})
	seedStage1Row(repo, 0)

	resp, err := svc.AwardExp(context.Background(), baseAward("atom_session", 0))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.ClampedDelta != 3 {
		t.Errorf("ClampedDelta = %d, want the parity fallback 3", resp.ClampedDelta)
	}
	if hookSource != "atom_session" {
		t.Errorf("fallback hook source = %q, want atom_session", hookSource)
	}
}

// TestAwardExp_ZeroEffectiveDeltaRejected — a zero-valued source (e.g.
// admin_grant) with RequestedDelta 0 has nothing to award: explicit delta
// required, fail-loud.
func TestAwardExp_ZeroEffectiveDeltaRejected(t *testing.T) {
	ruler := &fakeRuler{rule: growth.ExpRule{Value: 0, DailyCap: 0, Enabled: true}}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.ExpRules = ruler })
	seedStage1Row(repo, 0)

	if _, err := svc.AwardExp(context.Background(), baseAward("admin_grant", 0)); err == nil {
		t.Fatal("AwardExp with zero effective delta must error (explicit delta required)")
	}
}

// TestAwardExp_StageUpEmitsSlotUnlockedAndUnlocksPath — a stage transition
// emits skill_slot_unlocked (first real emission of the defined topic) and
// invokes the species-Path unlocker for the NEW stage.
func TestAwardExp_StageUpEmitsSlotUnlockedAndUnlocksPath(t *testing.T) {
	unlocker := &fakeUnlocker{}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40) // 40 + 15 = 55 >= 50 → stage 2

	resp, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if !resp.StageUpTriggered {
		t.Fatal("expected a stage-up")
	}
	topics := ox.topics()
	var slotEvent *outboxCall
	for i := range ox.calls {
		if ox.calls[i].Topic == growth.TopicCompanionSkillSlotUnlocked {
			slotEvent = &ox.calls[i]
		}
	}
	if slotEvent == nil {
		t.Fatalf("topics %v missing %s", topics, growth.TopicCompanionSkillSlotUnlocked)
	}
	if got := slotEvent.Payload["slots_after"]; got != 3 {
		t.Errorf("slots_after = %v, want 3 (SlotsForStage(2))", got)
	}
	if got := slotEvent.Payload["slots_before"]; got != 2 {
		t.Errorf("slots_before = %v, want 2 (SlotsForStage(1))", got)
	}
	if len(unlocker.calls) != 1 {
		t.Fatalf("unlocker calls = %d, want 1", len(unlocker.calls))
	}
	call := unlocker.calls[0]
	if call.CompanionID != "fam-1" || call.Species != "owl" || call.Stage != 2 {
		t.Errorf("unlocker call = %+v, want fam-1/owl/stage-2", call)
	}
}

// TestAwardExp_DuplicateReplayInvokesUnlocker — the redelivery path re-runs
// the (idempotent) unlocker WITHOUT re-emitting events, healing a crash
// between award-commit and unlock.
func TestAwardExp_DuplicateReplayInvokesUnlocker(t *testing.T) {
	unlocker := &fakeUnlocker{}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err != nil {
		t.Fatalf("first AwardExp: %v", err)
	}
	eventsBefore := len(ox.topics())
	unlockerBefore := len(unlocker.calls)

	resp, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err != nil {
		t.Fatalf("duplicate AwardExp: %v", err)
	}
	if !resp.Duplicate {
		t.Fatal("expected the duplicate replay")
	}
	if len(ox.topics()) != eventsBefore {
		t.Errorf("duplicate replay emitted events: %v", ox.topics()[eventsBefore:])
	}
	if len(unlocker.calls) != unlockerBefore+1 {
		t.Errorf("unlocker calls after duplicate = %d, want %d (self-healing re-run)", len(unlocker.calls), unlockerBefore+1)
	}
}

// TestAwardExp_UnlockerErrorFailsLoud — an unlock failure surfaces as an
// AwardExp error (Pub/Sub NACK → redelivery → duplicate path re-runs the
// unlocker). Never swallowed.
func TestAwardExp_UnlockerErrorFailsLoud(t *testing.T) {
	unlocker := &fakeUnlocker{err: errors.New("species_paths unavailable")}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err == nil {
		t.Fatal("AwardExp must fail loud when the Path unlocker errors")
	}
}

// TestHatchEgg_EmitsSlotUnlockedAndInvokesUnlocker — the 0→1 hatch
// transition also announces its slot growth (1→2) and runs the unlocker
// (a no-op at stage 1 band-wise, but uniform + band-tunable).
func TestHatchEgg_EmitsSlotUnlockedAndInvokesUnlocker(t *testing.T) {
	unlocker := &fakeUnlocker{}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	revealedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: 30, EggSku: "starter", // F-I1.3: stirring (>= threshold)
		// CHO-2229 commit-only hatch: the roll is already persisted.
		Species: "owl", SpeciesRarity: "common", RolledProb: 50, RevealedAt: &revealedAt,
	}

	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		DisplayName: "Blinky", Tone: "encouraging", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000009", Traceparent: "00-trace-01",
	})
	if err != nil {
		t.Fatalf("HatchEgg: %v", err)
	}
	found := false
	for _, topic := range ox.topics() {
		if topic == growth.TopicCompanionSkillSlotUnlocked {
			found = true
		}
	}
	if !found {
		t.Errorf("hatch topics %v missing skill_slot_unlocked", ox.topics())
	}
	if len(unlocker.calls) != 1 || unlocker.calls[0].Stage != 1 {
		t.Errorf("unlocker calls = %+v, want one call at stage 1", unlocker.calls)
	}
}
