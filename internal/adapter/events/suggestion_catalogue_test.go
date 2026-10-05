// suggestion_catalogue_test.go - ADR-244 D2 (CHO-2303): the atom catalogue on
// chora.consumption.concept_suggestion.requested.v1.
//
// The KEY NAMES here are the wire contract with the fog orchestrator's
// build_concept_prompt. If they drift, the catalogue renders "(none)" and the
// Companion silently proposes no atoms - which is indistinguishable from the
// pre-ADR-244 behaviour this whole story exists to fix.
package events

import "testing"

func TestSuggestionRequestPayload_CarriesAtomCatalogue(t *testing.T) {
	p := SuggestionRequestPayload(SuggestionRequestInput{
		TenantID: "t1", LearnerGCID: "g1",
		AtomCatalogue: []CatalogueAtom{
			{AtomID: "a1", Title: "Adding unlike fractions", AtomType: "mcq", TopicTags: []string{"fractions"}},
		},
	})
	raw, ok := p["atom_catalogue"]
	if !ok {
		t.Fatal("payload has no atom_catalogue key: the fog will render (none)")
	}
	cat, ok := raw.([]map[string]any)
	if !ok || len(cat) != 1 {
		t.Fatalf("atom_catalogue = %#v, want 1 entry", raw)
	}
	// Exactly the keys the fog's build_concept_prompt reads.
	for _, k := range []string{"atom_id", "title", "atom_type", "topic_tags"} {
		if _, ok := cat[0][k]; !ok {
			t.Errorf("catalogue entry missing %q", k)
		}
	}
	if cat[0]["title"] != "Adding unlike fractions" {
		t.Errorf("title = %v", cat[0]["title"])
	}
}

// An absent catalogue must serialise as an empty list, not null: the fog does
// `for a in atom_catalogue or []`, and a stable shape keeps that honest.
func TestSuggestionRequestPayload_EmptyCatalogueIsEmptyList(t *testing.T) {
	p := SuggestionRequestPayload(SuggestionRequestInput{TenantID: "t1", LearnerGCID: "g1"})
	cat, ok := p["atom_catalogue"].([]map[string]any)
	if !ok {
		t.Fatalf("atom_catalogue = %#v, want an empty slice", p["atom_catalogue"])
	}
	if len(cat) != 0 {
		t.Fatalf("want empty, got %d", len(cat))
	}
}
