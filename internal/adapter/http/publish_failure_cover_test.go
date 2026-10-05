// publish_failure_cover_test.go — white-box tests for the EVENT_PUBLISH_FAILED
// branches across the EXT + legacy handlers. A publish failure is a D6
// resilience-relevant path: the handler MUST surface 500 (so the caller / the
// outbox retries) rather than silently drop the side effect.
//
// We inject a failing events.Publisher into the public Publisher field after
// constructing the server (the in-memory default never errors on a valid
// envelope, so this branch was previously unreachable in tests).
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// failingKGEvents implements userknowledgegraph.EventPublisher and errors on
// every publish — models an outbox/Pub/Sub failure for the KG handlers.
type failingKGEvents struct{}

var errKGPublish = errors.New("kg pubsub unavailable")

func (failingKGEvents) PublishMapClusterCreated(context.Context, *userknowledgegraph.MapCluster) error {
	return errKGPublish
}
func (failingKGEvents) PublishMapClusterMerged(context.Context, *userknowledgegraph.MapCluster, *userknowledgegraph.MapCluster, string) error {
	return errKGPublish
}
func (failingKGEvents) PublishMapClusterArchived(context.Context, *userknowledgegraph.MapCluster) error {
	return errKGPublish
}
func (failingKGEvents) PublishExplorationCreated(context.Context, *userknowledgegraph.Exploration) error {
	return errKGPublish
}
func (failingKGEvents) PublishExplorationFocalChanged(context.Context, *userknowledgegraph.Exploration, *userknowledgegraph.TrailHop) error {
	return errKGPublish
}
func (failingKGEvents) PublishExplorationArchived(context.Context, *userknowledgegraph.Exploration) error {
	return errKGPublish
}
func (failingKGEvents) PublishHexagonFogGenerated(context.Context, *userknowledgegraph.HexagonNode, *userknowledgegraph.FogResult) error {
	return errKGPublish
}
func (failingKGEvents) PublishHexagonFogInvalidated(context.Context, *userknowledgegraph.HexagonNode) error {
	return errKGPublish
}
func (failingKGEvents) PublishJunctionDetected(context.Context, *userknowledgegraph.MapCluster, *userknowledgegraph.MapCluster, string, string) error {
	return errKGPublish
}
func (failingKGEvents) PublishJunctionAccepted(context.Context, *userknowledgegraph.Junction) error {
	return errKGPublish
}
func (failingKGEvents) PublishJunctionRejected(context.Context, *userknowledgegraph.Junction) error {
	return errKGPublish
}

// encodeJSONBody marshals v to a reader for httptest requests.
func encodeJSONBody(t *testing.T, v any) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

// firstSeededAtomID returns the first seeded atom id from the server's
// in-memory catalogue (so the feedback lookup succeeds).
func firstSeededAtomID(t *testing.T, srv *Server) string {
	t.Helper()
	seeds := srv.Atoms.Seeds()
	if len(seeds) == 0 {
		t.Skip("no seeded atoms in catalogue")
	}
	return seeds[0].AtomID
}

// failingPublisher implements events.Publisher and always errors — models a
// Pub/Sub publish failure / outbox-write failure.
type failingPublisher struct{}

func (failingPublisher) Publish(string, events.Envelope, map[string]any) error {
	return errors.New("pubsub unavailable")
}

// ---- EXT: POST /sessions start (publish atom_session.started) ----

func TestExt_StartSession_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	s.Publisher = failingPublisher{}
	h := s.Routes()
	w := extReq(t, h, http.MethodPost, "/sessions", map[string]any{"atom_id": tAtom1}, extHeaders())
	if w.Code != http.StatusInternalServerError {
		t.Errorf("start w/ publish failure = %d; want 500 EVENT_PUBLISH_FAILED; body=%s", w.Code, w.Body.String())
	}
}

// ---- EXT: POST /learning-paths/{id}/enrollments (publish enrollment.created) ----

func TestExt_Enroll_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	h := s.Routes()
	// Create a path first (with the working publisher).
	cw := extReq(t, h, http.MethodPost, "/learning-paths",
		map[string]any{"title": "T", "atom_ids": []string{tAtom1}}, extHeaders())
	if cw.Code != http.StatusCreated {
		t.Fatalf("create path = %d", cw.Code)
	}
	var pr map[string]any
	_ = decodeJSON(cw, &pr)
	pathID, _ := pr["path_id"].(string)

	// Now swap in the failing publisher and enroll.
	s.Publisher = failingPublisher{}
	w := extReq(t, h, http.MethodPost, "/learning-paths/"+pathID+"/enrollments", map[string]any{}, extHeaders())
	if w.Code != http.StatusInternalServerError {
		t.Errorf("enroll w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- EXT: submit answer that COMPLETES the session (publish completed) ----

func TestExt_SubmitAnswer_CompletedPublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	// Seed an in-progress session directly so we don't depend on the start path.
	sess, err := atom_attempt.Start(tTenant, tGCID, tAtom1, atom_attempt.SystemClock)
	if err != nil {
		t.Fatal(err)
	}
	s.Sessions.Save(context.Background(), sess)
	s.Publisher = failingPublisher{}
	_ = ctx

	h := s.Routes()
	// A correct answer completes the session → publish fires → 500.
	w := extReq(t, h, http.MethodPatch, "/sessions/"+sess.SessionID+"/answer",
		map[string]any{"answer": "x", "correct": true}, extHeaders())
	if w.Code != http.StatusInternalServerError {
		t.Errorf("submit-complete w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- EXT: abandon session (publish atom_session.abandoned) ----

func TestExt_AbandonSession_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	sess, err := atom_attempt.Start(tTenant, tGCID, tAtom1, atom_attempt.SystemClock)
	if err != nil {
		t.Fatal(err)
	}
	s.Sessions.Save(context.Background(), sess)
	s.Publisher = failingPublisher{}

	h := s.Routes()
	w := extReq(t, h, http.MethodPost, "/sessions/"+sess.SessionID+":abandon", nil, extHeaders())
	if w.Code != http.StatusInternalServerError {
		t.Errorf("abandon w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- legacy: /atoms/{id}/feedback (publish atom_session.completed) ----

func TestRouter_AtomFeedback_PublishFailure_500(t *testing.T) {
	srv := NewServer()
	srv.Publisher = failingPublisher{}
	h := srv.Routes()

	// Use a seeded atom from the catalogue so the lookup succeeds.
	atomID := firstSeededAtomID(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/atoms/"+atomID+"/feedback", encodeJSONBody(t, map[string]string{"grade": "i_remember"}))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("feedback w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- legacy: POST /v1/me/companions (publish companion.created) ----

func TestRouter_CreateCompanionInstance_PublishFailure_500(t *testing.T) {
	srv := NewServer()
	srv.Publisher = failingPublisher{}
	h := srv.Routes()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/companions",
		encodeJSONBody(t, map[string]string{"name": "Newton", "specialization": "math"}))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("create instance w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- KG: createKGCluster publish failure ----

func TestKG_CreateCluster_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters",
		map[string]string{"seed_topic": "agile", "seed_atom_id": kgAtomA})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("create cluster w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- KG: archiveKGCluster (DELETE) publish failure ----

func TestKG_ArchiveCluster_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = s.KGClusters.Save(ctx, c)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodDelete, "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("archive cluster w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- KG: rejectKGJunction publish failure ----

func TestKG_RejectJunction_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = s.KGJunctions.Save(ctx, j)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/reject", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("reject junction w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- canvas: archive publish failure ----

func TestCanvas_Archive_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = s.KGClusters.Save(ctx, c)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/archive", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("canvas archive w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- KG: acceptKGJunction publish failure (after merge) ----

func TestKG_AcceptJunction_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = s.KGClusters.Save(ctx, cA)
	_ = s.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = s.KGJunctions.Save(ctx, j)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/accept",
		map[string]string{"via_atom_id": kgAtomC})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("accept junction w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- KG: promoteFocal publish failure ----

func TestKG_PromoteFocal_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = s.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = s.KGExplorations.Save(ctx, exp)
	neighbors := kgFakeNeighbors(kgAtomA)
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, neighbors, "r", "m")
	_ = s.KGHexagons.Upsert(ctx, hex)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	target := neighbors[0].AtomID
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/promote/"+target, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("promote w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- canvas: junction decide=accept publish failure ----

func TestCanvas_JunctionDecideAccept_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = s.KGClusters.Save(ctx, cA)
	_ = s.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = s.KGJunctions.Save(ctx, j)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "accept"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("canvas accept w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- canvas: focal:move publish failure ----

func TestCanvas_FocalMove_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	cid, eid, c, exp := kgCanvasSeed(t, s, kgCanvasSixNeighbors())
	_ = c
	_ = exp
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{"targetAtomId": kgAtomB})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("canvas focal:move w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ---- canvas: junction decide=decline publish failure ----

func TestCanvas_JunctionDecideDecline_PublishFailure_500(t *testing.T) {
	s := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = s.KGClusters.Save(ctx, cA)
	_ = s.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = s.KGJunctions.Save(ctx, j)
	s.KGEvents = failingKGEvents{}
	h := s.Routes()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "decline"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("canvas decline w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}
