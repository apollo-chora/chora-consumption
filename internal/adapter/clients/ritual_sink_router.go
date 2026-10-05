// ritual_sink_router.go — CHO-2016 G5 item 1: the SinkWriter (satisfies
// companion.SinkWriter). A Ritual declares ONE of the 6 closed sinks (ADR-218
// D9); the router dispatches the buffered run outputs to the registered writer
// for that sink, atomically on completion.
//
// REAL, no fakes: the chat sink is fully wired (chat output is ephemeral — the
// run response carries it, so the sink write is a durable-ref no-op). The other
// 5 sinks bind to their existing subsystems (memory_note → companion memory,
// suggestion_inbox → KG suggestions, question_bank → learner QuestionBank,
// notification → notifications, calendar_artifact → GCS artifact); their
// concrete writers are REGISTERED at main.go wiring. An UNREGISTERED sink FAILS
// LOUD ("sink not wired") — never a fake success — so a ritual whose sink is not
// yet wired refunds + records failed rather than silently dropping output.
package clients

import (
	"context"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// RitualSinkAdapter writes buffered run outputs to ONE concrete sink.
type RitualSinkAdapter interface {
	Write(ctx context.Context, in companion.SinkWriteInput) (sinkRef string, err error)
}

// RitualSinkRouter implements companion.SinkWriter over a per-sink registry.
type RitualSinkRouter struct {
	writers map[string]RitualSinkAdapter
}

// NewRitualSinkRouter builds the router. `writers` maps a closed-sink key (see
// catalog.Sink*) to its adapter; main.go registers the wired sinks. The chat
// sink is registered by default (always available).
func NewRitualSinkRouter(writers map[string]RitualSinkAdapter) *RitualSinkRouter {
	m := map[string]RitualSinkAdapter{
		companion.SinkChat: ChatSink{},
	}
	for k, w := range writers {
		if w != nil {
			m[k] = w
		}
	}
	return &RitualSinkRouter{writers: m}
}

// Compile-time checks. The router satisfies BOTH the run-time writer port and
// the publish-time registry port, deliberately: one writer map answers "can
// this deployment write that sink?" for the publish gate and for the run, so
// the two can never disagree (N5).
var (
	_ companion.SinkWriter   = (*RitualSinkRouter)(nil)
	_ companion.SinkRegistry = (*RitualSinkRouter)(nil)
)

// IsSinkWired reports whether a concrete writer is registered for this sink.
// The publish gate uses it to refuse a ritual that could only ever fail at
// WriteToSink; the FE greys the same set from its own catalogue read.
func (r *RitualSinkRouter) IsSinkWired(sink string) bool {
	_, ok := r.writers[sink]
	return ok
}

// WiredSinks lists the registered sinks in the ADR-218 D9 closed-list order, so
// a boot log and any future read model can state what this deployment can
// actually write rather than what the schema permits.
func (r *RitualSinkRouter) WiredSinks() []string {
	ordered := []string{
		companion.SinkChat, companion.SinkMemoryNote, companion.SinkSuggestionInbox,
		companion.SinkQuestionBank, companion.SinkNotification, companion.SinkCalendarArtifact,
	}
	out := make([]string, 0, len(ordered))
	for _, s := range ordered {
		if r.IsSinkWired(s) {
			out = append(out, s)
		}
	}
	return out
}

// WriteToSink dispatches to the registered adapter. An unregistered sink is a
// fail-loud error (the runner refunds + records the run failed).
func (r *RitualSinkRouter) WriteToSink(ctx context.Context, sink string, in companion.SinkWriteInput) (string, error) {
	w, ok := r.writers[sink]
	if !ok {
		return "", fmt.Errorf("ritual_sink: sink %q not wired (register its subsystem writer at boot)", sink)
	}
	ref, err := w.Write(ctx, in)
	if err != nil {
		return "", fmt.Errorf("ritual_sink: write to %q: %w", sink, err)
	}
	return ref, nil
}

// ChatSink is the chat reply sink. Chat output is ephemeral — the run response
// (learner story + step outputs) carries it — so the durable write is a stable
// reference only. Fully real (no external subsystem).
type ChatSink struct{}

// Write returns a deterministic chat sink reference.
func (ChatSink) Write(_ context.Context, in companion.SinkWriteInput) (string, error) {
	return "chat:" + in.RunID, nil
}
