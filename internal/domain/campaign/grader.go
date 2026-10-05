// grader.go — the campaign grading service (WS-C1, CHO-2080): folds one
// server-graded answer into the ladder, plants/reviews the node's retention
// row (topic_retention keyed by the node's CONCEPT_KEY — addendum #2: rows
// do NOT exist implicitly; atom topic-tags are a different vocabulary), and
// emits the verified events. Callers arrive in WS-C2 (dose campaign slot)
// and WS-C3 (hex-tap question lane); both feed the same deterministic core.
//
// Ordering is persist-before-emit, matching the platform's goal-graduation
// precedent (state commits in its own tx; the outbox rides a separate
// connection). A post-persist emit failure surfaces as an error — the caller
// retries; ladder folds are idempotent-shaped (a re-graded clear lands as a
// refresher) so retries never double-advance.
package campaign

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// DefaultRetentionSeedStrengthDays seeds a freshly-planted concept-key
// retention row (mirrors the session-completion subscriber's 1-day seed —
// young memories decay fast until reviews strengthen them).
const DefaultRetentionSeedStrengthDays = 1.0

// RungClearedEvent feeds chora.consumption.campaign.rung_cleared.v1.
type RungClearedEvent struct {
	TenantID       string
	LearnerGCID    string
	GoalID         string
	ConceptID      string
	ConceptKey     string
	Rung           Rung
	IsRefresher    bool
	CorrectAnswers int
	ClearedAt      time.Time
}

// NodeWonEvent feeds chora.consumption.campaign.node_won.v1.
type NodeWonEvent struct {
	TenantID    string
	LearnerGCID string
	GoalID      string
	ConceptID   string
	ConceptKey  string
	WonAt       time.Time
}

// EventSink is the verified-stream port the grader emits through (the
// adapter builds envelopes + rides the transactional outbox).
type EventSink interface {
	CampaignRungCleared(ctx context.Context, e RungClearedEvent) error
	CampaignNodeWon(ctx context.Context, e NodeWonEvent) error
}

// GraderConfig wires the grader. Progress, Retention and Events are
// mandatory (fail-loud at construction — no silent no-op grading).
type GraderConfig struct {
	Progress  ProgressRepository
	Retention topic_retention.Repository
	Events    EventSink
	// RungClearCorrect resolves CAMPAIGN_RUNG_CLEAR_CORRECT (0 → default 2).
	// cmd/server reads the env override; the domain never reads env.
	RungClearCorrect int
	// RetentionSeedStrengthDays seeds newly-planted rows (0 → 1.0).
	RetentionSeedStrengthDays float64
}

// Grader is the deterministic campaign grading service.
type Grader struct {
	cfg GraderConfig
}

// NewGrader validates the wiring.
func NewGrader(cfg GraderConfig) (*Grader, error) {
	if cfg.Progress == nil || cfg.Retention == nil || cfg.Events == nil {
		return nil, fmt.Errorf("%w: grader needs Progress + Retention + Events", ErrInvalid)
	}
	if cfg.RungClearCorrect < 0 {
		return nil, fmt.Errorf("%w: RungClearCorrect must be >= 0 (0 = default)", ErrInvalid)
	}
	if cfg.RungClearCorrect == 0 {
		cfg.RungClearCorrect = DefaultRungClearCorrect
	}
	if cfg.RetentionSeedStrengthDays == 0 {
		cfg.RetentionSeedStrengthDays = DefaultRetentionSeedStrengthDays
	}
	return &Grader{cfg: cfg}, nil
}

// RungClearCorrect exposes the resolved server-graded-corrects-to-clear
// threshold (CAMPAIGN_RUNG_CLEAR_CORRECT, defaulted at construction). The
// answers door reports it verbatim as needed_correct without re-reading env
// (cmd/server is the only env layer per feedback_no_inline_config).
func (g *Grader) RungClearCorrect() int { return g.cfg.RungClearCorrect }

// RecordAnswerInput is one server-graded campaign answer. GoalID is the
// attribution goal (BINDING addendum #6 — every campaign event carries it);
// ConceptKey is the node's stable slug (retention key vocabulary).
type RecordAnswerInput struct {
	TenantID    string
	LearnerGCID string
	GoalID      string
	ConceptID   string
	ConceptKey  string
	Rung        Rung
	Correct     bool
	Now         time.Time
}

func (in RecordAnswerInput) validate() error {
	for name, v := range map[string]string{
		"tenant_id": in.TenantID, "learner_gcid": in.LearnerGCID, "goal_id": in.GoalID,
		"concept_id": in.ConceptID, "concept_key": in.ConceptKey,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%w: %s required", ErrInvalid, name)
		}
	}
	if !in.Rung.Valid() {
		return fmt.Errorf("%w: rung %d outside ladder", ErrInvalid, int(in.Rung))
	}
	if in.Now.IsZero() {
		return fmt.Errorf("%w: now required", ErrInvalid)
	}
	return nil
}

// RecordAnswerResult reports the ladder outcome + the node's retention after
// this review (the serve loop's cooling cue reads it).
type RecordAnswerResult struct {
	Outcome    GradeOutcome
	RetentionR float64
}

// RecordAnswer folds one graded answer: ladder → persist → retention
// plant/review → persist → events.
func (g *Grader) RecordAnswer(ctx context.Context, in RecordAnswerInput) (RecordAnswerResult, error) {
	if err := in.validate(); err != nil {
		return RecordAnswerResult{}, err
	}

	p, err := g.cfg.Progress.GetByConcept(ctx, in.TenantID, in.LearnerGCID, in.ConceptID)
	if err != nil {
		return RecordAnswerResult{}, fmt.Errorf("campaign: load progress: %w", err)
	}
	if p == nil {
		// Lazily claim the ladder on first graded contact (D1: subtree nodes
		// are implicitly claimed territory).
		p, err = NewNodeProgress(in.TenantID, in.LearnerGCID, in.ConceptID, in.Now)
		if err != nil {
			return RecordAnswerResult{}, err
		}
	}

	out, err := p.ApplyGraded(in.Rung, in.Correct, g.cfg.RungClearCorrect, in.Now)
	if err != nil {
		return RecordAnswerResult{}, err
	}
	if err := g.cfg.Progress.Save(ctx, p); err != nil {
		return RecordAnswerResult{}, fmt.Errorf("campaign: save progress: %w", err)
	}

	// Plant-or-review the concept-keyed retention row (addendum #2). EVERY
	// graded answer is a review — the Ebbinghaus side carries the penalty
	// for wrong answers; the ladder counter never resets.
	score, err := g.cfg.Retention.Get(ctx, in.TenantID, in.LearnerGCID, in.ConceptKey)
	if err != nil {
		return RecordAnswerResult{}, fmt.Errorf("campaign: load retention: %w", err)
	}
	if score == nil {
		score, err = topic_retention.New(in.TenantID, in.LearnerGCID, in.ConceptKey, in.Now, g.cfg.RetentionSeedStrengthDays)
		if err != nil {
			return RecordAnswerResult{}, fmt.Errorf("campaign: plant retention: %w", err)
		}
	}
	score.Review(in.Correct, in.Now)
	if err := g.cfg.Retention.Save(ctx, score); err != nil {
		return RecordAnswerResult{}, fmt.Errorf("campaign: save retention: %w", err)
	}

	// Verified events, after all state persisted.
	if out.ClearedRung != 0 {
		if err := g.cfg.Events.CampaignRungCleared(ctx, RungClearedEvent{
			TenantID: in.TenantID, LearnerGCID: in.LearnerGCID, GoalID: in.GoalID,
			ConceptID: in.ConceptID, ConceptKey: in.ConceptKey,
			Rung: out.ClearedRung, IsRefresher: false,
			CorrectAnswers: g.cfg.RungClearCorrect, ClearedAt: in.Now,
		}); err != nil {
			return RecordAnswerResult{}, fmt.Errorf("campaign: emit rung_cleared: %w", err)
		}
	} else if out.IsRefresher && in.Correct {
		if err := g.cfg.Events.CampaignRungCleared(ctx, RungClearedEvent{
			TenantID: in.TenantID, LearnerGCID: in.LearnerGCID, GoalID: in.GoalID,
			ConceptID: in.ConceptID, ConceptKey: in.ConceptKey,
			Rung: in.Rung, IsRefresher: true,
			CorrectAnswers: 1, ClearedAt: in.Now,
		}); err != nil {
			return RecordAnswerResult{}, fmt.Errorf("campaign: emit refresher rung_cleared: %w", err)
		}
	}
	if out.Won {
		if err := g.cfg.Events.CampaignNodeWon(ctx, NodeWonEvent{
			TenantID: in.TenantID, LearnerGCID: in.LearnerGCID, GoalID: in.GoalID,
			ConceptID: in.ConceptID, ConceptKey: in.ConceptKey, WonAt: in.Now,
		}); err != nil {
			return RecordAnswerResult{}, fmt.Errorf("campaign: emit node_won: %w", err)
		}
	}

	return RecordAnswerResult{Outcome: out, RetentionR: score.RetentionAt(in.Now)}, nil
}

// ServeDecisionFor computes the deterministic campaign serve for a node
// (WS-C2's slot picker + WS-C3's hex-tap lane both call this): ladder state
// + the concept-key retention row → DecideServe.
func (g *Grader) ServeDecisionFor(ctx context.Context, tenantID, learnerGCID, conceptID, conceptKey string, now time.Time) (ServeDecision, error) {
	p, err := g.cfg.Progress.GetByConcept(ctx, tenantID, learnerGCID, conceptID)
	if err != nil {
		return ServeDecision{}, fmt.Errorf("campaign: load progress: %w", err)
	}
	score, err := g.cfg.Retention.Get(ctx, tenantID, learnerGCID, conceptKey)
	if err != nil {
		return ServeDecision{}, fmt.Errorf("campaign: load retention: %w", err)
	}
	var r *float64
	if score != nil {
		v := score.RetentionAt(now)
		r = &v
	}
	return DecideServe(p, r, now)
}
