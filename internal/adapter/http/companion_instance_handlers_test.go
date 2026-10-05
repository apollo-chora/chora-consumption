// companion_instance_handlers_test.go — TDD spec for the multi-Companion
// (1:N) HTTP surface. Migrated from the chora-companion service-side
// handlers per the 2026-05-12 consolidation.
//
// Endpoints:
//
//	POST   /v1/me/companions            create a new Companion Instance
//	GET    /v1/me/companions            list the caller's Companion Instances
//	GET    /v1/me/companions/{id}       fetch one Companion Instance
//	DELETE /v1/me/companions/{id}       soft-delete
//
// Auth model: tenant_id from X-Tenant-Id + owner_gcid from `gcid` header
// (same MVP convention as the rest of the surface).
//
// Cap enforcement: max_companions_per_user defaults to
// companion.DefaultMaxCompanionsPerUser=3. Production reads the cap from the
// chora_tenancy tenant_entitlements event-sourced read model (M14.1+); the
// MVP hardcodes the default — flagged for the M12.3 outbox follow-up.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// instanceCreateReq mirrors the JSON shape produced by createInstanceReq.
type instanceCreateReq struct {
	Name           string `json:"name"`
	Specialization string `json:"specialization"`
}

func TestCreateCompanionInstance_HappyPath(t *testing.T) {
	srv := NewServer()
	body := instanceCreateReq{Name: "Newton", Specialization: "math"}
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["name"] != "Newton" {
		t.Errorf("name = %v, want Newton", resp["name"])
	}
	if resp["specialization"] != "math" {
		t.Errorf("specialization = %v, want math", resp["specialization"])
	}
	if resp["evolution_tier"] != "apprentice" {
		t.Errorf("evolution_tier = %v, want apprentice", resp["evolution_tier"])
	}
	// Event must be published.
	pub, ok := srv.Publisher.(*events.InMemoryPublisher)
	if !ok {
		t.Fatal("expected InMemoryPublisher")
	}
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicCompanionCreated {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected TopicCompanionCreated to be published")
	}
}

func TestCreateCompanionInstance_RequiresName(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions", instanceCreateReq{Specialization: "math"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateCompanionInstance_RequiresSpecialization(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions", instanceCreateReq{Name: "Newton"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateCompanionInstance_EnforcesRosterCap(t *testing.T) {
	srv := NewServer()
	// Default cap = 3. Insert 3.
	for _, spec := range []string{"math", "history", "coding"} {
		w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
			instanceCreateReq{Name: "F" + spec, Specialization: spec})
		if w.Code != http.StatusCreated {
			t.Fatalf("first 3 creates should succeed, got %d for %s: %s",
				w.Code, spec, w.Body.String())
		}
	}
	// 4th must reject with 409.
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Overlimit", Specialization: "music"})
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (cap reached), body=%s", w.Code, w.Body.String())
	}
}

func TestListCompanionInstances_HappyPath(t *testing.T) {
	srv := NewServer()
	// Create two.
	for _, spec := range []string{"math", "history"} {
		_ = authedReq(t, srv, http.MethodPost, "/v1/me/companions",
			instanceCreateReq{Name: "F" + spec, Specialization: spec})
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Errorf("got %d items, want 2", len(resp.Items))
	}
}

// TestListCompanionInstances_RosterItemsCarrySubject pins the cross-stream
// contract that drives the A+ dashboard "Ask Companion" specialist match: each
// roster item exposes a `subject` key carrying the Companion's topic axis (the
// 4th NewInstance arg / Specialization), so the FE can pick the topic-matched
// specialist Companion per Growth Edge instead of always companions[0]. The
// learner here owns Companions with DISTINCT subjects, and `subject` must equal
// `specialization` on every item.
func TestListCompanionInstances_RosterItemsCarrySubject(t *testing.T) {
	srv := NewServer()
	subjects := []string{"history", "math"}
	for _, spec := range subjects {
		w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
			instanceCreateReq{Name: "F" + spec, Specialization: spec})
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: status = %d, body=%s", spec, w.Code, w.Body.String())
		}
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(resp.Items))
	}
	seen := map[string]bool{}
	for i, item := range resp.Items {
		subject, ok := item["subject"].(string)
		if !ok || subject == "" {
			t.Fatalf("item[%d] missing non-empty subject: %v", i, item["subject"])
		}
		// `subject` mirrors `specialization` on the same item (single topic axis).
		if spec, _ := item["specialization"].(string); subject != spec {
			t.Errorf("item[%d] subject=%q != specialization=%q", i, subject, spec)
		}
		seen[subject] = true
	}
	for _, want := range subjects {
		if !seen[want] {
			t.Errorf("roster missing a Companion with subject %q (distinct-subject contract)", want)
		}
	}
}

func TestGetCompanionInstance_HappyPath(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created["companion_id"].(string)

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(got.Body.Bytes(), &resp)
	if resp["companion_id"] != id {
		t.Errorf("companion_id round-trip lost: got %v want %s", resp["companion_id"], id)
	}
}

func TestGetCompanionInstance_404OnUnknown(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteCompanionInstance_SoftDeletes(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created["companion_id"].(string)

	del := authedReq(t, srv, http.MethodDelete, "/v1/me/companions/"+id, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204, body=%s", del.Code, del.Body.String())
	}

	// Subsequent Get → 404.
	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusNotFound {
		t.Errorf("after delete: status = %d, want 404", got.Code)
	}

	// And the deleted slot frees up for cap purposes.
	more := []string{"history", "coding", "music"}
	for _, spec := range more {
		w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
			instanceCreateReq{Name: "F" + spec, Specialization: spec})
		if w.Code != http.StatusCreated {
			t.Fatalf("post-delete create %s: status = %d, body=%s",
				spec, w.Code, w.Body.String())
		}
	}
}

func TestCreateCompanionInstance_RequiresAuth(t *testing.T) {
	srv := NewServer()
	// Build the request manually without auth headers.
	req := httptest.NewRequest(http.MethodPost, "/v1/me/companions", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// debt #44 / A24 / B5 — enriched list endpoint (2026-05-16)
//
// The list endpoint now returns the canonical RosterEntry shape so FE can
// drop the per-Companion growth N+1 call (per chora-web commit 4cff383e +
// FE handoff §B5). Wire envelope additive-only: existing `items[]` is
// retained with the original fields PLUS the new nested `growth_state` +
// `cosmetic` objects.
// ---------------------------------------------------------------------------

func TestListCompanionInstances_EnrichesResponseWithGrowthAndCosmetic(t *testing.T) {
	srv := NewServer()
	// Seed two Companions; the in-memory adapter returns zero-value
	// GrowthSnapshot + CosmeticSnapshot for each (pg adapter populates from
	// the 0032 ALTER TABLE columns — that path is covered in
	// repo/pg/companion_roster_test.go).
	for _, spec := range []string{"math", "history"} {
		_ = authedReq(t, srv, http.MethodPost, "/v1/me/companions",
			instanceCreateReq{Name: "F" + spec, Specialization: spec})
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(resp.Items))
	}
	for i, item := range resp.Items {
		// Existing fields preserved (backwards-compat additive).
		if item["companion_id"] == "" || item["companion_id"] == nil {
			t.Errorf("item[%d] missing companion_id", i)
		}
		if item["name"] == "" || item["name"] == nil {
			t.Errorf("item[%d] missing name", i)
		}
		// New nested growth_state object is always present (NOT-OPTIONAL —
		// pre-hatch rows surface zero values).
		gs, ok := item["growth_state"].(map[string]any)
		if !ok {
			t.Fatalf("item[%d] missing growth_state object: %v", i, item["growth_state"])
		}
		// Stage + StageName always populated.
		if _, ok := gs["stage"]; !ok {
			t.Errorf("item[%d].growth_state.stage missing", i)
		}
		if _, ok := gs["stage_name"]; !ok {
			t.Errorf("item[%d].growth_state.stage_name missing", i)
		}
		if _, ok := gs["exp"]; !ok {
			t.Errorf("item[%d].growth_state.exp missing", i)
		}
		if _, ok := gs["exp_to_next_stage"]; !ok {
			t.Errorf("item[%d].growth_state.exp_to_next_stage missing", i)
		}
		// New nested cosmetic object.
		cos, ok := item["cosmetic"].(map[string]any)
		if !ok {
			t.Fatalf("item[%d] missing cosmetic object", i)
		}
		if _, ok := cos["shiny"]; !ok {
			t.Errorf("item[%d].cosmetic.shiny missing", i)
		}
		if _, ok := cos["rarity"]; !ok {
			t.Errorf("item[%d].cosmetic.rarity missing", i)
		}
	}
}

// stubCompanionInstanceRepo is a hand-rolled handler-test stub that lets
// debt-#44 tests pin the exact RosterEntry payload returned to the
// list-endpoint handler — the in-memory adapter only surfaces zero-value
// growth/cosmetic. Used here to verify the handler renders fully-populated
// projection fields (BreedRevealedAt timestamp, EquippedSkinID, etc).
type stubCompanionInstanceRepo struct {
	companion.InstanceRepository // delegate everything we don't override to inmem
	rosterEntries                []*companion.RosterEntry
}

func (s *stubCompanionInstanceRepo) ListRosterByOwner(_ context.Context, _, _ string) ([]*companion.RosterEntry, error) {
	return s.rosterEntries, nil
}

func TestListCompanionInstances_RendersFullyPopulatedRosterEntry(t *testing.T) {
	// Seed a server with the inmem adapter, then swap in the stub so we
	// can drive a populated RosterEntry. Mirror of the canonical Eira
	// fixture from migrations/0036_phyllis_eira_seed.up.sql.
	srv := NewServer()
	eira, err := companion.NewInstance(
		"11111111-1111-7111-8111-111111111111",
		"00000000-0000-7000-8000-000000001999",
		"Eira", "cspo",
	)
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	hatched := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	stageUp := hatched
	skin := "skin.dragon.fledgling.v1"
	srv.CompanionInstances = &stubCompanionInstanceRepo{
		InstanceRepository: srv.CompanionInstances,
		rosterEntries: []*companion.RosterEntry{
			{
				Instance: eira,
				Growth: &companion.GrowthSnapshot{
					Stage:            2,
					StageName:        companion.StageName(2),
					Exp:              200,
					ExpToNextStage:   companion.ExpToNextStage(2),
					CurrentBreed:     "dragon",
					BreedRevealedAt:  &hatched,
					EffectiveLLMTier: "flash-lite",
					LastStageUpAt:    &stageUp,
					ResonantAtomID:   "00000000-0000-7000-8000-00000000a0a2",
				},
				Cosmetic: &companion.CosmeticSnapshot{
					EquippedSkinID: skin,
					Shiny:          false,
					Rarity:         "common",
				},
			},
		},
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(resp.Items))
	}
	item := resp.Items[0]
	gs := item["growth_state"].(map[string]any)
	if gs["stage"].(float64) != 2 {
		t.Errorf("growth_state.stage = %v; want 2", gs["stage"])
	}
	if gs["stage_name"] != "fledgling" {
		t.Errorf("growth_state.stage_name = %v; want fledgling", gs["stage_name"])
	}
	if gs["current_breed"] != "dragon" {
		t.Errorf("growth_state.current_breed = %v; want dragon", gs["current_breed"])
	}
	if gs["breed_revealed_at"] == nil {
		t.Error("growth_state.breed_revealed_at is null; want timestamp")
	}
	if gs["effective_llm_tier"] != "flash-lite" {
		t.Errorf("growth_state.effective_llm_tier = %v; want flash-lite", gs["effective_llm_tier"])
	}
	if gs["exp_to_next_stage"].(float64) != 200 {
		t.Errorf("growth_state.exp_to_next_stage = %v; want 200", gs["exp_to_next_stage"])
	}
	if gs["last_stage_up_at"] == nil {
		t.Error("growth_state.last_stage_up_at is null; want timestamp")
	}
	cos := item["cosmetic"].(map[string]any)
	if cos["equipped_skin_id"] != "skin.dragon.fledgling.v1" {
		t.Errorf("cosmetic.equipped_skin_id = %v; want skin.dragon.fledgling.v1", cos["equipped_skin_id"])
	}
	if cos["rarity"] != "common" {
		t.Errorf("cosmetic.rarity = %v; want common", cos["rarity"])
	}
}

// stubFailingRosterRepo always returns an error from ListRosterByOwner so
// the handler's 500 path gets exercised.
type stubFailingRosterRepo struct {
	companion.InstanceRepository
	err error
}

func (s *stubFailingRosterRepo) ListRosterByOwner(_ context.Context, _, _ string) ([]*companion.RosterEntry, error) {
	return nil, s.err
}

func TestListCompanionInstances_FailsLoudOnRepoError(t *testing.T) {
	srv := NewServer()
	srv.CompanionInstances = &stubFailingRosterRepo{
		InstanceRepository: srv.CompanionInstances,
		err:                errors.New("database is on fire"),
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != "LIST_FAILED" {
		t.Errorf("code = %v, want LIST_FAILED", resp["code"])
	}
}

func TestListCompanionInstances_BackwardsCompatible_OriginalFieldsRetained(t *testing.T) {
	srv := NewServer()
	_ = authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(resp.Items))
	}
	item := resp.Items[0]
	// Per the handoff: existing fields MUST still be present so legacy
	// FE callers (or other clients) consuming the un-enriched shape keep
	// working. Surface a non-exhaustive sweep on the 12-field MVP envelope.
	for _, k := range []string{
		"companion_id", "tenant_id", "owner_gcid",
		"name", "specialization", "evolution_tier",
		"skill_slots_unlocked", "memory_context_capacity",
		"skill_grants", "configured_rules",
		"memory_bank_app_name", "created_at", "updated_at",
	} {
		if _, ok := item[k]; !ok {
			t.Errorf("legacy field %q dropped from item", k)
		}
	}
}

// Ensure compile-time use of companion package (so tests will recompile when
// the domain port signature changes).
var _ = companion.DefaultMaxCompanionsPerUser
