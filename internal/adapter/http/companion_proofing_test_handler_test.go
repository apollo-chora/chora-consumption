// companion_proofing_test_handler_test.go — CHO-2040 (R8-6/R8-7): the Virgin
// Proofing Test COMPOSED runner, RED-first.
//
//	POST /v1/me/companions/{id}/proofing-test   {"goal_id":"<uuid>"}
//	GET  /v1/me/proofing-tests?goal_id=
//
// Owner ruling R8-6: a composed goal-driven runner on the ceremony edge-scout
// conventions — NO catalogue row, NO grant/equip/stage gate. The runner reads
// the goal's ticked (intent-tagged) learning edges, reserves mana
// (proofing_test_gen, reserve→settle/refund), and publishes ONE
// chora.creation.ai_assist.started.v2 batch request whose field 19
// target_growth_edges carries the edges' CONCEPT KEYS (R8-7: keys, never
// titles — un-keyed edges 422 EDGES_LACK_KEYS).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

const (
	ptGoalID = "01970000-aaaa-7000-8000-00000000f7e0"
	ptRootID = "01970000-bbbb-7000-8000-00000000f7e1"
)

// ptEdgeReaderFake fakes proofingtest.TickedEdgeReader.
type ptEdgeReaderFake struct {
	edges    []pt.TargetEdge
	err      error
	gotRoot  string
	gotCalls int
}

func (f *ptEdgeReaderFake) ListTicked(_ context.Context, _, _, rootConceptID string) ([]pt.TargetEdge, error) {
	f.gotCalls++
	f.gotRoot = rootConceptID
	if f.err != nil {
		return nil, f.err
	}
	return f.edges, nil
}

// ptManaFake fakes proofingtest.ManaReserver, recording call order against the
// shared sequence counter so reserve-before-publish is pinned.
type ptManaFake struct {
	seq        *int
	reserveAt  int
	refundAt   int
	reserves   []pt.ReserveInput
	refunds    []pt.RefundInput
	reserveErr error
	refundErr  error
	price      int64
}

func (f *ptManaFake) Reserve(_ context.Context, in pt.ReserveInput) (pt.ReserveResult, error) {
	*f.seq++
	f.reserveAt = *f.seq
	f.reserves = append(f.reserves, in)
	if f.reserveErr != nil {
		return pt.ReserveResult{}, f.reserveErr
	}
	price := f.price
	if price == 0 {
		price = 25
	}
	return pt.ReserveResult{
		ReservationID: "proofing_test_gen:" + in.TenantID + ":" + in.GCID + ":" + in.ProofingTestID,
		PriceUnits:    price,
	}, nil
}

func (f *ptManaFake) Refund(_ context.Context, in pt.RefundInput) error {
	*f.seq++
	f.refundAt = *f.seq
	f.refunds = append(f.refunds, in)
	return f.refundErr
}

// ptPublisherFake captures the started.v2 publish.
type ptPublisherFake struct {
	seq       *int
	publishAt int
	topics    []string
	envs      []events.Envelope
	payloads  []map[string]any
	err       error
}

func (f *ptPublisherFake) Publish(topic string, env events.Envelope, payload map[string]any) error {
	*f.seq++
	f.publishAt = *f.seq
	f.topics = append(f.topics, topic)
	f.envs = append(f.envs, env)
	f.payloads = append(f.payloads, payload)
	return f.err
}

func ptKeyedEdges() []pt.TargetEdge {
	return []pt.TargetEdge{
		{ConceptID: "01970000-cccc-7000-8000-00000000f7e2", Key: "algebra.quadratic_roots", Title: "Quadratic roots", Intent: "remediate"},
		{ConceptID: "01970000-cccc-7000-8000-00000000f7e3", Key: "algebra.completing_square", Title: "Completing the square", Intent: "explore"},
	}
}

// seedProofingServer wires a Server with an owned companion, an owned rooted
// goal, keyed ticked edges, a funded reserver, an inmem repo, and a capturing
// publisher.
func seedProofingServer(t *testing.T) (*Server, string, *ptEdgeReaderFake, *ptManaFake, *ptPublisherFake, *inmem.ProofingTestRepo) {
	t.Helper()
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	root := ptRootID
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{
		ptGoalID: {
			GoalID: ptGoalID, TenantID: testTenant, LearnerGCID: testGCID,
			Kind: goal.KindCuriosity, Status: goal.StatusActive,
			RootConceptID: &root,
			ConceptSet:    []string{"algebra", "quadratics"},
		},
	}}
	seq := 0
	edges := &ptEdgeReaderFake{edges: ptKeyedEdges()}
	mana := &ptManaFake{seq: &seq}
	pub := &ptPublisherFake{seq: &seq}
	repo := inmem.NewProofingTestRepo()
	srv.ProofingTickedEdges = edges
	srv.ProofingMana = mana
	srv.ProofingPublisher = pub
	srv.ProofingTests = repo
	return srv, id, edges, mana, pub, repo
}

func ptPath(companionID string) string {
	return "/v1/me/companions/" + companionID + "/proofing-test"
}

func ptBody() map[string]any { return map[string]any{"goal_id": ptGoalID} }

// ----------------------------------------------------------------------------
// Gates
// ----------------------------------------------------------------------------

func TestProofingTest_NotWired(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	srv.ProofingPublisher = nil
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "PROOFING_TEST_NOT_WIRED" {
		t.Fatalf("code = %q, want PROOFING_TEST_NOT_WIRED", code)
	}
}

func TestProofingTest_RequiresAuth(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	w := plainReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

func TestProofingTest_MethodNotAllowed(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	w := authedReq(t, srv, http.MethodGet, ptPath(id), nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body=%s", w.Code, w.Body.String())
	}
}

func TestProofingTest_GoalRefValidation(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	for name, body := range map[string]map[string]any{
		"missing":   {},
		"blank":     {"goal_id": "  "},
		"malformed": {"goal_id": "not-a-uuid"},
	} {
		w := authedReq(t, srv, http.MethodPost, ptPath(id), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
			continue
		}
		if code := errCode(t, w.Body.Bytes()); code != "INVALID_GOAL_REF" {
			t.Errorf("%s: code = %q, want INVALID_GOAL_REF", name, code)
		}
	}
}

func TestProofingTest_PlanValidation(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	for name, body := range map[string]map[string]any{
		"negative": {"goal_id": ptGoalID, "mcq_count": -1},
		"overcap":  {"goal_id": ptGoalID, "oe_count": 99},
	} {
		w := authedReq(t, srv, http.MethodPost, ptPath(id), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
			continue
		}
		if code := errCode(t, w.Body.Bytes()); code != "INVALID_PLAN" {
			t.Errorf("%s: code = %q, want INVALID_PLAN", name, code)
		}
	}
}

func TestProofingTest_UnknownCompanion(t *testing.T) {
	srv, _, _, _, _, _ := seedProofingServer(t)
	w := authedReq(t, srv, http.MethodPost, ptPath("01970000-dead-7000-8000-00000000000a"), ptBody())
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "COMPANION_NOT_FOUND" {
		t.Fatalf("code = %q, want COMPANION_NOT_FOUND", code)
	}
}

func TestProofingTest_GoalNotFoundOrForeign(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	w := authedReq(t, srv, http.MethodPost, ptPath(id),
		map[string]any{"goal_id": "01970000-aaaa-7000-8000-00000000dead"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "GOAL_NOT_FOUND" {
		t.Fatalf("code = %q, want GOAL_NOT_FOUND", code)
	}
}

func TestProofingTest_NoTickedEdges422(t *testing.T) {
	srv, id, edges, mana, pub, _ := seedProofingServer(t)
	edges.edges = nil
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "NO_TICKED_EDGES" {
		t.Fatalf("code = %q, want NO_TICKED_EDGES", code)
	}
	if len(mana.reserves) != 0 || len(pub.topics) != 0 {
		t.Fatalf("no reserve/publish on a gate refusal")
	}
}

func TestProofingTest_RootlessGoal422(t *testing.T) {
	srv, id, _, _, _, _ := seedProofingServer(t)
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{
		ptGoalID: {
			GoalID: ptGoalID, TenantID: testTenant, LearnerGCID: testGCID,
			Kind: goal.KindCuriosity, Status: goal.StatusActive,
			// RootConceptID nil — no subtree, no ticked edges possible.
		},
	}}
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "NO_TICKED_EDGES" {
		t.Fatalf("code = %q, want NO_TICKED_EDGES", code)
	}
}

// R8-7 — THE gate: un-keyed ticked edges refuse 422 EDGES_LACK_KEYS. The KG
// session is adding key-at-mint separately; until those keys exist the runner
// must fail honestly, never publish titles as keys.
func TestProofingTest_EdgesLackKeys422_R87(t *testing.T) {
	srv, id, edges, mana, pub, repo := seedProofingServer(t)
	unkeyed := ptKeyedEdges()
	unkeyed[1].Key = "" // ConceptNode carries no key field today ⇒ adapter yields ""
	edges.edges = unkeyed
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "EDGES_LACK_KEYS" {
		t.Fatalf("code = %q, want EDGES_LACK_KEYS (R8-7)", code)
	}
	if len(mana.reserves) != 0 {
		t.Fatalf("no mana reserve behind the R8-7 gate")
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no publish behind the R8-7 gate")
	}
	got, _ := repo.ListByLearner(context.Background(), testTenant, testGCID, "")
	if len(got) != 0 {
		t.Fatalf("no row persisted behind the R8-7 gate")
	}
}

func TestProofingTest_EdgeReadFailure500(t *testing.T) {
	srv, id, edges, _, _, _ := seedProofingServer(t)
	edges.err = errors.New("pg down")
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "PROOFING_EDGES_READ_FAILED" {
		t.Fatalf("code = %q, want PROOFING_EDGES_READ_FAILED", code)
	}
}

// ----------------------------------------------------------------------------
// Mana
// ----------------------------------------------------------------------------

func TestProofingTest_InsufficientMana402(t *testing.T) {
	srv, id, _, mana, pub, _ := seedProofingServer(t)
	mana.reserveErr = &pt.ErrInsufficientMana{RequiredUnits: 25, CurrentBalance: 2}
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "insufficient_mana") {
		t.Fatalf("402 body must carry the insufficient_mana envelope, got %s", w.Body.String())
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no publish after a failed reserve")
	}
}

func TestProofingTest_ReserveInfraFailure502(t *testing.T) {
	srv, id, _, mana, pub, _ := seedProofingServer(t)
	mana.reserveErr = errors.New("identity unreachable")
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "PROOFING_MANA_RESERVE_FAILED" {
		t.Fatalf("code = %q, want PROOFING_MANA_RESERVE_FAILED", code)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no publish after a failed reserve")
	}
}

// ----------------------------------------------------------------------------
// Happy path — reserve → persist → publish → 202
// ----------------------------------------------------------------------------

func TestProofingTest_HappyPath202(t *testing.T) {
	srv, id, edges, mana, pub, repo := seedProofingServer(t)
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
	}

	// The edge read was root-scoped to the goal's root concept.
	if edges.gotRoot != ptRootID {
		t.Fatalf("edge read root = %q, want %q", edges.gotRoot, ptRootID)
	}

	// Reserve fired BEFORE publish, once, with the aggregate id as nonce.
	if len(mana.reserves) != 1 {
		t.Fatalf("reserves = %d, want 1", len(mana.reserves))
	}
	if len(pub.topics) != 1 {
		t.Fatalf("publishes = %d, want 1", len(pub.topics))
	}
	if !(mana.reserveAt < pub.publishAt) {
		t.Fatalf("reserve (%d) must precede publish (%d)", mana.reserveAt, pub.publishAt)
	}
	if mana.reserves[0].TenantID != testTenant || mana.reserves[0].GCID != testGCID {
		t.Fatalf("reserve identity mis-passed: %+v", mana.reserves[0])
	}

	// Row persisted, requested, with reservation stamped.
	rows, err := repo.ListByLearner(context.Background(), testTenant, testGCID, ptGoalID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d (%v), want 1", len(rows), err)
	}
	row := rows[0]
	if row.Status != pt.StatusRequested {
		t.Fatalf("row status = %q, want requested", row.Status)
	}
	if row.GoalID != ptGoalID || row.CompanionID != id {
		t.Fatalf("row refs mis-set: %+v", row)
	}
	if row.ManaReserved != 25 || row.ReservationID == "" {
		t.Fatalf("row reservation mis-set: reserved=%d id=%q", row.ManaReserved, row.ReservationID)
	}
	if mana.reserves[0].ProofingTestID != row.ID {
		t.Fatalf("reservation nonce = %q, want the aggregate id %q", mana.reserves[0].ProofingTestID, row.ID)
	}

	// The ONE published event: the qgen crew's request lane, batch-shaped.
	if pub.topics[0] != "chora.creation.ai_assist.started.v2" {
		t.Fatalf("topic = %q", pub.topics[0])
	}
	payload := pub.payloads[0]
	if payload["assist_id"] != row.AssistID {
		t.Fatalf("assist_id = %v, want %q", payload["assist_id"], row.AssistID)
	}
	if payload["tenant_id"] != testTenant || payload["author_gcid"] != testGCID {
		t.Fatalf("payload identity mis-set: %v / %v", payload["tenant_id"], payload["author_gcid"])
	}
	if payload["content_type"] != "mixed" {
		t.Fatalf("content_type = %v, want mixed", payload["content_type"])
	}
	// Field 19 — concept KEYS, in edge order (R8-7).
	keys, ok := payload["target_growth_edges"].([]string)
	if !ok || len(keys) != 2 || keys[0] != "algebra.quadratic_roots" || keys[1] != "algebra.completing_square" {
		t.Fatalf("target_growth_edges = %#v", payload["target_growth_edges"])
	}
	// Field 21 — default mixed plan 4 MCQ + 2 OE; requested_count = sum.
	plan, ok := payload["type_plan"].([]map[string]any)
	if !ok || len(plan) != 2 {
		t.Fatalf("type_plan = %#v", payload["type_plan"])
	}
	if plan[0]["question_type"] != "mcq" || plan[0]["count"] != pt.DefaultMCQCount {
		t.Fatalf("plan[0] = %#v", plan[0])
	}
	if plan[1]["question_type"] != "oe" || plan[1]["count"] != pt.DefaultOECount {
		t.Fatalf("plan[1] = %#v", plan[1])
	}
	if payload["requested_count"] != pt.DefaultMCQCount+pt.DefaultOECount {
		t.Fatalf("requested_count = %v", payload["requested_count"])
	}
	// ADR-195 compose trio — v2 routes on these.
	if payload["operation"] != "compose" || payload["intent"] != "new_question" || payload["input_kind"] != "prompt" {
		t.Fatalf("compose trio = %v/%v/%v", payload["operation"], payload["intent"], payload["input_kind"])
	}
	prompt, _ := payload["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		t.Fatalf("prompt must be non-empty (orchestrator validate rejects empty)")
	}
	if !strings.Contains(prompt, "algebra.quadratic_roots") {
		t.Fatalf("prompt must ground on the edge keys, got %q", prompt)
	}

	// Envelope: mandatory fields + deterministic idempotency key.
	env := pub.envs[0]
	if env.TenantID != testTenant || env.GCID != testGCID {
		t.Fatalf("envelope identity mis-set: %+v", env)
	}
	if env.IdempotencyKey != "proofing_test.requested."+row.AssistID {
		t.Fatalf("envelope idem key = %q", env.IdempotencyKey)
	}
	if env.Traceparent == "" {
		t.Fatalf("envelope traceparent must be ensured (outbox validation rejects empty)")
	}

	// 202 body carries the aggregate projection.
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	inner, _ := resp["proofing_test"].(map[string]any)
	if inner == nil {
		t.Fatalf("202 body missing proofing_test: %s", w.Body.String())
	}
	if inner["id"] != row.ID || inner["status"] != "requested" || inner["assist_id"] != row.AssistID {
		t.Fatalf("202 projection mis-shaped: %v", inner)
	}
	if inner["mana_reserved"] != float64(25) {
		t.Fatalf("mana_reserved = %v, want 25", inner["mana_reserved"])
	}
	edgesOut, _ := inner["target_edges"].([]any)
	if len(edgesOut) != 2 {
		t.Fatalf("target_edges = %#v", inner["target_edges"])
	}
}

func TestProofingTest_CustomPlanCounts(t *testing.T) {
	srv, id, _, _, pub, _ := seedProofingServer(t)
	w := authedReq(t, srv, http.MethodPost, ptPath(id),
		map[string]any{"goal_id": ptGoalID, "mcq_count": 2, "oe_count": 1})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
	}
	plan := pub.payloads[0]["type_plan"].([]map[string]any)
	if plan[0]["count"] != 2 || plan[1]["count"] != 1 {
		t.Fatalf("custom plan not honoured: %#v", plan)
	}
	if pub.payloads[0]["requested_count"] != 3 {
		t.Fatalf("requested_count = %v, want 3", pub.payloads[0]["requested_count"])
	}
}

// ----------------------------------------------------------------------------
// Failure compensation
// ----------------------------------------------------------------------------

func TestProofingTest_PublishFailureRefundsAndFails(t *testing.T) {
	srv, id, _, mana, pub, repo := seedProofingServer(t)
	pub.err = errors.New("outbox down")
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "PROOFING_TEST_PUBLISH_FAILED" {
		t.Fatalf("code = %q, want PROOFING_TEST_PUBLISH_FAILED", code)
	}
	if len(mana.refunds) != 1 {
		t.Fatalf("refunds = %d, want 1 (reserve must not strand)", len(mana.refunds))
	}
	if mana.refunds[0].ReservationID == "" || mana.refunds[0].Units != 25 {
		t.Fatalf("refund mis-passed: %+v", mana.refunds[0])
	}
	// The persisted row is honestly failed, not stranded in requested.
	rows, _ := repo.ListByLearner(context.Background(), testTenant, testGCID, "")
	if len(rows) != 1 || rows[0].Status != pt.StatusFailed {
		t.Fatalf("row must be failed after a publish failure, got %+v", rows)
	}
}

func TestProofingTest_PersistFailureRefunds(t *testing.T) {
	srv, id, _, mana, pub, repo := seedProofingServer(t)
	repo.FailCreate(errors.New("pg down"))
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "PROOFING_TEST_PERSIST_FAILED" {
		t.Fatalf("code = %q, want PROOFING_TEST_PERSIST_FAILED", code)
	}
	if len(mana.refunds) != 1 {
		t.Fatalf("refunds = %d, want 1", len(mana.refunds))
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no publish after a failed persist")
	}
}

// ----------------------------------------------------------------------------
// GET /v1/me/proofing-tests
// ----------------------------------------------------------------------------

func TestListProofingTests_RequiresAuthAndWiring(t *testing.T) {
	srv, _, _, _, _, _ := seedProofingServer(t)
	w := plainReq(t, srv, http.MethodGet, "/v1/me/proofing-tests", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	srv.ProofingTests = nil
	w = authedReq(t, srv, http.MethodGet, "/v1/me/proofing-tests", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestListProofingTests_FilterAndShape(t *testing.T) {
	srv, id, edges, _, _, _ := seedProofingServer(t)
	// Seed two DISTINCT runs on the goal via the real endpoint. They must
	// differ: the runner now collapses an identical in-flight request (same
	// goal + edge-title signature), so two identical POSTs would be one row.
	if w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody()); w.Code != http.StatusAccepted {
		t.Fatalf("seed run 0: %d %s", w.Code, w.Body.String())
	}
	edges.edges = []pt.TargetEdge{
		{ConceptID: "01970000-cccc-7000-8000-00000000f7ea", Key: "algebra.factoring", Title: "Factoring", Intent: "remediate"},
	}
	if w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody()); w.Code != http.StatusAccepted {
		t.Fatalf("seed run 1: %d %s", w.Code, w.Body.String())
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/proofing-tests?goal_id="+ptGoalID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(resp.Items))
	}
	if resp.Items[0]["goal_id"] != ptGoalID || resp.Items[0]["status"] != "requested" {
		t.Fatalf("item mis-shaped: %v", resp.Items[0])
	}
	// Foreign goal filter → empty NON-NULL array.
	w = authedReq(t, srv, http.MethodGet, "/v1/me/proofing-tests?goal_id=01970000-aaaa-7000-8000-00000000dead", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must render items:[] (never null), got %s", w.Body.String())
	}
	// Malformed filter fails loud.
	w = authedReq(t, srv, http.MethodGet, "/v1/me/proofing-tests?goal_id=nope", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ----------------------------------------------------------------------------
// Within-request title-dedupe (CHO-2040 loop tail)
// ----------------------------------------------------------------------------

// TestProofingTest_DedupesDuplicateTitleEdges — when the ticked-edge reader
// yields two edges sharing a normalized title (cross-ceremony ConceptNode
// dupes), the ONE published request carries each concept key ONCE, the prompt
// lists the concept once, and the persisted row stores the deduped set. The
// economy is unchanged (one reserve, one publish).
func TestProofingTest_DedupesDuplicateTitleEdges(t *testing.T) {
	srv, id, edges, mana, pub, repo := seedProofingServer(t)
	edges.edges = []pt.TargetEdge{
		{ConceptID: "01970000-cccc-7000-8000-00000000f7e2", Key: "algebra.quadratic_roots", Title: "Quadratic roots", Intent: "remediate"},
		{ConceptID: "01970000-cccc-7000-8000-00000000f7e3", Key: "algebra.completing_square", Title: "Completing the square", Intent: "explore"},
		// Cross-ceremony duplicate of the first by normalized title.
		{ConceptID: "01970000-cccc-7000-8000-00000000f7e4", Key: "algebra.roots_dup", Title: "  quadratic   ROOTS ", Intent: "explore"},
	}
	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
	}

	// Field 19 keys: the dup collapsed, first occurrence's key kept, in order.
	keys, ok := pub.payloads[0]["target_growth_edges"].([]string)
	if !ok || len(keys) != 2 || keys[0] != "algebra.quadratic_roots" || keys[1] != "algebra.completing_square" {
		t.Fatalf("target_growth_edges not deduped: %#v", pub.payloads[0]["target_growth_edges"])
	}
	// The generator prompt lists the concept once.
	prompt, _ := pub.payloads[0]["prompt"].(string)
	if n := strings.Count(prompt, "algebra.quadratic_roots"); n != 1 {
		t.Fatalf("prompt lists the deduped key %d times, want 1:\n%s", n, prompt)
	}
	// Persisted row stores the deduped edge set.
	rows, _ := repo.ListByLearner(context.Background(), testTenant, testGCID, ptGoalID)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if len(rows[0].TargetEdges) != 2 {
		t.Fatalf("row edges = %d, want 2 (deduped)", len(rows[0].TargetEdges))
	}
	// Dedupe touches the payload, not the economy.
	if len(mana.reserves) != 1 || len(pub.topics) != 1 {
		t.Fatalf("reserves=%d publishes=%d, want 1/1", len(mana.reserves), len(pub.topics))
	}
}

// ----------------------------------------------------------------------------
// Cross-request idempotency (double-submit / double-charge guard)
// ----------------------------------------------------------------------------

// proofingID extracts proofing_test.id from a 202 body.
func proofingID(t *testing.T, body []byte) string {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	inner, _ := resp["proofing_test"].(map[string]any)
	sid, _ := inner["id"].(string)
	if sid == "" {
		t.Fatalf("no proofing_test.id in %s", string(body))
	}
	return sid
}

// TestProofingTest_IdempotentOnInFlightDuplicate — a double-submit / FE retry
// for the same goal + edge-title signature while a generation is still in
// flight returns the SAME row and does NOT reserve mana again.
func TestProofingTest_IdempotentOnInFlightDuplicate(t *testing.T) {
	srv, id, _, mana, pub, repo := seedProofingServer(t)

	w1 := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w1.Code != http.StatusAccepted {
		t.Fatalf("first: status = %d, body=%s", w1.Code, w1.Body.String())
	}
	firstID := proofingID(t, w1.Body.Bytes())

	w2 := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w2.Code != http.StatusAccepted {
		t.Fatalf("second: status = %d, body=%s", w2.Code, w2.Body.String())
	}
	if got := proofingID(t, w2.Body.Bytes()); got != firstID {
		t.Fatalf("idempotent replay must return the in-flight id %q, got %q", firstID, got)
	}
	// No second charge, no second publish, no second row.
	if len(mana.reserves) != 1 {
		t.Fatalf("reserves = %d, want 1 (no double charge)", len(mana.reserves))
	}
	if len(pub.topics) != 1 {
		t.Fatalf("publishes = %d, want 1", len(pub.topics))
	}
	rows, _ := repo.ListByLearner(context.Background(), testTenant, testGCID, ptGoalID)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (in-flight duplicate not persisted)", len(rows))
	}
}

// TestProofingTest_TerminalDoesNotBlockNewRequest — once a proofing test is
// terminal (ready|failed) the guard lets a fresh identical request through, so
// the learner can regenerate.
func TestProofingTest_TerminalDoesNotBlockNewRequest(t *testing.T) {
	srv, id, _, mana, _, repo := seedProofingServer(t)

	if w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody()); w.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	rows, _ := repo.ListByLearner(context.Background(), testTenant, testGCID, ptGoalID)
	if len(rows) != 1 {
		t.Fatalf("setup rows = %d, want 1", len(rows))
	}
	// Land the first as terminal.
	row := rows[0]
	if err := row.MarkFailed("qgen refused", time.Time{}); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if err := repo.Update(context.Background(), row); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// A fresh identical request now creates a NEW row + charges again.
	if w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody()); w.Code != http.StatusAccepted {
		t.Fatalf("second: %d %s", w.Code, w.Body.String())
	}
	rows, _ = repo.ListByLearner(context.Background(), testTenant, testGCID, ptGoalID)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (terminal must not block regeneration)", len(rows))
	}
	if len(mana.reserves) != 2 {
		t.Fatalf("reserves = %d, want 2", len(mana.reserves))
	}
}

// raceProofingRepo simulates a lost concurrent race: the pre-probe sees no
// in-flight row (1st ListByLearner), but Create hits the DB partial-unique
// index (ErrDuplicateInFlight); the post-conflict lookup (2nd ListByLearner)
// then finds the winning in-flight row.
type raceProofingRepo struct {
	winner    *pt.ProofingTest
	listCalls int
}

func (r *raceProofingRepo) Create(context.Context, *pt.ProofingTest) error {
	return pt.ErrDuplicateInFlight
}
func (r *raceProofingRepo) GetByID(context.Context, string, string, string) (*pt.ProofingTest, error) {
	return nil, nil
}
func (r *raceProofingRepo) GetByAssistID(context.Context, string, string) (*pt.ProofingTest, error) {
	return nil, nil
}
func (r *raceProofingRepo) ListByLearner(_ context.Context, _, _, _ string) ([]*pt.ProofingTest, error) {
	r.listCalls++
	if r.listCalls == 1 {
		return nil, nil // pre-probe misses (the winner hasn't committed yet)
	}
	return []*pt.ProofingTest{r.winner}, nil // post-conflict lookup finds it
}
func (r *raceProofingRepo) Update(context.Context, *pt.ProofingTest) error { return nil }

// TestProofingTest_ConcurrentConflictReturnsWinner — when the DB partial-unique
// index rejects a row the pre-probe didn't catch (a truly-concurrent double-
// submit), the runner refunds its reservation and returns the winning
// in-flight row instead of double-charging or minting a duplicate.
func TestProofingTest_ConcurrentConflictReturnsWinner(t *testing.T) {
	srv, id, _, mana, pub, _ := seedProofingServer(t)
	winner, err := pt.NewRequested(pt.NewRequestedInput{
		TenantID: testTenant, LearnerGCID: testGCID, GoalID: ptGoalID,
		CompanionID: id, TargetEdges: ptKeyedEdges(),
	})
	if err != nil {
		t.Fatalf("winner: %v", err)
	}
	srv.ProofingTests = &raceProofingRepo{winner: winner}

	w := authedReq(t, srv, http.MethodPost, ptPath(id), ptBody())
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
	}
	if got := proofingID(t, w.Body.Bytes()); got != winner.ID {
		t.Fatalf("must return the winning in-flight id %q, got %q", winner.ID, got)
	}
	// We reserved (probe missed) then refunded on the conflict; never published.
	if len(mana.reserves) != 1 {
		t.Fatalf("reserves = %d, want 1", len(mana.reserves))
	}
	if len(mana.refunds) != 1 {
		t.Fatalf("refunds = %d, want 1 (loser must release its reservation)", len(mana.refunds))
	}
	if len(pub.topics) != 0 {
		t.Fatalf("publishes = %d, want 0 (no duplicate generation)", len(pub.topics))
	}
}
