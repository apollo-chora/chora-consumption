// exp_rules_test.go — ADR-218 D6: the EXP economy unifies on identity's
// ExpRuleResolver; the in-code curve.go map becomes the DOCUMENTED FALLBACK.
// The parity fixture below is the behaviour-neutrality gate: it mirrors the
// identity migration 0030 seed literals, and this suite pins curve.go to the
// same values. Identity pins ITS copy in
// services/chora-identity/internal/adapter/pg (migration-content test).
// Either side drifting fails its own suite. RED-first for CHO-2012 P0.
package growth

import (
	"context"
	"errors"
	"testing"
)

// identityParitySeed mirrors chora-identity migration
// 0030_familiar_exp_live_source_parity (exp_source_def rows for the LIVE
// vocabulary). DO NOT EDIT one side alone — ADR-218 D6 parity contract.
var identityParitySeed = map[string]ExpRule{
	"atom_session":      {Value: 3, DailyCap: 30, Enabled: true},
	"ebbinghaus_review": {Value: 5, DailyCap: 15, Enabled: true},
	"hex_expand":        {Value: 4, DailyCap: 12, Enabled: true},
	"conv_turn":         {Value: 2, DailyCap: 10, Enabled: true},
	"daily_dose_open":   {Value: 2, DailyCap: 2, Enabled: true},
	"social_share":      {Value: 8, DailyCap: 16, Enabled: true},
	"social_reaction":   {Value: 1, DailyCap: 10, Enabled: true},
	"junction_accepted": {Value: 15, DailyCap: 15, Enabled: true},
	"atom_authored":     {Value: 20, DailyCap: 40, Enabled: true},
	"admin_grant":       {Value: 0, DailyCap: 0, Enabled: true},
	"hatch_roll":        {Value: 0, DailyCap: 0, Enabled: true},

	// ADR-227 D10 / WS-C5 (CHO-2084): campaign conquest sources — mirrors
	// identity migration 0036_familiar_exp_campaign_sources. The seal's
	// additional ~1/week spacing is a code semantic in the campaign XP
	// subscriber (exp_source_def carries daily caps only).
	"campaign_rung_cleared":   {Value: 8, DailyCap: 32, Enabled: true},
	"campaign_rung_refreshed": {Value: 3, DailyCap: 12, Enabled: true},
	"campaign_node_won":       {Value: 25, DailyCap: 75, Enabled: true},
	"campaign_goal_sealed":    {Value: 120, DailyCap: 120, Enabled: true},

	// ADR-228 D4 Wave 1 / F-I3 (CHO-2090): "the power of a little bit" —
	// mirrors identity migration 0037_familiar_exp_wave1_sources. Flat
	// per-event pricing (L16: verified events only, no difficulty scaling).
	"weakness_grown":    {Value: 10, DailyCap: 30, Enabled: true},
	"submission_graded": {Value: 30, DailyCap: 60, Enabled: true},
	"module_completed":  {Value: 15, DailyCap: 45, Enabled: true},
}

// TestExpParityFixtureCoversEveryCanonicalSource — the parity seed must
// cover the whole live vocabulary, no more, no less.
func TestExpParityFixtureCoversEveryCanonicalSource(t *testing.T) {
	for source := range identityParitySeed {
		if !IsValidSource(source) {
			t.Errorf("parity fixture has %q which is not a canonical curve.go source", source)
		}
	}
	for source := range canonicalSources {
		if _, ok := identityParitySeed[source]; !ok {
			t.Errorf("canonical source %q missing from the identity parity fixture", source)
		}
	}
}

// TestFallbackExpRuleMatchesParitySeed — the documented fallback (in-code
// map) must return EXACTLY the identity seed values for every source. This
// is the D6 behaviour-neutrality gate on the consumption side.
func TestFallbackExpRuleMatchesParitySeed(t *testing.T) {
	for source, want := range identityParitySeed {
		got, err := FallbackExpRule(source)
		if err != nil {
			t.Errorf("FallbackExpRule(%q) unexpected error: %v", source, err)
			continue
		}
		if got != want {
			t.Errorf("FallbackExpRule(%q) = %+v, want %+v (parity with identity 0030)", source, got, want)
		}
	}
}

// TestFallbackExpRuleUnknownSource — fail-loud on a non-canonical source.
func TestFallbackExpRuleUnknownSource(t *testing.T) {
	if _, err := FallbackExpRule("i_promise_i_learned"); err == nil {
		t.Fatal("FallbackExpRule with unknown source must error (verified-EXP invariant L16)")
	}
}

// staticExpRuler is a test double for the ExpRuler port.
type staticExpRuler struct {
	rule ExpRule
	err  error
	// calls records the (tenantID, source) pairs seen.
	calls []string
}

func (s *staticExpRuler) ResolveExpRule(_ context.Context, tenantID, source string) (ExpRule, error) {
	s.calls = append(s.calls, tenantID+"|"+source)
	if s.err != nil {
		return ExpRule{}, s.err
	}
	return s.rule, nil
}

// TestResolveExpRuleOrFallbackPrefersResolver — when the resolver answers,
// its rule wins and no fallback fires.
func TestResolveExpRuleOrFallbackPrefersResolver(t *testing.T) {
	ruler := &staticExpRuler{rule: ExpRule{Value: 99, DailyCap: 123, Enabled: true}}
	fallbackFired := false
	got := resolveExpRuleOrFallback(context.Background(), ruler, "tenant-1", "atom_session",
		func(source string, err error) { fallbackFired = true })
	if got.Value != 99 || got.DailyCap != 123 || !got.Enabled {
		t.Errorf("resolved rule = %+v, want the resolver's {99 123 true}", got)
	}
	if fallbackFired {
		t.Error("fallback hook fired although the resolver answered")
	}
	if len(ruler.calls) != 1 || ruler.calls[0] != "tenant-1|atom_session" {
		t.Errorf("resolver calls = %v, want exactly [tenant-1|atom_session]", ruler.calls)
	}
}

// TestResolveExpRuleOrFallbackOnError — a resolver error falls back to the
// in-code parity map AND fires the loud-fallback hook (ADR-218 D6 documented
// fallback: never silent).
func TestResolveExpRuleOrFallbackOnError(t *testing.T) {
	ruler := &staticExpRuler{err: errors.New("identity unreachable")}
	var hookSource string
	var hookErr error
	got := resolveExpRuleOrFallback(context.Background(), ruler, "tenant-1", "social_share",
		func(source string, err error) { hookSource, hookErr = source, err })
	want := identityParitySeed["social_share"]
	if got != want {
		t.Errorf("fallback rule = %+v, want parity %+v", got, want)
	}
	if hookSource != "social_share" || hookErr == nil {
		t.Errorf("fallback hook = (%q, %v), want (social_share, non-nil error)", hookSource, hookErr)
	}
}

// TestResolveExpRuleOrFallbackNilRuler — no resolver wired (build-out /
// tests) uses the fallback map WITHOUT firing the hook (nothing failed —
// the resolver is simply not configured yet).
func TestResolveExpRuleOrFallbackNilRuler(t *testing.T) {
	fired := false
	got := resolveExpRuleOrFallback(context.Background(), nil, "tenant-1", "hex_expand",
		func(string, error) { fired = true })
	if want := identityParitySeed["hex_expand"]; got != want {
		t.Errorf("nil-ruler rule = %+v, want parity %+v", got, want)
	}
	if fired {
		t.Error("fallback hook fired for a nil ruler — reserved for resolver FAILURES")
	}
}
