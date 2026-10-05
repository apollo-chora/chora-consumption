// daily_dose_ai_candidates_test.go — CHO-1662 session-state-injection RAG.
//
// The recommender crew is sidecar-less and consumption :9090 is STRICT-mTLS, so
// the crew CANNOT dial back for atom candidates. consumption owns atom_index and
// already initiates the Recommend call, so the daily-dose/ai handler pre-fetches
// the candidate atoms in-process (AtomIndex.SearchForLearner) and injects them
// onto the RecommendRequest. The client writes them into the recommender session
// state; the tool reads them back. Soft-fail: an AtomIndex error or empty result
// degrades to no candidates (the tool's stub fallback) — never a 5xx.
package http

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// fakeAtomIndex is an in-test atom_index.Repo capturing the SearchForLearner
// call. Only SearchForLearner is exercised; the other methods satisfy the
// interface but are unused by the daily-dose path.
type fakeAtomIndex struct {
	ret        []*atom_index.AtomIndex
	err        error
	calls      int
	gotTenant  string
	gotTopic   string
	gotLimit   int
	gotCtxTen  string
	gotCtxGCID string
}

func (f *fakeAtomIndex) Save(context.Context, *atom_index.AtomIndex) error { return nil }
func (f *fakeAtomIndex) Get(context.Context, string) (*atom_index.AtomIndex, error) {
	return nil, errors.New("fakeAtomIndex: Get not used by the daily-dose path")
}
func (f *fakeAtomIndex) ListByCourse(context.Context, string, string) ([]*atom_index.AtomIndex, error) {
	return nil, nil
}
func (f *fakeAtomIndex) SearchForLearner(ctx context.Context, tenantID, topicHint string, limit int) ([]*atom_index.AtomIndex, error) {
	f.calls++
	f.gotTenant = tenantID
	f.gotTopic = topicHint
	f.gotLimit = limit
	f.gotCtxTen = tracing.TenantIDFromContext(ctx)
	f.gotCtxGCID = tracing.GCIDFromContext(ctx)
	return f.ret, f.err
}
func (f *fakeAtomIndex) MarkPublished(context.Context, string, atom_index.Status, string, string, int, string) error {
	return nil
}

func threeRealAtoms() []*atom_index.AtomIndex {
	return []*atom_index.AtomIndex{
		{AtomID: "atom-solid", TenantID: testTenant, Title: "SOLID: the S Principle", AtomType: "mcq"},
		{AtomID: "atom-bigo", TenantID: testTenant, Title: "Big-O of Binary Search", AtomType: "mcq"},
		{AtomID: "atom-vcs", TenantID: testTenant, Title: "Version Control Basics", AtomType: "mcq"},
	}
}

func TestDailyDoseAI_InjectsAtomIndexCandidatesIntoRecommendRequest(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	fa := &fakeAtomIndex{ret: threeRealAtoms()}
	srv.AtomIndex = fa
	mustRecordPathTopics(t, srv.ActivePathTopics, testTenant, testGCID, []string{"solid"})
	rec := srv.RecommenderEngine.(*fakeDailyDoseRecommender)

	w := authedReq(t, srv, "GET", "/companion/daily-dose/ai", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	// The RecommendRequest must carry the 3 real atoms as candidates.
	if len(rec.lastReq.Candidates) != 3 {
		t.Fatalf("Recommend Candidates = %d; want 3 from atom_index", len(rec.lastReq.Candidates))
	}
	if rec.lastReq.Candidates[0].AtomID != "atom-solid" || rec.lastReq.Candidates[0].Title != "SOLID: the S Principle" {
		t.Errorf("candidate[0] = %+v; want the SOLID atom", rec.lastReq.Candidates[0])
	}
	// The ctx passed to SearchForLearner MUST carry the tenant (RLS) + gcid.
	if fa.gotCtxTen != testTenant {
		t.Errorf("SearchForLearner ctx tenant = %q; want %q (RLS-bearing)", fa.gotCtxTen, testTenant)
	}
	if fa.gotCtxGCID != testGCID {
		t.Errorf("SearchForLearner ctx gcid = %q; want %q", fa.gotCtxGCID, testGCID)
	}
	if fa.gotTenant != testTenant {
		t.Errorf("SearchForLearner tenant arg = %q; want %q", fa.gotTenant, testTenant)
	}
	// topic_hint is threaded from active path topics (option a relevance signal).
	if fa.gotTopic != "solid" {
		t.Errorf("SearchForLearner topic = %q; want solid (from active path)", fa.gotTopic)
	}
}

func TestDailyDoseAI_SoftFailsWhenAtomIndexErrors(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.AtomIndex = &fakeAtomIndex{err: context.DeadlineExceeded}
	rec := srv.RecommenderEngine.(*fakeDailyDoseRecommender)

	w := authedReq(t, srv, "GET", "/companion/daily-dose/ai", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d (must degrade, never 5xx), body=%s", w.Code, w.Body.String())
	}
	if len(rec.lastReq.Candidates) != 0 {
		t.Errorf("Candidates = %d; want 0 on atom_index error (soft-fail)", len(rec.lastReq.Candidates))
	}
	// The recommender STILL ran (degraded to its own stub fallback path).
	if rec.calls != 1 {
		t.Errorf("recommender calls = %d; want 1 (soft-fail must still attempt picks)", rec.calls)
	}
}

func TestDailyDoseAI_SoftFailsWhenAtomIndexEmpty(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.AtomIndex = &fakeAtomIndex{ret: nil} // no published atoms yet
	rec := srv.RecommenderEngine.(*fakeDailyDoseRecommender)

	w := authedReq(t, srv, "GET", "/companion/daily-dose/ai", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(rec.lastReq.Candidates) != 0 {
		t.Errorf("Candidates = %d; want 0 when atom_index empty", len(rec.lastReq.Candidates))
	}
	if rec.calls != 1 {
		t.Errorf("recommender calls = %d; want 1", rec.calls)
	}
}

func TestDailyDoseAI_NilAtomIndexSkipsPreFetch(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	// srv.AtomIndex left nil — must skip pre-fetch gracefully (no panic, no 5xx).
	rec := srv.RecommenderEngine.(*fakeDailyDoseRecommender)

	w := authedReq(t, srv, "GET", "/companion/daily-dose/ai", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(rec.lastReq.Candidates) != 0 {
		t.Errorf("Candidates = %d; want 0 when AtomIndex nil", len(rec.lastReq.Candidates))
	}
	if rec.calls != 1 {
		t.Errorf("recommender calls = %d; want 1", rec.calls)
	}
}

// Compile-time guard: the fake satisfies the production atom_index.Repo port the
// handler depends on (so the handler field stays the real interface, not a
// test-only narrowing).
var _ atom_index.Repo = (*fakeAtomIndex)(nil)

// ensure the client AtomCandidate shape is what we map into (guards the import
// + the mapping contract).
var _ = clients.AtomCandidate{}
