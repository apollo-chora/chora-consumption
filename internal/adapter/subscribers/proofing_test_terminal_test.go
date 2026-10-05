// proofing_test_terminal_test.go — CHO-2040: the qgen terminal-event bridge
// that advances a ProofingTest when the crew's TestSetComposer completes (or
// the batch is refused), RED-first.
//
// The completed/refused topics are SHARED with chora-creation's authoring +
// R+ batch jobs — an assist_id with no proofing_tests row MUST ack-skip
// silently (mirrors creation's unknown-job ack; a NACK would poison the
// subscription forever).
package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

const (
	ptTenant  = "11111111-1111-4111-8111-111111111111"
	ptLearner = "22222222-2222-4222-8222-222222222222"
)

type terminalManaFake struct {
	refunds   []pt.RefundInput
	refundErr error
}

func (f *terminalManaFake) Reserve(_ context.Context, _ pt.ReserveInput) (pt.ReserveResult, error) {
	return pt.ReserveResult{}, errors.New("terminal subscriber never reserves")
}

func (f *terminalManaFake) Refund(_ context.Context, in pt.RefundInput) error {
	f.refunds = append(f.refunds, in)
	return f.refundErr
}

func seedTerminal(t *testing.T) (*ProofingTestTerminalSubscriber, *inmem.ProofingTestRepo, *terminalManaFake, *pt.ProofingTest) {
	t.Helper()
	repo := inmem.NewProofingTestRepo()
	row, err := pt.NewRequested(pt.NewRequestedInput{
		TenantID:    ptTenant,
		LearnerGCID: ptLearner,
		GoalID:      "33333333-3333-4333-8333-333333333333",
		CompanionID: "44444444-4444-4444-8444-444444444444",
		TargetEdges: []pt.TargetEdge{{
			ConceptID: "55555555-5555-4555-8555-555555555555",
			Key:       "algebra.quadratic_roots",
			Title:     "Quadratic roots",
			Intent:    "remediate",
		}},
		ManaReserved:  25,
		ReservationID: "proofing_test_gen:t:g:x",
		Now:           time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed NewRequested: %v", err)
	}
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	mana := &terminalManaFake{}
	sub := NewProofingTestTerminalSubscriber(repo, mana)
	return sub, repo, mana, row
}

func terminalEnv(assistID string) events.Envelope {
	now := time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC)
	return events.Envelope{
		EventID:        "0197a001-aaaa-7bbb-8ccc-0000000000ff",
		IdempotencyKey: "terminal." + assistID,
		TenantID:       ptTenant,
		GCID:           ptLearner,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-11111111111111111111111111111111-2222222222222222-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-ai-kernel-orchestrator",
		SchemaVersion:  1,
	}
}

const composedPayload = `{"candidates":[{"draft_id":"d1"}],"proposed_test_set":{"title":"Proofing: Quadratics","order":["d1"]}}`

func TestTerminal_CompletedAdvancesToReady(t *testing.T) {
	sub, repo, mana, row := seedTerminal(t)
	err := sub.HandleCompleted(context.Background(), terminalEnv(row.AssistID), ProofingCompletedPayload{
		AssistID:             row.AssistID,
		CandidatePayloadJSON: composedPayload,
	})
	if err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if got == nil || got.Status != pt.StatusReady {
		t.Fatalf("row must be ready, got %+v", got)
	}
	if !strings.Contains(string(got.TestSetPayload), "proposed_test_set") {
		t.Fatalf("composed payload must be stored, got %s", got.TestSetPayload)
	}
	if len(mana.refunds) != 0 {
		t.Fatalf("no refund on success (the reserve settles)")
	}
}

func TestTerminal_CompletedUnknownAssistAcksSilently(t *testing.T) {
	sub, repo, _, row := seedTerminal(t)
	err := sub.HandleCompleted(context.Background(), terminalEnv("01970000-not-ours-0000-000000000001"), ProofingCompletedPayload{
		AssistID:             "01970000-aaaa-7000-8000-00000000beef", // an authoring job, not ours
		CandidatePayloadJSON: composedPayload,
	})
	if err != nil {
		t.Fatalf("unknown assist_id must ack (nil), got %v", err)
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if got.Status != pt.StatusRequested {
		t.Fatalf("our row must be untouched, got %q", got.Status)
	}
}

func TestTerminal_CompletedIsIdempotentOnRedelivery(t *testing.T) {
	sub, repo, _, row := seedTerminal(t)
	env := terminalEnv(row.AssistID)
	p := ProofingCompletedPayload{AssistID: row.AssistID, CandidatePayloadJSON: composedPayload}
	if err := sub.HandleCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandleCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery must ack (nil), got %v", err)
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if got.Status != pt.StatusReady {
		t.Fatalf("row must stay ready, got %q", got.Status)
	}
}

func TestTerminal_CompletedMissingAssistFailsLoud(t *testing.T) {
	sub, _, _, _ := seedTerminal(t)
	if err := sub.HandleCompleted(context.Background(), terminalEnv("x"), ProofingCompletedPayload{}); err == nil {
		t.Fatalf("empty assist_id must error (NACK) — a malformed producer bug must be visible")
	}
}

func TestTerminal_RefusedRefundsThenFails(t *testing.T) {
	sub, repo, mana, row := seedTerminal(t)
	err := sub.HandleRefused(context.Background(), terminalEnv(row.AssistID), ProofingRefusedPayload{
		AssistID:      row.AssistID,
		RefusalReason: "guardrail_block",
	})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if got.Status != pt.StatusFailed {
		t.Fatalf("row must be failed, got %q", got.Status)
	}
	if !strings.Contains(got.FailureReason, "guardrail_block") {
		t.Fatalf("failure reason must carry the refusal, got %q", got.FailureReason)
	}
	if len(mana.refunds) != 1 {
		t.Fatalf("refunds = %d, want 1", len(mana.refunds))
	}
	r := mana.refunds[0]
	if r.ReservationID != row.ReservationID || r.Units != 25 || r.TenantID != ptTenant || r.GCID != ptLearner {
		t.Fatalf("refund mis-passed: %+v", r)
	}
}

func TestTerminal_RefusedRefundFailureNACKsBeforeStateFlip(t *testing.T) {
	sub, repo, mana, row := seedTerminal(t)
	mana.refundErr = errors.New("identity down")
	err := sub.HandleRefused(context.Background(), terminalEnv(row.AssistID), ProofingRefusedPayload{
		AssistID:      row.AssistID,
		RefusalReason: "guardrail_block",
	})
	if err == nil {
		t.Fatalf("refund failure must error (NACK) so redelivery retries the refund")
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if got.Status != pt.StatusRequested {
		t.Fatalf("row must NOT flip terminal before the refund lands (redelivery must retry), got %q", got.Status)
	}
}

func TestTerminal_RefusedUnknownAssistAcks(t *testing.T) {
	sub, _, mana, _ := seedTerminal(t)
	err := sub.HandleRefused(context.Background(), terminalEnv("z"), ProofingRefusedPayload{
		AssistID:      "01970000-aaaa-7000-8000-00000000beef",
		RefusalReason: "not ours",
	})
	if err != nil {
		t.Fatalf("unknown assist_id must ack (nil), got %v", err)
	}
	if len(mana.refunds) != 0 {
		t.Fatalf("never refund a foreign job")
	}
}
