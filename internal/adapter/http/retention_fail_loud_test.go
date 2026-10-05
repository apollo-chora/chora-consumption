package http

// retention_fail_loud_test.go — the retention-loop handlers FAIL LOUD (500)
// on storage errors (no-debt directive 2026-06-10). A dropped SM-2 / streak /
// XP write must never be silently swallowed: it starves the Ebbinghaus slot
// and lies to the learner about their progress.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// errSM2Store errors on every method.
type errSM2Store struct{}

func (e *errSM2Store) GetState(context.Context, string, string, string) (companion.SM2State, bool, error) {
	return companion.SM2State{}, false, errors.New("boom: sm2 pg down")
}
func (e *errSM2Store) SaveState(context.Context, string, string, companion.SM2State) error {
	return errors.New("boom: sm2 pg down")
}
func (e *errSM2Store) AllStatesForLearner(context.Context, string, string) (map[string]companion.SM2State, error) {
	return nil, errors.New("boom: sm2 pg down")
}
func (e *errSM2Store) SeenTopics(context.Context, string, string) (map[string]bool, error) {
	return nil, errors.New("boom: sm2 pg down")
}
func (e *errSM2Store) MarkTopicSeen(context.Context, string, string, string) error {
	return errors.New("boom: sm2 pg down")
}

// errStreakStore errors on every method.
type errStreakStore struct{}

func (e *errStreakStore) Get(context.Context, string, string) (*companion.Streak, error) {
	return nil, errors.New("boom: streak pg down")
}
func (e *errStreakStore) RecordActivity(context.Context, string, string, time.Time) error {
	return errors.New("boom: streak pg down")
}

// errXPStore errors on every method.
type errXPStore struct{}

func (e *errXPStore) Get(context.Context, string, string) (int, error) {
	return 0, errors.New("boom: xp pg down")
}
func (e *errXPStore) Award(context.Context, string, string, int) error {
	return errors.New("boom: xp pg down")
}

func TestDailyDose_FailsLoud_OnSM2StateError(t *testing.T) {
	srv := NewServer()
	srv.SM2 = &errSM2Store{}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (SM-2 read failure must not silently compose), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DOSE_STATE_FAILED") {
		t.Errorf("body = %s, want DOSE_STATE_FAILED", w.Body.String())
	}
}

func TestDailyDose_FailsLoud_OnStreakWriteError(t *testing.T) {
	srv := NewServer()
	srv.Streaks = &errStreakStore{}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (streak write failure), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "STREAK_WRITE_FAILED") {
		t.Errorf("body = %s, want STREAK_WRITE_FAILED", w.Body.String())
	}
}

func TestDailyDose_FailsLoud_OnXPWriteError(t *testing.T) {
	srv := NewServer()
	srv.XP = &errXPStore{}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (XP write failure), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "XP_WRITE_FAILED") {
		t.Errorf("body = %s, want XP_WRITE_FAILED", w.Body.String())
	}
}

func TestAtomFeedback_FailsLoud_OnSM2WriteError(t *testing.T) {
	srv := NewServer()
	srv.SM2 = &errSM2Store{}
	atomID := srv.Atoms.Seeds()[0].AtomID

	w := authedReq(t, srv, http.MethodPost, "/atoms/"+atomID+"/feedback", feedbackReq{Grade: "i_remember"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (SM-2 failure must surface — a dropped write starves Ebbinghaus), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SM2_READ_FAILED") {
		t.Errorf("body = %s, want SM2_READ_FAILED", w.Body.String())
	}
}

func TestMeStreak_FailsLoud_OnStoreError(t *testing.T) {
	srv := NewServer()
	srv.Streaks = &errStreakStore{}

	w := authedReq(t, srv, http.MethodGet, "/v1/me/streak", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "STREAK_READ_FAILED") {
		t.Errorf("body = %s, want STREAK_READ_FAILED", w.Body.String())
	}
}

func TestMeXP_FailsLoud_OnStoreError(t *testing.T) {
	srv := NewServer()
	srv.XP = &errXPStore{}

	w := authedReq(t, srv, http.MethodGet, "/v1/me/xp", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "XP_READ_FAILED") {
		t.Errorf("body = %s, want XP_READ_FAILED", w.Body.String())
	}
}
