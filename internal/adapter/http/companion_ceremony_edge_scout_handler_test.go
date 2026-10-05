// companion_ceremony_edge_scout_handler_test.go — CHO-2040 (CR §8 R7-3): the
// ceremony edge-scout PROPOSE runner.
//
//	POST /v1/me/companions/{id}/ceremony/edge-scout   {"goal_id":"<uuid>"}
//
// Composed runner on the CHO-2013 invoke machinery — NO grant/equip/stage gate
// (the ceremony IS the acquisition moment). Sources: learner weaknesses ∪
// goal-adjacent WS-4 fog suggestions ∪ Delivery graded-assessment overall
// comments; ONE fenced extraction turn (gateway = sole debiter, ADR-177);
// virgin fallback = explore-only, no LLM, no charge. Fail-loud everywhere.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// ----------------------------------------------------------------------------
// Fakes for the edge-scout read ports. The engine / mana / embedder / concept
// fakes are reused from the chat + invoke test files in this package.
// ----------------------------------------------------------------------------

// esGoalReader fakes the Goal read port (GetByID only; read-only contract).
type esGoalReader struct {
	goals map[string]*goal.Goal
	err   error
}

func (f *esGoalReader) GetByID(_ context.Context, _, _, goalID string) (*goal.Goal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.goals[goalID], nil // (nil, nil) live-miss per the repo contract
}

// esFogReader fakes the WS-4 pending-suggestion read (the fog output store).
type esFogReader struct {
	pending    []*conceptgraph.Suggestion
	err        error
	focalCalls []string
	listCalls  int
}

func (f *esFogReader) ListPending(_ context.Context, _, _ string) ([]*conceptgraph.Suggestion, error) {
	f.listCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.pending, nil
}

func (f *esFogReader) ListPendingForFocal(_ context.Context, _, _, focalConceptID string) ([]*conceptgraph.Suggestion, error) {
	f.focalCalls = append(f.focalCalls, focalConceptID)
	if f.err != nil {
		return nil, f.err
	}
	return f.pending, nil
}

// esCommentsReader fakes the Delivery.ListLearnerGradedSubmissions seam.
type esCommentsReader struct {
	comments []edgescout.CommentSignal
	err      error
	gotLimit int
}

func (f *esCommentsReader) ListLearnerGradedComments(_ context.Context, _, _ string, limit int) ([]edgescout.CommentSignal, error) {
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.comments, nil
}

// esRunStore fakes the R8-1 first-run ledger (edgescout.RunStore). Keyed on
// the full (tenant, goal, learner) triple; error injection per method.
type esRunStore struct {
	runs      map[string]bool
	hasErr    error
	recordErr error
	recorded  int
}

func newESRunStore() *esRunStore { return &esRunStore{runs: map[string]bool{}} }

func (f *esRunStore) key(tenantID, goalID, gcid string) string {
	return tenantID + "|" + goalID + "|" + gcid
}

func (f *esRunStore) HasRun(_ context.Context, tenantID, goalID, gcid string) (bool, error) {
	if f.hasErr != nil {
		return false, f.hasErr
	}
	return f.runs[f.key(tenantID, goalID, gcid)], nil
}

func (f *esRunStore) RecordRun(_ context.Context, tenantID, goalID, gcid string, _ time.Time) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded++
	f.runs[f.key(tenantID, goalID, gcid)] = true
	return nil
}

// esLWStub is a settable lw.Repository double (local per-suite copy, mirrors
// doseLWStub — local recording keeps the suites independent).
type esLWStub struct {
	items   []lw.LearnerWeakness
	listErr error
	gotList lw.ListQuery
}

func (s *esLWStub) Upsert(context.Context, lw.UpsertInput) (lw.UpsertResult, error) {
	return lw.UpsertResult{}, nil
}
func (s *esLWStub) List(_ context.Context, q lw.ListQuery) (lw.ListResult, error) {
	s.gotList = q
	if s.listErr != nil {
		return lw.ListResult{}, s.listErr
	}
	return lw.ListResult{Items: s.items}, nil
}
func (s *esLWStub) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return s.items, s.listErr
}
func (s *esLWStub) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (s *esLWStub) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (s *esLWStub) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *esLWStub) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *esLWStub) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

// ----------------------------------------------------------------------------
// Seeding
// ----------------------------------------------------------------------------

const (
	esGoalID = "01970000-aaaa-7000-8000-00000000e5c0"
	esRootID = "01970000-bbbb-7000-8000-00000000e5c1"
)

// esReply is the canonical strict-JSON extraction reply the fake engine emits.
const esReply = `{"candidates":[
	{"title":"Recursion base cases","intent":"remediate","source":"weakness","rationale":"stop conditions shaky"},
	{"title":"Tail calls","intent":"explore","source":"fog","rationale":"one hop from recursion"},
	{"title":"Unit conversions","intent":"remediate","source":"comment","rationale":"grader flagged repeatedly"}
]}`

func esTokenFrames(text string) []clients.ChatStreamFrame {
	data, _ := json.Marshal(map[string]string{"text": text})
	return []clients.ChatStreamFrame{{Type: clients.ChatFrameToken, Data: data}}
}

// seedEdgeScoutServer wires a Server with an owned companion, an owned goal
// (root concept + concept set), one weakness, one pending fog suggestion, one
// graded comment, a funded wallet, a canned strict-JSON engine, an embedder,
// and an EMPTY R8-1 first-run ledger (the seeded learner has never scouted
// this goal; paid-path tests pre-record a run via esRecordRun). Returns the
// server, the companion id, and the fakes for asserts.
func seedEdgeScoutServer(t *testing.T, engine *fakeChatEngine) (*Server, string, *esFogReader, *esCommentsReader, *fakeEmbedder) {
	t.Helper()
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	srv.CeremonyRuns = newESRunStore()
	srv.CompanionEngineResource = "gke://companion-head-test"
	srv.CompanionEngine = engine
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 1000}}

	root := esRootID
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{
		esGoalID: {
			GoalID: esGoalID, TenantID: testTenant, LearnerGCID: testGCID,
			Kind: goal.KindCuriosity, Status: goal.StatusActive,
			RootConceptID: &root,
			ConceptSet:    []string{"hydrology", "drainage design"},
		},
	}}
	srv.ConceptNodes = &fakeConceptReader{nodes: map[string]*conceptgraph.ConceptNode{
		esRootID: {ConceptID: esRootID, Title: "Flood risk engineering"},
	}}
	// #19: the full-subtree fog fence needs the learner's live map + edges. The
	// default seed is a single-node map (the root) with no children — individual
	// tests override to model won children / sibling goals.
	srv.KgExploreMap = &fakeKgExploreMap{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: esRootID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Flood risk engineering"},
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{}
	srv.LearnerWeakness = &esLWStub{items: []lw.LearnerWeakness{{
		ID: "w1", TenantID: testTenant, LearnerGCID: testGCID,
		ConceptLabel: "Recursion base cases", Strength: 0.9,
		Embedding:          []float32{1, 0},
		CachedDrillAtomIDs: []string{"01970000-cccc-7000-8000-00000000a001"},
		Descriptor:         lw.Descriptor{Summary: "confuses the stop condition"},
		Status:             lw.StatusActive,
	}}}
	fog := &esFogReader{pending: []*conceptgraph.Suggestion{{
		SuggestionID: "s1", TenantID: testTenant, LearnerGCID: testGCID,
		Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
		Title: "Tail calls", Rationale: "one hop from recursion",
		AtomRefs: []string{"01970000-cccc-7000-8000-00000000a002"},
	}}}
	srv.FogSuggestions = fog
	comments := &esCommentsReader{comments: []edgescout.CommentSignal{{
		Excerpt: "Strong overall, but unit conversions tripped every applied question.",
		Outcome: "FAILED",
	}}}
	srv.GradedComments = comments
	emb := &fakeEmbedder{vec: []float32{1, 0}}
	srv.Embedder = emb
	return srv, id, fog, comments, emb
}

func esPath(companionID string) string {
	return "/v1/me/companions/" + companionID + "/ceremony/edge-scout"
}

func esBody() map[string]any { return map[string]any{"goal_id": esGoalID} }

// esRecordRun burns the R8-1 freebie for the seeded (tenant, goal, learner)
// triple so the test exercises the PAID re-run path.
func esRecordRun(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.CeremonyRuns.RecordRun(context.Background(), testTenant, esGoalID, testGCID, time.Now().UTC()); err != nil {
		t.Fatalf("esRecordRun: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Auth / routing / validation gates
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_RequiresAuth(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{frames: esTokenFrames(esReply)})
	w := plainReq(t, srv, http.MethodPost, esPath(id), nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyEdgeScout_MethodNotAllowed(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodGet, esPath(id), nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyEdgeScout_GoalRefValidation(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	for name, body := range map[string]map[string]any{
		"missing":   {},
		"blank":     {"goal_id": "   "},
		"malformed": {"goal_id": "not-a-uuid"},
	} {
		w := authedReq(t, srv, http.MethodPost, esPath(id), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
			continue
		}
		if code := errCode(t, w.Body.Bytes()); code != "INVALID_GOAL_REF" {
			t.Errorf("%s: code = %q, want INVALID_GOAL_REF", name, code)
		}
	}
}

func TestCeremonyEdgeScout_NotWired(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	srv.GradedComments = nil // any missing composed-source port ⇒ refuse loudly
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_NOT_WIRED" {
		t.Errorf("code = %q, want EDGE_SCOUT_NOT_WIRED", code)
	}
}

func TestCeremonyEdgeScout_UnknownCompanion(t *testing.T) {
	srv, _, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodPost,
		esPath("01970000-dead-7000-8000-000000000009"), esBody())
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "COMPANION_NOT_FOUND" {
		t.Errorf("code = %q, want COMPANION_NOT_FOUND", code)
	}
}

func TestCeremonyEdgeScout_GoalNotFoundOrForeign(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	// Unknown goal id → 404.
	w := authedReq(t, srv, http.MethodPost, esPath(id),
		map[string]any{"goal_id": "01970000-aaaa-7000-8000-00000000dead"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown: status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "GOAL_NOT_FOUND" {
		t.Errorf("unknown: code = %q, want GOAL_NOT_FOUND", code)
	}
	// A goal owned by another learner must 404 (no cross-owner disclosure).
	foreign := *srv.Goals.(*esGoalReader).goals[esGoalID]
	foreign.LearnerGCID = "01970000-0000-7000-9000-00000000ffff"
	srv.Goals.(*esGoalReader).goals[esGoalID] = &foreign
	w = authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign: status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Mana gate (ADR-177 — gateway is the sole debiter; runner pre-checks only)
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_InsufficientMana(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	esRecordRun(t, srv) // R8-1: the freebie is burnt — this is the PAID re-run
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 1}}
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", w.Code, w.Body.String())
	}
	if engine.calls != 0 {
		t.Errorf("engine must not run on an unaffordable ceremony (calls=%d)", engine.calls)
	}
}

// ----------------------------------------------------------------------------
// Happy path — compose, rank, ONE fenced turn, strict parse, reconcile
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_HappyPath(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, fog, comments, emb := seedEdgeScoutServer(t, engine)
	esRecordRun(t, srv) // R8-1: freebie burnt — this asserts the PAID re-run path

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title     string   `json:"title"`
			Intent    string   `json:"intent"`
			Source    string   `json:"source"`
			AtomRefs  []string `json:"atom_refs"`
			Rationale string   `json:"rationale"`
		} `json:"candidates"`
		Fallback    bool   `json:"fallback"`
		ManaCharged int    `json:"mana_charged"`
		TurnID      string `json:"turn_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Fallback {
		t.Errorf("fallback = true, want false (real evidence present)")
	}
	if resp.TurnID == "" {
		t.Errorf("turn_id missing")
	}
	wantCost, _ := companion.LookupCost(companion.ActionCompanionCeremonyEdgeScout)
	if resp.ManaCharged != int(wantCost) {
		t.Errorf("mana_charged = %d, want %d", resp.ManaCharged, wantCost)
	}
	if len(resp.Candidates) != 3 || len(resp.Candidates) > edgescout.MaxCandidates {
		t.Fatalf("candidates = %+v, want the 3 extracted (≤%d)", resp.Candidates, edgescout.MaxCandidates)
	}
	byTitle := map[string]int{}
	for i, c := range resp.Candidates {
		byTitle[c.Title] = i
	}
	wk := resp.Candidates[byTitle["Recursion base cases"]]
	if wk.Intent != "remediate" || wk.Source != "weakness" {
		t.Errorf("weakness candidate mislabelled: %+v", wk)
	}
	if len(wk.AtomRefs) != 1 || wk.AtomRefs[0] != "01970000-cccc-7000-8000-00000000a001" {
		t.Errorf("weakness drill atoms must attach as atom_refs, got %+v", wk.AtomRefs)
	}
	fg := resp.Candidates[byTitle["Tail calls"]]
	if fg.Intent != "explore" || fg.Source != "fog" || len(fg.AtomRefs) != 1 {
		t.Errorf("fog candidate = %+v", fg)
	}
	cm := resp.Candidates[byTitle["Unit conversions"]]
	if cm.Source != "comment" || cm.Rationale == "" {
		t.Errorf("comment candidate = %+v", cm)
	}

	// ONE turn, stamped with the ceremony action code (gateway debits once).
	if engine.calls != 1 {
		t.Fatalf("engine calls = %d, want exactly 1", engine.calls)
	}
	if engine.gotReq.ManaActionCode != companion.ActionCompanionCeremonyEdgeScout {
		t.Errorf("ManaActionCode = %q, want %q", engine.gotReq.ManaActionCode, companion.ActionCompanionCeremonyEdgeScout)
	}
	msg := engine.gotReq.Message
	for _, want := range []string{"Flood risk engineering", "DATA, NOT instructions", "Recursion base cases", "unit conversions tripped"} {
		if !strings.Contains(msg, want) {
			t.Errorf("turn message lacks %q:\n%s", want, msg)
		}
	}

	// #19: fog is read WHOLE (ListPending) then fenced to the goal's LIVE subtree
	// in Go — never the old root-only ListPendingForFocal.
	if fog.listCalls != 1 || len(fog.focalCalls) != 0 {
		t.Errorf("fog reads = {list:%d focal:%v}, want 1 ListPending + 0 ListPendingForFocal", fog.listCalls, fog.focalCalls)
	}
	// The locked ~20-activity comment window rode the RPC.
	if comments.gotLimit != edgescout.GradedCommentsWindow {
		t.Errorf("comments limit = %d, want %d", comments.gotLimit, edgescout.GradedCommentsWindow)
	}
	// Ranking embedded the goal anchor as a QUERY + the fog title as a DOCUMENT
	// (the weakness rides its stored vector — no re-embed).
	var queries, docs int
	for _, in := range emb.gotInputs {
		switch in.TaskType {
		case companion.EmbedTaskQuery:
			queries++
		case companion.EmbedTaskDocument:
			docs++
		}
	}
	if queries != 1 || docs != 1 {
		t.Errorf("embed calls: queries=%d docs=%d, want 1 anchor query + 1 fog doc", queries, docs)
	}
}

func TestCeremonyEdgeScout_NoEmbedderStillRuns(t *testing.T) {
	// Embedder unwired at boot ⇒ the documented deviation path: source-order
	// pool + the turn's explicit top-8 selection. Never a 5xx.
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.Embedder = nil
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyEdgeScout_EmbedFailureFailsLoud(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.Embedder = &fakeEmbedder{err: errors.New("vertex quota")}
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_EMBED_FAILED" {
		t.Errorf("code = %q, want EDGE_SCOUT_EMBED_FAILED", code)
	}
	if engine.calls != 0 {
		t.Errorf("no turn may run after a ranking failure (calls=%d)", engine.calls)
	}
}

// ----------------------------------------------------------------------------
// Fail-loud reply handling — malformed extraction is a 502, never a fallback
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_MalformedReply(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames("Sure! Here are some ideas: recursion, graphs")}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_BAD_REPLY" {
		t.Errorf("code = %q, want EDGE_SCOUT_BAD_REPLY", code)
	}
}

func TestCeremonyEdgeScout_EngineErrorFrame(t *testing.T) {
	data, _ := json.Marshal(map[string]string{"message": "armor blocked"})
	engine := &fakeChatEngine{frames: []clients.ChatStreamFrame{{Type: clients.ChatFrameError, Data: data}}}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_ENGINE_ERROR" {
		t.Errorf("code = %q, want EDGE_SCOUT_ENGINE_ERROR", code)
	}
}

func TestCeremonyEdgeScout_EmptyReply(t *testing.T) {
	engine := &fakeChatEngine{} // zero frames → empty reply
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_EMPTY_REPLY" {
		t.Errorf("code = %q, want EDGE_SCOUT_EMPTY_REPLY", code)
	}
}

func TestCeremonyEdgeScout_EngineNotConfigured(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	srv.CompanionEngine = nil
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "ENGINE_NOT_CONFIGURED" {
		t.Errorf("code = %q, want ENGINE_NOT_CONFIGURED", code)
	}
}

// ----------------------------------------------------------------------------
// Source-read failures surface loudly (never swallowed into a thin panel)
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_SourceReadFailures(t *testing.T) {
	t.Run("weakness", func(t *testing.T) {
		srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
		srv.LearnerWeakness = &esLWStub{listErr: errors.New("pg down")}
		w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
		}
		if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_WEAKNESS_READ_FAILED" {
			t.Errorf("code = %q", code)
		}
	})
	t.Run("fog", func(t *testing.T) {
		srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
		srv.FogSuggestions = &esFogReader{err: errors.New("pg down")}
		w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
		}
		if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_FOG_READ_FAILED" {
			t.Errorf("code = %q", code)
		}
	})
	t.Run("comments", func(t *testing.T) {
		srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
		srv.GradedComments = &esCommentsReader{err: errors.New("delivery unavailable")}
		w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (upstream RPC), body=%s", w.Code, w.Body.String())
		}
		if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_COMMENTS_READ_FAILED" {
			t.Errorf("code = %q", code)
		}
	})
}

// ----------------------------------------------------------------------------
// Virgin fallback — no LLM, no charge, never a blank panel
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_VirginFallback_FogSeeded(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.LearnerWeakness = &esLWStub{}                                                     // zero weaknesses
	srv.GradedComments = &esCommentsReader{}                                              // zero comments
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 0}} // broke is fine — no charge
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title  string `json:"title"`
			Intent string `json:"intent"`
			Source string `json:"source"`
		} `json:"candidates"`
		Fallback    bool   `json:"fallback"`
		ManaCharged int    `json:"mana_charged"`
		TurnID      string `json:"turn_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Fallback || resp.ManaCharged != 0 || resp.TurnID != "" {
		t.Errorf("fallback envelope wrong: fallback=%v charged=%d turn=%q", resp.Fallback, resp.ManaCharged, resp.TurnID)
	}
	if engine.calls != 0 {
		t.Errorf("virgin fallback must SKIP the engine turn (calls=%d)", engine.calls)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].Title != "Tail calls" ||
		resp.Candidates[0].Intent != "explore" || resp.Candidates[0].Source != "fog" {
		t.Errorf("candidates = %+v, want the explore-only fog seed", resp.Candidates)
	}
}

func TestCeremonyEdgeScout_VirginFallback_ConceptSetSeeded(t *testing.T) {
	engine := &fakeChatEngine{}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.LearnerWeakness = &esLWStub{}
	srv.GradedComments = &esCommentsReader{}
	srv.FogSuggestions = &esFogReader{} // map fog empty too
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title  string `json:"title"`
			Intent string `json:"intent"`
			Source string `json:"source"`
		} `json:"candidates"`
		Fallback bool `json:"fallback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Fallback {
		t.Errorf("fallback = false, want true")
	}
	if len(resp.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want the 2 concept-set seeds", resp.Candidates)
	}
	for _, c := range resp.Candidates {
		if c.Intent != "explore" || c.Source != "goal" {
			t.Errorf("concept-set seed = %+v, want explore/goal", c)
		}
	}
	if engine.calls != 0 {
		t.Errorf("virgin fallback must never turn the engine (calls=%d)", engine.calls)
	}
}

// ----------------------------------------------------------------------------
// R8-1 — first run per goal FREE, re-runs 25 (owner ruling, CHO-2040 Unit B)
// ----------------------------------------------------------------------------

func TestCeremonyEdgeScout_FirstRunFree(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	// A BROKE wallet must not block the first run — the balance pre-check is
	// skipped (zero-cost code); the gateway meters the turn and debits 0.
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 0}}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (first run is free), body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Fallback    bool `json:"fallback"`
		ManaCharged int  `json:"mana_charged"`
		FirstRun    bool `json:"first_run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.FirstRun {
		t.Errorf("first_run = false, want true")
	}
	if resp.ManaCharged != 0 {
		t.Errorf("mana_charged = %d, want 0 (owner-ruled free first run)", resp.ManaCharged)
	}
	if resp.Fallback {
		t.Errorf("fallback = true, want false (real evidence present)")
	}
	// The turn MUST still be metered — the zero-cost code rides the engine
	// request so the gateway sees the turn (IMDA D3: never un-metered).
	if engine.calls != 1 {
		t.Fatalf("engine calls = %d, want 1", engine.calls)
	}
	if engine.gotReq.ManaActionCode != companion.ActionCompanionCeremonyEdgeScoutFirst {
		t.Errorf("ManaActionCode = %q, want %q", engine.gotReq.ManaActionCode, companion.ActionCompanionCeremonyEdgeScoutFirst)
	}
	// The successful first run burns the freebie.
	store := srv.CeremonyRuns.(*esRunStore)
	if store.recorded != 1 {
		t.Errorf("run rows recorded = %d, want 1", store.recorded)
	}
	if has, _ := store.HasRun(context.Background(), testTenant, esGoalID, testGCID); !has {
		t.Errorf("run row missing after a successful first run")
	}
}

func TestCeremonyEdgeScout_RerunStampsPaidCode(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	esRecordRun(t, srv)

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ManaCharged int  `json:"mana_charged"`
		FirstRun    bool `json:"first_run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FirstRun {
		t.Errorf("first_run = true on a re-run, want false")
	}
	if resp.ManaCharged != 25 {
		t.Errorf("mana_charged = %d, want the R8-1 locked 25", resp.ManaCharged)
	}
	if engine.gotReq.ManaActionCode != companion.ActionCompanionCeremonyEdgeScout {
		t.Errorf("ManaActionCode = %q, want the paid %q", engine.gotReq.ManaActionCode, companion.ActionCompanionCeremonyEdgeScout)
	}
	// A re-run must NOT mint a second ledger row (RecordRun is first-run-only).
	if store := srv.CeremonyRuns.(*esRunStore); store.recorded != 1 {
		t.Errorf("run rows recorded = %d, want the pre-seeded 1 only", store.recorded)
	}
}

func TestCeremonyEdgeScout_VirginFallbackKeepsFreebie(t *testing.T) {
	// The no-turn fallback (zero weaknesses + zero comments) must NOT consume
	// the first-run freebie: no LLM turn ran, so nothing is recorded — the
	// learner's first REAL scout is still free.
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.LearnerWeakness = &esLWStub{}
	srv.GradedComments = &esCommentsReader{}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Fallback bool `json:"fallback"`
		FirstRun bool `json:"first_run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Fallback {
		t.Fatalf("fallback = false, want true")
	}
	if !resp.FirstRun {
		t.Errorf("first_run = false, want true (informational: the freebie is intact)")
	}
	store := srv.CeremonyRuns.(*esRunStore)
	if store.recorded != 0 {
		t.Errorf("run rows recorded = %d, want 0 (fallback never burns the freebie)", store.recorded)
	}
}

func TestCeremonyEdgeScout_RunLedgerReadFailureIsLoud(t *testing.T) {
	// If first-run-ness cannot be determined, the runner refuses — guessing
	// free = un-metered abuse; guessing paid = silently overriding the ruling.
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.CeremonyRuns = &esRunStore{hasErr: errors.New("pg down")}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_RUN_READ_FAILED" {
		t.Errorf("code = %q, want EDGE_SCOUT_RUN_READ_FAILED", code)
	}
	if engine.calls != 0 {
		t.Errorf("no turn may run when the ledger is unreadable (calls=%d)", engine.calls)
	}
}

func TestCeremonyEdgeScout_RunRecordFailureIsLoud(t *testing.T) {
	// The row write is PART of the successful first-run flow: a failure is a
	// loud 500 (never a silent repeat-freebie).
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	srv.CeremonyRuns = &esRunStore{runs: map[string]bool{}, recordErr: errors.New("pg down")}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_RUN_RECORD_FAILED" {
		t.Errorf("code = %q, want EDGE_SCOUT_RUN_RECORD_FAILED", code)
	}
}

func TestCeremonyEdgeScout_RunsNotWired(t *testing.T) {
	// A nil ledger port is a wiring hole: silently treating every run as paid
	// (or free) would misprice the ruling — refuse per-request instead.
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	srv.CeremonyRuns = nil
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGE_SCOUT_NOT_WIRED" {
		t.Errorf("code = %q, want EDGE_SCOUT_NOT_WIRED", code)
	}
}

// ----------------------------------------------------------------------------
// #19 — full-subtree fog fence: a WON CHILD's focal fog belongs to this goal
// ----------------------------------------------------------------------------

// TestCeremonyEdgeScout_WonChildFocalFog_Surfaces pins bug #19: fog anchored on
// a WON CHILD (a descendant focal, not the goal root) belongs to THIS goal's map
// and must reach the ceremony pool. The old root-anchored fence
// (ListPendingForFocal(root) + focal==root) dropped it in two places; the
// full-subtree fence (SubtreeConceptIDs over KgExploreMap+KgExploreEdges — the same
// walk the read-side scopeSuggestionsToGoal uses) admits it, while STILL fencing
// out a SIBLING goal's focal fog. Won-ness needs no re-check here: a child-focal
// suggestion can only exist because that node was won (refuseUnwonGoalReveal
// gates generation), so subtree membership is the whole test.
func TestCeremonyEdgeScout_WonChildFocalFog_Surfaces(t *testing.T) {
	const (
		wonChildID = "01970000-bbbb-7000-8000-00000000e5c2" // a WON child under esRootID
		siblingID  = "01970000-bbbb-7000-8000-00000000e5c9" // a node in ANOTHER goal's map
	)
	// The engine copies pool-grounded titles back. Reconcile is pool-truth-wins:
	// a fog title NOT in the fenced pool 502s the whole reply — so the reply names
	// only the WON-CHILD fog; the sibling's exclusion is asserted on the prompt
	// (BuildPrompt renders every pool candidate).
	reply := `{"candidates":[
		{"title":"Recursion base cases","intent":"remediate","source":"weakness","rationale":"stop conditions shaky"},
		{"title":"French drains","intent":"explore","source":"fog","rationale":"beside your drainage work"}
	]}`
	engine := &fakeChatEngine{frames: esTokenFrames(reply)}
	srv, id, fog, _, _ := seedEdgeScoutServer(t, engine)
	esRecordRun(t, srv) // paid path; the fence is orthogonal to pricing

	// A 2-node goal map: root ─hierarchy→ wonChild (both live). The sibling node
	// is NOT under this root, so it is not in the subtree.
	srv.KgExploreMap = &fakeKgExploreMap{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: esRootID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Flood risk engineering"},
		{ConceptID: wonChildID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Drainage design"},
		{ConceptID: siblingID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Botany"},
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{edges: []*conceptgraph.Edge{
		scoutHierEdge(esRootID, wonChildID),
	}}
	// Two pending fogs: one focal-anchored on the WON CHILD (in-subtree), one on a
	// SIBLING goal's node (out-of-subtree). ListPending returns both flat.
	fog.pending = []*conceptgraph.Suggestion{
		{
			SuggestionID: "s-child", TenantID: testTenant, LearnerGCID: testGCID,
			Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
			Title: "French drains", FocalConceptID: wonChildID,
			AtomRefs: []string{"01970000-cccc-7000-8000-00000000a003"},
		},
		{
			SuggestionID: "s-sibling", TenantID: testTenant, LearnerGCID: testGCID,
			Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
			Title: "Photosynthesis", FocalConceptID: siblingID,
		},
	}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title  string `json:"title"`
			Source string `json:"source"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	titles := map[string]bool{}
	for _, c := range resp.Candidates {
		titles[c.Title] = true
	}
	// RED today: the root-anchored fence dropped the won-child fog from the pool,
	// so pool-truth-wins reconcile 502'd the reply that named it.
	if !titles["French drains"] {
		t.Errorf("won-child-focal fog 'French drains' missing from candidates %+v — the fence dropped it", resp.Candidates)
	}
	// The engine saw a pool that ADMITTED the won-child fog but fenced OUT the
	// sibling goal's focal fog (BuildPrompt renders every pool candidate).
	if msg := engine.gotReq.Message; !strings.Contains(msg, "French drains") {
		t.Errorf("edge-scout prompt lacks the won-child fog 'French drains':\n%s", msg)
	}
	if msg := engine.gotReq.Message; strings.Contains(msg, "Photosynthesis") {
		t.Errorf("sibling-goal focal fog 'Photosynthesis' leaked into the edge-scout pool:\n%s", msg)
	}
	// The whole pending inbox is read once (ListPending) then fenced in Go —
	// never the old root-only ListPendingForFocal.
	if fog.listCalls != 1 || len(fog.focalCalls) != 0 {
		t.Errorf("fog reads = {list:%d focal:%v}, want 1 ListPending + 0 ListPendingForFocal", fog.listCalls, fog.focalCalls)
	}
}
