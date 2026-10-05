package http

// daily_dose_real_atoms_test.go — L4 Fix-1 (CHO-1702): the daily-dose atom
// UNIVERSE comes from the learner's REAL enrolled-course LearningPaths
// (learning_path.Repo.ListByLearner → AtomIndex.Get), replacing the synthetic
// 20-atom NewAtomCatalogue. The 40/30/30 + SM-2 composer is untouched — only
// DailyDoseInput.Seeds (and the feedback atom resolution) swap sources.
//
// Contract (fail-loud directive 2026-06-10):
//   - nil LearningPaths / nil AtomIndex ⇒ synthetic catalogue (wiring-level
//     absence — tests/dev without pg).
//   - a repo ERROR ⇒ 500 (fail loud — never silently serve synthetic atoms).
//   - enrolled-but-unprojected (or not enrolled at all) ⇒ EMPTY dose
//     (honest "no atoms queued"), NOT synthetic.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const syntheticAtomPrefix = "01970000-"

// realDoseAtomIDs are UUIDv7-shaped ids distinct from the synthetic
// catalogue's 01970000- prefix.
var realDoseAtomIDs = []string{
	"019e0000-0000-7000-a000-000000000001",
	"019e0000-0000-7000-a000-000000000002",
	"019e0000-0000-7000-a000-000000000003",
	"019e0000-0000-7000-a000-000000000004",
	"019e0000-0000-7000-a000-000000000005",
	"019e0000-0000-7000-a000-000000000006",
}

// seedRealAtomUniverse wires pg-equivalent in-memory LearningPath + AtomIndex
// repos onto the Server: one enrolled path holding atomIDs, each projected
// into atom_index with a title + topic tag.
func seedRealAtomUniverse(t *testing.T, srv *Server, atomIDs []string) {
	t.Helper()
	paths := repoinmem.NewLearningPathRepo()
	p, err := learning_path.New(testTenant, testGCID, "CSPO Fundamentals", atomIDs)
	if err != nil {
		t.Fatalf("learning_path.New: %v", err)
	}
	if err := paths.Save(context.Background(), p); err != nil {
		t.Fatalf("paths.Save: %v", err)
	}
	idx := repoinmem.NewAtomIndexRepo()
	for i, id := range atomIDs {
		// Published: the real enrolled universe is serveable atoms (CHO-1968).
		saveAtomProjection(t, idx, id, "Real Atom "+string(rune('A'+i)), "product-ownership", atom_index.StatusPublished)
	}
	srv.LearningPaths = paths
	srv.AtomIndex = idx
}

func saveAtomProjection(t *testing.T, idx atom_index.Repo, atomID, title, topic string, status atom_index.Status) {
	t.Helper()
	err := idx.Save(context.Background(), &atom_index.AtomIndex{
		AtomID:   atomID,
		TenantID: testTenant,
		CourseID: "019e0000-0000-7000-b000-000000000001",
		Title:    title,
		AtomType: "mcq",
		// Gradable: a real published MCQ carries an answer key (CHO-1627) so the
		// drill→grow loop can grade it; the focused dose only serves gradable
		// cached drill atoms (CHO-1895).
		CorrectOptionID: "opt-a",
		AnswerCount:     4,
		TopicTags:       []string{topic},
		Status:          status, // CHO-1968: only StatusPublished is dose-playable
		PublishedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("atom_index.Save(%s): %v", atomID, err)
	}
}

func doseEntriesAtomIDs(t *testing.T, srv *Server) []string {
	t.Helper()
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	out := make([]string, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, e.AtomID)
	}
	return out
}

// ----- dose universe -----

func TestDailyDose_ComposesOverRealEnrolledAtoms(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)

	got := doseEntriesAtomIDs(t, srv)
	if len(got) == 0 {
		t.Fatal("dose entries empty, want picks from the real enrolled universe")
	}
	real := make(map[string]bool, len(realDoseAtomIDs))
	for _, id := range realDoseAtomIDs {
		real[id] = true
	}
	for _, id := range got {
		if strings.HasPrefix(id, syntheticAtomPrefix) {
			t.Errorf("dose entry %s is from the synthetic catalogue; want only real enrolled atoms", id)
		}
		if !real[id] {
			t.Errorf("dose entry %s is not in the learner's enrolled universe %v", id, realDoseAtomIDs)
		}
	}
}

func TestDailyDose_FallsBackToSynthetic_WhenLearningPathsNil(t *testing.T) {
	srv := NewServer() // LearningPaths nil by default
	srv.AtomIndex = repoinmem.NewAtomIndexRepo()

	got := doseEntriesAtomIDs(t, srv)
	if len(got) == 0 {
		t.Fatal("dose entries empty, want synthetic fallback when LearningPaths is nil")
	}
	for _, id := range got {
		if !strings.HasPrefix(id, syntheticAtomPrefix) {
			t.Errorf("dose entry %s not synthetic; nil LearningPaths must fall back to the catalogue", id)
		}
	}
}

func TestDailyDose_FallsBackToSynthetic_WhenAtomIndexNil(t *testing.T) {
	srv := NewServer()
	paths := repoinmem.NewLearningPathRepo()
	p, err := learning_path.New(testTenant, testGCID, "CSPO Fundamentals", realDoseAtomIDs)
	if err != nil {
		t.Fatalf("learning_path.New: %v", err)
	}
	if err := paths.Save(context.Background(), p); err != nil {
		t.Fatalf("paths.Save: %v", err)
	}
	srv.LearningPaths = paths
	srv.AtomIndex = nil

	got := doseEntriesAtomIDs(t, srv)
	if len(got) == 0 {
		t.Fatal("dose entries empty, want synthetic fallback when AtomIndex is nil")
	}
	for _, id := range got {
		if !strings.HasPrefix(id, syntheticAtomPrefix) {
			t.Errorf("dose entry %s not synthetic; nil AtomIndex must fall back to the catalogue", id)
		}
	}
}

func TestDailyDose_EmptyDose_WhenEnrolledButUnprojected(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.AtomIndex = repoinmem.NewAtomIndexRepo() // wipe projections: enrolled atoms unprojected

	got := doseEntriesAtomIDs(t, srv)
	if len(got) != 0 {
		t.Errorf("dose entries = %v, want EMPTY (honest) when enrolled atoms are unprojected — never synthetic", got)
	}
}

func TestDailyDose_EmptyDose_WhenNotEnrolled(t *testing.T) {
	srv := NewServer()
	srv.LearningPaths = repoinmem.NewLearningPathRepo() // wired, zero paths
	srv.AtomIndex = repoinmem.NewAtomIndexRepo()

	got := doseEntriesAtomIDs(t, srv)
	if len(got) != 0 {
		t.Errorf("dose entries = %v, want EMPTY (honest) when the learner has no enrolment — never synthetic", got)
	}
}

// ----- CHO-1968 playability filter -----

// seedSingleAtomPath enrols the learner into a 1-atom path and returns the
// shared inmem AtomIndex so the test can project that atom in any status.
func seedSingleAtomPath(t *testing.T, srv *Server, atomID string) *repoinmem.AtomIndexRepo {
	t.Helper()
	paths := repoinmem.NewLearningPathRepo()
	p, err := learning_path.New(testTenant, testGCID, "Course", []string{atomID})
	if err != nil {
		t.Fatalf("learning_path.New: %v", err)
	}
	if err := paths.Save(context.Background(), p); err != nil {
		t.Fatalf("paths.Save: %v", err)
	}
	idx := repoinmem.NewAtomIndexRepo()
	srv.LearningPaths = paths
	srv.AtomIndex = idx
	return idx
}

// TestDailyDose_ExcludesNotPlayableAtom — an enrolled + projected but DRAFT atom
// must never be served. Pre-filter it leaks into the dose (the universe had no
// status gate) — RED.
func TestDailyDose_ExcludesNotPlayableAtom(t *testing.T) {
	srv := NewServer()
	idx := seedSingleAtomPath(t, srv, realDoseAtomIDs[0])
	saveAtomProjection(t, idx, realDoseAtomIDs[0], "Draft Atom", "product-ownership", atom_index.StatusDraft)

	got := doseEntriesAtomIDs(t, srv)
	if len(got) != 0 {
		t.Errorf("dose = %v; want EMPTY — a DRAFT (not-playable) atom must be filtered out", got)
	}
}

// TestDailyDose_IncludesAtomOnlyAfterPublishedFlip — the SAME atom is excluded
// while draft and served only after the AtomPublishedSubscriber flips it.
func TestDailyDose_IncludesAtomOnlyAfterPublishedFlip(t *testing.T) {
	srv := NewServer()
	idx := seedSingleAtomPath(t, srv, realDoseAtomIDs[0])
	saveAtomProjection(t, idx, realDoseAtomIDs[0], "Atom", "product-ownership", atom_index.StatusDraft)

	if got := doseEntriesAtomIDs(t, srv); len(got) != 0 {
		t.Fatalf("dose = %v; want EMPTY before the publish flip", got)
	}

	// Flip to published via the REAL subscriber (sets the answer key from the event).
	pubSub := subscribers.NewAtomPublishedSubscriber(idx)
	env := events.Envelope{
		EventID: "evt-flip-1", IdempotencyKey: "evt-flip-1", TenantID: testTenant,
		OccurredAt: time.Now().UTC(), Traceparent: "00-aaa-bbb-01",
		SourceProject: "chora-content", SourceService: "chora-creation", SchemaVersion: 1,
	}
	if err := pubSub.Handle(tracing.WithTenantID(context.Background(), testTenant), env,
		subscribers.AtomPublishedIndexPayload{
			AtomID: realDoseAtomIDs[0], TenantID: testTenant, AtomType: "mcq",
			CorrectOptionID: "opt-a", AnswerCount: 4,
		}); err != nil {
		t.Fatalf("publish flip: %v", err)
	}

	got := doseEntriesAtomIDs(t, srv)
	found := false
	for _, id := range got {
		if id == realDoseAtomIDs[0] {
			found = true
		}
	}
	if !found {
		t.Errorf("dose = %v; want the atom served AFTER the publish flip", got)
	}
}

// TestDailyDose_PublishedOpenEndedAtom_NotFilteredOut — a published OPEN-ENDED
// atom (no MCQ answer key) is answerable and MUST be served. Guards against an
// IsMCQ-only filter that would wrongly drop open-ended atoms.
func TestDailyDose_PublishedOpenEndedAtom_NotFilteredOut(t *testing.T) {
	srv := NewServer()
	idx := seedSingleAtomPath(t, srv, realDoseAtomIDs[0])
	if err := idx.Save(context.Background(), &atom_index.AtomIndex{
		AtomID:      realDoseAtomIDs[0],
		TenantID:    testTenant,
		Title:       "Reflect on product ownership",
		AtomType:    "essay", // open-ended: no MCQ key
		TopicTags:   []string{"product-ownership"},
		Status:      atom_index.StatusPublished,
		PublishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save OE atom: %v", err)
	}

	got := doseEntriesAtomIDs(t, srv)
	found := false
	for _, id := range got {
		if id == realDoseAtomIDs[0] {
			found = true
		}
	}
	if !found {
		t.Errorf("dose = %v; a PUBLISHED open-ended atom must be served (answerable without an MCQ key)", got)
	}
}

func TestDailyDose_DedupsAtomsAcrossPaths(t *testing.T) {
	srv := NewServer()
	paths := repoinmem.NewLearningPathRepo()
	shared := realDoseAtomIDs[0]
	p1, err := learning_path.New(testTenant, testGCID, "Course One", []string{shared, realDoseAtomIDs[1]})
	if err != nil {
		t.Fatalf("learning_path.New: %v", err)
	}
	p2, err := learning_path.New(testTenant, testGCID, "Course Two", []string{shared, realDoseAtomIDs[2]})
	if err != nil {
		t.Fatalf("learning_path.New: %v", err)
	}
	for _, p := range []*learning_path.LearningPath{p1, p2} {
		if err := paths.Save(context.Background(), p); err != nil {
			t.Fatalf("paths.Save: %v", err)
		}
	}
	idx := repoinmem.NewAtomIndexRepo()
	for i, id := range []string{shared, realDoseAtomIDs[1], realDoseAtomIDs[2]} {
		saveAtomProjection(t, idx, id, "Dedup Atom "+string(rune('A'+i)), "product-ownership", atom_index.StatusPublished)
	}
	srv.LearningPaths = paths
	srv.AtomIndex = idx

	got := doseEntriesAtomIDs(t, srv)
	seen := make(map[string]int)
	for _, id := range got {
		seen[id]++
		if seen[id] > 1 {
			t.Errorf("dose entry %s appears %d times; universe must dedup atoms shared across paths", id, seen[id])
		}
	}
}

func TestDailyDose_FailsLoud_OnListByLearnerError(t *testing.T) {
	srv := NewServer()
	srv.LearningPaths = &errListPathRepo{}
	srv.AtomIndex = repoinmem.NewAtomIndexRepo()

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail loud — a repo ERROR must never silently serve synthetic atoms), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DOSE_UNIVERSE_FAILED") {
		t.Errorf("body = %s, want code DOSE_UNIVERSE_FAILED", w.Body.String())
	}
}

func TestDailyDose_FailsLoud_OnAtomIndexError(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.AtomIndex = &errAtomIndexRepo{}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail loud — an atom_index ERROR is not 'unprojected'), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DOSE_UNIVERSE_FAILED") {
		t.Errorf("body = %s, want code DOSE_UNIVERSE_FAILED", w.Body.String())
	}
}

func TestAtomFeedback_FailsLoud_OnAtomIndexError(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.AtomIndex = &errAtomIndexRepo{}

	w := authedReq(t, srv, http.MethodPost, "/atoms/"+realDoseAtomIDs[0]+"/feedback", feedbackReq{Grade: "i_remember"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail loud — resolve error must not masquerade as 404), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ATOM_RESOLVE_FAILED") {
		t.Errorf("body = %s, want code ATOM_RESOLVE_FAILED", w.Body.String())
	}
}

func TestDailyDose_PassesRLSScopedContextToListByLearner(t *testing.T) {
	srv := NewServer()
	capture := &ctxCapturePathRepo{inner: repoinmem.NewLearningPathRepo()}
	srv.LearningPaths = capture
	srv.AtomIndex = repoinmem.NewAtomIndexRepo()

	_ = doseEntriesAtomIDs(t, srv)
	if capture.gotTenant != testTenant {
		t.Errorf("ListByLearner ctx tenant = %q, want %q (rls.ApplySession reads tracing.TenantIDFromContext)", capture.gotTenant, testTenant)
	}
	if capture.gotGCID != testGCID {
		t.Errorf("ListByLearner ctx gcid = %q, want %q", capture.gotGCID, testGCID)
	}
}

// ----- /atoms/{id}/feedback resolution -----

func TestAtomFeedback_ResolvesRealAtomFromAtomIndex(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)

	real := realDoseAtomIDs[0]
	w := authedReq(t, srv, http.MethodPost, "/atoms/"+real+"/feedback", feedbackReq{Grade: "i_remember"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (real atom must resolve via AtomIndex), body=%s", w.Code, w.Body.String())
	}
	if _, exists, gerr := srv.SM2.GetState(context.Background(), testTenant, testGCID, real); gerr != nil || !exists {
		t.Error("SM-2 state not written for the real atom — Ebbinghaus slot will never fill")
	}
}

func TestAtomFeedback_SyntheticCatalogueFallbackStillWorks(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)

	// A synthetic catalogue atom NOT in atom_index must still resolve via the
	// legacy Lookup fallback (regression guard).
	synthetic := "01970000-0000-7000-a000-000000000000"
	w := authedReq(t, srv, http.MethodPost, "/atoms/"+synthetic+"/feedback", feedbackReq{Grade: "i_remember"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (synthetic fallback), body=%s", w.Code, w.Body.String())
	}
}

func TestAtomFeedback_UnknownAtomStill404s(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)

	w := authedReq(t, srv, http.MethodPost, "/atoms/019effff-0000-7000-a000-00000000dead/feedback", feedbackReq{Grade: "i_remember"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an atom in neither AtomIndex nor catalogue, body=%s", w.Code, w.Body.String())
	}
}

// ----- stubs -----

// errListPathRepo errors on ListByLearner; everything else delegates to the
// embedded interface (nil — those methods must not be called on this path).
type errListPathRepo struct {
	learning_path.Repo
}

func (e *errListPathRepo) ListByLearner(_ context.Context, _, _ string, _ int) ([]*learning_path.LearningPath, error) {
	return nil, errors.New("boom: pg unavailable")
}

// ctxCapturePathRepo records the tracing tenant/gcid present on the
// ListByLearner context (the RLS session contract) then delegates.
type ctxCapturePathRepo struct {
	inner     learning_path.Repo
	gotTenant string
	gotGCID   string
}

func (c *ctxCapturePathRepo) Save(ctx context.Context, p *learning_path.LearningPath) error {
	return c.inner.Save(ctx, p)
}

func (c *ctxCapturePathRepo) Get(ctx context.Context, id string) (*learning_path.LearningPath, error) {
	return c.inner.Get(ctx, id)
}

func (c *ctxCapturePathRepo) GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*learning_path.LearningPath, error) {
	return c.inner.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
}

// ADR-233 provenance lookups (migration 0093) — delegate to the inner repo.
func (c *ctxCapturePathRepo) GetBySourceCollection(ctx context.Context, tenantID, ownerGCID, collectionID string) (*learning_path.LearningPath, error) {
	return c.inner.GetBySourceCollection(ctx, tenantID, ownerGCID, collectionID)
}

func (c *ctxCapturePathRepo) GetByStudyListEventID(ctx context.Context, studyListEventID string) (*learning_path.LearningPath, error) {
	return c.inner.GetByStudyListEventID(ctx, studyListEventID)
}

func (c *ctxCapturePathRepo) ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*learning_path.LearningPath, error) {
	c.gotTenant = tracing.TenantIDFromContext(ctx)
	c.gotGCID = tracing.GCIDFromContext(ctx)
	return c.inner.ListByLearner(ctx, tenantID, gcid, limit)
}

func (c *ctxCapturePathRepo) ListByLearnerWithAtom(ctx context.Context, tenantID, gcid, atomID string) ([]*learning_path.LearningPath, error) {
	return c.inner.ListByLearnerWithAtom(ctx, tenantID, gcid, atomID)
}

func (c *ctxCapturePathRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*learning_path.LearningPath, error) {
	return c.inner.ListByCourse(ctx, tenantID, courseID)
}

// ----- nil-catalogue guards (direct unit coverage) -----

func TestDoseSeeds_NilCatalogueGuards(t *testing.T) {
	srv := &Server{} // no catalogue, no repos at all
	if got := srv.syntheticSeeds(); got != nil {
		t.Errorf("syntheticSeeds with nil catalogue = %v, want nil", got)
	}
	if got, err := srv.doseAtomUniverse(context.Background(), testTenant, testGCID); got != nil || err != nil {
		t.Errorf("doseAtomUniverse with nil repos+catalogue = (%v, %v), want (nil, nil)", got, err)
	}
	if seed, ok, err := srv.resolveAtomSeed(context.Background(), realDoseAtomIDs[0]); ok || err != nil {
		t.Errorf("resolveAtomSeed with nil index+catalogue = (%v, %v, %v), want ok=false, err=nil", seed, ok, err)
	}
}

func TestResolveAtomSeed_IndexErrorFallsBackToCatalogue(t *testing.T) {
	srv := NewServer()
	srv.AtomIndex = repoinmem.NewAtomIndexRepo() // empty: Get errors ErrNotFound
	synthetic := srv.Atoms.Seeds()[0]
	seed, ok, err := srv.resolveAtomSeed(context.Background(), synthetic.AtomID)
	if err != nil {
		t.Fatalf("resolveAtomSeed: %v", err)
	}
	if !ok || seed.AtomID != synthetic.AtomID {
		t.Errorf("resolveAtomSeed(%s) = (%v, %v), want catalogue fallback hit", synthetic.AtomID, seed, ok)
	}
}

// errAtomIndexRepo errors on Get with a NON-NotFound error (pg outage class);
// other methods delegate to the embedded interface (must not be called).
type errAtomIndexRepo struct {
	atom_index.Repo
}

func (e *errAtomIndexRepo) Get(_ context.Context, _ string) (*atom_index.AtomIndex, error) {
	return nil, errors.New("boom: pg connection refused")
}
