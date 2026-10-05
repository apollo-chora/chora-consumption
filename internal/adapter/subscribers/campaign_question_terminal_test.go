// campaign_question_terminal_test.go — WS-C3 (CHO-2082): the inbound bridge
// from the qgen crew's terminal events to the campaign QuestionSet aggregate.
// Mirrors the proofing terminal bridge on the SAME shared topics: unknown
// assist ids ack-skip (foreign jobs), terminal rows ack idempotently, an
// empty completed payload refuses (no fabricated ready), and — unlike
// proofing — there is NO mana refund (the campaign lane is D13-exempt).
package subscribers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
)

type cqFakeRepo struct {
	byAssist map[string]*cq.QuestionSet
	saved    []*cq.QuestionSet
}

func (r *cqFakeRepo) GetByConceptRung(context.Context, string, string, string, int) (*cq.QuestionSet, error) {
	return nil, nil
}
func (r *cqFakeRepo) GetByAssistID(_ context.Context, _ string, assistID string) (*cq.QuestionSet, error) {
	return r.byAssist[assistID], nil
}
func (r *cqFakeRepo) CountRequestedOn(context.Context, string, string, cq.RequestOrigin, time.Time) (int, error) {
	return 0, nil
}
func (r *cqFakeRepo) Save(_ context.Context, s *cq.QuestionSet) error {
	r.saved = append(r.saved, s)
	return nil
}

func cqRequestedSet(t *testing.T, assistID string) *cq.QuestionSet {
	t.Helper()
	s, err := cq.New("11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222",
		"33333333-3333-7333-8333-333333333333", "addition", 2, time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.MarkRequested(assistID, time.Date(2026, 7, 9, 8, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("MarkRequested: %v", err)
	}
	return s
}

func cqEnv() events.Envelope {
	return events.Envelope{TenantID: "11111111-1111-7111-8111-111111111111"}
}

func TestCampaignTerminal_CompletedLandsReady(t *testing.T) {
	repo := &cqFakeRepo{byAssist: map[string]*cq.QuestionSet{
		"assist-1": cqRequestedSet(t, "assist-1"),
	}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{
		AssistID:             "assist-1",
		CandidatePayloadJSON: `{"candidates":[{"q":"2+2?"}]}`,
	})
	if err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].GenerationStatus != cq.StatusReady || !repo.saved[0].CanServe() {
		t.Fatalf("completed must persist a READY servable set, got %+v", repo.saved)
	}
}

func TestCampaignTerminal_UnknownAssistAckSkips(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqFakeRepo{})
	if err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{
		AssistID: "foreign", CandidatePayloadJSON: `{}`,
	}); err != nil {
		t.Fatalf("foreign assist must ack-skip, got %v", err)
	}
}

func TestCampaignTerminal_EmptyPayloadRefuses(t *testing.T) {
	repo := &cqFakeRepo{byAssist: map[string]*cq.QuestionSet{
		"assist-1": cqRequestedSet(t, "assist-1"),
	}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{AssistID: "assist-1"})
	if err == nil || !strings.Contains(err.Error(), "fabricated") {
		t.Fatalf("empty payload must refuse a fabricated ready, got %v", err)
	}
}

func TestCampaignTerminal_TerminalRowIdempotent(t *testing.T) {
	ready := cqRequestedSet(t, "assist-1")
	if err := ready.MarkReady([]byte(`{"candidates":[]}`), time.Now().UTC()); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	repo := &cqFakeRepo{byAssist: map[string]*cq.QuestionSet{"assist-1": ready}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	if err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{
		AssistID: "assist-1", CandidatePayloadJSON: `{"candidates":[]}`,
	}); err != nil {
		t.Fatalf("redelivery on a terminal row must ack, got %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("terminal redelivery must not re-save, got %d saves", len(repo.saved))
	}
}

func TestCampaignTerminal_RefusedLandsFailed_NoRefund(t *testing.T) {
	repo := &cqFakeRepo{byAssist: map[string]*cq.QuestionSet{
		"assist-1": cqRequestedSet(t, "assist-1"),
	}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{
		AssistID: "assist-1", RefusalReason: "guardrail",
	})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].GenerationStatus != cq.StatusFailed ||
		!strings.Contains(repo.saved[0].FailureReason, "guardrail") {
		t.Fatalf("refusal must persist FAILED with the reason, got %+v", repo.saved)
	}
}
