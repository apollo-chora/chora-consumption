// Package http — S5.1 Maya retention loop endpoint tests.
//
// Tests for `/v1/me/streak` + `/v1/me/xp` (read-only endpoints surfaced
// to the A+ Daily Dose card stack + LearningPath progress sidebar).
//
// TDD strict (RED → GREEN → REFACTOR).
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

func parseDay(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tt
}

// ----- /v1/me/streak -----

func TestMeStreak_NewLearnerReturnsZero(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/streak", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["count"].(float64) != 0 {
		t.Errorf("count = %v, want 0 (new learner)", resp["count"])
	}
	if resp["learner_gcid"] != testGCID {
		t.Errorf("learner_gcid = %v, want %v", resp["learner_gcid"], testGCID)
	}
}

func TestMeStreak_RequiresAuth(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/streak", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMeStreak_RejectsNonGET(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/streak", map[string]any{"count": 999})
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestMeStreak_RLSLeak — cross-user attempts return only the requesting
// learner's data (in-memory; production has RLS at DB layer).
func TestMeStreak_RLSLeak(t *testing.T) {
	srv := NewServer()
	// Boost streak for a DIFFERENT user.
	srv.Streaks.Get(context.Background(), testTenant, "other-learner")
	for d := 0; d < 5; d++ {
		srv.Streaks.RecordActivity(context.Background(), testTenant, "other-learner",
			parseDay(t, "2026-05-01T00:00:00Z").AddDate(0, 0, d))
	}
	w := authedReq(t, srv, http.MethodGet, "/v1/me/streak", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	// testGCID should still see 0; the boost was for "other-learner".
	if resp["count"].(float64) != 0 {
		t.Errorf("RLS leak: testGCID count = %v, want 0", resp["count"])
	}
}

// ----- /v1/me/xp -----

func TestMeXP_NewLearnerReturnsZero(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodGet, "/v1/me/xp", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["xp"].(float64) != 0 {
		t.Errorf("xp = %v, want 0", resp["xp"])
	}
}

func TestMeXP_AccumulatesAcrossActivity(t *testing.T) {
	srv := NewServer()
	srv.XP.Award(context.Background(), testTenant, testGCID, 10)  // atom completed
	srv.XP.Award(context.Background(), testTenant, testGCID, 50)  // dose completed
	srv.XP.Award(context.Background(), testTenant, testGCID, 200) // path milestone

	w := authedReq(t, srv, http.MethodGet, "/v1/me/xp", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["xp"].(float64) != 260 {
		t.Errorf("xp = %v, want 260 (10+50+200)", resp["xp"])
	}
}

func TestMeXP_RequiresAuth(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/xp", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMeXP_RejectsNonGET(t *testing.T) {
	srv := NewServer()
	w := authedReq(t, srv, http.MethodPost, "/v1/me/xp", map[string]any{"xp": 999})
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestMeXP_RLSLeak — cross-user attempts return only the requesting
// learner's XP.
func TestMeXP_RLSLeak(t *testing.T) {
	srv := NewServer()
	srv.XP.Award(context.Background(), testTenant, "other-learner", 9999)
	w := authedReq(t, srv, http.MethodGet, "/v1/me/xp", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["xp"].(float64) != 0 {
		t.Errorf("RLS leak: testGCID xp = %v, want 0", resp["xp"])
	}
}

// ----- daily-dose IMDA evidence emit (S5.1 wiring) -----

// TestDailyDose_EmitsIMDAEvidence — the daily-dose served event MUST
// include IMDA D1 (accountability) + lifecycle_stage=runtime evidence
// per ADR-141.
func TestDailyDose_EmitsIMDAEvidence(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	pub, ok := srv.Publisher.(*events.InMemoryPublisher)
	if !ok {
		t.Fatal("publisher is not *events.InMemoryPublisher")
	}
	found := false
	for _, e := range pub.Events() {
		if !strings.Contains(e.Topic, "daily_dose.served") {
			continue
		}
		found = true
		if e.Payload["chora_imda_dimension"] != "accountability" {
			t.Errorf("missing IMDA D1 evidence: chora_imda_dimension = %v",
				e.Payload["chora_imda_dimension"])
		}
		if e.Payload["imda_lifecycle_stage"] != "runtime" {
			t.Errorf("missing IMDA lifecycle_stage: imda_lifecycle_stage = %v",
				e.Payload["imda_lifecycle_stage"])
		}
	}
	if !found {
		t.Error("no daily_dose.served event published")
	}
}
