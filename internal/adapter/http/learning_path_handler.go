// learning_path_handler.go — EXT scope LearningPath REST handlers.
//
// Endpoints:
//
//	POST /learning-paths
//	POST /learning-paths/{id}/enrollments
//	GET  /learning-paths/{id}/progress?gcid=X
//
// Mandatory request headers (multi-tenant + observability):
//
//	X-Tenant-Id   tenant UUIDv7
//	gcid          learner UUIDv7
//	traceparent   W3C trace context (mandatory per CLAUDE.md §6 — propagated
//	              into Pub/Sub event envelopes)
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ---------- payloads ----------

type extCreatePathReq struct {
	Title   string   `json:"title"`
	AtomIDs []string `json:"atom_ids"`
}

type extPathResp struct {
	PathID    string   `json:"path_id"`
	TenantID  string   `json:"tenant_id"`
	OwnerGCID string   `json:"owner_gcid"`
	Title     string   `json:"title"`
	AtomIDs   []string `json:"atom_ids"`
}

type extEnrollResp struct {
	EnrollmentID string `json:"enrollment_id"`
	TenantID     string `json:"tenant_id"`
	PathID       string `json:"path_id"`
	LearnerGCID  string `json:"learner_gcid"`
}

type extProgressResp struct {
	PathID          string  `json:"path_id"`
	LearnerGCID     string  `json:"learner_gcid"`
	PercentComplete float32 `json:"percent_complete"`
	Completed       int     `json:"completed_atoms"`
	Total           int     `json:"total_atoms"`
}

// ---------- dispatcher ----------

// handleLearningPaths dispatches POST /learning-paths.
func (s *ExtServer) handleLearningPaths(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	s.createLearningPath(w, r)
}

// handleLearningPathByID dispatches /learning-paths/{id} subroutes:
//
//	POST /learning-paths/{id}/enrollments
//	GET  /learning-paths/{id}/progress
func (s *ExtServer) handleLearningPathByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/learning-paths/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "")
		return
	}
	pathID := parts[0]
	if len(parts) == 1 {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
		return
	}
	switch parts[1] {
	case "enrollments":
		if r.Method != http.MethodPost {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.enrollLearner(w, r, pathID)
	case "progress":
		if r.Method != http.MethodGet {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.getProgress(w, r, pathID)
	default:
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
	}
}

// ---------- handlers ----------

func (s *ExtServer) createLearningPath(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req extCreatePathReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	p, err := learning_path.New(tenantID, gcid, req.Title, req.AtomIDs)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "INVALID_PATH", err.Error())
		return
	}
	// pg Paths repo is RLS-bound (rls.ApplySession reads tenant/gcid from ctx).
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	if err := s.Paths.Save(ctx, p); err != nil {
		extWriteError(w, http.StatusInternalServerError, "PATH_SAVE_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusCreated, extPathResp{
		PathID:    p.PathID,
		TenantID:  p.TenantID,
		OwnerGCID: p.OwnerGCID,
		Title:     p.Title,
		AtomIDs:   p.AtomIDs,
	})
}

func (s *ExtServer) enrollLearner(w http.ResponseWriter, r *http.Request, pathID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	if _, err := s.Paths.Get(ctx, pathID); err != nil {
		extWriteError(w, http.StatusNotFound, "PATH_NOT_FOUND", "")
		return
	}
	e, err := learning_path.Enroll(tenantID, pathID, gcid)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "INVALID_ENROLLMENT", err.Error())
		return
	}
	s.Enrollments.Save(e)

	// Publish enrollment.created event.
	traceparent, tracestate := extTraceFromHeaders(r)
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, e.EnrollmentID)
	if err := s.Publisher.Publish(events.TopicLearningPathEnrollmentCreated, env, map[string]any{
		"enrollment_id": e.EnrollmentID,
		"path_id":       pathID,
		"learner_gcid":  gcid,
	}); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	extWriteJSON(w, http.StatusCreated, extEnrollResp{
		EnrollmentID: e.EnrollmentID,
		TenantID:     e.TenantID,
		PathID:       e.PathID,
		LearnerGCID:  e.LearnerGCID,
	})
}

func (s *ExtServer) getProgress(w http.ResponseWriter, r *http.Request, pathID string) {
	queryGCID := r.URL.Query().Get("gcid")
	if queryGCID == "" {
		// Fall back to header gcid (parity with admin tooling) but require
		// at least one source.
		queryGCID = r.Header.Get("gcid")
	}
	if queryGCID == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_GCID", "gcid query param required")
		return
	}
	// RLS: Paths.Get runs rls.ApplySession(ctx), which reads the tenant from the
	// CONTEXT. The gateway stamps X-Tenant-Id for the JWT-gated learning-paths
	// routes, so require + wrap it — without this the path read errors
	// ErrNoTenantContext and progress 404s on EVERY prod call. learning_paths RLS
	// is tenant-only (0001_initial), so WithTenantID suffices; the gcid query
	// param identifies the queried learner (enrollment lookup), not the RLS scope.
	// (CHO-1624 RLS-ctx sweep.)
	tenantID := r.Header.Get("X-Tenant-Id")
	if tenantID == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", "X-Tenant-Id required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	p, err := s.Paths.Get(ctx, pathID)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "PATH_NOT_FOUND", "")
		return
	}
	e, err := s.Enrollments.GetByPathAndGCID(pathID, queryGCID)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "ENROLLMENT_NOT_FOUND", "")
		return
	}
	pct := e.Progress(p.AtomIDs)
	completed := 0
	pathSet := make(map[string]struct{}, len(p.AtomIDs))
	for _, a := range p.AtomIDs {
		pathSet[a] = struct{}{}
	}
	for _, c := range e.CompletedAtomIDs {
		if _, ok := pathSet[c]; ok {
			completed++
		}
	}
	extWriteJSON(w, http.StatusOK, extProgressResp{
		PathID:          pathID,
		LearnerGCID:     queryGCID,
		PercentComplete: pct,
		Completed:       completed,
		Total:           len(p.AtomIDs),
	})
}

// ---------- helpers ----------

func extRequireContext(r *http.Request) (tenantID, gcid string, err error) {
	tenantID = r.Header.Get("X-Tenant-Id")
	gcid = r.Header.Get("gcid")
	if tenantID == "" {
		return "", "", errors.New("X-Tenant-Id required")
	}
	if gcid == "" {
		return "", "", errors.New("gcid required")
	}
	return tenantID, gcid, nil
}

// extTraceFromHeaders returns the W3C trace context to stamp on an outbound
// event envelope.
//
// The id is taken from the LIVE OTel span context that
// chora-go-common/tracing.Middleware started for this request, the span
// Cloud Trace actually exported. Reading the inbound header instead would
// reference a span id this service never emitted, and a consumer continuing
// the trace from the envelope would be parented to a phantom.
//
// ⚠ This function used to fall back to "00-0af7651916cd43dd8448eb211c80319c-
// b7ad6b7169203331-00", the traceparent printed as an EXAMPLE in the W3C
// spec. That is a constant, so every request that arrived without a header
// collapsed onto one shared trace id, 99 unrelated spans on a single trace
// in Cloud Trace. Fixed 2026-08-07 (FINDING-02). A fallback here must always
// be UNIQUE per request, never a literal.
func extTraceFromHeaders(r *http.Request) (string, string) {
	ts := r.Header.Get("tracestate")
	if sc := oteltrace.SpanContextFromContext(r.Context()); sc.IsValid() {
		return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + sc.TraceFlags().String(), ts
	}
	// No live span (SDK not wired, or a unit test calling the handler
	// directly). EnsureTraceparent forwards a structurally valid inbound
	// header and otherwise mints a fresh crypto/rand root.
	return tracing.EnsureTraceparent(r.Header.Get("traceparent")), ts
}
