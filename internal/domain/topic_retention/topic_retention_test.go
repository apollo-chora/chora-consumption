// Package topic_retention — RED phase tests for the Ebbinghaus retention
// scorer. The implementation does not yet exist; these tests drive it into
// being.
//
// Per docs/m13/phyllis-mvp-2026-05-08.md §5.4: "TopicRetention scoring with
// Ebbinghaus R(t) = e^(-t/S)". The aggregate is per-user, per-topic.
package topic_retention

import (
	"errors"
	"math"
	"testing"
	"time"
)

func mustNewScore(t *testing.T, tenantID, gcid, topicID string, last time.Time, strength float64) *TopicScore {
	t.Helper()
	s, err := New(tenantID, gcid, topicID, last, strength)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNew_RejectsBlankFields(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		tenantID, gcid, topic string
		strength              float64
		errSubstr             string
	}{
		{"", "g", "topic", 1.0, "tenant_id"},
		{"t", "", "topic", 1.0, "gcid"},
		{"t", "g", "", 1.0, "topic_id"},
		{"t", "g", "topic", 0, "strength"},
		{"t", "g", "topic", -1, "strength"},
	}
	for _, c := range cases {
		_, err := New(c.tenantID, c.gcid, c.topic, now, c.strength)
		if err == nil {
			t.Errorf("expected error for %+v", c)
			continue
		}
	}
}

func TestNew_DefaultsScoreToOneAtT0(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", now, 1.0)
	if s.Strength != 1.0 {
		t.Errorf("Strength = %v; want 1.0", s.Strength)
	}
	got := s.RetentionAt(now)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("RetentionAt(t=last_reviewed) = %v; want 1.0 (no decay)", got)
	}
}

func TestRetentionAt_DecaysExponentially(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	// strength=1 day; after 1 day: e^-1 ≈ 0.367879
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)

	cases := []struct {
		elapsed time.Duration
		want    float64
	}{
		{0 * time.Hour, 1.0},
		{12 * time.Hour, math.Exp(-0.5)},
		{24 * time.Hour, math.Exp(-1.0)},
		{48 * time.Hour, math.Exp(-2.0)},
		{7 * 24 * time.Hour, math.Exp(-7.0)},
	}
	for _, c := range cases {
		got := s.RetentionAt(last.Add(c.elapsed))
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("RetentionAt(elapsed=%v) = %v; want %v", c.elapsed, got, c.want)
		}
	}
}

func TestRetentionAt_NeverGoesBelowZero(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	got := s.RetentionAt(last.Add(365 * 24 * time.Hour)) // 1 year of decay
	if got < 0 {
		t.Errorf("retention = %v; should be >= 0", got)
	}
	if got > 1.0 {
		t.Errorf("retention = %v; should be <= 1.0", got)
	}
}

func TestReview_GrowsStrengthOnCorrect(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	prevStrength := s.Strength
	prevLast := s.LastReviewedAt

	now := last.Add(48 * time.Hour)
	s.Review(true, now)

	if s.Strength <= prevStrength {
		t.Errorf("Review(correct=true) Strength = %v; want > %v", s.Strength, prevStrength)
	}
	if !s.LastReviewedAt.Equal(now) {
		t.Errorf("LastReviewedAt = %v; want %v", s.LastReviewedAt, now)
	}
	if s.LastReviewedAt.Equal(prevLast) {
		t.Errorf("LastReviewedAt did not advance")
	}
}

func TestReview_ShrinksStrengthOnIncorrect_FloorAtMin(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	now := last.Add(48 * time.Hour)
	s.Review(false, now)
	if s.Strength >= 1.0 {
		t.Errorf("Review(correct=false) Strength = %v; want shrunk below 1.0", s.Strength)
	}
	if s.Strength < MinStrengthDays {
		t.Errorf("Strength = %v; below MinStrengthDays = %v", s.Strength, MinStrengthDays)
	}
}

func TestReview_StrengthCappedAtMax(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, MaxStrengthDays-1)
	for i := 0; i < 50; i++ {
		s.Review(true, last.Add(time.Duration(i+1)*24*time.Hour))
	}
	if s.Strength > MaxStrengthDays {
		t.Errorf("Strength = %v; want <= %v", s.Strength, MaxStrengthDays)
	}
}

func TestReview_RetentionScoreSnapshotted(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 2.0)
	// Right after review the snapshot should be 1.0 (we just reviewed).
	now := last.Add(48 * time.Hour)
	s.Review(true, now)
	if math.Abs(s.RetentionScore-1.0) > 1e-9 {
		t.Errorf("RetentionScore = %v; want 1.0 (just reviewed)", s.RetentionScore)
	}
}

func TestReview_IncrementsReviewCount(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	if s.ReviewCount != 0 {
		t.Errorf("initial ReviewCount = %d; want 0", s.ReviewCount)
	}
	s.Review(true, last.Add(time.Hour))
	s.Review(false, last.Add(2*time.Hour))
	if s.ReviewCount != 2 {
		t.Errorf("ReviewCount = %d; want 2", s.ReviewCount)
	}
}

func TestErrorTypes(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	_, err := New("", "", "", now, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidScore) {
		t.Errorf("err = %v; want errors.Is(ErrInvalidScore)", err)
	}
}

func TestSoftDelete_StampsDeletedAt(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	if s.DeletedAt != nil {
		t.Fatalf("DeletedAt should start nil")
	}
	now := last.Add(time.Hour)
	s.SoftDelete(now)
	if s.DeletedAt == nil || !s.DeletedAt.Equal(now) {
		t.Errorf("DeletedAt = %v; want %v", s.DeletedAt, now)
	}
	if !s.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v; want %v", s.UpdatedAt, now)
	}
}

func TestRetentionAt_BeforeOrAtLastReviewed(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	// Negative elapsed -> short-circuit returns 1.0.
	got := s.RetentionAt(last.Add(-1 * time.Hour))
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("RetentionAt(t<last) = %v; want 1.0", got)
	}
}

func TestIsDueAt_FreshScoreNotDue(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	// R(0)=1.0 ≥ DueThreshold → the topic was just reviewed, not due.
	if s.IsDueAt(last) {
		t.Errorf("IsDueAt(t=last_reviewed, R=1.0) = true; want false (≥ %v)", DueThreshold)
	}
}

func TestIsDueAt_DecayedScoreIsDue(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0)
	// After 1 day at S=1: R=e^-1≈0.368 < DueThreshold → due for review.
	if !s.IsDueAt(last.Add(24 * time.Hour)) {
		t.Errorf("IsDueAt(elapsed=1d, R≈0.368) = false; want true (< %v)", DueThreshold)
	}
}

// IsDueAt flips strictly at DueThreshold: a topic retained ABOVE the threshold
// is not due; one decayed BELOW it is. Threshold-relative (not knife-edge) so
// it survives float wobble and tracks any future DueThreshold tuning.
func TestIsDueAt_StraddlesThreshold(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, 1.0) // S=1 day
	// elapsedForRetention returns the wall-clock at which R(t)==target (S=1):
	// R=e^(-t/S) ⟹ t = -S·ln(target) days.
	elapsedForRetention := func(target float64) time.Time {
		days := -math.Log(target)
		return last.Add(time.Duration(days * 24 * float64(time.Hour)))
	}
	if s.IsDueAt(elapsedForRetention(DueThreshold + 0.1)) {
		t.Errorf("IsDueAt(R=%.2f above threshold %.2f) = true; want false", DueThreshold+0.1, DueThreshold)
	}
	if !s.IsDueAt(elapsedForRetention(DueThreshold - 0.1)) {
		t.Errorf("IsDueAt(R=%.2f below threshold %.2f) = false; want true", DueThreshold-0.1, DueThreshold)
	}
}

func TestNew_ZeroNowDefaultsToWallClock(t *testing.T) {
	s, err := New("t1", "g1", "agile", time.Time{}, 1.0)
	if err != nil {
		t.Fatalf("New zero-now: %v", err)
	}
	if s.LastReviewedAt.IsZero() {
		t.Errorf("LastReviewedAt should default to wall clock when zero passed")
	}
	if s.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt should default to wall clock when zero passed")
	}
}

func TestNew_StrengthClampedToMaxOnInput(t *testing.T) {
	last := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s := mustNewScore(t, "t1", "g1", "agile", last, MaxStrengthDays*2)
	if s.Strength > MaxStrengthDays {
		t.Errorf("Strength = %v; want clamped to %v", s.Strength, MaxStrengthDays)
	}
}
