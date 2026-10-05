// companion_retire_handler_test.go — CHO-2033: learner retire/release of a
// roster companion (soft-delete frees the cap-3 slot; pseudonymise-not-delete),
// RED-first.
//
//	POST /v1/me/companions/{id}/retire
package http

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// companionRetirePub captures Publisher emits for the retire assertions.
type companionRetirePub struct {
	topics   []string
	payloads []map[string]any
	err      error
}

func (p *companionRetirePub) Publish(topic string, _ events.Envelope, payload map[string]any) error {
	p.topics = append(p.topics, topic)
	p.payloads = append(p.payloads, payload)
	return p.err
}

var _ events.Publisher = (*companionRetirePub)(nil)

func retirePath(id string) string { return "/v1/me/companions/" + id + "/retire" }

// TestRetireCompanion_HappyPath — retire soft-deletes the owned companion (frees
// the roster slot) and emits companion.retired.v1.
func TestRetireCompanion_HappyPath(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	pub := &companionRetirePub{}
	srv.Publisher = pub

	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	// Soft-deleted: Get (excludes deleted) no longer finds it.
	if inst, _ := srv.CompanionInstances.Get(context.Background(), id); inst != nil {
		t.Fatalf("companion must be soft-deleted, Get still returns %+v", inst)
	}
	// Exactly one companion.retired.v1, carrying the companion id.
	if len(pub.topics) != 1 || pub.topics[0] != "chora.consumption.companion.retired.v1" {
		t.Fatalf("expected one companion.retired.v1, got %v", pub.topics)
	}
	if pub.payloads[0]["companion_id"] != id {
		t.Fatalf("event companion_id = %v, want %s", pub.payloads[0]["companion_id"], id)
	}
}

// TestRetireCompanion_UnknownOrForeign404 — an unknown/foreign companion id 404s
// (Get excludes soft-deleted, so a double-retire is a 404 too); no event.
func TestRetireCompanion_UnknownOrForeign404(t *testing.T) {
	srv := NewServer()
	_ = seedSkillCompanion(t, srv)
	pub := &companionRetirePub{}
	srv.Publisher = pub

	w := authedReq(t, srv, http.MethodPost, retirePath("01970000-dead-7000-8000-00000000000a"), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no event on a 404, got %v", pub.topics)
	}
}

// TestRetireCompanion_MethodNotAllowed — only POST retires.
func TestRetireCompanion_MethodNotAllowed(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	w := authedReq(t, srv, http.MethodGet, retirePath(id), nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body=%s", w.Code, w.Body.String())
	}
}

// TestRetireCompanion_Unwired503 — a nil roster repo refuses loud.
func TestRetireCompanion_Unwired503(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	srv.CompanionInstances = nil
	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
	}
}

// TestRetireCompanion_DetachesMapBinding — retiring a companion bonded to a map
// clears that map's Goal.AttachedCompanionID so the map no longer shows a dead
// companion (map↔roster consistency). A second goal bonded to a DIFFERENT
// companion is left untouched.
func TestRetireCompanion_DetachesMapBinding(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)

	bonded := mapsGoal("g-bonded", mapsPtr("c-root"))
	bonded.AttachedCompanionID = &id
	otherFam := "01970000-0000-7000-a000-00000000beef"
	untouched := mapsGoal("g-other", mapsPtr("c-x"))
	untouched.AttachedCompanionID = &otherFam
	goals := &mapsGoalStub{list: []*goal.Goal{bonded, untouched}}
	srv.CompanionMaps = NewGoalCompanionDetacher(goals)
	srv.Publisher = &companionRetirePub{}

	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if goals.updated == nil {
		t.Fatalf("expected the bonded goal to be Updated (detached)")
	}
	if goals.updated.GoalID != "g-bonded" {
		t.Fatalf("detached the wrong goal: %s", goals.updated.GoalID)
	}
	if goals.updated.AttachedCompanionID != nil {
		t.Errorf("goal still bonded: AttachedCompanionID = %v, want nil", *goals.updated.AttachedCompanionID)
	}
	// The companion is still soft-deleted (retire succeeded).
	if inst, _ := srv.CompanionInstances.Get(context.Background(), id); inst != nil {
		t.Fatalf("companion must be soft-deleted, Get still returns %+v", inst)
	}
}

// TestRetireCompanion_DetachListError_AbortsRetire — detach runs BEFORE the
// soft-delete, so a Goals repo failure aborts the whole retire (500) and leaves
// the companion intact. This prevents a dead companion stranded on a live map.
func TestRetireCompanion_DetachListError_AbortsRetire(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	srv.CompanionMaps = NewGoalCompanionDetacher(&mapsGoalStub{listErr: errors.New("boom")})
	srv.Publisher = &companionRetirePub{}

	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	// NOT soft-deleted — the companion survives the aborted retire.
	if inst, _ := srv.CompanionInstances.Get(context.Background(), id); inst == nil {
		t.Fatalf("companion must survive an aborted retire (detach-first)")
	}
}

// ---- CHO-2096: retire purges the companion's retained memories ----------------

// companionRetireMemStub records SoftDeleteByCompanion calls (the CHO-2096 purge).
type companionRetireMemStub struct {
	calls [][2]string // (tenantID, companionID)
	err   error
}

func (s *companionRetireMemStub) SoftDeleteByCompanion(_ context.Context, tenantID, companionID string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.calls = append(s.calls, [2]string{tenantID, companionID})
	return 3, nil
}

// TestRetireCompanion_PurgesMemory — a successful retire soft-deletes the
// companion's memory rows (the records it retains about the learner's goals)
// exactly once, scoped to (tenant, companion).
func TestRetireCompanion_PurgesMemory(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	mem := &companionRetireMemStub{}
	srv.CompanionMemoryRetire = mem

	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if len(mem.calls) != 1 {
		t.Fatalf("memory purge calls = %d, want 1", len(mem.calls))
	}
	if mem.calls[0][1] != id {
		t.Fatalf("purged companion = %s, want %s", mem.calls[0][1], id)
	}
	if mem.calls[0][0] == "" {
		t.Fatal("purge must carry the tenant id")
	}
}

// TestRetireCompanion_MemoryPurgeFailureAborts — a purge failure aborts the
// retire (fail-loud): 500, the companion stays live, no retired.v1 emitted. The
// learner retries; the irreversibility promise is never silently broken.
func TestRetireCompanion_MemoryPurgeFailureAborts(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	pub := &companionRetirePub{}
	srv.Publisher = pub
	srv.CompanionMemoryRetire = &companionRetireMemStub{err: errors.New("pg down")}

	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if inst, _ := srv.CompanionInstances.Get(context.Background(), id); inst == nil {
		t.Fatal("companion must NOT be soft-deleted when the memory purge fails")
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no retired.v1 on an aborted retire, got %v", pub.topics)
	}
}

// TestRetireCompanion_NilRetirerStillRetires — memory unwired (dev without pg ⇒
// nothing was ever recorded) skips the purge with a loud log and still retires
// (parity with the CompanionMaps nil-detacher posture).
func TestRetireCompanion_NilRetirerStillRetires(t *testing.T) {
	srv := NewServer()
	id := seedSkillCompanion(t, srv)

	w := authedReq(t, srv, http.MethodPost, retirePath(id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if inst, _ := srv.CompanionInstances.Get(context.Background(), id); inst != nil {
		t.Fatal("companion must be soft-deleted with a nil retirer")
	}
}
