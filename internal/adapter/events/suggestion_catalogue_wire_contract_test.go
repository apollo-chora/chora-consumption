// suggestion_catalogue_wire_contract_test.go - the ONE test that can catch a
// silent Go/Python drift on the ADR-245 catalogue.
//
// WHY A SHARED FIXTURE RATHER THAN TWO ASSERTIONS. chora-consumption WRITES
// `relevance` into the concept_suggestion.requested.v1 payload and the fog
// orchestrator READS it. Both sides can be independently green forever while
// the key names disagree: the Go side asserts the key it writes, the Python
// side hand-fills a dict with the key it reads, and no test anywhere holds the
// two together. That is the failure mode the project has already been bitten by
// (a template keyed on a field the wire never carries renders its fallback
// 100% of the time, and hand-filled fixtures pass forever).
//
// So the wire is a COMMITTED ARTIFACT. This test asserts the artifact is
// exactly what the Go producer emits. The fog's
// tests/unit/test_wire_contract_catalogue.py reads the SAME file and asserts
// its renderer bands it. Rename the key on either side and one of the two
// fails: regenerate the fixture and the Python test breaks, or leave it and
// this test breaks.
//
// The fixture used to live in the CONSUMER's tree (the fog orchestrator) so the
// reader could see it. ADR-254 D13 (2026-08-22) retired the fog orchestrator
// and deleted its tree (7dfacb733); the consumer of this wire is now the
// kennel's kg_explore LaneContract (chora-ai-kernel-orchestrator
// single_agent_workflow.py), so the artifact now lives HERE, in the producer's
// testdata, byte-identical to the last committed fog copy, and the kennel's
// unit test reads this file by path. Rename the key on either side and one of
// the two still fails.
package events

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// wireContractFixture is read by BOTH services: this producer test and the
// kennel's wire-contract test (by path into this package's testdata).
const wireContractFixture = "testdata/concept_suggestion_requested.wire.json"

// wireContractInput is the single source both the fixture and this assertion
// derive from. Deliberately banded AND ordered on-theme-first, because that is
// what the fog is being asked to trust.
func wireContractInput() SuggestionRequestInput {
	return SuggestionRequestInput{
		TenantID:       "11111111-1111-7111-8111-111111111111",
		LearnerGCID:    "01970000-0000-7000-8000-0000000000aa",
		CompanionID:    "01970000-0000-7000-a000-0000000000f1",
		MapTheme:       "Fractions",
		FocalConceptID: "01970000-0000-7000-b000-0000000000c1",
		FocalTitle:     "Comparing fractions",
		FocalAtomRefs:  []string{},
		Existing:       []ExistingConcept{{ConceptID: "01970000-0000-7000-b000-0000000000c2", Title: "Numerator"}},
		AtomCatalogue: []CatalogueAtom{
			{AtomID: "01970000-0000-7000-a000-0000000000f1", Title: "Comparing Fractions: 2/3 vs 3/5",
				AtomType: "mcq", TopicTags: []string{}, Relevance: "on_theme"},
			{AtomID: "01970000-0000-7000-a000-0000000000f2", Title: "Photosynthesis and the Calvin cycle",
				AtomType: "mcq", TopicTags: []string{"biology"}, Relevance: "off_theme"},
		},
		RequestedAt:   time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC),
		RequestSource: SuggestionSourceLearnerRequest,
		// ADR-247 Capability B (CHO-2328): the learner-aware fog inputs. The fixture
		// carries the full shape (a non-null weakness object + a non-empty ancestor
		// chain) so a Go/Python drift on any of the four keys breaks the golden
		// compare here and the fog's test_wire_contract_catalogue read on main.
		SubGoal:   "Compare two fractions by finding a common denominator",
		GoalTitle: "Fractions",
		Ancestors: []string{"Fraction basics", "Number sense"},
		Weakness: &WeaknessPayload{
			Descriptor:     "Compares fractions by numerator alone, ignoring the denominator",
			Misconceptions: []string{"bigger numerator means bigger fraction"},
			Evidence:       []string{"picked 3/8 over 1/2"},
		},
	}
}

// TestConceptSuggestionWireContract_MatchesCommittedFixture fails whenever the
// Go producer's output stops matching the artifact the fog reads.
//
// Regenerate with: go test ./internal/adapter/events/ -run WireContract -update
func TestConceptSuggestionWireContract_MatchesCommittedFixture(t *testing.T) {
	got, err := json.MarshalIndent(SuggestionRequestPayload(wireContractInput()), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	if *updateWireFixture {
		if err := os.WriteFile(wireContractFixture, got, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("fixture regenerated: %s", wireContractFixture)
		return
	}

	want, err := os.ReadFile(wireContractFixture)
	if err != nil {
		t.Fatalf("read fixture (regenerate with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("the concept_suggestion.requested.v1 wire changed.\n"+
			"The fog orchestrator reads this artifact; a rename here renders its\n"+
			"fallback silently. Regenerate with -update, then re-run the fog's\n"+
			"test_wire_contract_catalogue.py before assuming it is safe.\n\ngot:\n%s\nwant:\n%s",
			got, want)
	}
}

// The band must survive marshalling under its exact wire key. Asserted
// separately from the golden compare so a failure names the cause rather than
// dumping a diff.
func TestConceptSuggestionWireContract_CarriesRelevanceKey(t *testing.T) {
	p := SuggestionRequestPayload(wireContractInput())
	cat, ok := p["atom_catalogue"].([]map[string]any)
	if !ok || len(cat) == 0 {
		t.Fatalf("atom_catalogue = %#v", p["atom_catalogue"])
	}
	v, ok := cat[0]["relevance"]
	if !ok {
		t.Fatal("catalogue entry has no `relevance` key: the fog renders the unbanded prompt")
	}
	if v != "on_theme" {
		t.Errorf("relevance = %v, want on_theme", v)
	}
}

// An unbanded catalogue must still carry the key, as "". A missing key and an
// empty value both mean "unscored" to the fog, but a STABLE shape keeps its
// branch keyed on the value rather than on presence.
func TestConceptSuggestionWireContract_UnbandedCarriesEmptyRelevance(t *testing.T) {
	p := SuggestionRequestPayload(SuggestionRequestInput{
		AtomCatalogue: []CatalogueAtom{{AtomID: "a1", Title: "T", AtomType: "mcq"}},
	})
	cat := p["atom_catalogue"].([]map[string]any)
	v, ok := cat[0]["relevance"]
	if !ok {
		t.Fatal("unbanded entry dropped the `relevance` key entirely")
	}
	if v != "" {
		t.Errorf("relevance = %q, want empty for an unscored atom", v)
	}
}

// ADR-247 Capability B (CHO-2328): the four learner-aware keys must appear on the
// wire with the fog consumer's exact names + shapes. Asserted separately from the
// golden compare so a failure names the offending key rather than dumping a diff.
func TestConceptSuggestionWireContract_CarriesCapabilityBKeys(t *testing.T) {
	p := SuggestionRequestPayload(wireContractInput())

	if got := p["sub_goal"]; got != "Compare two fractions by finding a common denominator" {
		t.Errorf("sub_goal = %v", got)
	}
	if got := p["goal_title"]; got != "Fractions" {
		t.Errorf("goal_title = %v", got)
	}

	anc, ok := p["ancestors"].([]string)
	if !ok {
		t.Fatalf("ancestors is not a []string: %#v", p["ancestors"])
	}
	if len(anc) != 2 || anc[0] != "Fraction basics" || anc[1] != "Number sense" {
		t.Errorf("ancestors = %#v, want nearest-parent-first [Fraction basics, Number sense]", anc)
	}

	w, ok := p["weakness"].(map[string]any)
	if !ok {
		t.Fatalf("weakness is not an object: %#v", p["weakness"])
	}
	if w["descriptor"] != "Compares fractions by numerator alone, ignoring the denominator" {
		t.Errorf("weakness.descriptor = %v", w["descriptor"])
	}
	misc, ok := w["misconceptions"].([]string)
	if !ok || len(misc) != 1 || misc[0] != "bigger numerator means bigger fraction" {
		t.Errorf("weakness.misconceptions = %#v", w["misconceptions"])
	}
	evid, ok := w["evidence"].([]string)
	if !ok || len(evid) != 1 || evid[0] != "picked 3/8 over 1/2" {
		t.Errorf("weakness.evidence = %#v", w["evidence"])
	}
}

// The weakness key must marshal to JSON null (not {}) when no weakness was
// diagnosed, and ancestors must marshal to [] (not null) when empty. Both are the
// fog's "nothing to add" signals, and both must be present so the fog reads a
// stable shape (its handler keys on the VALUE, never on presence).
func TestConceptSuggestionWireContract_AbsentWeaknessIsNullEmptyAncestorsIsArray(t *testing.T) {
	p := SuggestionRequestPayload(SuggestionRequestInput{
		SubGoal:   "",
		GoalTitle: "",
		Ancestors: nil, // an unresolved lineage
		Weakness:  nil, // no diagnosis
	})

	wv, ok := p["weakness"]
	if !ok {
		t.Fatal("weakness key dropped entirely: the fog reads req.weakness and expects the key present")
	}
	// The contract is the WIRE, not the in-memory value: a nil map[string]any held
	// in an interface is non-nil under ==, yet marshals to JSON null (which is what
	// the fog's _normalise_weakness treats as "no diagnosis"). Assert the marshalled
	// form, never the interface identity.
	if wb, err := json.Marshal(wv); err != nil {
		t.Fatalf("marshal weakness: %v", err)
	} else if string(wb) != "null" {
		t.Errorf("weakness marshalled to %s, want null when no weakness is diagnosed", wb)
	}

	av, ok := p["ancestors"]
	if !ok {
		t.Fatal("ancestors key dropped entirely")
	}
	anc, ok := av.([]string)
	if !ok {
		t.Fatalf("ancestors is not a []string: %#v", av)
	}
	if anc == nil {
		t.Error("ancestors is nil; want a non-nil empty slice so it marshals to [] not null")
	}
	if len(anc) != 0 {
		t.Errorf("ancestors = %#v, want empty", anc)
	}

	// Marshal the whole payload and confirm the JSON literally carries the two
	// shapes the fog depends on (a golden compare would catch this too, but this
	// pins the exact bytes independent of the fixture).
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	if !strings.Contains(js, `"weakness":null`) {
		t.Errorf("payload JSON does not carry \"weakness\":null: %s", js)
	}
	if !strings.Contains(js, `"ancestors":[]`) {
		t.Errorf("payload JSON does not carry \"ancestors\":[]: %s", js)
	}
}
