package clients

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// bus_dose_recommender_test.go - ADR-254 D4: the daily-dose AI picks ride the
// dose_recommendation lane (chora.consumption.dose_recommendation.requested.v1
// -> .completed.v1). BusDoseRecommender implements RecommenderEnginePort so the
// daily-dose/ai handler keeps its shape: publish the request (turn row + outbox
// in one tx), wait on the store, map recommended_atom_ids + rationale.

func completeDoseWhenBegun(t *testing.T, store *fakeTurnStore, res companion.TurnResult) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			store.mu.Lock()
			var id string
			for k := range store.turns {
				id = k
			}
			store.mu.Unlock()
			if id != "" {
				res.CompletedAt = time.Now()
				_, _ = store.Complete(context.Background(), btTenant, id, res)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
}

func newDoseRecommender(t *testing.T, store companion.TurnStore, pub TurnRequestPublisher, deadline time.Duration) *BusDoseRecommender {
	t.Helper()
	r, err := NewBusDoseRecommender(BusDoseRecommenderConfig{
		Store: store, Publish: pub, Deadline: deadline, PollInterval: 3 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBusDoseRecommender: %v", err)
	}
	return r
}

func TestBusDoseRecommender_PublishesRequestAndMapsResult(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	rec := newDoseRecommender(t, store, pub, 2*time.Second)
	completeDoseWhenBegun(t, store, companion.TurnResult{
		WireStatus: "OK", WorkflowID: "wf-d", GeneratedByModelID: "gemini-2.5-flash",
		RecommendedAtomIDs: []string{"a1", "a2"}, Rationale: "weak spots first",
	})
	resp, err := rec.Recommend(context.Background(), RecommendRequest{
		TenantID: btTenant, UserGCID: btGCID, ManaTier: "basic", LearnerPersona: "curious-explorer",
		TopicHint: "fractions", Candidates: []AtomCandidate{{AtomID: "a1", Title: "T1"}, {AtomID: "a2", Title: "T2"}},
	})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if len(resp.AtomIDs) != 2 || resp.AtomIDs[0] != "a1" || resp.Narrative != "weak spots first" || resp.Model != "gemini-2.5-flash" {
		t.Fatalf("resp = %+v", resp)
	}
	if pub.count() != 1 || pub.calls[0].topic != events.TopicDoseRecommendationRequested {
		t.Fatalf("published = %+v", pub.calls)
	}
	p := pub.calls[0].payload
	for _, k := range []string{"dose_request_id", "tenant_id", "gcid", "trigger", "context_json", "requested_at"} {
		if p[k] == nil || p[k] == "" {
			t.Errorf("payload missing %s: %v", k, p)
		}
	}
	if pub.calls[0].env.IdempotencyKey != p["dose_request_id"] {
		t.Errorf("envelope idempotency_key must be the dose_request_id (ADR-254 D4 business key)")
	}
	if p["trigger"] != "daily_dose_ai" {
		t.Errorf("trigger = %v", p["trigger"])
	}
}

func TestBusDoseRecommender_FailedOrTimeoutIsAnError(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	rec := newDoseRecommender(t, store, pub, 2*time.Second)
	completeDoseWhenBegun(t, store, companion.TurnResult{WireStatus: "FAILED", ErrorCode: "boom"})
	if _, err := rec.Recommend(context.Background(), RecommendRequest{TenantID: btTenant, UserGCID: btGCID, ManaTier: "basic"}); err == nil {
		t.Fatal("a FAILED result must surface as an error (the dose handler degrades to templated copy)")
	}

	store2 := newFakeTurnStore()
	rec2 := newDoseRecommender(t, store2, &fakeTurnPublisher{}, 30*time.Millisecond)
	_, err := rec2.Recommend(context.Background(), RecommendRequest{TenantID: btTenant, UserGCID: btGCID, ManaTier: "basic"})
	if err == nil || !errors.Is(err, ErrDoseRecommendationTimeout) {
		t.Fatalf("timeout err = %v, want ErrDoseRecommendationTimeout", err)
	}
}

func TestBusDoseRecommender_RequiresTenantAndLearner(t *testing.T) {
	rec := newDoseRecommender(t, newFakeTurnStore(), &fakeTurnPublisher{}, time.Second)
	if _, err := rec.Recommend(context.Background(), RecommendRequest{TenantID: "", UserGCID: btGCID}); err == nil {
		t.Fatal("missing tenant must fail loud")
	}
}
