// merge_split_transform_test.go — WS-C6 (CHO-2085, ADR-227 D14) contract for
// the retention side of a concept merge/split. On merge, the survivor keeps
// the WEAKEST retention of the two concepts (WeakerOf); on both merge and
// split, the winning curve is re-keyed onto the new concept id (CloneForTopic)
// so the map's due-terrain follows the survivor / children.
//
// RED phase: WeakerOf / CloneForTopic do not exist yet — this file must fail
// to COMPILE until the transform is written.
package topic_retention

import (
	"errors"
	"testing"
	"time"
)

func TestWeakerOf_PicksLowerRetention(t *testing.T) {
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	// a decayed heavily (reviewed 10 days ago, S=1 -> R ~ e^-10); b is fresh.
	a := mustNewScore(t, "t1", "g1", "algebra", now.Add(-10*24*time.Hour), 1.0)
	b := mustNewScore(t, "t1", "g1", "algebra", now, 1.0)

	aStrength, aReviews, aLast := a.Strength, a.ReviewCount, a.LastReviewedAt

	if got := WeakerOf(a, b, now); got != a {
		t.Errorf("WeakerOf(a,b) = %p, want a (lower retention)", got)
	}
	if got := WeakerOf(b, a, now); got != a {
		t.Errorf("WeakerOf(b,a) = %p, want a (order-independent weakest)", got)
	}
	// pure: WeakerOf must not mutate its inputs.
	if a.Strength != aStrength || a.ReviewCount != aReviews || !a.LastReviewedAt.Equal(aLast) {
		t.Error("WeakerOf mutated input a")
	}
}

func TestWeakerOf_TieReturnsA(t *testing.T) {
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	last := now.Add(-2 * 24 * time.Hour)
	// identical curve state -> identical RetentionAt -> tie resolves to a.
	a := mustNewScore(t, "t1", "g1", "algebra", last, 3.0)
	b := mustNewScore(t, "t1", "g1", "algebra", last, 3.0)
	if got := WeakerOf(a, b, now); got != a {
		t.Errorf("tie: got %p, want a", got)
	}
}

func TestWeakerOf_NilSafety(t *testing.T) {
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	a := mustNewScore(t, "t1", "g1", "algebra", now, 1.0)
	if WeakerOf(nil, nil, now) != nil {
		t.Error("(nil,nil) should be nil")
	}
	if WeakerOf(a, nil, now) != a {
		t.Error("(a,nil) should be a")
	}
	if WeakerOf(nil, a, now) != a {
		t.Error("(nil,a) should be a")
	}
}

func TestCloneForTopic_CarriesCurveState(t *testing.T) {
	last := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	src := mustNewScore(t, "t1", "g1", "algebra", last, 8.0)
	// force distinct snapshot values so the carry is unambiguous.
	src.RetentionScore = 0.42
	src.ReviewCount = 5

	clone, err := CloneForTopic(src, "geometry", now)
	if err != nil {
		t.Fatalf("CloneForTopic: %v", err)
	}
	if clone.Strength != src.Strength {
		t.Errorf("Strength = %v, want %v", clone.Strength, src.Strength)
	}
	if clone.RetentionScore != src.RetentionScore {
		t.Errorf("RetentionScore = %v, want %v", clone.RetentionScore, src.RetentionScore)
	}
	if clone.ReviewCount != src.ReviewCount {
		t.Errorf("ReviewCount = %d, want %d", clone.ReviewCount, src.ReviewCount)
	}
	if !clone.LastReviewedAt.Equal(src.LastReviewedAt) {
		t.Errorf("LastReviewedAt = %v, want %v", clone.LastReviewedAt, src.LastReviewedAt)
	}
	if clone.TenantID != "t1" || clone.GCID != "g1" {
		t.Errorf("tenant/gcid = (%s,%s), want (t1,g1)", clone.TenantID, clone.GCID)
	}
	if clone.TopicID != "geometry" {
		t.Errorf("TopicID = %q, want geometry", clone.TopicID)
	}
	if !clone.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want now %v", clone.UpdatedAt, now)
	}
	if clone.ScoreID == "" || clone.ScoreID == src.ScoreID {
		t.Errorf("ScoreID = %q, want a fresh non-empty id", clone.ScoreID)
	}
	if clone.DeletedAt != nil {
		t.Errorf("DeletedAt = %v, want nil (a clone is a live row)", clone.DeletedAt)
	}
	// src must be untouched.
	if src.TopicID != "algebra" {
		t.Errorf("src.TopicID = %q, want algebra (src mutated)", src.TopicID)
	}
}

func TestCloneForTopic_Guards(t *testing.T) {
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	src := mustNewScore(t, "t1", "g1", "algebra", now, 1.0)
	if _, err := CloneForTopic(nil, "geometry", now); !errors.Is(err, ErrInvalidScore) {
		t.Errorf("nil src: err = %v, want ErrInvalidScore", err)
	}
	if _, err := CloneForTopic(src, "", now); !errors.Is(err, ErrInvalidScore) {
		t.Errorf("blank topic: err = %v, want ErrInvalidScore", err)
	}
	if _, err := CloneForTopic(src, "   ", now); !errors.Is(err, ErrInvalidScore) {
		t.Errorf("whitespace topic: err = %v, want ErrInvalidScore", err)
	}
}
