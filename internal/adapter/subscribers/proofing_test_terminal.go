// proofing_test_terminal.go — CHO-2040: the inbound bridge from the qgen
// crew's terminal events to the ProofingTest aggregate.
//
//	chora.creation.ai_assist.completed.v1 → requested|composing → READY
//	    (candidate_payload_json — with QGEN_TESTSET_COMPOSE_ENABLED live it is
//	    the BatchCandidatePayload OBJECT {"candidates":[...],
//	    "proposed_test_set":{...}} — stored verbatim as the takeable payload)
//	chora.creation.ai_assist.refused.v1   → REFUND the reservation, then
//	    requested|composing → FAILED (no-partial-success batch refusal)
//
// These topics are SHARED with chora-creation's authoring + R+ batch jobs: an
// assist_id with no proofing_tests row ACK-skips silently (mirrors creation's
// ai_assist_terminal_subscriber unknown-job ack — a NACK would poison the
// subscription). Redelivery of an already-terminal row acks (idempotent).
//
// Refund ordering (deliberate): the refund lands BEFORE the failed flip. If
// the refund RPC errors we return the error (Pub/Sub NACK + redelivery) while
// the row is still non-terminal, so the retry re-attempts the refund; the
// credit itself is idempotent on "<reservation>:refund", so a crash between
// refund and flip cannot double-credit.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// ProofingCompletedPayload is the decoded slice of AiAssistCompleted the
// bridge needs (assist_id f2, candidate_payload_json f11, quality_warning f13;
// tenant/gcid ride the ENVELOPE — completed.v1 carries no tenant field).
type ProofingCompletedPayload struct {
	AssistID             string
	CandidatePayloadJSON string
	QualityWarning       bool
}

// ProofingRefusedPayload is the decoded slice of AiAssistRefused (assist_id
// f2, refusal_reason f3, user_facing_message f6).
type ProofingRefusedPayload struct {
	AssistID          string
	RefusalReason     string
	UserFacingMessage string
}

// ProofingTestTerminalSubscriber advances ProofingTest rows on terminal events.
type ProofingTestTerminalSubscriber struct {
	repo pt.Repository
	mana pt.ManaReserver
	now  func() time.Time
}

// NewProofingTestTerminalSubscriber wires the bridge. Panics on nil repo/mana
// (composition-root bug, not a runtime state).
func NewProofingTestTerminalSubscriber(repo pt.Repository, mana pt.ManaReserver) *ProofingTestTerminalSubscriber {
	if repo == nil {
		panic("subscribers: ProofingTestTerminalSubscriber requires a repository")
	}
	if mana == nil {
		panic("subscribers: ProofingTestTerminalSubscriber requires a mana reserver")
	}
	return &ProofingTestTerminalSubscriber{
		repo: repo,
		mana: mana,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// WithNow injects a clock (tests). Returns the subscriber for chaining.
func (s *ProofingTestTerminalSubscriber) WithNow(now func() time.Time) *ProofingTestTerminalSubscriber {
	if now != nil {
		s.now = now
	}
	return s
}

// resolveRow loads the proofing test the event keys on. (nil, nil) ⇒ not ours
// (ack-skip); a repo error surfaces (NACK — infra, retryable).
func (s *ProofingTestTerminalSubscriber) resolveRow(ctx context.Context, env events.Envelope, assistID string) (*pt.ProofingTest, error) {
	assistID = strings.TrimSpace(assistID)
	if assistID == "" {
		return nil, errors.New("proofing_terminal: empty assist_id on terminal event (malformed producer)")
	}
	tenantID := strings.TrimSpace(env.TenantID)
	if tenantID == "" {
		return nil, errors.New("proofing_terminal: empty tenant_id on terminal envelope")
	}
	row, err := s.repo.GetByAssistID(ctx, tenantID, assistID)
	if err != nil {
		return nil, fmt.Errorf("proofing_terminal: resolve assist_id %s: %w", assistID, err)
	}
	return row, nil
}

// HandleCompleted lands the composed test set.
func (s *ProofingTestTerminalSubscriber) HandleCompleted(ctx context.Context, env events.Envelope, p ProofingCompletedPayload) error {
	row, err := s.resolveRow(ctx, env, p.AssistID)
	if err != nil {
		return err
	}
	if row == nil {
		// Not a proofing test (an authoring / R+ batch job on the shared
		// topic) — ack + drop, mirroring creation's unknown-job ack.
		return nil
	}
	if row.Terminal() {
		return nil // redelivery of a settled row — idempotent ack
	}
	payload := strings.TrimSpace(p.CandidatePayloadJSON)
	if payload == "" {
		return fmt.Errorf("proofing_terminal: completed.v1 for %s carried no candidate_payload_json (refusing a fabricated ready)", p.AssistID)
	}
	if err := row.MarkReady([]byte(payload), s.now()); err != nil {
		return fmt.Errorf("proofing_terminal: mark ready %s: %w", row.ID, err)
	}
	if err := s.repo.Update(ctx, row); err != nil {
		return fmt.Errorf("proofing_terminal: persist ready %s: %w", row.ID, err)
	}
	log.Printf("proofing_terminal: proofing test %s READY (assist=%s, quality_warning=%v)",
		row.ID, row.AssistID, p.QualityWarning)
	return nil
}

// HandleRefused refunds the reservation, then fails the row.
func (s *ProofingTestTerminalSubscriber) HandleRefused(ctx context.Context, env events.Envelope, p ProofingRefusedPayload) error {
	row, err := s.resolveRow(ctx, env, p.AssistID)
	if err != nil {
		return err
	}
	if row == nil {
		return nil // foreign job — never touch, never refund
	}
	if row.Terminal() {
		return nil // idempotent redelivery
	}
	// Refund FIRST (idempotent by key). An error NACKs while the row is
	// still non-terminal so redelivery retries the refund.
	if row.ManaReserved > 0 && row.ReservationID != "" {
		if err := s.mana.Refund(ctx, pt.RefundInput{
			TenantID:      row.TenantID,
			GCID:          row.LearnerGCID,
			ReservationID: row.ReservationID,
			Units:         row.ManaReserved,
			Reason:        "proofing test refused: " + strings.TrimSpace(p.RefusalReason),
		}); err != nil {
			return fmt.Errorf("proofing_terminal: refund %s: %w", row.ID, err)
		}
	}
	reason := strings.TrimSpace(p.RefusalReason)
	if reason == "" {
		reason = "qgen batch refused"
	}
	if msg := strings.TrimSpace(p.UserFacingMessage); msg != "" {
		reason = reason + ": " + msg
	}
	if err := row.MarkFailed(reason, s.now()); err != nil {
		return fmt.Errorf("proofing_terminal: mark failed %s: %w", row.ID, err)
	}
	if err := s.repo.Update(ctx, row); err != nil {
		return fmt.Errorf("proofing_terminal: persist failed %s: %w", row.ID, err)
	}
	log.Printf("proofing_terminal: proofing test %s FAILED (assist=%s, reason=%q, refunded=%d)",
		row.ID, row.AssistID, reason, row.ManaReserved)
	return nil
}
