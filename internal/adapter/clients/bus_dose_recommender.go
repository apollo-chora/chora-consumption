// bus_dose_recommender.go - ADR-254 D4: the daily-dose recommender on the BUS.
//
// The AI picks for a daily dose ride the caller-facing lane
//
//	chora.consumption.dose_recommendation.requested.v1  (dose_request_id, goal_id,
//	    trigger, companion_id, context_json: the recommender's inputs)
//	chora.consumption.dose_recommendation.completed.v1  (recommended_atom_ids[],
//	    rationale, generated_by_model_id, status, error_code, workflow_id)
//
// BusDoseRecommender implements RecommenderEnginePort: the daily-dose/ai
// handler keeps its shape (publish, wait, map) and its graceful degradation
// (an error here = templated copy, never a 5xx). The request is the same
// turn-store idiom as the chat lane (turn row + outbox request in one tx,
// kind dose), so the dose completion consumer and the replay/ops reads share
// one table.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ErrDoseRecommendationTimeout is returned when the kennel's result did not
// arrive within the deadline (the turn row is reaped as timeout).
var ErrDoseRecommendationTimeout = errors.New("bus dose recommender: recommendation timed out")

// DoseRecommendationTrigger is the request trigger value for the async
// daily-dose AI enrichment (GET /companion/daily-dose/ai).
const DoseRecommendationTrigger = "daily_dose_ai"

// DefaultDoseRecommendationDeadline matches the handler's enrichment budget.
const DefaultDoseRecommendationDeadline = 30 * time.Second

// BusDoseRecommenderConfig wires the recommender.
type BusDoseRecommenderConfig struct {
	Store        companion.TurnStore
	Publish      TurnRequestPublisher
	Deadline     time.Duration
	PollInterval time.Duration
	Now          func() time.Time
}

// BusDoseRecommender is the production RecommenderEnginePort.
type BusDoseRecommender struct {
	cfg BusDoseRecommenderConfig
}

var _ RecommenderEnginePort = (*BusDoseRecommender)(nil)

// NewBusDoseRecommender validates the wiring and applies defaults.
func NewBusDoseRecommender(cfg BusDoseRecommenderConfig) (*BusDoseRecommender, error) {
	if cfg.Store == nil {
		return nil, errors.New("bus dose recommender: TurnStore required")
	}
	if cfg.Publish == nil {
		return nil, errors.New("bus dose recommender: TurnRequestPublisher required")
	}
	if cfg.Deadline <= 0 {
		cfg.Deadline = DefaultDoseRecommendationDeadline
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultTurnPollInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &BusDoseRecommender{cfg: cfg}, nil
}

// doseContext is context_json: everything the recommender used to receive in
// its session state (RecommenderEngineClient.Recommend), unchanged in meaning.
type doseContext struct {
	ManaTier       string          `json:"mana_tier"`
	LearnerPersona string          `json:"learner_persona,omitempty"`
	UserPrompt     string          `json:"user_prompt,omitempty"`
	TopicHint      string          `json:"topic_hint,omitempty"`
	Candidates     []AtomCandidate `json:"candidates"`
}

// Recommend publishes one dose_recommendation request and blocks for its result.
func (r *BusDoseRecommender) Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error) {
	if strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.UserGCID) == "" {
		return RecommendResponse{}, errors.New("bus dose recommender: tenant_id + user_gcid required")
	}
	tier := strings.TrimSpace(req.ManaTier)
	if tier == "" {
		tier = "basic"
	}
	candidates := req.Candidates
	if candidates == nil {
		candidates = []AtomCandidate{}
	}
	ctxJSON, err := json.Marshal(doseContext{
		ManaTier: tier, LearnerPersona: req.LearnerPersona, UserPrompt: req.UserPrompt,
		TopicHint: req.TopicHint, Candidates: candidates,
	})
	if err != nil {
		return RecommendResponse{}, fmt.Errorf("bus dose recommender: context_json: %w", err)
	}
	now := r.cfg.Now().UTC()
	doseRequestID := domain.NewUUIDv7()
	payload := map[string]any{
		"dose_request_id": doseRequestID,
		"tenant_id":       req.TenantID,
		"gcid":            req.UserGCID,
		"trigger":         DoseRecommendationTrigger,
		"context_json":    string(ctxJSON),
		"requested_at":    now.Format(time.RFC3339Nano),
	}
	body, _ := json.Marshal(payload)
	turn, err := companion.NewTurn(companion.NewTurnInput{
		TurnID:    doseRequestID,
		TenantID:  req.TenantID,
		OwnerGCID: req.UserGCID,
		Kind:      companion.TurnKindDose,
		Lane:      companion.TurnLaneDoseRecommendation,
		Request:   body,
		Now:       now,
		Deadline:  r.cfg.Deadline,
	})
	if err != nil {
		return RecommendResponse{}, fmt.Errorf("bus dose recommender: %w", err)
	}
	traceparent := TraceparentFromContext(ctx)
	if traceparent == "" {
		traceparent = syntheticTraceparent(doseRequestID)
	}
	env := events.NewEnvelope(req.TenantID, req.UserGCID, traceparent, "", doseRequestID)
	if err := r.cfg.Store.Begin(ctx, turn, func(ctx context.Context) error {
		return r.cfg.Publish.PublishInTx(ctx, events.TopicDoseRecommendationRequested, env, payload)
	}); err != nil {
		return RecommendResponse{}, fmt.Errorf("bus dose recommender: begin %s: %w", doseRequestID, err)
	}

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		t, gerr := r.cfg.Store.Get(ctx, req.TenantID, doseRequestID)
		if gerr == nil {
			if t.IsTerminal() {
				return r.mapResult(t)
			}
			if n := r.cfg.Now().UTC(); !n.Before(t.DeadlineAt) {
				if _, terr := r.cfg.Store.Timeout(ctx, req.TenantID, doseRequestID, n); terr == nil {
					if again, aerr := r.cfg.Store.Get(ctx, req.TenantID, doseRequestID); aerr == nil && again.IsTerminal() {
						return r.mapResult(again)
					}
				}
			}
		} else if ctx.Err() != nil {
			return RecommendResponse{}, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return RecommendResponse{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *BusDoseRecommender) mapResult(t *companion.Turn) (RecommendResponse, error) {
	switch t.Status {
	case companion.TurnStatusCompleted:
		parsed, err := companion.ParseTurnResultJSON(t.Lane, t.Result)
		if err != nil {
			return RecommendResponse{}, fmt.Errorf("bus dose recommender: stored result of %s unreadable: %w", t.TurnID, err)
		}
		res := parsed.Result
		model := res.GeneratedByModelID
		if model == "" {
			model = t.GeneratedByModelID
		}
		return RecommendResponse{
			AtomIDs:       res.RecommendedAtomIDs,
			NarrativeOnly: len(res.RecommendedAtomIDs) == 0 && res.Rationale != "",
			Narrative:     res.Rationale,
			Model:         model,
			FinishReason:  "stop",
		}, nil
	case companion.TurnStatusTimeout:
		return RecommendResponse{}, fmt.Errorf("%w: %s", ErrDoseRecommendationTimeout, t.TurnID)
	default:
		return RecommendResponse{}, fmt.Errorf("bus dose recommender: %s (%s: %s)", t.CallerErrorCode(), t.Status, t.ErrorCode)
	}
}
