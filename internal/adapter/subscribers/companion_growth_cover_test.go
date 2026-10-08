// companion_growth_cover_test.go — coverage tests for the CompanionGrowthSubscriber
// error + dedup branches not reached by companion_growth_subscriber_test.go:
//
//   - HandleAtomSessionCompleted idempotent dedup short-circuit (event_id replay)
//   - HandleAtomSessionCompleted AwardExp error propagation (first award)
//   - HandleAtomSessionCompleted ReviewDue second-AwardExp error propagation
//   - awardSimple ErrNoCompanion silent-drop + generic resolver-error propagation
package subscribers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestSubscriber_AtomSessionCompleted_IdempotentDedupe(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	env := validEnv()
	p := subscribers.AtomSessionCompletedPayload{AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true}
	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err != nil {
		t.Fatal(err)
	}
	// Same event_id → atom_session dedup key short-circuits the second call.
	if err := sub.HandleAtomSessionCompleted(context.Background(), env, p); err != nil {
		t.Fatal(err)
	}
	if len(ap.Calls()) != 1 {
		t.Errorf("expected 1 award (event_id dedup), got %d", len(ap.Calls()))
	}
}

func TestSubscriber_AtomSessionCompleted_AwardErrorPropagates(t *testing.T) {
	ap := &fakeAwardPort{err: errors.New("award failed")}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true})
	if err == nil {
		t.Error("expected AwardExp error to propagate (nack)")
	}
}

func TestSubscriber_AtomSessionCompleted_ReviewDueAwardErrorPropagates(t *testing.T) {
	// The base AwardExp succeeds; the ebbinghaus_review AwardExp fails. The
	// fakeAwardPort errors on EVERY call, so the first award would also fail —
	// to isolate the review path we use a resolver-backed port that fails only
	// after the first call.
	ap := &failAfterFirstAward{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{companionID: "fam-1"})
	err := sub.HandleAtomSessionCompleted(context.Background(), validEnv(),
		subscribers.AtomSessionCompletedPayload{
			AtomID: "a1", LearnerGCID: "user-1", IsCorrect: true, ReviewDue: true,
		})
	if err == nil {
		t.Error("expected the ebbinghaus_review AwardExp error to propagate")
	}
	if ap.calls != 2 {
		t.Errorf("calls = %d; want 2 (base ok, review failed)", ap.calls)
	}
}

func TestSubscriber_AwardSimple_NoCompanionSilentDrop(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{err: subscribers.ErrNoCompanion})
	if err := sub.HandleJunctionAccepted(context.Background(), validEnv(),
		subscribers.KGJunctionAcceptedPayload{LearnerGCID: "user-1"}); err != nil {
		t.Errorf("expected silent drop on ErrNoCompanion, got %v", err)
	}
	if len(ap.Calls()) != 0 {
		t.Errorf("expected no award, got %d", len(ap.Calls()))
	}
}

func TestSubscriber_AwardSimple_ResolverErrorPropagates(t *testing.T) {
	ap := &fakeAwardPort{}
	sub := subscribers.NewCompanionGrowthSubscriber(ap, &fakeResolver{err: errors.New("resolver down")})
	if err := sub.HandleJunctionAccepted(context.Background(), validEnv(),
		subscribers.KGJunctionAcceptedPayload{LearnerGCID: "user-1"}); err == nil {
		t.Error("expected resolver error to propagate from awardSimple")
	}
}

// failAfterFirstAward is an AwardExpPort whose FIRST AwardExp succeeds and
// every subsequent one errors — used to exercise the ReviewDue second-award
// error branch without failing the base award.
type failAfterFirstAward struct {
	calls int
}

func (f *failAfterFirstAward) AwardExp(_ context.Context, in growth.AwardExpInput) (*growth.AwardExpResponse, error) {
	f.calls++
	if f.calls > 1 {
		return nil, errors.New("second award failed")
	}
	return &growth.AwardExpResponse{ClampedDelta: in.RequestedDelta}, nil
}
