package http

// companion_growth_retirement_test.go — CHO-2013 P1 (R3-1): PickKgNeighbor +
// the per-stage neighbor-count geometry are RETIRED, superseded by binding +
// ring radius (resonant-concept centre). The REST surface disappears (404),
// and the growth state stops carrying visible_kg_neighbors (the column stays
// in the DB per the xp_unlock_threshold discipline; the read path goes).

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestKgNeighborsRoute_Retired404(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{})
	resp := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/kg-neighbors",
		map[string]string{"atom_id": "01970000-0000-7000-a000-000000000001"}, "tenant-1", "gcid-1")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d want 404 (surface retired per R3-1)", resp.StatusCode)
	}
}

func TestGrowthState_NoVisibleKgNeighborsKey(t *testing.T) {
	srv := newServerWithGrowth(&fakeGrowth{
		getFn: func(_, companionID, _ string) (*growth.State, error) {
			return &growth.State{CompanionID: companionID, GrowthStage: 2, StageName: "hatchling"}, nil
		},
	})
	resp := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "gcid-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["visible_kg_neighbors"]; present {
		t.Fatalf("visible_kg_neighbors still in the state payload: %v", raw)
	}
}
