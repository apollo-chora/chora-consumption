// companion_daily_dose_dispatch_rls_test.go — RLS-context propagation proof
// for the CHO-1577 N-Companion daily-dose dispatch path.
//
// Regression for the 2026-05-29 Phase 3 finding: the daily-dose handler
// invoked s.CompanionInstances.ListRosterByOwner with a bare r.Context().
// The pgx repo's rls.ApplySession reads tenant_id + gcid via
// tracing.{TenantID,GCID}FromContext and returns ErrNoTenantContext when
// they are empty — so against the deployed pg repo the call errored,
// rErr != nil silently skipped the entire dispatch block, and greeting_from
// was dropped from the response (observed live: /api/companion/daily-dose
// returned no greeting_from despite a 4-Companion hatched roster).
//
// Same bug class as companion_growth_handlers_rls_test.go (debt #40). Unit
// tests missed it because the inmem repo ignores the ctx. This test stubs a
// ctx-capturing InstanceRepository so the contract is asserted without a DB.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ctxCapturingInstanceRepo records the ctx passed to ListRosterByOwner and
// returns a single hatched Companion so SelectGreetingCompanion yields a winner.
type ctxCapturingInstanceRepo struct {
	companion.InstanceRepository // delegate non-overridden methods to inmem
	lastCtx                      context.Context
	roster                       []*companion.RosterEntry
}

func (c *ctxCapturingInstanceRepo) ListRosterByOwner(ctx context.Context, _, _ string) ([]*companion.RosterEntry, error) {
	c.lastCtx = ctx
	return c.roster, nil
}

// TestDailyDose_PropagatesRLSContextAndPopulatesGreetingFrom asserts both the
// RLS-context contract (ctx carries tenant+gcid for rls.ApplySession) and the
// observable CHO-1577 outcome (greeting_from present in the response).
func TestDailyDose_PropagatesRLSContextAndPopulatesGreetingFrom(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)

	eira, err := companion.NewInstance(testTenant, testGCID, "Eira", "cspo")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	eira.ConfiguredRules = map[string]string{"voice_accent": "warm"}
	hatched := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	instRepo := &ctxCapturingInstanceRepo{
		InstanceRepository: srv.CompanionInstances,
		roster: []*companion.RosterEntry{
			{
				Instance: eira,
				Growth: &companion.GrowthSnapshot{
					Stage:           2, // hatched ⇒ dispatch candidate
					Exp:             200,
					BreedRevealedAt: &hatched,
				},
			},
		},
	}
	srv.CompanionInstances = instRepo

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	// Contract 1: handler injected the RLS context before the repo call.
	if instRepo.lastCtx == nil {
		t.Fatal("ListRosterByOwner not invoked — dispatch block skipped")
	}
	if v := tracing.TenantIDFromContext(instRepo.lastCtx); v != testTenant {
		t.Errorf("tracing.TenantIDFromContext(ctx) = %q; want %q (RLS context missing — handler needs repoCtx wrap)", v, testTenant)
	}
	if v := tracing.GCIDFromContext(instRepo.lastCtx); v != testGCID {
		t.Errorf("tracing.GCIDFromContext(ctx) = %q; want %q (RLS context missing — handler needs repoCtx wrap)", v, testGCID)
	}

	// Contract 2: greeting_from is populated from the hatched roster.
	var resp dailyDoseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.GreetingFrom == nil {
		t.Fatal("greeting_from absent — CHO-1577 N-Companion dispatch dropped")
	}
	if resp.GreetingFrom.CompanionID != eira.CompanionID {
		t.Errorf("greeting_from.companion_id = %q; want %q", resp.GreetingFrom.CompanionID, eira.CompanionID)
	}
	if resp.GreetingFrom.Name != "Eira" {
		t.Errorf("greeting_from.name = %q; want Eira", resp.GreetingFrom.Name)
	}
}

// TestDailyDose_CompanionLevelReflectsGreetingCompanion asserts that when the
// GREETING Companion (selected by SelectGreetingCompanion) differs from the
// learner's first/legacy Companion, the response's companion_level reflects the
// GREETING Companion's ADR-149 GrowthStage — NOT the first Companion's legacy
// XP-level. Before the fix the dispatch overrode the NAME but left the level
// pinned to the first Companion, so the learner saw the greeter's name with the
// wrong (first Companion's) level.
func TestDailyDose_CompanionLevelReflectsGreetingCompanion(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)

	// Legacy first Companion at Level 3 — feeds the pre-dispatch companionLevel.
	first, err := companion.New(testTenant, testGCID, "Newton")
	if err != nil {
		t.Fatalf("companion.New: %v", err)
	}
	first.Level = 3
	srv.Companions.Save(first)

	// Greeting Companion (roster instance) at GrowthStage 5 — wins the dispatch
	// (single hatched candidate) and must drive companion_level.
	greeter, err := companion.NewInstance(testTenant, testGCID, "Aria", "history")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	hatched := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	srv.CompanionInstances = &ctxCapturingInstanceRepo{
		InstanceRepository: srv.CompanionInstances,
		roster: []*companion.RosterEntry{
			{
				Instance: greeter,
				Growth: &companion.GrowthSnapshot{
					Stage:           5, // hatched ⇒ dispatch candidate; != first level 3
					Exp:             1200,
					BreedRevealedAt: &hatched,
				},
			},
		},
	}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	// Sanity: the greeter actually won the dispatch (name overridden).
	if resp.GreetingFrom == nil || resp.GreetingFrom.CompanionID != greeter.CompanionID {
		t.Fatalf("greeting_from = %+v; want greeter %q", resp.GreetingFrom, greeter.CompanionID)
	}
	if resp.CompanionName != "Aria" {
		t.Errorf("companion_name = %q; want greeter Aria", resp.CompanionName)
	}
	// The fix: companion_level tracks the GREETING Companion's GrowthStage (5),
	// not the first Companion's legacy level (3).
	if resp.CompanionLevel != 5 {
		t.Errorf("companion_level = %d; want 5 (greeting Companion GrowthStage, not first Companion level 3)", resp.CompanionLevel)
	}
}

// TestDailyDose_CompanionQuoteNamesGreetingCompanion asserts that when the
// GREETING Companion differs from the learner's first/legacy Companion, the
// deterministic greeting PROSE (companion_quote) names the GREETING Companion —
// matching companion_name — NOT the first Companion. Before the fix the prose was
// composed early from the first Companion (companionName="Newton") and the
// dispatch block overrode only companion_name + companion_level, leaving the
// speech-bubble saying "Newton here…" while the chip showed "Aria / Stage 5".
// Sync engines default OFF (dailyDoseSyncEngines=false) so the deterministic
// greeting is the live path.
func TestDailyDose_CompanionQuoteNamesGreetingCompanion(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)

	// Legacy first Companion — feeds the EARLY deterministic greeting prose.
	first, err := companion.New(testTenant, testGCID, "Newton")
	if err != nil {
		t.Fatalf("companion.New: %v", err)
	}
	first.Level = 3
	srv.Companions.Save(first)

	// Greeting Companion (roster instance) — wins the dispatch (single hatched
	// candidate) and must drive BOTH the name/level chip AND the prose.
	greeter, err := companion.NewInstance(testTenant, testGCID, "Aria", "history")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	hatched := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	srv.CompanionInstances = &ctxCapturingInstanceRepo{
		InstanceRepository: srv.CompanionInstances,
		roster: []*companion.RosterEntry{
			{
				Instance: greeter,
				Growth: &companion.GrowthSnapshot{
					Stage:           5, // hatched ⇒ dispatch candidate
					Exp:             1200,
					BreedRevealedAt: &hatched,
				},
			},
		},
	}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	// Sanity: the greeter actually won the dispatch.
	if resp.GreetingFrom == nil || resp.GreetingFrom.CompanionID != greeter.CompanionID {
		t.Fatalf("greeting_from = %+v; want greeter %q", resp.GreetingFrom, greeter.CompanionID)
	}
	if resp.CompanionName != "Aria" {
		t.Fatalf("companion_name = %q; want greeter Aria", resp.CompanionName)
	}
	// The fix: the prose names the GREETING Companion (matches companion_name)…
	if !strings.Contains(resp.CompanionQuote, resp.CompanionName) {
		t.Errorf("companion_quote = %q; want it to name the greeting Companion %q", resp.CompanionQuote, resp.CompanionName)
	}
	// …and NOT the first/legacy Companion.
	if strings.Contains(resp.CompanionQuote, "Newton") {
		t.Errorf("companion_quote = %q; must NOT name the first Companion Newton (stale greeting prose)", resp.CompanionQuote)
	}
}
