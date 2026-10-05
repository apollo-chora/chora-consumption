// companion_chat_memory_test.go — F4 (ADR-173) per-Companion pgvector RAG
// memory wiring in the chat handler: recall-before-turn injection into the
// engine session state + record-after-turn, with graceful-degrade + soft-fail.
//
// Reuses fakeChatEngine / newFakeChatRepo / seedChatEngine / authedChatReq /
// fakeManaQuoter / companionTestID from companion_chat_handler_test.go.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// fakeEmbedder records inputs and returns a canned vector (or error).
type fakeEmbedder struct {
	vec       []float32
	err       error
	calls     int
	gotInputs []companion.EmbedInput
}

func (f *fakeEmbedder) Embed(_ context.Context, in companion.EmbedInput) ([]float32, error) {
	f.calls++
	f.gotInputs = append(f.gotInputs, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

// fakeCompanionMemory records recall queries + persisted memories.
type fakeCompanionMemory struct {
	recall    []companion.MemoryRow
	recallErr error
	recordErr error
	recalls   int
	recorded  []companion.RecordMemoryInput
}

func (f *fakeCompanionMemory) Recall(_ context.Context, _ string, _ []float32, _ int) ([]companion.MemoryRow, error) {
	f.recalls++
	if f.recallErr != nil {
		return nil, f.recallErr
	}
	return f.recall, nil
}

func (f *fakeCompanionMemory) Record(_ context.Context, in companion.RecordMemoryInput) error {
	f.recorded = append(f.recorded, in)
	return f.recordErr
}

// chatFramesWithReply is a minimal session_open → token → turn_complete stream.
func chatFramesWithReply(replyText string) []clients.ChatStreamFrame {
	open, _ := json.Marshal(map[string]any{"engine_session_id": "sess-mem"})
	tok, _ := json.Marshal(map[string]any{"text": replyText})
	done, _ := json.Marshal(map[string]any{
		"engine_session_id": "sess-mem", "model": "gemini-2.5-flash-lite", "finish_reason": "STOP",
	})
	return []clients.ChatStreamFrame{
		{Type: clients.ChatFrameSessionOpen, Data: open},
		{Type: clients.ChatFrameToken, Data: tok},
		{Type: clients.ChatFrameTurnComplete, Data: done},
	}
}

func newMemChatServer(t *testing.T, engine *fakeChatEngine) *Server {
	t.Helper()
	srv := NewServer()
	seedChatEngine(srv, engine, newFakeChatRepo())
	// ADR-177: read-only balance pre-check (no debit) — fund the wallet so the
	// affordability gate passes; the gateway does the per-turn debit.
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 1000}}
	return srv
}

// When BOTH Embedder + CompanionMemory are wired: the recalled memory is
// injected into the engine request, and the assembled turn is recorded.
func TestCompanionChat_Memory_RecallInjected_AndRecorded(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("Hello again!")}
	srv := newMemChatServer(t, engine)
	emb := &fakeEmbedder{vec: []float32{0.11, 0.22, 0.33}}
	mem := &fakeCompanionMemory{recall: []companion.MemoryRow{{
		ID:          "m1",
		MemoryType:  "chat_turn",
		ContentText: "Learner: I love astronomy\nCompanion: The stars await!",
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}}}
	srv.Embedder = emb
	srv.CompanionMemory = mem
	srv.CompanionEmbeddingModelID = "text-embedding-004"

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "what did I say I love?",
	})
	if w.Code != 200 {
		t.Fatalf("status = %d; want 200\nbody=%s", w.Code, w.Body.String())
	}

	// Recall injected into the engine session state (companion_memory key).
	if !strings.Contains(engine.gotReq.CompanionMemoryJSON, "astronomy") {
		t.Errorf("CompanionMemoryJSON = %q; want it to contain the recalled memory", engine.gotReq.CompanionMemoryJSON)
	}
	if mem.recalls != 1 {
		t.Errorf("Recall calls = %d; want 1", mem.recalls)
	}

	// Record called once with user message + assembled reply + correct metadata.
	if len(mem.recorded) != 1 {
		t.Fatalf("Record calls = %d; want 1", len(mem.recorded))
	}
	rec := mem.recorded[0]
	if !strings.Contains(rec.ContentText, "what did I say I love?") || !strings.Contains(rec.ContentText, "Hello again!") {
		t.Errorf("recorded ContentText = %q; want both message and reply", rec.ContentText)
	}
	if rec.ModelID != "text-embedding-004" {
		t.Errorf("recorded ModelID = %q; want text-embedding-004", rec.ModelID)
	}
	if rec.CompanionID != companionTestID {
		t.Errorf("recorded CompanionID = %q; want %q", rec.CompanionID, companionTestID)
	}
	if rec.MemoryType != "chat_turn" {
		t.Errorf("recorded MemoryType = %q; want chat_turn", rec.MemoryType)
	}

	// Embedder called twice: RETRIEVAL_QUERY for recall, RETRIEVAL_DOCUMENT for record.
	if emb.calls != 2 {
		t.Fatalf("Embed calls = %d; want 2 (recall + record)", emb.calls)
	}
	if emb.gotInputs[0].TaskType != companion.EmbedTaskQuery {
		t.Errorf("recall task type = %q; want RETRIEVAL_QUERY", emb.gotInputs[0].TaskType)
	}
	if emb.gotInputs[1].TaskType != companion.EmbedTaskDocument {
		t.Errorf("record task type = %q; want RETRIEVAL_DOCUMENT", emb.gotInputs[1].TaskType)
	}
}

// With Embedder nil (model resource unconfigured) memory is disabled: no
// injection, no record, chat unchanged.
func TestCompanionChat_Memory_DisabledWhenEmbedderNil(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("hi")}
	srv := newMemChatServer(t, engine)
	mem := &fakeCompanionMemory{recall: []companion.MemoryRow{{ContentText: "should not be used"}}}
	srv.CompanionMemory = mem
	// srv.Embedder intentionally nil.

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{"message": "hello"})
	if w.Code != 200 {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if engine.gotReq.CompanionMemoryJSON != "" {
		t.Errorf("CompanionMemoryJSON = %q; want empty (memory disabled)", engine.gotReq.CompanionMemoryJSON)
	}
	if mem.recalls != 0 || len(mem.recorded) != 0 {
		t.Errorf("recalls=%d records=%d; want 0/0 when embedder nil", mem.recalls, len(mem.recorded))
	}
}

// Embed errors are non-fatal: the turn still streams 200, nothing is injected
// or recorded.
func TestCompanionChat_Memory_EmbedError_SoftFails(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("hi")}
	srv := newMemChatServer(t, engine)
	srv.Embedder = &fakeEmbedder{err: errors.New("vertex unreachable")}
	mem := &fakeCompanionMemory{}
	srv.CompanionMemory = mem

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{"message": "hello"})
	if w.Code != 200 {
		t.Fatalf("status = %d; want 200 (embed error must not block the turn)", w.Code)
	}
	if engine.gotReq.CompanionMemoryJSON != "" {
		t.Errorf("CompanionMemoryJSON = %q; want empty on recall embed error", engine.gotReq.CompanionMemoryJSON)
	}
	if len(mem.recorded) != 0 {
		t.Errorf("Record calls = %d; want 0 when record embed errors", len(mem.recorded))
	}
}
