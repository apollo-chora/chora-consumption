// companion_chat_handler_test.go — ADR-154 conversational chat surface
// HTTP-handler tests.
//
// Endpoint: POST /v1/me/companions/{id}/chat
//
// SSE wire format per ADR-154 D1:
//
//	Content-Type: text/event-stream
//	Cache-Control: no-cache
//	X-Accel-Buffering: no
//
// Per-frame format: `event: <type>\ndata: <json>\n\n` with a flush
// after each write.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ----------------------------------------------------------------------------
// Fakes for the chat-handler dependencies (engine port + chat session repo).
// fakeManaQuoter is reused from companion_mana_test.go in the same package.
// ----------------------------------------------------------------------------

// fakeChatEngine implements clients.CompanionEnginePort. Only the StreamChat
// method matters for the chat tests; Greet is a stub.
type fakeChatEngine struct {
	// frames is the prepared stream emitted on every StreamChat call.
	frames []clients.ChatStreamFrame
	// errOnOpen makes StreamChat return a synchronous error before any
	// frames flow (engine resource not configured / CreateSession error).
	errOnOpen error
	// gotReq captures the last incoming request for assertions.
	gotReq clients.CompanionChatRequest
	calls  int
}

func (f *fakeChatEngine) Greet(_ context.Context, _ clients.CompanionGreetRequest) (clients.CompanionGreetResponse, error) {
	return clients.CompanionGreetResponse{}, nil
}

func (f *fakeChatEngine) StreamChat(_ context.Context, req clients.CompanionChatRequest) (<-chan clients.ChatStreamFrame, error) {
	f.calls++
	f.gotReq = req
	if f.errOnOpen != nil {
		return nil, f.errOnOpen
	}
	ch := make(chan clients.ChatStreamFrame, len(f.frames)+1)
	go func() {
		defer close(ch)
		for _, frame := range f.frames {
			ch <- frame
		}
	}()
	return ch, nil
}

// fakeChatRepo implements companion.ChatSessionRepository in-memory.
type fakeChatRepo struct {
	bySession map[string]companion.ChatSession
	byOwner   map[string]companion.ChatSession // key = owner_gcid|companion_id
	saves     []companion.ChatSession
	closed    []string
}

func newFakeChatRepo() *fakeChatRepo {
	return &fakeChatRepo{
		bySession: map[string]companion.ChatSession{},
		byOwner:   map[string]companion.ChatSession{},
	}
}

func (r *fakeChatRepo) Get(_ context.Context, gcid, companionID string) (*companion.ChatSession, error) {
	if s, ok := r.byOwner[gcid+"|"+companionID]; ok && s.IsOpen() {
		return &s, nil
	}
	return nil, companion.ErrChatSessionNotFound
}

func (r *fakeChatRepo) Save(_ context.Context, sess companion.ChatSession) error {
	r.saves = append(r.saves, sess)
	r.bySession[sess.ID] = sess
	r.byOwner[sess.OwnerGCID+"|"+sess.CompanionID] = sess
	return nil
}

func (r *fakeChatRepo) MarkClosed(_ context.Context, sessionID string) error {
	r.closed = append(r.closed, sessionID)
	if s, ok := r.bySession[sessionID]; ok {
		closed := s.Close(s.LastUsedAt)
		r.bySession[sessionID] = closed
		r.byOwner[s.OwnerGCID+"|"+s.CompanionID] = closed
	}
	return nil
}

// ----------------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------------

// authedChatReq is a POST helper that sets the same headers as authedReq
// but with a JSON body.
func authedChatReq(t *testing.T, srv *Server, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return authedReq(t, srv, http.MethodPost, target, body)
}

// canonical UUIDv7-ish IDs for tests. companionTestID matches one of the
// stubCompanionID* constants in companion_handlers.go so the engine state
// payload is canonical.
const companionTestID = "01957c8c-1111-7000-aaaa-1111aaaa1111"

// seedChatEngine wires the chat-handler deps onto a Server. Mirrors
// seedDailyDoseEngines for the chat surface.
func seedChatEngine(srv *Server, engine *fakeChatEngine, repo *fakeChatRepo) {
	srv.CompanionEngineResource = "projects/test/locations/us-central1/reasoningEngines/test-companion"
	srv.CompanionEngine = engine
	srv.CompanionChatSessions = repo
}

// parseSSE collects all frames from an SSE body into (type, data) pairs.
func parseSSE(body string) []struct{ Type, Data string } {
	var out []struct{ Type, Data string }
	var curType string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			curType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			out = append(out, struct{ Type, Data string }{curType, data})
			curType = ""
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// Happy path
// ----------------------------------------------------------------------------

func TestCompanionChat_HappyPath_StreamsAllFrameTypes(t *testing.T) {
	openPayload, _ := json.Marshal(map[string]any{
		"engine_session_id": "sess-1",
	})
	toolPayload, _ := json.Marshal(map[string]any{
		"tool": "atom_search",
		"args": map[string]any{"query": "forgetting curve"},
	})
	tokenPayload, _ := json.Marshal(map[string]any{"text": "Hi Phyllis,"})
	turnCompletePayload, _ := json.Marshal(map[string]any{
		"engine_session_id": "sess-1",
		"output_tokens":     42,
		"model":             "gemini-2.5-flash-lite",
		"finish_reason":     "STOP",
	})

	engine := &fakeChatEngine{
		frames: []clients.ChatStreamFrame{
			{Type: clients.ChatFrameSessionOpen, Data: openPayload},
			{Type: clients.ChatFrameToolCall, Data: toolPayload},
			{Type: clients.ChatFrameToken, Data: tokenPayload},
			{Type: clients.ChatFrameTurnComplete, Data: turnCompletePayload},
		},
	}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	// ADR-177: the handler no longer debits — it does a read-only balance
	// pre-check. Fund the wallet so the affordability gate passes and the
	// gateway (not this handler) does the per-turn debit.
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 1000}}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "What should I learn next?",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q; want text/event-stream", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q; want no-cache", got)
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q; want no", got)
	}

	frames := parseSSE(w.Body.String())
	if len(frames) < 4 {
		t.Fatalf("parsed %d SSE frames; want 4 (open + tool_call + token + turn_complete). body=%q",
			len(frames), w.Body.String())
	}
	if frames[0].Type != "session_open" {
		t.Errorf("frames[0].Type = %q; want session_open", frames[0].Type)
	}
	if !strings.Contains(frames[1].Type, "tool_call") {
		t.Errorf("frames[1].Type = %q; want tool_call", frames[1].Type)
	}
	if frames[2].Type != "token" {
		t.Errorf("frames[2].Type = %q; want token", frames[2].Type)
	}
	if frames[3].Type != "turn_complete" {
		t.Errorf("frames[3].Type = %q; want turn_complete", frames[3].Type)
	}

	// Engine MUST receive the user's message.
	if engine.gotReq.Message != "What should I learn next?" {
		t.Errorf("engine.Message = %q; want forwarded user message",
			engine.gotReq.Message)
	}

	// Session row MUST be saved with the engine_session_id from the
	// session_open frame.
	if len(repo.saves) == 0 {
		t.Fatal("no session row saved")
	}
	saved := repo.saves[len(repo.saves)-1]
	if saved.EngineSessionID != "sess-1" {
		t.Errorf("saved.EngineSessionID = %q; want sess-1", saved.EngineSessionID)
	}
	if saved.TurnCount != 1 {
		t.Errorf("saved.TurnCount = %d; want 1 (post first turn)", saved.TurnCount)
	}
	if saved.ManaChargedTotal != 5 {
		// basic tier = 5 mana
		t.Errorf("saved.ManaChargedTotal = %d; want 5", saved.ManaChargedTotal)
	}
}

// ----------------------------------------------------------------------------
// Insufficient mana → 402 verbatim envelope from learner-economy.yaml
// ----------------------------------------------------------------------------

func TestCompanionChat_InsufficientMana_Returns402WithUpsellEnvelope(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	// ADR-177: the 402 now comes from the read-only balance pre-check (no
	// debit). A wallet below the per-turn cost (basic = 5) trips the upsell.
	srv.ManaQuoter = &fakeManaQuoter{
		balance: companion.BalanceSnapshot{BalanceUnits: 0},
	}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q; want application/json (NOT SSE on 402)", got)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errMap, _ := body["error"].(map[string]any)
	if errMap == nil {
		t.Fatalf("body.error missing; body=%v", body)
	}
	if errMap["code"] != "insufficient_mana" {
		t.Errorf("error.code = %v; want insufficient_mana (verbatim from learner-economy.yaml)", errMap["code"])
	}
	upsell, _ := errMap["upsell"].(map[string]any)
	if upsell == nil {
		t.Fatalf("error.upsell missing; body=%v", body)
	}
	if got, _ := upsell["required_units"].(float64); int(got) != 5 {
		t.Errorf("upsell.required_units = %v; want 5", upsell["required_units"])
	}
	if got, _ := upsell["current_balance_units"].(float64); int(got) != 0 {
		t.Errorf("upsell.current_balance_units = %v; want 0", upsell["current_balance_units"])
	}

	// Engine MUST NOT have been called.
	if engine.calls != 0 {
		t.Errorf("engine.StreamChat called %d times; want 0 on 402", engine.calls)
	}
}

// ----------------------------------------------------------------------------
// Engine not configured → 503
// ----------------------------------------------------------------------------

func TestCompanionChat_EngineNotConfigured_Returns503(t *testing.T) {
	repo := newFakeChatRepo()
	srv := NewServer()
	srv.CompanionChatSessions = repo
	srv.ManaQuoter = &fakeManaQuoter{}
	// NO engine resource + no engine.

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	errMap, _ := body["error"].(map[string]any)
	if errMap == nil || errMap["code"] != "ENGINE_NOT_CONFIGURED" {
		t.Errorf("expected error.code=ENGINE_NOT_CONFIGURED, body=%v", body)
	}
}

// ----------------------------------------------------------------------------
// Method routing — only POST permitted
// ----------------------------------------------------------------------------

func TestCompanionChat_RejectsNonPOST(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.ManaQuoter = &fakeManaQuoter{}

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+companionTestID+"/chat", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ----------------------------------------------------------------------------
// Body validation — empty message → 400
// ----------------------------------------------------------------------------

func TestCompanionChat_EmptyMessage_Returns400(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.ManaQuoter = &fakeManaQuoter{}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ----------------------------------------------------------------------------
// Session reuse — second turn reads the existing session row and forwards
// its engine_session_id to StreamChat instead of creating a new one.
// ----------------------------------------------------------------------------

func TestCompanionChat_SecondTurn_ReusesEngineSessionID(t *testing.T) {
	// Seed an existing open session.
	existing, _ := companion.NewChatSession(companion.NewChatSessionInput{
		ID:              "existing-session-1",
		TenantID:        testTenant,
		OwnerGCID:       testGCID,
		CompanionID:     companionTestID,
		EngineSessionID: "vertex-EXISTING",
	})
	repo := newFakeChatRepo()
	_ = repo.Save(context.Background(), existing)

	openPayload, _ := json.Marshal(map[string]any{"engine_session_id": "vertex-EXISTING"})
	turnCompletePayload, _ := json.Marshal(map[string]any{
		"engine_session_id": "vertex-EXISTING",
		"output_tokens":     7,
		"model":             "gemini-2.5-flash-lite",
		"finish_reason":     "STOP",
	})
	engine := &fakeChatEngine{
		frames: []clients.ChatStreamFrame{
			{Type: clients.ChatFrameSessionOpen, Data: openPayload},
			{Type: clients.ChatFrameTurnComplete, Data: turnCompletePayload},
		},
	}
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 1000}}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "follow-up",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	if engine.gotReq.ExistingSessionID != "vertex-EXISTING" {
		t.Errorf("engine.ExistingSessionID = %q; want vertex-EXISTING (reuse path)",
			engine.gotReq.ExistingSessionID)
	}
}

// CHO-2060 leak #3: buildMemoryContextJSON renders each recalled memory into the
// companion's system prompt with a raw RFC3339 recorded_at (carries clock time)
// and unsanitized content that can hold a baked-in ref UUID. The system prompt
// drives a learner-facing reply, so the timestamp drops to date-only and the
// content is sanitized. (uuidRE is defined in the same package's invoke test.)
func TestBuildMemoryContextJSON_NoUUIDorClockLeak(t *testing.T) {
	rows := []companion.MemoryRow{
		{ID: "1", MemoryType: "chat_turn",
			ContentText: "We discussed atom 01890a5d-ac96-774b-bcce-b302099a8057 yesterday",
			CreatedAt:   time.Date(2026, 7, 8, 14, 23, 5, 0, time.UTC)},
	}
	out := buildMemoryContextJSON(rows)
	if out == "" {
		t.Fatal("buildMemoryContextJSON returned empty (marshal failed)")
	}
	if uuidRE.MatchString(out) {
		t.Errorf("memory context leaked a raw UUID into the system prompt:\n%s", out)
	}
	if strings.Contains(out, "14:23") {
		t.Errorf("memory context leaked a raw clock timestamp:\n%s", out)
	}
	// The date still anchors the memory (date-only is the learner-safe form).
	if !strings.Contains(out, "2026-07-08") {
		t.Errorf("memory context dropped the date entirely:\n%s", out)
	}
}
