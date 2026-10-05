// companion_growth_nextunlocks_test.go — CHO-2030 (R3-9 named-tease):
// GET /v1/me/companions/{id}/growth carries the next-stage Path unlock
// preview (option A of the R5-1 contract) so the ceremony can reveal
// what is stirring next without a FE fork of the band data.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func nextUnlocksFakeGrowth(stage int, species string) *fakeGrowth {
	return &fakeGrowth{
		getFn: func(tenantID, companionID, callerGCID string) (*growth.State, error) {
			return &growth.State{
				CompanionID:      companionID,
				GrowthStage:      stage,
				StageName:        "fledgling",
				Species:          species,
				ExpCurrent:       2,
				ExpNextThreshold: 200,
			}, nil
		},
	}
}

func TestGetGrowth_NextUnlocksCarriesNextStageBand(t *testing.T) {
	srv := newServerWithGrowth(nextUnlocksFakeGrowth(2, "owl"))
	activateSkill(t, srv, "explain_anew") // make at least one flag state deterministic

	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got stateResp
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	owlPath := seedspec.Paths()["owl"]
	wantKeys := owlPath[4:8] // stage-3 band for a stage-2 companion (bands 4/4/…)
	if len(got.NextUnlocks) != len(wantKeys) {
		t.Fatalf("next_unlocks len = %d, want %d (%v)", len(got.NextUnlocks), len(wantKeys), got.NextUnlocks)
	}
	cat, err := srv.SkillCatalog.ListCatalogue(context.Background())
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	for i, want := range wantKeys {
		row := got.NextUnlocks[i]
		if row.SkillKey != want {
			t.Errorf("[%d].skill_key = %q, want %q", i, row.SkillKey, want)
		}
		if row.UnlocksAtStage != 3 {
			t.Errorf("[%d].unlocks_at_stage = %d, want 3", i, row.UnlocksAtStage)
		}
		if row.CatalogueActive != cat[want].Active {
			t.Errorf("[%d].catalogue_active = %v, want %v (mirror the catalogue)",
				i, row.CatalogueActive, cat[want].Active)
		}
	}
}

func TestGetGrowth_NextUnlocksEmptyAtTopStage(t *testing.T) {
	srv := newServerWithGrowth(nextUnlocksFakeGrowth(6, "owl"))
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got stateResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if len(got.NextUnlocks) != 0 {
		t.Fatalf("next_unlocks = %v, want empty at the top stage", got.NextUnlocks)
	}
}

func TestGetGrowth_NextUnlocksSkippedWhenPathsUnwired(t *testing.T) {
	srv := newServerWithGrowth(nextUnlocksFakeGrowth(2, "owl"))
	srv.SpeciesPaths = nil // pre-CHO-2030 unit servers
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d — unwired paths must not break the growth read", res.StatusCode)
	}
	var got stateResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.NextUnlocks != nil {
		t.Fatalf("next_unlocks = %v, want absent when the reader is unwired", got.NextUnlocks)
	}
}

type erroringPathReader struct{}

func (erroringPathReader) ActivePathForSpecies(context.Context, string) (*companion.SpeciesPath, error) {
	return nil, errors.New("pg exploded")
}

func TestGetGrowth_NextUnlocksReadErrorFailsLoud(t *testing.T) {
	srv := newServerWithGrowth(nextUnlocksFakeGrowth(2, "owl"))
	srv.SpeciesPaths = erroringPathReader{}
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 fail-loud on a path read error", res.StatusCode)
	}
}

func TestGetGrowth_NextUnlocksMissingPathDegradesLoudlyNotFatal(t *testing.T) {
	// A species with no active path (legacy/odd rows) must not brick the
	// growth read — the tease is omitted and the gap is logged.
	srv := newServerWithGrowth(nextUnlocksFakeGrowth(2, "unmapped-species"))
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "user-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the tease omitted", res.StatusCode)
	}
	var got stateResp
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.NextUnlocks != nil {
		t.Fatalf("next_unlocks = %v, want omitted for a pathless species", got.NextUnlocks)
	}
}
