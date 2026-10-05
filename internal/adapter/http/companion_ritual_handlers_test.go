// companion_ritual_handlers_test.go — CHO-2016 G5 item 3: the rituals BFF
// handlers verified via httptest against fakes (routing, method checks, JSON
// contract, status codes, error mapping, 503-when-nil).
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---- fakes -----------------------------------------------------------------

type rrepo struct {
	byID   map[string]*companion.Ritual
	list   []*companion.Ritual
	nextNo int
}

func newRRepo() *rrepo { return &rrepo{byID: map[string]*companion.Ritual{}} }

func (r *rrepo) Create(_ context.Context, rt *companion.Ritual) error {
	r.byID[rt.RitualID] = rt
	r.list = append(r.list, rt)
	return nil
}
func (r *rrepo) GetByID(_ context.Context, _, id string) (*companion.Ritual, error) {
	return r.byID[id], nil
}
func (r *rrepo) ListByCompanion(_ context.Context, _, _ string) ([]*companion.Ritual, error) {
	return r.list, nil
}
func (r *rrepo) AppendRevision(_ context.Context, _, id string, rev companion.RitualRevision, price int) error {
	if rt := r.byID[id]; rt != nil {
		rt.CurrentRevision = rev.RevisionNo
		rt.PublishedPriceUnits = price
		rt.Revisions = append(rt.Revisions, rev)
	}
	return nil
}
func (r *rrepo) SetEnabled(_ context.Context, _, id string, enabled bool) error {
	if rt := r.byID[id]; rt != nil {
		rt.Enabled = enabled
	}
	return nil
}
func (r *rrepo) CountEnabledByCompanion(_ context.Context, _, _, _ string) (int, error) {
	return 0, nil
}
func (r *rrepo) SoftDelete(_ context.Context, _, _ string) error { return nil }

type runrepo struct{ runs []*companion.RitualRun }

func (r *runrepo) CreateRun(_ context.Context, run *companion.RitualRun) error {
	r.runs = append(r.runs, run)
	return nil
}
func (r *runrepo) UpdateRun(_ context.Context, _ *companion.RitualRun) error { return nil }
func (r *runrepo) SweepStaleRunning(_ context.Context, _ time.Time) ([]*companion.RitualRun, error) {
	return nil, nil
}
func (r *runrepo) ListRunsByRitual(_ context.Context, _, ritualID string, _ int) ([]*companion.RitualRun, error) {
	return []*companion.RitualRun{{RunID: "run-1", RitualID: ritualID, Status: companion.RunStatusCompleted, ManaCharged: 35}}, nil
}

// GetRun reads what was actually stored, unlike ListRunsByRitual above, which
// synthesises a row for whatever ritual id it is handed. The single read is the
// one place a test needs the run's REAL ritual id, because the handler refuses
// a run served under the wrong ritual.
func (r *runrepo) GetRun(_ context.Context, _, runID string) (*companion.RitualRun, error) {
	for _, run := range r.runs {
		if run.RunID == runID {
			return run, nil
		}
	}
	return nil, companion.ErrRitualRunNotFound
}

type capsResolver struct{ insufficientStage int }

func (c *capsResolver) ResolveCapability(_ context.Context, _, _, _, _ string) (companion.RitualCapabilityContext, error) {
	return companion.RitualCapabilityContext{
		Stage:                4,
		EquippedActiveSkills: map[string]bool{"explain_anew": true, "flashcard_forge": true},
		Catalogue: map[string]companion.CatalogEntry{
			"explain_anew":    {SkillKey: "explain_anew", Name: "Explain It Differently", Active: true, PolicyClass: companion.PolicyStandard, ToolHandlerRefs: []string{"atom.search"}},
			"flashcard_forge": {SkillKey: "flashcard_forge", Name: "Flashcard Forge", Active: true, PolicyClass: companion.PolicyGenerative, ToolHandlerRefs: []string{"qgen.invoke"}},
		},
	}, nil
}

type catReader struct{}

func (catReader) ListCatalogue(_ context.Context) (map[string]companion.CatalogEntry, error) {
	return map[string]companion.CatalogEntry{"explain_anew": {SkillKey: "explain_anew", Name: "Explain It Differently"}}, nil
}

type stepExec struct{}

func (stepExec) ExecuteStep(_ context.Context, in companion.StepExecInput) (companion.StepExecResult, error) {
	return companion.StepExecResult{Output: "out", Stamp: companion.StepStamp{StepIndex: in.StepIndex, SkillKey: in.SkillKey}}, nil
}

type sinkW struct{}

func (sinkW) WriteToSink(_ context.Context, _ string, in companion.SinkWriteInput) (string, error) {
	return "sink:" + in.RunID, nil
}

type manaR struct{ insufficient bool }

func (m *manaR) Reserve(_ context.Context, in companion.RitualReserveInput) (companion.RitualReserveResult, error) {
	if m.insufficient {
		return companion.RitualReserveResult{}, &companion.ErrInsufficientMana{RequiredUnits: int64(in.Units), CurrentBalance: 0}
	}
	return companion.RitualReserveResult{ReservationID: "res", PriceUnits: in.Units}, nil
}
func (m *manaR) Refund(_ context.Context, _ companion.RitualRefundInput) error { return nil }

type obx struct{}

func (obx) PublishRitualEvent(_ context.Context, _ string, _ map[string]any, _ companion.LoadoutEnvelope) error {
	return nil
}

// recObx records published topics (CHO-2138 list-runs opportunistic sweep).
type recObx struct{ topics []string }

func (o *recObx) PublishRitualEvent(_ context.Context, topic string, _ map[string]any, _ companion.LoadoutEnvelope) error {
	o.topics = append(o.topics, topic)
	return nil
}

// sweepRunRepo returns a seeded stale 'running' row once and records the
// terminal UpdateRun the sweep issues (CHO-2138).
type sweepRunRepo struct {
	stale   []*companion.RitualRun
	updated []*companion.RitualRun
}

func (r *sweepRunRepo) CreateRun(_ context.Context, _ *companion.RitualRun) error { return nil }
func (r *sweepRunRepo) UpdateRun(_ context.Context, run *companion.RitualRun) error {
	r.updated = append(r.updated, run)
	return nil
}
func (r *sweepRunRepo) SweepStaleRunning(_ context.Context, _ time.Time) ([]*companion.RitualRun, error) {
	s := r.stale
	r.stale = nil // return once — a real sweep would not re-see a reclaimed row
	return s, nil
}
func (r *sweepRunRepo) GetRun(_ context.Context, _, _ string) (*companion.RitualRun, error) {
	return nil, companion.ErrRitualRunNotFound
}
func (r *sweepRunRepo) ListRunsByRitual(_ context.Context, _, ritualID string, _ int) ([]*companion.RitualRun, error) {
	return []*companion.RitualRun{{RunID: "run-1", RitualID: ritualID, Status: companion.RunStatusFailed}}, nil
}

// asyncRunRepo records the run rows and SIGNALS each terminal UpdateRun, so a
// test can await the DETACHED execution the 202 ack spawns (CHO-2145).
// Mutex-guarded: the handler goroutine writes while the test reads (-race).
type asyncRunRepo struct {
	mu       sync.Mutex
	created  []*companion.RitualRun
	updated  []*companion.RitualRun
	terminal chan *companion.RitualRun
}

func newAsyncRunRepo() *asyncRunRepo {
	return &asyncRunRepo{terminal: make(chan *companion.RitualRun, 4)}
}

func (r *asyncRunRepo) CreateRun(_ context.Context, run *companion.RitualRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, run)
	return nil
}

func (r *asyncRunRepo) UpdateRun(_ context.Context, run *companion.RitualRun) error {
	r.mu.Lock()
	snapshot := *run // copy: the runner owns the pointer, the test reads the value
	r.updated = append(r.updated, &snapshot)
	r.mu.Unlock()
	r.terminal <- &snapshot
	return nil
}

func (r *asyncRunRepo) SweepStaleRunning(_ context.Context, _ time.Time) ([]*companion.RitualRun, error) {
	return nil, nil
}

func (r *asyncRunRepo) GetRun(_ context.Context, _, _ string) (*companion.RitualRun, error) {
	return nil, companion.ErrRitualRunNotFound
}
func (r *asyncRunRepo) ListRunsByRitual(_ context.Context, _, _ string, _ int) ([]*companion.RitualRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*companion.RitualRun(nil), r.updated...), nil
}

// awaitTerminal blocks for the detached run's terminal write (fail-loud on a
// hang — a never-terminating run is exactly the strand this story forbids).
func (r *asyncRunRepo) awaitTerminal(t *testing.T) *companion.RitualRun {
	t.Helper()
	select {
	case run := <-r.terminal:
		return run
	case <-time.After(3 * time.Second):
		t.Fatal("the detached run never reached a terminal state (stranded 'running')")
		return nil
	}
}

// gatedStepExec holds step 0 until the test opens the gate — long enough for
// the test to cancel the REQUEST ctx first, exactly as net/http does the moment
// the 202 response completes (and as the gateway's 5s 504 did mid-run). If the
// handler leaked the request ctx into the detached execution, the step's ctx is
// already dead when the gate opens and the step fails loud → the run terminates
// FAILED instead of completed, which is the regression this guards.
type gatedStepExec struct{ gate chan struct{} }

func (g *gatedStepExec) ExecuteStep(ctx context.Context, in companion.StepExecInput) (companion.StepExecResult, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return companion.StepExecResult{}, ctx.Err()
	}
	return companion.StepExecResult{
		Output: "out",
		Stamp:  companion.StepStamp{StepIndex: in.StepIndex, SkillKey: in.SkillKey},
	}, nil
}

// ---- harness ---------------------------------------------------------------

func ritualServer(t *testing.T, mana *manaR) *Server {
	t.Helper()
	return ritualServerWith(t, mana, stepExec{}, &runrepo{})
}

// ritualServerWith builds the rituals server with an explicit step executor +
// run repository — the async lane (CHO-2145) needs to gate the steps and
// observe the DETACHED terminal write.
func ritualServerWith(t *testing.T, mana *manaR, steps companion.StepExecutor, runs companion.RunRepository) *Server {
	t.Helper()
	repo := newRRepo()
	pub, err := companion.NewRitualPublisher(companion.RitualPublisherConfig{
		Repo: repo, Caps: &capsResolver{}, Outbox: obx{},
		// The REAL production registry (boot passes nil writers too), so these
		// tests cannot pass while the deployed sink set disagrees.
		Sinks: clients.NewRitualSinkRouter(nil),
		Clock: time.Now, NewID: func() string { return "evt" },
	})
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}
	runner, err := companion.NewRitualRunner(companion.RitualRunnerConfig{
		Steps: steps, Sink: sinkW{}, Mana: mana, Runs: runs, Outbox: obx{},
		Catalogue: catReader{}, Clock: time.Now, NewID: func() string { return "run-1" },
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	return &Server{
		RitualPublisher: pub, RitualRunner: runner, Rituals: repo,
		RitualRuns: runs, RitualCaps: &capsResolver{}, SkillCatalog: catReader{},
		// The REAL registry, built exactly as boot builds it (nil writers), so
		// a test can never pass while the deployed sink set disagrees.
		RitualSinks: clients.NewRitualSinkRouter(nil),
	}
}

// seedPublishedRitual puts a published 1-step ritual straight into the repo.
func seedPublishedRitual(t *testing.T, s *Server) *companion.Ritual {
	t.Helper()
	rt, err := companion.NewRitual("t-1", "fam-1", "R", companion.TriggerManual, companion.SinkChat)
	if err != nil {
		t.Fatalf("NewRitual: %v", err)
	}
	if _, err := rt.Publish([]companion.RitualStep{{SkillKey: "explain_anew"}}, "allow", (&capsResolver{}).mustCaps(), time.Now); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	s.Rituals.(*rrepo).byID[rt.RitualID] = rt
	return rt
}

func do(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec, cancel := doCancellable(t, s, method, path, body)
	cancel()
	return rec
}

// doCancellable serves the request and hands back the request-ctx cancel so a
// test can fire it EXACTLY when net/http would: the instant the response
// completes. That is the disconnect that used to strand the run.
func doCancellable(t *testing.T, s *Server, method, path string, body any) (*httptest.ResponseRecorder, context.CancelFunc) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Tenant-Id", "t-1")
	req.Header.Set("gcid", "gcid-1")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleCompanionGrowthRoute(rec, req)
	return rec, cancel
}

// ---- tests -----------------------------------------------------------------

func TestRituals_503WhenNotWired(t *testing.T) {
	rec := do(t, &Server{}, http.MethodGet, "/v1/me/companions/fam-1/rituals", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil publisher = %d, want 503", rec.Code)
	}
}

func TestRituals_CreateThenPublishThenRun(t *testing.T) {
	s := ritualServer(t, &manaR{})

	// CREATE draft
	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals",
		createRitualReq{Name: "Morning Review", Trigger: "manual", Sink: "chat"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created ritualDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.RitualID == "" || created.Name != "Morning Review" || created.CurrentRevision != 0 {
		t.Fatalf("create DTO wrong: %+v", created)
	}

	// PUBLISH (2 steps: standard + generative → price 35)
	rec = do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+created.RitualID+"/publish",
		publishRitualReq{Steps: []ritualStepDTO{{SkillKey: "explain_anew"}, {SkillKey: "flashcard_forge"}}, ArmorVerdict: "allow"})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var pub ritualDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &pub)
	if pub.CurrentRevision != 1 || pub.PublishedPriceUnits != 35 || len(pub.Steps) != 2 {
		t.Fatalf("publish DTO wrong: %+v", pub)
	}

	// RUN → 202 ack (running); the ~30s LLM phase executes detached (CHO-2145).
	rec = do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+created.RitualID+"/run", runRitualReq{})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var run ritualRunDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	if run.Status != "running" || run.RunID == "" || run.ManaCharged != 35 || run.Story == nil {
		t.Fatalf("run ack DTO wrong: %+v", run)
	}
	if run.Story.Status != "running" {
		t.Errorf("story status = %q, want running", run.Story.Status)
	}
}

// CHO-2145: the run POST ACKS 202 with the durable running row and executes the
// ~30s LLM phase DETACHED. The api-gateway 504s the route at 5s — a synchronous
// run therefore surfaced a false "Something went wrong" on a HEALTHY run. The
// detached execution must survive the request-ctx cancel that follows the
// response (net/http cancels it; so did the gateway's 504) and still reach a
// terminal state: no strand, no false error.
func TestRituals_RunAcks202ThenCompletesDetachedAfterRequestCancel(t *testing.T) {
	gate := make(chan struct{})
	runs := newAsyncRunRepo()
	s := ritualServerWith(t, &manaR{}, &gatedStepExec{gate: gate}, runs)
	rt := seedPublishedRitual(t, s)

	rec, cancelRequest := doCancellable(t, s, http.MethodPost,
		"/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/run", nil)

	// 1. The ACK — non-error, immediate, carrying the id the FE will poll.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run ack = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var ack ritualRunDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &ack)
	if ack.Status != string(companion.RunStatusRunning) || ack.RunID == "" {
		t.Fatalf("ack = %+v, want a running run with an id", ack)
	}
	if ack.CompletedAt != nil {
		t.Errorf("a running ack carries no completedAt, got %v", ack.CompletedAt)
	}

	// 2. The disconnect: net/http cancels the request ctx once the response is
	//    written (and the gateway 504 cancelled it mid-run before that).
	cancelRequest()

	// 3. The detached phase runs anyway and terminates the SAME row.
	close(gate)
	terminal := runs.awaitTerminal(t)
	if terminal.Status != companion.RunStatusCompleted {
		t.Fatalf("detached terminal = %v (err=%q), want completed — the request ctx leaked into the detached execution",
			terminal.Status, terminal.Error)
	}
	if terminal.RunID != ack.RunID {
		t.Errorf("terminal run id %q != acked %q", terminal.RunID, ack.RunID)
	}
	if terminal.SinkRef == "" {
		t.Error("a completed run must carry its sink ref")
	}
}

// A pre-flight guard (stage lock / since-unequipped skill / unpublished) still
// fails SYNCHRONOUSLY with a real status — never a 202 carrying a run id the FE
// would poll for forever.
func TestRituals_RunPreflightGuardStillFailsSynchronously(t *testing.T) {
	runs := newAsyncRunRepo()
	s := ritualServerWith(t, &manaR{}, stepExec{}, runs)
	// A ritual with no published revision cannot run.
	rt, _ := companion.NewRitual("t-1", "fam-1", "Draft", companion.TriggerManual, companion.SinkChat)
	s.Rituals.(*rrepo).byID[rt.RitualID] = rt

	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/run", nil)
	if rec.Code == http.StatusAccepted {
		t.Fatalf("an unpublished ritual must NOT be acked 202; body=%s", rec.Body.String())
	}
	if rec.Code < 400 {
		t.Fatalf("run of an unpublished ritual = %d, want a 4xx/5xx fail-loud", rec.Code)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if len(runs.created) != 0 {
		t.Errorf("a guard failure must persist no run row, got %+v", runs.created)
	}
}

// Insufficient mana is TERMINAL AT ACK — the reserve refused, so there is
// nothing to execute detached. It is a 200 (the final record), not a 202: a
// designed pause, never a 4xx/5xx, and never something the FE polls.
func TestRituals_RunSkippedBudgetIs200(t *testing.T) {
	s := ritualServer(t, &manaR{insufficient: true})
	rt := seedPublishedRitual(t, s)

	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+rt.RitualID+"/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("skipped_budget = %d, want 200 (terminal at ack, not 202)", rec.Code)
	}
	var run ritualRunDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	if run.Status != "skipped_budget" {
		t.Errorf("status = %q, want skipped_budget", run.Status)
	}
}

func TestRituals_RunNotFound(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/nope/run", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ritual run = %d, want 404", rec.Code)
	}
}

func TestRituals_ListAndRuns(t *testing.T) {
	s := ritualServer(t, &manaR{})
	_, _ = s.RitualPublisher.CreateDraft(context.Background(), "t-1", "fam-1", "R", companion.TriggerManual, companion.SinkChat)

	rec := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	var list listRitualsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Rituals) != 1 {
		t.Fatalf("list len = %d, want 1", len(list.Rituals))
	}

	rec = do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/rit-1/runs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("runs = %d, want 200", rec.Code)
	}
	var runs listRunsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].Status != "completed" {
		t.Fatalf("runs wrong: %+v", runs)
	}
}

// CHO-2138: opening run history opportunistically reclaims this tenant's
// stranded 'running' rows (RLS-scoped self-heal) — the row is swept to failed
// and a terminal event is published before the list is served.
func TestRituals_ListRunsSweepsStrandedRows(t *testing.T) {
	repo := newRRepo()
	pub, err := companion.NewRitualPublisher(companion.RitualPublisherConfig{
		Repo: repo, Caps: &capsResolver{}, Outbox: obx{}, Sinks: clients.NewRitualSinkRouter(nil),
		Clock: time.Now, NewID: func() string { return "evt" },
	})
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}
	sr := &sweepRunRepo{stale: []*companion.RitualRun{
		{RunID: "orphan-1", TenantID: "t-1", OwnerGCID: "gcid-1", RitualID: "rit-1",
			Status: companion.RunStatusRunning, ManaCharged: 20, ReservationID: "resv-x"},
	}}
	ob := &recObx{}
	runner, err := companion.NewRitualRunner(companion.RitualRunnerConfig{
		Steps: stepExec{}, Sink: sinkW{}, Mana: &manaR{}, Runs: sr, Outbox: ob,
		Catalogue: catReader{}, Clock: time.Now, NewID: func() string { return "run-1" },
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	s := &Server{
		RitualPublisher: pub, Rituals: repo, RitualRuns: sr, RitualRunner: runner,
		RitualCaps: &capsResolver{}, SkillCatalog: catReader{},
	}
	rec := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/rit-1/runs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list runs = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(sr.updated) != 1 || sr.updated[0].Status != companion.RunStatusFailed {
		t.Errorf("stranded run not reclaimed to failed by the list-runs sweep: %+v", sr.updated)
	}
	if len(ob.topics) == 0 {
		t.Error("swept run did not publish a terminal event on the list-runs path")
	}
}

func TestRituals_CreateBadSink422(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals",
		createRitualReq{Name: "R", Trigger: "manual", Sink: "webhook"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad sink = %d, want 422", rec.Code)
	}
}

// mustCaps exposes the fixed caps for direct domain seeding in tests.
func (c *capsResolver) mustCaps() companion.RitualCapabilityContext {
	caps, _ := c.ResolveCapability(context.Background(), "", "", "", "")
	return caps
}

// TestRituals_PublishUnwiredSink422 — N5 (tracker row B3). memory_note is a real
// ADR-218 D9 sink and a valid draft, but this deployment registers no writer for
// it, so publish must refuse with its OWN code rather than letting the ritual
// publish and then fail at WriteToSink on every run, refunding each time.
//
// Gated against the REAL production registry (clients.NewRitualSinkRouter,
// which the boot wiring also builds with nil writers), not a stub, so the test
// cannot pass while production disagrees.
func TestRituals_PublishUnwiredSink422(t *testing.T) {
	s := ritualServer(t, &manaR{})

	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals",
		createRitualReq{Name: "Nightly Note", Trigger: "manual", Sink: "memory_note"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with a real-but-unwired sink should still draft, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var created ritualDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+created.RitualID+"/publish",
		publishRitualReq{Steps: []ritualStepDTO{{SkillKey: "explain_anew"}}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish to an unwired sink = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "RITUAL_SINK_NOT_WIRED") {
		t.Errorf("expected the named code RITUAL_SINK_NOT_WIRED so the FE can grey rather than "+
			"show a validation error, got: %s", rec.Body.String())
	}
}

// TestRituals_PublishWiredSinkSucceeds — the positive control for the gate
// above: chat IS registered, so the same flow publishes.
func TestRituals_PublishWiredSinkSucceeds(t *testing.T) {
	s := ritualServer(t, &manaR{})

	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals",
		createRitualReq{Name: "Morning Review", Trigger: "manual", Sink: "chat"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rec.Code)
	}
	var created ritualDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+created.RitualID+"/publish",
		publishRitualReq{Steps: []ritualStepDTO{{SkillKey: "explain_anew"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish to the wired chat sink = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRituals_PublishReservedStepField422 — ADR-257 N1 at the edge: a reserved
// field reaching the API is refused with its own code, so the FE can say "not
// yet" rather than "invalid".
func TestRituals_PublishReservedStepField422(t *testing.T) {
	s := ritualServer(t, &manaR{})

	rec := do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals",
		createRitualReq{Name: "Chained", Trigger: "manual", Sink: "chat"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rec.Code)
	}
	var created ritualDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, s, http.MethodPost, "/v1/me/companions/fam-1/rituals/"+created.RitualID+"/publish",
		publishRitualReq{Steps: []ritualStepDTO{
			{SkillKey: "explain_anew"},
			{SkillKey: "flashcard_forge", FromStepID: "019f26d5-e707-745c-891f-44a1c7694b75"},
		}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish with from_step_id = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "RITUAL_STEP_RESERVED") {
		t.Errorf("expected RITUAL_STEP_RESERVED, got: %s", rec.Body.String())
	}
}

// ── B3b: the wired-sink set is served, not guessed client-side ──────────────

// TestRituals_ListServesWiredSinks is the contract test for the additive field.
// Before this, the composer greyed sinks from a hardcoded client constant that
// could drift from the deployment; now the server answers and there is one
// source of truth.
func TestRituals_ListServesWiredSinks(t *testing.T) {
	s := ritualServer(t, &manaR{})

	rec := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		Rituals    []map[string]any `json:"rituals"`
		WiredSinks []string         `json:"wiredSinks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The test server wires the real router with nil writers, exactly as boot
	// does, so the served answer must be chat and nothing else.
	if len(got.WiredSinks) != 1 || got.WiredSinks[0] != "chat" {
		t.Errorf("wiredSinks = %v, want [chat]", got.WiredSinks)
	}
}

// TestRituals_ListWiredSinksTracksTheRegistry: the field reports the
// DEPLOYMENT, not a second hardcoded list. Register a writer and it appears.
func TestRituals_ListWiredSinksTracksTheRegistry(t *testing.T) {
	s := ritualServer(t, &manaR{})
	s.RitualSinks = clients.NewRitualSinkRouter(map[string]clients.RitualSinkAdapter{
		companion.SinkMemoryNote: clients.ChatSink{}, // stand-in; registration is the subject
	})

	rec := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals", nil)
	var got struct {
		WiredSinks []string `json:"wiredSinks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	if len(got.WiredSinks) != 2 || got.WiredSinks[0] != "chat" || got.WiredSinks[1] != "memory_note" {
		t.Errorf("wiredSinks = %v, want [chat memory_note] in closed-list order", got.WiredSinks)
	}
}

// TestRituals_ListWiredSinksEmptyWhenUnwired: an unwired server must serve an
// EMPTY ARRAY, never omit the field and never null. The FE greys anything not
// in the list, so a missing field and "nothing is wired" must be
// distinguishable from a JSON shape alone.
func TestRituals_ListWiredSinksEmptyWhenUnwired(t *testing.T) {
	s := ritualServer(t, &manaR{})
	s.RitualSinks = nil // registry not wired at boot

	rec := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"wiredSinks":[]`) {
		t.Errorf("an unwired server must serve an empty array, not null and not an absent key; got: %s",
			rec.Body.String())
	}
}
