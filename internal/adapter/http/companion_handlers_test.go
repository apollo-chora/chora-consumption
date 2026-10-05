package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// seedDailyDoseEngines satisfies the M14.iter5 fail-loud gate on
// /companion/daily-dose without performing real engine calls. Tests that
// hit the daily-dose endpoint MUST seed both resources + inject the
// fake engine ports or the handler will 503.
//
// 5.B: seeded resources only. 5.C: also inject fakeDailyDoseCompanion +
// fakeDailyDoseRecommender so the handler can complete its engine round
// trips without dialing the network. The fakes return canonical canned
// responses.
func seedDailyDoseEngines(srv *Server) {
	srv.CompanionEngineResource = "projects/test/locations/us-central1/reasoningEngines/test-companion"
	srv.RecommenderEngineResource = "projects/test/locations/us-central1/reasoningEngines/test-recommender"
	srv.CompanionEngine = &fakeDailyDoseCompanion{
		greeting: "Welcome back, Phyllis — today's dose awaits.",
		model:    "gemini-2.5-flash-lite",
	}
	srv.RecommenderEngine = &fakeDailyDoseRecommender{
		atomIDs: []string{"atom-rec-001", "atom-rec-002"},
		model:   "gemini-2.5-flash-lite",
	}
}

// fakeDailyDoseCompanion implements clients.CompanionEnginePort for tests.
type fakeDailyDoseCompanion struct {
	greeting string
	model    string
	err      error
	calls    int
	// lastReq captures the most recent Greet request so tests can pin
	// the BFF → engine wiring contract (CompanionID format, state fields).
	lastReq clients.CompanionGreetRequest
}

func (f *fakeDailyDoseCompanion) Greet(_ context.Context, req clients.CompanionGreetRequest) (clients.CompanionGreetResponse, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return clients.CompanionGreetResponse{}, f.err
	}
	return clients.CompanionGreetResponse{
		Greeting:     f.greeting,
		Model:        f.model,
		OutputTokens: 12,
		FinishReason: "STOP",
	}, nil
}

// StreamChat is the ADR-154 chat surface — daily-dose tests do not
// exercise it; return a closed channel so the port contract is satisfied
// without affecting any daily-dose assertion.
func (f *fakeDailyDoseCompanion) StreamChat(_ context.Context, _ clients.CompanionChatRequest) (<-chan clients.ChatStreamFrame, error) {
	ch := make(chan clients.ChatStreamFrame)
	close(ch)
	return ch, nil
}

// fakeDailyDoseRecommender implements clients.RecommenderEnginePort.
type fakeDailyDoseRecommender struct {
	atomIDs   []string
	narrative string
	model     string
	err       error
	calls     int
	// lastReq captures the most recent Recommend request so tests can pin the
	// BFF → engine wiring contract (TopicHint threading).
	lastReq clients.RecommendRequest
}

func (f *fakeDailyDoseRecommender) Recommend(_ context.Context, req clients.RecommendRequest) (clients.RecommendResponse, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return clients.RecommendResponse{}, f.err
	}
	return clients.RecommendResponse{
		AtomIDs:       f.atomIDs,
		NarrativeOnly: len(f.atomIDs) == 0,
		Narrative:     f.narrative,
		Model:         f.model,
		OutputTokens:  8,
		FinishReason:  "STOP",
	}, nil
}

// helper to issue an authed request against a fresh Server (so tests can
// observe the events publisher state).
func authedReq(t *testing.T, srv *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

// ----- /companion/me -----

func TestCompanionMe_Returns404WhenNotSummoned(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/companion/me", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (not summoned), body=%s", w.Code, w.Body.String())
	}
}

func TestCompanionMe_RequiresAuth(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/companion/me", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (missing context)", w.Code)
	}
}

// ----- /companion/summon -----

func TestCompanionSummon_HappyPath(t *testing.T) {
	srv := NewServer()
	body := map[string]any{
		"name":      "Eira",
		"archetype": "ScholarOwl",
		"stats": map[string]int{
			"curiosity": 1, "focus": 1, "recall": 1,
		},
	}
	w := authedReq(t, srv, http.MethodPost, "/companion/summon", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["archetype"] != "ScholarOwl" {
		t.Errorf("archetype = %v, want ScholarOwl", resp["archetype"])
	}
	if resp["name"] != "Eira" {
		t.Errorf("name = %v, want Eira", resp["name"])
	}
	if resp["level"].(float64) != 1 {
		t.Errorf("level = %v, want 1", resp["level"])
	}

	// /companion/me should now succeed.
	mw := authedReq(t, srv, http.MethodGet, "/companion/me", nil)
	if mw.Code != http.StatusOK {
		t.Errorf("/companion/me after summon = %d, want 200", mw.Code)
	}
}

func TestCompanionSummon_Returns409OnDoubleSummon(t *testing.T) {
	srv := NewServer()
	body := map[string]any{
		"name":      "Eira",
		"archetype": "ScholarOwl",
		"stats":     map[string]int{"curiosity": 3},
	}
	authedReq(t, srv, http.MethodPost, "/companion/summon", body)
	w := authedReq(t, srv, http.MethodPost, "/companion/summon", body)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already summoned)", w.Code)
	}
}

func TestCompanionSummon_Returns400OnInvalidArchetype(t *testing.T) {
	srv := NewServer()
	body := map[string]any{
		"name":      "Eira",
		"archetype": "EvilDragon",
		"stats":     map[string]int{"curiosity": 3},
	}
	w := authedReq(t, srv, http.MethodPost, "/companion/summon", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid archetype)", w.Code)
	}
}

func TestCompanionSummon_Returns400OnBadStatAllocation(t *testing.T) {
	srv := NewServer()
	body := map[string]any{
		"name":      "Eira",
		"archetype": "ScholarOwl",
		"stats":     map[string]int{"curiosity": 5}, // sum != 3
	}
	w := authedReq(t, srv, http.MethodPost, "/companion/summon", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (sum != 3)", w.Code)
	}
}

// ----- /companion/daily-dose -----

// TestDailyDose_DegradesGracefullyWhenEnginesNotConfigured asserts the F4
// graceful-degradation contract (Phyllis demo unblock, 2026-05-13): when
// neither engine resource is configured the handler MUST still serve 200
// with deterministic SM-2 + Ebbinghaus picks + templated greeting +
// templated narrative. The Degraded flag signals partial state.
//
// Supersedes the prior fail-loud-503 contract per
// docs/m13/HANDOFF_BE_F2_F4_PHYLLIS_BLOCKERS_2026-05-13.md §3.5.
func TestDailyDose_DegradesGracefullyWhenEnginesNotConfigured(t *testing.T) {
	srv := NewServer() // env empty in tests → both resources empty
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded), body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if !resp.Degraded {
		t.Error("expected Degraded=true when no engines configured")
	}
	if resp.CompanionQuote == "" {
		t.Error("expected templated companionQuote (greeting); got empty")
	}
	if resp.RecommenderNarrative == "" {
		t.Error("expected templated recommender narrative; got empty (AC-F4.3)")
	}
}

// TestDailyDose_Iter5C_GreetingDrivenByCompanionEngine asserts that the
// Greeting field comes from the CompanionEngine call (replacing the prior
// deterministic / GreetingPort path). Pins the M14.iter5.C contract.
func TestDailyDose_Iter5C_GreetingDrivenByCompanionEngine(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	// Replace the default fake with one carrying a distinct greeting.
	srv.CompanionEngine = &fakeDailyDoseCompanion{
		greeting: "Eira here — let's tackle today's dose together.",
		model:    "gemini-2.5-pro",
	}
	// Engine enrichment moved off the dose hot path to the async /ai endpoint
	// (the 3s synchronous budget always cancelled the real LLM call). The
	// greeting now comes from GET /companion/daily-dose/ai.
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseAIResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Greeting != "Eira here — let's tackle today's dose together." {
		t.Errorf("Greeting = %q; want engine-supplied", resp.Greeting)
	}
	if resp.EngineMetadata == nil || resp.EngineMetadata.CompanionModel != "gemini-2.5-pro" {
		t.Errorf("EngineMetadata.CompanionModel = %+v; want gemini-2.5-pro", resp.EngineMetadata)
	}
}

// TestDailyDose_Iter5C_AIPicksFromRecommenderToolCall asserts the
// ai_picks field is populated from RecommenderEngine.AtomIDs.
func TestDailyDose_Iter5C_AIPicksFromRecommenderToolCall(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.RecommenderEngine = &fakeDailyDoseRecommender{
		atomIDs: []string{"atom-x", "atom-y"},
		model:   "gemini-2.5-flash",
	}
	// AI picks now come from the async /ai endpoint (recommender runs to
	// completion there instead of being cancelled at the 3s hot-path budget).
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseAIResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.AIPicks) != 2 || resp.AIPicks[0] != "atom-x" || resp.AIPicks[1] != "atom-y" {
		t.Errorf("AIPicks = %v; want [atom-x atom-y]", resp.AIPicks)
	}
	if resp.Narrative != "" {
		t.Errorf("Narrative = %q; want empty when tool invoked", resp.Narrative)
	}
}

// TestDailyDose_Iter5C_RecommenderNarrativeOnlyDoesNot502 asserts the
// graceful-degradation contract: when the recommender returns text
// without invoking its tool (the Iter 3.5 prompt-tweak followup case),
// the daily-dose endpoint returns 200 with ai_picks empty +
// recommender_narrative populated.
func TestDailyDose_Iter5C_RecommenderNarrativeOnlyDoesNot502(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.RecommenderEngine = &fakeDailyDoseRecommender{
		atomIDs:   nil,
		narrative: "Could you tell me your top 3 favourite topics?",
	}
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseAIResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.AIPicks) != 0 {
		t.Errorf("AIPicks = %v; want empty when tool not invoked", resp.AIPicks)
	}
	if resp.Narrative != "Could you tell me your top 3 favourite topics?" {
		t.Errorf("Narrative = %q; want engine free-text", resp.Narrative)
	}
}

// TestDailyDose_CompanionEngineErrorDegradesGracefully asserts the F4
// graceful-degradation contract (per HANDOFF_BE_F2_F4_PHYLLIS_BLOCKERS
// §3.5): engine call failures fall back to deterministic templated
// greeting + 200 OK rather than 502. Supersedes the prior fail-loud-502
// behaviour for this surface.
func TestDailyDose_CompanionEngineErrorDegradesGracefully(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.CompanionEngine = &fakeDailyDoseCompanion{err: assertErr("engine boom")}
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded), body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseAIResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Degraded {
		t.Error("expected Degraded=true when companion engine errors")
	}
	if resp.Greeting == "" {
		t.Error("expected templated greeting when engine errors")
	}
}

// TestDailyDose_RecommenderEngineErrorDegradesGracefully — same
// graceful-degradation contract for the recommender path.
func TestDailyDose_RecommenderEngineErrorDegradesGracefully(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.RecommenderEngine = &fakeDailyDoseRecommender{err: assertErr("recommender boom")}
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded), body=%s", w.Code, w.Body.String())
	}
	var resp dailyDoseAIResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Degraded {
		t.Error("expected Degraded=true when recommender errors")
	}
	// Narrative stays empty on the /ai path when the recommender errors — the
	// FE keeps its already-rendered deterministic narrative from the dose. The
	// degraded flag is the contract signal.
}

// TestDailyDose_Iter5F_CompanionIDIsCanonicalUUID pins the BFF → engine
// dispatch contract surfaced by the M14.iter5F dress rehearsal (2026-05-13):
//
//   - The Companion engine's instancedispatch plugin looks up companion_id in
//     the skill registry. The deployed sandbox registry (POC) only knows
//     three canonical UUIDs (Newton / Curie / Lovelace stubs). Passing a
//     lowercase name like "newton" returns ErrCompanionNotFound, which the
//     plugin's BeforeRunCallback surfaces as a non-SSE error envelope. The
//     SSE parser sees no `data:` lines, lastFinal stays empty, and the
//     client 502s with "companion engine returned empty greeting" (correct
//     fail-loud, wrong root cause).
//
// This test asserts the BFF emits one of the three canonical UUIDs so the
// engine can resolve and execute. M14.iter6 will replace the static map
// with a chora_consumption.companion_instances projection.
func TestDailyDose_Iter5F_CompanionIDIsCanonicalUUID(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	fake := &fakeDailyDoseCompanion{
		greeting: "ok",
		model:    "gemini-2.5-flash-lite",
	}
	srv.CompanionEngine = fake

	// The Greet engine call now happens on the async /ai endpoint.
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("CompanionEngine.Greet calls = %d, want 1", fake.calls)
	}
	got := fake.lastReq.CompanionID
	// The POC stub registry keys are canonical UUIDv7 strings — never a
	// bare lowercase name. If this regresses to e.g. "newton" the deployed
	// engine refuses the turn (ErrCompanionNotFound).
	for _, banned := range []string{"newton", "curie", "lovelace", ""} {
		if got == banned {
			t.Fatalf("CompanionID = %q; bare names refuse at the engine (use canonical UUIDs from skillregistry stubs)", got)
		}
	}
	// All canonical stub UUIDs use the v7 "01957c8c-..." prefix per
	// services/chora-ai-kernel-orchestrator/agents/companion_chat_adk_go/internal/skillregistry/registry.go.
	if !strings.HasPrefix(got, "01957c8c-") {
		t.Errorf("CompanionID = %q; want UUID with stub registry prefix \"01957c8c-\"", got)
	}
}

// assertErr returns a plain error used to drive fail-loud test paths.
func assertErr(msg string) error { return &simpleErr{msg} }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// ---------- F4 (Phyllis demo unblock) — chora-web DailyDose shape -----------

// TestDailyDose_ResponseShapeMatchesFEDailyDoseModel pins the response JSON
// to the chora-web DailyDose interface
// (chora-web/src/app/features/surfaces/aplus/daily-dose/daily-dose.model.ts).
// The FE binds component fields straight off the response — any missing
// or differently-named field surfaces as an empty UI cell.
func TestDailyDose_ResponseShapeMatchesFEDailyDoseModel(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	// Parse into a loose map so we can verify camelCase keys are present
	// (vs the legacy snake_case shape).
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}

	required := []string{
		"doseId",
		"servedOn",
		"atoms",
		"composition",
		"totalXpAvailable",
		"nextDoseAt",
		"companionName",
		"companionLevel",
		"companionQuote",
	}
	for _, key := range required {
		if _, ok := resp[key]; !ok {
			t.Errorf("FE-required key %q missing from response; body=%s", key, w.Body.String())
		}
	}

	// Composition is an object with the 3 percent keys.
	composition, _ := resp["composition"].(map[string]any)
	for _, ck := range []string{"reviewPercent", "newPercent", "stretchPercent"} {
		if _, ok := composition[ck]; !ok {
			t.Errorf("composition.%s missing", ck)
		}
	}

	// Atoms array element shape: {atomId, courseCode, title, summary,
	// category, topic, xpOnComplete}.
	atoms, ok := resp["atoms"].([]any)
	if !ok || len(atoms) == 0 {
		t.Fatalf("atoms must be a non-empty array; got %v", resp["atoms"])
	}
	first, _ := atoms[0].(map[string]any)
	for _, ak := range []string{"atomId", "courseCode", "title", "summary", "category", "topic", "xpOnComplete"} {
		if _, ok := first[ak]; !ok {
			t.Errorf("atoms[0].%s missing", ak)
		}
	}
	// Category enum is restricted to "review" | "new" | "stretch".
	cat, _ := first["category"].(string)
	switch cat {
	case "review", "new", "stretch":
		// ok
	default:
		t.Errorf("atoms[0].category = %q; must be one of review|new|stretch", cat)
	}
}

func TestDailyDose_DoseIDIsDeterministicAcrossSameDayCalls(t *testing.T) {
	// Same gcid + same date = same doseId (so FE refresh doesn't bump it
	// + downstream subscribers can dedupe).
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w1 := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	w2 := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)

	var r1, r2 dailyDoseResp
	_ = json.Unmarshal(w1.Body.Bytes(), &r1)
	_ = json.Unmarshal(w2.Body.Bytes(), &r2)
	if r1.DoseID == "" || r1.DoseID != r2.DoseID {
		t.Errorf("DoseID should be deterministic across same-day calls; got %q then %q", r1.DoseID, r2.DoseID)
	}
}

func TestDailyDose_CompanionQuoteIsTemplatedOnHotPath(t *testing.T) {
	// New async contract: the dose hot path returns a TEMPLATED companionQuote
	// instantly (no synchronous engine call); the FE swaps in the real engine
	// greeting once GET /companion/daily-dose/ai resolves. companionQuote must be
	// non-empty (the speech bubble always renders) and Degraded must be false
	// (the dose itself is complete — AI enrichment is a separate async fetch).
	srv := NewServer()
	seedDailyDoseEngines(srv)
	srv.CompanionEngine = &fakeDailyDoseCompanion{
		greeting: "Today we'll explore something new together.",
		model:    "gemini-2.5-pro",
	}
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	var resp dailyDoseResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.CompanionQuote == "" {
		t.Error("companionQuote should be a non-empty templated greeting on the hot path")
	}
	if resp.Degraded {
		t.Error("Degraded should be false on the hot path (AI enrichment is async)")
	}
}

func TestDailyDose_CompositionPercentsSumWithinTolerance(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	var resp dailyDoseResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	sum := resp.Composition.ReviewPercent + resp.Composition.NewPercent + resp.Composition.StretchPercent
	// Integer truncation may lose 1-2 percentage points; floor at 98 to
	// allow that without forcing rounding.
	if sum > 100 || sum < 98 {
		t.Errorf("composition percents sum = %d; want 98..100 (got %+v)", sum, resp.Composition)
	}
}

func TestDailyDose_TotalXPAvailableNonNegativeAndConsistent(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	var resp dailyDoseResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	want := 0
	for _, a := range resp.Atoms {
		want += a.XPOnComplete
	}
	if resp.TotalXPAvailable != want {
		t.Errorf("totalXpAvailable = %d; want sum of atom XP = %d", resp.TotalXPAvailable, want)
	}
}

func TestDailyDose_NextDoseAtIsMidnightUTC(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	var resp dailyDoseResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.NextDoseAt, "T00:00:00") {
		t.Errorf("nextDoseAt should be midnight UTC; got %q", resp.NextDoseAt)
	}
}

func TestDailyDose_DegradesWhenOnlyCompanionConfigured(t *testing.T) {
	srv := NewServer()
	srv.CompanionEngineResource = "projects/test/locations/us-central1/reasoningEngines/x"
	// RecommenderEngineResource intentionally empty — half-configured
	// counts as "not fully available" and triggers graceful degradation.
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded)", w.Code)
	}
	var resp dailyDoseResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Degraded {
		t.Error("expected Degraded=true when one engine resource missing")
	}
}

func TestDailyDose_HappyPath(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	entries, _ := resp["entries"].([]any)
	if len(entries) != 5 {
		t.Errorf("entries length = %d, want 5 (Comic Ch6 P14 P4)", len(entries))
	}
	msg, _ := resp["message"].(string)
	// Comic Ch6 P14 P4 invariant.
	if !strings.Contains(msg, "Daily Dose is ready") {
		t.Errorf("message = %q, want to contain 'Daily Dose is ready'", msg)
	}
	if !strings.Contains(msg, "5 atoms") {
		t.Errorf("message = %q, want to contain '5 atoms'", msg)
	}
	if !strings.Contains(msg, "memory's fading") {
		t.Errorf("message = %q, want to contain 'memory's fading' (Comic Ch6 P14)", msg)
	}
}

func TestDailyDose_RequiresAuth(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/companion/daily-dose", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestDailyDose_PublishesEvent(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	pub := srv.Publisher.(*events.InMemoryPublisher)
	authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	emitted := pub.Events()
	count := 0
	for _, e := range emitted {
		if e.Topic == events.TopicDailyDoseServed {
			count++
		}
	}
	if count != 1 {
		t.Errorf("daily_dose.served events = %d, want 1", count)
	}
}

// fakeCoachPort captures the request + returns canned recommendations so
// the handler integration can be exercised without a live orchestrator.
type fakeCoachPort struct {
	calls []companion.ExamPrepCoachRequest
	resp  *companion.ExamPrepCoachRecommendation
}

func (f *fakeCoachPort) RecommendDose(_ context.Context, req companion.ExamPrepCoachRequest) (*companion.ExamPrepCoachRecommendation, error) {
	f.calls = append(f.calls, req)
	return f.resp, nil
}

// TestDailyDose_NoExamPrepGoal_NoCoachCall — when the learner has no
// active exam-prep goal, the handler must NOT call the coach and the
// vanilla 2-1-2 dose is returned (BE-EP1 behaviour preservation).
func TestDailyDose_NoExamPrepGoal_NoCoachCall(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	coach := &fakeCoachPort{
		resp: &companion.ExamPrepCoachRecommendation{Topics: []string{"velocity_estimation"}},
	}
	srv.ExamPrepCoach = coach

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := len(coach.calls); got != 0 {
		t.Errorf("coach calls = %d, want 0 (no goal)", got)
	}
}

// TestDailyDose_WithExamPrepGoal_CallsCoachAndTagsDrills — when the
// learner has an active goal AND the coach port is wired, the dose
// handler invokes the coach and the response includes at least one
// entry tagged DoseReasonExamPrepDrill (BE-EP1 happy path).
func TestDailyDose_WithExamPrepGoal_CallsCoachAndTagsDrills(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	coach := &fakeCoachPort{
		resp: &companion.ExamPrepCoachRecommendation{
			// Topics chosen to match the seeded AtomCatalogue fixture; the
			// canonical 5-topic catalogue includes these labels.
			Topics: []string{"agile-fundamentals"},
		},
	}
	srv.ExamPrepCoach = coach

	// Pick an existing topic from the seeded catalogue for the goal so
	// the swap can find a matching seed atom.
	seeds := srv.Atoms.Seeds()
	if len(seeds) == 0 {
		t.Fatal("seed catalogue empty")
	}
	topicForGoal := seeds[0].Topic
	coach.resp = &companion.ExamPrepCoachRecommendation{Topics: []string{topicForGoal}}

	srv.ExamPrepGoals.Save(testTenant, testGCID, &companion.ExamPrepGoal{
		LearnerGCID: testGCID,
		ExamID:      "scrum-master-cert",
		ExamDate:    time.Now().Add(14 * 24 * time.Hour),
		Retention: []companion.ExamPrepTopicRetention{
			{Topic: topicForGoal, Score: 0.10},
		},
	})

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := len(coach.calls); got != 1 {
		t.Fatalf("coach calls = %d, want 1", got)
	}
	if got := coach.calls[0].LearnerGCID; got != testGCID {
		t.Errorf("forwarded LearnerGCID = %q, want %q", got, testGCID)
	}
	if got := coach.calls[0].ExamID; got != "scrum-master-cert" {
		t.Errorf("forwarded ExamID = %q", got)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries, _ := resp["entries"].([]any)
	drillCount := 0
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if m["dose_reason"] == string(companion.DoseReasonExamPrepDrill) {
			drillCount++
		}
	}
	if drillCount < 1 {
		t.Errorf("dose drill entries = %d, want >= 1", drillCount)
	}
}

// ----- /atoms/{id}/feedback -----

func TestAtomFeedback_HappyPathIRemember(t *testing.T) {
	srv := NewServer()
	// Pick a known atom_id from the catalogue.
	seeds := srv.Atoms.Seeds()
	if len(seeds) == 0 {
		t.Fatal("no seed atoms")
	}
	atomID := seeds[0].AtomID
	body := map[string]any{"grade": "i_remember"}
	w := authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["grade"] != "i_remember" {
		t.Errorf("grade = %v, want i_remember", resp["grade"])
	}
	if resp["interval_days"].(float64) != 1 {
		t.Errorf("interval_days = %v, want 1 (first SM-2 repetition)", resp["interval_days"])
	}
	if resp["xp_earned"].(float64) <= 0 {
		t.Errorf("xp_earned = %v, want > 0", resp["xp_earned"])
	}
}

func TestAtomFeedback_ForgotResetsInterval(t *testing.T) {
	srv := NewServer()
	atomID := srv.Atoms.Seeds()[0].AtomID
	// First two i_remembers build up interval.
	authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", map[string]any{"grade": "i_remember"})
	authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", map[string]any{"grade": "i_remember"})
	// Then forgot should reset to 1.
	w := authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", map[string]any{"grade": "forgot"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["interval_days"].(float64) != 1 {
		t.Errorf("interval_days = %v, want 1 (forgot reset)", resp["interval_days"])
	}
}

func TestAtomFeedback_InvalidGrade(t *testing.T) {
	srv := NewServer()
	atomID := srv.Atoms.Seeds()[0].AtomID
	body := map[string]any{"grade": "bogus"}
	w := authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAtomFeedback_AtomNotFound(t *testing.T) {
	srv := NewServer()
	body := map[string]any{"grade": "i_remember"}
	w := authedReq(t, srv, http.MethodPost, "/atoms/01970000-0000-7000-fff0-deadbeefdead/feedback", body)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (unknown atom)", w.Code)
	}
}

func TestAtomFeedback_RequiresAuth(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/atoms/some/feedback", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAtomFeedback_PublishesEvent(t *testing.T) {
	srv := NewServer()
	pub := srv.Publisher.(*events.InMemoryPublisher)
	atomID := srv.Atoms.Seeds()[0].AtomID
	body := map[string]any{"grade": "i_remember"}
	authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", body)
	count := 0
	for _, e := range pub.Events() {
		if e.Topic == events.TopicAtomSessionCompleted {
			count++
		}
	}
	if count != 1 {
		t.Errorf("atom_session.completed events = %d, want 1", count)
	}
}

func TestAtomFeedback_LevelUpEmitsTraitNudge(t *testing.T) {
	srv := NewServer()
	atomID := srv.Atoms.Seeds()[0].AtomID
	// 4 i_remembers × 100 XP = 400 XP → level 2 trait "Quick Learner".
	var lastResp map[string]any
	for i := 0; i < 4; i++ {
		w := authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback",
			map[string]any{"grade": "i_remember"})
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: status=%d body=%s", i, w.Code, w.Body.String())
		}
		json.Unmarshal(w.Body.Bytes(), &lastResp)
	}
	leveled, _ := lastResp["leveled_up"].(bool)
	if !leveled {
		t.Errorf("expected level-up after 4×100 XP, resp=%v", lastResp)
	}
	trait, _ := lastResp["unlocked_trait"].(string)
	if trait != "Quick Learner" {
		t.Errorf("unlocked_trait = %q, want 'Quick Learner'", trait)
	}
	nudge, _ := lastResp["nudge_message"].(string)
	if !strings.Contains(nudge, "leveled up") {
		t.Errorf("nudge_message = %q, want to contain 'leveled up'", nudge)
	}
	if !strings.Contains(nudge, "Quick Learner") {
		t.Errorf("nudge_message = %q, want to contain trait name", nudge)
	}
}
