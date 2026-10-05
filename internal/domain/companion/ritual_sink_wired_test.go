// ritual_sink_wired_test.go — RED-first for N5 (tracker row B3): publish
// refuses a sink whose writer is not registered in this deployment.
//
// The defect this closes, measured on main: NewRitual accepts any of the six
// ADR-218 D9 closed sinks and the FE offers all six, while the boot wiring
// registers exactly one (chat). So a learner can publish a ritual that can only
// ever fail: the runner reaches WriteToSink, the router refuses, and the run
// terminates failed with a refund. A refund is not a fix; the ritual was never
// runnable.
//
// "Wired" is a DEPLOYMENT fact, not an aggregate invariant, so the port lives
// in the domain, the check lives in the domain SERVICE, and the sink registry
// itself is the adapter that already owns the writer map.
package companion

import (
	"context"
	"errors"
	"testing"
)

// stubSinkRegistry reports a fixed wired set.
type stubSinkRegistry struct{ wired map[string]bool }

func (s stubSinkRegistry) IsSinkWired(sink string) bool { return s.wired[sink] }

// WiredSinks returns the wired set in the closed-list order, matching the
// production router's ordering so a test cannot pass on a different contract.
func (s stubSinkRegistry) WiredSinks() []string {
	out := []string{}
	for _, k := range []string{
		SinkChat, SinkMemoryNote, SinkSuggestionInbox,
		SinkQuestionBank, SinkNotification, SinkCalendarArtifact,
	} {
		if s.wired[k] {
			out = append(out, k)
		}
	}
	return out
}

// chatOnlySinks mirrors the live boot wiring (ritual_wiring.go passes nil
// writers, so RitualSinkRouter registers chat and nothing else).
func chatOnlySinks() SinkRegistry {
	return stubSinkRegistry{wired: map[string]bool{SinkChat: true}}
}

func allSinksWired() SinkRegistry {
	return stubSinkRegistry{wired: map[string]bool{
		SinkChat: true, SinkMemoryNote: true, SinkSuggestionInbox: true,
		SinkQuestionBank: true, SinkNotification: true, SinkCalendarArtifact: true,
	}}
}

func TestNewRitualPublisher_RequiresSinkRegistry(t *testing.T) {
	_, err := NewRitualPublisher(RitualPublisherConfig{
		Repo:   newFakeRitualRepo(),
		Caps:   &fakeCapsResolver{caps: ritualTestCaps()},
		Outbox: &fakeRitualOutbox{},
		NewID:  func() string { return "019f26d5-e707-745c-891f-44a1c7694b75" },
	})
	if err == nil {
		t.Fatal("publisher must refuse to build without a SinkRegistry: an absent registry " +
			"would silently mean 'every sink is wired', which is the failure this row exists to stop")
	}
}

// TestPublish_RefusesUnwiredSink is the B3 acceptance clause.
func TestPublish_RefusesUnwiredSink(t *testing.T) {
	p := newTestPublisher(t, chatOnlySinks())
	_, _, err := p.Publish(context.Background(), PublishRitualInput{
		TenantID: "t-1", CompanionID: "comp-1", OwnerGCID: "gcid-1",
		Name: "Nightly Note", Trigger: TriggerManual, Sink: SinkMemoryNote,
		Steps:       []RitualStep{{SkillKey: "quiz_me"}},
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	if !errors.Is(err, ErrRitualSinkNotWired) {
		t.Fatalf("err = %v, want ErrRitualSinkNotWired", err)
	}
}

// TestPublish_RefusesUnwiredSinkBeforeAnyWrite — a refused publish must persist
// nothing and emit nothing, matching the aggregate's existing discipline.
func TestPublish_RefusesUnwiredSinkBeforeAnyWrite(t *testing.T) {
	repo := newFakeRitualRepo()
	outbox := &fakeRitualOutbox{}
	p, err := NewRitualPublisher(RitualPublisherConfig{
		Repo: repo, Caps: &fakeCapsResolver{caps: ritualTestCaps()}, Outbox: outbox,
		Sinks: chatOnlySinks(), Clock: fixedClock(),
		NewID: func() string { return "019f26d5-e707-745c-891f-44a1c7694b75" },
	})
	if err != nil {
		t.Fatalf("NewRitualPublisher: %v", err)
	}
	_, _, err = p.Publish(context.Background(), PublishRitualInput{
		TenantID: "t-1", CompanionID: "comp-1", OwnerGCID: "gcid-1",
		Name: "Nightly Note", Trigger: TriggerManual, Sink: SinkQuestionBank,
		Steps:       []RitualStep{{SkillKey: "quiz_me"}},
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	if !errors.Is(err, ErrRitualSinkNotWired) {
		t.Fatalf("err = %v, want ErrRitualSinkNotWired", err)
	}
	if len(repo.created) != 0 {
		t.Error("a refused publish must not create the ritual")
	}
	if len(outbox.topics) != 0 {
		t.Errorf("a refused publish must emit nothing, got %d events", len(outbox.topics))
	}
}

// TestPublish_AllowsWiredSink — the positive control. Without it, a check that
// refused EVERYTHING would pass the two tests above.
func TestPublish_AllowsWiredSink(t *testing.T) {
	p := newTestPublisher(t, chatOnlySinks())
	_, rev, err := p.Publish(context.Background(), PublishRitualInput{
		TenantID: "t-1", CompanionID: "comp-1", OwnerGCID: "gcid-1",
		Name: "Morning Review", Trigger: TriggerManual, Sink: SinkChat,
		Steps:       []RitualStep{{SkillKey: "quiz_me"}},
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	if err != nil {
		t.Fatalf("publish to the wired chat sink must succeed, got: %v", err)
	}
	if rev.RevisionNo != 1 {
		t.Errorf("RevisionNo = %d, want 1", rev.RevisionNo)
	}
}

// TestPublish_WiringTheSinkMakesItPublishable — the refusal tracks the
// deployment, not a hardcoded allowlist, so registering the writer is what
// unblocks the sink.
func TestPublish_WiringTheSinkMakesItPublishable(t *testing.T) {
	p := newTestPublisher(t, allSinksWired())
	_, _, err := p.Publish(context.Background(), PublishRitualInput{
		TenantID: "t-1", CompanionID: "comp-1", OwnerGCID: "gcid-1",
		Name: "Nightly Note", Trigger: TriggerManual, Sink: SinkMemoryNote,
		Steps:       []RitualStep{{SkillKey: "quiz_me"}},
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	if err != nil {
		t.Fatalf("memory_note must publish once its writer is registered, got: %v", err)
	}
}

func newTestPublisher(t *testing.T, sinks SinkRegistry) *RitualPublisher {
	t.Helper()
	p, err := NewRitualPublisher(RitualPublisherConfig{
		Repo:   newFakeRitualRepo(),
		Caps:   &fakeCapsResolver{caps: ritualTestCaps()},
		Outbox: &fakeRitualOutbox{},
		Sinks:  sinks,
		Clock:  fixedClock(),
		NewID:  func() string { return "019f26d5-e707-745c-891f-44a1c7694b75" },
	})
	if err != nil {
		t.Fatalf("NewRitualPublisher: %v", err)
	}
	return p
}
