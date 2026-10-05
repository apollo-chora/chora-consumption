package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// stubBreedDist is a single-SKU breed distribution for tests.
type stubBreedDist struct{}

func (stubBreedDist) Lookup(_, _ string) ([]growth.BreedWeight, error) {
	return []growth.BreedWeight{
		{Species: "owl", Probability: 50, Rarity: "common"},
		{Species: "dragon", Probability: 50, Rarity: "legendary"},
	}, nil
}

// TestGrowthServiceAdapter_RealServicePassthrough wires a real
// growth.Service via NewGrowthServiceAdapter and exercises each method.
func TestGrowthServiceAdapter_RealServicePassthrough(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t1", OwnerGCID: "u1",
		GrowthStage: 2, GrowthExp: 100,
	})
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, AggregateType: "companion"})
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: outbox.NewGrowthOutbox(pub),
		Dist:   stubBreedDist{},
		// CHO-2225: NewID is required — event_id is a UUIDv7 envelope field.
		NewID: func() string { return "019f6a1c-89f8-7089-8142-e3af46113b6b" },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	a := NewGrowthServiceAdapter(svc)
	ctx := context.Background()

	if _, err := a.GetCompanionGrowth(ctx, "t1", "fam-1", "u1"); err != nil {
		t.Errorf("Get: %v", err)
	}
	if _, err := a.ListGrowthEvents(ctx, growth.ListGrowthEventsInput{
		TenantID: "t1", CompanionID: "fam-1", CallerGCID: "u1", PageSize: 10,
	}); err != nil {
		t.Errorf("List: %v", err)
	}
	// Seed an egg row for the reveal → hatch ceremony (CHO-2229: the roll
	// happens at RevealBreed; HatchEgg is commit-only).
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "t1", OwnerGCID: "u1",
		GrowthStage: 0, GrowthExp: 30, EggSku: "egg.standard.v1", // F-I1.3: stirring
	})
	if _, err := a.RevealBreed(ctx, growth.RevealBreedInput{
		TenantID: "t1", CompanionID: "fam-egg", OwnerGCID: "u1",
		Traceparent: "00-aa-bb-00",
	}); err != nil {
		t.Errorf("Reveal: %v", err)
	}
	if _, err := a.HatchEgg(ctx, growth.HatchEggInput{
		TenantID: "t1", CompanionID: "fam-egg", OwnerGCID: "u1",
		DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-aa-bb-00",
	}); err != nil {
		t.Errorf("Hatch: %v", err)
	}
	// Seed a stage-3 row.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-3", TenantID: "t1", OwnerGCID: "u1",
		GrowthStage: 3,
	})
	if _, err := a.TriggerSourceRevelation(ctx, growth.TriggerSourceRevelationInput{
		TenantID: "t1", CompanionID: "fam-3", OwnerGCID: "u1",
		ManaTier: "premium", Traceparent: "00-aa-bb-00",
	}); err != nil {
		t.Errorf("Trigger: %v", err)
	}
}

func TestHatchEgg_NotWired(t *testing.T) {
	srv := NewServer()
	srv.Growth = nil
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "x"}, "t", "u")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestHatchEgg_MissingContext(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	r := httptest.NewRequest(http.MethodPost, "/v1/me/companions/fam-1/hatch",
		strings.NewReader(`{"display_name":"x"}`))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", w.Result().StatusCode)
	}
}

func TestHatchEgg_InvalidArguments(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, growth.ErrInvalidArguments
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{}, "t", "u")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestTriggerSourceRevelation_NotWired(t *testing.T) {
	srv := NewServer()
	srv.Growth = nil
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/source-revelation",
		triggerRevelationReq{}, "t", "u")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestTriggerSourceRevelation_BadJSON(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	r := httptest.NewRequest(http.MethodPost, "/v1/me/companions/fam-1/source-revelation",
		strings.NewReader("bad"))
	r.Header.Set("X-Tenant-Id", "t")
	r.Header.Set("gcid", "u")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", w.Result().StatusCode)
	}
}

func TestListGrowthEvents_NotWired(t *testing.T) {
	srv := NewServer()
	srv.Growth = nil
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth-events",
		nil, "t", "u")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestListGrowthEvents_MissingContext(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/fam-1/growth-events", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", w.Result().StatusCode)
	}
}

func TestListGrowthEvents_PageTokenPassed(t *testing.T) {
	g := &fakeGrowth{
		listFn: func(in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
			if in.PageToken != "ptok" {
				t.Errorf("PageToken = %q, want ptok", in.PageToken)
			}
			return &growth.ListGrowthEventsOutput{}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet,
		"/v1/me/companions/fam-1/growth-events?page_token=ptok",
		nil, "t", "u")
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestMapGrowthError_InvalidSource(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, growth.ErrInvalidSource
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{Tone: "socratic", DisplayName: "x", LearnerPersona: "curious-explorer", ResonantAtomID: "a"},
		"t", "u")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestMapGrowthError_NotInEggStage(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, growth.ErrNotInEggStage
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer", ResonantAtomID: "a"},
		"t", "u")
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestMapGrowthError_InternalDefault(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, io.EOF // arbitrary non-sentinel error
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer", ResonantAtomID: "a"},
		"t", "u")
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestRoute_MissingID(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	// ServeMux normalizes double-slashes; a redirect or a 404 is both
	// acceptable boundary behaviour. We just exercise the route to cover
	// the dispatch branch.
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions//growth", nil, "t", "u")
	if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want 404 or 307", res.StatusCode)
	}
}

func TestToStateResp_NilSafety(t *testing.T) {
	if toStateResp(nil) != nil {
		t.Errorf("nil should map to nil")
	}
}

func TestToStateResp_EmptyFields(t *testing.T) {
	st := &growth.State{
		CompanionID: "f",
		GrowthStage: 1,
	}
	out := toStateResp(st)
	if out.UnlockedTools == nil {
		t.Errorf("UnlockedTools should default to []")
	}
}

func TestTriggerSourceRevelation_MissingContext(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	r := httptest.NewRequest(http.MethodPost, "/v1/me/companions/fam-1/source-revelation",
		strings.NewReader(`{"mana_tier":"premium"}`))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", w.Result().StatusCode)
	}
}

func TestListGrowthEvents_ServiceError(t *testing.T) {
	g := &fakeGrowth{
		listFn: func(_ growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
			return nil, growth.ErrInvalidArguments
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth-events", nil, "t", "u")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", res.StatusCode)
	}
}

// Ensure JSON encoding survives time fields in the response.
func TestHatchEgg_ResponseJSON(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(_ growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return &growth.HatchEggResponse{
				State:        growth.State{CompanionID: "f", GrowthStage: 1, DisplayName: "Ember"},
				Species:      "owl",
				ShinyVariant: true,
				Rarity:       "common",
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer", ResonantAtomID: "a"},
		"t", "u")
	var got hatchEggResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if !got.ShinyVariant {
		t.Errorf("shiny not propagated")
	}
	// CHO-2028: the growth state must surface display_name or the FE
	// profile header falls back to "Untitled Companion" right after the
	// learner named their companion in the hatch ceremony.
	if got.State == nil || got.State.DisplayName != "Ember" {
		t.Errorf("display_name not propagated in state resp: %+v", got.State)
	}
}
