package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-common/tracing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// companion_turn_completed_handler_test.go - ADR-254 D4/D8: the pull consumer
// on chora-consumption.companion-turn-completed (and the dose twin) decodes the
// kennel's JSON result (envelope on the attributes), scopes the tenant from the
// envelope, and completes the stored turn exactly once. Contract pinned here:
//   - a well-formed result for a known accepted turn completes it (ACK);
//   - a second delivery of the same result is a no-op (ACK, idempotent);
//   - a result for an unknown turn id is logged and ACKed (it cannot be retried
//     into existence; the DLQ would only hide it);
//   - a malformed body / missing tenant is NACKed (returns error) so it
//     dead-letters loudly;
//   - a store failure is NACKed (retry).

type fakeTurnStore struct {
	mu        sync.Mutex
	turns     map[string]*companion.Turn
	completes []string
	failNext  error
	tenants   []string
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

func (f *fakeTurnStore) Get(ctx context.Context, tenantID, turnID string) (*companion.Turn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.turns[turnID]
	if !ok || t.TenantID != tenantID {
		return nil, companion.ErrTurnNotFound
	}
	cp := *t
	return &cp, nil
}

func (f *fakeTurnStore) Complete(ctx context.Context, tenantID, turnID string, res companion.TurnResult) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants = append(f.tenants, tracing.TenantIDFromContext(ctx))
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return false, err
	}
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
	f.completes = append(f.completes, turnID)
	return true, nil
}

func (f *fakeTurnStore) Timeout(ctx context.Context, tenantID, turnID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.turns[turnID]
	if !ok || t.TenantID != tenantID {
		return false, companion.ErrTurnNotFound
	}
	return t.Timeout(now), nil
}

func seedAcceptedTurn(t *testing.T, store *fakeTurnStore, tenant, turnID string) {
	t.Helper()
	turn, err := companion.NewTurn(companion.NewTurnInput{
		TurnID: turnID, TenantID: tenant, OwnerGCID: "22222222-2222-7222-8222-222222222222",
		CompanionID: "33333333-3333-7333-8333-333333333333", Kind: companion.TurnKindTyped,
		Lane: companion.TurnLaneCompanionTurn, Request: []byte(`{"message":"hi"}`),
		Now: time.Now(), Deadline: time.Minute,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Begin(context.Background(), turn, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("seed begin: %v", err)
	}
}

func completedMsg(tenant, body string) eventbus.Message {
	now := time.Now().UTC()
	return eventbus.Message{
		Subject: TopicCompanionTurnCompleted,
		Envelope: envelope.Envelope{
			EventID: "evt-1", IdempotencyKey: "turn-1", TenantID: tenant,
			GCID: "22222222-2222-7222-8222-222222222222", OccurredAt: now, PublishedAt: now,
			Traceparent:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			SourceProject: "chora-489812", SourceService: "chora-ai-kernel-orchestrator", SchemaVersion: 1,
		},
		Payload: []byte(body),
	}
}

const tenantA = "11111111-1111-7111-8111-111111111111"

func TestCompanionTurnCompletedHandler_CompletesKnownTurnOnce(t *testing.T) {
	store := newFakeTurnStore()
	seedAcceptedTurn(t, store, tenantA, "turn-1")
	h := NewCompanionTurnCompletedHandler(store, companion.TurnLaneCompanionTurn)

	body := `{"turn_id":"turn-1","status":"OK","workflow_id":"wf-1","generated_by_model_id":"gemini-2.5-flash","reply_text":"hello","completed_at":"2026-08-23T04:00:05Z"}`
	if err := h(context.Background(), completedMsg(tenantA, body)); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	got, _ := store.Get(context.Background(), tenantA, "turn-1")
	if got.Status != companion.TurnStatusCompleted || got.GeneratedByModelID != "gemini-2.5-flash" {
		t.Fatalf("turn not completed: %+v", got)
	}
	if len(store.tenants) != 1 || store.tenants[0] != tenantA {
		t.Fatalf("tenant must be scoped from the envelope on the store ctx, got %v", store.tenants)
	}
	// Redelivery: idempotent, ACK.
	if err := h(context.Background(), completedMsg(tenantA, body)); err != nil {
		t.Fatalf("redelivery must ACK, got %v", err)
	}
	if len(store.completes) != 1 {
		t.Fatalf("completed %d times, want 1", len(store.completes))
	}
}

func TestCompanionTurnCompletedHandler_UnknownTurnIsAckedLoudly(t *testing.T) {
	store := newFakeTurnStore()
	h := NewCompanionTurnCompletedHandler(store, companion.TurnLaneCompanionTurn)
	body := `{"turn_id":"ghost","status":"OK"}`
	if err := h(context.Background(), completedMsg(tenantA, body)); err != nil {
		t.Fatalf("unknown turn must ACK (cannot be retried into existence), got %v", err)
	}
}

func TestCompanionTurnCompletedHandler_MalformedBodyIsNacked(t *testing.T) {
	store := newFakeTurnStore()
	h := NewCompanionTurnCompletedHandler(store, companion.TurnLaneCompanionTurn)
	if err := h(context.Background(), completedMsg(tenantA, `{"status":"OK"}`)); err == nil {
		t.Fatal("a result without turn_id must NACK (dead-letter)")
	}
	if err := h(context.Background(), completedMsg(tenantA, `not json`)); err == nil {
		t.Fatal("malformed JSON must NACK")
	}
}

func TestCompanionTurnCompletedHandler_MissingTenantIsNacked(t *testing.T) {
	store := newFakeTurnStore()
	seedAcceptedTurn(t, store, tenantA, "turn-1")
	h := NewCompanionTurnCompletedHandler(store, companion.TurnLaneCompanionTurn)
	msg := completedMsg("", `{"turn_id":"turn-1","status":"OK"}`)
	if err := h(context.Background(), msg); err == nil {
		t.Fatal("a result with no tenant on the envelope nor the body must NACK")
	}
}

func TestCompanionTurnCompletedHandler_StoreErrorIsNacked(t *testing.T) {
	store := newFakeTurnStore()
	seedAcceptedTurn(t, store, tenantA, "turn-1")
	store.failNext = errors.New("pg down")
	h := NewCompanionTurnCompletedHandler(store, companion.TurnLaneCompanionTurn)
	if err := h(context.Background(), completedMsg(tenantA, `{"turn_id":"turn-1","status":"OK"}`)); err == nil {
		t.Fatal("a store failure must NACK so the delivery retries")
	}
}

func TestCompanionTurnCompletedHandler_DoseLaneKeysOnDoseRequestID(t *testing.T) {
	store := newFakeTurnStore()
	turn, _ := companion.NewTurn(companion.NewTurnInput{
		TurnID: "dose-1", TenantID: tenantA, OwnerGCID: "22222222-2222-7222-8222-222222222222",
		Kind: companion.TurnKindDose, Lane: companion.TurnLaneDoseRecommendation,
		Request: []byte(`{"dose_request_id":"dose-1"}`), Now: time.Now(), Deadline: time.Minute,
	})
	_ = store.Begin(context.Background(), turn, func(context.Context) error { return nil })
	h := NewCompanionTurnCompletedHandler(store, companion.TurnLaneDoseRecommendation)
	body := `{"dose_request_id":"dose-1","status":"OK","recommended_atom_ids":["a1"],"rationale":"r"}`
	if err := h(context.Background(), completedMsg(tenantA, body)); err != nil {
		t.Fatalf("dose completion: %v", err)
	}
	got, _ := store.Get(context.Background(), tenantA, "dose-1")
	if got.Status != companion.TurnStatusCompleted {
		t.Fatalf("dose turn not completed: %+v", got)
	}
}
