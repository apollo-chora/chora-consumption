// derived_test.go — W3-derived: blended strength scoring for performance-derived
// Growth Edges + the Ebbinghaus recovery path (Merge only RAISES strength; the
// docstring reserves recovery for this separate path).
package learner_weakness

import (
	"testing"
	"time"
)

func TestDerivedStrength_AccuracyOnly(t *testing.T) {
	cases := []struct {
		name     string
		accuracy float64
		want     float64
	}{
		{"all wrong", 0.0, 1.0},
		{"half right", 0.5, 0.5},
		{"all right", 1.0, 0.0},
		{"negative accuracy clamps", -0.5, 1.0},
		{"overflow accuracy clamps", 1.5, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DerivedStrength(tc.accuracy, 0, false)
			if !floatEq(got, tc.want) {
				t.Fatalf("DerivedStrength(%v, _, false) = %v, want %v", tc.accuracy, got, tc.want)
			}
		})
	}
}

func TestDerivedStrength_BlendsRetention(t *testing.T) {
	// 0.7*miss + 0.3*forget per the documented weights.
	got := DerivedStrength(0.5, 0.5, true)
	if !floatEq(got, 0.7*0.5+0.3*0.5) {
		t.Fatalf("blend = %v, want 0.5", got)
	}
	// Full retention contributes zero forgetting.
	got = DerivedStrength(0.5, 1.0, true)
	if !floatEq(got, 0.35) {
		t.Fatalf("blend at full retention = %v, want 0.35", got)
	}
	// Zero retention maximises the forgetting term.
	got = DerivedStrength(1.0, 0.0, true)
	if !floatEq(got, 0.3) {
		t.Fatalf("blend at zero retention = %v, want 0.3", got)
	}
	// Out-of-range retention clamps.
	if got := DerivedStrength(1.0, 7.0, true); !floatEq(got, 0.0) {
		t.Fatalf("overflow retention = %v, want 0", got)
	}
}

func TestRecover_LowersStrengthOnly(t *testing.T) {
	w := mustEdge(t, 0.8)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	if !w.Recover(0.4, now) {
		t.Fatal("Recover to a lower strength must apply")
	}
	if !floatEq(w.Strength, 0.4) {
		t.Fatalf("strength = %v, want 0.4", w.Strength)
	}
	if w.Status != StatusActive {
		t.Fatalf("status = %q, want active", w.Status)
	}
	if !w.LastEvidencedAt.Equal(now) || !w.UpdatedAt.Equal(now) {
		t.Fatal("Recover must advance last_evidenced_at + updated_at")
	}

	// A HIGHER strength never applies through Recover (that is Merge's job).
	before := *w
	if w.Recover(0.9, now.Add(time.Hour)) {
		t.Fatal("Recover must not raise strength")
	}
	if !floatEq(w.Strength, before.Strength) || !w.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("a non-applying Recover must not mutate the edge")
	}
}

func TestRecover_ToMasteredFlipsGrown(t *testing.T) {
	w := mustEdge(t, 0.8)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if !w.Recover(0.05, now) {
		t.Fatal("Recover must apply")
	}
	if w.Status != StatusGrown {
		t.Fatalf("status = %q, want grown at strength <= %v", w.Status, MasteredStrengthThreshold)
	}
}

func TestRecover_ClampsInput(t *testing.T) {
	w := mustEdge(t, 0.8)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if !w.Recover(-3, now) {
		t.Fatal("clamped-to-zero Recover must apply")
	}
	if !floatEq(w.Strength, 0) || w.Status != StatusGrown {
		t.Fatalf("strength = %v status = %q, want 0/grown", w.Strength, w.Status)
	}
}

// --- helpers ---

func mustEdge(t *testing.T, strength float64) *LearnerWeakness {
	t.Helper()
	w, err := New(UpsertInput{
		TenantID:     "11111111-1111-7111-8111-111111111111",
		LearnerGCID:  "00000000-0000-7000-8000-000000001999",
		ConceptLabel: "Multiplication Tables",
		Embedding:    []float32{0.1, 0.2},
		Strength:     strength,
		Source:       SourceDerived,
		Now:          time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

func floatEq(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
