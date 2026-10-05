// confirm_test.go — CHO-2040 (owner ruling R8-5): pure core of the ceremony
// confirm hook. The FE echoes the CHO-2038 POST response (the ticked edges)
// back to /ceremony/edge-scout/confirm; remediate ticks feed
// chora.consumption.weakness.analyzed.v1 (→ per-Companion RAG), explore ticks
// become ONE ceremony memory-note. This file covers the pure helpers:
// validation, the remediate/explore split, the DETERMINISTIC synthetic
// upload id (the subscriber's inbox dedup key — a re-confirm must never
// double-feed), and the plain-factual explore note.
package edgescout

import (
	"strings"
	"testing"
)

const (
	cfTenant = "01970000-0000-7000-8000-0000000000c1"
	cfGoal   = "01970000-aaaa-7000-8000-00000000e5c0"
)

func cfEdge(id, title string, intent Intent) ConfirmEdge {
	return ConfirmEdge{ConceptID: id, Title: title, Intent: intent}
}

const (
	cfCID1 = "01970000-cccc-7000-8000-000000000001"
	cfCID2 = "01970000-cccc-7000-8000-000000000002"
	cfCID3 = "01970000-cccc-7000-8000-000000000003"
)

// ----------------------------------------------------------------------------
// ValidateConfirmEdges
// ----------------------------------------------------------------------------

func TestValidateConfirmEdges_Valid(t *testing.T) {
	err := ValidateConfirmEdges([]ConfirmEdge{
		cfEdge(cfCID1, "Recursion base cases", IntentRemediate),
		cfEdge(cfCID2, "Tail calls", IntentExplore),
	})
	if err != nil {
		t.Fatalf("ValidateConfirmEdges = %v, want nil", err)
	}
}

func TestValidateConfirmEdges_Rejections(t *testing.T) {
	overlong := strings.Repeat("x", MaxConfirmTitleLen+1)
	cases := map[string][]ConfirmEdge{
		"empty":            {},
		"over cap":         make([]ConfirmEdge, MaxCandidates+1),
		"blank title":      {cfEdge(cfCID1, "   ", IntentRemediate)},
		"overlong title":   {cfEdge(cfCID1, overlong, IntentRemediate)},
		"bad intent":       {{ConceptID: cfCID1, Title: "T", Intent: Intent("boost")}},
		"empty intent":     {{ConceptID: cfCID1, Title: "T"}},
		"blank concept id": {cfEdge("  ", "T", IntentExplore)},
		"duplicate concept id": {
			cfEdge(cfCID1, "A", IntentRemediate),
			cfEdge(cfCID1, "B", IntentExplore),
		},
	}
	// Fill the over-cap case with valid-looking edges so ONLY the cap trips.
	for i := range cases["over cap"] {
		cases["over cap"][i] = cfEdge(cfCID1[:len(cfCID1)-1]+string(rune('a'+i)), "T", IntentExplore)
	}
	for name, edges := range cases {
		if err := ValidateConfirmEdges(edges); err == nil {
			t.Errorf("%s: ValidateConfirmEdges = nil, want error", name)
		}
	}
}

// ----------------------------------------------------------------------------
// SplitConfirm
// ----------------------------------------------------------------------------

func TestSplitConfirm_Partitions(t *testing.T) {
	r, e := SplitConfirm([]ConfirmEdge{
		cfEdge(cfCID1, "Recursion base cases", IntentRemediate),
		cfEdge(cfCID2, "Tail calls", IntentExplore),
		cfEdge(cfCID3, "Unit conversions", IntentRemediate),
	})
	if len(r) != 2 || r[0].Title != "Recursion base cases" || r[1].Title != "Unit conversions" {
		t.Errorf("remediate = %+v", r)
	}
	if len(e) != 1 || e[0].Title != "Tail calls" {
		t.Errorf("explore = %+v", e)
	}
}

// ----------------------------------------------------------------------------
// ConfirmUploadID — the deterministic dedup carrier
// ----------------------------------------------------------------------------

func TestConfirmUploadID_DeterministicAndOrderInsensitive(t *testing.T) {
	a := ConfirmUploadID(cfTenant, cfGoal, []string{cfCID1, cfCID2})
	b := ConfirmUploadID(cfTenant, cfGoal, []string{cfCID2, cfCID1})
	if a != b {
		t.Fatalf("upload id must be order-insensitive: %q vs %q", a, b)
	}
	if c := ConfirmUploadID(cfTenant, cfGoal, []string{cfCID1, cfCID2}); c != a {
		t.Fatalf("upload id must be stable across calls: %q vs %q", c, a)
	}
}

func TestConfirmUploadID_VariesWithInputs(t *testing.T) {
	base := ConfirmUploadID(cfTenant, cfGoal, []string{cfCID1})
	for name, got := range map[string]string{
		"different tenant":   ConfirmUploadID("01970000-0000-7000-8000-0000000000c2", cfGoal, []string{cfCID1}),
		"different goal":     ConfirmUploadID(cfTenant, "01970000-aaaa-7000-8000-00000000e5c1", []string{cfCID1}),
		"different tick set": ConfirmUploadID(cfTenant, cfGoal, []string{cfCID2}),
		"superset tick set":  ConfirmUploadID(cfTenant, cfGoal, []string{cfCID1, cfCID2}),
	} {
		if got == base {
			t.Errorf("%s: upload id collided with the base id %q", name, base)
		}
	}
}

func TestConfirmUploadID_UUIDShaped(t *testing.T) {
	// The synthetic id flows into the analyzed subscriber's JobCompleter,
	// whose UPDATE compares it against a UUID column — a non-UUID string
	// would 22P02 the whole event into a NACK loop.
	id := ConfirmUploadID(cfTenant, cfGoal, []string{cfCID1})
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("upload id %q is not UUID-shaped", id)
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("upload id %q has a non-hex rune %q at %d", id, r, i)
		}
	}
}

// ----------------------------------------------------------------------------
// ComposeExploreNote — plain and factual
// ----------------------------------------------------------------------------

func TestComposeExploreNote_TitlesAndAnchor(t *testing.T) {
	note := ComposeExploreNote([]string{"Tail calls", "Graph traversal"}, "Flood risk engineering")
	for _, want := range []string{"chose to explore", "Tail calls", "Graph traversal", "Flood risk engineering", "binding ceremony"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
}

func TestComposeExploreNote_AnchorFallback(t *testing.T) {
	note := ComposeExploreNote([]string{"Tail calls"}, "")
	if !strings.Contains(note, "their goal") {
		t.Errorf("anchorless note must fall back to \"their goal\":\n%s", note)
	}
}
