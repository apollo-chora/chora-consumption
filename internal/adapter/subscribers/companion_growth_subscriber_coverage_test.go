package subscribers_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func TestAtomSessionCompleted_ReviewDueResolverFails(t *testing.T) {
	ap := &fakeAwardPort{}
	// Resolver returns no-companion — ebbinghaus review path silently drops too.
	r := &fakeResolver{err: subscribers.ErrNoCompanion}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, r)
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{
			LearnerGCID: "u-1", IsCorrect: true, ReviewDue: true,
		})
	if err != nil {
		t.Errorf("expected silent drop, got %v", err)
	}
	if len(ap.Calls()) != 0 {
		t.Errorf("expected no awards, got %d", len(ap.Calls()))
	}
}

func TestReactionAdded_EmptyTarget_NoAward(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	err := sub.HandleReactionAdded(context.Background(), validEnv(),
		subscribers.ReactionAddedPayload{TargetGCID: ""})
	if err != nil {
		t.Errorf("got error: %v", err)
	}
	if len(ap.Calls()) != 0 {
		t.Errorf("calls = %d, want 0", len(ap.Calls()))
	}
}

func TestAtomPublished_EmptyAuthor_NoAward(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	err := sub.HandleAtomPublished(context.Background(), validEnv(),
		subscribers.AtomPublishedPayload{AuthorGCID: ""})
	if err != nil {
		t.Errorf("got error: %v", err)
	}
	if len(ap.Calls()) != 0 {
		t.Errorf("calls = %d, want 0", len(ap.Calls()))
	}
}

func TestProvisionEgg_PaidAtZero_FallsBackToOccurredAt(t *testing.T) {
	pp := &fakeProvisionPort{}
	sub := subscribers.NewProvisionEggSubscriber(pp)
	env := validEnv()
	env.OccurredAt = time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	err := sub.Handle(context.Background(), env,
		subscribers.EggPaymentSucceededPayload{
			PurchaseID:     "p-zero",
			PurchaserGCID:  "u-1",
			TargetTenantID: "t-1",
			EggSku:         "egg.s.v1",
			// PaidAt intentionally zero
		})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(pp.calls) != 1 {
		t.Fatalf("calls = %d", len(pp.calls))
	}
}

func TestProvisionEgg_BadEnvelope(t *testing.T) {
	pp := &fakeProvisionPort{}
	sub := subscribers.NewProvisionEggSubscriber(pp)
	bad := validEnv()
	bad.EventID = ""
	err := sub.Handle(context.Background(), bad,
		subscribers.EggPaymentSucceededPayload{PurchaseID: "p"})
	if err == nil {
		t.Errorf("expected envelope validation error")
	}
}

func TestAwardSimple_BadEnvelope(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	bad := validEnv()
	bad.EventID = ""
	err := sub.HandleDailyDoseServed(context.Background(), bad,
		subscribers.DailyDoseServedPayload{LearnerGCID: "u-1"})
	if err == nil {
		t.Errorf("expected validation error")
	}
}
