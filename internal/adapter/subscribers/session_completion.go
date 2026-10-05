// session_completion.go — in-process side-effect handler for
// `chora.consumption.atom_session.completed.v1`.
//
// Triggered by the AtomAttempt HTTP handler immediately AFTER
// publishing the completed event. Within chora-consumption (same DB so
// no Pub/Sub round-trip needed), the side-effects are:
//
//   - Advance every LearningPath the learner owns that contains the atom
//   - Update TopicRetention scoring per topic_tag of the atom
//   - Emit chora.consumption.learning_path.advanced.v1 (idempotent)
//   - Emit chora.consumption.learning_path.completed.v1 when path closes
//   - Emit IMDA accountability evidence (D1 — learning record)
//
// M12+ promotes this into a Pub/Sub Push subscriber inside
// chora-consumption (same domain, no cross-domain hop). The interface
// stays stable.
package subscribers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// DefaultTopicStrengthDays is the seed strength assigned to a fresh
// (gcid, topic) score when the atom is first reviewed. 1.0 day → R(t)
// halves at t=ln(2)*1d ≈ 16.6 hours after the first correct review.
const DefaultTopicStrengthDays = 1.0

// SessionCompletedInput captures the fields needed for the side-effect
// fan-out. Mirrors the payload of
// `chora.consumption.atom_session.completed.v1` minus envelope fields.
type SessionCompletedInput struct {
	TenantID    string
	LearnerGCID string
	AtomID      string
	IsCorrect   bool
	SessionID   string
	Traceparent string
	Tracestate  string
	OccurredAt  time.Time
}

// SessionCompletionResult is returned by Handle for handler-side
// telemetry / 200 OK response shaping.
type SessionCompletionResult struct {
	PathsAdvanced  int  // count of LearningPaths that advanced
	PathsCompleted int  // count of LearningPaths that completed in this call
	TopicScored    bool // true when at least one TopicScore was updated
	IMDAEvidenceID string
}

// SessionCompletionHandler fans out completion side-effects.
type SessionCompletionHandler struct {
	paths     learning_path.Repo
	atoms     atom_index.Repo
	retention topic_retention.Repository
	publisher events.Publisher
}

// NewSessionCompletionHandler constructs the handler.
func NewSessionCompletionHandler(paths learning_path.Repo, atoms atom_index.Repo, retention topic_retention.Repository, publisher events.Publisher) *SessionCompletionHandler {
	return &SessionCompletionHandler{
		paths:     paths,
		atoms:     atoms,
		retention: retention,
		publisher: publisher,
	}
}

// Handle fans out side-effects + emits derived events. ctx carries the RLS
// tenant session for the pg-backed atom_index repo.
func (h *SessionCompletionHandler) Handle(ctx context.Context, in SessionCompletedInput) (SessionCompletionResult, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return SessionCompletionResult{}, errors.New("session_completion: tenant_id required")
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return SessionCompletionResult{}, errors.New("session_completion: learner_gcid required")
	}
	if strings.TrimSpace(in.AtomID) == "" {
		return SessionCompletionResult{}, errors.New("session_completion: atom_id required")
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}

	res := SessionCompletionResult{}
	atomEntry, _ := h.atoms.Get(ctx, in.AtomID) // tolerate missing — out-of-order Pub/Sub

	// Path advancement is gated on `is_correct=true` for the Straight-Up
	// linear cert mode (only correct answers advance the cert).
	if in.IsCorrect {
		paths, lerr := h.paths.ListByLearnerWithAtom(ctx, in.TenantID, in.LearnerGCID, in.AtomID)
		if lerr != nil {
			return res, lerr
		}
		for _, p := range paths {
			r, err := p.Advance(in.AtomID, in.OccurredAt)
			// Two sentinels are correct NO-OPS for the cursor lane, not failures:
			//
			//   ErrAtomNotInPath      — replay / out-of-order tolerance.
			//   ErrSpacedPathNoCursor — ADR-233 D3: the path is a spaced study
			//     list, whose cursor is INERT (SM-2 + Ebbinghaus schedule it off
			//     sm2_states). Skip it. Treating this as fatal would NACK every
			//     completion of any atom that also sits in a study list, sending
			//     the whole retention/XP pipeline to the DLQ.
			//
			// Any OTHER error still fails loud.
			if err != nil &&
				!errors.Is(err, learning_path.ErrAtomNotInPath) &&
				!errors.Is(err, learning_path.ErrSpacedPathNoCursor) {
				return res, err
			}
			if r.Advanced {
				res.PathsAdvanced++
				if err := h.paths.Save(ctx, p); err != nil {
					return res, err
				}
				if err := h.publishAdvanced(in, p); err != nil {
					return res, err
				}
				if r.Completed {
					res.PathsCompleted++
					if err := h.publishCompleted(in, p); err != nil {
						return res, err
					}
				}
			}
		}
	}

	// TopicRetention update — works even on incorrect answers (incorrect
	// = "I just reviewed but failed" = strength shrinks).
	if atomEntry != nil {
		topic := atomEntry.PrimaryTopic()
		if topic != "" {
			score, err := h.retention.Get(ctx, in.TenantID, in.LearnerGCID, topic)
			if err != nil {
				return res, err // fail-loud: a real read failure must not be masked as "fresh score"
			}
			if score == nil {
				score, err = topic_retention.New(in.TenantID, in.LearnerGCID, topic, in.OccurredAt, DefaultTopicStrengthDays)
				if err != nil {
					return res, err
				}
			}
			score.Review(in.IsCorrect, in.OccurredAt)
			if err := h.retention.Save(ctx, score); err != nil {
				return res, err
			}
			res.TopicScored = true
		}
	}

	// IMDA D1 accountability evidence — emit one envelope per session
	// completion. The chora_imda_dimension key on the payload makes this
	// queryable by the O+ governance dashboard.
	imdaEnv := events.NewEnvelope(in.TenantID, in.LearnerGCID, in.Traceparent, in.Tracestate, "imda:"+in.SessionID)
	res.IMDAEvidenceID = imdaEnv.EventID
	if err := h.publisher.Publish(events.TopicAtomSessionCompletedV1, imdaEnv, map[string]any{
		"chora_imda_dimension": "accountability",
		"session_id":           in.SessionID,
		"atom_id":              in.AtomID,
		"learner_gcid":         in.LearnerGCID,
		"answer_correct":       in.IsCorrect, // canonical proto field (atom_session.proto:65); was "is_correct" — drift (F3 growth-EXP key reconcile)
		"paths_advanced":       res.PathsAdvanced,
		"paths_completed":      res.PathsCompleted,
		"topic_scored":         res.TopicScored,
		"source_action":        "imda_evidence",
	}); err != nil {
		return res, err
	}

	return res, nil
}

func (h *SessionCompletionHandler) publishAdvanced(in SessionCompletedInput, p *learning_path.LearningPath) error {
	env := events.NewEnvelope(in.TenantID, in.LearnerGCID, in.Traceparent, in.Tracestate, "lp_advance:"+p.PathID+":"+in.AtomID)
	return h.publisher.Publish(events.TopicLearningPathAdvanced, env, map[string]any{
		"path_id":          p.PathID,
		"course_id":        p.CourseID,
		"learner_gcid":     in.LearnerGCID,
		"atom_id":          in.AtomID,
		"current_index":    p.CurrentIndex,
		"total_atoms":      len(p.AtomIDs),
		"progress_percent": p.ProgressPercent(),
	})
}

func (h *SessionCompletionHandler) publishCompleted(in SessionCompletedInput, p *learning_path.LearningPath) error {
	env := events.NewEnvelope(in.TenantID, in.LearnerGCID, in.Traceparent, in.Tracestate, "lp_complete:"+p.PathID)
	return h.publisher.Publish(events.TopicLearningPathCompleted, env, map[string]any{
		"path_id":      p.PathID,
		"course_id":    p.CourseID,
		"learner_gcid": in.LearnerGCID,
		"atom_count":   len(p.AtomIDs),
		"completed_at": p.CompletedAt,
	})
}
