// memory_provenance_test.go — CHO-2192 (ADR-231 D4): the RECALL path must carry
// the provenance CHO-2179 persisted.
//
// CHO-2185 widened the VIEW path (companionmind.EpisodicMemory) so a learner who
// re-opens a research note sees its sources. It did NOT widen the RECALL path —
// so when the Companion vector-recalls that same note into a chat turn and
// restates the claim, the learner gets an assertion with NO source at all. This
// package's MemoryRow is the type that recall path returns.
//
// Every assertion here is a PRESENCE assertion, deliberately: the defect is that
// provenance ARRIVES NOWHERE, so a test asserting an absence would encode the bug
// as the spec. → [[reusable_gotcha_absence_assertion_can_encode_the_bug]]
package companion

import "testing"

// IsEmpty must be TOTAL over the contract — nil, an empty husk, and each
// single-channel case. The wire rule downstream is "attribution is emitted IFF
// there is provenance", so a husk must be indistinguishable from NULL. A husk
// that leaked through would render an attribution block with nothing in it,
// implying a web search that never happened.
//
// Mirrors companionmind.Provenance.IsEmpty — same rule, the two bounded packages
// each own their read model.
func TestMemoryProvenance_IsEmpty_IsTotalOverTheContract(t *testing.T) {
	cases := []struct {
		name string
		p    *MemoryProvenance
		want bool
	}{
		{"nil — pre-0094 note, genuinely unrecorded", nil, true},
		{"empty husk — both channels blank", &MemoryProvenance{}, true},
		{
			"queries only — a search that returned nothing is still a search",
			&MemoryProvenance{WebSearchQueries: []string{"how warm should an egg be"}},
			false,
		},
		{
			"citations only",
			&MemoryProvenance{Citations: []MemoryCitation{{Domain: "cdc.gov"}}},
			false,
		},
		{
			"both channels",
			&MemoryProvenance{
				WebSearchQueries: []string{"how warm should an egg be"},
				Citations:        []MemoryCitation{{Domain: "cdc.gov"}},
			},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A recalled memory must be able to CARRY its provenance. Without this field the
// pg adapter has nowhere to put source_metadata and the chat handler has nothing
// to attribute — which is precisely the CHO-2192 defect.
func TestMemoryRow_CarriesProvenance(t *testing.T) {
	row := MemoryRow{
		ID:          "01970000-0000-7000-d000-000000000001",
		MemoryType:  MemoryTypeResearch,
		ContentText: "an incubating egg is held near 37.5C",
		Provenance: &MemoryProvenance{
			WebSearchQueries: []string{"how warm should an egg be"},
			Citations:        []MemoryCitation{{Domain: "cdc.gov", Title: "Incubation basics"}},
		},
	}
	if row.Provenance == nil {
		t.Fatal("MemoryRow.Provenance = nil; the recall path must carry provenance")
	}
	if row.Provenance.IsEmpty() {
		t.Error("Provenance.IsEmpty() = true; want false — the row was grounded")
	}
	if got := row.Provenance.Citations[0].Domain; got != "cdc.gov" {
		t.Errorf("citation domain = %q; want cdc.gov (the DURABLE identity)", got)
	}
}

// IsGrounded is the emit rule for the whole feature: a turn shows attribution IFF
// it recalled a GROUNDED note. It keys on memory_type, NOT on whether provenance
// happens to be present — a research note written before mig 0094 IS grounded, it
// simply has no recoverable sources, and the learner must be told that rather than
// left to read our silence as "no web search happened".
func TestMemoryRow_IsGrounded_KeysOnMemoryType_NotOnProvenancePresence(t *testing.T) {
	cases := []struct {
		name string
		row  MemoryRow
		want bool
	}{
		{
			"research note WITH provenance",
			MemoryRow{MemoryType: MemoryTypeResearch, Provenance: &MemoryProvenance{
				Citations: []MemoryCitation{{Domain: "cdc.gov"}},
			}},
			true,
		},
		{
			"research note WITHOUT provenance (pre-0094) — still grounded",
			MemoryRow{MemoryType: MemoryTypeResearch},
			true,
		},
		{"chat_turn — never grounded", MemoryRow{MemoryType: "chat_turn"}, false},
		{"recap — never grounded", MemoryRow{MemoryType: "recap"}, false},
		{"ceremony — never grounded", MemoryRow{MemoryType: "ceremony"}, false},
		{"empty type — not grounded", MemoryRow{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.IsGrounded(); got != tc.want {
				t.Errorf("IsGrounded() = %v, want %v", got, tc.want)
			}
		})
	}
}
