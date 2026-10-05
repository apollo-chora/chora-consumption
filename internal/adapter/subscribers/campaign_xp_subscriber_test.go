// campaign_xp_subscriber_test.go — WS-C5 (CHO-2084, ADR-227 D10 + addendum
// #6) unit suite for the campaign conquest XP consumer. RED-first.
//
// Binding semantics pinned here:
//   - XP routes to the GOAL'S companion via ResolveForGoal (first real
//     caller of the port — addendum #6), never blindly to the global
//     active companion.
//   - rung_cleared with is_refresher=true awards the REDUCED source
//     campaign_rung_refreshed (D10 "defence/re-clears reduced" row).
//   - goal_sealed carries an additional ~1/week spacing guard per companion
//     (exp_source_def has daily caps only): a prior campaign_goal_sealed
//     award within SealMinSpacing skips the award (ack, no NACK loop).
//   - Idempotency is DB-ONLY (AwardExpTx UNIQUE(companion_id, source,
//     idempotency_key)): a redelivery re-runs the award with the SAME key
//     and lands on the conflict — there is deliberately NO
//     mark-before-process tracker (the WS-C5 live smoke proved that
//     pattern's loss window: mark → transient award failure → NACK → retry
//     sees "seen" → silent ack → XP gone). Transient failures NACK and the
//     retry MUST still award. Missing envelope fields fail loud (NACK→DLQ).
//   - Bare PersonalCompletedAt never awards: no personal-completion source
//     exists in the canonical vocabulary — the ONLY seal-shaped source is
//     campaign_goal_sealed, fed exclusively by the domain-verified
//     goal_sealed.v1 (frontier-clear enforced in domain/campaign).
package subscribers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// --- fakes -----------------------------------------------------------------

type cxpAwarder struct {
	mu    sync.Mutex
	calls []growth.AwardExpInput
	err   error
	failN int // fail the next N calls (transient-failure simulation)
}

func (f *cxpAwarder) AwardExp(_ context.Context, in growth.AwardExpInput) (*growth.AwardExpResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.failN > 0 {
		f.failN--
		return nil, errors.New("transient: db unavailable")
	}
	f.calls = append(f.calls, in)
	return &growth.AwardExpResponse{}, nil
}

func (f *cxpAwarder) awards() []growth.AwardExpInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]growth.AwardExpInput, len(f.calls))
	copy(out, f.calls)
	return out
}

type cxpResolver struct {
	byGoal map[string]string // goalID → companionID
	err    error
	calls  int
}

func (f *cxpResolver) ResolveForGoal(_ context.Context, _, _, goalID string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if id, ok := f.byGoal[goalID]; ok {
		return id, nil
	}
	return "", ErrNoCompanion
}

type cxpHistory struct {
	last     time.Time
	err      error
	gotTen   string
	gotFam   string
	gotSrc   string
	numCalls int
}

func (f *cxpHistory) LastAwardAt(_ context.Context, tenantID, companionID, source string) (time.Time, error) {
	f.numCalls++
	f.gotTen, f.gotFam, f.gotSrc = tenantID, companionID, source
	if f.err != nil {
		return time.Time{}, f.err
	}
	return f.last, nil
}

// --- helpers ---------------------------------------------------------------

const (
	cxpTenant = "11111111-1111-7111-8111-111111111111"
	cxpGCID   = "00000000-0000-7000-8000-000000001999"
	cxpGoal   = "01980000-0000-7000-8000-00000000a001"
	cxpFam    = "01980000-0000-7000-8000-00000000f001"
)

func cxpFixedNow() time.Time {
	return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
}

func newCXPForTest(a *cxpAwarder, r *cxpResolver, h *cxpHistory) *CampaignXPSubscriber {
	// A lineage with no tombstoned history (0, false) — the WS-C5 behaviours
	// pinned by this file are lineage-neutral; the D14 guard has its own
	// suite (campaign_xp_lineage_guard_test.go).
	return NewCampaignXPSubscriber(a, r, h, &cxpLineage{}, DefaultSealXPMinSpacing, cxpFixedNow)
}

func cxpRungPayload() CampaignRungClearedPayload {
	return CampaignRungClearedPayload{
		TenantID:       cxpTenant,
		LearnerGCID:    cxpGCID,
		GoalID:         cxpGoal,
		ConceptID:      "c-node-1",
		ConceptKey:     "photosynthesis",
		Rung:           3,
		IsRefresher:    false,
		CorrectAnswers: 2,
		EventID:        "01980000-0000-7000-8000-00000000e101",
		Traceparent:    "00-11111111111111111111111111111111-2222222222222222-01",
		Tracestate:     "chora=1",
	}
}

func cxpWonPayload() CampaignNodeWonPayload {
	return CampaignNodeWonPayload{
		TenantID:    cxpTenant,
		LearnerGCID: cxpGCID,
		GoalID:      cxpGoal,
		ConceptID:   "c-node-1",
		ConceptKey:  "photosynthesis",
		EventID:     "01980000-0000-7000-8000-00000000e201",
		Traceparent: "00-11111111111111111111111111111111-2222222222222222-01",
	}
}

func cxpSealedPayload() CampaignGoalSealedPayload {
	return CampaignGoalSealedPayload{
		TenantID:       cxpTenant,
		LearnerGCID:    cxpGCID,
		GoalID:         cxpGoal,
		RootConceptID:  "c-root",
		RootConceptKey: "psle-science",
		NodesWon:       7,
		EventID:        "01980000-0000-7000-8000-00000000e301",
		Traceparent:    "00-11111111111111111111111111111111-2222222222222222-01",
	}
}

// --- rung_cleared ------------------------------------------------------------

func TestCampaignXPRungCleared_RoutesToGoalCompanion(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	p := cxpRungPayload()
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("HandleRungCleared: %v", err)
	}
	got := a.awards()
	if len(got) != 1 {
		t.Fatalf("awards = %d, want 1", len(got))
	}
	in := got[0]
	if in.CompanionID != cxpFam {
		t.Errorf("CompanionID = %s, want the GOAL'S companion %s (addendum #6 ResolveForGoal)", in.CompanionID, cxpFam)
	}
	if in.Source != "campaign_rung_cleared" {
		t.Errorf("Source = %q, want campaign_rung_cleared", in.Source)
	}
	if in.RequestedDelta != 0 {
		t.Errorf("RequestedDelta = %d, want 0 (resolver-priced, ADR-218 D6)", in.RequestedDelta)
	}
	if in.IdempotencyKey != p.EventID || in.SourceEventID != p.EventID {
		t.Errorf("idempotency anchors = (%s, %s), want the upstream event id %s", in.IdempotencyKey, in.SourceEventID, p.EventID)
	}
	if in.SourceTopic != events.TopicCampaignRungCleared {
		t.Errorf("SourceTopic = %q, want %q", in.SourceTopic, events.TopicCampaignRungCleared)
	}
	if in.TenantID != cxpTenant || in.OwnerGCID != cxpGCID {
		t.Errorf("tenant/owner = (%s, %s), want payload values", in.TenantID, in.OwnerGCID)
	}
	if in.Traceparent != p.Traceparent || in.Tracestate != p.Tracestate {
		t.Errorf("trace ctx not propagated: (%q, %q)", in.Traceparent, in.Tracestate)
	}
}

func TestCampaignXPRungCleared_RefresherUsesReducedSource(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	p := cxpRungPayload()
	p.IsRefresher = true
	p.EventID = "01980000-0000-7000-8000-00000000e102"
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("HandleRungCleared(refresher): %v", err)
	}
	got := a.awards()
	if len(got) != 1 {
		t.Fatalf("awards = %d, want 1", len(got))
	}
	if got[0].Source != "campaign_rung_refreshed" {
		t.Errorf("Source = %q, want campaign_rung_refreshed (D10 reduced re-clear)", got[0].Source)
	}
}

// Redelivery re-runs the award with the SAME idempotency key — the DB
// UNIQUE(companion_id, source, idempotency_key) is the sole dedup anchor
// (the second insert lands ON CONFLICT DO NOTHING → Duplicate=true → ack).
func TestCampaignXPRungCleared_RedeliveryReplaysSameIdempotencyKey(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	p := cxpRungPayload()
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	got := a.awards()
	if len(got) != 2 {
		t.Fatalf("award invocations = %d, want 2 (re-run; DB dedupes)", len(got))
	}
	if got[0].IdempotencyKey != p.EventID || got[1].IdempotencyKey != p.EventID ||
		got[0].Source != got[1].Source || got[0].CompanionID != got[1].CompanionID {
		t.Errorf("redelivery must replay the IDENTICAL (companion, source, key) so the DB conflict dedupes: %+v vs %+v", got[0], got[1])
	}
}

// The live-found WS-C5 regression: a transient award failure NACKs and the
// retry MUST still award — no mark-before-process state may swallow it.
func TestCampaignXPRungCleared_TransientFailureThenRetryAwards(t *testing.T) {
	a := &cxpAwarder{failN: 1}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	p := cxpRungPayload()
	if err := s.HandleRungCleared(context.Background(), p); err == nil {
		t.Fatal("first delivery: want transient error to NACK")
	}
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	if n := len(a.awards()); n != 1 {
		t.Errorf("successful awards = %d, want 1 — the retry must not be silently dropped (loss-window regression)", n)
	}
}

func TestCampaignXPRungCleared_NoCompanionDrops(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{} // no goal binding, no roster → ErrNoCompanion
	s := newCXPForTest(a, r, &cxpHistory{})

	if err := s.HandleRungCleared(context.Background(), cxpRungPayload()); err != nil {
		t.Fatalf("want silent drop on ErrNoCompanion, got %v", err)
	}
	if n := len(a.awards()); n != 0 {
		t.Errorf("awards = %d, want 0", n)
	}
}

func TestCampaignXPRungCleared_MissingFieldsError(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CampaignRungClearedPayload)
	}{
		{"missing_event_id", func(p *CampaignRungClearedPayload) { p.EventID = "" }},
		{"missing_tenant", func(p *CampaignRungClearedPayload) { p.TenantID = "" }},
		{"missing_gcid", func(p *CampaignRungClearedPayload) { p.LearnerGCID = "" }},
		{"missing_goal", func(p *CampaignRungClearedPayload) { p.GoalID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &cxpAwarder{}
			r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
			s := newCXPForTest(a, r, &cxpHistory{})
			p := cxpRungPayload()
			tc.mutate(&p)
			if err := s.HandleRungCleared(context.Background(), p); err == nil {
				t.Error("want fail-loud error (NACK→DLQ) on malformed payload")
			}
			if n := len(a.awards()); n != 0 {
				t.Errorf("awards = %d, want 0", n)
			}
		})
	}
}

func TestCampaignXPRungCleared_ResolverErrorNACKs(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{err: errors.New("pg down")}
	s := newCXPForTest(a, r, &cxpHistory{})

	if err := s.HandleRungCleared(context.Background(), cxpRungPayload()); err == nil {
		t.Error("want resolver error to propagate (NACK → redelivery)")
	}
}

func TestCampaignXPRungCleared_AwardErrorNACKs(t *testing.T) {
	a := &cxpAwarder{err: errors.New("tx failed")}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	if err := s.HandleRungCleared(context.Background(), cxpRungPayload()); err == nil {
		t.Error("want award error to propagate (NACK → redelivery)")
	}
}

// --- node_won ----------------------------------------------------------------

func TestCampaignXPNodeWon_AwardsMediumBonus(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	p := cxpWonPayload()
	if err := s.HandleNodeWon(context.Background(), p); err != nil {
		t.Fatalf("HandleNodeWon: %v", err)
	}
	got := a.awards()
	if len(got) != 1 {
		t.Fatalf("awards = %d, want 1", len(got))
	}
	in := got[0]
	if in.Source != "campaign_node_won" {
		t.Errorf("Source = %q, want campaign_node_won", in.Source)
	}
	if in.CompanionID != cxpFam {
		t.Errorf("CompanionID = %s, want goal companion %s", in.CompanionID, cxpFam)
	}
	if in.SourceTopic != events.TopicCampaignNodeWon {
		t.Errorf("SourceTopic = %q, want %q", in.SourceTopic, events.TopicCampaignNodeWon)
	}
	if in.IdempotencyKey != p.EventID {
		t.Errorf("IdempotencyKey = %s, want %s", in.IdempotencyKey, p.EventID)
	}
}

func TestCampaignXPNodeWon_RedeliveryReplaysSameIdempotencyKey(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	s := newCXPForTest(a, r, &cxpHistory{})

	p := cxpWonPayload()
	if err := s.HandleNodeWon(context.Background(), p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := s.HandleNodeWon(context.Background(), p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	got := a.awards()
	if len(got) != 2 || got[0].IdempotencyKey != p.EventID || got[1].IdempotencyKey != p.EventID {
		t.Errorf("want 2 identical-key award invocations (DB dedupes), got %d", len(got))
	}
}

// --- goal_sealed ---------------------------------------------------------------

func TestCampaignXPGoalSealed_AwardsWhenNoPriorSeal(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	h := &cxpHistory{} // zero time = never awarded
	s := newCXPForTest(a, r, h)

	p := cxpSealedPayload()
	if err := s.HandleGoalSealed(context.Background(), p); err != nil {
		t.Fatalf("HandleGoalSealed: %v", err)
	}
	got := a.awards()
	if len(got) != 1 {
		t.Fatalf("awards = %d, want 1", len(got))
	}
	in := got[0]
	if in.Source != "campaign_goal_sealed" {
		t.Errorf("Source = %q, want campaign_goal_sealed", in.Source)
	}
	if in.SourceTopic != events.TopicCampaignGoalSealed {
		t.Errorf("SourceTopic = %q, want %q", in.SourceTopic, events.TopicCampaignGoalSealed)
	}
	if h.numCalls != 1 || h.gotTen != cxpTenant || h.gotFam != cxpFam || h.gotSrc != "campaign_goal_sealed" {
		t.Errorf("history consulted = %d×(%s,%s,%s), want 1×(tenant,goal-companion,campaign_goal_sealed)",
			h.numCalls, h.gotTen, h.gotFam, h.gotSrc)
	}
}

func TestCampaignXPGoalSealed_SkipsWithinMinSpacing(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	h := &cxpHistory{last: cxpFixedNow().Add(-72 * time.Hour)} // sealed 3d ago
	s := newCXPForTest(a, r, h)

	if err := s.HandleGoalSealed(context.Background(), cxpSealedPayload()); err != nil {
		t.Fatalf("want ack (skip, no NACK loop) inside spacing window, got %v", err)
	}
	if n := len(a.awards()); n != 0 {
		t.Errorf("awards = %d, want 0 (tier-S ~1/week spacing, ADR-227 D10)", n)
	}
}

func TestCampaignXPGoalSealed_AwardsPastMinSpacing(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	h := &cxpHistory{last: cxpFixedNow().Add(-DefaultSealXPMinSpacing - time.Hour)}
	s := newCXPForTest(a, r, h)

	if err := s.HandleGoalSealed(context.Background(), cxpSealedPayload()); err != nil {
		t.Fatalf("HandleGoalSealed: %v", err)
	}
	if n := len(a.awards()); n != 1 {
		t.Errorf("awards = %d, want 1 (spacing satisfied)", n)
	}
}

func TestCampaignXPGoalSealed_HistoryErrorNACKs(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	h := &cxpHistory{err: errors.New("pg down")}
	s := newCXPForTest(a, r, h)

	if err := s.HandleGoalSealed(context.Background(), cxpSealedPayload()); err == nil {
		t.Error("want history error to propagate (fail-loud NACK), never award-anyway")
	}
	if n := len(a.awards()); n != 0 {
		t.Errorf("awards = %d, want 0", n)
	}
}

// --- construction + invariants --------------------------------------------------

func TestNewCampaignXPSubscriber_PanicsOnNilDeps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want panic on nil deps (feedback_no_stubs_real_wiring)")
		}
	}()
	NewCampaignXPSubscriber(nil, nil, nil, nil, 0, nil)
}

func TestNewCampaignXPSubscriber_DefaultsSpacing(t *testing.T) {
	// Zero spacing + nil clock → the exported 7-day default applies. A seal
	// 100h after the previous one must therefore skip.
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	h := &cxpHistory{last: time.Now().Add(-100 * time.Hour)}
	s := NewCampaignXPSubscriber(a, r, h, &cxpLineage{}, 0, nil)

	if err := s.HandleGoalSealed(context.Background(), cxpSealedPayload()); err != nil {
		t.Fatalf("HandleGoalSealed: %v", err)
	}
	if n := len(a.awards()); n != 0 {
		t.Errorf("awards = %d, want 0 (default spacing %v applies)", n, DefaultSealXPMinSpacing)
	}
}

// TestNoBarePersonalCompletionSource pins CHO-2084 AC-1 (ADR-227 D10): the
// learner's sovereign PersonalCompletedAt NEVER awards XP. There is no
// personal-completion token in the canonical vocabulary — the only
// seal-shaped source is campaign_goal_sealed, and its sole feeder is the
// domain-verified goal_sealed.v1 (frontier-clear enforced by
// domain/campaign EvaluateSeal before emission).
func TestNoBarePersonalCompletionSource(t *testing.T) {
	for _, forbidden := range []string{
		"personal_completed", "goal_personal_completed", "goal_completed", "personal_completion",
	} {
		if growth.IsValidSource(forbidden) {
			t.Errorf("forbidden self-declared source %q is canonical — bare PersonalCompletedAt must never award", forbidden)
		}
	}
	if !growth.IsValidSource("campaign_goal_sealed") {
		t.Error("campaign_goal_sealed missing from canonical sources (WS-C5 vocabulary)")
	}
}
