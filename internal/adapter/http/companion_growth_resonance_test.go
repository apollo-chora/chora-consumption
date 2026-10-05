package http

// companion_growth_resonance_test.go — CHO-2013 P1: the awakening
// resonant-concept pick endpoint.
//
//	POST /v1/me/companions/{id}/resonance   body: {concept_id}
//
// Error contract: 409 COMPANION_NOT_BOUND / COMPANION_NOT_HATCHED,
// 404 CONCEPT_NOT_FOUND / COMPANION_NOT_FOUND, 400 INVALID_ARGUMENT,
// 503 RESONANCE_NOT_WIRED (fail-loud unwired ports).

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestResonance_HappyPathReturnsState(t *testing.T) {
	var got growth.PickResonantConceptInput
	srv := newServerWithGrowth(&fakeGrowth{
		resonanceFn: func(in growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error) {
			got = in
			return &growth.PickResonantConceptResponse{State: growth.State{
				CompanionID:       in.CompanionID,
				GrowthStage:       2,
				StageName:         "hatchling",
				ResonantConceptID: in.ConceptID,
			}}, nil
		},
	})
	resp := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/resonance",
		map[string]string{"concept_id": "019f2620-0000-7000-8000-00000000cc01"}, "tenant-1", "gcid-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.CompanionID != "fam-1" || got.TenantID != "tenant-1" || got.OwnerGCID != "gcid-1" {
		t.Fatalf("input not threaded: %+v", got)
	}
	if got.ConceptID != "019f2620-0000-7000-8000-00000000cc01" {
		t.Fatalf("concept_id not threaded: %q", got.ConceptID)
	}
	var body struct {
		State struct {
			ResonantConceptID string `json:"resonant_concept_id"`
		} `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.State.ResonantConceptID != got.ConceptID {
		t.Fatalf("state.resonant_concept_id = %q", body.State.ResonantConceptID)
	}
}

func TestResonance_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantKey  string
	}{
		{"not bound", growth.ErrCompanionNotBound, http.StatusConflict, "COMPANION_NOT_BOUND"},
		{"not hatched", growth.ErrCompanionNotHatched, http.StatusConflict, "COMPANION_NOT_HATCHED"},
		{"concept missing", growth.ErrConceptNotFound, http.StatusNotFound, "CONCEPT_NOT_FOUND"},
		{"companion missing", growth.ErrCompanionNotFound, http.StatusNotFound, "COMPANION_NOT_FOUND"},
		{"invalid", growth.ErrInvalidArguments, http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"unwired", growth.ErrResonanceNotWired, http.StatusServiceUnavailable, "RESONANCE_NOT_WIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServerWithGrowth(&fakeGrowth{
				resonanceFn: func(growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error) {
					return nil, tc.err
				},
			})
			resp := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/resonance",
				map[string]string{"concept_id": "019f2620-0000-7000-8000-00000000cc01"}, "tenant-1", "gcid-1")
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d want %d", resp.StatusCode, tc.wantCode)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Code != tc.wantKey {
				t.Fatalf("code = %q want %q", body.Code, tc.wantKey)
			}
		})
	}
}

func TestResonance_MethodNotAllowed(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	resp := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/resonance", nil, "tenant-1", "gcid-1")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d want 405", resp.StatusCode)
	}
}
