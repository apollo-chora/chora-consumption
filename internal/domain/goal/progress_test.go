// progress_test.go — RED tests for the pure verified-progress computation
// (CHO-1921). ProgressPercent feeds the A+ Goal %-ring: the share of a Goal's
// concept set that the learner has VERIFIABLY mastered (grown Growth Edges).
// Pure-domain: no clock, no I/O — just round + clamp.
package goal

import "testing"

func TestProgressPercent(t *testing.T) {
	cases := []struct {
		name            string
		mastered, total int
		want            int
	}{
		{"nothing to verify", 0, 0, 0},
		{"half", 1, 2, 50},
		{"all", 3, 3, 100},
		{"one third rounds down", 1, 3, 33},
		{"two thirds rounds up", 2, 3, 67},
		{"over-count clamps to 100", 5, 4, 100},
		{"negative mastered guards to 0", -1, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProgressPercent(tc.mastered, tc.total); got != tc.want {
				t.Errorf("ProgressPercent(%d, %d) = %d, want %d", tc.mastered, tc.total, got, tc.want)
			}
		})
	}
}
