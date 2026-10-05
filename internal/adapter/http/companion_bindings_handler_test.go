// companion_bindings_handler_test.go — WS-A3 (My Knowledge unification, CHO-2005):
// GET /v1/me/companions/bindings is now sourced from the learner's Goals
// (map = Goal, ADR-214 D1) — one binding per map that has a designated Companion
// (Goal.AttachedCompanionID). The theme-keyed CompanionMapBinding is retired.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

func bindingsServe(s *ExtServer, method, tenantID, gcid string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/v1/me/companions/bindings", nil)
	if tenantID != "" {
		r.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	w := httptest.NewRecorder()
	s.handleMeCompanionBindings(w, r)
	return w
}

func TestBindings_SourcedFromGoals(t *testing.T) {
	fid := "01970000-0000-7000-a000-0000000000d1"
	g1 := mapsGoal("g-1", mapsPtr("c-root"))
	g1.AttachedCompanionID = &fid
	g2 := mapsGoal("g-2", mapsPtr("c-x")) // no Companion → excluded
	s := &ExtServer{
		Goals:    &mapsGoalStub{list: []*goal.Goal{g1, g2}},
		Concepts: acqRootConcept("Scrum"),
	}
	w := bindingsServe(s, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d; want 1 (only the attached map)", len(resp.Items))
	}
	b := resp.Items[0]
	if b["bindingId"] != "g-1" {
		t.Errorf("bindingId = %v; want g-1 (the map/goal id)", b["bindingId"])
	}
	if b["companionId"] != fid {
		t.Errorf("companionId = %v; want %s", b["companionId"], fid)
	}
	if b["mapTheme"] != "Scrum" {
		t.Errorf("mapTheme = %v; want Scrum (root concept title)", b["mapTheme"])
	}
}

func TestBindings_Unwired503(t *testing.T) {
	if w := bindingsServe(&ExtServer{}, http.MethodGet, fmTenantID, fmGCID); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
}

func TestBindings_MethodNotAllowed405(t *testing.T) {
	s := &ExtServer{Goals: &mapsGoalStub{}}
	if w := bindingsServe(s, http.MethodPost, fmTenantID, fmGCID); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestBindings_MissingContext400(t *testing.T) {
	s := &ExtServer{Goals: &mapsGoalStub{}}
	if w := bindingsServe(s, http.MethodGet, "", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
}

func TestBindings_RepoError500(t *testing.T) {
	s := &ExtServer{Goals: &mapsGoalStub{listErr: errors.New("boom")}}
	if w := bindingsServe(s, http.MethodGet, fmTenantID, fmGCID); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", w.Code)
	}
}
