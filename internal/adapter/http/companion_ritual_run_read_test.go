// companion_ritual_run_read_test.go: RED first for the single-run read the
// agent editor replays from (UX Track U package D2).
//
// The LIST endpoint already returns every run of a ritual with its story, but a
// replay opens ONE run, usually from a link, and pulling fifty rows to render
// one of them is a read the editor should not have to make. It also cannot
// address a run that has fallen off the capped list at all.
//
// The dispatch change this needs has a security edge, so it is pinned here too:
// seg[1] is EVERYTHING after the ritual id, so cutting it into action + tail
// must not let ".../publish/extra" or ".../run/extra" perform the action.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// seedRun puts one terminal run straight into the fake repo. The runner is not
// exercised here: this is a READ test, and driving a real run to seed it would
// couple the read's RED to the runner's health.
func seedRun(t *testing.T, s *Server, ritualID, runID string) *companion.RitualRun {
	t.Helper()
	repo, ok := s.RitualRuns.(*runrepo)
	if !ok {
		t.Fatalf("run repo is %T, want *runrepo", s.RitualRuns)
	}
	completed := time.Now()
	run := &companion.RitualRun{
		RunID: runID, TenantID: "t-1", CompanionID: "fam-1", OwnerGCID: "gcid-1",
		RitualID: ritualID, RitualName: "R", RevisionNo: 1,
		Status: companion.RunStatusCompleted, ManaCharged: 35,
		Stamps:      []companion.StepStamp{{StepIndex: 0, SkillKey: "explain_anew"}},
		StartedAt:   completed.Add(-time.Minute),
		CompletedAt: &completed,
	}
	repo.runs = append(repo.runs, run)
	return run
}

func TestGetRitualRun_ReturnsTheOneRunWithItsStory(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	seedRun(t, s, rt.RitualID, "run-1")

	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/runs/run-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", w.Code, w.Body.String())
	}
	var got struct {
		RunID    string `json:"runId"`
		RitualID string `json:"ritualId"`
		Story    *struct {
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"story"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != "run-1" {
		t.Errorf("runId = %q; want run-1", got.RunID)
	}
	if got.RitualID != rt.RitualID {
		t.Errorf("ritualId = %q; want %q", got.RitualID, rt.RitualID)
	}
	// The single run is served as ONE object, not wrapped in the collection's
	// {"runs":[...]}: a replay that has to index [0] would render an empty run
	// as a successful read of nothing.
	if got.Story == nil || got.Story.Status == "" {
		t.Error("story missing; the replay view is the whole reason this endpoint exists")
	}
}

// An unknown run id is a 404, not an empty 200. An empty 200 would render as a
// run that happened and produced nothing.
func TestGetRitualRun_UnknownRunIs404(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	seedRun(t, s, rt.RitualID, "run-1")

	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/runs/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404 for an unknown run", w.Code, w.Body.String())
	}
	// A bare 404 proves nothing: before this endpoint existed the router 404'd
	// every one of these paths with NOT_FOUND, so a test asserting only the
	// status would pass against no handler at all. The CODE pins that the
	// handler ran and made the decision.
	if got := errCode(t, w.Body.Bytes()); got != "RITUAL_RUN_NOT_FOUND" {
		t.Errorf("code = %q; want RITUAL_RUN_NOT_FOUND (a router 404 is not the handler refusing)", got)
	}
}

// A run that belongs to a DIFFERENT ritual is not reachable through this
// ritual's path, even though the repository read is already tenant-scoped. The
// path asserts a relationship and the handler has to hold it, or a link to one
// ritual's replay would render another ritual's run.
func TestGetRitualRun_RunOfAnotherRitualIsNotReachableHere(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	seedRun(t, s, rt.RitualID, "run-1")

	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/some-other-ritual/runs/run-1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404: a run was served under a ritual it does not belong to",
			w.Code, w.Body.String())
	}
	// A bare 404 proves nothing: before this endpoint existed the router 404'd
	// every one of these paths with NOT_FOUND, so a test asserting only the
	// status would pass against no handler at all. The CODE pins that the
	// handler ran and made the decision.
	if got := errCode(t, w.Body.Bytes()); got != "RITUAL_RUN_NOT_FOUND" {
		t.Errorf("code = %q; want RITUAL_RUN_NOT_FOUND (a router 404 is not the handler refusing)", got)
	}
}

func TestGetRitualRun_NonGetIsRefused(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	seedRun(t, s, rt.RitualID, "run-1")

	w := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/runs/run-1", map[string]any{})
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

// The LIST endpoint keeps working unchanged: adding a segment must not move the
// route that was already there.
func TestListRitualRuns_StillServesTheCollection(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)

	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/runs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", w.Code, w.Body.String())
	}
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Runs) == 0 {
		t.Error("the collection read lost its runs")
	}
}

// A trailing segment on an ACTION must 404 rather than silently performing the
// action: /rituals/{id}/publish/anything is not a publish, and /run/anything is
// not a run. One test per action, because the cut is shared and a fix that
// covers only the one you thought of is the defect-class half-fix.
func TestRitualsRoute_TrailingSegmentDoesNotPublish(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	before := rt.CurrentRevision

	w := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/publish/extra",
		map[string]any{"steps": []map[string]any{{"skillKey": "explain_anew"}}, "policyDecision": "allow"})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, body = %s; want 404", w.Code, w.Body.String())
	}
	// The status alone is not the claim. Prove no publish HAPPENED.
	if after := rt.CurrentRevision; after != before {
		t.Errorf("revision moved %d -> %d: a publish ran from a path with a trailing segment", before, after)
	}
}

func TestRitualsRoute_TrailingSegmentDoesNotRun(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	repo := s.RitualRuns.(*runrepo)
	before := len(repo.runs)

	w := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/run/extra", map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, body = %s; want 404", w.Code, w.Body.String())
	}
	if after := len(repo.runs); after != before {
		t.Errorf("runs went %d -> %d: a ritual ran from a path with a trailing segment", before, after)
	}
}

// erroringRunRepo fails every read. It exists to pin the one distinction that
// matters on this endpoint: a store that could not be READ is not a run that
// does not EXIST.
type erroringRunRepo struct{ runrepo }

func (r *erroringRunRepo) GetRun(_ context.Context, _, _ string) (*companion.RitualRun, error) {
	return nil, errors.New("connection refused")
}

func TestGetRitualRun_ReadFailureIs500NotA404(t *testing.T) {
	s := ritualServerWith(t, &manaR{}, stepExec{}, &erroringRunRepo{})
	rt := seedPublishedRitual(t, s)

	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/runs/run-1", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want 500", w.Code, w.Body.String())
	}
	// 404 here would tell a learner their run does not exist because the
	// database was unreachable: a confident false claim about their history.
	if got := errCode(t, w.Body.Bytes()); got != "RITUAL_RUN_READ_FAILED" {
		t.Errorf("code = %q; want RITUAL_RUN_READ_FAILED", got)
	}
}

func TestGetRitualRun_UnwiredIs503(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	s.RitualRuns = nil // the run ledger is not wired in this deployment

	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/runs/run-1", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want 503", w.Code, w.Body.String())
	}
	if got := errCode(t, w.Body.Bytes()); got != "RITUALS_NOT_WIRED" {
		t.Errorf("code = %q; want RITUALS_NOT_WIRED", got)
	}
}
