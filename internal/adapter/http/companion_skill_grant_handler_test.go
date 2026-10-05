// companion_skill_grant_handler_test.go — CHO-2012 loadout endpoints
// (ADR-218 D3: owned forever, cap binds the equipped set, free swap).
//
//	GET    /v1/me/companions/{id}/skills               loadout view
//	POST   /v1/me/companions/{id}/skills               admin/dev grant mint (owned, unequipped)
//	PUT    /v1/me/companions/{id}/skills/{key}/equip   equip (slot-cost capped → 409 SKILL_SLOTS_FULL)
//	DELETE /v1/me/companions/{id}/skills/{key}/equip   unequip (free)
//
// Events: skill_granted on a real mint; loadout_changed on equip/unequip.
// The slot cap derives from the growth stage (growth.SlotsForStage); the
// pre-P0 grant-time cap + tier axis are retired.
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type skillGrantReq struct {
	SkillKey string `json:"skill_key"`
}

func seedSkillCompanion(t *testing.T, srv *Server) string {
	t.Helper()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed: status=%d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	return created["companion_id"].(string)
}

func decodeLoadout(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	return resp
}

// activateSkill flips the catalogue release gate so equip paths can run
// (P0 seeds everything dark; equip requires an ACTIVE row).
func activateSkill(t *testing.T, srv *Server, keys ...string) {
	t.Helper()
	cat, ok := srv.SkillCatalog.(*inmem.SkillCatalog)
	if !ok {
		t.Fatalf("test server SkillCatalog is %T, want *inmem.SkillCatalog", srv.SkillCatalog)
	}
	for _, k := range keys {
		cat.SetActive(k, true)
	}
}

// plainReq issues a request WITHOUT the auth headers.
func plainReq(t *testing.T, srv *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

func TestGrantSkill_MintOwnedUnequipped(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "explain_anew"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	resp := decodeLoadout(t, w.Body.Bytes())
	grants, ok := resp["skill_grants"].([]any)
	if !ok || len(grants) != 1 || grants[0] != "explain_anew" {
		t.Errorf("skill_grants = %v, want [explain_anew]", resp["skill_grants"])
	}
	// Owned ≠ equipped (ADR-218 D3): a mint does not consume a slot.
	if equipped, _ := resp["equipped_skills"].([]any); len(equipped) != 0 {
		t.Errorf("equipped_skills = %v, want empty after a bare mint", equipped)
	}

	// skill_granted published with the loadout metadata.
	pub := srv.Publisher.(*events.InMemoryPublisher)
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicCompanionSkillGranted {
			found = true
			if ev.Payload["skill_key"] != "explain_anew" || ev.Payload["unlocked_via"] != "admin_grant" {
				t.Errorf("skill_granted payload = %v, want explain_anew via admin_grant", ev.Payload)
			}
		}
	}
	if !found {
		t.Error("skill_granted not published on a real mint")
	}

	// Re-mint is idempotent: 200 (not 201) and NO duplicate event.
	evBefore := len(pub.Events())
	w = authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "explain_anew"})
	if w.Code != http.StatusOK {
		t.Fatalf("re-mint status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if len(pub.Events()) != evBefore {
		t.Error("idempotent re-mint must not re-publish skill_granted")
	}
}

func TestGrantSkill_UnknownKeyRejected(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "skill.tree.hack"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-catalogue key, body=%s", w.Code, w.Body.String())
	}
}

func TestEquipSkill_HappyPathAndLoadoutChanged(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	activateSkill(t, srv, "explain_anew")

	// Mint then equip.
	if w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "explain_anew"}); w.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	w := authedReq(t, srv, http.MethodPut, "/v1/me/companions/"+id+"/skills/explain_anew/equip", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("equip status = %d, body=%s", w.Code, w.Body.String())
	}
	resp := decodeLoadout(t, w.Body.Bytes())
	equipped, _ := resp["equipped_skills"].([]any)
	if len(equipped) != 1 || equipped[0] != "explain_anew" {
		t.Errorf("equipped_skills = %v, want [explain_anew]", equipped)
	}

	pub := srv.Publisher.(*events.InMemoryPublisher)
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicCompanionLoadoutChanged {
			found = true
			if ev.Payload["skill_key"] != "explain_anew" || ev.Payload["action"] != "equip" {
				t.Errorf("loadout_changed payload = %v", ev.Payload)
			}
		}
	}
	if !found {
		t.Error("loadout_changed not published on equip")
	}

	// Unequip (free swap) → 200 + loadout_changed(action=unequip).
	w = authedReq(t, srv, http.MethodDelete, "/v1/me/companions/"+id+"/skills/explain_anew/equip", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("unequip status = %d, body=%s", w.Code, w.Body.String())
	}
	resp = decodeLoadout(t, w.Body.Bytes())
	if equipped, _ := resp["equipped_skills"].([]any); len(equipped) != 0 {
		t.Errorf("equipped_skills after unequip = %v, want empty", equipped)
	}
}

func TestEquipSkill_SlotCapConflict(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	activateSkill(t, srv, "explain_anew")
	activateSkill(t, srv, "recap_scribe")

	// A fresh companion is stage 0 → 1 slot (ADR-218 D2). Mint two skills,
	// equip one, then the second equip must 409 SKILL_SLOTS_FULL.
	for _, key := range []string{"explain_anew", "recap_scribe"} {
		if w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
			skillGrantReq{SkillKey: key}); w.Code != http.StatusCreated {
			t.Fatalf("mint %s: %d %s", key, w.Code, w.Body.String())
		}
	}
	if w := authedReq(t, srv, http.MethodPut, "/v1/me/companions/"+id+"/skills/explain_anew/equip", nil); w.Code != http.StatusOK {
		t.Fatalf("first equip: %d %s", w.Code, w.Body.String())
	}
	w := authedReq(t, srv, http.MethodPut, "/v1/me/companions/"+id+"/skills/recap_scribe/equip", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("over-cap equip status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	var errResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "SKILL_SLOTS_FULL" {
		t.Errorf("error code = %v, want SKILL_SLOTS_FULL", errResp["code"])
	}
}

func TestEquipSkill_NotOwnedAndInactive(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	activateSkill(t, srv, "explain_anew")

	// Not owned → 409 SKILL_NOT_OWNED.
	w := authedReq(t, srv, http.MethodPut, "/v1/me/companions/"+id+"/skills/explain_anew/equip", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("unowned equip = %d, want 409, body=%s", w.Code, w.Body.String())
	}

	// Owned but catalogue row NOT active (dark seed) → 409 SKILL_NOT_ACTIVE.
	if w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "quiz_me"}); w.Code != http.StatusCreated {
		t.Fatalf("mint quiz_me: %d %s", w.Code, w.Body.String())
	}
	w = authedReq(t, srv, http.MethodPut, "/v1/me/companions/"+id+"/skills/quiz_me/equip", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("inactive equip = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	var errResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "SKILL_NOT_ACTIVE" {
		t.Errorf("error code = %v, want SKILL_NOT_ACTIVE", errResp["code"])
	}
}

func TestListSkills_LoadoutView(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	if w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "explain_anew"}); w.Code != http.StatusCreated {
		t.Fatalf("mint: %d", w.Code)
	}

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id+"/skills", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", w.Code, w.Body.String())
	}
	resp := decodeLoadout(t, w.Body.Bytes())
	if resp["skill_slots_unlocked"].(float64) != 1 {
		t.Errorf("skill_slots_unlocked = %v, want 1 (stage-0 derivation)", resp["skill_slots_unlocked"])
	}
	if resp["slots_used"].(float64) != 0 {
		t.Errorf("slots_used = %v, want 0", resp["slots_used"])
	}
	grants, _ := resp["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("grants = %v, want 1 detailed grant", resp["grants"])
	}
	g := grants[0].(map[string]any)
	if g["skill_key"] != "explain_anew" || g["skill_kind"] != string(companion.SkillKindActive) || g["equipped"] != false {
		t.Errorf("grant detail = %v", g)
	}
}

func TestSkillsRoutes_RequireAuthContext(t *testing.T) {
	srv := NewServer()
	w := plainReq(t, srv, http.MethodGet, "/v1/me/companions/some-id/skills", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", w.Code)
	}
}

// ----- CHO-2030: the catalogue-activation flag on the loadout wire -----

// TestListSkills_CatalogueActiveFlag proves each grant row carries the
// catalogue release state so the FE renders dormants as named-tease
// ("stirring, not yet awake") instead of badging skill_kind as ACTIVE.
func TestListSkills_CatalogueActiveFlag(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)

	for _, key := range []string{"explain_anew", "reminder_bell"} {
		w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
			skillGrantReq{SkillKey: key})
		if w.Code != http.StatusCreated {
			t.Fatalf("mint %s: status=%d body=%s", key, w.Code, w.Body.String())
		}
	}
	activateSkill(t, srv, "explain_anew") // reminder_bell stays dark (R4-1)

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id+"/skills", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Grants []struct {
			SkillKey        string `json:"skill_key"`
			CatalogueActive *bool  `json:"catalogue_active"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v (%s)", err, w.Body.String())
	}
	seen := map[string]bool{}
	for _, g := range resp.Grants {
		if g.CatalogueActive == nil {
			t.Fatalf("grant %s missing catalogue_active on the wire", g.SkillKey)
		}
		seen[g.SkillKey] = *g.CatalogueActive
	}
	if !seen["explain_anew"] {
		t.Errorf("explain_anew catalogue_active = false, want true after release")
	}
	if active, ok := seen["reminder_bell"]; !ok || active {
		t.Errorf("reminder_bell catalogue_active = %v (present=%v), want false (owned-dark dormant)", active, ok)
	}
}
