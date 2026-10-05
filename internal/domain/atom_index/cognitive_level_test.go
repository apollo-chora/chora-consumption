// cognitive_level_test.go — CHO-2082 (WS-C3): the atom_index cognitive_level
// projection. Values are ORIGINAL-Bloom lowercase labels (the campaign
// Rung.Label() vocabulary); "" = unknown and never level-matches, so the
// question lane honestly falls through to gap generation (AC1/AC2).
package atom_index

import "testing"

func TestNormalizeCognitiveLevel(t *testing.T) {
	cases := map[string]string{
		"application":                   "application",
		"  Application ":                "application",
		"COGNITIVE_LEVEL_APPLICATION":   "application",
		"COGNITIVE_LEVEL_SYNTHESIS":     "synthesis",
		"COGNITIVE_LEVEL_UNSPECIFIED":   "",
		"":                              "",
		"bogus":                         "",
		"COGNITIVE_LEVEL_COMPREHENSION": "comprehension",
	}
	for in, want := range cases {
		if got := NormalizeCognitiveLevel(in); got != want {
			t.Errorf("NormalizeCognitiveLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchesCognitiveLevel(t *testing.T) {
	a := &AtomIndex{AtomID: "a", CognitiveLevel: "application"}
	if !a.MatchesCognitiveLevel("application") {
		t.Fatal("exact label must match")
	}
	if a.MatchesCognitiveLevel("analysis") {
		t.Fatal("different label must not match")
	}
	unknown := &AtomIndex{AtomID: "b"}
	if unknown.MatchesCognitiveLevel("application") {
		t.Fatal("unknown level must NEVER level-match (honest gap-gen)")
	}
	if a.MatchesCognitiveLevel("") {
		t.Fatal("blank target must never match")
	}
}
