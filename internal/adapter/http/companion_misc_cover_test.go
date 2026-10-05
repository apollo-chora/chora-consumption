// companion_misc_cover_test.go — white-box tests for the remaining branch +
// error paths across the Phyllis/MVP companion handlers:
//
//   - chatTurnCost tier table (pure)
//   - companion_instance dispatcher: 405, missing-context, bad JSON, missing id
//   - companion_handlers: handleCompanionMe 405, summon 405/bad-json/already, atoms 404
//   - handleCompanionGrowthRoute dispatcher: sub-route → handler + 405 + unknown
//
// Reuses authedReq / NewServer / testTenant / testGCID / testAtom.
package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---------------------------------------------------------------------------
// toInstanceResp / toRosterItemResp DTO mapping
// ---------------------------------------------------------------------------

func TestToInstanceResp_NormalizesNilGrantsAndRules(t *testing.T) {
	// An Instance with nil SkillGrants + nil ConfiguredRules must surface as
	// empty [] / {} (never null) in the wire shape.
	inst := &companion.Instance{
		CompanionID:    "fam-1",
		TenantID:       testTenant,
		OwnerGCID:      testGCID,
		Name:           "Newton",
		Specialization: "math",
		EvolutionTier:  companion.EvolutionTier("apprentice"),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		// SkillGrants + ConfiguredRules intentionally nil
	}
	resp := toInstanceResp(inst)
	if resp.SkillGrants == nil {
		t.Errorf("SkillGrants must be non-nil []")
	}
	if resp.ConfiguredRules == nil {
		t.Errorf("ConfiguredRules must be non-nil {}")
	}
	if resp.CompanionID != "fam-1" || resp.Name != "Newton" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestToRosterItemResp_WithGrowthAndCosmetic(t *testing.T) {
	now := time.Now().UTC()
	inst := &companion.Instance{
		CompanionID:     "fam-2",
		TenantID:        testTenant,
		OwnerGCID:       testGCID,
		Name:            "Curie",
		Specialization:  "physics",
		EvolutionTier:   companion.EvolutionTier("apprentice"),
		ConfiguredRules: map[string]string{"tone": "warm"},
		SkillGrants:     []string{"hint"},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	entry := &companion.RosterEntry{
		Instance: inst,
		Growth: &companion.GrowthSnapshot{
			Stage:                2,
			StageName:            "", // empty → derived via StageName(2)="fledgling"
			Exp:                  120,
			ExpToNextStage:       80,
			CurrentBreed:         "owl",
			EffectiveLLMTier:     "standard",
			BreedRevealedAt:      &now,
			LastStageUpAt:        &now,
			AhaMomentActiveUntil: &now,
		},
		Cosmetic: &companion.CosmeticSnapshot{
			EquippedSkinID: "skin-1",
			Shiny:          true,
			Rarity:         "rare",
		},
	}
	resp := toRosterItemResp(entry, 25, nil)
	if resp.GrowthState == nil {
		t.Fatal("GrowthState must be populated")
	}
	if resp.GrowthState.StageName != "fledgling" {
		t.Errorf("StageName = %q; want derived 'fledgling'", resp.GrowthState.StageName)
	}
	if resp.GrowthState.BreedRevealedAt == nil || resp.GrowthState.LastStageUpAt == nil || resp.GrowthState.AhaMomentActiveUntil == nil {
		t.Errorf("growth timestamp pointers must be formatted, got %+v", resp.GrowthState)
	}
	if resp.Cosmetic == nil || resp.Cosmetic.EquippedSkinID == nil || *resp.Cosmetic.EquippedSkinID != "skin-1" {
		t.Errorf("cosmetic = %+v", resp.Cosmetic)
	}
	if !resp.Cosmetic.Shiny || resp.Cosmetic.Rarity != "rare" {
		t.Errorf("cosmetic shiny/rarity = %+v", resp.Cosmetic)
	}
}

func TestToRosterItemResp_NilGrowthAndCosmeticOmitted(t *testing.T) {
	inst := &companion.Instance{
		CompanionID: "fam-3", TenantID: testTenant, OwnerGCID: testGCID,
		Name: "Lovelace", Specialization: "coding",
		EvolutionTier: companion.EvolutionTier("apprentice"),
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	resp := toRosterItemResp(&companion.RosterEntry{Instance: inst}, 25, nil)
	if resp.GrowthState != nil {
		t.Errorf("GrowthState should be nil/omitted when entry.Growth is nil")
	}
	if resp.Cosmetic != nil {
		t.Errorf("Cosmetic should be nil/omitted when entry.Cosmetic is nil")
	}
}

// ---------------------------------------------------------------------------
// chatTurnCost (pure)
// ---------------------------------------------------------------------------

func TestAugmentTurnCompleteFrame(t *testing.T) {
	// Valid JSON frame → turn_id + mana_charged injected.
	out := augmentTurnCompleteFrame([]byte(`{"text":"done"}`), "turn-42", 15)
	got := string(out)
	for _, want := range []string{`"turn_id":"turn-42"`, `"mana_charged":15`, `"text":"done"`} {
		if !contains(got, want) {
			t.Errorf("augmented frame %q missing %q", got, want)
		}
	}
	// Invalid JSON → returned unchanged (defensive passthrough).
	bad := []byte(`{not json`)
	if string(augmentTurnCompleteFrame(bad, "x", 1)) != string(bad) {
		t.Errorf("invalid JSON frame must pass through unchanged")
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}

func TestChatTurnCost(t *testing.T) {
	cases := []struct {
		tier string
		want int
	}{
		{"premium", 30},
		{"standard", 15},
		{"basic", 5},
		{"", 5},        // default
		{"unknown", 5}, // default
	}
	for _, c := range cases {
		if got := chatTurnCost(c.tier); got != c.want {
			t.Errorf("chatTurnCost(%q) = %d; want %d", c.tier, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// toCompanionMVPResp nil-slice normalization
// ---------------------------------------------------------------------------

func TestToCompanionMVPResp_NormalizesNilSlices(t *testing.T) {
	// A freshly-summoned companion with nil Traits/UnlockedTraits must surface
	// as empty [] (not null) in the wire shape. The summon ceremony allocates
	// EXACTLY 3 stat points across the 5 dimensions.
	f, err := companion.Summon(testTenant, testGCID, "Newton", companion.ArchetypeScholarOwl,
		companion.StatAllocation{Curiosity: 1, Focus: 1, Recall: 1})
	if err != nil {
		t.Fatalf("summon: %v", err)
	}
	resp := toCompanionMVPResp(f)
	if resp.Traits == nil {
		t.Errorf("Traits must be non-nil []")
	}
	if resp.UnlockedTraits == nil {
		t.Errorf("UnlockedTraits must be non-nil []")
	}
	if resp.Name != "Newton" {
		t.Errorf("Name = %q; want Newton", resp.Name)
	}
}

func TestToCompanionMVPResp_PreservesPopulatedSlices(t *testing.T) {
	f, err := companion.Summon(testTenant, testGCID, "Curie", companion.ArchetypeScholarOwl,
		companion.StatAllocation{Curiosity: 1, Focus: 1, Recall: 1})
	if err != nil {
		t.Fatalf("summon: %v", err)
	}
	// Populate the slices so the non-nil pass-through branch is exercised.
	f.Traits = []string{"focus", "recall"}
	f.UnlockedTraits = []string{"pattern-recognition"}
	resp := toCompanionMVPResp(f)
	if len(resp.Traits) != 2 || resp.Traits[0] != "focus" {
		t.Errorf("Traits = %v; want [focus recall]", resp.Traits)
	}
	if len(resp.UnlockedTraits) != 1 || resp.UnlockedTraits[0] != "pattern-recognition" {
		t.Errorf("UnlockedTraits = %v", resp.UnlockedTraits)
	}
}

// ---------------------------------------------------------------------------
// /companion/me + /companion/summon branches
// ---------------------------------------------------------------------------

func TestCompanionMe_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/companion/me", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /companion/me = %d; want 405", w.Code)
	}
}

func TestCompanionSummon_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/companion/summon", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /companion/summon = %d; want 405", w.Code)
	}
}

func TestCompanionSummon_MissingContext(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/companion/summon", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("summon w/o context = %d; want 401", w.Code)
	}
}

func TestCompanionSummon_BadJSON(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/companion/summon", bytes.NewBufferString("{bad"))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("summon bad JSON = %d; want 400", w.Code)
	}
}

func TestCompanionSummon_InvalidArchetype(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/companion/summon", map[string]any{
		"name":      "X",
		"archetype": "not-a-real-archetype",
		"stats":     map[string]int{"curiosity": 2, "focus": 2, "recall": 2, "social": 2, "perseverance": 2},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("summon invalid archetype = %d; want 400 INVALID_SUMMON; body=%s", w.Code, w.Body.String())
	}
}

func TestDailyDose_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/companion/daily-dose", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /companion/daily-dose = %d; want 405", w.Code)
	}
}

func TestAtomFeedback_BadJSON(t *testing.T) {
	srv := NewServer()
	atomID := srv.Atoms.Seeds()[0].AtomID
	req := httptest.NewRequest(http.MethodPost, "/atoms/"+atomID+"/feedback", bytes.NewBufferString("{bad"))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("feedback bad JSON = %d; want 400", w.Code)
	}
}

func TestAtomFeedback_InvalidGradeRejected(t *testing.T) {
	srv := NewServer()
	atomID := srv.Atoms.Seeds()[0].AtomID
	w := authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback",
		map[string]string{"grade": "totally-bogus"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("feedback invalid grade = %d; want 400 INVALID_GRADE; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateCompanionInstance_WithConfiguredRules(t *testing.T) {
	srv := NewServer()
	// ConfiguredRules in the body exercises the SetRule loop (line 245).
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions", map[string]any{
		"name":             "Newton",
		"specialization":   "math",
		"configured_rules": map[string]string{"tone": "warm", "hint_policy": "generous"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create w/ rules = %d body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// /atoms/{id}/feedback dispatcher (handleAtomsRoot)
// ---------------------------------------------------------------------------

func TestAtomsRoot_NotFoundOnBadShape(t *testing.T) {
	srv := NewServer()
	// "/atoms/abc" has no /feedback suffix → NOT_FOUND.
	w := authedReq(t, srv, http.MethodPost, "/atoms/abc", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("/atoms/abc = %d; want 404", w.Code)
	}
}

func TestAtomFeedback_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	// GET on the feedback endpoint → 405 (POST only).
	w := authedReq(t, srv, http.MethodGet, "/atoms/"+testAtom+"/feedback", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET feedback = %d; want 405", w.Code)
	}
}

func TestAtomFeedback_MissingContext(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/atoms/"+testAtom+"/feedback", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("feedback w/o context = %d; want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// companion_instance dispatcher branches
// ---------------------------------------------------------------------------

func TestCompanionInstances_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPut, "/v1/me/companions", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /v1/me/companions = %d; want 405", w.Code)
	}
}

func TestCreateCompanionInstance_BadJSON(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/companions", bytes.NewBufferString("{bad"))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create bad JSON = %d; want 400", w.Code)
	}
}

func TestCompanionInstanceByID_MissingID(t *testing.T) {
	srv := NewServer()
	// "/v1/me/companions/" trims to empty → the collection route handles it
	// (GET list). Assert it does not 500.
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/", nil)
	if w.Code >= 500 {
		t.Errorf("trailing-slash list = %d; want <500", w.Code)
	}
}

func TestCompanionInstanceByID_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPut, "/v1/me/companions/abc", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT by-id = %d; want 405", w.Code)
	}
}

func TestGetCompanionInstance_MissingContext(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/companions/abc", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("get by-id w/o context = %d; want 401", w.Code)
	}
}

func TestDeleteCompanionInstance_MissingContext(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodDelete, "/v1/me/companions/abc", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("delete by-id w/o context = %d; want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// handleCompanionGrowthRoute dispatcher — sub-route routing + 405 + unknown.
// Growth is nil in NewServer(), so the GET/POST handlers return 503
// GROWTH_NOT_WIRED; the dispatcher's method guards return 405 first.
// ---------------------------------------------------------------------------

func TestGrowthRoute_GrowthWrongMethod(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/growth", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST growth = %d; want 405", w.Code)
	}
}

func TestGrowthRoute_GrowthNotWired503(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET growth (nil Growth) = %d; want 503", w.Code)
	}
}

func TestGrowthRoute_HatchWrongMethod(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/hatch", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET hatch = %d; want 405", w.Code)
	}
}

func TestGrowthRoute_ResonanceWrongMethod(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/resonance", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET resonance = %d; want 405", w.Code)
	}
}

func TestGrowthRoute_SourceRevelationWrongMethod(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/source-revelation", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET source-revelation = %d; want 405", w.Code)
	}
}

func TestGrowthRoute_GrowthEventsWrongMethod(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/growth-events", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST growth-events = %d; want 405", w.Code)
	}
}

func TestGrowthRoute_UnknownSubroute(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/bogus", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subroute = %d; want 404", w.Code)
	}
}

// ---------------------------------------------------------------------------
// handleCompanionChat early-guard branches (CompanionEngine nil in NewServer())
// ---------------------------------------------------------------------------

func TestCompanionChat_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	// GET reaches handleCompanionChat via the /chat sub-route; the handler's
	// own method check returns 405.
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/chat", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET chat = %d; want 405", w.Code)
	}
}

func TestCompanionChat_MissingContext(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/companions/fam-1/chat", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("chat w/o context = %d; want 401", w.Code)
	}
}

func TestCompanionChat_EngineNotConfigured_503(t *testing.T) {
	srv := NewServer() // CompanionEngine nil
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/chat",
		map[string]any{"message": "hi"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("chat w/ nil engine = %d; want 503 ENGINE_NOT_CONFIGURED; body=%s", w.Code, w.Body.String())
	}
}

// With a wired (env-driven GKE) engine, the body-decode + empty-message guards
// run BEFORE the engine stream opens — so they are reachable without a live
// engine dial.
func TestCompanionChat_BadJSON_WithEngine(t *testing.T) {
	withGetenv(t, map[string]string{"COMPANION_ENGINE_GKE_ENDPOINT": "http://companion-gke.invalid:8080"})
	srv := NewServer()
	if srv.CompanionEngine == nil {
		t.Skip("engine not wired")
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/me/companions/fam-1/chat", bytes.NewBufferString("{bad"))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("chat bad JSON = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestCompanionChat_EmptyMessage_WithEngine(t *testing.T) {
	withGetenv(t, map[string]string{"COMPANION_ENGINE_GKE_ENDPOINT": "http://companion-gke.invalid:8080"})
	srv := NewServer()
	if srv.CompanionEngine == nil {
		t.Skip("engine not wired")
	}
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/chat",
		map[string]any{"message": "   "})
	if w.Code != http.StatusBadRequest {
		t.Errorf("chat empty message = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestCompanionChat_MissingCompanionID_WithEngine(t *testing.T) {
	withGetenv(t, map[string]string{"COMPANION_ENGINE_GKE_ENDPOINT": "http://companion-gke.invalid:8080"})
	srv := NewServer()
	if srv.CompanionEngine == nil {
		t.Skip("engine not wired")
	}
	// Call the handler directly with an empty companionID to hit the
	// COMPANION_NOT_FOUND guard (the route always supplies a non-empty id).
	req := httptest.NewRequest(http.MethodPost, "/v1/me/companions//chat", bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	srv.handleCompanionChat(w, req, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("chat empty companion id = %d; want 404; body=%s", w.Code, w.Body.String())
	}
}
