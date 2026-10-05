// Package companion — Mana-metered LLM greeting hook tests (S5.1).
//
// Per ADR-142 §4 the LLM-personalised greeting on the Companion daily-dose
// surface is a medium-tier action costing 50 mana. The greeting hook is
// scaffolded with hexagonal ports so the production wiring (Model Broker
// Gateway via chora-ai-kernel-orchestrator) lands cleanly later.
//
// Behaviour:
//
//   - No greeting port → return DefaultGreeting (deterministic, no mana spent).
//   - Greeting port present + no quoter → fail-open, call port directly.
//   - Greeting port + quoter, sufficient mana → debit, call port.
//   - Greeting port + quoter, insufficient mana → DefaultGreeting (NOT
//     ErrInsufficientMana — greeting is non-critical, gracefully degrade).
//   - Greeting port returns error → DefaultGreeting (fail-soft).
//
// Determinism: the deterministic fallback string is built from companion
// name + level so the same inputs yield the same string (test-friendly).
//
// TDD strict (RED → GREEN → REFACTOR).
package companion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGreetingPort captures inputs + returns canned greeting.
type fakeGreetingPort struct {
	calls []GreetingRequest
	resp  string
	err   error
}

func (f *fakeGreetingPort) Greet(_ context.Context, req GreetingRequest) (string, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return "", f.err
	}
	return f.resp, nil
}

// fakeGreetingQuoter is a local quoter that returns a queued sequence of
// errors. Distinct from the package-shared fakeManaQuoter (single err
// field) so greeting tests can swap behaviour cleanly.
type fakeGreetingQuoter struct {
	deductCalls []DeductManaInput
	errs        []error
}

func (f *fakeGreetingQuoter) DeductMana(_ context.Context, in DeductManaInput) error {
	f.deductCalls = append(f.deductCalls, in)
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakeGreetingQuoter) GetBalance(_ context.Context, _ string) (BalanceSnapshot, error) {
	return BalanceSnapshot{}, nil
}

// TestGreeting_NoPortReturnsDefault — when no greeting port is wired,
// caller gets a deterministic fallback. No mana spent.
func TestGreeting_NoPortReturnsDefault(t *testing.T) {
	quoter := &fakeGreetingQuoter{}
	greeting, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "Eira",
		Level:         3,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, nil, quoter, "idem-key-1")
	if err != nil {
		t.Fatalf("PersonalisedGreeting err = %v", err)
	}
	if !strings.Contains(greeting, "Eira") {
		t.Errorf("greeting = %q, want to contain companion name 'Eira'", greeting)
	}
	if len(quoter.deductCalls) != 0 {
		t.Errorf("quoter should NOT be called when port is nil; got %d calls", len(quoter.deductCalls))
	}
}

// TestGreeting_WithPort_DebitsManaThenCalls — greeting port + quoter
// both wired: mana debited BEFORE the port is called.
func TestGreeting_WithPort_DebitsManaThenCalls(t *testing.T) {
	port := &fakeGreetingPort{resp: "Hello adventurer! Ready for today?"}
	quoter := &fakeGreetingQuoter{}
	greeting, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "Eira",
		Level:         5,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, port, quoter, "idem-key-2")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if greeting != "Hello adventurer! Ready for today?" {
		t.Errorf("greeting = %q, want LLM response", greeting)
	}
	if len(quoter.deductCalls) != 1 {
		t.Fatalf("expected 1 quoter call; got %d", len(quoter.deductCalls))
	}
	if quoter.deductCalls[0].ActionCode != ActionCompanionGreeting {
		t.Errorf("ActionCode = %q, want %q", quoter.deductCalls[0].ActionCode, ActionCompanionGreeting)
	}
	if len(port.calls) != 1 {
		t.Errorf("expected 1 port call; got %d", len(port.calls))
	}
}

// TestGreeting_InsufficientMana_FallsBackToDefault — when the learner is
// short on mana, the greeting port is NOT called and the deterministic
// fallback is returned (NOT an ErrInsufficientMana). Greeting is a
// non-critical surface — gracefully degrade.
func TestGreeting_InsufficientMana_FallsBackToDefault(t *testing.T) {
	port := &fakeGreetingPort{resp: "should not be called"}
	quoter := &fakeGreetingQuoter{
		errs: []error{&ErrInsufficientMana{RequiredUnits: 50, CurrentBalance: 5}},
	}
	greeting, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "Maya",
		Level:         2,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, port, quoter, "idem-key-3")
	if err != nil {
		t.Errorf("expected nil error (greeting is non-critical); got %v", err)
	}
	if !strings.Contains(greeting, "Maya") {
		t.Errorf("greeting = %q, want to contain 'Maya' (default fallback)", greeting)
	}
	if len(port.calls) != 0 {
		t.Errorf("port should NOT be called on insufficient mana; got %d calls", len(port.calls))
	}
}

// TestGreeting_PortError_FallsBackToDefault — if the port returns a
// non-mana error, fall back to default. (Greeting is non-critical.)
func TestGreeting_PortError_FallsBackToDefault(t *testing.T) {
	port := &fakeGreetingPort{err: errors.New("upstream down")}
	quoter := &fakeGreetingQuoter{}
	greeting, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "Eira",
		Level:         4,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, port, quoter, "idem-key-4")
	if err != nil {
		t.Errorf("expected nil error (greeting is non-critical); got %v", err)
	}
	if !strings.Contains(greeting, "Eira") {
		t.Errorf("greeting = %q, want default fallback containing name", greeting)
	}
}

// TestGreeting_DefaultIsDeterministic — same inputs produce same default
// greeting (no time-based randomness in the fallback path).
func TestGreeting_DefaultIsDeterministic(t *testing.T) {
	in := GreetingInput{
		CompanionName: "Eira",
		Level:         7,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}
	g1, _ := PersonalisedGreeting(context.Background(), in, nil, nil, "idem-key-5")
	g2, _ := PersonalisedGreeting(context.Background(), in, nil, nil, "idem-key-5")
	if g1 != g2 {
		t.Errorf("default greeting non-deterministic: %q vs %q", g1, g2)
	}
}

// TestGreeting_PortCalledWithCompanionContext — the port gets the level +
// name + GCID so the AI agent can synthesise persona-aware text.
func TestGreeting_PortCalledWithCompanionContext(t *testing.T) {
	port := &fakeGreetingPort{resp: "ok"}
	quoter := &fakeGreetingQuoter{}
	in := GreetingInput{
		CompanionName:   "Maya",
		Level:           12,
		LearnerGCID:     "learner-marcus",
		TenantID:        "tenant-acme",
		ActivePathTitle: "CSPO Foundation",
	}
	if _, err := PersonalisedGreeting(context.Background(), in, port, quoter, "idem-key-6"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(port.calls) != 1 {
		t.Fatalf("expected 1 port call; got %d", len(port.calls))
	}
	got := port.calls[0]
	if got.CompanionName != "Maya" {
		t.Errorf("CompanionName = %q, want Maya", got.CompanionName)
	}
	if got.Level != 12 {
		t.Errorf("Level = %d, want 12", got.Level)
	}
	if got.LearnerGCID != "learner-marcus" {
		t.Errorf("LearnerGCID = %q, want learner-marcus", got.LearnerGCID)
	}
	if got.ActivePathTitle != "CSPO Foundation" {
		t.Errorf("ActivePathTitle = %q, want CSPO Foundation", got.ActivePathTitle)
	}
}

// TestGreeting_ActionCodeConst — the mana action code is constant +
// matches ADR-142 §4.
func TestGreeting_ActionCodeConst(t *testing.T) {
	if ActionCompanionGreeting != "companion_greeting" {
		t.Errorf("ActionCompanionGreeting = %q, want 'companion_greeting' (ADR-142 §4)",
			ActionCompanionGreeting)
	}
}

// TestDefaultGreeting_EmptyNameSubstitutes — defensive: if surface
// passes empty name, default uses "Companion".
func TestDefaultGreeting_EmptyNameSubstitutes(t *testing.T) {
	g := DefaultGreeting(GreetingInput{CompanionName: "", Level: 1})
	if !strings.Contains(g, "Companion") {
		t.Errorf("default greeting = %q, want to contain 'Companion' fallback", g)
	}
}

// TestDefaultGreeting_LevelZeroOmitsLevelString — level<=0 branch
// returns a greeting WITHOUT the "(level N)" segment.
func TestDefaultGreeting_LevelZeroOmitsLevelString(t *testing.T) {
	g := DefaultGreeting(GreetingInput{CompanionName: "Maya", Level: 0})
	if strings.Contains(g, "level 0") {
		t.Errorf("default greeting at level 0 = %q, must not include 'level 0'", g)
	}
	g2 := DefaultGreeting(GreetingInput{CompanionName: "Maya", Level: -1})
	if strings.Contains(g2, "-1") {
		t.Errorf("default greeting at negative level = %q, must not echo level", g2)
	}
}

// TestPersonalisedGreeting_EmptyName_StillReturnsString — defensive: if
// the surface passes an empty name, the composer substitutes
// "Companion" + still returns a non-empty string.
func TestPersonalisedGreeting_EmptyName_StillReturnsString(t *testing.T) {
	g, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "",
		Level:         2,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, nil, nil, "idem")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if g == "" {
		t.Error("greeting empty, want non-empty fallback")
	}
}

// TestPersonalisedGreeting_NonManaError_FallsBack — when the quoter
// returns a non-mana error (e.g., service unreachable), the composer
// falls open to default greeting (no port call, no 5xx).
func TestPersonalisedGreeting_NonManaError_FallsBack(t *testing.T) {
	port := &fakeGreetingPort{resp: "should not be called"}
	quoter := &fakeGreetingQuoter{
		errs: []error{errors.New("identity unreachable")},
	}
	g, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "Eira",
		Level:         3,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, port, quoter, "idem")
	if err != nil {
		t.Errorf("expected nil err on non-mana error (fail-open); got %v", err)
	}
	if g == "" {
		t.Error("greeting empty on quoter error")
	}
	if len(port.calls) != 0 {
		t.Errorf("port should NOT be called when quoter errored; got %d calls", len(port.calls))
	}
}

// TestPersonalisedGreeting_PortReturnsEmpty_FallsBack — defensive: if
// the port returns success with empty string, fall back to default.
func TestPersonalisedGreeting_PortReturnsEmpty_FallsBack(t *testing.T) {
	port := &fakeGreetingPort{resp: ""}
	quoter := &fakeGreetingQuoter{}
	g, err := PersonalisedGreeting(context.Background(), GreetingInput{
		CompanionName: "Eira",
		Level:         3,
		LearnerGCID:   "learner",
		TenantID:      "tenant",
	}, port, quoter, "idem")
	if err != nil {
		t.Errorf("err = %v", err)
	}
	if g == "" || !strings.Contains(g, "Eira") {
		t.Errorf("greeting = %q, want default fallback containing name", g)
	}
}
