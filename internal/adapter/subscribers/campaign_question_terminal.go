// campaign_question_terminal.go — WS-C3 (CHO-2082): the inbound bridge from
// the qgen crew's terminal events to the campaign QuestionSet aggregate.
//
//	chora.creation.ai_assist.completed.v1 → requested → READY
//	    (candidate_payload_json stored verbatim — the reusable question bank)
//	chora.creation.ai_assist.refused.v1   → requested → FAILED
//
// The topics are SHARED with chora-creation's authoring/R+ jobs AND the
// proofing-test runner: the push dispatch tries the proofing bridge first,
// then this one; an assist_id neither knows ACK-skips silently (a NACK would
// poison the shared subscription). Redelivery of an already-terminal set
// acks (idempotent). Unlike proofing there is NO mana refund on refusal —
// the campaign lane is mana-EXEMPT (ADR-227 D13). Payload shapes are reused
// from the proofing bridge (same wire messages).
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
)

// CampaignQuestionTerminalSubscriber advances QuestionSets on terminal events.
type CampaignQuestionTerminalSubscriber struct {
	repo cq.Repository
	now  func() time.Time
}

// NewCampaignQuestionTerminalSubscriber wires the bridge. Panics on a nil
// repo (composition-root bug, not a runtime state).
func NewCampaignQuestionTerminalSubscriber(repo cq.Repository) *CampaignQuestionTerminalSubscriber {
	if repo == nil {
		panic("subscribers: CampaignQuestionTerminalSubscriber requires a repository")
	}
	return &CampaignQuestionTerminalSubscriber{
		repo: repo,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// WithNow injects a clock (tests). Returns the subscriber for chaining.
func (s *CampaignQuestionTerminalSubscriber) WithNow(now func() time.Time) *CampaignQuestionTerminalSubscriber {
	if now != nil {
		s.now = now
	}
	return s
}

// resolveSet loads the question set the event keys on. (nil, nil) ⇒ not ours
// (ack-skip); a repo error surfaces (NACK — infra, retryable).
func (s *CampaignQuestionTerminalSubscriber) resolveSet(ctx context.Context, env events.Envelope, assistID string) (*cq.QuestionSet, error) {
	assistID = strings.TrimSpace(assistID)
	if assistID == "" {
		return nil, errors.New("campaign_question_terminal: empty assist_id on terminal event (malformed producer)")
	}
	tenantID := strings.TrimSpace(env.TenantID)
	if tenantID == "" {
		return nil, errors.New("campaign_question_terminal: empty tenant_id on terminal envelope")
	}
	set, err := s.repo.GetByAssistID(ctx, tenantID, assistID)
	if err != nil {
		return nil, fmt.Errorf("campaign_question_terminal: resolve assist_id %s: %w", assistID, err)
	}
	return set, nil
}

// HandleCompleted lands the composed question payload as the reusable bank.
func (s *CampaignQuestionTerminalSubscriber) HandleCompleted(ctx context.Context, env events.Envelope, p ProofingCompletedPayload) error {
	set, err := s.resolveSet(ctx, env, p.AssistID)
	if err != nil {
		return err
	}
	if set == nil {
		return nil // foreign job on the shared topic — ack + drop
	}
	if set.GenerationStatus == cq.StatusReady || set.GenerationStatus == cq.StatusFailed {
		return nil // redelivery of a settled set — idempotent ack
	}
	payload := strings.TrimSpace(p.CandidatePayloadJSON)
	if payload == "" {
		return fmt.Errorf("campaign_question_terminal: completed.v1 for %s carried no candidate_payload_json (refusing a fabricated ready)", p.AssistID)
	}
	if err := set.MarkReady([]byte(payload), s.now()); err != nil {
		return fmt.Errorf("campaign_question_terminal: mark ready %s: %w", set.ID, err)
	}
	if err := s.repo.Save(ctx, set); err != nil {
		return fmt.Errorf("campaign_question_terminal: persist ready %s: %w", set.ID, err)
	}
	log.Printf("campaign_question_terminal: question set %s READY (assist=%s, concept=%s rung=%d, quality_warning=%v)",
		set.ID, set.AssistID, set.ConceptKey, set.Rung, p.QualityWarning)
	return nil
}

// HandleRefused fails the set. NO refund — the lane is mana-exempt (D13).
func (s *CampaignQuestionTerminalSubscriber) HandleRefused(ctx context.Context, env events.Envelope, p ProofingRefusedPayload) error {
	set, err := s.resolveSet(ctx, env, p.AssistID)
	if err != nil {
		return err
	}
	if set == nil {
		return nil // foreign job — never touch
	}
	if set.GenerationStatus == cq.StatusReady || set.GenerationStatus == cq.StatusFailed {
		return nil // idempotent redelivery
	}
	reason := strings.TrimSpace(p.RefusalReason)
	if reason == "" {
		reason = "qgen batch refused"
	}
	if msg := strings.TrimSpace(p.UserFacingMessage); msg != "" {
		reason = reason + ": " + msg
	}
	if err := set.MarkFailed(reason, s.now()); err != nil {
		return fmt.Errorf("campaign_question_terminal: mark failed %s: %w", set.ID, err)
	}
	if err := s.repo.Save(ctx, set); err != nil {
		return fmt.Errorf("campaign_question_terminal: persist failed %s: %w", set.ID, err)
	}
	log.Printf("campaign_question_terminal: question set %s FAILED (assist=%s, reason=%q)", set.ID, set.AssistID, reason)
	return nil
}
