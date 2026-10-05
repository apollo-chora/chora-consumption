// atom_attempt_handler_test - EXT scope AtomAttempt state-machine.
//
//	POST /sessions                  → started
//	PATCH /sessions/{id}/answer     → in_progress|completed
//	POST /sessions/{id}:abandon     → abandoned
//	GET  /sessions/{id}             → detail
package http

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// TestExt_StartSession transitions to STARTED state.
func TestExt_StartSession(t *testing.T) {
	s, h := extServer(t)
	body := map[string]any{"atom_id": tAtom1}
	w := extReq(t, h, http.MethodPost, "/sessions", body, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "started" {
		t.Errorf("status = %v, want started", resp["status"])
	}

	// Started event published.
	pub := s.Publisher.(*events.InMemoryPublisher)
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicAtomSessionStarted {
			found = true
		}
	}
	if !found {
		t.Errorf("expected session.started event")
	}
}

// TestExt_SubmitAnswer_Incorrect → in_progress.
func TestExt_SubmitAnswer_Incorrect(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	w2 := extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "wrong", "correct": false, "hint_used": true},
		extHeaders())
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp["status"] != "in_progress" {
		t.Errorf("status = %v, want in_progress", resp["status"])
	}
}

// TestExt_SubmitAnswer_Correct → completed; emits completed event.
func TestExt_SubmitAnswer_Correct(t *testing.T) {
	s, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	w2 := extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "right", "correct": true, "hint_used": false},
		extHeaders())
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d", w2.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp["status"] != "completed" {
		t.Errorf("status = %v, want completed", resp["status"])
	}

	// Completed event published.
	pub := s.Publisher.(*events.InMemoryPublisher)
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicAtomSessionCompletedV1 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected session.completed.v1 event")
	}
}

// TestExt_SubmitAnswer_AfterCompleted returns 409 invalid transition.
func TestExt_SubmitAnswer_AfterCompleted(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	// First answer → completed.
	extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "right", "correct": true}, extHeaders())

	// Second answer → 409.
	w2 := extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "again", "correct": true}, extHeaders())
	if w2.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w2.Code)
	}
}

// TestExt_SubmitAnswer_HintsCappedAt3 — 4th hint = 409.
func TestExt_SubmitAnswer_HintsCappedAt3(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	for i := 0; i < 3; i++ {
		extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
			map[string]any{"answer": "wrong", "correct": false, "hint_used": true},
			extHeaders())
	}
	w2 := extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "wrong", "correct": false, "hint_used": true},
		extHeaders())
	if w2.Code != http.StatusConflict {
		t.Errorf("4th hint status = %d, want 409", w2.Code)
	}
}

// TestExt_AbandonSession → abandoned + emits event.
func TestExt_AbandonSession(t *testing.T) {
	s, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	w2 := extReq(t, h, http.MethodPost, "/sessions/"+sid+":abandon", nil, extHeaders())
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp["status"] != "abandoned" {
		t.Errorf("status = %v, want abandoned", resp["status"])
	}

	pub := s.Publisher.(*events.InMemoryPublisher)
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicAtomSessionAbandoned {
			found = true
		}
	}
	if !found {
		t.Errorf("expected session.abandoned event")
	}
}

// TestExt_AbandonSession_AfterCompleted returns 409.
func TestExt_AbandonSession_AfterCompleted(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "right", "correct": true}, extHeaders())

	w2 := extReq(t, h, http.MethodPost, "/sessions/"+sid+":abandon", nil, extHeaders())
	if w2.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w2.Code)
	}
}

// TestExt_GetSession returns the session by ID.
func TestExt_GetSession(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions",
		map[string]any{"atom_id": tAtom1}, extHeaders())
	var sr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sr)
	sid := sr["session_id"].(string)

	w2 := extReq(t, h, http.MethodGet, "/sessions/"+sid, nil, extHeaders())
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp["session_id"] != sid {
		t.Errorf("session_id = %v, want %s", resp["session_id"], sid)
	}
}

// TestExt_GetSession_NotFound returns 404.
func TestExt_GetSession_NotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions/nonexistent", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestExt_StartSession_RequiresAtom rejects missing atom_id.
func TestExt_StartSession_RequiresAtom(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions", map[string]any{}, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
