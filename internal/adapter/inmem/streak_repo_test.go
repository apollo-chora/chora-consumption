// Package inmem — S5.1 streak / XP / topic-accuracy in-memory adapters.
//
// Per the S5.1 A-Companion briefing the new endpoints `/v1/me/streak`,
// `/v1/me/xp`, and the dose composer's `TopicAccuracy` input require
// per-(tenant, gcid) state. Production (M12) replaces these with
// PostgreSQL repos under `multi-tenant-rls` policies.
//
// TDD strict (RED → GREEN → REFACTOR).
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---------- StreakRepo ----------

func TestStreakRepo_GetOrCreate_NewLearnerHasZero(t *testing.T) {
	r := NewStreakRepo()
	got := mustStreak(t, r, "tenant-a", "learner-1")
	if got.Count != 0 {
		t.Errorf("new learner Count = %d, want 0", got.Count)
	}
}

func TestStreakRepo_RecordActivity_PersistsBetweenReads(t *testing.T) {
	r := NewStreakRepo()
	day1 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	r.RecordActivity(context.Background(), "tenant-a", "learner-1", day1)
	r.RecordActivity(context.Background(), "tenant-a", "learner-1", day1.AddDate(0, 0, 1))
	got := mustStreak(t, r, "tenant-a", "learner-1")
	if got.Count != 2 {
		t.Errorf("after 2-day streak read = %d, want 2", got.Count)
	}
}

func TestStreakRepo_TenantIsolation(t *testing.T) {
	r := NewStreakRepo()
	day := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	r.RecordActivity(context.Background(), "tenant-a", "learner-1", day)
	gotB := mustStreak(t, r, "tenant-b", "learner-1")
	if gotB.Count != 0 {
		t.Errorf("cross-tenant leak: tenant-b learner-1 count = %d, want 0", gotB.Count)
	}
}

// TestStreakRepo_UserIsolation — same tenant, two learners, distinct
// streaks (RLS leak prevention at adapter level).
func TestStreakRepo_UserIsolation(t *testing.T) {
	r := NewStreakRepo()
	day := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	r.RecordActivity(context.Background(), "tenant-a", "learner-1", day)
	r.RecordActivity(context.Background(), "tenant-a", "learner-1", day.AddDate(0, 0, 1))
	r.RecordActivity(context.Background(), "tenant-a", "learner-1", day.AddDate(0, 0, 2))
	got2 := mustStreak(t, r, "tenant-a", "learner-2")
	if got2.Count != 0 {
		t.Errorf("cross-user leak: learner-2 count = %d, want 0 (isolated from learner-1)", got2.Count)
	}
}

// ---------- XPRepo ----------

func TestXPRepo_GetOrCreate_NewLearnerHasZero(t *testing.T) {
	r := NewXPRepo()
	xp := mustXP(t, r, "tenant-a", "learner-1")
	if xp != 0 {
		t.Errorf("new learner XP = %d, want 0", xp)
	}
}

func TestXPRepo_Award_AccumulatesXP(t *testing.T) {
	r := NewXPRepo()
	r.Award(context.Background(), "tenant-a", "learner-1", companion.XPForActivity(companion.ActivityAtomCompleted))
	r.Award(context.Background(), "tenant-a", "learner-1", companion.XPForActivity(companion.ActivityAtomCompleted))
	r.Award(context.Background(), "tenant-a", "learner-1", companion.XPForActivity(companion.ActivityDailyDoseCompleted))
	got := mustXP(t, r, "tenant-a", "learner-1")
	want := 2*companion.XPPerAtomCompleted + companion.XPPerDailyDoseCompleted
	if got != want {
		t.Errorf("Get = %d, want %d", got, want)
	}
}

func TestXPRepo_TenantIsolation(t *testing.T) {
	r := NewXPRepo()
	r.Award(context.Background(), "tenant-a", "learner-1", 100)
	if got := mustXP(t, r, "tenant-b", "learner-1"); got != 0 {
		t.Errorf("cross-tenant leak: tenant-b learner-1 XP = %d, want 0", got)
	}
}

// ---------- TopicAccuracyRepo ----------

func TestTopicAccuracyRepo_GetByLearner_EmptyByDefault(t *testing.T) {
	r := NewTopicAccuracyRepo()
	if got := mustGetByLearner(t, r, "tenant-a", "learner-1"); len(got) != 0 {
		t.Errorf("empty-state map size = %d, want 0", len(got))
	}
}

func TestTopicAccuracyRepo_RecordAttempt_ComputesRollingAverage(t *testing.T) {
	r := NewTopicAccuracyRepo()
	// 3 attempts in "scrum": 2 wrong + 1 right → 33.3% accuracy.
	mustRecordAttempt(t, r, "tenant-a", "learner-1", "scrum", false)
	mustRecordAttempt(t, r, "tenant-a", "learner-1", "scrum", false)
	mustRecordAttempt(t, r, "tenant-a", "learner-1", "scrum", true)
	got := mustGetByLearner(t, r, "tenant-a", "learner-1")
	if len(got) != 1 {
		t.Fatalf("topic count = %d, want 1", len(got))
	}
	scrum, ok := got["scrum"]
	if !ok {
		t.Fatal("scrum topic not present")
	}
	want := 1.0 / 3.0
	if scrum < want-0.001 || scrum > want+0.001 {
		t.Errorf("scrum accuracy = %f, want %f", scrum, want)
	}
}

func TestTopicAccuracyRepo_TenantIsolation(t *testing.T) {
	r := NewTopicAccuracyRepo()
	mustRecordAttempt(t, r, "tenant-a", "learner-1", "scrum", true)
	got := mustGetByLearner(t, r, "tenant-b", "learner-1")
	if len(got) != 0 {
		t.Errorf("cross-tenant leak: tenant-b map size = %d, want 0", len(got))
	}
}

// ---------- ActivePathTopicsRepo ----------

func TestActivePathTopicsRepo_GetTopics_EmptyByDefault(t *testing.T) {
	r := NewActivePathTopicsRepo()
	if got := mustGetTopics(t, r, "tenant-a", "learner-1"); len(got) != 0 {
		t.Errorf("empty-state map size = %d, want 0", len(got))
	}
}

func TestActivePathTopicsRepo_RecordPathTopics_PersistsAndUnions(t *testing.T) {
	r := NewActivePathTopicsRepo()
	mustRecordPathTopics(t, r, "tenant-a", "learner-1", []string{"agile", "scrum"})
	mustRecordPathTopics(t, r, "tenant-a", "learner-1", []string{"scrum", "kanban"})
	got := mustGetTopics(t, r, "tenant-a", "learner-1")
	want := map[string]bool{"agile": true, "scrum": true, "kanban": true}
	for k := range want {
		if !got[k] {
			t.Errorf("topic %q missing from got", k)
		}
	}
	if len(got) != 3 {
		t.Errorf("topic count = %d, want 3 (agile, scrum, kanban union)", len(got))
	}
}

func TestActivePathTopicsRepo_TenantIsolation(t *testing.T) {
	r := NewActivePathTopicsRepo()
	mustRecordPathTopics(t, r, "tenant-a", "learner-1", []string{"agile"})
	got := mustGetTopics(t, r, "tenant-b", "learner-1")
	if len(got) != 0 {
		t.Errorf("cross-tenant leak: tenant-b topic count = %d, want 0", len(got))
	}
}

// TestActivePathTopicsRepo_UserIsolation — RLS leak prevention.
func TestActivePathTopicsRepo_UserIsolation(t *testing.T) {
	r := NewActivePathTopicsRepo()
	mustRecordPathTopics(t, r, "tenant-a", "learner-1", []string{"agile", "scrum"})
	got := mustGetTopics(t, r, "tenant-a", "learner-2")
	if len(got) != 0 {
		t.Errorf("cross-user leak: learner-2 topic count = %d, want 0", len(got))
	}
}

// mustStreak reads a streak via the port, failing the test on error.
func mustStreak(t *testing.T, r *StreakRepo, tenantID, gcid string) *companion.Streak {
	t.Helper()
	s, err := r.Get(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("Streaks.Get: %v", err)
	}
	return s
}

// mustXP reads XP via the port, failing the test on error.
func mustXP(t *testing.T, r *XPRepo, tenantID, gcid string) int {
	t.Helper()
	xp, err := r.Get(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("XP.Get: %v", err)
	}
	return xp
}

// ---------- tail-arm sanitisation ----------

func TestTopicAccuracyRepo_RecordAttempt_EmptyTopicNoop(t *testing.T) {
	r := NewTopicAccuracyRepo()
	if err := r.RecordAttempt(context.Background(), "tenant-a", "learner-1", "", true); err != nil {
		t.Fatalf("RecordAttempt(empty topic): %v", err)
	}
	if got := mustGetByLearner(t, r, "tenant-a", "learner-1"); len(got) != 0 {
		t.Errorf("empty topic recorded a row: %v", got)
	}
}

func TestActivePathTopicsRepo_RecordPathTopics_EmptyAndDirtyEdgeCases(t *testing.T) {
	r := NewActivePathTopicsRepo()
	// Empty topic-set is a no-op (early return).
	if err := r.RecordPathTopics(context.Background(), "tenant-a", "learner-1", testPathID, nil); err != nil {
		t.Fatalf("RecordPathTopics(nil): %v", err)
	}
	// Empty learning_path_id + empty topic strings are silently dropped.
	if err := r.RecordPathTopics(context.Background(), "tenant-a", "learner-1", "", []string{"agile", "", "scrum"}); err != nil {
		t.Fatalf("RecordPathTopics dirty: %v", err)
	}
	got := mustGetTopics(t, r, "tenant-a", "learner-1")
	if !got["agile"] || !got["scrum"] {
		t.Errorf("dirty input dropped valid topics: got %v", got)
	}
	if got[""] {
		t.Errorf("empty topic string leaked into the set")
	}
}
