// companion_loadout_card_fields_test.go — RED-first for N2 (tracker row D1):
// the loadout grant row carries what a step card has to show.
//
// The editor's step card is specified to show receives, gives, tools and cost
// (spec section b.4). Every one of those already exists on companion.CatalogEntry
// and NONE of them reached the wire, so the composer could only ever render a
// machine key and a slot count. This widens the projection; it invents nothing.
//
// ⚠ Two fields are deliberately shaped against a trap each:
//
//   - price_units is RESOLVED from the canonical cost map, not copied from the
//     catalogue, because the catalogue carries a price KEY and the FE needs a
//     number. An unknown or empty key yields nil rather than a fabricated 0:
//     "free" and "unknown" are different, and rendering an unknown price as
//     free would understate what a learner is about to spend.
//   - tool_handler_refs ships so the card CAN show tools, but the card must
//     render them dark until the per-step allowlist is actually enforced in the
//     agent (ADR-257 section 5). That is a UI rule, enforced in the FE specs;
//     the wire carrying the data is not itself the claim.
package http

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func cardFieldGrants() ([]companion.SkillGrant, map[string]companion.CatalogEntry) {
	grants := []companion.SkillGrant{
		{SkillKey: "explain_anew", SkillKind: companion.SkillKindActive, SlotCost: 1, Equipped: true},
		{SkillKey: "web_research", SkillKind: companion.SkillKindActive, SlotCost: 2},
		{SkillKey: "long_weaving", SkillKind: companion.SkillKindCraft, SlotCost: 0},
		{SkillKey: "ghost_skill", SkillKind: companion.SkillKindActive, SlotCost: 1},
	}
	catalogue := map[string]companion.CatalogEntry{
		"explain_anew": {
			SkillKey: "explain_anew", Name: "Explain It Differently",
			Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard,
			OutputSink: companion.SinkChat, PriceKey: companion.ActionCompanionSkillExplainAnew,
			ToolHandlerRefs: []string{"atom.search", "atom.cite"}, Active: true,
		},
		"web_research": {
			SkillKey: "web_research", Name: "Far Sight",
			Family: companion.FamilySeeker, PolicyClass: companion.PolicyExternalEgress,
			OutputSink: companion.SinkMemoryNote, PriceKey: companion.ActionCompanionSkillWebResearch,
			ToolHandlerRefs: []string{"gateway.grounded_search", "kg.suggest"}, Active: true,
		},
		"long_weaving": {
			SkillKey: "long_weaving", Name: "Long Weaving",
			Family: companion.FamilyCraft, PolicyClass: companion.PolicyStandard,
			OutputSink: "", PriceKey: "", ToolHandlerRefs: []string{}, Active: true,
		},
		// A row whose price key is not in the canonical cost map at all.
		"ghost_skill": {
			SkillKey: "ghost_skill", Name: "Ghost", Family: companion.FamilyScholar,
			PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat,
			PriceKey: "companion_skill_not_registered", Active: true,
		},
	}
	return grants, catalogue
}

func grantsByKey(t *testing.T) map[string]loadoutGrantResp {
	t.Helper()
	grants, catalogue := cardFieldGrants()
	resp := toLoadoutResp("comp-1", 5, grants, catalogue)
	out := map[string]loadoutGrantResp{}
	for _, g := range resp.Grants {
		out[g.SkillKey] = g
	}
	return out
}

// TestLoadoutGrant_CarriesTheCardFields — the four descriptive fields the step
// card needs, straight from the catalogue projection.
func TestLoadoutGrant_CarriesTheCardFields(t *testing.T) {
	by := grantsByKey(t)

	ea := by["explain_anew"]
	if ea.Name != "Explain It Differently" {
		t.Errorf("Name = %q, want the catalogue display name", ea.Name)
	}
	if ea.Family != companion.FamilyScholar {
		t.Errorf("Family = %q, want scholar", ea.Family)
	}
	if ea.PolicyClass != companion.PolicyStandard {
		t.Errorf("PolicyClass = %q, want standard", ea.PolicyClass)
	}
	if ea.OutputSink != companion.SinkChat {
		t.Errorf("OutputSink = %q, want chat", ea.OutputSink)
	}
	if len(ea.ToolHandlerRefs) != 2 || ea.ToolHandlerRefs[0] != "atom.search" {
		t.Errorf("ToolHandlerRefs = %v, want the catalogue refs verbatim", ea.ToolHandlerRefs)
	}

	wr := by["web_research"]
	if wr.PolicyClass != companion.PolicyExternalEgress {
		t.Errorf("web_research PolicyClass = %q, want external_egress (the card prices off this)", wr.PolicyClass)
	}
	if wr.OutputSink != companion.SinkMemoryNote {
		t.Errorf("web_research OutputSink = %q, want memory_note", wr.OutputSink)
	}
}

// TestLoadoutGrant_ResolvesPriceUnits — the catalogue carries a price KEY; the
// card needs a number, so the projection resolves it.
func TestLoadoutGrant_ResolvesPriceUnits(t *testing.T) {
	by := grantsByKey(t)

	ea := by["explain_anew"]
	if ea.PriceUnits == nil {
		t.Fatal("explain_anew must carry a resolved price")
	}
	if *ea.PriceUnits != 10 {
		t.Errorf("explain_anew price = %d, want 10 (canonical cost map)", *ea.PriceUnits)
	}

	wr := by["web_research"]
	if wr.PriceUnits == nil || *wr.PriceUnits != 80 {
		t.Errorf("web_research price = %v, want 80", wr.PriceUnits)
	}
}

// TestLoadoutGrant_UnknownPriceIsAbsentNotZero is the load-bearing one. "Free"
// and "unknown" are different facts and a card that renders an unknown price as
// 0 understates what the learner is about to spend.
func TestLoadoutGrant_UnknownPriceIsAbsentNotZero(t *testing.T) {
	by := grantsByKey(t)

	if p := by["ghost_skill"].PriceUnits; p != nil {
		t.Errorf("a price key absent from the cost map must yield NO price, got %d", *p)
	}
	if p := by["long_weaving"].PriceUnits; p != nil {
		t.Errorf("a craft skill has no price key and must yield NO price, got %d", *p)
	}
}

// TestLoadoutGrant_FreePriceIsZeroNotAbsent — the other half of the same
// distinction: a genuinely free skill carries 0, and must not read as unknown.
func TestLoadoutGrant_FreePriceIsZeroNotAbsent(t *testing.T) {
	grants := []companion.SkillGrant{
		{SkillKey: "progress_mirror", SkillKind: companion.SkillKindActive, SlotCost: 1},
	}
	catalogue := map[string]companion.CatalogEntry{
		"progress_mirror": {
			SkillKey: "progress_mirror", Name: "Progress Mirror",
			Family: companion.FamilySight, PolicyClass: companion.PolicyStandard,
			OutputSink: companion.SinkChat, PriceKey: companion.ActionCompanionSkillProgressMirror,
			Active: true,
		},
	}
	resp := toLoadoutResp("comp-1", 5, grants, catalogue)
	p := resp.Grants[0].PriceUnits
	if p == nil {
		t.Fatal("progress_mirror is FREE, not unknown: it must carry 0, not nothing")
	}
	if *p != 0 {
		t.Errorf("progress_mirror price = %d, want 0", *p)
	}
}

// TestLoadoutGrant_MissingCatalogueRowStaysHonest — a grant whose catalogue row
// has gone (a retired skill still owned) must not fabricate card fields.
func TestLoadoutGrant_MissingCatalogueRowStaysHonest(t *testing.T) {
	grants := []companion.SkillGrant{
		{SkillKey: "retired_skill", SkillKind: companion.SkillKindActive, SlotCost: 1},
	}
	resp := toLoadoutResp("comp-1", 5, grants, map[string]companion.CatalogEntry{})
	g := resp.Grants[0]
	if g.Name != "" || g.Family != "" || g.PolicyClass != "" || g.OutputSink != "" {
		t.Errorf("an absent catalogue row must leave the card fields empty, got %+v", g)
	}
	if g.PriceUnits != nil {
		t.Errorf("an absent catalogue row must yield no price, got %d", *g.PriceUnits)
	}
	if g.CatalogueActive {
		t.Error("an absent catalogue row is not active")
	}
}
