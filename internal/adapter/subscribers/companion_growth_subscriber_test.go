package subscribers_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ----- fakes -----

type fakeAwardPort struct {
	mu    sync.Mutex
	calls []growth.AwardExpInput
	err   error
}

func (f *fakeAwardPort) AwardExp(_ context.Context, in growth.AwardExpInput) (*growth.AwardExpResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, in)
	return &growth.AwardExpResponse{
		ClampedDelta: in.RequestedDelta,
	}, nil
}

func (f *fakeAwardPort) Calls() []growth.AwardExpInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]growth.AwardExpInput, len(f.calls))
	copy(out, f.calls)
	return out
}

type fakeResolver struct {
	companionID string
	err         error
}

func (r *fakeResolver) ResolveActiveCompanion(_ context.Context, _, _ string) (string, error) {
	return r.companionID, r.err
}

type fakeProvisionPort struct {
	mu    sync.Mutex
	calls []growth.ProvisionEggInput
}

func (f *fakeProvisionPort) ProvisionEgg(_ context.Context, in growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return &growth.CompanionGrowthRow{CompanionID: "fam-" + in.EggPurchaseID}, nil
}

// ----- helpers -----

func validEnv() events.Envelope {
	return events.Envelope{
		EventID:        "evt-1",
		IdempotencyKey: "idem-1",
		TenantID:       "tenant-1",
		GCID:           "user-1",
		OccurredAt:     time.Now(),
		PublishedAt:    time.Now(),
		Traceparent:    "00-aaa-bbb-00",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

// ----- tests -----

func TestSubscriber_AtomSessionCompleted_Correct(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{companionID: "fam-1"}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)

	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{
			AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true,
		})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	calls := ap.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].RequestedDelta != 0 {
		t.Errorf("delta = %d, want 0 — resolved default, CHO-2012 ADR-218 D6 (parity 3)", calls[0].RequestedDelta)
	}
	if calls[0].Source != "atom_session" {
		t.Errorf("source = %q", calls[0].Source)
	}
}

func TestSubscriber_AtomSessionCompleted_Incorrect(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{companionID: "fam-1"}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{
			AtomID: "a1", LearnerGCID: "user-1", IsCorrect: false,
		})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	calls := ap.Calls()
	if calls[0].RequestedDelta != 1 {
		t.Errorf("delta = %d, want 1 (incorrect)", calls[0].RequestedDelta)
	}
}

func TestSubscriber_AtomSessionCompleted_ReviewDue_AwardsBoth(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{companionID: "fam-1"}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{
			AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true, ReviewDue: true,
		})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	calls := ap.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	sources := map[string]int{}
	for _, c := range calls {
		sources[c.Source] = c.RequestedDelta
	}
	// CHO-2012 (ADR-218 D6): both awards request the RESOLVED default (0).
	if sources["atom_session"] != 0 {
		t.Errorf("atom_session delta = %d, want 0 (resolved default)", sources["atom_session"])
	}
	if sources["ebbinghaus_review"] != 0 {
		t.Errorf("ebbinghaus_review delta = %d, want 0 (resolved default)", sources["ebbinghaus_review"])
	}
}

func TestSubscriber_NoCompanion_SilentDrop(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{err: subscribers.ErrNoCompanion}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{
			AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true,
		})
	if err != nil {
		t.Errorf("expected silent drop, got %v", err)
	}
	if len(ap.Calls()) != 0 {
		t.Errorf("calls = %d, want 0", len(ap.Calls()))
	}
}

func TestSubscriber_ResolverError(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{err: errors.New("db down")}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{LearnerGCID: "user-1"})
	if err == nil {
		t.Errorf("expected resolver error")
	}
}

func TestSubscriber_BadEnvelope(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{companionID: "fam-1"}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	env := validEnv()
	env.EventID = "" // invalid
	err := sub.HandleAtomSessionCompleted(context.Background(), env,
		subscribers.AtomSessionCompletedPayload{LearnerGCID: "user-1"})
	if err == nil {
		t.Errorf("expected envelope validation error")
	}
}

func TestSubscriber_HexExpand(t *testing.T) {
	ap := &fakeAwardPort{}
	r := &fakeResolver{companionID: "fam-1"}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	err := sub.HandleHexagonExpanded(context.Background(), validEnv(),
		subscribers.KGHexagonExpandedPayload{LearnerGCID: "user-1"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	calls := ap.Calls()
	if calls[0].Source != "hex_expand" || calls[0].RequestedDelta != 0 {
		t.Errorf("hex_expand call = %+v", calls[0])
	}
}

func TestSubscriber_JunctionAccepted(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	_ = sub.HandleJunctionAccepted(context.Background(), validEnv(),
		subscribers.KGJunctionAcceptedPayload{LearnerGCID: "user-1"})
	if ap.Calls()[0].Source != "junction_accepted" {
		t.Errorf("source = %q", ap.Calls()[0].Source)
	}
	if ap.Calls()[0].RequestedDelta != 0 {
		t.Errorf("delta = %d, want 0 — resolved default (parity 15)", ap.Calls()[0].RequestedDelta)
	}
}

func TestSubscriber_DailyDoseServed(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	_ = sub.HandleDailyDoseServed(context.Background(), validEnv(),
		subscribers.DailyDoseServedPayload{LearnerGCID: "user-1"})
	if ap.Calls()[0].Source != "daily_dose_open" {
		t.Errorf("source = %q", ap.Calls()[0].Source)
	}
}

func TestSubscriber_PostCreated_SelfOnly(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	_ = sub.HandlePostCreated(context.Background(), validEnv(),
		subscribers.PostCreatedPayload{AuthorGCID: "user-1"})
	calls := ap.Calls()
	if len(calls) != 1 || calls[0].Source != "social_share" {
		t.Errorf("calls = %v", calls)
	}
}

func TestSubscriber_PostCreated_EmptyAuthor_NoAward(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	_ = sub.HandlePostCreated(context.Background(), validEnv(),
		subscribers.PostCreatedPayload{AuthorGCID: ""})
	if len(ap.Calls()) != 0 {
		t.Errorf("expected no award for empty author")
	}
}

func TestSubscriber_ReactionAdded_TargetGets(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	_ = sub.HandleReactionAdded(context.Background(), validEnv(),
		subscribers.ReactionAddedPayload{TargetGCID: "user-1"})
	if ap.Calls()[0].Source != "social_reaction" {
		t.Errorf("source = %q", ap.Calls()[0].Source)
	}
}

func TestSubscriber_AtomPublished(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	_ = sub.HandleAtomPublished(context.Background(), validEnv(),
		subscribers.AtomPublishedPayload{AuthorGCID: "user-1"})
	calls := ap.Calls()
	if calls[0].Source != "atom_authored" || calls[0].RequestedDelta != 0 {
		t.Errorf("call = %+v", calls[0])
	}
}

func TestSubscriber_IdempotencyDedupe(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	env := validEnv()
	_ = sub.HandleHexagonExpanded(context.Background(), env,
		subscribers.KGHexagonExpandedPayload{LearnerGCID: "user-1"})
	_ = sub.HandleHexagonExpanded(context.Background(), env,
		subscribers.KGHexagonExpandedPayload{LearnerGCID: "user-1"})
	if len(ap.Calls()) != 1 {
		t.Errorf("expected dedupe, got %d", len(ap.Calls()))
	}
}

// ----- ProvisionEggSubscriber -----

func TestProvisionEggSubscriber_HappyPath(t *testing.T) {
	pp := &fakeProvisionPort{}
	sub := subscribers.NewProvisionEggSubscriber(pp)
	now := time.Now()
	env := validEnv()
	err := sub.Handle(context.Background(), env,
		subscribers.EggPaymentSucceededPayload{
			PurchaseID:      "p1",
			PurchaserGCID:   "user-1",
			TargetTenantID:  "tenant-1",
			EggSku:          "egg.standard.v1",
			StripeSessionID: "cs_test_123",
			AmountCentsPaid: 999,
			Currency:        "USD",
			PaidAt:          now,
		})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(pp.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(pp.calls))
	}
	c := pp.calls[0]
	if c.EggPurchaseID != "p1" {
		t.Errorf("PurchaseID = %q", c.EggPurchaseID)
	}
	if c.OwnerGCID != "user-1" {
		t.Errorf("OwnerGCID = %q", c.OwnerGCID)
	}
	// CHO-2028: the subscriber must thread the inbound envelope trace context
	// and the payments stripe fields into ProvisionEggInput so the emitted
	// egg_purchased.v1 carries real values (no 00-prov-egg-00 stub, schema
	// fields populated).
	if c.Traceparent != env.Traceparent {
		t.Errorf("Traceparent = %q, want %q", c.Traceparent, env.Traceparent)
	}
	if c.Tracestate != env.Tracestate {
		t.Errorf("Tracestate = %q, want %q", c.Tracestate, env.Tracestate)
	}
	if c.StripeSessionID != "cs_test_123" {
		t.Errorf("StripeSessionID = %q", c.StripeSessionID)
	}
	if c.AmountCents != 999 {
		t.Errorf("AmountCents = %d", c.AmountCents)
	}
	if c.Currency != "USD" {
		t.Errorf("Currency = %q", c.Currency)
	}
}

func TestProvisionEggSubscriber_IdempotencyDedupe(t *testing.T) {
	pp := &fakeProvisionPort{}
	sub := subscribers.NewProvisionEggSubscriber(pp)
	env := validEnv()
	payload := subscribers.EggPaymentSucceededPayload{
		PurchaseID: "p1", PurchaserGCID: "user-1", TargetTenantID: "tenant-1",
		EggSku: "egg.standard.v1", PaidAt: time.Now(),
	}
	_ = sub.Handle(context.Background(), env, payload)
	_ = sub.Handle(context.Background(), env, payload)
	if len(pp.calls) != 1 {
		t.Errorf("expected dedupe by event_id, got %d", len(pp.calls))
	}
}

// ----- CHO-2107: process-then-mark — no event loss on transient failure -----
//
// Mark-first idempotency burned the dedupe key BEFORE processing: a failure
// after markSeen returned the error (NACK), but the Pub/Sub redelivery of the
// SAME event_id was then swallowed as a duplicate — the award/provision was
// permanently lost for the pod lifetime. These tests pin the fixed contract:
// a failed delivery leaves the key UNMARKED so the redelivery processes, and
// the side effect lands exactly once at this layer (the DB UNIQUE anchor
// absorbs any residual concurrent double-process downstream).

// flakyAwardPort fails the first failN AwardExp attempts then succeeds,
// recording only successful awards — models a transient downstream outage
// healed before the Pub/Sub redelivery.
type flakyAwardPort struct {
	mu       sync.Mutex
	failN    int
	attempts int
	calls    []growth.AwardExpInput
}

func (f *flakyAwardPort) AwardExp(_ context.Context, in growth.AwardExpInput) (*growth.AwardExpResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.attempts <= f.failN {
		return nil, errors.New("transient award failure")
	}
	f.calls = append(f.calls, in)
	return &growth.AwardExpResponse{ClampedDelta: in.RequestedDelta}, nil
}

func (f *flakyAwardPort) Calls() []growth.AwardExpInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]growth.AwardExpInput, len(f.calls))
	copy(out, f.calls)
	return out
}

// flakyResolver errors the first ResolveActiveCompanion call (transient DB
// blip) and resolves normally afterwards.
type flakyResolver struct {
	mu          sync.Mutex
	attempts    int
	companionID string
}

func (r *flakyResolver) ResolveActiveCompanion(_ context.Context, _, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.attempts == 1 {
		return "", errors.New("transient resolver failure")
	}
	return r.companionID, nil
}

// failSecondAward fails ONLY the 2nd AwardExp attempt (the ebbinghaus_review
// award on first delivery); every other attempt succeeds. Successful awards
// are counted per source.
type failSecondAward struct {
	mu        sync.Mutex
	attempts  int
	successes map[string]int
}

func (f *failSecondAward) AwardExp(_ context.Context, in growth.AwardExpInput) (*growth.AwardExpResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.attempts == 2 {
		return nil, errors.New("transient review-award failure")
	}
	if f.successes == nil {
		f.successes = map[string]int{}
	}
	f.successes[in.Source]++
	return &growth.AwardExpResponse{ClampedDelta: in.RequestedDelta}, nil
}

func (f *failSecondAward) successCount(source string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.successes[source]
}

// flakyProvisionPort fails the first failN ProvisionEgg attempts then
// succeeds, recording only successful provisions.
type flakyProvisionPort struct {
	mu       sync.Mutex
	failN    int
	attempts int
	calls    []growth.ProvisionEggInput
}

func (f *flakyProvisionPort) ProvisionEgg(_ context.Context, in growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.attempts <= f.failN {
		return nil, errors.New("transient provision failure")
	}
	f.calls = append(f.calls, in)
	return &growth.CompanionGrowthRow{CompanionID: "fam-" + in.EggPurchaseID}, nil
}

func (f *flakyProvisionPort) Calls() []growth.ProvisionEggInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]growth.ProvisionEggInput, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestSubscriber_AtomSessionCompleted_TransientAwardFailure_RedeliveryLands(t *testing.T) {
	ap := &flakyAwardPort{failN: 1}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	env := validEnv()
	p := subscribers.AtomSessionCompletedPayload{AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true}

	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err == nil {
		t.Fatal("first delivery: expected transient award error to propagate (nack)")
	}
	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if got := len(ap.Calls()); got != 1 {
		t.Errorf("successful awards = %d, want exactly 1 (failed delivery must NOT burn the dedupe key)", got)
	}
}

func TestSubscriber_AtomSessionCompleted_TransientResolverFailure_RedeliveryLands(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &flakyResolver{companionID: "fam-1"})
	env := validEnv()
	p := subscribers.AtomSessionCompletedPayload{AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true}

	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err == nil {
		t.Fatal("first delivery: expected transient resolver error to propagate (nack)")
	}
	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if got := len(ap.Calls()); got != 1 {
		t.Errorf("awards = %d, want exactly 1 (redelivery must land after resolver recovery)", got)
	}
}

func TestSubscriber_AtomSessionCompleted_ReviewFailure_RedeliveryLandsReview(t *testing.T) {
	ap := &failSecondAward{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	env := validEnv()
	p := subscribers.AtomSessionCompletedPayload{
		AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true, ReviewDue: true,
	}

	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err == nil {
		t.Fatal("first delivery: expected the ebbinghaus_review award error to propagate (nack)")
	}
	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if got := ap.successCount("ebbinghaus_review"); got != 1 {
		t.Errorf("ebbinghaus_review awards = %d, want exactly 1 (review award must not be lost)", got)
	}
	// The base award re-fires on redelivery — the admitted double-process at
	// this layer; companion_growth_events UNIQUE (companion_id, source,
	// idempotency_key) absorbs it downstream (migration 0032).
	if got := ap.successCount("atom_session"); got < 1 {
		t.Errorf("atom_session awards = %d, want >= 1", got)
	}
}

func TestSubscriber_AwardSimple_TransientAwardFailure_RedeliveryLands(t *testing.T) {
	ap := &flakyAwardPort{failN: 1}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	env := validEnv()
	p := subscribers.KGHexagonExpandedPayload{LearnerGCID: "user-1"}

	if err := sub.HandleHexagonExpanded(context.Background(), env, p); err == nil {
		t.Fatal("first delivery: expected transient award error to propagate (nack)")
	}
	if err := sub.HandleHexagonExpanded(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	calls := ap.Calls()
	if len(calls) != 1 {
		t.Fatalf("successful awards = %d, want exactly 1 (failed delivery must NOT burn the dedupe key)", len(calls))
	}
	if calls[0].Source != "hex_expand" {
		t.Errorf("source = %q", calls[0].Source)
	}
}

func TestProvisionEggSubscriber_TransientFailure_RedeliveryProvisions(t *testing.T) {
	pp := &flakyProvisionPort{failN: 1}
	sub := subscribers.NewProvisionEggSubscriber(pp)
	env := validEnv()
	payload := subscribers.EggPaymentSucceededPayload{
		PurchaseID: "p1", PurchaserGCID: "user-1", TargetTenantID: "tenant-1",
		EggSku: "egg.standard.v1", PaidAt: time.Now(),
	}

	if err := sub.Handle(context.Background(), env, payload); err == nil {
		t.Fatal("first delivery: expected transient provision error to propagate (nack)")
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if got := len(pp.Calls()); got != 1 {
		t.Errorf("successful provisions = %d, want exactly 1 (failed delivery must NOT burn the dedupe key)", got)
	}
}
