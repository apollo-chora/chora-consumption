// companion_growth_reveal_handler_test.go — CHO-2229 (ADR-228 Phase 2): the
// POST /v1/me/companions/{id}/reveal endpoint. The reveal precedes naming —
// it rolls + persists the breed on a stirring pod, idempotently; the hatch
// endpoint refuses an unrevealed pod with 409 NOT_REVEALED.
package http

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestRevealBreed_HTTP_HappyPath(t *testing.T) {
	revealedAt := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	var got growth.RevealBreedInput
	g := &fakeGrowth{
		revealFn: func(in growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
			got = in
			return &growth.RevealBreedResponse{
				State:             growth.State{CompanionID: in.CompanionID, GrowthStage: 0, StageName: "egg", Species: "fox", RevealedAt: &revealedAt},
				Species:           "fox",
				ShinyVariant:      true,
				Rarity:            "uncommon",
				RolledProbability: 12.5,
				RevealedAt:        revealedAt,
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got.TenantID != "tenant-1" || got.OwnerGCID != "user-1" || got.CompanionID != "fam-1" {
		t.Errorf("input = %+v (headers not mapped)", got)
	}
	if got.Traceparent == "" {
		t.Errorf("traceparent header must reach the domain input")
	}
	var body struct {
		State             *stateResp `json:"state"`
		Species           string     `json:"species"`
		ShinyVariant      bool       `json:"shiny_variant"`
		Rarity            string     `json:"rarity"`
		RolledProbability float64    `json:"rolled_probability"`
		RevealedAt        *time.Time `json:"revealed_at"`
		AlreadyRevealed   bool       `json:"already_revealed"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Species != "fox" || !body.ShinyVariant || body.Rarity != "uncommon" || body.RolledProbability != 12.5 {
		t.Errorf("roll fields = %+v", body)
	}
	if body.RevealedAt == nil || !body.RevealedAt.Equal(revealedAt) {
		t.Errorf("revealed_at = %v, want %v", body.RevealedAt, revealedAt)
	}
	if body.AlreadyRevealed {
		t.Errorf("fresh reveal must report already_revealed=false")
	}
	if body.State == nil || body.State.Species != "fox" {
		t.Errorf("state missing or species undisclosed: %+v", body.State)
	}
	if body.State.RevealedAt == nil {
		t.Errorf("state must carry revealed_at for FE ceremony resume")
	}
}

func TestRevealBreed_HTTP_IdempotentReplay(t *testing.T) {
	revealedAt := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	g := &fakeGrowth{
		revealFn: func(in growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
			return &growth.RevealBreedResponse{
				State:           growth.State{CompanionID: in.CompanionID, GrowthStage: 0, Species: "fox", RevealedAt: &revealedAt},
				Species:         "fox",
				Rarity:          "uncommon",
				RevealedAt:      revealedAt,
				AlreadyRevealed: true,
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("idempotent replay must be 200, got %d", res.StatusCode)
	}
	var body struct {
		AlreadyRevealed bool `json:"already_revealed"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if !body.AlreadyRevealed {
		t.Errorf("replay must report already_revealed=true")
	}
}

func TestRevealBreed_HTTP_MethodNotAllowed(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/reveal", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}

func TestRevealBreed_HTTP_NotStirringMaps409(t *testing.T) {
	g := &fakeGrowth{
		revealFn: func(growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
			return nil, growth.ErrNotStirring
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.Code != "NOT_STIRRING" {
		t.Errorf("code = %q, want NOT_STIRRING", body.Code)
	}
}

func TestRevealBreed_HTTP_MissingContext(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, "", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestHatchEgg_HTTP_NotRevealedMaps409(t *testing.T) {
	g := &fakeGrowth{
		hatchFn: func(growth.HatchEggInput) (*growth.HatchEggResponse, error) {
			return nil, growth.ErrNotRevealed
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer"},
		"tenant-1", "user-1")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.Code != "NOT_REVEALED" {
		t.Errorf("code = %q, want NOT_REVEALED", body.Code)
	}
}

func TestRevealBreed_HTTP_NotWired503(t *testing.T) {
	srv := NewServer()
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
}

// TestRevealBreed_PropagatesRLSContextToService — write-path mirror of the
// Debt #40 contract: the handler must stamp tenant+gcid onto ctx BEFORE the
// domain call so rls.ApplySession can SET LOCAL inside the transaction.
func TestRevealBreed_PropagatesRLSContextToService(t *testing.T) {
	g := &ctxCapturingGrowth{}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, growthRLSTenant, growthRLSGCID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	assertCtxHasTenantGCID(t, g.lastCtx)
}

// TestRevealBreed_HTTP_CarriesAwakeningClass — Phase 3 (CHO-2235): the
// reveal response exposes the domain's species→class taxonomy so the FE
// flavours the reveal (HATCH / WAKE / POWER-ON) without hardcoding it.
func TestRevealBreed_HTTP_CarriesAwakeningClass(t *testing.T) {
	revealedAt := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	g := &fakeGrowth{
		revealFn: func(in growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
			return &growth.RevealBreedResponse{
				State:          growth.State{CompanionID: in.CompanionID, GrowthStage: 0, Species: "fox", RevealedAt: &revealedAt},
				Species:        "fox",
				Rarity:         "uncommon",
				RevealedAt:     revealedAt,
				AwakeningClass: growth.AwakeningWake,
			}, nil
		},
	}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/reveal", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		AwakeningClass string `json:"awakening_class"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AwakeningClass != "wake" {
		t.Errorf("awakening_class = %q, want wake", body.AwakeningClass)
	}
}
