// companion_skill_invoke_scout_goalscope_test.go — CHO-2117: goal-scoped
// kg_explore pool (AC1/AC2/AC4).
//
// RED-first: when the invoking Companion is BOUND to a rooted Goal (ADR-214 —
// the goal ≡ the map's root concept; scope = the sub-tree under that root),
// the deterministic fog pool must be assembled from the SUBTREE's concepts
// ONLY — never from the learner's other maps / loose concepts (the
// cross-domain corpus leak this story closes). The filter sits in the pool
// assembly, strictly BEFORE the one fenced LLM turn (AC2). An empty scoped
// pool yields the honest empty-state narration — NEVER a fallback to the
// unscoped whole-map corpus (AC4).
//
// Unbound companions (no goal designates them) and rootless goals (no map
// linkage) keep the whole-own-map pool — there is no goal context to scope
// by; that is the scope's definition, not a fallback.
package http

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakeKgExploreGoals struct {
	goals []*goal.Goal
	err   error
}

func (f *fakeKgExploreGoals) ListByLearner(_ context.Context, _, _ string) ([]*goal.Goal, error) {
	return f.goals, f.err
}

type fakeKgExploreEdges struct {
	edges []*conceptgraph.Edge
	err   error
}

func (f *fakeKgExploreEdges) ListByLearner(_ context.Context, _, _ string) ([]*conceptgraph.Edge, error) {
	return f.edges, f.err
}

// ---------------------------------------------------------------------------
// fixture helpers
// ---------------------------------------------------------------------------

const (
	fogAtomC = "01970000-f0f0-7000-8000-0000000000f3"
	fogAtomD = "01970000-f0f0-7000-8000-0000000000f4"
	fogAtomE = "01970000-f0f0-7000-8000-0000000000f5"
)

// scoutGoal builds a live learner Goal bound to companionID (blank = unbound),
// rooted at rootConceptID (nil = rootless / no map linkage).
func scoutGoal(goalID, companionID string, rootConceptID *string) *goal.Goal {
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	g := &goal.Goal{
		GoalID: goalID, TenantID: testTenant, LearnerGCID: testGCID,
		Kind: goal.KindCuriosity, Status: goal.StatusActive,
		RootConceptID: rootConceptID,
		CreatedAt:     now, UpdatedAt: now,
	}
	if companionID != "" {
		g.AttachedCompanionID = &companionID
	}
	return g
}

func scoutHierEdge(parentID, childID string) *conceptgraph.Edge {
	return &conceptgraph.Edge{
		EdgeID: "e-" + parentID + "-" + childID, TenantID: testTenant, LearnerGCID: testGCID,
		SourceConceptID: parentID, TargetConceptID: childID,
		Class: conceptgraph.EdgeClassHierarchy,
	}
}

// seedGoalScopedScout builds the canonical two-map learner:
//
//	Maths map (goal-rooted):  "Maths" ─hierarchy→ "Fractions" (atom tagged "Decimals")
//	Off-map concept        :  "Software Design" (atom tagged "Continuous Integration")
//
// and binds the maths goal to the invoking companion. Returns the server, the
// companion id and the engine fake.
func seedGoalScopedScout(t *testing.T, reply string) (*Server, string, *fakeChatEngine) {
	t.Helper()
	srv, id, engine, fmap, _, atoms := seedKgExploreServer(t, 4, reply)
	saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
	saveTopicAtom(t, atoms, fogAtomD, "CI pipelines", "Continuous Integration")

	root := fogConcept("Maths")
	child := fogConcept("Fractions", fogAtomC)
	offMap := fogConcept("Software Design", fogAtomD)
	fmap.nodes = []*conceptgraph.ConceptNode{root, child, offMap}

	srv.KgExploreGoals = &fakeKgExploreGoals{goals: []*goal.Goal{
		scoutGoal("g-maths", id, &root.ConceptID),
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{edges: []*conceptgraph.Edge{
		scoutHierEdge(root.ConceptID, child.ConceptID),
	}}
	equipForInvoke(t, srv, id, "kg_explore")
	return srv, id, engine
}

// ---------------------------------------------------------------------------
// AC1 + AC2 — the pool is goal-subtree-scoped BEFORE the LLM turn
// ---------------------------------------------------------------------------

func TestInvokeKgExplore_GoalScoped_SubtreeOnlyPool(t *testing.T) {
	decimalsOnly := `{"candidates":[{"title":"Decimals","intent":"explore","source":"fog","rationale":"adjacent to your fractions work"}]}`
	srv, id, engine := seedGoalScopedScout(t, decimalsOnly)

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "kg_explore", map[string]string{"count": "3"}))

	if resp["result_kind"] != "suggestion" {
		t.Fatalf("result_kind = %v, want suggestion", resp["result_kind"])
	}
	// The fenced pool carries the goal-subtree topic…
	if !strings.Contains(engine.gotReq.Message, "Decimals") {
		t.Errorf("goal-scoped pool missing subtree topic %q:\n%s", "Decimals", engine.gotReq.Message)
	}
	// …and NEVER the off-map (cross-domain) topic — filtered BEFORE the LLM.
	if strings.Contains(engine.gotReq.Message, "Continuous Integration") {
		t.Errorf("goal-scoped pool leaked an off-goal topic into the fenced turn:\n%s", engine.gotReq.Message)
	}
}

func TestInvokeKgExplore_GoalScoped_OffGoalTitleReconcileRejected(t *testing.T) {
	// Even if the model names the off-goal topic, it is not in the scoped pool —
	// the reconcile floor rejects it wholesale (502, nothing written).
	forged := `{"candidates":[{"title":"Continuous Integration","intent":"explore","source":"fog","rationale":"x"}]}`
	srv, id, _ := seedGoalScopedScout(t, forged)

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/kg_explore/invoke",
		map[string]any{"params": map[string]string{}})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s, want 502 (off-goal title not in scoped pool)", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "KG_EXPLORE_BAD_REPLY" {
		t.Errorf("code = %q, want KG_EXPLORE_BAD_REPLY", code)
	}
}

// ---------------------------------------------------------------------------
// AC4 — empty scoped pool ⇒ honest empty state, NEVER the off-goal corpus
// ---------------------------------------------------------------------------

func TestInvokeKgExplore_GoalScoped_EmptyScopedPool_HonestEmptyState(t *testing.T) {
	srv, id, engine, fmap, sink, atoms := seedKgExploreServer(t, 4,
		"Nothing new on this map yet — keep studying and fresh trails will appear.")
	// The maths subtree's only atom tag normalises to an existing concept title
	// ("Fractions") ⇒ excluded as conceptualised ⇒ scoped pool EMPTY. The off-map
	// concept still has an unexplored topic — it must NOT be used as a fallback.
	saveTopicAtom(t, atoms, fogAtomC, "Fractions drill", "Fractions")
	saveTopicAtom(t, atoms, fogAtomD, "CI pipelines", "Continuous Integration")
	root := fogConcept("Maths")
	child := fogConcept("Fractions", fogAtomC)
	offMap := fogConcept("Software Design", fogAtomD)
	fmap.nodes = []*conceptgraph.ConceptNode{root, child, offMap}
	srv.KgExploreGoals = &fakeKgExploreGoals{goals: []*goal.Goal{scoutGoal("g-maths", id, &root.ConceptID)}}
	srv.KgExploreEdges = &fakeKgExploreEdges{edges: []*conceptgraph.Edge{scoutHierEdge(root.ConceptID, child.ConceptID)}}
	equipForInvoke(t, srv, id, "kg_explore")

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "kg_explore", nil))

	// Honest empty state: no strict-JSON candidate list, no off-goal topic fenced.
	if strings.Contains(engine.gotReq.Message, "STRICT JSON") {
		t.Errorf("empty scoped pool must yield the empty-state turn, not a candidate list:\n%s", engine.gotReq.Message)
	}
	if strings.Contains(engine.gotReq.Message, "Continuous Integration") {
		t.Errorf("empty scoped pool FELL BACK to the off-goal corpus:\n%s", engine.gotReq.Message)
	}
	if len(sink.all()) != 0 {
		t.Errorf("empty scoped pool must write no suggestions; got %d", len(sink.all()))
	}
	if resp["recorded"] != false {
		t.Errorf("recorded = %v, want false on empty scoped pool", resp["recorded"])
	}
}

// ---------------------------------------------------------------------------
// scope definition — unbound companion / rootless goal keep the whole-map pool
// ---------------------------------------------------------------------------

// scoutDecimalsReply names a topic every scoped/unscoped fixture pool fences,
// so the reconcile floor passes and the assertion focus stays on the POOL.
const scoutDecimalsReply = `{"candidates":[{"title":"Decimals","intent":"explore","source":"fog","rationale":"adjacent to your fractions work"}]}`

func TestInvokeKgExplore_UnboundCompanion_WholeMapPool(t *testing.T) {
	srv, id, engine, fmap, _, atoms := seedKgExploreServer(t, 4, scoutDecimalsReply)
	saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
	saveTopicAtom(t, atoms, fogAtomD, "CI pipelines", "Continuous Integration")
	root := fogConcept("Maths")
	child := fogConcept("Fractions", fogAtomC)
	offMap := fogConcept("Software Design", fogAtomD)
	fmap.nodes = []*conceptgraph.ConceptNode{root, child, offMap}
	// A rooted goal exists but designates a DIFFERENT companion — the invoking
	// companion is unbound, so there is no goal context: whole-own-map pool.
	srv.KgExploreGoals = &fakeKgExploreGoals{goals: []*goal.Goal{
		scoutGoal("g-maths", "01970000-aaaa-7000-8000-00000000aaaa", &root.ConceptID),
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{edges: []*conceptgraph.Edge{scoutHierEdge(root.ConceptID, child.ConceptID)}}
	equipForInvoke(t, srv, id, "kg_explore")

	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "kg_explore", nil))

	for _, topic := range []string{"Decimals", "Continuous Integration"} {
		if !strings.Contains(engine.gotReq.Message, topic) {
			t.Errorf("unbound companion must keep the whole-map pool; missing %q:\n%s", topic, engine.gotReq.Message)
		}
	}
}

func TestInvokeKgExplore_RootlessBoundGoal_WholeMapPool(t *testing.T) {
	srv, id, engine, fmap, _, atoms := seedKgExploreServer(t, 4, scoutDecimalsReply)
	saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
	saveTopicAtom(t, atoms, fogAtomD, "CI pipelines", "Continuous Integration")
	fmap.nodes = []*conceptgraph.ConceptNode{
		fogConcept("Fractions", fogAtomC),
		fogConcept("Software Design", fogAtomD),
	}
	// Bound goal with NO root concept — no map linkage exists to scope by.
	srv.KgExploreGoals = &fakeKgExploreGoals{goals: []*goal.Goal{scoutGoal("g-rootless", id, nil)}}
	equipForInvoke(t, srv, id, "kg_explore")

	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "kg_explore", nil))

	for _, topic := range []string{"Decimals", "Continuous Integration"} {
		if !strings.Contains(engine.gotReq.Message, topic) {
			t.Errorf("rootless bound goal must keep the whole-map pool; missing %q:\n%s", topic, engine.gotReq.Message)
		}
	}
}

func TestInvokeKgExplore_TwoBoundGoals_UnionScope(t *testing.T) {
	srv, id, engine, fmap, _, atoms := seedKgExploreServer(t, 4, scoutDecimalsReply)
	saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
	saveTopicAtom(t, atoms, fogAtomD, "CI pipelines", "Continuous Integration")
	saveTopicAtom(t, atoms, fogAtomE, "Sonnet study", "Rhyme Schemes")
	maths := fogConcept("Maths", fogAtomC)
	se := fogConcept("Software Design", fogAtomD)
	poetry := fogConcept("Poetry", fogAtomE) // no goal roots this map
	fmap.nodes = []*conceptgraph.ConceptNode{maths, se, poetry}
	// Data anomaly / future multi-bond: BOTH rooted goals designate this
	// companion — the scope is the UNION of their subtrees, never the loose map.
	srv.KgExploreGoals = &fakeKgExploreGoals{goals: []*goal.Goal{
		scoutGoal("g-maths", id, &maths.ConceptID),
		scoutGoal("g-se", id, &se.ConceptID),
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{}
	equipForInvoke(t, srv, id, "kg_explore")

	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "kg_explore", nil))

	for _, topic := range []string{"Decimals", "Continuous Integration"} {
		if !strings.Contains(engine.gotReq.Message, topic) {
			t.Errorf("union scope missing %q:\n%s", topic, engine.gotReq.Message)
		}
	}
	if strings.Contains(engine.gotReq.Message, "Rhyme Schemes") {
		t.Errorf("union scope leaked a loose-map topic:\n%s", engine.gotReq.Message)
	}
}

// ---------------------------------------------------------------------------
// fail-loud reads + wiring gate
// ---------------------------------------------------------------------------

func TestInvokeKgExplore_GoalReadErrorIsLoud500(t *testing.T) {
	srv, id, _, fmap, sink, atoms := seedKgExploreServer(t, 4, fogScoutJSON)
	saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
	fmap.nodes = []*conceptgraph.ConceptNode{fogConcept("Maths", fogAtomC)}
	srv.KgExploreGoals = &fakeKgExploreGoals{err: context.DeadlineExceeded}
	equipForInvoke(t, srv, id, "kg_explore")

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/kg_explore/invoke",
		map[string]any{"params": map[string]string{}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500 on goal read failure (never a silently unscoped pool)", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "KG_EXPLORE_GOAL_READ_FAILED" {
		t.Errorf("code = %q, want KG_EXPLORE_GOAL_READ_FAILED", code)
	}
	if len(sink.all()) != 0 {
		t.Errorf("a failed goal read must write nothing; got %d", len(sink.all()))
	}
}

func TestInvokeKgExplore_EdgeReadErrorIsLoud500(t *testing.T) {
	srv, id, _, fmap, _, atoms := seedKgExploreServer(t, 4, fogScoutJSON)
	saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
	root := fogConcept("Maths", fogAtomC)
	fmap.nodes = []*conceptgraph.ConceptNode{root}
	srv.KgExploreGoals = &fakeKgExploreGoals{goals: []*goal.Goal{scoutGoal("g-maths", id, &root.ConceptID)}}
	srv.KgExploreEdges = &fakeKgExploreEdges{err: context.DeadlineExceeded}
	equipForInvoke(t, srv, id, "kg_explore")

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/kg_explore/invoke",
		map[string]any{"params": map[string]string{}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500 on edge read failure", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "KG_EXPLORE_EDGE_READ_FAILED" {
		t.Errorf("code = %q, want KG_EXPLORE_EDGE_READ_FAILED", code)
	}
}

func TestInvokeKgExplore_GoalScopePortsNilAre503(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*Server)
	}{
		{"goal lister nil", func(s *Server) { s.KgExploreGoals = nil }},
		{"edge reader nil", func(s *Server) { s.KgExploreEdges = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, id, _, fmap, _, atoms := seedKgExploreServer(t, 4, fogScoutJSON)
			saveTopicAtom(t, atoms, fogAtomC, "Comparing decimals", "Decimals")
			fmap.nodes = []*conceptgraph.ConceptNode{fogConcept("Maths", fogAtomC)}
			equipForInvoke(t, srv, id, "kg_explore")
			tc.break_(srv)

			w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/kg_explore/invoke",
				map[string]any{"params": map[string]string{}})
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s, want 503 (goal-scope port unwired = fail-closed)", w.Code, w.Body.String())
			}
			if code := errCode(t, w.Body.Bytes()); code != "SKILL_INVOKE_NOT_WIRED" {
				t.Errorf("code = %q, want SKILL_INVOKE_NOT_WIRED", code)
			}
		})
	}
}
