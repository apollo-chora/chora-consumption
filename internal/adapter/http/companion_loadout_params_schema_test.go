// companion_loadout_params_schema_test.go - CHO-2362: the loadout skills list
// carries each sheet-bearing Skill's params_schema (rendered from the domain
// table, deploy-order-safe - the FE editor and the validator can never
// disagree). Sheetless skills omit the field.
package http

import (
	"encoding/json"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestLoadoutRespCarriesParamsSchema(t *testing.T) {
	grants := []companion.SkillGrant{
		{SkillKey: "explain_anew", SkillKind: companion.SkillKindActive, SlotCost: 1, Equipped: true},
		{SkillKey: "reminder_bell", SkillKind: companion.SkillKindActive, SlotCost: 1},
	}
	catalogue := map[string]companion.CatalogEntry{
		"explain_anew":  {SkillKey: "explain_anew", Active: true},
		"reminder_bell": {SkillKey: "reminder_bell", Active: false},
	}

	resp := toLoadoutResp("fam-1", 4, grants, catalogue)
	if len(resp.Grants) != 2 {
		t.Fatalf("want 2 grant rows, got %d", len(resp.Grants))
	}

	byKey := map[string]loadoutGrantResp{}
	for _, g := range resp.Grants {
		byKey[g.SkillKey] = g
	}

	// explain_anew (sheet-bearing) carries the rendered schema, byte-equal to
	// the domain rendering the 0106 seed is also generated from.
	got := byKey["explain_anew"].ParamsSchema
	want := companion.SkillParamsSchemaJSON("explain_anew")
	if string(got) != string(want) {
		t.Errorf("explain_anew params_schema:\n got %s\nwant %s", got, want)
	}

	// reminder_bell (no builder → no sheet) omits the field entirely on the
	// wire - the FE renders its honest "no editable parameters" state.
	if byKey["reminder_bell"].ParamsSchema != nil {
		t.Errorf("reminder_bell must carry no params_schema, got %s", byKey["reminder_bell"].ParamsSchema)
	}
	wire, err := json.Marshal(byKey["reminder_bell"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if jsonHas(t, wire, "params_schema") {
		t.Errorf("sheetless row must omit params_schema on the wire, got %s", wire)
	}
	wireEA, _ := json.Marshal(byKey["explain_anew"])
	if !jsonHas(t, wireEA, "params_schema") {
		t.Errorf("sheet-bearing row must carry params_schema on the wire, got %s", wireEA)
	}
}

func jsonHas(t *testing.T, b []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_, ok := m[key]
	return ok
}
