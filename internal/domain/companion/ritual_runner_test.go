// ritual_runner_test.go — RED-first for CHO-2016 P4 (the DETERMINISTIC Go
// runner, ADR-219 D4 + spec §6). Exercises the sequencer with fake driven
// ports: sequential per-step gateway turns, per-step allowlist NARROWING,
// buffered-then-atomic sink, reserve→charge/refund economy, decision stamps,
// the failure/blocked/skipped_budget matrix, run-to-run determinism, and the
// stale-running sweep (replicas=1 mitigation).
//
// References symbols not yet built (RitualRunner, StepExecutor, …) — the
// package fails to COMPILE (RED). GREEN slice G2 (ritual_runner.go + ports)
// makes it compile + pass against these same fakes. Do NOT add impl here.
package companion

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---- fake driven ports -----------------------------------------------------

type recordedStep struct {
	index        int
	skillKey     string
	allowedTools []string
}

type fakeStepExecutor struct {
	calls   []recordedStep
	failAt  int // step index to fail at; -1 = never
	blockAt int // step index to Armor-block at; -1 = never
	// wedge simulates a step that never returns (a hung LLM turn). The step
	// unblocks only when its ctx dies — which is how a RunTimeout-bounded
	// Execute reclaims a wedged run instead of stranding it 'running' (CHO-2145).
	wedge bool
}

func newFakeExec() *fakeStepExecutor { return &fakeStepExecutor{failAt: -1, blockAt: -1} }

func (f *fakeStepExecutor) ExecuteStep(ctx context.Context, in StepExecInput) (StepExecResult, error) {
	f.calls = append(f.calls, recordedStep{index: in.StepIndex, skillKey: in.SkillKey, allowedTools: in.AllowedTools})
	if f.wedge {
		<-ctx.Done() // only the deadline frees us
		return StepExecResult{}, ctx.Err()
	}
	if in.StepIndex == f.blockAt {
		return StepExecResult{Blocked: true, BlockReason: "armor: unsafe content"}, nil
	}
	if in.StepIndex == f.failAt {
		return StepExecResult{}, errors.New("step exploded")
	}
	return StepExecResult{
		Output: "out:" + in.SkillKey,
		Stamp: StepStamp{StepIndex: in.StepIndex, SkillKey: in.SkillKey,
			PromptVersion: in.SkillKey + "@v1", PromptHash: "sha256:abc", ToolsInvoked: in.AllowedTools},
	}, nil
}

type fakeSink struct {
	calls   int
	sink    string
	outputs []any
}

func (f *fakeSink) WriteToSink(_ context.Context, sink string, in SinkWriteInput) (string, error) {
	f.calls++
	f.sink = sink
	f.outputs = in.Outputs
	return "sink-ref-1", nil
}

type fakeReserver struct {
	reserveCalls  int
	refundCalls   int
	reservedUnits int
	refundUnits   int
	insufficient  bool
}

func (f *fakeReserver) Reserve(_ context.Context, in RitualReserveInput) (RitualReserveResult, error) {
	f.reserveCalls++
	if f.insufficient {
		return RitualReserveResult{}, &ErrInsufficientMana{RequiredUnits: int64(in.Units), CurrentBalance: 1}
	}
	f.reservedUnits = in.Units
	return RitualReserveResult{ReservationID: "resv-1", PriceUnits: in.Units}, nil
}

func (f *fakeReserver) Refund(_ context.Context, in RitualRefundInput) error {
	f.refundCalls++
	f.refundUnits = in.Units
	return nil
}

type fakeRunRepo struct {
	created []*RitualRun
	updated []*RitualRun
	stale   []*RitualRun
}

func (f *fakeRunRepo) CreateRun(_ context.Context, r *RitualRun) error {
	f.created = append(f.created, r)
	return nil
}
func (f *fakeRunRepo) UpdateRun(_ context.Context, r *RitualRun) error {
	f.updated = append(f.updated, r)
	return nil
}
func (f *fakeRunRepo) SweepStaleRunning(_ context.Context, _ time.Time) ([]*RitualRun, error) {
	return f.stale, nil
}
func (f *fakeRunRepo) GetRun(_ context.Context, _, _ string) (*RitualRun, error) {
	return nil, ErrRitualRunNotFound
}
func (f *fakeRunRepo) ListRunsByRitual(_ context.Context, _, _ string, _ int) ([]*RitualRun, error) {
	return nil, nil
}

type fakeOutbox struct {
	topics    []string
	payloads  []map[string]any
	envelopes []LoadoutEnvelope
}

func (f *fakeOutbox) PublishRitualEvent(_ context.Context, topic string, payload map[string]any, env LoadoutEnvelope) error {
	f.topics = append(f.topics, topic)
	f.payloads = append(f.payloads, payload)
	f.envelopes = append(f.envelopes, env)
	return nil
}

// payloadFor returns the first captured payload published on topic, or nil.
func (f *fakeOutbox) payloadFor(topic string) map[string]any {
	for i, tp := range f.topics {
		if tp == topic {
			return f.payloads[i]
		}
	}
	return nil
}

// NOTE: the CatalogReader fake (fakeCatalogReader{entries}) is reused from
// path_unlocker_test.go — same package, one fake, no duplication.

// ---- harness ---------------------------------------------------------------

type runnerHarness struct {
	exec *fakeStepExecutor
	sink *fakeSink
	mana *fakeReserver
	runs *fakeRunRepo
	obx  *fakeOutbox
}

func newRunner(t *testing.T, h *runnerHarness) *RitualRunner {
	t.Helper()
	rr, err := NewRitualRunner(RitualRunnerConfig{
		Steps: h.exec, Sink: h.sink, Mana: h.mana, Runs: h.runs, Outbox: h.obx,
		Catalogue:      &fakeCatalogReader{entries: ritualTestCatalogue()},
		StepTimeout:    60 * time.Second,
		Clock:          fixedClock(),
		NewID:          func() string { return "run-1" },
		NewTraceparent: func() string { return runTestTraceparent },
	})
	if err != nil {
		t.Fatalf("NewRitualRunner: %v", err)
	}
	return rr
}

func newHarness() *runnerHarness {
	return &runnerHarness{exec: newFakeExec(), sink: &fakeSink{}, mana: &fakeReserver{}, runs: &fakeRunRepo{}, obx: &fakeOutbox{}}
}

// publishedRitual builds an enabled 2-step ritual (explain_anew standard →
// flashcard_forge generative), sink question_bank, price 35.
func publishedRitual(t *testing.T) *Ritual {
	t.Helper()
	r, err := NewRitual("t-1", "fam-1", "Morning Review", TriggerManual, SinkQuestionBank)
	if err != nil {
		t.Fatalf("NewRitual: %v", err)
	}
	caps := ritualTestCaps()
	if _, err := r.Publish(steps("explain_anew", "flashcard_forge"), "allow", caps, fixedClock()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := r.Enable(caps); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return r
}

const runTestTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func runInput(r *Ritual) RunInput {
	return RunInput{TenantID: "t-1", CompanionID: "fam-1", OwnerGCID: "gcid-1", Ritual: r,
		Caps: ritualTestCaps(), TriggerSource: "manual",
		Traceparent: runTestTraceparent}
}

// Owner ruling #6 (2026-07-07): re-check equipped-active at RUN time. A ritual
// whose step Skill was unequipped since publish must fail loud BEFORE any spend
// — no reserve, no steps, no run record mutation.
func TestRunFailsLoudOnSinceUnequippedSkill(t *testing.T) {
	h := newHarness()
	rr := newRunner(t, h)
	in := runInput(publishedRitual(t))
	// The learner has since turned off flashcard_forge (step 2 of the ritual).
	in.Caps = ritualTestCaps()
	delete(in.Caps.EquippedActiveSkills, "flashcard_forge")

	run, err := rr.Run(context.Background(), in)
	if err == nil {
		t.Fatal("run with a since-unequipped step Skill must fail loud (Go error)")
	}
	if !errors.Is(err, ErrRitualStepSkillNotEquipped) {
		t.Errorf("err = %v, want ErrRitualStepSkillNotEquipped", err)
	}
	if run != nil {
		t.Errorf("no run record on a pre-flight guard failure, got %+v", run)
	}
	if h.mana.reserveCalls != 0 || len(h.exec.calls) != 0 || h.sink.calls != 0 {
		t.Error("pre-flight guard must not reserve, execute, or sink")
	}
}

// ---- construction + pre-flight guards --------------------------------------

func TestNewRitualRunnerRequiresDeps(t *testing.T) {
	if _, err := NewRitualRunner(RitualRunnerConfig{}); err == nil {
		t.Error("a runner with nil ports must fail loud")
	}
}

func TestRunPreflightGuards(t *testing.T) {
	// nil ritual
	h := newHarness()
	if _, err := newRunner(t, h).Run(context.Background(), RunInput{Caps: ritualTestCaps()}); err == nil {
		t.Error("nil ritual must fail loud")
	}
	// published but NOT enabled → still runs (an explicit run ignores the
	// enabled flag; enabled gates unattended triggers only).
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkQuestionBank)
	_, _ = r.Publish(steps("explain_anew"), "allow", ritualTestCaps(), fixedClock())
	if run, err := newRunner(t, newHarness()).Run(context.Background(), runInput(r)); err != nil || run.Status != RunStatusCompleted {
		t.Errorf("a published-but-unenabled ritual must run on demand: err=%v status=%v", err, run)
	}
	// enabled but caller stage below structural → unlock guard
	rr := publishedRitual(t)
	below := runInput(rr)
	below.Caps.Stage = 3
	if _, err := newRunner(t, newHarness()).Run(context.Background(), below); !errors.Is(err, ErrRitualUnlockStage) {
		t.Errorf("run below st4 = %v, want ErrRitualUnlockStage", err)
	}
	// soft-deleted ritual
	del := publishedRitual(t)
	del.SoftDelete(fixedClock())
	if _, err := newRunner(t, newHarness()).Run(context.Background(), runInput(del)); err == nil {
		t.Error("a soft-deleted ritual must not run")
	}
}

// ---- happy path ------------------------------------------------------------

func TestRunHappyPathSequentialAtomicSink(t *testing.T) {
	h := newHarness()
	rr := newRunner(t, h)
	run, err := rr.Run(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Reserve once, up-front, for the frozen price.
	if h.mana.reserveCalls != 1 || h.mana.reservedUnits != 35 {
		t.Errorf("reserve calls=%d units=%d, want 1/35", h.mana.reserveCalls, h.mana.reservedUnits)
	}
	if h.mana.refundCalls != 0 {
		t.Errorf("refund calls=%d, want 0 on success", h.mana.refundCalls)
	}
	// Steps run strictly in revision order.
	if len(h.exec.calls) != 2 || h.exec.calls[0].index != 0 || h.exec.calls[1].index != 1 {
		t.Fatalf("step order = %+v, want indices 0,1", h.exec.calls)
	}
	if h.exec.calls[0].skillKey != "explain_anew" || h.exec.calls[1].skillKey != "flashcard_forge" {
		t.Errorf("step keys = %q,%q", h.exec.calls[0].skillKey, h.exec.calls[1].skillKey)
	}
	// Sink written EXACTLY once, after both steps, with both outputs in order.
	if h.sink.calls != 1 {
		t.Fatalf("sink writes = %d, want exactly 1 (atomic)", h.sink.calls)
	}
	if h.sink.sink != SinkQuestionBank || len(h.sink.outputs) != 2 {
		t.Errorf("sink=%q outputs=%d, want question_bank / 2", h.sink.sink, len(h.sink.outputs))
	}
	// Run record: completed, charged, two stamps, sink ref, event emitted.
	if run.Status != RunStatusCompleted || run.ManaCharged != 35 {
		t.Errorf("run status=%q charged=%d, want completed/35", run.Status, run.ManaCharged)
	}
	if len(run.Stamps) != 2 {
		t.Errorf("stamps = %d, want 2 (one per step)", len(run.Stamps))
	}
	if run.SinkRef == "" {
		t.Error("completed run must carry the sink ref")
	}
	if !contains(h.obx.topics, TopicCompanionRitualRunCompleted) {
		t.Errorf("outbox topics = %v, want ritual_run_completed.v1", h.obx.topics)
	}
}

// CHO-2136 walk-found P0: publishCompleted hardcoded Traceparent "" onto the
// terminal envelope, so BuildRow's envelope validation rejected EVERY
// ritual_run_completed publish ("outbox: envelope.traceparent required") —
// the run persisted terminal but Run() returned an error and the O+ audit
// projection NEVER received a single event. The RunInput's trace context must
// thread onto the terminal envelope.
func TestRunThreadsTraceContextOntoTerminalEvent(t *testing.T) {
	h := newHarness()
	in := runInput(publishedRitual(t))
	in.Tracestate = "chora=walk"
	if _, err := newRunner(t, h).Run(context.Background(), in); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(h.obx.envelopes) == 0 {
		t.Fatal("no terminal envelope captured")
	}
	env := h.obx.envelopes[len(h.obx.envelopes)-1]
	if env.Traceparent != runTestTraceparent {
		t.Errorf("terminal envelope traceparent = %q, want the RunInput's %q (a blank one fails outbox envelope validation and kills the publish)", env.Traceparent, runTestTraceparent)
	}
	if env.Tracestate != "chora=walk" {
		t.Errorf("terminal envelope tracestate = %q, want chora=walk", env.Tracestate)
	}
}

// The guard is PRE-SPEND (mirrors ruling #6 fail-loud-before-any-spend): a
// caller that forgot to mint a traceparent fails before reserve — not after
// the run persisted, which stranded a terminal row with no event.
func TestRunRequiresTraceparentBeforeAnySpend(t *testing.T) {
	h := newHarness()
	in := runInput(publishedRitual(t))
	in.Traceparent = ""
	if _, err := newRunner(t, h).Run(context.Background(), in); err == nil {
		t.Fatal("a blank traceparent must fail loud (the terminal event envelope requires W3C trace context)")
	}
	if h.mana.reserveCalls != 0 {
		t.Errorf("guard must fire BEFORE reserve; got %d reserve calls", h.mana.reserveCalls)
	}
	if len(h.obx.topics) != 0 {
		t.Errorf("no event may publish on a guard failure; got %v", h.obx.topics)
	}
}

// CHO-2136: the ritual_run_completed payload must be WIRE-READY — stamps as
// snake_case []map[string]any matching the flat proto field names (the outbox
// binary encoder + the O+ audit consumer's snake_case stamp keys both read
// them). Raw []StepStamp domain structs JSON-marshal with Go field names
// (StepIndex/SkillKey — StepStamp has no json tags), which the consumer's
// snake_case decode silently drops.
func TestRunPublishesWireReadyStampMaps(t *testing.T) {
	h := newHarness()
	if _, err := newRunner(t, h).Run(context.Background(), runInput(publishedRitual(t))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p := h.obx.payloadFor(TopicCompanionRitualRunCompleted)
	if p == nil {
		t.Fatal("no ritual_run_completed payload captured")
	}
	stamps, ok := p["stamps"].([]map[string]any)
	if !ok {
		t.Fatalf("payload stamps = %T, want wire-ready []map[string]any (snake_case keys per the flat proto)", p["stamps"])
	}
	if len(stamps) != 2 {
		t.Fatalf("stamps = %d, want 2 (one per step)", len(stamps))
	}
	for i, s := range stamps {
		for _, key := range []string{"step_index", "skill_key", "prompt_version", "prompt_hash", "tools_invoked", "citations"} {
			if _, present := s[key]; !present {
				t.Errorf("stamp[%d] missing snake_case key %q: %v", i, key, s)
			}
		}
	}
	if stamps[0]["skill_key"] != "explain_anew" || stamps[1]["skill_key"] != "flashcard_forge" {
		t.Errorf("stamp skill keys = %v/%v", stamps[0]["skill_key"], stamps[1]["skill_key"])
	}
}

// Per-step allowlist NARROWING: each turn sees ONLY its own Skill's tools,
// never the innate∪equipped union (ADR-219 D4 / spec §0 runtime).
func TestRunPerStepAllowlistNarrowed(t *testing.T) {
	h := newHarness()
	rr := newRunner(t, h)
	if _, err := rr.Run(context.Background(), runInput(publishedRitual(t))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sameSet(h.exec.calls[0].allowedTools, []string{"atom.search", "atom.cite"}) {
		t.Errorf("step 0 allowlist = %v, want explain_anew tools only", h.exec.calls[0].allowedTools)
	}
	if !sameSet(h.exec.calls[1].allowedTools, []string{"qgen.invoke"}) {
		t.Errorf("step 1 allowlist = %v, want flashcard_forge tools only", h.exec.calls[1].allowedTools)
	}
	// The narrowing must NOT leak the other step's tool into this step.
	if contains(h.exec.calls[0].allowedTools, "qgen.invoke") {
		t.Error("step 0 allowlist leaked a later step's tool (union, not narrowed)")
	}
}

// ---- failure matrix --------------------------------------------------------

func TestRunStepFailureRefundsAndSinksNothing(t *testing.T) {
	h := newHarness()
	h.exec.failAt = 1 // second step explodes
	rr := newRunner(t, h)
	run, err := rr.Run(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("terminal failure must be captured in the run, not returned as a Go error: %v", err)
	}
	if h.sink.calls != 0 {
		t.Errorf("sink writes = %d, want 0 — a failed run sinks NOTHING", h.sink.calls)
	}
	if h.mana.refundCalls != 1 || h.mana.refundUnits != 35 {
		t.Errorf("refund calls=%d units=%d, want 1/35 on failure", h.mana.refundCalls, h.mana.refundUnits)
	}
	if run.Status != RunStatusFailed || run.Error == "" {
		t.Errorf("run status=%q err=%q, want failed + reason", run.Status, run.Error)
	}
	if !contains(h.obx.topics, TopicCompanionRitualRunCompleted) {
		t.Error("a failed run must still emit ritual_run_completed.v1 (terminal)")
	}
}

func TestRunArmorBlocked(t *testing.T) {
	h := newHarness()
	h.exec.blockAt = 0 // Armor blocks the first step
	rr := newRunner(t, h)
	run, err := rr.Run(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != RunStatusBlocked {
		t.Errorf("run status = %q, want blocked", run.Status)
	}
	if h.sink.calls != 0 {
		t.Error("blocked run must sink nothing")
	}
	if h.mana.refundCalls != 1 {
		t.Errorf("blocked run must refund, refund calls = %d", h.mana.refundCalls)
	}
	// Only the blocked step ran; the runner aborts.
	if len(h.exec.calls) != 1 {
		t.Errorf("steps executed = %d, want 1 (abort on block)", len(h.exec.calls))
	}
}

func TestRunSkippedBudgetIsNotAnError(t *testing.T) {
	h := newHarness()
	h.mana.insufficient = true // wallet + pool depleted
	rr := newRunner(t, h)
	run, err := rr.Run(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("skipped_budget is a DESIGNED paused state, never a Go/HTTP error: %v", err)
	}
	if run.Status != RunStatusSkippedBudget || run.ManaCharged != 0 {
		t.Errorf("run status=%q charged=%d, want skipped_budget/0", run.Status, run.ManaCharged)
	}
	if len(h.exec.calls) != 0 || h.sink.calls != 0 {
		t.Error("skipped_budget must run no steps and sink nothing")
	}
	if h.mana.refundCalls != 0 {
		t.Error("nothing was reserved → nothing to refund")
	}
	if !contains(h.obx.topics, TopicCompanionRitualRunCompleted) {
		t.Error("skipped_budget still emits a terminal run event")
	}
}

// ---- determinism -----------------------------------------------------------

func TestRunDeterministicControlFlow(t *testing.T) {
	rit := publishedRitual(t)
	var first, second []recordedStep
	for i, dst := range []*[]recordedStep{&first, &second} {
		h := newHarness()
		rr := newRunner(t, h)
		if _, err := rr.Run(context.Background(), runInput(rit)); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
		*dst = h.exec.calls
	}
	if len(first) != len(second) {
		t.Fatalf("non-deterministic step count: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].index != second[i].index || first[i].skillKey != second[i].skillKey ||
			!sameSet(first[i].allowedTools, second[i].allowedTools) {
			t.Errorf("step %d differs across runs: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// ---- stale-running sweep (replicas=1 mitigation, ADR-219 D4) ---------------

func TestSweepStaleRunningFailsAndRefunds(t *testing.T) {
	h := newHarness()
	h.runs.stale = []*RitualRun{
		{RunID: "orphan-1", TenantID: "t-1", OwnerGCID: "gcid-1", Status: RunStatusRunning, ManaCharged: 35},
	}
	rr := newRunner(t, h)
	n, err := rr.SweepStale(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 1 {
		t.Errorf("swept = %d, want 1", n)
	}
	if h.mana.refundCalls != 1 {
		t.Errorf("a swept orphan must be refunded, refund calls = %d", h.mana.refundCalls)
	}
	// The orphan is marked failed (never left dangling 'running').
	if len(h.runs.updated) != 1 || h.runs.updated[0].Status != RunStatusFailed {
		t.Errorf("swept orphan not marked failed: %+v", h.runs.updated)
	}
}

// ctxCaptureRunRepo records whether the ctx handed to UpdateRun was already
// cancelled. A real pg driver aborts a query on a cancelled ctx, so a
// non-detached terminate() would fail the terminal write and strand the row
// 'running' when the request ctx cancels mid-run (CHO-2138).
type ctxCaptureRunRepo struct {
	fakeRunRepo
	updateCtxCancelled bool
}

func (f *ctxCaptureRunRepo) UpdateRun(ctx context.Context, r *RitualRun) error {
	if ctx.Err() != nil {
		f.updateCtxCancelled = true
		return ctx.Err() // simulate the pg driver aborting on a cancelled ctx
	}
	return f.fakeRunRepo.UpdateRun(ctx, r)
}

// CHO-2138 pillar 1: the gateway 504s the run POST at 5s while LLM steps run
// ~30s, cancelling the request ctx mid-run. terminate() must detach its
// write+publish (context.WithoutCancel) so the run always reaches a terminal
// state — the row must NEVER strand 'running'.
func TestTerminateSurvivesCancelledRequestContext(t *testing.T) {
	h := newHarness()
	repo := &ctxCaptureRunRepo{}
	rr, err := NewRitualRunner(RitualRunnerConfig{
		Steps: h.exec, Sink: h.sink, Mana: h.mana, Runs: repo, Outbox: h.obx,
		Catalogue:      &fakeCatalogReader{entries: ritualTestCatalogue()},
		StepTimeout:    60 * time.Second,
		Clock:          fixedClock(),
		NewID:          func() string { return "run-1" },
		NewTraceparent: func() string { return runTestTraceparent },
	})
	if err != nil {
		t.Fatalf("NewRitualRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client/gateway dropped (gateway 504) — request ctx is done

	run, err := rr.Run(ctx, runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("detached terminate must survive a cancelled request ctx (no strand): %v", err)
	}
	if repo.updateCtxCancelled {
		t.Error("terminate persisted on a CANCELLED ctx — it must detach via context.WithoutCancel")
	}
	if run == nil || run.Status != RunStatusCompleted {
		t.Fatalf("run should reach a terminal state, got %+v", run)
	}
	if len(repo.updated) != 1 || repo.updated[0].Status != RunStatusCompleted {
		t.Errorf("terminal row not persisted completed: %+v", repo.updated)
	}
}

// CHO-2138 pillar 2 (AC2): a swept orphan must emit a terminal
// ritual_run_completed(failed) so the O+ audit projection is not left with a
// phantom 'running'. The orphan's originating request trace is not persisted,
// so the sweep mints a fresh W3C traceparent (the envelope requires one —
// CHO-2136) rather than publishing a blank envelope that outbox validation
// rejects.
func TestSweepStalePublishesFailedTerminalEvent(t *testing.T) {
	h := newHarness()
	h.runs.stale = []*RitualRun{
		{RunID: "orphan-1", TenantID: "t-1", OwnerGCID: "gcid-1", RitualID: "rit-1",
			Status: RunStatusRunning, ManaCharged: 20, ReservationID: "resv-x"},
	}
	rr := newRunner(t, h)
	n, err := rr.SweepStale(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept = %d, want 1", n)
	}
	payload := h.obx.payloadFor(TopicCompanionRitualRunCompleted)
	if payload == nil {
		t.Fatal("swept orphan did not publish ritual_run_completed (O+ audit would keep a phantom running)")
	}
	if payload["status"] != string(RunStatusFailed) {
		t.Errorf("swept terminal status = %v, want failed", payload["status"])
	}
	var env *LoadoutEnvelope
	for i, tp := range h.obx.topics {
		if tp == TopicCompanionRitualRunCompleted {
			env = &h.obx.envelopes[i]
			break
		}
	}
	if env == nil || env.Traceparent == "" {
		t.Error("swept terminal event envelope missing a W3C traceparent (would fail outbox validation)")
	}
}

// ---- helpers ---------------------------------------------------------------

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// ---- CHO-2145: two-phase ack (StartRun) + detached execution (Execute) ------
//
// The run POST cannot stay synchronous: a multi-step LLM run takes ~30s while
// the api-gateway 504s the route at 5s, so a healthy run surfaced as
// "Something went wrong". The runner therefore splits into a SYNCHRONOUS
// start (pre-flight → reserve → durable 'running' row, all fast) that the HTTP
// adapter acks 202 with, and a DETACHED Execute (the slow phase). Run() stays
// the composed whole for callers that can wait.

// StartRun must hand back a DURABLE, identified, still-running row having
// executed nothing — that row is what the 202 carries and what the FE polls.
func TestStartRunAcksDurableRunningRowBeforeAnyStep(t *testing.T) {
	h := newHarness()
	rr := newRunner(t, h)

	p, err := rr.StartRun(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if p == nil || p.Run == nil || p.Run.RunID == "" {
		t.Fatal("StartRun must hand back the run id — the 202 ack + poll key")
	}
	if p.Terminal() {
		t.Error("a reserved multi-step run is not terminal at ack")
	}
	if p.Run.Status != RunStatusRunning {
		t.Errorf("ack status = %v, want running", p.Run.Status)
	}
	// Durable BEFORE the ack: the FE polls GET /runs by this id, and the stale
	// sweep can only reclaim a row that exists (no strand on a pod death).
	if len(h.runs.created) != 1 || h.runs.created[0].Status != RunStatusRunning {
		t.Errorf("running row not persisted at ack: %+v", h.runs.created)
	}
	if h.mana.reserveCalls != 1 {
		t.Errorf("reserve calls = %d, want 1 (reserved up-front, before the ack)", h.mana.reserveCalls)
	}
	// The SLOW phase must not have run.
	if len(h.exec.calls) != 0 || h.sink.calls != 0 || len(h.runs.updated) != 0 {
		t.Errorf("StartRun executed the slow phase: steps=%d sink=%d terminalWrites=%d",
			len(h.exec.calls), h.sink.calls, len(h.runs.updated))
	}
	if len(h.obx.topics) != 0 {
		t.Errorf("no terminal event may fire at ack, got %v", h.obx.topics)
	}
}

// Execute finishes a started run — steps → atomic sink → terminal + event —
// against the SAME row StartRun created (one create, one update; never a
// second row, never a double charge).
func TestExecuteFinishesStartedRunToTerminal(t *testing.T) {
	h := newHarness()
	rr := newRunner(t, h)
	p, err := rr.StartRun(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := p.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if run.Status != RunStatusCompleted || run.SinkRef == "" {
		t.Fatalf("terminal = %+v, want completed with a sink ref", run)
	}
	if run.RunID != p.Run.RunID {
		t.Errorf("Execute terminal run id %q != acked id %q", run.RunID, p.Run.RunID)
	}
	if len(h.exec.calls) != 2 || h.sink.calls != 1 {
		t.Errorf("steps=%d sink=%d, want 2/1", len(h.exec.calls), h.sink.calls)
	}
	if run.ManaCharged != 35 || h.mana.reserveCalls != 1 || h.mana.refundCalls != 0 {
		t.Errorf("economy wrong: charged=%d reserves=%d refunds=%d, want 35/1/0",
			run.ManaCharged, h.mana.reserveCalls, h.mana.refundCalls)
	}
	if len(h.runs.created) != 1 || len(h.runs.updated) != 1 {
		t.Errorf("created=%d updated=%d, want 1/1 (the acked row, transitioned)",
			len(h.runs.created), len(h.runs.updated))
	}
	if len(h.obx.topics) != 1 || h.obx.topics[0] != TopicCompanionRitualRunCompleted {
		t.Errorf("terminal event = %v, want one ritual_run_completed", h.obx.topics)
	}
}

// Insufficient mana is TERMINAL AT ACK (skipped_budget — a designed pause, not
// an error): the row exists, so the caller renders the pause straight from the
// ack and never polls. Execute on it is a no-op — no steps, no double charge.
func TestStartRunSkippedBudgetIsTerminalAtAck(t *testing.T) {
	h := newHarness()
	h.mana.insufficient = true
	rr := newRunner(t, h)

	p, err := rr.StartRun(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("skipped_budget must never be a Go error: %v", err)
	}
	if !p.Terminal() || p.Run.Status != RunStatusSkippedBudget {
		t.Fatalf("want terminal skipped_budget at ack; got terminal=%v status=%v", p.Terminal(), p.Run.Status)
	}
	run, err := p.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute on an already-terminal pending run: %v", err)
	}
	if run.Status != RunStatusSkippedBudget || len(h.exec.calls) != 0 || h.sink.calls != 0 {
		t.Errorf("Execute must be a no-op on a terminal pending run: status=%v steps=%d sink=%d",
			run.Status, len(h.exec.calls), h.sink.calls)
	}
}

// A pre-flight guard failure yields NO pending run: the adapter must map it to
// a 4xx, never a 202 carrying a run id the FE would poll for forever.
func TestStartRunPreflightGuardYieldsNoPendingRun(t *testing.T) {
	h := newHarness()
	in := runInput(publishedRitual(t))
	in.Caps.Stage = 3 // below the structural unlock

	p, err := newRunner(t, h).StartRun(context.Background(), in)
	if !errors.Is(err, ErrRitualUnlockStage) {
		t.Fatalf("err = %v, want ErrRitualUnlockStage", err)
	}
	if p != nil {
		t.Errorf("no pending run on a guard failure, got %+v", p)
	}
	if len(h.runs.created) != 0 || h.mana.reserveCalls != 0 {
		t.Error("a guard failure must not persist a row or reserve mana")
	}
}

// Execute BOUNDS the detached phase (RunTimeout). A wedged step (hung LLM turn)
// must not hold the row 'running' forever behind the adapter's un-cancellable
// ctx: the deadline fires, the step errors, and the run reaches a terminal
// state + refunds. Detach must never mean unbounded.
func TestExecuteIsBoundedByRunTimeout(t *testing.T) {
	h := newHarness()
	h.exec.wedge = true
	rr, err := NewRitualRunner(RitualRunnerConfig{
		Steps: h.exec, Sink: h.sink, Mana: h.mana, Runs: h.runs, Outbox: h.obx,
		Catalogue:      &fakeCatalogReader{entries: ritualTestCatalogue()},
		RunTimeout:     30 * time.Millisecond,
		Clock:          fixedClock(),
		NewID:          func() string { return "run-1" },
		NewTraceparent: func() string { return runTestTraceparent },
	})
	if err != nil {
		t.Fatalf("NewRitualRunner: %v", err)
	}

	p, err := rr.StartRun(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// context.Background() has no deadline — only the runner's own RunTimeout
	// can free the wedged step.
	run, err := p.Execute(context.Background())
	if err != nil {
		t.Fatalf("a wedged step is a terminal outcome, not an infra error: %v", err)
	}
	if run.Status != RunStatusFailed {
		t.Errorf("status = %v, want failed (RunTimeout reclaimed the wedged run)", run.Status)
	}
	if run.ManaCharged != 0 || h.mana.refundCalls != 1 {
		t.Errorf("a wedged run must refund: charged=%d refunds=%d", run.ManaCharged, h.mana.refundCalls)
	}
	if len(h.runs.updated) != 1 || h.runs.updated[0].Status != RunStatusFailed {
		t.Errorf("terminal row not written: %+v", h.runs.updated)
	}
}
