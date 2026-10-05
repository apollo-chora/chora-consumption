package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ----- test fake GrowthServer -----

type fakeGrowth struct {
	getFn       func(tenantID, companionID, callerGCID string) (*growth.State, error)
	hatchFn     func(in growth.HatchEggInput) (*growth.HatchEggResponse, error)
	revealFn    func(in growth.RevealBreedInput) (*growth.RevealBreedResponse, error)
	resonanceFn func(in growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error)
	revFn       func(in growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error)
	listFn      func(in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error)
}

func (f *fakeGrowth) RevealBreed(_ context.Context, in growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
	if f.revealFn != nil {
		return f.revealFn(in)
	}
	return nil, errors.New("not stubbed")
}

func (f *fakeGrowth) PickResonantConcept(_ context.Context, in growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error) {
	if f.resonanceFn != nil {
		return f.resonanceFn(in)
	}
	return nil, errors.New("not stubbed")
}

func (f *fakeGrowth) GetCompanionGrowth(_ context.Context, tenantID, companionID, callerGCID string) (*growth.State, error) {
	if f.getFn != nil {
		return f.getFn(tenantID, companionID, callerGCID)
	}
	return nil, errors.New("not stubbed")
}
func (f *fakeGrowth) HatchEgg(_ context.Context, in growth.HatchEggInput) (*growth.HatchEggResponse, error) {
	if f.hatchFn != nil {
		return f.hatchFn(in)
	}
	return nil, errors.New("not stubbed")
}
func (f *fakeGrowth) TriggerSourceRevelation(_ context.Context, in growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error) {
	if f.revFn != nil {
		return f.revFn(in)
	}
	return nil, errors.New("not stubbed")
}
func (f *fakeGrowth) ListGrowthEvents(_ context.Context, in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	if f.listFn != nil {
		return f.listFn(in)
	}
	return nil, errors.New("not stubbed")
}

// ----- helpers -----

func newServerWithGrowth(g GrowthServer) *Server {
	srv := NewServer()
	srv.Growth = g
	return srv
}

func doGrowthReq(t *testing.T, srv *Server, method, path string, body any, tenantID, gcid string) *http.Response {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, buf)
	r.Header.Set("X-Tenant-Id", tenantID)
	r.Header.Set("gcid", gcid)
	r.Header.Set("traceparent", "00-aaa-bbb-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	return w.Result()
}

// ----- tests -----

func TestGetGrowth_HappyPath(t *testing.T) {
	tNow := time.Now()
	g := &fakeGrowth{
		getFn: func(tenantID, companionID, callerGCID string) (*growth.State, error) {
			return &growth.State{
				CompanionID:            companionID,
				GrowthStage:            2,
				StageName:              "fledgling",
				Species:                "owl",
				ExpCurrent:             10,
				ExpNextThreshold:       200,
				ExpCumulative:          60,
				EffectiveLLMTier:       "flash-lite",
				EffectiveMaxOutputToks: 800,
				UnlockedTools:          []string{"cite_atom", "atom_search"},
				HatchedAt:              &tNow,
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got stateResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.GrowthStage != 2 {
		t.Errorf("stage = %d", got.GrowthStage)
	}
	if got.Species != "owl" {
		t.Errorf("species = %q", got.Species)
	}
}

func TestGetGrowth_MissingContext(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/fam-1/growth", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", w.Result().StatusCode)
	}
}

func TestGetGrowth_NotWired(t *testing.T) {
	srv := NewServer()
	srv.Growth = nil
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestGetGrowth_NotFound(t *testing.T) {
	g := &fakeGrowth{
		getFn: func(_, _, _ string) (*growth.State, error) {
			return nil, growth.ErrCompanionNotFound
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestHatchEgg_HappyPath(t *testing.T) {
	called := false
	g := &fakeGrowth{
		hatchFn: func(in growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			called = true
			if in.DisplayName != "Eira" {
				t.Errorf("display_name = %q", in.DisplayName)
			}
			return &growth.HatchEggResponse{
				State:             growth.State{CompanionID: in.CompanionID, GrowthStage: 1, StageName: "baby", Species: "dragon"},
				Species:           "dragon",
				ShinyVariant:      false,
				Rarity:            "legendary",
				RolledProbability: 25.0,
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	body := hatchEggReq{
		DisplayName:    "Eira",
		Tone:           "socratic",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "atom-1",
	}
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch", body, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d body=%s", res.StatusCode, b)
	}
	if !called {
		t.Errorf("HatchEgg not called")
	}
	var got hatchEggResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.Species != "dragon" {
		t.Errorf("species = %q", got.Species)
	}
}

func TestHatchEgg_BadJSON(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	r := httptest.NewRequest(http.MethodPost, "/v1/me/companions/fam-1/hatch",
		strings.NewReader("not-json"))
	r.Header.Set("X-Tenant-Id", "t")
	r.Header.Set("gcid", "g")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", w.Result().StatusCode)
	}
}

func TestHatchEgg_AlreadyHatched(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, growth.ErrAlreadyHatched
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer", ResonantAtomID: "a-1"},
		"tenant-1", "user-1")
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d", res.StatusCode)
	}
}

// F-I1.3 (ADR-228): a pre-stirring hatch attempt maps to 409 NOT_STIRRING so
// the FE can render "your egg needs more incubation" rather than a 500.
func TestHatchEgg_NotStirring(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, growth.ErrNotStirring
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer", ResonantAtomID: "a-1"},
		"tenant-1", "user-1")
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if code, _ := body["code"].(string); code != "NOT_STIRRING" {
		t.Errorf("error code = %v, want NOT_STIRRING", body["code"])
	}
}

func TestTriggerSourceRevelation_HappyPath(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	g := &fakeGrowth{
		revFn: func(in growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error) {
			return &growth.TriggerSourceRevelationResponse{
				State:                 growth.State{CompanionID: in.CompanionID, GrowthStage: 3},
				PreviewLLMTier:        "pro",
				WindowExpiresAt:       expires,
				WindowDurationSeconds: 86400,
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/source-revelation",
		triggerRevelationReq{ManaTier: "premium"}, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d body=%s", res.StatusCode, b)
	}
	var got triggerRevelationResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.PreviewLLMTier != "pro" {
		t.Errorf("preview = %q", got.PreviewLLMTier)
	}
}

func TestTriggerSourceRevelation_AlreadyConsumed(t *testing.T) {
	g := &fakeGrowth{
		revFn: func(_ growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error) {
			return nil, growth.ErrAhaMomentConsumed
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/source-revelation",
		triggerRevelationReq{ManaTier: "premium"}, "tenant-1", "user-1")
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestListGrowthEvents_HappyPath(t *testing.T) {
	tNow := time.Now()
	g := &fakeGrowth{
		listFn: func(in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
			if in.PageSize != 50 {
				t.Errorf("PageSize = %d, want 50", in.PageSize)
			}
			return &growth.ListGrowthEventsOutput{
				Events: []*growth.GrowthEventRow{
					{GrowthEventID: "e1", Source: "atom_session", AwardedDelta: 3, ExpTotalAfter: 3, AwardedAt: tNow},
				},
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth-events", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got listEventsResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if len(got.Events) != 1 || got.Events[0].Source != "atom_session" {
		t.Errorf("events = %v", got.Events)
	}
}

// CHO-2144 BE gap: the growth-events row persists only exp + a triggered_stage_up
// bool, but the FE needs stage_before/stage_after to render the numeric stage-up
// pill. Stage is a pure function of cumulative EXP, so the handler reconstructs
// both from ExpTotalAfter (− AwardedDelta) via the growth curve — no schema change.
func TestListGrowthEvents_ReconstructsStageBeforeAfter(t *testing.T) {
	const before, delta = 0, 5000 // a single large award: stage 0 → top stage
	g := &fakeGrowth{
		listFn: func(in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
			return &growth.ListGrowthEventsOutput{
				Events: []*growth.GrowthEventRow{
					{GrowthEventID: "e1", Source: "quiz", AwardedDelta: delta,
						ExpTotalAfter: before + delta, TriggeredStageUp: true, AwardedAt: time.Now()},
				},
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth-events", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got listEventsResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if len(got.Events) != 1 {
		t.Fatalf("events = %v", got.Events)
	}
	if wantAfter := growth.StageForExp(before + delta); got.Events[0].StageAfter != wantAfter {
		t.Errorf("stage_after = %d, want %d (StageForExp(exp_total_after))", got.Events[0].StageAfter, wantAfter)
	}
	if wantBefore := growth.StageForExp(before); got.Events[0].StageBefore != wantBefore {
		t.Errorf("stage_before = %d, want %d (StageForExp(exp_total_after - delta))", got.Events[0].StageBefore, wantBefore)
	}
}

func TestListGrowthEvents_CustomPageSize(t *testing.T) {
	g := &fakeGrowth{
		listFn: func(in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
			if in.PageSize != 25 {
				t.Errorf("PageSize = %d, want 25", in.PageSize)
			}
			return &growth.ListGrowthEventsOutput{Events: nil}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth-events?page_size=25", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	res := doGrowthReq(t, srv, http.MethodDelete, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestUnknownSubpath(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/bogus-subpath", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", res.StatusCode)
	}
}

// Ensure growthServiceAdapter compiles with a real growth.Service.
func TestGrowthServiceAdapterCompiles(t *testing.T) {
	// We don't actually run anything — just ensure NewGrowthServiceAdapter
	// type-checks against a real growth.Service.
	var s *growth.Service
	_ = NewGrowthServiceAdapter(s)
	_ = context.Background
}
