// ritual_sink_router_test.go — CHO-2016 G5 item 1: the sink router dispatches
// to the registered sink writer; the chat sink is real; an unwired sink FAILS
// LOUD (no fake success).
package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type recordingSink struct {
	got companion.SinkWriteInput
	err error
}

func (s *recordingSink) Write(_ context.Context, in companion.SinkWriteInput) (string, error) {
	s.got = in
	if s.err != nil {
		return "", s.err
	}
	return "ref-1", nil
}

func TestSinkRouter_ChatIsWiredByDefault(t *testing.T) {
	router := NewRitualSinkRouter(nil)
	ref, err := router.WriteToSink(context.Background(), companion.SinkChat, companion.SinkWriteInput{RunID: "run-1", Outputs: []any{"x"}})
	if err != nil {
		t.Fatalf("chat sink must be wired by default: %v", err)
	}
	if ref != "chat:run-1" {
		t.Errorf("chat ref = %q, want chat:run-1", ref)
	}
}

func TestSinkRouter_DispatchesToRegisteredSink(t *testing.T) {
	rec := &recordingSink{}
	router := NewRitualSinkRouter(map[string]RitualSinkAdapter{companion.SinkQuestionBank: rec})
	ref, err := router.WriteToSink(context.Background(), companion.SinkQuestionBank, companion.SinkWriteInput{RunID: "run-9", Outputs: []any{"a", "b"}})
	if err != nil {
		t.Fatalf("WriteToSink: %v", err)
	}
	if ref != "ref-1" || rec.got.RunID != "run-9" || len(rec.got.Outputs) != 2 {
		t.Errorf("dispatch wrong: ref=%q got=%+v", ref, rec.got)
	}
}

func TestSinkRouter_UnwiredSinkFailsLoud(t *testing.T) {
	router := NewRitualSinkRouter(nil)
	if _, err := router.WriteToSink(context.Background(), companion.SinkNotification, companion.SinkWriteInput{RunID: "r"}); err == nil {
		t.Fatal("an unwired sink must FAIL LOUD (never a fake success — the run refunds + records failed)")
	}
}

func TestSinkRouter_WriteErrorSurfaces(t *testing.T) {
	rec := &recordingSink{err: errors.New("subsystem down")}
	router := NewRitualSinkRouter(map[string]RitualSinkAdapter{companion.SinkMemoryNote: rec})
	if _, err := router.WriteToSink(context.Background(), companion.SinkMemoryNote, companion.SinkWriteInput{RunID: "r"}); err == nil {
		t.Fatal("a sink write error must surface fail-loud")
	}
}

// TestRitualSinkRouter_IsSinkWired — N5: the router answers the publish gate
// from the SAME writer map it dispatches runs through, so the two can never
// disagree about what this deployment can write.
func TestRitualSinkRouter_IsSinkWired(t *testing.T) {
	// Boot passes nil writers, so this is the live production set.
	r := NewRitualSinkRouter(nil)

	if !r.IsSinkWired(companion.SinkChat) {
		t.Error("chat is registered by default and must read as wired")
	}
	for _, s := range []string{
		companion.SinkMemoryNote, companion.SinkSuggestionInbox, companion.SinkQuestionBank,
		companion.SinkNotification, companion.SinkCalendarArtifact,
	} {
		if r.IsSinkWired(s) {
			t.Errorf("sink %q has no writer registered at boot and must read as unwired", s)
		}
	}
	if r.IsSinkWired("webhook") {
		t.Error("a sink outside the closed list must never read as wired")
	}
}

// TestRitualSinkRouter_WiredSinksTracksRegistration — registering a writer is
// what makes a sink publishable; the gate is not a hardcoded allowlist.
func TestRitualSinkRouter_WiredSinksTracksRegistration(t *testing.T) {
	base := NewRitualSinkRouter(nil)
	if got := base.WiredSinks(); len(got) != 1 || got[0] != companion.SinkChat {
		t.Fatalf("boot wired sinks = %v, want exactly [chat]", got)
	}

	withNote := NewRitualSinkRouter(map[string]RitualSinkAdapter{
		companion.SinkMemoryNote: ChatSink{}, // stand-in writer; registration is the subject
	})
	if !withNote.IsSinkWired(companion.SinkMemoryNote) {
		t.Error("registering a writer must make its sink wired")
	}
	got := withNote.WiredSinks()
	if len(got) != 2 || got[0] != companion.SinkChat || got[1] != companion.SinkMemoryNote {
		t.Errorf("WiredSinks() = %v, want [chat memory_note] in closed-list order", got)
	}
}

// TestRitualSinkRouter_NilWriterIsNotRegistered — a nil entry must not count as
// wired, or the publish gate would pass a sink whose run then nil-panics.
func TestRitualSinkRouter_NilWriterIsNotRegistered(t *testing.T) {
	r := NewRitualSinkRouter(map[string]RitualSinkAdapter{companion.SinkNotification: nil})
	if r.IsSinkWired(companion.SinkNotification) {
		t.Error("a nil writer must not read as wired")
	}
}
