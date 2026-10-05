// companion_profile_handler_test.go — TDD spec for the F5 enriched per-instance
// profile projection on GET /v1/me/companions/{id}.
//
// Before F5 the per-instance GET returned the thin instanceResp (identity +
// skill/memory caps only). The A+ FE Companion profile pane needs a single
// real, complete projection: identity + growth axis + roster context +
// an OPTIONAL memory_summary (gracefully empty when Memory Bank is not wired —
// F4 is designed separately; this batch never calls Vertex AI Memory Bank).
//
// Auth + RLS: tenant_id from X-Tenant-Id + owner_gcid from `gcid` (same MVP
// convention as the rest of the surface). The handler MUST scope the read to
// (tenant, owner) — a Companion belonging to another owner/tenant returns 404.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// fakeGrowthServer is a hand-rolled GrowthServer stub so the profile test can
// pin the growth-axis projection returned to the handler without a Postgres
// pool. Only GetCompanionGrowth is exercised by the profile path; the rest
// return zero values (never called here).
type fakeGrowthServer struct {
	state   *growth.State
	err     error
	gotTID  string
	gotFID  string
	gotGCID string
}

func (f *fakeGrowthServer) GetCompanionGrowth(_ context.Context, tenantID, companionID, callerGCID string) (*growth.State, error) {
	f.gotTID, f.gotFID, f.gotGCID = tenantID, companionID, callerGCID
	if f.err != nil {
		return nil, f.err
	}
	return f.state, nil
}

func (f *fakeGrowthServer) PickResonantConcept(_ context.Context, _ growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error) {
	return nil, errors.New("not stubbed")
}
func (f *fakeGrowthServer) HatchEgg(context.Context, growth.HatchEggInput) (*growth.HatchEggResponse, error) {
	return nil, nil
}
func (f *fakeGrowthServer) RevealBreed(context.Context, growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
	return nil, nil
}
func (f *fakeGrowthServer) TriggerSourceRevelation(context.Context, growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error) {
	return nil, nil
}
func (f *fakeGrowthServer) ListGrowthEvents(context.Context, growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	return nil, nil
}

func seedProfileCompanion(t *testing.T, srv *Server, name, spec string) string {
	t.Helper()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: name, Specialization: spec})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create %s: status=%d body=%s", name, w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("seed json: %v", err)
	}
	return created["companion_id"].(string)
}

func TestGetCompanionInstance_ReturnsFullProfile_Identity(t *testing.T) {
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	// Identity block (always present).
	for _, k := range []string{
		"companion_id", "tenant_id", "owner_gcid", "name", "specialization",
		"evolution_tier", "skill_slots_unlocked", "memory_context_capacity",
		"skill_grants", "configured_rules", "memory_bank_app_name",
		"created_at", "updated_at",
	} {
		if _, ok := resp[k]; !ok {
			t.Errorf("profile missing identity field %q", k)
		}
	}
	if resp["name"] != "Newton" {
		t.Errorf("name = %v, want Newton", resp["name"])
	}
	if resp["display_name"] != "Newton" {
		t.Errorf("display_name = %v, want Newton (alias)", resp["display_name"])
	}
}

func TestGetCompanionInstance_ReturnsFullProfile_GrowthAxis(t *testing.T) {
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Eira", "cspo")

	hatched := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	stageUp := hatched
	srv.Growth = &fakeGrowthServer{
		state: &growth.State{
			CompanionID:      id,
			GrowthStage:      2,
			StageName:        "fledgling",
			Species:          "dragon",
			ShinyVariant:     true,
			Rarity:           "rare",
			ExpCurrent:       12,
			ExpNextThreshold: 200,
			ExpCumulative:    62,
			EffectiveLLMTier: "flash",
			ResonantAtomID:   "00000000-0000-7000-8000-00000000a0a2",
			HatchedAt:        &hatched,
			LastStageUpAt:    &stageUp,
		},
	}

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	gs, ok := resp["growth_state"].(map[string]any)
	if !ok {
		t.Fatalf("profile missing growth_state object: %v", resp["growth_state"])
	}
	if gs["stage"].(float64) != 2 {
		t.Errorf("growth_state.stage = %v, want 2", gs["stage"])
	}
	if gs["stage_name"] != "fledgling" {
		t.Errorf("growth_state.stage_name = %v, want fledgling", gs["stage_name"])
	}
	if gs["effective_llm_tier"] != "flash" {
		t.Errorf("growth_state.effective_llm_tier = %v, want flash", gs["effective_llm_tier"])
	}
	if gs["growth_exp"].(float64) != 62 {
		t.Errorf("growth_state.growth_exp = %v, want 62 (cumulative)", gs["growth_exp"])
	}
	if gs["resonant_atom_id"] != "00000000-0000-7000-8000-00000000a0a2" {
		t.Errorf("growth_state.resonant_atom_id = %v", gs["resonant_atom_id"])
	}
	if gs["last_stage_up_at"] == nil {
		t.Error("growth_state.last_stage_up_at is null; want timestamp")
	}
	// Identity-level shiny + rarity surface from the growth axis.
	cos, ok := resp["cosmetic"].(map[string]any)
	if !ok {
		t.Fatalf("profile missing cosmetic object")
	}
	if cos["shiny"] != true {
		t.Errorf("cosmetic.shiny = %v, want true", cos["shiny"])
	}
	if cos["rarity"] != "rare" {
		t.Errorf("cosmetic.rarity = %v, want rare", cos["rarity"])
	}
	// Identity block carries species + species_rarity + shiny_variant per F5.
	if resp["species"] != "dragon" {
		t.Errorf("species = %v, want dragon", resp["species"])
	}
	if resp["species_rarity"] != "rare" {
		t.Errorf("species_rarity = %v, want rare", resp["species_rarity"])
	}
	if resp["shiny_variant"] != true {
		t.Errorf("shiny_variant = %v, want true", resp["shiny_variant"])
	}
	if resp["growth_stage"].(float64) != 2 {
		t.Errorf("growth_stage = %v, want 2", resp["growth_stage"])
	}
}

func TestGetCompanionInstance_GrowthGracefulWhenNotWired(t *testing.T) {
	// When Growth is nil (no pgx pool / dev) the profile still returns 200
	// with a pre-hatch (Stage-0) growth projection — never 503/500. F5
	// requires the profile to degrade gracefully like the list endpoint.
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")
	srv.Growth = nil

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (graceful), body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &resp)
	gs, ok := resp["growth_state"].(map[string]any)
	if !ok {
		t.Fatalf("profile missing growth_state even when growth unwired")
	}
	if gs["stage"].(float64) != 0 {
		t.Errorf("growth_state.stage = %v, want 0 (pre-hatch fallback)", gs["stage"])
	}
}

func TestGetCompanionInstance_GrowthNotFoundDegradesToPreHatch(t *testing.T) {
	// A wired Growth server that returns ErrCompanionNotFound (no growth row
	// provisioned yet for this Companion) must NOT 404 the whole profile —
	// the base instance exists. Degrade to a pre-hatch growth projection.
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")
	srv.Growth = &fakeGrowthServer{err: growth.ErrCompanionNotFound}

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &resp)
	gs := resp["growth_state"].(map[string]any)
	if gs["stage"].(float64) != 0 {
		t.Errorf("growth_state.stage = %v, want 0 (pre-hatch on growth miss)", gs["stage"])
	}
}

func TestGetCompanionInstance_RosterContext(t *testing.T) {
	srv := NewServer()
	_ = seedProfileCompanion(t, srv, "Newton", "math")
	_ = seedProfileCompanion(t, srv, "Curie", "history")
	id := seedProfileCompanion(t, srv, "Lovelace", "coding")

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &resp)
	rc, ok := resp["roster_context"].(map[string]any)
	if !ok {
		t.Fatalf("profile missing roster_context object: %v", resp["roster_context"])
	}
	if rc["roster_size"].(float64) != 3 {
		t.Errorf("roster_context.roster_size = %v, want 3", rc["roster_size"])
	}
	if rc["max_companions"].(float64) != float64(companion.DefaultMaxCompanionsPerUser) {
		t.Errorf("roster_context.max_companions = %v, want %d", rc["max_companions"], companion.DefaultMaxCompanionsPerUser)
	}
}

func TestGetCompanionInstance_MemorySummaryOmittedWhenMemoryBankUnwired(t *testing.T) {
	// F4 (Vertex AI Memory Bank) is designed separately. With no Memory Bank
	// resolver wired, the optional memory_summary field is OMITTED (omitempty)
	// — the profile must NEVER block on a missing memory summary.
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")
	if srv.MemorySummaries != nil {
		t.Fatal("MemorySummaries should default to nil (Memory Bank not wired in this batch)")
	}

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &resp)
	if _, present := resp["memory_summary"]; present {
		t.Errorf("memory_summary should be omitted when Memory Bank is not wired, got %v", resp["memory_summary"])
	}
}

// fakeMemoryResolver lets the test wire a memory summary so the optional
// field renders when F4 eventually lands. This batch ships ONLY the nil-safe
// port — no Vertex AI Memory Bank client.
type fakeMemoryResolver struct {
	summary string
	err     error
}

func (f fakeMemoryResolver) ResolveMemorySummary(_ context.Context, _, _, _ string) (string, error) {
	return f.summary, f.err
}

func TestGetCompanionInstance_MemorySummaryRenderedWhenResolverWired(t *testing.T) {
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")
	srv.MemorySummaries = fakeMemoryResolver{summary: "Loves calculus; struggles with proofs."}

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &resp)
	if resp["memory_summary"] != "Loves calculus; struggles with proofs." {
		t.Errorf("memory_summary = %v, want the resolved summary", resp["memory_summary"])
	}
}

func TestGetCompanionInstance_MemorySummaryOmittedWhenResolverErrors(t *testing.T) {
	// A resolver error (Memory Bank unreachable) MUST degrade to omitting the
	// field — never 500 the whole profile (memory is supplementary).
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")
	srv.MemorySummaries = fakeMemoryResolver{err: context.DeadlineExceeded}

	got := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", got.Code, got.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &resp)
	if _, present := resp["memory_summary"]; present {
		t.Errorf("memory_summary should be omitted on resolver error, got %v", resp["memory_summary"])
	}
}

func TestGetCompanionInstance_404OnUnknownProfile(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/00000000-0000-7000-8000-0000deadbeef", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGetCompanionInstance_RequiresAuthProfile(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/companions/some-id", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestGetCompanionInstance_GrowthScopedToCallerGCID(t *testing.T) {
	// The growth read MUST be invoked with the caller's gcid so RLS + the
	// service-layer ownership guard scope correctly.
	srv := NewServer()
	id := seedProfileCompanion(t, srv, "Newton", "math")
	fg := &fakeGrowthServer{state: &growth.State{CompanionID: id, GrowthStage: 1, StageName: "baby"}}
	srv.Growth = fg

	_ = authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if fg.gotGCID != testGCID {
		t.Errorf("growth read caller_gcid = %q, want %q", fg.gotGCID, testGCID)
	}
	if fg.gotTID != testTenant {
		t.Errorf("growth read tenant_id = %q, want %q", fg.gotTID, testTenant)
	}
	if fg.gotFID != id {
		t.Errorf("growth read companion_id = %q, want %q", fg.gotFID, id)
	}
}
