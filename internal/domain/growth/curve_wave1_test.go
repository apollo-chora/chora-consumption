// curve_wave1_test.go — F-I3 (CHO-2090, ADR-228 D4 Wave 1): the three NEW
// verified companion-EXP sources join the canonical curve.go vocabulary with
// flat per-event pricing + daily caps (ADR-203 L16 — verified events only,
// NO difficulty/LLM scaling):
//
//	weakness_grown     B  10 / 30  — a Growth Edge recovered (weakness.grown.v1)
//	submission_graded  A  30 / 60  — an R+ assessment graded (delivery binary)
//	module_completed   B  15 / 45  — a W7 StudentModuleProgress module completed
//
// Values parity-pinned against identity migration 0037 (exp_source_def) per
// the ADR-218 D6 contract — the identity side pins the same literals in
// companion_exp_wave1_sources_migration_test.go. RED-first.
package growth

import "testing"

// wave1Sources pins the F-I3 vocabulary + pricing (value, daily cap).
var wave1Sources = []struct {
	source string
	value  int
	cap    int
}{
	{"weakness_grown", 10, 30},
	{"submission_graded", 30, 60},
	{"module_completed", 15, 45},
}

// TestWave1SourcesAreCanonical — the three ADR-228 D4 Wave-1 tokens are
// canonical (IsValidSource gates AwardExp; a non-canonical token can never
// award — L16).
func TestWave1SourcesAreCanonical(t *testing.T) {
	for _, w := range wave1Sources {
		if !IsValidSource(w.source) {
			t.Errorf("IsValidSource(%q) = false, want true (ADR-228 D4 Wave 1)", w.source)
		}
	}
}

// TestWave1ValuesAndCaps — flat per-event value + daily cap for each Wave-1
// source (documented-fallback side of the parity contract).
func TestWave1ValuesAndCaps(t *testing.T) {
	for _, w := range wave1Sources {
		if got := ExpPerEventForSource(w.source); got != w.value {
			t.Errorf("ExpPerEventForSource(%q) = %d, want %d (identity 0037 parity)", w.source, got, w.value)
		}
		if got := DailyCapForSource(w.source); got != w.cap {
			t.Errorf("DailyCapForSource(%q) = %d, want %d (identity 0037 parity)", w.source, got, w.cap)
		}
	}
}

// TestWave1DailyCapClamps — the per-source daily cap clamps the running
// day's awards for each new source: a full award inside the cap, a partial
// clamp at the boundary, and a zero award once the cap is reached.
func TestWave1DailyCapClamps(t *testing.T) {
	for _, w := range wave1Sources {
		t.Run(w.source, func(t *testing.T) {
			// Fresh day: the full flat value awards; capHit only if the single
			// award already exhausts the cap.
			got, capHit := ClampDelta(w.source, 0, w.value)
			if got != w.value {
				t.Errorf("fresh-day clamp = %d, want full value %d", got, w.value)
			}
			if wantHit := w.value >= w.cap; capHit != wantHit {
				t.Errorf("fresh-day capHit = %v, want %v", capHit, wantHit)
			}
			// One unit of headroom left: the award clamps to the remainder.
			got, capHit = ClampDelta(w.source, w.cap-1, w.value)
			if got != 1 || !capHit {
				t.Errorf("boundary clamp = (%d, %v), want (1, true)", got, capHit)
			}
			// Cap already reached: zero award, capHit=true.
			got, capHit = ClampDelta(w.source, w.cap, w.value)
			if got != 0 || !capHit {
				t.Errorf("post-cap clamp = (%d, %v), want (0, true)", got, capHit)
			}
		})
	}
}

// TestWave1FallbackRuleEnabled — the documented fallback resolves each
// Wave-1 source enabled with its flat value + cap (the resolver-outage
// behaviour must match identity 0037 exactly).
func TestWave1FallbackRuleEnabled(t *testing.T) {
	for _, w := range wave1Sources {
		rule, err := FallbackExpRule(w.source)
		if err != nil {
			t.Errorf("FallbackExpRule(%q) error: %v", w.source, err)
			continue
		}
		want := ExpRule{Value: w.value, DailyCap: w.cap, Enabled: true}
		if rule != want {
			t.Errorf("FallbackExpRule(%q) = %+v, want %+v", w.source, rule, want)
		}
	}
}
