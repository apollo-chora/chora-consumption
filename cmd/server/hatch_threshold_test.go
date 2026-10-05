// hatch_threshold_test.go — F-I1.2 (CHO-2088, ADR-228): boot-time resolution
// of COMPANION_HATCH_EXP_THRESHOLD. Empty ⇒ the domain default; a non-empty
// value must parse and satisfy 0 < t < 50 or the boot fails loud (no silent
// clamp of a misconfigured incubation threshold).
package main

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestParseHatchExpThreshold(t *testing.T) {
	valid := []struct {
		raw  string
		want int
	}{
		{"", growth.DefaultHatchExpThreshold},
		{"  ", growth.DefaultHatchExpThreshold},
		{"30", 30},
		{"1", 1},
		{"49", 49},
		{" 33 ", 33},
	}
	for _, tc := range valid {
		got, err := parseHatchExpThreshold(tc.raw)
		if err != nil {
			t.Errorf("parseHatchExpThreshold(%q): unexpected error %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseHatchExpThreshold(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}

	invalid := []string{"0", "50", "51", "-5", "abc", "12.5"}
	for _, raw := range invalid {
		if _, err := parseHatchExpThreshold(raw); err == nil {
			t.Errorf("parseHatchExpThreshold(%q): expected fail-loud error, got nil", raw)
		}
	}
}
