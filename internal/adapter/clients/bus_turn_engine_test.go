package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// bus_turn_engine_test.go - ADR-254 D4/D8: the BusTurnEngine is the ONLY way a
// Companion turn leaves consumption: it writes the turn row + the outbox request
// in one transaction (TurnStore.Begin), emits session_open + accepted the moment
// that is durable, then waits on the store for the kennel's result and emits
// turn_complete | error. It implements clients.CompanionEnginePort so every
// existing caller (SSE chat, skill invoke, ceremony edge scout, ritual step,
// dose greeting) rides the same lane unchanged.
//
// Pinned here: the BINDING payload keys (coordinator 2026-08-22 17:31Z; the
// kennel passes the body verbatim to the chat binary), the frame sequence, the
// replay of a retried turn_id from the store without a second publish, the
// attach-to-in-flight behaviour, the timeout reaper, the caller error-code map,
// and the fail-loud paths.

type fakeTurnStore struct {
	mu       sync.Mutex
	turns    map[string]*companion.Turn
	timeouts []string
}

func newFakeTurnStore() *fakeTurnStore { return &fakeTurnStore{turns: map[string]*companion.Turn{}} }

func (f *fakeTurnStore) Begin(ctx context.Context, turn *companion.Turn, publish func(ctx context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.turns[turn.TurnID]; ok {
		return companion.ErrTurnExists
	}
	if err := publish(ctx); err != nil {
		return err
	}
	cp := *turn
	f.turns[turn.TurnID] = &cp
	return nil
}

func (f *fakeTurnStore) Get(_ context.Context, tenantID, turnID string) (*companion.Turn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.turns[turnID]
	if !ok || t.TenantID != tenantID {
		return nil, companion.ErrTurnNotFound
	}
	cp := *t
	return &cp, nil
}

func (f *fakeTurnStore) Complete(_ context.Context, tenantID, turnID string, res companion.TurnResult) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.turns[turnID]
	if !ok || t.TenantID != tenantID {
		return false, companion.ErrTurnNotFound
	}
	if err := t.Complete(res); err != nil {
		if errors.Is(err, companion.ErrTurnAlreadyTerminal) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (f *fakeTurnStore) Timeout(_ context.Context, tenantID, turnID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.turns[turnID]
	if !ok || t.TenantID != tenantID {
		return false, companion.ErrTurnNotFound
	}
	f.timeouts = append(f.timeouts, turnID)
	return t.Timeout(now), nil
}

// waitForTurn blocks until the store holds turnID (the engine has begun it).
func (f *fakeTurnStore) waitForTurn(t *testing.T, turnID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		_, ok := f.turns[turnID]
		f.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("turn %s never reached the store", turnID)
}

type publishedRequest struct {
	topic   string
	env     events.Envelope
	payload map[string]any
}

type fakeTurnPublisher struct {
	mu    sync.Mutex
	calls []publishedRequest
	fail  error
}

func (p *fakeTurnPublisher) PublishInTx(_ context.Context, topic string, env events.Envelope, payload map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	p.calls = append(p.calls, publishedRequest{topic: topic, env: env, payload: payload})
	return nil
}

func (p *fakeTurnPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type fakePromptOverrides struct{}

func (fakePromptOverrides) ResolvePromptOverrides(context.Context, string) (PromptOverridesResolution, error) {
	return PromptOverridesResolution{OverridesJSON: `{"tone":"warm"}`, Version: "v3", Source: "registry"}, nil
}

const (
	btTenant    = "11111111-1111-7111-8111-111111111111"
	btGCID      = "22222222-2222-7222-8222-222222222222"
	btCompanion = "33333333-3333-7333-8333-333333333333"
	btConv      = "44444444-4444-7444-8444-444444444444"
	btTrace     = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
)

func newBusEngine(t *testing.T, store companion.TurnStore, pub TurnRequestPublisher, deadline time.Duration) *BusTurnEngine {
	t.Helper()
	eng, err := NewBusTurnEngine(BusTurnEngineConfig{
		Store:           store,
		Publish:         pub,
		PromptOverrides: fakePromptOverrides{},
		TurnDeadline:    deadline,
		PollInterval:    3 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBusTurnEngine: %v", err)
	}
	return eng
}

func companionConfigJSON(t *testing.T) string {
	t.Helper()
	cfg := &consumptionv1.CompanionInstanceConfig{
		CompanionId:          btCompanion,
		OwnerGcid:            btGCID,
		TenantId:             btTenant,
		Name:                 "Ember",
		GrowthStage:          3,
		Species:              "owl",
		AhaMomentActiveUntil: timestamppb.New(time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC)),
	}
	b, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatalf("protojson: %v", err)
	}
	return string(b)
}

func baseChatReq(t *testing.T, turnID string) CompanionChatRequest {
	t.Helper()
	return CompanionChatRequest{
		TenantID:            btTenant,
		UserGCID:            btGCID,
		CompanionID:         btCompanion,
		ManaTier:            "basic",
		Message:             "hi there",
		TurnID:              turnID,
		ConversationID:      btConv,
		Locale:              "en",
		CompanionConfigJSON: companionConfigJSON(t),
		CompanionMemoryJSON: `{"memories":[{"content":"m1"}]}`,
		LearnerWeaknessJSON: `{"edges":[{"concept_key":"fractions"}]}`,
		ManaActionCode:      "companion_chat_turn_basic",
		Traceparent:         btTrace,
	}
}

func collectFrames(t *testing.T, frames <-chan ChatStreamFrame, n int, within time.Duration) []ChatStreamFrame {
	t.Helper()
	var out []ChatStreamFrame
	timer := time.NewTimer(within)
	defer timer.Stop()
	for len(out) < n {
		select {
		case f, ok := <-frames:
			if !ok {
				return out
			}
			out = append(out, f)
		case <-timer.C:
			t.Fatalf("only %d of %d frames arrived within %s: %+v", len(out), n, within, frameTypes(out))
		}
	}
	return out
}

func frameTypes(fs []ChatStreamFrame) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Type)
	}
	return out
}

func decodeFrame(t *testing.T, f ChatStreamFrame) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(f.Data, &m); err != nil {
		t.Fatalf("frame %s data %q: %v", f.Type, f.Data, err)
	}
	return m
}

func TestBusTurnEngine_StreamChat_PublishesBindingPayloadThenAcceptedThenTurnComplete(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, 2*time.Second)

	frames, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-1"))
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	// The request is durable before StreamChat returns.
	if pub.count() != 1 {
		t.Fatalf("published %d requests, want 1", pub.count())
	}
	call := pub.calls[0]
	if call.topic != events.TopicCompanionTurnRequested {
		t.Fatalf("topic = %q", call.topic)
	}
	if call.env.IdempotencyKey != "t-1" || call.env.TenantID != btTenant || call.env.GCID != btGCID || call.env.Traceparent != btTrace {
		t.Fatalf("envelope = %+v", call.env)
	}
	p := call.payload
	want := map[string]any{
		"tenant_id":               btTenant,
		"gcid":                    btGCID,
		"familiar_id":             btCompanion,
		"conversation_id":         btConv,
		"turn_id":                 "t-1",
		"turn_kind":               "typed",
		"message":                 "hi there",
		"locale":                  "en",
		"mana_tier":               "basic",
		"mana_action_code":        "companion_chat_turn_basic",
		"species":                 "owl",
		"familiar_memory":         `{"memories":[{"content":"m1"}]}`,
		"learner_weakness":        `{"edges":[{"concept_key":"fractions"}]}`,
		"prompt_overrides_json":   `{"tone":"warm"}`,
		"resolved_prompt_version": "v3",
		"aha_moment_active_until": "2026-08-23T05:00:00Z",
	}
	for k, v := range want {
		if fmt.Sprint(p[k]) != fmt.Sprint(v) {
			t.Errorf("payload[%s] = %v, want %v", k, p[k], v)
		}
	}
	if p["familiar_config"] != companionConfigJSON(t) {
		t.Errorf("familiar_config must be the protojson passed in verbatim")
	}
	// growth_stage is a JSON NUMBER (the plugin refuses a string permanently).
	if _, isString := p["growth_stage"].(string); isString || fmt.Sprint(p["growth_stage"]) != "3" {
		t.Errorf("growth_stage = %#v, want numeric 3", p["growth_stage"])
	}
	if _, ok := p["visible_kg_neighbors"]; ok {
		t.Errorf("visible_kg_neighbors must be OMITTED (RETIRED R3-1, coordinator ruling)")
	}
	if _, ok := p["requested_at"]; !ok {
		t.Errorf("requested_at missing")
	}

	// session_open + accepted arrive immediately (no kennel yet).
	first := collectFrames(t, frames, 2, time.Second)
	if first[0].Type != ChatFrameSessionOpen || first[1].Type != ChatFrameAccepted {
		t.Fatalf("frame order = %v, want [session_open accepted]", frameTypes(first))
	}
	so := decodeFrame(t, first[0])
	if so["conversation_id"] != btConv || so["turn_id"] != "t-1" {
		t.Errorf("session_open = %v", so)
	}
	acc := decodeFrame(t, first[1])
	if acc["turn_id"] != "t-1" || acc["accepted_at"] == nil {
		t.Errorf("accepted = %v", acc)
	}

	// The kennel answers (any pod's consumer completes the stored turn).
	if _, err := store.Complete(context.Background(), btTenant, "t-1", companion.TurnResult{
		WireStatus: "OK", WorkflowID: "wf-1", GeneratedByModelID: "gemini-2.5-flash",
		ReplyText: "Hello, learner.", ToolCalls: []companion.TurnToolCall{{Name: "weakness.read", Summary: "3 edges"}},
		Grounding: []companion.TurnGrounding{{AtomID: "a1", Title: "T"}}, CompletedAt: time.Now(),
		Raw: json.RawMessage(`{"status":"OK"}`),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	rest := collectFrames(t, frames, 2, 2*time.Second)
	if rest[0].Type != ChatFrameToolCall || rest[1].Type != ChatFrameTurnComplete {
		t.Fatalf("frame order = %v, want [tool_call turn_complete]", frameTypes(rest))
	}
	tc := decodeFrame(t, rest[1])
	if tc["turn_id"] != "t-1" || tc["reply_text"] != "Hello, learner." || tc["generated_by_model_id"] != "gemini-2.5-flash" || tc["model"] != "gemini-2.5-flash" || tc["conversation_id"] != btConv {
		t.Errorf("turn_complete = %v", tc)
	}
	if tools, _ := tc["tool_calls"].([]any); len(tools) != 1 {
		t.Errorf("turn_complete tool_calls = %v", tc["tool_calls"])
	}
	if grounding, _ := tc["grounding"].([]any); len(grounding) != 1 {
		t.Errorf("turn_complete grounding = %v", tc["grounding"])
	}
	if _, open := <-frames; open {
		t.Fatal("channel must close after turn_complete")
	}
}

func TestBusTurnEngine_StreamChat_ReplaysStoredResultWithoutRepublishing(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, 2*time.Second)
	frames, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-2"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	collectFrames(t, frames, 2, time.Second)
	_, _ = store.Complete(context.Background(), btTenant, "t-2", companion.TurnResult{WireStatus: "OK", ReplyText: "again", CompletedAt: time.Now()})
	collectFrames(t, frames, 1, 2*time.Second)

	// Retry of the same turn_id: served from the store, nothing republished.
	again, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-2"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	got := collectFrames(t, again, 3, time.Second)
	if types := fmt.Sprint(frameTypes(got)); types != "[session_open accepted turn_complete]" {
		t.Fatalf("replay frames = %s", types)
	}
	if decodeFrame(t, got[2])["reply_text"] != "again" {
		t.Fatalf("replayed reply = %v", decodeFrame(t, got[2]))
	}
	if pub.count() != 1 {
		t.Fatalf("published %d requests, want 1 (no republish on replay)", pub.count())
	}
}

func TestBusTurnEngine_StreamChat_AttachesToInFlightTurn(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, 2*time.Second)
	f1, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-3"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	f2, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-3"))
	if err != nil {
		t.Fatalf("second (attach): %v", err)
	}
	if pub.count() != 1 {
		t.Fatalf("published %d, want 1", pub.count())
	}
	collectFrames(t, f1, 2, time.Second)
	collectFrames(t, f2, 2, time.Second)
	_, _ = store.Complete(context.Background(), btTenant, "t-3", companion.TurnResult{WireStatus: "OK", ReplyText: "both", CompletedAt: time.Now()})
	if decodeFrame(t, collectFrames(t, f1, 1, 2*time.Second)[0])["reply_text"] != "both" {
		t.Fatal("first waiter did not get the reply")
	}
	if decodeFrame(t, collectFrames(t, f2, 1, 2*time.Second)[0])["reply_text"] != "both" {
		t.Fatal("second waiter did not get the reply")
	}
}

func TestBusTurnEngine_StreamChat_TimeoutEmitsErrorAndReapsTurn(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, 40*time.Millisecond)
	frames, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-4"))
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	got := collectFrames(t, frames, 3, 2*time.Second)
	if got[2].Type != ChatFrameError {
		t.Fatalf("frames = %v, want error last", frameTypes(got))
	}
	e := decodeFrame(t, got[2])
	if e["code"] != companion.TurnErrCodeTimeout || e["turn_id"] != "t-4" {
		t.Fatalf("error frame = %v", e)
	}
	turn, _ := store.Get(context.Background(), btTenant, "t-4")
	if turn.Status != companion.TurnStatusTimeout {
		t.Fatalf("turn status = %q, want timeout", turn.Status)
	}
}

func TestBusTurnEngine_StreamChat_MapsKennelOutcomesToCallerCodes(t *testing.T) {
	cases := []struct {
		name     string
		res      companion.TurnResult
		wantCode string
	}{
		{"rejected suspended", companion.TurnResult{WireStatus: "REJECTED", ErrorCode: "companion_suspended"}, companion.TurnErrCodeSuspended},
		{"rejected cap", companion.TurnResult{WireStatus: "REJECTED", ErrorCode: "tenant_in_flight_cap"}, companion.TurnErrCodeRejected},
		{"failed", companion.TurnResult{WireStatus: "FAILED"}, companion.TurnErrCodeFailed},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeTurnStore()
			pub := &fakeTurnPublisher{}
			eng := newBusEngine(t, store, pub, 2*time.Second)
			id := fmt.Sprintf("t-5-%d", i)
			frames, err := eng.StreamChat(context.Background(), baseChatReq(t, id))
			if err != nil {
				t.Fatalf("StreamChat: %v", err)
			}
			collectFrames(t, frames, 2, time.Second)
			c.res.CompletedAt = time.Now()
			_, _ = store.Complete(context.Background(), btTenant, id, c.res)
			got := collectFrames(t, frames, 1, 2*time.Second)
			if got[0].Type != ChatFrameError {
				t.Fatalf("frame = %s, want error", got[0].Type)
			}
			if e := decodeFrame(t, got[0]); e["code"] != c.wantCode {
				t.Fatalf("code = %v, want %s", e["code"], c.wantCode)
			}
		})
	}
}

func TestBusTurnEngine_StreamChat_RefusalRidesTurnComplete(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, 2*time.Second)
	frames, _ := eng.StreamChat(context.Background(), baseChatReq(t, "t-6"))
	collectFrames(t, frames, 2, time.Second)
	_, _ = store.Complete(context.Background(), btTenant, "t-6", companion.TurnResult{WireStatus: "OK", RefusalReason: "policy", CompletedAt: time.Now()})
	got := collectFrames(t, frames, 1, 2*time.Second)
	tc := decodeFrame(t, got[0])
	if got[0].Type != ChatFrameTurnComplete || tc["refusal_reason"] != "policy" || tc["finish_reason"] != "refusal" {
		t.Fatalf("refusal frame = %s %v", got[0].Type, tc)
	}
}

func TestBusTurnEngine_StreamChat_FailsLoud(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, time.Second)

	bad := baseChatReq(t, "t-7")
	bad.Message = ""
	if _, err := eng.StreamChat(context.Background(), bad); !errors.Is(err, agentengine.ErrInvalidRequest) {
		t.Fatalf("empty message err = %v, want ErrInvalidRequest", err)
	}
	noConv := baseChatReq(t, "t-8")
	noConv.ConversationID = ""
	if _, err := eng.StreamChat(context.Background(), noConv); !errors.Is(err, agentengine.ErrInvalidRequest) {
		t.Fatalf("missing conversation_id err = %v, want ErrInvalidRequest (the agent needs it to persist the session)", err)
	}
	badCfg := baseChatReq(t, "t-9")
	badCfg.CompanionConfigJSON = "{not json"
	if _, err := eng.StreamChat(context.Background(), badCfg); err == nil {
		t.Fatal("an unparseable familiar_config must fail loud, not publish a turn the agent will refuse")
	}

	pub.fail = errors.New("outbox down")
	if _, err := eng.StreamChat(context.Background(), baseChatReq(t, "t-10")); err == nil {
		t.Fatal("a publish failure must surface (no accepted frame for an unqueued request)")
	}
	if _, err := store.Get(context.Background(), btTenant, "t-10"); !errors.Is(err, companion.ErrTurnNotFound) {
		t.Fatal("a failed publish must not leave a turn row behind")
	}
}

func TestBusTurnEngine_Greet_BlocksForTheReply(t *testing.T) {
	store := newFakeTurnStore()
	pub := &fakeTurnPublisher{}
	eng := newBusEngine(t, store, pub, 2*time.Second)
	go func() {
		store.waitForTurn(t, "")
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// complete whichever turn the greeting began
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			store.mu.Lock()
			var id string
			for k := range store.turns {
				id = k
			}
			store.mu.Unlock()
			if id != "" {
				_, _ = store.Complete(context.Background(), btTenant, id, companion.TurnResult{
					WireStatus: "OK", ReplyText: "Good morning!", GeneratedByModelID: "gemini-2.5-flash", CompletedAt: time.Now(),
				})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	resp, err := eng.Greet(context.Background(), CompanionGreetRequest{
		TenantID: btTenant, UserGCID: btGCID, CompanionID: btCompanion, ManaTier: "basic",
		CompanionConfigJSON: companionConfigJSON(t), Traceparent: btTrace,
	})
	<-done
	if err != nil {
		t.Fatalf("Greet: %v", err)
	}
	if resp.Greeting != "Good morning!" || resp.Model != "gemini-2.5-flash" {
		t.Fatalf("greet = %+v", resp)
	}
	if pub.count() != 1 || pub.calls[0].payload["turn_kind"] != "typed" || pub.calls[0].payload["message"] == "" {
		t.Fatalf("greeting request = %+v", pub.calls[0].payload)
	}
	if pub.calls[0].payload["conversation_id"] == "" {
		t.Fatal("a greeting still needs a conversation_id for the agent session")
	}
}
