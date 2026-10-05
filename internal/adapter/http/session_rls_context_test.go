// session_rls_context_test.go — CHO-2029 live-verify regression (2026-07-03).
//
// The pg AtomSessionRepo runs rls.ApplySession, which fail-louds with
// ErrNoTenantContext when the request context lacks the tracing tenant/gcid
// identity. Every atom-session HTTP lane (ext /v1/me/atom-sessions*, ext
// legacy /sessions*, internal /api/sessions*) passed a bare r.Context() to
// Sessions.Save/Get — green units (inmem repo ignores ctx) + healthy pods,
// yet EVERY live session start 500'd (SESSION_PERSIST_FAILED). Same bug
// class the KG routes already solved at the registration seam
// (withKGRLSContext). These tests pin the seam for the session lanes.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

// rlsRecordingSessions delegates to the real (inmem) repo while recording
// the RLS identity present on the ctx of each Save/Get — the exact values
// rls.ApplySession would read in the pg adapter.
type rlsRecordingSessions struct {
	inner      atom_attempt.Repository
	saveTenant string
	saveGCID   string
	getTenant  string
	getGCID    string
}

func (r *rlsRecordingSessions) Save(ctx context.Context, s *atom_attempt.AtomAttempt) error {
	r.saveTenant = tracing.TenantIDFromContext(ctx)
	r.saveGCID = tracing.GCIDFromContext(ctx)
	return r.inner.Save(ctx, s)
}

func (r *rlsRecordingSessions) Get(ctx context.Context, id string) (*atom_attempt.AtomAttempt, error) {
	r.getTenant = tracing.TenantIDFromContext(ctx)
	r.getGCID = tracing.GCIDFromContext(ctx)
	return r.inner.Get(ctx, id)
}

func requireRLSIdentity(t *testing.T, op, gotTenant, gotGCID, wantTenant, wantGCID string) {
	t.Helper()
	if gotTenant != wantTenant || gotGCID != wantGCID {
		t.Fatalf("%s ctx missing RLS identity: tenant=%q gcid=%q (want %q/%q) — pg repo would fail rls.ApplySession", op, gotTenant, gotGCID, wantTenant, wantGCID)
	}
}

// --- ext /v1/me/atom-sessions (start) ---

func TestPostMeAtomSessions_SaveCtxCarriesRLSIdentity(t *testing.T) {
	srv, h := extServer(t)
	rec := &rlsRecordingSessions{inner: srv.Sessions}
	srv.Sessions = rec

	w := extReq(t, h, http.MethodPost, "/v1/me/atom-sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	requireRLSIdentity(t, "Save(start)", rec.saveTenant, rec.saveGCID, tTenant, tGCID)
}

// --- ext /v1/me/atom-sessions/{id}/answers (submit: Get + Save) ---

func TestPostMeAtomSessionsAnswers_GetAndSaveCtxCarryRLSIdentity(t *testing.T) {
	srv, h := extServer(t)
	rec := &rlsRecordingSessions{inner: srv.Sessions}
	srv.Sessions = rec

	w := extReq(t, h, http.MethodPost, "/v1/me/atom-sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d; body=%s", w.Code, w.Body.String())
	}
	var started map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &started)
	sid, _ := started["session_id"].(string)
	if sid == "" {
		t.Fatalf("no session_id in start response: %s", w.Body.String())
	}

	w = extReq(t, h, http.MethodPost, "/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "a1", "answer": "42"}, extHeaders())
	if w.Code != http.StatusOK {
		t.Fatalf("answers status = %d; body=%s", w.Code, w.Body.String())
	}
	requireRLSIdentity(t, "Get(submit)", rec.getTenant, rec.getGCID, tTenant, tGCID)
	requireRLSIdentity(t, "Save(submit)", rec.saveTenant, rec.saveGCID, tTenant, tGCID)
}

// --- ext legacy /sessions (start) ---

func TestExtLegacySessions_SaveCtxCarriesRLSIdentity(t *testing.T) {
	srv, h := extServer(t)
	rec := &rlsRecordingSessions{inner: srv.Sessions}
	srv.Sessions = rec

	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	requireRLSIdentity(t, "Save(legacy start)", rec.saveTenant, rec.saveGCID, tTenant, tGCID)
}

// --- internal /api/sessions (start) ---

func TestInternalAPISessions_SaveCtxCarriesRLSIdentity(t *testing.T) {
	srv := NewServer()
	rec := &rlsRecordingSessions{inner: srv.Sessions}
	srv.Sessions = rec
	h := srv.Routes()

	buf, _ := json.Marshal(map[string]any{"atom_id": tAtom1})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tTenant)
	req.Header.Set("gcid", tGCID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	requireRLSIdentity(t, "Save(internal start)", rec.saveTenant, rec.saveGCID, tTenant, tGCID)
}
