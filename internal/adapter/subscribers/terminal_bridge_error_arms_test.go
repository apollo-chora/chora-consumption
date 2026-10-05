// terminal_bridge_error_arms_test.go — complements the existing terminal-bridge
// tests (campaign_question_terminal + proofing_test_terminal) for the arms the
// happy-path and wiring-branch tests still leave open. These are the branches a
// repo that can fail on demand (or a malformed/terminal row) reaches without a
// real store:
//
//   - campaign HandleRefused: foreign (set == nil) ack-skip, idempotent
//     redelivery of a settled set, and the MarkReady/MarkFailed error paths
//     (a set that reached the terminal bridge off a non-requested status).
//   - proofing HandleRefused: idempotent redelivery of a terminal row, plus the
//     MarkReady/MarkFailed error paths (a soft-deleted, non-terminal row that
//     slips past the handler's Terminal() guard).
//
// The repo/domain helpers (cqEnv, cqRequestedSet, ptErrSeedRow, terminalEnv,
// terminalManaFake) are package-level from the other subscribers tests.
package subscribers

import (
	"context"
	"strings"
	"testing"
	"time"

	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// cqErrRepo is a cq.Repository whose readable path can fail on demand, so the
// terminal bridge's error arms are reachable without a live store.
type cqErrRepo struct {
	rows    map[string]*cq.QuestionSet
	getErr  error
	saveErr error
}

func (r *cqErrRepo) GetByConceptRung(context.Context, string, string, string, int) (*cq.QuestionSet, error) {
	return nil, r.getErr
}
func (r *cqErrRepo) GetByAssistID(_ context.Context, _ string, assistID string) (*cq.QuestionSet, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.rows[assistID], nil
}
func (r *cqErrRepo) CountRequestedOn(context.Context, string, string, cq.RequestOrigin, time.Time) (int, error) {
	return 0, nil
}
func (r *cqErrRepo) Save(_ context.Context, _ *cq.QuestionSet) error { return r.saveErr }

// cqIdleSet builds a fresh question set that cq.New leaves in StatusIdle (never
// requested). It is exposed to the terminal bridge off the Requested status, so
// MarkReady / MarkFailed reject it — driving those error branches.
func cqIdleSet(t *testing.T) *cq.QuestionSet {
	t.Helper()
	s, err := cq.New("11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222",
		"33333333-3333-7333-8333-333333333333", "addition", 2, time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestCampaignTerminal_RefusedForeignAssistAckSkips(t *testing.T) {
	// A refusal for an assist the campaign question-set repo does not own must
	// ack (nil) and never touch a row — the shared topic carries other jobs.
	sub := NewCampaignQuestionTerminalSubscriber(&cqErrRepo{rows: map[string]*cq.QuestionSet{
		"own-1": cqRequestedSet(t, "own-1"),
	}})
	if err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{
		AssistID: "someone-elses-assist", RefusalReason: "not ours",
	}); err != nil {
		t.Fatalf("a refusal on a foreign assist must ack (nil), got %v", err)
	}
}

func TestCampaignTerminal_RefusedIdempotentRedelivery(t *testing.T) {
	// A refusal re-delivered for an already-settled set (failed) must ack and
	// not re-persist — the row is terminal.
	set := cqRequestedSet(t, "a1")
	if err := set.MarkFailed("first refusal", time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	repo := &cqErrRepo{rows: map[string]*cq.QuestionSet{"a1": set}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	if err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{
		AssistID: "a1", RefusalReason: "again",
	}); err != nil {
		t.Fatalf("redelivery on a settled set must ack, got %v", err)
	}
}

func TestCampaignTerminal_CompletedMarkReadyErrorSurfaces(t *testing.T) {
	// A completed delivery on an idle (never-requested) set hits the handler's
	// push guards (non-nil, not ready/failed) but MarkReady rejects the
	// requested-only transition — the error must surface (NACK).
	sub := NewCampaignQuestionTerminalSubscriber(&cqErrRepo{rows: map[string]*cq.QuestionSet{
		"idle-1": cqIdleSet(t),
	}})
	err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{
		AssistID: "idle-1", CandidatePayloadJSON: `{"candidates":[]}`,
	})
	if err == nil || !strings.Contains(err.Error(), "mark ready") {
		t.Fatalf("MarkReady rejection must surface, got %v", err)
	}
}

func TestCampaignTerminal_RefusedMarkFailedErrorSurfaces(t *testing.T) {
	// Mirror of the above for the refuse path: an idle set passes the guards and
	// MarkFailed rejects it, surfacing the error.
	sub := NewCampaignQuestionTerminalSubscriber(&cqErrRepo{rows: map[string]*cq.QuestionSet{
		"idle-1": cqIdleSet(t),
	}})
	if err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{
		AssistID: "idle-1", RefusalReason: "x",
	}); err == nil || !strings.Contains(err.Error(), "mark failed") {
		t.Fatalf("MarkFailed rejection must surface, got %v", err)
	}
}

// --- proofing test terminal error arms -------------------------------------

// ptErrRepo lets the proofing bridge's arms run without a live store; rows are
// served only via GetByAssistID (the terminal read).
type ptErrRepo struct {
	row       *pt.ProofingTest
	getErr    error
	updateErr error
}

func (r *ptErrRepo) Create(context.Context, *pt.ProofingTest) error { return nil }
func (r *ptErrRepo) GetByID(context.Context, string, string, string) (*pt.ProofingTest, error) {
	return nil, nil
}
func (r *ptErrRepo) GetByAssistID(_ context.Context, _ string, _ string) (*pt.ProofingTest, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.row, nil
}
func (r *ptErrRepo) ListByLearner(context.Context, string, string, string) ([]*pt.ProofingTest, error) {
	return nil, nil
}
func (r *ptErrRepo) Update(_ context.Context, _ *pt.ProofingTest) error { return r.updateErr }

// ptErrSeedRow builds a requested ProofingTest row so the error-arm tests can
// back a ptErrRepo with a real (non-terminal) aggregate, mirroring seedTerminal
// without an inmem store.
func ptErrSeedRow(t *testing.T) *pt.ProofingTest {
	t.Helper()
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
		t.Fatalf("NewRequested: %v", err)
	}
	return row
}

func TestProofingTerminal_RefusedIdempotentRedelivery(t *testing.T) {
	// A refusal re-delivered for an already-failed row must ack and never refund
	// again (the row.Terminal() guard runs before the refund).
	row := ptErrSeedRow(t)
	if err := row.MarkFailed("first refusal", time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	sub := NewProofingTestTerminalSubscriber(&ptErrRepo{row: row}, &terminalManaFake{})
	if err := sub.HandleRefused(context.Background(), terminalEnv(row.AssistID), ProofingRefusedPayload{
		AssistID: row.AssistID, RefusalReason: "again",
	}); err != nil {
		t.Fatalf("redelivery on a terminal row must ack, got %v", err)
	}
}

func TestProofingTerminal_CompletedTerminalRowAckSkips(t *testing.T) {
	// A completed re-delivery for an already-ready row acks idempotently.
	row := ptErrSeedRow(t)
	if err := row.MarkReady([]byte(composedPayload), time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	sub := NewProofingTestTerminalSubscriber(&ptErrRepo{row: row}, &terminalManaFake{})
	if err := sub.HandleCompleted(context.Background(), terminalEnv(row.AssistID), ProofingCompletedPayload{
		AssistID: row.AssistID, CandidatePayloadJSON: composedPayload,
	}); err != nil {
		t.Fatalf("redelivery on a terminal row must ack (nil), got %v", err)
	}
}

func TestProofingTerminal_CompletedSoftDeletedRowFailsLoud(t *testing.T) {
	// A soft-deleted (but non-terminal) row slips past the handler's Terminal()
	// guard, and MarkReady must reject it — surfacing an error rather than
	// resurrecting a tombstoned row.
	row := ptErrSeedRow(t)
	row.SoftDelete(time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC))
	sub := NewProofingTestTerminalSubscriber(&ptErrRepo{row: row}, &terminalManaFake{})
	err := sub.HandleCompleted(context.Background(), terminalEnv(row.AssistID), ProofingCompletedPayload{
		AssistID: row.AssistID, CandidatePayloadJSON: composedPayload,
	})
	if err == nil || !strings.Contains(err.Error(), "mark ready") {
		t.Fatalf("a soft-deleted row must fail loud on mark ready, got %v", err)
	}
}

func TestProofingTerminal_RefusedSoftDeletedRowFailsLoud(t *testing.T) {
	// Same tombstone guard on the refuse path: the terminal guard passes, the
	// refund is skipped (NACK would re-try a dead row), and MarkFailed rejects.
	row := ptErrSeedRow(t)
	row.SoftDelete(time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC))
	sub := NewProofingTestTerminalSubscriber(&ptErrRepo{row: row}, &terminalManaFake{})
	if err := sub.HandleRefused(context.Background(), terminalEnv(row.AssistID), ProofingRefusedPayload{
		AssistID: row.AssistID, RefusalReason: "x",
	}); err == nil || !strings.Contains(err.Error(), "mark failed") {
		t.Fatalf("a soft-deleted row must fail loud on mark failed, got %v", err)
	}
}
