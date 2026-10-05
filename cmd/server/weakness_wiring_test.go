// weakness_wiring_test.go — ADR-238 M-D2: pin diagnoseConceptMatchMinCosine, the
// boot-time resolution of the goal-scoped Diagnose concept-match cosine floor
// from CHORA_DIAGNOSE_CONCEPT_MATCH_MIN_COSINE. Unset / blank / unparseable /
// out-of-[0,1] ⇒ the SAFE 0.55 default (a permitted inline sensitivity dial, not
// a secret or URL — feedback_no_inline_config); a valid float in [0,1] passes
// through verbatim. The default's calibration provenance (one Scrum upload,
// 2 nodes) + the "adjust the global env var first" tuning procedure live in
// ADR-238 §Implementation. This closes the coverage gap: the matcher behaviour
// at 0.55 was tested, but the DERIVATION of 0.55 (and its clamp) was not.
package main

import (
	"math"
	"testing"
)

func TestDiagnoseConceptMatchMinCosine(t *testing.T) {
	const def = 0.55
	cases := []struct {
		name string
		raw  string
		want float64
	}{
		{"blank ⇒ default", "", def},
		{"whitespace ⇒ default", "   ", def},
		{"valid mid-range", "0.7", 0.7},
		{"valid with surrounding space", " 0.8 ", 0.8},
		{"valid lower boundary 0", "0", 0},
		{"valid upper boundary 1", "1", 1},
		{"explicit default", "0.55", def},
		{"above range ⇒ default", "1.5", def},
		{"below range ⇒ default", "-0.1", def},
		{"unparseable ⇒ default", "abc", def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CHORA_DIAGNOSE_CONCEPT_MATCH_MIN_COSINE", tc.raw)
			got := diagnoseConceptMatchMinCosine()
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("diagnoseConceptMatchMinCosine() with env=%q = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
