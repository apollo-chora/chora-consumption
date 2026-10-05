// atom_attempt_handler.go - EXT scope AtomAttempt state-machine handlers.
//
// Endpoints:
//
//	POST   /sessions                   start
//	PATCH  /sessions/{id}/answer       submit
//	POST   /sessions/{id}:abandon      abandon
//	GET    /sessions/{id}              detail
//
// State transitions are enforced by domain/atom_attempt - invalid
// transitions surface as 409 Conflict; over-cap hint usage is also 409.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

// ---------- payloads ----------

type extStartSessionReq struct {
	AtomID string `json:"atom_id"`
}

type extAnswerReq struct {
	Answer   string `json:"answer"`
	HintUsed bool   `json:"hint_used"`
	Correct  bool   `json:"correct"`
}

type extSessionResp struct {
	SessionID   string  `json:"session_id"`
	TenantID    string  `json:"tenant_id"`
	LearnerGCID string  `json:"learner_gcid"`
	AtomID      string  `json:"atom_id"`
	Status      string  `json:"status"`
	HintsUsed   int     `json:"hints_used"`
	AnswerCount int     `json:"answer_count"`
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at,omitempty"`
	AbandonedAt *string `json:"abandoned_at,omitempty"`
}

func toExtSessionResp(s *atom_attempt.AtomAttempt) extSessionResp {
	resp := extSessionResp{
		SessionID:   s.SessionID,
		TenantID:    s.TenantID,
		LearnerGCID: s.LearnerGCID,
		AtomID:      s.AtomID,
		Status:      string(s.Status),
		HintsUsed:   s.HintsUsed,
		AnswerCount: s.AnswerCount,
		StartedAt:   s.StartedAt.Format("2006-01-02T15:04:05.000Z"),
	}
	if s.CompletedAt != nil {
		ts := s.CompletedAt.Format("2006-01-02T15:04:05.000Z")
		resp.CompletedAt = &ts
	}
	if s.AbandonedAt != nil {
		ts := s.AbandonedAt.Format("2006-01-02T15:04:05.000Z")
		resp.AbandonedAt = &ts
	}
	return resp
}

// ---------- dispatcher ----------

func (s *ExtServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	s.startSession(w, r)
}

// handleSessionByID dispatches:
//
//	GET    /sessions/{id}
//	PATCH  /sessions/{id}/answer
//	POST   /sessions/{id}:abandon
//
// The verb-style suffix `:abandon` is non-RPC HTTP convention used by
// Google's REST API guidelines for non-CRUD actions.
func (s *ExtServer) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/sessions/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_SESSION_ID", "")
		return
	}

	// {id}:abandon — verb suffix.
	if strings.Contains(rest, ":") {
		colonIdx := strings.LastIndex(rest, ":")
		id := rest[:colonIdx]
		verb := rest[colonIdx+1:]
		if verb == "abandon" {
			if r.Method != http.MethodPost {
				extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
				return
			}
			s.abandonSession(w, r, id)
			return
		}
		extWriteError(w, http.StatusNotFound, "UNKNOWN_VERB", "")
		return
	}

	// {id} or {id}/answer.
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.getSession(w, r, id)
		return
	}
	if parts[1] == "answer" {
		if r.Method != http.MethodPatch {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.submitAnswer(w, r, id)
		return
	}
	extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
}

// ---------- handlers ----------

func (s *ExtServer) startSession(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req extStartSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	sess, err := atom_attempt.Start(tenantID, gcid, req.AtomID, atom_attempt.SystemClock)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "INVALID_START", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, sess.SessionID)
	if err := s.Publisher.Publish(events.TopicAtomSessionStarted, env, map[string]any{
		"session_id":   sess.SessionID,
		"learner_gcid": gcid,
		"atom_id":      sess.AtomID,
		"started_at":   sess.StartedAt,
	}); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	extWriteJSON(w, http.StatusCreated, toExtSessionResp(sess))
}

func (s *ExtServer) submitAnswer(w http.ResponseWriter, r *http.Request, id string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	sess, err := s.Sessions.Get(r.Context(), id)
	if errors.Is(err, atom_attempt.ErrNotFound) {
		extWriteError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "")
		return
	}
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_READ_FAILED", err.Error())
		return
	}
	var req extAnswerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	res, err := sess.SubmitAnswer(req.Answer, req.HintUsed, req.Correct, atom_attempt.SystemClock)
	if err != nil {
		// State machine errors → 409.
		if errors.Is(err, atom_attempt.ErrInvalidTransition) || errors.Is(err, atom_attempt.ErrHintsExceeded) {
			extWriteError(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
			return
		}
		extWriteError(w, http.StatusBadRequest, "SUBMIT_FAILED", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}

	// Publish completed event when transition lands on COMPLETED.
	if res.Status == atom_attempt.StatusCompleted {
		traceparent, tracestate := extTraceFromHeaders(r)
		env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, sess.SessionID+"|completed")
		if err := s.Publisher.Publish(events.TopicAtomSessionCompletedV1, env, map[string]any{
			"session_id":     sess.SessionID,
			"learner_gcid":   gcid,
			"atom_id":        sess.AtomID,
			"answer_correct": res.Correct,
			"hints_used":     sess.HintsUsed,
			"answer_count":   sess.AnswerCount,
			"completed_at":   sess.CompletedAt,
			"source_action":  "ext", // disambiguates from companion SM-2 flow
		}); err != nil {
			extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
	}

	extWriteJSON(w, http.StatusOK, toExtSessionResp(sess))
}

func (s *ExtServer) abandonSession(w http.ResponseWriter, r *http.Request, id string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	sess, err := s.Sessions.Get(r.Context(), id)
	if errors.Is(err, atom_attempt.ErrNotFound) {
		extWriteError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "")
		return
	}
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_READ_FAILED", err.Error())
		return
	}
	if err := sess.Abandon(atom_attempt.SystemClock); err != nil {
		if errors.Is(err, atom_attempt.ErrInvalidTransition) {
			extWriteError(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
			return
		}
		extWriteError(w, http.StatusBadRequest, "ABANDON_FAILED", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, sess.SessionID+"|abandoned")
	if err := s.Publisher.Publish(events.TopicAtomSessionAbandoned, env, map[string]any{
		"session_id":   sess.SessionID,
		"learner_gcid": gcid,
		"atom_id":      sess.AtomID,
		"abandoned_at": sess.AbandonedAt,
	}); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	extWriteJSON(w, http.StatusOK, toExtSessionResp(sess))
}

func (s *ExtServer) getSession(w http.ResponseWriter, r *http.Request, id string) {
	sess, err := s.Sessions.Get(r.Context(), id)
	if errors.Is(err, atom_attempt.ErrNotFound) {
		extWriteError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "")
		return
	}
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_READ_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, toExtSessionResp(sess))
}
