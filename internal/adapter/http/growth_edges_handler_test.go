// growth_edges_handler_test.go — RED tests for the A+ Growth-Edges read API
// (W8a). GET list (filter/sort/paginate) + GET one + DELETE, mapping to the
// already-tested lw.Repository. Learner-scoped (GCID from session headers).
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const (
	geTenant = "01970000-0000-7000-8000-0000000000ab"
	geGCID   = "01970000-0000-7000-9000-0000000000ab"
)

// growthEdgeRepoStub is a settable lw.Repository double for the read handlers.
type growthEdgeRepoStub struct {
	listResult    lw.ListResult
	listQuery     lw.ListQuery
	listAllResult []lw.LearnerWeakness // feeds the rollup list endpoint (ListAll)
	listAllQuery  lw.ListQuery
	listAllErr    error
	getResult     *lw.LearnerWeakness
	getErr        error
	deleted       []string
	deleteErr     error
	softDeleted   bool
}

func (s *growthEdgeRepoStub) Upsert(context.Context, lw.UpsertInput) (lw.UpsertResult, error) {
	return lw.UpsertResult{}, nil
}
func (s *growthEdgeRepoStub) List(_ context.Context, q lw.ListQuery) (lw.ListResult, error) {
	s.listQuery = q
	return s.listResult, nil
}
func (s *growthEdgeRepoStub) ListAll(_ context.Context, q lw.ListQuery) ([]lw.LearnerWeakness, error) {
	s.listAllQuery = q
	return s.listAllResult, s.listAllErr
}
func (s *growthEdgeRepoStub) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return s.getResult, s.getErr
}
func (s *growthEdgeRepoStub) SoftDelete(_ context.Context, _, id string, _ time.Time) error {
	s.softDeleted = true
	s.deleted = append(s.deleted, id)
	return s.deleteErr
}
func (s *growthEdgeRepoStub) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *growthEdgeRepoStub) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *growthEdgeRepoStub) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

func geServer(repo lw.Repository) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.LearnerWeakness = repo
	return ext
}

func geRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Tenant-Id", geTenant)
	r.Header.Set("gcid", geGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	return r
}

func sampleEdge() lw.LearnerWeakness {
	return lw.LearnerWeakness{
		ID:           "01970000-0000-7000-c000-0000000000ab",
		TenantID:     geTenant,
		LearnerGCID:  geGCID,
		ConceptKey:   "riverine-flood-causes",
		ConceptLabel: "causes of riverine flooding",
		Category:     "physical-geography",
		Tags:         []string{"flooding", "causation"},
		TopicID:      "",
		Strength:     0.7,
		Sources:      []lw.Source{lw.SourceExplicit},
		Descriptor: lw.Descriptor{
			Summary:         "shaky on causation",
			Misconceptions:  []string{"confuses cause/effect"},
			SuggestedAngles: []string{"ask for the mechanism"},
		},
		CachedDrillAtomIDs: []string{"01970000-0000-7000-d000-0000000000ab"},
		Status:             lw.StatusActive,
		FirstSeenAt:        time.Now().UTC().Add(-48 * time.Hour),
		LastEvidencedAt:    time.Now().UTC(),
	}
}

func TestListMyGrowthEdges_MapsAndFilters(t *testing.T) {
	// The list endpoint reads the FULL set via ListAll, then read-time rolls up.
	repo := &growthEdgeRepoStub{listAllResult: []lw.LearnerWeakness{sampleEdge()}}
	srv := geServer(repo)

	r := geRequest("GET", "/v1/me/growth-edges?category=physical-geography&min_strength=0.5&sort=last_evidenced_desc&page_size=50&tag=flooding")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items         []map[string]any `json:"items"`
		NextPageToken string           `json:"next_page_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	// One rolled-up edge fits a 50-row page → no next token.
	if len(resp.Items) != 1 || resp.NextPageToken != "" {
		t.Fatalf("items=%d next=%q (single rolled-up edge -> no next page)", len(resp.Items), resp.NextPageToken)
	}
	e := resp.Items[0]
	if e["concept_key"] != "riverine-flood-causes" || e["concept_label"] != "causes of riverine flooding" {
		t.Errorf("concept = %v / %v", e["concept_key"], e["concept_label"])
	}
	if e["status"] != "active" {
		t.Errorf("status = %v", e["status"])
	}
	if srcs, ok := e["sources"].([]any); !ok || len(srcs) != 1 || srcs[0] != "explicit" {
		t.Errorf("sources = %v", e["sources"])
	}
	// Query parsing → the ListAll feed query (filters drive the rollup input).
	if repo.listAllQuery.Category != "physical-geography" {
		t.Errorf("category=%q", repo.listAllQuery.Category)
	}
	if repo.listAllQuery.MinStrength != 0.5 {
		t.Errorf("min_strength=%v", repo.listAllQuery.MinStrength)
	}
	if repo.listAllQuery.Sort != lw.SortLastEvidencedDesc {
		t.Errorf("sort=%v", repo.listAllQuery.Sort)
	}
	if repo.listAllQuery.PageSize != 50 {
		t.Errorf("page_size=%v", repo.listAllQuery.PageSize)
	}
	if len(repo.listAllQuery.Tags) != 1 || repo.listAllQuery.Tags[0] != "flooding" {
		t.Errorf("tags=%v", repo.listAllQuery.Tags)
	}
	// learner scoping comes from the session, not the query string.
	if repo.listAllQuery.LearnerGCID != geGCID || repo.listAllQuery.TenantID != geTenant {
		t.Errorf("scope=%q/%q", repo.listAllQuery.TenantID, repo.listAllQuery.LearnerGCID)
	}
}

// The list endpoint collapses near-duplicate edges sharing a category/topic
// bucket into one representative (the rollup the contract + migration promised).
func TestListMyGrowthEdges_RollsUpByCategory(t *testing.T) {
	mk := func(id, label, cat string, strength float64) lw.LearnerWeakness {
		e := sampleEdge()
		e.ID, e.ConceptKey, e.ConceptLabel, e.Category, e.Strength = id, "ck-"+id, label, cat, strength
		e.TopicID, e.Tags = "", nil
		return e
	}
	repo := &growthEdgeRepoStub{listAllResult: []lw.LearnerWeakness{
		mk("s1", "Sprint Review vs Planning", "scrum", 0.6),
		mk("s2", "Purpose of the Retrospective", "scrum", 0.9), // strongest in the scrum bucket
		mk("k1", "Kanban WIP limits", "kanban", 0.5),
	}}
	srv := geServer(repo)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, geRequest("GET", "/v1/me/growth-edges?page_size=50"))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Items) != 2 {
		t.Fatalf("got %d items; want 2 (scrum bucket collapsed, kanban distinct)", len(resp.Items))
	}
	// strength_desc default → the scrum representative (strongest, 0.9) is first.
	if resp.Items[0]["concept_label"] != "Purpose of the Retrospective" {
		t.Errorf("scrum representative = %v; want the strongest member", resp.Items[0]["concept_label"])
	}
}

// next_page_token walks the ROLLED-UP set so the FE can fetch every edge.
func TestListMyGrowthEdges_PaginatesRolledUp(t *testing.T) {
	var feed []lw.LearnerWeakness
	for i := 0; i < 5; i++ {
		e := sampleEdge()
		e.ID = "01970000-0000-7000-c000-00000000000" + string(rune('a'+i))
		e.ConceptKey, e.ConceptLabel = "ck"+string(rune('a'+i)), "L"+string(rune('a'+i))
		e.Category, e.TopicID, e.Tags = "", "", nil // keyless → each stands alone
		e.Strength = 0.9 - float64(i)/10.0          // 0.9,0.8,0.7,0.6,0.5
		feed = append(feed, e)
	}
	repo := &growthEdgeRepoStub{listAllResult: feed}
	srv := geServer(repo)

	// Page 1 (size 2) → 2 items + a token.
	w1 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w1, geRequest("GET", "/v1/me/growth-edges?page_size=2"))
	var p1 struct {
		Items         []map[string]any `json:"items"`
		NextPageToken string           `json:"next_page_token"`
	}
	_ = json.NewDecoder(w1.Body).Decode(&p1)
	if len(p1.Items) != 2 || p1.NextPageToken == "" {
		t.Fatalf("page1 items=%d next=%q; want 2 + token", len(p1.Items), p1.NextPageToken)
	}

	// Page 2 via the token → next 2 items.
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, geRequest("GET", "/v1/me/growth-edges?page_size=2&page_token="+p1.NextPageToken))
	var p2 struct {
		Items         []map[string]any `json:"items"`
		NextPageToken string           `json:"next_page_token"`
	}
	_ = json.NewDecoder(w2.Body).Decode(&p2)
	if len(p2.Items) != 2 || p2.NextPageToken == "" {
		t.Fatalf("page2 items=%d next=%q; want 2 + token", len(p2.Items), p2.NextPageToken)
	}
	// Pages must be disjoint (no duplicate representatives across pages).
	if p1.Items[0]["id"] == p2.Items[0]["id"] {
		t.Errorf("page1 + page2 overlap: %v", p1.Items[0]["id"])
	}
}

func TestGetMyGrowthEdge_OK(t *testing.T) {
	e := sampleEdge()
	srv := geServer(&growthEdgeRepoStub{getResult: &e})

	r := geRequest("GET", "/v1/me/growth-edges/"+e.ID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["id"] != e.ID {
		t.Errorf("id=%v", got["id"])
	}
	if ids, ok := got["cached_drill_atom_ids"].([]any); !ok || len(ids) != 1 {
		t.Errorf("cached_drill_atom_ids=%v", got["cached_drill_atom_ids"])
	}
	desc, ok := got["descriptor"].(map[string]any)
	if !ok || desc["summary"] != "shaky on causation" {
		t.Errorf("descriptor=%v", got["descriptor"])
	}
}

func TestGetMyGrowthEdge_NotFound(t *testing.T) {
	srv := geServer(&growthEdgeRepoStub{getResult: nil})
	r := geRequest("GET", "/v1/me/growth-edges/01970000-0000-7000-c000-00000000ffff")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestGetMyGrowthEdge_RLSLeakGuard(t *testing.T) {
	e := sampleEdge()
	e.LearnerGCID = "01970000-0000-7000-9000-00000000dead" // belongs to another learner
	srv := geServer(&growthEdgeRepoStub{getResult: &e})
	r := geRequest("GET", "/v1/me/growth-edges/"+e.ID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (cross-learner leak)", w.Code)
	}
}

func TestDeleteMyGrowthEdge_SoftDeletes(t *testing.T) {
	repo := &growthEdgeRepoStub{}
	srv := geServer(repo)
	r := geRequest("DELETE", "/v1/me/growth-edges/01970000-0000-7000-c000-0000000000ab")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 body=%s", w.Code, w.Body.String())
	}
	if !repo.softDeleted || len(repo.deleted) != 1 {
		t.Errorf("soft-delete not called: %+v", repo)
	}
}

func TestGrowthEdges_NilRepoServiceUnavailable(t *testing.T) {
	srv := httpadapter.NewExtServer(nil) // LearnerWeakness unset
	r := geRequest("GET", "/v1/me/growth-edges")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (repo not wired)", w.Code)
	}
}

func TestListMyGrowthEdges_MethodNotAllowed(t *testing.T) {
	srv := geServer(&growthEdgeRepoStub{})
	r := geRequest("POST", "/v1/me/growth-edges")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", w.Code)
	}
}

func TestListMyGrowthEdges_IncludeGrownSortPageToken(t *testing.T) {
	// Also exercises growthEdgeToDTO's nil-slice → [] normalization (this edge
	// has nil Tags + nil CachedDrillAtomIDs).
	repo := &growthEdgeRepoStub{listAllResult: []lw.LearnerWeakness{{
		ID: "01970000-0000-7000-c000-00000000aa01", TenantID: geTenant, LearnerGCID: geGCID,
		ConceptKey: "k", ConceptLabel: "k", Strength: 0.05, Status: lw.StatusGrown,
		Sources: []lw.Source{lw.SourceDerived},
	}}}
	srv := geServer(repo)
	r := geRequest("GET", "/v1/me/growth-edges?include_grown=true&sort=first_seen_desc&page_token=tok-1")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !repo.listAllQuery.IncludeGrown {
		t.Errorf("include_grown not parsed")
	}
	if repo.listAllQuery.Sort != lw.SortFirstSeenDesc {
		t.Errorf("sort=%v", repo.listAllQuery.Sort)
	}
	if repo.listAllQuery.PageToken != "tok-1" {
		t.Errorf("page_token=%q", repo.listAllQuery.PageToken)
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if tags, ok := resp.Items[0]["tags"].([]any); !ok || tags == nil {
		t.Errorf("tags must serialize as [] not null: %v", resp.Items[0]["tags"])
	}
	if resp.Items[0]["status"] != "grown" {
		t.Errorf("status=%v", resp.Items[0]["status"])
	}
}

func TestGrowthEdgeByID_MethodNotAllowed(t *testing.T) {
	srv := geServer(&growthEdgeRepoStub{})
	r := geRequest("PUT", "/v1/me/growth-edges/01970000-0000-7000-c000-0000000000ab")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", w.Code)
	}
}

func TestGrowthEdgeByID_MissingID(t *testing.T) {
	srv := geServer(&growthEdgeRepoStub{})
	r := geRequest("GET", "/v1/me/growth-edges/")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (missing path id)", w.Code)
	}
}

func TestGrowthEdges_MissingContext(t *testing.T) {
	srv := geServer(&growthEdgeRepoStub{})
	r := httptest.NewRequest("GET", "/v1/me/growth-edges", nil) // no headers
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestGrowthEdgeByID_ErrorPaths(t *testing.T) {
	id := "01970000-0000-7000-c000-0000000000ab"
	t.Run("nil repo 503", func(t *testing.T) {
		srv := httpadapter.NewExtServer(nil)
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, geRequest("GET", "/v1/me/growth-edges/"+id))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503", w.Code)
		}
	})
	t.Run("missing context 400", func(t *testing.T) {
		srv := geServer(&growthEdgeRepoStub{})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/v1/me/growth-edges/"+id, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", w.Code)
		}
	})
	t.Run("get error 500", func(t *testing.T) {
		srv := geServer(&growthEdgeRepoStub{getErr: context.DeadlineExceeded})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, geRequest("GET", "/v1/me/growth-edges/"+id))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500", w.Code)
		}
	})
	t.Run("delete error 500", func(t *testing.T) {
		srv := geServer(&growthEdgeRepoStub{deleteErr: context.DeadlineExceeded})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, geRequest("DELETE", "/v1/me/growth-edges/"+id))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500", w.Code)
		}
	})
}
