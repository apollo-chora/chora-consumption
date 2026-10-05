// learner_profile_preferences_subscriber_test.go — RED-first (TDD) for the WS1
// preferences leg subscriber handler (ADR-200 deferred
// `consumption.preferences.updated` → FactPreference).
//
// References symbols that DO NOT EXIST YET (PreferencesUpdatedPayload,
// LearnerProfileSubscriber.HandlePreferencesUpdated, learner_profile.PreferenceKV)
// so the package fails to COMPILE — the intended RED. Do NOT implement GREEN here.
//
// Reuses the package's existing test doubles/helpers: fakeProfileRepo, lpEnv,
// lpLearner, newTestEnvelope (from learner_profile_subscriber_test.go /
// subscribers_test.go).
package subscribers

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

func prefsPayload() PreferencesUpdatedPayload {
	return PreferencesUpdatedPayload{
		LearnerGCID: lpLearner,
		Prefs: []learner_profile.PreferenceKV{
			{Key: "dose.map.aaaa1111-1111-1111-1111-111111111111", Value: "excluded"},
			{Key: "dose.map.bbbb2222-2222-2222-2222-222222222222", Value: "included"},
		},
		OccurredAt: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC),
	}
}

// One verified preferences.updated event projects one FactPreference per key
// (+ one activity line), each carrying the source event_id (verified-only).
func TestLearnerProfile_PreferencesUpdated_ProjectsPerKey(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-0000000000f1")

	if err := sub.HandlePreferencesUpdated(env, prefsPayload()); err != nil {
		t.Fatalf("HandlePreferencesUpdated: %v", err)
	}
	if len(repo.facts) != 2 {
		t.Fatalf("want 2 preference facts (one per key), got %d", len(repo.facts))
	}
	for _, f := range repo.facts {
		if f.Type != learner_profile.FactPreference {
			t.Errorf("type = %q; want preference", f.Type)
		}
		if f.SourceEventID != env.EventID {
			t.Errorf("source_event_id = %q; want the verified envelope event id %q", f.SourceEventID, env.EventID)
		}
		if f.LearnerGCID != lpLearner {
			t.Errorf("learner = %q; want %q", f.LearnerGCID, lpLearner)
		}
		if !f.OccurredAt.Equal(prefsPayload().OccurredAt) {
			t.Errorf("occurred_at = %v; want the event instant", f.OccurredAt)
		}
	}
	if repo.facts[0].RefID != "dose.map.aaaa1111-1111-1111-1111-111111111111" || repo.facts[0].Detail.Label != "excluded" {
		t.Errorf("fact[0] key/value wrong: ref=%q label=%q", repo.facts[0].RefID, repo.facts[0].Detail.Label)
	}
	// Q4: a preference change is SUPPRESSED from RecentActivity — no activity line.
	if len(repo.activities) != 0 {
		t.Fatalf("preference changes must not append to the activity log (Q4), got %d", len(repo.activities))
	}
}

// An incomplete envelope is rejected before any write (fail-loud, no partial state).
func TestLearnerProfile_PreferencesUpdated_RejectsIncompleteEnvelope(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)

	if err := sub.HandlePreferencesUpdated(events.Envelope{}, prefsPayload()); err == nil {
		t.Fatal("expected envelope-validation error")
	}
	if len(repo.facts) != 0 {
		t.Fatalf("must not write on invalid envelope, got %d facts", len(repo.facts))
	}
}

// Redelivery of the same event_id is a no-op (idempotent claim-run-persist).
func TestLearnerProfile_PreferencesUpdated_RedeliveryIsNoOp(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-0000000000f2")

	if err := sub.HandlePreferencesUpdated(env, prefsPayload()); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandlePreferencesUpdated(env, prefsPayload()); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(repo.facts) != 2 {
		t.Fatalf("dedup: want 2 facts after redelivery, got %d", len(repo.facts))
	}
}

// A repo failure propagates (fail-loud → Pub/Sub retry → DLQ), never a swallowed error.
func TestLearnerProfile_PreferencesUpdated_UpsertError_FailLoud(t *testing.T) {
	repo := &fakeProfileRepo{upsertErr: errors.New("boom")}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-0000000000f3")

	if err := sub.HandlePreferencesUpdated(env, prefsPayload()); err == nil {
		t.Fatal("expected upsert error to propagate")
	}
}

// A per-learner event with no learner_gcid cannot project a learner fact.
func TestLearnerProfile_PreferencesUpdated_MissingLearnerRejected(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-0000000000f4")
	p := prefsPayload()
	p.LearnerGCID = ""

	if err := sub.HandlePreferencesUpdated(env, p); err == nil {
		t.Fatal("expected rejection when learner_gcid is empty")
	}
	if len(repo.facts) != 0 {
		t.Fatalf("must not write a fact with no learner, got %d facts", len(repo.facts))
	}
}
