// rls_leak_test.go — multi-tenant RLS leak guard tests for the
// /v1/me/* endpoints.
//
// The handler-side defence is "verify (tenant_id, gcid) on every read".
// Production (M12+) layers this with Postgres RLS policies on
// chora_consumption tables — the handler check is the FIRST line of
// defence (per multi-tenant-rls skill: "RLS handles cross-tenant within
// each domain"). These tests confirm the handler returns 404 (not 200,
// not 403, not the row) on cross-tenant + cross-learner attempts.
package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// TestRLS_LearningPath_CrossLearnerSameTenantReturns404 — same tenant,
// different GCID → 404.
func TestRLS_LearningPath_CrossLearnerSameTenantReturns404(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:    meTenantID,
		LearnerGCID: "owner-gcid",
		CourseID:    meCourseID,
		AtomIDs:     []string{meAtomID},
		Now:         now,
	})
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths/"+p.PathID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", "different-gcid") // ← attacker
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (cross-learner same-tenant leak)", w.Code)
	}
}

// TestRLS_AtomSession_CrossLearnerReturns404 — answer-submit attempt by
// a different GCID than the session owner.
func TestRLS_AtomSession_CrossLearnerReturns404(t *testing.T) {
	srv := newMeServer()
	clk := atom_attempt.SystemClock
	sess, _ := atom_attempt.Start(meTenantID, "owner-gcid", meAtomID, clk)
	srv.Sessions.Save(context.Background(), sess)

	r := httptest.NewRequest("POST", "/v1/me/atom-sessions/"+sess.SessionID+"/answers", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", "attacker-gcid")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (cross-learner session)", w.Code)
	}
}

// TestRLS_TopicRetention_CrossLearnerExcludedFromList — listing only
// returns the requesting GCID's scores.
func TestRLS_TopicRetention_CrossLearnerExcludedFromList(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	mineA, _ := topic_retention.New(meTenantID, meGCID, "agile", now, 1.0)
	_ = srv.Retention.Save(context.Background(), mineA)
	otherA, _ := topic_retention.New(meTenantID, "other-gcid", "agile", now, 1.0)
	_ = srv.Retention.Save(context.Background(), otherA)
	otherTenant, _ := topic_retention.New("other-tenant", meGCID, "agile", now, 1.0)
	_ = srv.Retention.Save(context.Background(), otherTenant)

	r := httptest.NewRequest("GET", "/v1/me/topic-retention", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	// "other-gcid" must not leak; "other-tenant" must not leak.
	for _, leak := range []string{"other-gcid", "other-tenant"} {
		if containsString(body, leak) {
			t.Errorf("response leaks %q: %s", leak, body)
		}
	}
}

// TestRLS_MissingTenantIDIs400 — every /v1/me/* endpoint MUST require
// X-Tenant-Id (not just gcid).
func TestRLS_MissingTenantIDIs400(t *testing.T) {
	srv := newMeServer()
	r := httptest.NewRequest("GET", "/v1/me/learning-paths", nil)
	r.Header.Set("gcid", meGCID)
	// X-Tenant-Id intentionally missing.
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing tenant)", w.Code)
	}
}

// TestRLS_MissingGCIDIs400 — every /v1/me/* endpoint MUST require gcid.
func TestRLS_MissingGCIDIs400(t *testing.T) {
	srv := newMeServer()
	r := httptest.NewRequest("GET", "/v1/me/topic-retention", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	// gcid intentionally missing.
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing gcid)", w.Code)
	}
}

// ---------- KG (S5.2) ----------

// TestRLS_KGCluster_CrossTenantReturns404 — different tenant must not
// see another tenant's KG cluster, even with matching gcid.
func TestRLS_KGCluster_CrossTenantReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraphRLSCluster("other-tenant", meGCID, meAtomID)
	_ = srv.KGClusters.Save(rlsContext(), c)

	r := httptest.NewRequest("GET", "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID) // attacker's tenant
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (cross-tenant)", w.Code)
	}
}

// TestRLS_KGCluster_ListExcludesOtherUsers — listing returns only the
// caller's clusters; other-user rows must not leak.
func TestRLS_KGCluster_ListExcludesOtherUsers(t *testing.T) {
	srv := NewExtServer(nil)
	mine, _ := userknowledgegraphRLSCluster(meTenantID, meGCID, meAtomID)
	_ = srv.KGClusters.Save(rlsContext(), mine)
	other, _ := userknowledgegraphRLSCluster(meTenantID, "other-gcid", meAtomID)
	_ = srv.KGClusters.Save(rlsContext(), other)

	r := httptest.NewRequest("GET", "/v1/me/knowledge-graph/clusters", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if containsString(body, "other-gcid") {
		t.Errorf("response leaks other-gcid: %s", body)
	}
}

// userknowledgegraphRLSCluster constructs a MapCluster for the RLS
// leak tests without dragging the user_knowledge_graph package into
// the rls_leak_test surface (we still need a valid cluster, so we
// import via the same path the kg_handler tests use).
func userknowledgegraphRLSCluster(tenantID, gcid, atomID string) (*userknowledgegraph.MapCluster, error) {
	return userknowledgegraph.NewMapCluster(tenantID, gcid, "test-topic", atomID, "")
}

// rlsContext returns a non-nil context for repo operations in RLS tests.
func rlsContext() context.Context { return context.Background() }

func containsString(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
