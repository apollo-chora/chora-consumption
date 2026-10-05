// memory_provenance_test.go — CHO-2185 (ADR-231 D4): the durable provenance
// CHO-2179 persists must survive the READ back out to the learner.
//
// The invariant under test is a PRESENCE one, deliberately. CHO-2179 wrote
// source_metadata that nothing could read; asserting "the reader drops nothing"
// is the whole point, so every assertion here checks that data ARRIVES — never
// that it is absent (an absence assertion is how a live defect gets encoded as
// the spec).
package companionmind

import (
	"testing"
	"time"
)

// IsEmpty must be TOTAL over the contract: nil, an empty husk, and each
// single-channel case. The wire rule downstream is "sourceMetadata is present
// IFF there is provenance", so a husk must be indistinguishable from NULL —
// otherwise the FE renders an empty attribution block that implies a search
// which never happened.
func TestProvenance_IsEmpty_IsTotalOverTheContract(t *testing.T) {
	cases := []struct {
		name string
		p    *Provenance
		want bool
	}{
		{"nil — pre-0094 note, genuinely unrecorded", nil, true},
		{"empty husk — both channels blank", &Provenance{}, true},
		{
			"queries only — a search that returned nothing is still a search",
			&Provenance{WebSearchQueries: []string{"shape of the earth"}},
			false,
		},
		{
			"citations only",
			&Provenance{Citations: []ProvenanceCitation{{Domain: "nasa.gov"}}},
			false,
		},
		{
			"both channels",
			&Provenance{
				WebSearchQueries: []string{"shape of the earth"},
				Citations:        []ProvenanceCitation{{Domain: "nasa.gov"}},
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

// A research note's provenance must survive assembly intact — domain, title and
// snippet all reach the view. The persisted record deliberately carries NO uri
// (it expires), so domain is the only durable handle the learner gets.
func TestAssembleView_ResearchNotesCarryProvenanceIntact(t *testing.T) {
	now := time.Now().UTC()
	notes := []EpisodicMemory{{
		ID:         "mem-1",
		MemoryType: MemoryTypeResearch,
		Content:    "The Earth is an oblate spheroid.",
		CreatedAt:  now,
		Provenance: &Provenance{
			WebSearchQueries: []string{"shape of the earth", "oblate spheroid"},
			Citations: []ProvenanceCitation{
				{Domain: "nasa.gov", Title: "Earth's true shape", Snippet: "…bulges at the equator…"},
			},
		},
	}}

	v := AssembleView(InstanceContext{CompanionID: "fam-1"}, nil, nil, notes)

	if len(v.ResearchNotes) != 1 {
		t.Fatalf("ResearchNotes len = %d, want 1", len(v.ResearchNotes))
	}
	got := v.ResearchNotes[0]
	if got.Provenance == nil {
		t.Fatal("provenance dropped by AssembleView — the CHO-2179 bug, one layer up")
	}
	if n := len(got.Provenance.WebSearchQueries); n != 2 {
		t.Errorf("WebSearchQueries len = %d, want 2", n)
	}
	if n := len(got.Provenance.Citations); n != 1 {
		t.Fatalf("Citations len = %d, want 1", n)
	}
	c := got.Provenance.Citations[0]
	if c.Domain != "nasa.gov" {
		t.Errorf("Domain = %q, want nasa.gov (the DURABLE handle)", c.Domain)
	}
	if c.Title != "Earth's true shape" {
		t.Errorf("Title = %q, want the headline", c.Title)
	}
	if c.Snippet == "" {
		t.Error("Snippet dropped — it is part of the durable record")
	}
}

// Nil research notes must normalise to a non-nil empty slice: the wire shape has
// to be a stable [], never null, so the FE's empty-state branch is the only one
// that can fire.
func TestAssembleView_NilResearchNotesNormaliseToEmptySlice(t *testing.T) {
	v := AssembleView(InstanceContext{CompanionID: "fam-1"}, nil, nil, nil)

	if v.ResearchNotes == nil {
		t.Fatal("ResearchNotes = nil, want a non-nil empty slice (stable wire shape)")
	}
	if len(v.ResearchNotes) != 0 {
		t.Errorf("ResearchNotes len = %d, want 0", len(v.ResearchNotes))
	}
}

// A pre-0094 research note has genuinely unrecoverable provenance. It must still
// reach the learner — with nil provenance, which the adapter renders as an honest
// "we did not record this", never as a fabricated or blank attribution.
func TestAssembleView_PreMigrationNoteSurvivesWithNilProvenance(t *testing.T) {
	notes := []EpisodicMemory{{
		ID:         "mem-old",
		MemoryType: MemoryTypeResearch,
		Content:    "A note written before mig 0094.",
		CreatedAt:  time.Now().UTC(),
		Provenance: nil,
	}}

	v := AssembleView(InstanceContext{CompanionID: "fam-1"}, nil, nil, notes)

	if len(v.ResearchNotes) != 1 {
		t.Fatalf("ResearchNotes len = %d, want 1 — the note itself is not lost", len(v.ResearchNotes))
	}
	if v.ResearchNotes[0].Provenance != nil {
		t.Error("provenance must stay nil — inventing one would be a lie")
	}
	if v.ResearchNotes[0].Content == "" {
		t.Error("content dropped — the note is still readable, only its provenance is not")
	}
}
