// dose_preferences_test.go — CHO-2045 / ADR-224 sub-phase B2: the per-KG dose
// preference filter (weakness pool) + the GET/PUT /v1/me/dose-preferences
// endpoint. A learner excludes a map (Goal) from their daily dose; its concepts
// (Goal.ConceptSet) are then dropped from the weakness pool before allocation.
// Default = every map included (sparse store: only exclusions are rows).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/dose_pref"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

type dosePrefStub struct {
	excluded   map[string]bool
	excludeErr error
	setErr     error
	setCalls   []struct {
		mapID    string
		included bool
	}
}

func (s *dosePrefStub) List(context.Context, string, string) ([]dose_pref.DoseKGPref, error) {
	return nil, nil
}
func (s *dosePrefStub) ExcludedMapIDs(context.Context, string, string) (map[string]bool, error) {
	if s.excludeErr != nil {
		return nil, s.excludeErr
	}
	return s.excluded, nil
}
func (s *dosePrefStub) Set(_ context.Context, _, _, mapID string, included bool, _ time.Time) (dose_pref.DoseKGPref, error) {
	s.setCalls = append(s.setCalls, struct {
		mapID    string
		included bool
	}{mapID, included})
	if s.setErr != nil {
		return dose_pref.DoseKGPref{}, s.setErr
	}
	return dose_pref.DoseKGPref{MapID: mapID, Included: included}, nil
}

// ── Filter: an excluded map's concepts are dropped from the weakness pool ─────

func TestDoseGrowthEdges_ExcludesPreferredOutMaps(t *testing.T) {
	srv := NewServer()
	srv.LearnerWeakness = &doseLWStub{items: []lw.LearnerWeakness{
		doseEdgeCat(t, "Fractions", "Math", 0.8, "atom-x"),         // concept_key "fractions"
		doseEdgeCat(t, "Photosynthesis", "Science", 0.6, "atom-y"), // concept_key "photosynthesis"
	}}
	srv.DoseKGPrefs = &dosePrefStub{excluded: map[string]bool{"goal-math": true}}
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{
		"goal-math": {GoalID: "goal-math", ConceptSet: []string{"fractions"}},
	}}

	edges := srv.doseGrowthEdges(context.Background(), testTenant, testGCID)
	labels := map[string]bool{}
	for _, e := range edges {
		labels[e.ConceptLabel] = true
	}
	if labels["Fractions"] {
		t.Fatalf("excluded map's edge (Fractions) must be filtered out; got %+v", edges)
	}
	if !labels["Photosynthesis"] {
		t.Fatalf("non-excluded edge (Photosynthesis) must remain; got %+v", edges)
	}
}

func TestDoseGrowthEdges_PrefReadError_FailsSoftNoFilter(t *testing.T) {
	srv := NewServer()
	srv.LearnerWeakness = &doseLWStub{items: []lw.LearnerWeakness{
		doseEdgeCat(t, "Fractions", "Math", 0.8, "atom-x"),
	}}
	srv.DoseKGPrefs = &dosePrefStub{excludeErr: errors.New("boom")}
	srv.Goals = &esGoalReader{}

	if got := len(srv.doseGrowthEdges(context.Background(), testTenant, testGCID)); got != 1 {
		t.Fatalf("a preference read error must fail soft (no filter); got %d edges", got)
	}
}

func TestDoseGrowthEdges_NoPrefRepo_AllEdgesPass(t *testing.T) {
	srv := NewServer()
	srv.LearnerWeakness = &doseLWStub{items: []lw.LearnerWeakness{
		doseEdgeCat(t, "Fractions", "Math", 0.8, "atom-x"),
	}}
	// DoseKGPrefs nil ⇒ default all-included ⇒ no filtering.
	if got := len(srv.doseGrowthEdges(context.Background(), testTenant, testGCID)); got != 1 {
		t.Fatalf("nil preference repo ⇒ no filter; got %d edges", got)
	}
}

// ── Endpoint: GET returns the excluded set; PUT upserts one preference ────────

func TestDosePreferences_GetReturnsExcludedSet(t *testing.T) {
	srv := NewServer()
	srv.DoseKGPrefs = &dosePrefStub{excluded: map[string]bool{"goal-1": true, "goal-2": true}}

	w := authedReq(t, srv, http.MethodGet, "/v1/me/dose-preferences", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ExcludedMapIDs []string `json:"excludedMapIds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.ExcludedMapIDs) != 2 {
		t.Fatalf("want 2 excluded map ids, got %v", resp.ExcludedMapIDs)
	}
}

func TestDosePreferences_PutSetsPreference(t *testing.T) {
	srv := NewServer()
	stub := &dosePrefStub{}
	srv.DoseKGPrefs = stub
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{"goal-1": {GoalID: "goal-1"}}}

	w := authedReq(t, srv, http.MethodPut, "/v1/me/dose-preferences",
		map[string]any{"mapId": "goal-1", "included": false})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(stub.setCalls) != 1 || stub.setCalls[0].mapID != "goal-1" || stub.setCalls[0].included {
		t.Fatalf("Set(goal-1, included=false) not called correctly: %+v", stub.setCalls)
	}
}

func TestDosePreferences_PutUnknownMap_404(t *testing.T) {
	srv := NewServer()
	srv.DoseKGPrefs = &dosePrefStub{}
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{}} // GetByID ⇒ (nil,nil) live-miss

	w := authedReq(t, srv, http.MethodPut, "/v1/me/dose-preferences",
		map[string]any{"mapId": "ghost", "included": false})
	if w.Code != http.StatusNotFound {
		t.Fatalf("PUT for a non-existent map must 404; got %d", w.Code)
	}
}

func TestDosePreferences_NilRepo_503(t *testing.T) {
	srv := NewServer() // DoseKGPrefs nil
	w := authedReq(t, srv, http.MethodGet, "/v1/me/dose-preferences", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil dose-prefs repo ⇒ 503 (fail-loud); got %d", w.Code)
	}
}
