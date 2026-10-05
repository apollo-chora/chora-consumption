// companion_skill_invoke_handler_test.go — CHO-2013 P1.B (R4-4): the
// consumption-side single-step Skill invoke runner.
//
//	POST /v1/me/companions/{id}/skills/{key}/invoke
//
// Spec-§6 Ritual-runner semantics at N=1: validate (owned + equipped +
// active + stage), pre-fetch the Skill's read context locally, drive ONE
// gateway-routed agent turn (ManaActionCode = the Skill's price key so the
// model-gateway debits once per ADR-177), then the RUNNER writes the sink
// (chat → JSON reply; memory_note → companion_memory recap row, LOUD on
// failure). The sidecar-less agent can never call back into consumption's
// STRICT-mTLS surfaces — reads are injected, sinks are runner-side.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	extinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// ----------------------------------------------------------------------------
// Fakes for the invoke-runner read ports (profile / memory view / concepts).
// The engine + mana + embedder + companion-memory fakes are reused from the
// chat/mana/memory test files in this package.
// ----------------------------------------------------------------------------

type fakeProfileReader struct {
	facts    []*lp.Fact
	activity []*lp.ActivityEntry
	err      error
}

func (f *fakeProfileReader) ListFacts(_ context.Context, _, _ string) ([]*lp.Fact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.facts, nil
}

func (f *fakeProfileReader) RecentActivity(_ context.Context, _, _ string, _ int) ([]*lp.ActivityEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.activity, nil
}

type fakeMemoryView struct {
	rows []companionmind.EpisodicMemory
	err  error
	got  companionmind.Query
}

func (f *fakeMemoryView) RecentMemories(_ context.Context, q companionmind.Query) ([]companionmind.EpisodicMemory, error) {
	f.got = q
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

type fakeConceptReader struct {
	nodes map[string]*conceptgraph.ConceptNode
	err   error
}

func (f *fakeConceptReader) GetByID(_ context.Context, _, _, conceptID string) (*conceptgraph.ConceptNode, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.nodes[conceptID], nil // (nil, nil) = live-miss per the repo contract
}

// ----------------------------------------------------------------------------
// Seeding helpers
// ----------------------------------------------------------------------------

// seedInvokeServer wires a Server with an owned companion at the given growth
// stage, a funded wallet, and a fake engine that replies with replyText.
func seedInvokeServer(t *testing.T, stage int, engine *fakeChatEngine) (*Server, string) {
	t.Helper()
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	srv.Growth = &fakeGrowth{getFn: func(_, companionID, _ string) (*growth.State, error) {
		return &growth.State{CompanionID: companionID, GrowthStage: stage, StageName: "test"}, nil
	}}
	srv.CompanionEngineResource = "gke://companion-head-test"
	srv.CompanionEngine = engine
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 1000}}
	return srv, id
}

// equipForInvoke activates + mints + equips a skill on the companion.
func equipForInvoke(t *testing.T, srv *Server, id, key string) {
	t.Helper()
	activateSkill(t, srv, key)
	if w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: key}); w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("mint %s: %d %s", key, w.Code, w.Body.String())
	}
	if w := authedReq(t, srv, http.MethodPut, "/v1/me/companions/"+id+"/skills/"+key+"/equip", nil); w.Code != http.StatusOK {
		t.Fatalf("equip %s: %d %s", key, w.Code, w.Body.String())
	}
}

func invokeSkill(t *testing.T, srv *Server, id, key string, params map[string]string) *json.Decoder {
	t.Helper()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/"+key+"/invoke",
		map[string]any{"params": params})
	if w.Code != http.StatusOK {
		t.Fatalf("invoke %s: status=%d body=%s", key, w.Code, w.Body.String())
	}
	return json.NewDecoder(strings.NewReader(w.Body.String()))
}

func decodeInvokeResp(t *testing.T, dec *json.Decoder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode invoke resp: %v", err)
	}
	return m
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, body)
	}
	if code, ok := m["code"].(string); ok {
		return code
	}
	if e, ok := m["error"].(map[string]any); ok {
		code, _ := e["code"].(string)
		return code
	}
	return ""
}

// activateSkillOff flips a catalogue row back to dark (release rollback).
func activateSkillOff(t *testing.T, srv *Server, key string) {
	t.Helper()
	cat, ok := srv.SkillCatalog.(*inmem.SkillCatalog)
	if !ok {
		t.Fatalf("test server SkillCatalog is %T, want *inmem.SkillCatalog", srv.SkillCatalog)
	}
	cat.SetActive(key, false)
}

// seedProfileFixtures gives the fake profile reader one recent cert fact +
// one old (2-month) fact + one recent activity line.
func seedProfileFixtures(now time.Time) *fakeProfileReader {
	score := 0.92
	return &fakeProfileReader{
		facts: []*lp.Fact{
			{Type: lp.FactCertificationIssued, RefID: "cert-1",
				Detail: lp.Detail{Label: "Golang Fundamentals Certificate", Score: &score}, OccurredAt: now.Add(-48 * time.Hour)},
			{Type: lp.FactCourseCompleted, RefID: "course-old",
				Detail: lp.Detail{Label: "Ancient History 101"}, OccurredAt: now.AddDate(0, -2, 0)},
		},
		activity: []*lp.ActivityEntry{
			{Kind: "atom_session", Summary: "Answered 5 recursion atoms", OccurredAt: now.Add(-24 * time.Hour)},
		},
	}
}

// ----------------------------------------------------------------------------
// Auth / routing / validation gates
// ----------------------------------------------------------------------------

func TestInvokeSkill_RequiresAuth(t *testing.T) {
	srv := NewServer()
	w := plainReq(t, srv, http.MethodPost, "/v1/me/companions/some-id/skills/progress_mirror/invoke", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

func TestInvokeSkill_MethodNotAllowed(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body=%s", w.Code, w.Body.String())
	}
}

func TestInvokeSkill_UnknownCompanion(t *testing.T) {
	srv, _ := seedInvokeServer(t, 2, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodPost,
		"/v1/me/companions/01970000-dead-7000-8000-000000000009/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestInvokeSkill_UnknownKeyRejected(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/skill.tree.hack/invoke",
		map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_NOT_IN_CATALOGUE" {
		t.Errorf("code = %q, want SKILL_NOT_IN_CATALOGUE", code)
	}
}

func TestInvokeSkill_CraftSkillNotInvokable(t *testing.T) {
	srv, id := seedInvokeServer(t, 5, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/long_weaving/invoke",
		map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_NOT_INVOKABLE" {
		t.Errorf("code = %q, want SKILL_NOT_INVOKABLE", code)
	}
}

func TestInvokeSkill_UnsupportedActiveSkill(t *testing.T) {
	// worked_example is a valid ACTIVE catalogue row but this build has no
	// invoke runner for it — fail loud 501, never a silent generic turn.
	// (quiz_me + socratic_drill gained builders in CHO-2016; worked_example
	// remains an un-built active Scholar skill.)
	srv, id := seedInvokeServer(t, 3, &fakeChatEngine{})
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/worked_example/invoke",
		map[string]any{})
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_INVOKE_UNSUPPORTED" {
		t.Errorf("code = %q, want SKILL_INVOKE_UNSUPPORTED", code)
	}
}

func TestInvokeSkill_NotOwned(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	activateSkill(t, srv, "progress_mirror")
	srv.LearnerProfiles = &fakeProfileReader{}
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_NOT_OWNED" {
		t.Errorf("code = %q, want SKILL_NOT_OWNED", code)
	}
}

func TestInvokeSkill_NotEquipped(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	activateSkill(t, srv, "progress_mirror")
	srv.LearnerProfiles = &fakeProfileReader{}
	// Mint WITHOUT equipping.
	if w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills",
		skillGrantReq{SkillKey: "progress_mirror"}); w.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_NOT_EQUIPPED" {
		t.Errorf("code = %q, want SKILL_NOT_EQUIPPED", code)
	}
}

func TestInvokeSkill_NotActiveAfterRollback(t *testing.T) {
	// Equip while active, then the release gate rolls back (SetActive false):
	// invoking must 409 SKILL_NOT_ACTIVE (ADR-174 — effect requires active).
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	srv.LearnerProfiles = &fakeProfileReader{}
	equipForInvoke(t, srv, id, "progress_mirror")
	activateSkillOff(t, srv, "progress_mirror")
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_NOT_ACTIVE" {
		t.Errorf("code = %q, want SKILL_NOT_ACTIVE", code)
	}
}

func TestInvokeSkill_StageLocked(t *testing.T) {
	// Equip a st2 Skill (explain_anew, min_growth_stage=2) at stage 2 (legal),
	// then the growth read reports stage 1 (e.g. an admin-granted skill on an
	// under-stage companion): invoke must 409 — min_growth_stage is an
	// invoke-time gate, not just an unlock gate. NB: progress_mirror can no
	// longer stand in here — ADR-228 D3 lowered it to st1, so it is legal at
	// stage 1; explain_anew keeps a genuine st2 floor to exercise the gate.
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	srv.LearnerProfiles = &fakeProfileReader{}
	equipForInvoke(t, srv, id, "explain_anew")
	srv.Growth = &fakeGrowth{getFn: func(_, companionID, _ string) (*growth.State, error) {
		return &growth.State{CompanionID: companionID, GrowthStage: 1}, nil
	}}
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/explain_anew/invoke",
		map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_STAGE_LOCKED" {
		t.Errorf("code = %q, want SKILL_STAGE_LOCKED", code)
	}
}

func TestInvokeSkill_InsufficientMana(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("nope")}
	srv, id := seedInvokeServer(t, 2, engine)
	srv.ConceptNodes = &fakeConceptReader{}
	equipForInvoke(t, srv, id, "explain_anew") // costs 10
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 3}}
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/explain_anew/invoke",
		map[string]any{"params": map[string]string{"target": "01970000-aaaa-7000-8000-00000000c0de"}})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "insufficient_mana" {
		t.Errorf("code = %q, want insufficient_mana", code)
	}
	if engine.calls != 0 {
		t.Errorf("engine called %d times on a 402; want 0", engine.calls)
	}
}

func TestInvokeSkill_EngineNotConfigured(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	srv.LearnerProfiles = &fakeProfileReader{}
	equipForInvoke(t, srv, id, "progress_mirror")
	srv.CompanionEngine = nil
	srv.CompanionEngineResource = ""
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "ENGINE_NOT_CONFIGURED" {
		t.Errorf("code = %q, want ENGINE_NOT_CONFIGURED", code)
	}
}

func TestInvokeSkill_ProgressMirror_ReadPortNotWired(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	equipForInvoke(t, srv, id, "progress_mirror")
	srv.LearnerProfiles = nil // profile projection not wired → fail loud
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_INVOKE_NOT_WIRED" {
		t.Errorf("code = %q, want SKILL_INVOKE_NOT_WIRED", code)
	}
}

// ----------------------------------------------------------------------------
// progress_mirror — sink=chat, price 0, context = ADR-200 verified profile
// ----------------------------------------------------------------------------

func TestInvokeSkill_ProgressMirror_HappyPath(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("You earned your Golang certificate this week — brilliant work.")}
	srv, id := seedInvokeServer(t, 2, engine)
	srv.LearnerProfiles = seedProfileFixtures(time.Now().UTC())
	// Free skill: an EMPTY wallet must not block a 0-cost invoke.
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 0}}
	// Config resolver present → its JSON must ride the engine request.
	srv.CompanionConfigJSONResolver = func(_ context.Context, _, _, _ string) (string, error) {
		return `{"cfg":true}`, nil
	}
	// Observe that NO memory row is recorded for a chat-sink skill.
	mem := &fakeCompanionMemory{}
	srv.CompanionMemory = mem
	srv.Embedder = &fakeEmbedder{vec: []float32{0.1}}
	equipForInvoke(t, srv, id, "progress_mirror")

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "progress_mirror", nil))

	if resp["skill_key"] != "progress_mirror" {
		t.Errorf("skill_key = %v", resp["skill_key"])
	}
	if reply, _ := resp["reply"].(string); !strings.Contains(reply, "Golang certificate") {
		t.Errorf("reply = %q; want the engine reply", reply)
	}
	if resp["recorded"] != false {
		t.Errorf("recorded = %v, want false for a chat-sink skill", resp["recorded"])
	}
	if resp["mana_charged"].(float64) != 0 {
		t.Errorf("mana_charged = %v, want 0", resp["mana_charged"])
	}
	if turnID, _ := resp["turn_id"].(string); turnID == "" {
		t.Error("turn_id empty")
	}
	if len(mem.recorded) != 0 {
		t.Errorf("memory rows recorded = %d, want 0", len(mem.recorded))
	}

	// The single agent turn carries the skill frame + injected profile context.
	msg := engine.gotReq.Message
	for _, want := range []string{
		"[SKILL INVOCATION: progress_mirror",
		"window=all",
		"Golang Fundamentals Certificate",
		"Answered 5 recursion atoms",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("engine message missing %q; msg=%q", want, msg)
		}
	}
	if engine.gotReq.ManaActionCode != "companion_skill_progress_mirror" {
		t.Errorf("ManaActionCode = %q, want companion_skill_progress_mirror", engine.gotReq.ManaActionCode)
	}
	if engine.gotReq.CompanionConfigJSON != `{"cfg":true}` {
		t.Errorf("CompanionConfigJSON = %q; want the resolver output", engine.gotReq.CompanionConfigJSON)
	}
	if engine.gotReq.CompanionID != id || engine.gotReq.TenantID == "" || engine.gotReq.UserGCID == "" {
		t.Errorf("engine identity fields incomplete: %+v", engine.gotReq)
	}
}

// TestInvokeSkill_ProgressMirror_NeverLeaksRawUUID reproduces the sighted leak
// (a companion narrating "a course identified as '05000000-…'"): enrollment/path
// facts carry only a raw course_id, a cert fact's label IS a course UUID, and a
// legacy activity summary baked the ref UUID into free text. The composed agent
// turn must contain NO raw UUID — un-named refs read as a generic noun instead.
func TestInvokeSkill_ProgressMirror_NeverLeaksRawUUID(t *testing.T) {
	const courseUUID = "05000000-0000-7000-8000-0000000c5301"
	const certUUID = "05000000-0000-7000-8000-0000000ce770"
	now := time.Now().UTC()
	engine := &fakeChatEngine{frames: chatFramesWithReply("Here is your honest reflection.")}
	srv, id := seedInvokeServer(t, 2, engine)
	srv.LearnerProfiles = &fakeProfileReader{
		facts: []*lp.Fact{
			{Type: lp.FactEnrollment, RefID: courseUUID, OccurredAt: now.Add(-72 * time.Hour)},
			{Type: lp.FactCertificationIssued, RefID: certUUID,
				Detail: lp.Detail{Label: courseUUID}, OccurredAt: now.Add(-48 * time.Hour)},
		},
		activity: []*lp.ActivityEntry{
			{Kind: "enrolled", Summary: "Enrolled in course " + courseUUID,
				RefID: courseUUID, OccurredAt: now.Add(-72 * time.Hour)},
		},
	}
	equipForInvoke(t, srv, id, "progress_mirror")

	_ = invokeSkill(t, srv, id, "progress_mirror", nil) // fatals on non-200
	msg := engine.gotReq.Message
	if uuidRE.MatchString(msg) {
		t.Errorf("progress_mirror turn leaked a raw UUID:\n%s", msg)
	}
	if !strings.Contains(msg, "a course") {
		t.Errorf("expected a generic 'a course' noun for the un-named refs; msg=%q", msg)
	}
}

var uuidRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func TestInvokeSkill_ProgressMirror_WindowFiltersFacts(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("ok")}
	srv, id := seedInvokeServer(t, 2, engine)
	srv.LearnerProfiles = seedProfileFixtures(time.Now().UTC())
	equipForInvoke(t, srv, id, "progress_mirror")

	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "progress_mirror", map[string]string{"window": "week"}))

	msg := engine.gotReq.Message
	if !strings.Contains(msg, "window=week") {
		t.Errorf("message missing window=week param echo; msg=%q", msg)
	}
	if !strings.Contains(msg, "Golang Fundamentals Certificate") {
		t.Errorf("recent fact missing from week window; msg=%q", msg)
	}
	if strings.Contains(msg, "Ancient History 101") {
		t.Errorf("2-month-old fact leaked into week window; msg=%q", msg)
	}
}

func TestInvokeSkill_ProgressMirror_BadWindow(t *testing.T) {
	srv, id := seedInvokeServer(t, 2, &fakeChatEngine{})
	srv.LearnerProfiles = &fakeProfileReader{}
	equipForInvoke(t, srv, id, "progress_mirror")
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{"params": map[string]string{"window": "decade"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "INVALID_SKILL_PARAMS" {
		t.Errorf("code = %q, want INVALID_SKILL_PARAMS", code)
	}
}

// ----------------------------------------------------------------------------
// recap_scribe — sink=memory_note, price 5, context = recent chat_turn rows
// ----------------------------------------------------------------------------

func recapScribeServer(t *testing.T, engine *fakeChatEngine, rows []companionmind.EpisodicMemory) (*Server, string, *fakeCompanionMemory, *fakeEmbedder) {
	t.Helper()
	srv, id := seedInvokeServer(t, 2, engine)
	srv.CompanionMemoryView = &fakeMemoryView{rows: rows}
	mem := &fakeCompanionMemory{}
	emb := &fakeEmbedder{vec: []float32{0.5, 0.25}}
	srv.CompanionMemory = mem
	srv.Embedder = emb
	srv.CompanionEmbeddingModelID = "text-embedding-test"
	equipForInvoke(t, srv, id, "recap_scribe")
	return srv, id, mem, emb
}

// CHO-2060 leak #2: the recap prompt renders each session turn as
// "- (2006-01-02 15:04) <content>" — a raw clock timestamp plus unsanitized
// content that can carry a baked-in ref UUID. Both reach the companion prompt
// whose reply the learner sees, so the clock must drop to date-only and the
// content must be sanitized.
func TestInvokeSkill_RecapScribe_NoUUIDorClockLeakInPrompt(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("ok")}
	rows := []companionmind.EpisodicMemory{
		{ID: "m0", MemoryType: "chat_turn",
			Content:   "Learner: explain atom 01890a5d-ac96-774b-bcce-b302099a8057\nCompanion: sure",
			CreatedAt: time.Date(2026, 7, 8, 14, 23, 0, 0, time.UTC)},
	}
	srv, id, _, _ := recapScribeServer(t, engine, rows)
	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "recap_scribe", nil))

	msg := engine.gotReq.Message
	if uuidRE.MatchString(msg) {
		t.Errorf("recap prompt leaked a raw UUID:\n%s", msg)
	}
	if strings.Contains(msg, "14:23") {
		t.Errorf("recap prompt leaked a raw clock timestamp:\n%s", msg)
	}
	// The date still anchors the entry (date-only is the learner-safe form).
	if !strings.Contains(msg, "2026-07-08") {
		t.Errorf("recap prompt dropped the date entirely:\n%s", msg)
	}
}

func TestInvokeSkill_RecapScribe_RecordsRecapNote(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("Today we drilled recursion base cases and you nailed 4 of 5.")}
	rows := []companionmind.EpisodicMemory{
		{ID: "m2", MemoryType: "chat_turn", Content: "Learner: what is a base case?\nCompanion: the stopping condition", CreatedAt: time.Now().Add(-10 * time.Minute)},
		{ID: "m1", MemoryType: "recap", Content: "Yesterday's recap — ignore me", CreatedAt: time.Now().Add(-26 * time.Hour)},
		{ID: "m0", MemoryType: "chat_turn", Content: "Learner: drill me on recursion\nCompanion: here we go", CreatedAt: time.Now().Add(-20 * time.Minute)},
	}
	srv, id, mem, emb := recapScribeServer(t, engine, rows)

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "recap_scribe", nil))

	if resp["recorded"] != true {
		t.Fatalf("recorded = %v, want true", resp["recorded"])
	}
	if resp["mana_charged"].(float64) != 5 {
		t.Errorf("mana_charged = %v, want 5", resp["mana_charged"])
	}
	if engine.gotReq.ManaActionCode != "companion_skill_recap_scribe" {
		t.Errorf("ManaActionCode = %q", engine.gotReq.ManaActionCode)
	}
	// Context injection: chat_turn rows only (a prior recap must NOT feed
	// the new recap — session facts only per the spec sheet).
	msg := engine.gotReq.Message
	if !strings.Contains(msg, "base case") || !strings.Contains(msg, "drill me on recursion") {
		t.Errorf("session rows missing from message: %q", msg)
	}
	if strings.Contains(msg, "Yesterday's recap") {
		t.Errorf("non-chat_turn row leaked into recap context: %q", msg)
	}
	// Sink write: the persisted note IS the agent's recap reply.
	if len(mem.recorded) != 1 {
		t.Fatalf("recorded %d memory rows, want 1", len(mem.recorded))
	}
	rec := mem.recorded[0]
	if rec.MemoryType != "recap" {
		t.Errorf("MemoryType = %q, want recap", rec.MemoryType)
	}
	if !strings.Contains(rec.ContentText, "recursion base cases") {
		t.Errorf("ContentText = %q; want the reply text", rec.ContentText)
	}
	if rec.ModelID != "text-embedding-test" {
		t.Errorf("ModelID = %q", rec.ModelID)
	}
	if rec.SourceTurnID == "" || rec.SourceTurnID != resp["turn_id"].(string) {
		t.Errorf("SourceTurnID = %q, want the response turn_id %v", rec.SourceTurnID, resp["turn_id"])
	}
	if emb.calls != 1 || emb.gotInputs[0].TaskType != companion.EmbedTaskDocument {
		t.Errorf("embedder calls=%d inputs=%+v; want 1 document-task embed", emb.calls, emb.gotInputs)
	}
}

func TestInvokeSkill_RecapScribe_EmptySessionSkipsRecord(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("We haven't studied together yet — shall we start?")}
	srv, id, mem, _ := recapScribeServer(t, engine, nil)

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "recap_scribe", nil))

	if resp["recorded"] != false {
		t.Errorf("recorded = %v, want false with no session facts", resp["recorded"])
	}
	if len(mem.recorded) != 0 {
		t.Errorf("recorded %d rows, want 0 — an empty session must not mint a recap note", len(mem.recorded))
	}
}

func TestInvokeSkill_RecapScribe_RecordFailureIsLoud(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("recap text")}
	rows := []companionmind.EpisodicMemory{
		{ID: "m1", MemoryType: "chat_turn", Content: "Learner: hello", CreatedAt: time.Now()},
	}
	srv, id, mem, _ := recapScribeServer(t, engine, rows)
	mem.recordErr = context.DeadlineExceeded

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/recap_scribe/invoke",
		map[string]any{})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — the note IS the point (fail-loud), body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "RECAP_PERSIST_FAILED" {
		t.Errorf("code = %q, want RECAP_PERSIST_FAILED", code)
	}
}

func TestInvokeSkill_RecapScribe_BadSessionParam(t *testing.T) {
	srv, id, _, _ := recapScribeServer(t, &fakeChatEngine{}, nil)
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/recap_scribe/invoke",
		map[string]any{"params": map[string]string{"session": "next_week"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "INVALID_SKILL_PARAMS" {
		t.Errorf("code = %q, want INVALID_SKILL_PARAMS", code)
	}
}

// ----------------------------------------------------------------------------
// explain_anew — sink=chat, price 10, context = target concept/atom
// ----------------------------------------------------------------------------

const (
	testConceptID = "01970000-cccc-7000-8000-000000000001"
	testAtomID    = "01970000-aaaa-7000-8000-000000000002"
)

func explainAnewServer(t *testing.T, engine *fakeChatEngine, withConcept bool) (*Server, string) {
	t.Helper()
	srv, id := seedInvokeServer(t, 2, engine)
	atoms := extinmem.NewAtomIndexRepo()
	atom, err := atom_index.New(atom_index.NewParams{
		AtomID: testAtomID, TenantID: testTenant, Title: "Recursion base cases",
		AtomType: "mcq", TopicTags: []string{"cs", "recursion"},
		Status: atom_index.StatusPublished, PublishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("atom fixture: %v", err)
	}
	if err := atoms.Save(context.Background(), atom); err != nil {
		t.Fatalf("atom save: %v", err)
	}
	srv.AtomIndex = atoms
	concepts := &fakeConceptReader{nodes: map[string]*conceptgraph.ConceptNode{}}
	if withConcept {
		concepts.nodes[testConceptID] = &conceptgraph.ConceptNode{
			ConceptID: testConceptID, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "Recursion", AtomRefs: []string{testAtomID},
		}
	}
	srv.ConceptNodes = concepts
	equipForInvoke(t, srv, id, "explain_anew")
	return srv, id
}

func TestInvokeSkill_ExplainAnew_ConceptTarget(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("Imagine a stack of plates — that's recursion.")}
	srv, id := explainAnewServer(t, engine, true)

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "explain_anew",
		map[string]string{"target": testConceptID}))

	if resp["mana_charged"].(float64) != 10 {
		t.Errorf("mana_charged = %v, want 10", resp["mana_charged"])
	}
	if engine.gotReq.ManaActionCode != "companion_skill_explain_anew" {
		t.Errorf("ManaActionCode = %q", engine.gotReq.ManaActionCode)
	}
	msg := engine.gotReq.Message
	for _, want := range []string{
		"[SKILL INVOCATION: explain_anew",
		"Recursion",            // the learner-named concept title
		"Recursion base cases", // the resolved atom title
		"style=analogy",        // default style echoed
		"length=short",         // default length echoed
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("engine message missing %q; msg=%q", want, msg)
		}
	}
	if engine.gotReq.MaxOutputTokens != 512 {
		t.Errorf("MaxOutputTokens = %d, want 512 for length=short", engine.gotReq.MaxOutputTokens)
	}
}

func TestInvokeSkill_ExplainAnew_AtomTargetFallback(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("Here is the atom, told as a story.")}
	srv, id := explainAnewServer(t, engine, false) // concept read misses → atom resolves

	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "explain_anew",
		map[string]string{"target": testAtomID, "style": "story", "length": "full"}))

	msg := engine.gotReq.Message
	if !strings.Contains(msg, "Recursion base cases") {
		t.Errorf("atom title missing from message: %q", msg)
	}
	if !strings.Contains(msg, "style=story") || !strings.Contains(msg, "length=full") {
		t.Errorf("explicit params not echoed: %q", msg)
	}
	if engine.gotReq.MaxOutputTokens != 1024 {
		t.Errorf("MaxOutputTokens = %d, want 1024 for length=full", engine.gotReq.MaxOutputTokens)
	}
}

func TestInvokeSkill_ExplainAnew_TargetNotFound(t *testing.T) {
	srv, id := explainAnewServer(t, &fakeChatEngine{}, false)
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/explain_anew/invoke",
		map[string]any{"params": map[string]string{"target": "01970000-dead-7000-8000-0000000000ff"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "TARGET_NOT_FOUND" {
		t.Errorf("code = %q, want TARGET_NOT_FOUND", code)
	}
}

func TestInvokeSkill_ExplainAnew_MissingTarget(t *testing.T) {
	srv, id := explainAnewServer(t, &fakeChatEngine{}, true)
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/explain_anew/invoke",
		map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "INVALID_SKILL_PARAMS" {
		t.Errorf("code = %q, want INVALID_SKILL_PARAMS", code)
	}
}

func TestInvokeSkill_ExplainAnew_MalformedTarget(t *testing.T) {
	// A non-UUID target must 400 at param validation — it must never reach
	// the pg concept repo (where a malformed uuid would surface as a 500).
	srv, id := explainAnewServer(t, &fakeChatEngine{}, true)
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/explain_anew/invoke",
		map[string]any{"params": map[string]string{"target": "recursion'; DROP TABLE atoms;--"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "INVALID_SKILL_PARAMS" {
		t.Errorf("code = %q, want INVALID_SKILL_PARAMS", code)
	}
}

func TestInvokeSkill_ExplainAnew_BadStyle(t *testing.T) {
	srv, id := explainAnewServer(t, &fakeChatEngine{}, true)
	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/explain_anew/invoke",
		map[string]any{"params": map[string]string{"target": testConceptID, "style": "interpretive_dance"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "INVALID_SKILL_PARAMS" {
		t.Errorf("code = %q, want INVALID_SKILL_PARAMS", code)
	}
}

// ----------------------------------------------------------------------------
// Engine failure surfaces
// ----------------------------------------------------------------------------

func TestInvokeSkill_EngineErrorFrameIsLoud(t *testing.T) {
	errData, _ := json.Marshal(map[string]any{"code": "ENGINE_STREAM_ABORTED", "message": "model exploded"})
	engine := &fakeChatEngine{frames: []clients.ChatStreamFrame{
		{Type: clients.ChatFrameError, Data: errData},
	}}
	srv, id := seedInvokeServer(t, 2, engine)
	srv.LearnerProfiles = &fakeProfileReader{}
	equipForInvoke(t, srv, id, "progress_mirror")

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_INVOKE_ENGINE_ERROR" {
		t.Errorf("code = %q, want SKILL_INVOKE_ENGINE_ERROR", code)
	}
}

func TestInvokeSkill_EngineEmptyReplyIsLoud(t *testing.T) {
	// A turn that streams no text is a failed Skill use — surface it, never
	// 200 an empty reply (fail-loud; mirrors the Greet empty-reply guard).
	open, _ := json.Marshal(map[string]any{"engine_session_id": "s"})
	done, _ := json.Marshal(map[string]any{"engine_session_id": "s", "finish_reason": "STOP"})
	engine := &fakeChatEngine{frames: []clients.ChatStreamFrame{
		{Type: clients.ChatFrameSessionOpen, Data: open},
		{Type: clients.ChatFrameTurnComplete, Data: done},
	}}
	srv, id := seedInvokeServer(t, 2, engine)
	srv.LearnerProfiles = &fakeProfileReader{}
	equipForInvoke(t, srv, id, "progress_mirror")

	w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/progress_mirror/invoke",
		map[string]any{})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 on empty reply, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "SKILL_INVOKE_EMPTY_REPLY" {
		t.Errorf("code = %q, want SKILL_INVOKE_EMPTY_REPLY", code)
	}
}
