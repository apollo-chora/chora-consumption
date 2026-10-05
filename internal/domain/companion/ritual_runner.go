// ritual_runner.go — the DETERMINISTIC Go runner (CHO-2016 P4, ADR-219 D4 +
// spec §6). Rituals are DATA interpreted by platform code: the runner
// sequences one gateway-routed agent turn per step (allowlist narrowed to the
// step's Skill), buffers outputs, writes them ATOMICALLY to the one declared
// sink on completion, and reserves→charges/refunds mana. Control flow is
// data-driven (no branching/loops in v1, no LLM-decided routing) → the
// sequence, per-step allowlist, and stamping are deterministic; only the LLM
// turn's content varies (behind StepExecutor).
//
// Failure matrix (spec §6): step error → failed+refund; Armor block →
// blocked+refund; insufficient mana → skipped_budget (no error); all terminal
// outcomes are captured in the run record with err==nil — only infrastructure
// faults surface as a Go error (fail-loud).
package companion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// RitualRunnerConfig wires the runner (config-injection like PathUnlocker;
// Clock/NewID injected for test determinism, StepTimeout ⚙ 60s).
type RitualRunnerConfig struct {
	Steps       StepExecutor
	Sink        SinkWriter
	Mana        RitualManaReserver
	Runs        RunRepository
	Outbox      RitualOutbox
	Catalogue   CatalogReader
	StepTimeout time.Duration
	// TerminateTimeout bounds the DETACHED terminal write+publish (CHO-2138). A
	// client/gateway disconnect cancels the request ctx mid-run; terminate() runs
	// its persist+publish on a context.WithoutCancel copy so the run always
	// reaches a terminal state, and this deadline caps that detached work. ⚙ 30s.
	TerminateTimeout time.Duration
	// RunTimeout bounds ONE whole run's slow phase (Execute: all steps + the
	// sink write). The 202 lane runs Execute on a ctx with no deadline of its
	// own (it is detached from the request — CHO-2145), so this is the only
	// thing standing between a wedged LLM turn and a row stuck 'running'.
	// Must exceed a legit full run (StepTimeout × max steps = 60s × 8) and stay
	// well under StaleCutoff, so a live run is never racing the sweep. ⚙ 10m.
	RunTimeout time.Duration
	// StaleCutoff: a 'running' row older than this is reclaimed by SweepStaleNow
	// (a crashed/disconnected runner orphan). Must exceed the longest legit run
	// (StepTimeout × max steps). ⚙ 30m.
	StaleCutoff time.Duration
	Clock       func() time.Time
	NewID       func() string
	// NewTraceparent mints a fresh W3C traceparent for a SWEPT run's terminal
	// event: the orphan's originating request trace was never persisted, but the
	// terminal envelope still requires W3C trace context (CHO-2136). ⚙ crypto/rand
	// root; the composition root may inject tracing.EnsureTraceparent for parity.
	NewTraceparent func() string
}

// RitualRunner is the deterministic sequencer.
type RitualRunner struct {
	cfg RitualRunnerConfig
}

// NewRitualRunner validates dependencies and applies safe defaults.
func NewRitualRunner(cfg RitualRunnerConfig) (*RitualRunner, error) {
	if cfg.Steps == nil || cfg.Sink == nil || cfg.Mana == nil || cfg.Runs == nil || cfg.Outbox == nil || cfg.Catalogue == nil {
		return nil, errors.New("companion.ritualrunner: steps/sink/mana/runs/outbox/catalogue all required")
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 60 * time.Second
	}
	if cfg.TerminateTimeout <= 0 {
		cfg.TerminateTimeout = 30 * time.Second
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = 10 * time.Minute
	}
	if cfg.StaleCutoff <= 0 {
		cfg.StaleCutoff = 30 * time.Minute
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		cfg.NewID = domain.NewUUIDv7
	}
	if cfg.NewTraceparent == nil {
		cfg.NewTraceparent = defaultTraceparent
	}
	return &RitualRunner{cfg: cfg}, nil
}

// defaultTraceparent mints a fresh root W3C traceparent (crypto/rand) so a swept
// run's terminal event passes outbox envelope validation. Mirrors
// tracing.newTraceparent — the domain avoids importing the HTTP tracing lib.
func defaultTraceparent() string {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	_, _ = rand.Read(traceID)
	_, _ = rand.Read(spanID)
	return "00-" + hex.EncodeToString(traceID) + "-" + hex.EncodeToString(spanID) + "-01"
}

// RunInput identifies the run. Ritual is the published + enabled ritual; Caps
// carries the CURRENT capability state for the run-time equipped-active
// re-check (owner ruling #6, 2026-07-07 — a since-unequipped/deactivated Skill
// fails loud BEFORE any spend).
type RunInput struct {
	TenantID      string
	CompanionID   string
	OwnerGCID     string
	Ritual        *Ritual
	Caps          RitualCapabilityContext
	TriggerSource string
	Traceparent   string
	Tracestate    string
}

// PendingRun is a STARTED run: the pre-flight passed, the mana is reserved, and
// the 'running' row is DURABLE — so a caller can acknowledge the run
// immediately (202 + run id) and finish the slow phase elsewhere. Execute
// finishes it.
//
// This split exists because the run's slow phase (one gateway-routed LLM turn
// per step, ~30s) outlives the api-gateway's 5s route budget: a synchronous run
// POST was 504'd at the edge and the learner saw "Something went wrong" on a
// perfectly healthy run (CHO-2145). The HTTP lane now acks StartRun's row and
// calls Execute on a ctx DETACHED from the request.
type PendingRun struct {
	// Run is the persisted row as of the ack: 'running' (Execute finishes it),
	// or already-terminal 'skipped_budget' (the reserve refused — a designed
	// pause with nothing left to execute).
	//
	// ⚠ Execute TAKES OWNERSHIP of it (stamps, status, terminal fields are
	// mutated in place). Read it only BEFORE calling Execute — reading it
	// afterwards from another goroutine is a data race (the 202 lane snapshots
	// its response DTO first, then hands the run off).
	Run *RitualRun

	rr        *RitualRunner
	in        RunInput
	steps     []RitualStep
	catalogue map[string]CatalogEntry
	terminal  bool
}

// Terminal reports whether the run reached a terminal state during StartRun
// (skipped_budget). Execute on a terminal PendingRun is a no-op — the caller
// renders the outcome straight from the ack and never polls.
func (p *PendingRun) Terminal() bool { return p.terminal }

// Run executes the ritual deterministically, start to terminal, on the caller's
// ctx — the composed whole for a caller that can wait (an unattended trigger).
// Terminal outcomes (completed/failed/blocked/skipped_budget) are returned as
// (run, nil); pre-flight guard failures and infrastructure faults return
// (nil, err).
//
// The HTTP run route does NOT use this: it acks StartRun's durable row with a
// 202 and runs Execute detached (CHO-2145). Both lanes are the same code path.
func (rr *RitualRunner) Run(ctx context.Context, in RunInput) (*RitualRun, error) {
	p, err := rr.StartRun(ctx, in)
	if err != nil {
		return nil, err
	}
	return p.Execute(ctx)
}

// StartRun performs the FAST phase: the pre-flight guards (fail-loud — the
// caller maps these to a 4xx, never to an ack), the up-front mana reserve, and
// the durable 'running' row. Everything here is a bounded DB/gRPC hop, so the
// caller can acknowledge within the edge's route budget.
//
// Returns (nil, err) on a guard failure or infrastructure fault — there is no
// run id to hand out, so nothing can be left polling for one.
func (rr *RitualRunner) StartRun(ctx context.Context, in RunInput) (*PendingRun, error) {
	r := in.Ritual
	if r == nil {
		return nil, errors.New("companion.ritualrunner: nil ritual")
	}
	if r.DeletedAt != nil {
		return nil, errors.New("companion.ritualrunner: ritual is deleted")
	}
	// NOTE: an explicit run does NOT require r.Enabled — the enabled flag +
	// enabled-quota gate UNATTENDED triggers (on_map_open / on_dose_completed /
	// P6 autonomy), not a learner's explicit "run" click. A published ritual is
	// always runnable on demand.
	if r.CurrentRevision == 0 || len(r.Revisions) == 0 {
		return nil, fmt.Errorf("companion.ritualrunner: %w", ErrRitualNoPublishedRevision)
	}
	if !RitualsUnlockedForStage(in.Caps.Stage) {
		return nil, fmt.Errorf("companion.ritualrunner: %w (stage %d)", ErrRitualUnlockStage, in.Caps.Stage)
	}
	// Pre-spend guard (CHO-2136): the terminal ritual_run_completed envelope
	// REQUIRES W3C trace context (outbox envelope validation). Failing here —
	// before reserve — beats failing after the run persisted, which stranded a
	// terminal row with no event. Callers mint at the boundary
	// (tracing.EnsureTraceparent in the HTTP handler).
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, errors.New("companion.ritualrunner: run input traceparent required (terminal event envelope needs W3C trace context — mint at the caller boundary)")
	}
	revSteps := r.CurrentSteps()
	// Ruling #6: re-validate equipped-active at RUN time — a Skill unequipped
	// or eval-deactivated since publish fails loud before any spend.
	if err := r.ValidateSteps(revSteps, in.Caps); err != nil {
		return nil, fmt.Errorf("companion.ritualrunner: run-time capability re-check: %w", err)
	}

	catalogue, err := rr.cfg.Catalogue.ListCatalogue(ctx)
	if err != nil {
		return nil, fmt.Errorf("companion.ritualrunner: load catalogue: %w", err)
	}

	now := rr.cfg.Clock()
	run := &RitualRun{
		RunID:         rr.cfg.NewID(),
		TenantID:      in.TenantID,
		CompanionID:   in.CompanionID,
		OwnerGCID:     in.OwnerGCID,
		RitualID:      r.RitualID,
		RitualName:    r.Name,
		RevisionNo:    r.CurrentRevision,
		TriggerSource: in.TriggerSource,
		Status:        RunStatusRunning,
		StartedAt:     now,
		traceparent:   in.Traceparent,
		tracestate:    in.Tracestate,
	}
	p := &PendingRun{Run: run, rr: rr, in: in, steps: revSteps, catalogue: catalogue}

	// Reserve the composed-flat price up-front (owner ruling #2). Insufficient
	// balance → skipped_budget: a DESIGNED paused state, never a Go/HTTP error.
	// It is TERMINAL AT ACK — nothing to execute, so the caller renders the
	// pause from the ack itself.
	reservation, rerr := rr.cfg.Mana.Reserve(ctx, RitualReserveInput{
		TenantID: in.TenantID, GCID: in.OwnerGCID, RunID: run.RunID, Units: r.PublishedPriceUnits,
	})
	if rerr != nil {
		if errors.Is(rerr, ErrInsufficientManaSentinel) {
			run.ManaCharged = 0
			skipped, terr := rr.terminate(ctx, run, RunStatusSkippedBudget, "", "insufficient mana — paused (top up to run)")
			if terr != nil {
				return nil, terr
			}
			p.Run, p.terminal = skipped, true
			return p, nil
		}
		return nil, fmt.Errorf("companion.ritualrunner: reserve: %w", rerr)
	}
	run.ReservationID = reservation.ReservationID
	run.ManaCharged = r.PublishedPriceUnits // reserved snapshot (kept on success)

	// Persist the running row BEFORE the ack: it is the id the learner polls,
	// and the stale-sweep can only reclaim a row that exists (pod death mid-run).
	if err := rr.cfg.Runs.CreateRun(ctx, run); err != nil {
		rr.refund(run, "create_run_failed")
		return nil, fmt.Errorf("companion.ritualrunner: create run: %w", err)
	}
	return p, nil
}

// Execute finishes a started run — the SLOW phase: one narrowed gateway turn
// per step, the buffered-then-atomic sink write, and the terminal transition +
// event. Terminal outcomes are returned as (run, nil); only infrastructure
// faults return an error.
//
// Execute BOUNDS itself with RunTimeout. The 202 lane hands it a ctx that is
// deliberately un-cancellable (detached from the dead request), so this
// deadline is the only thing that can free a wedged LLM turn — and terminate()
// persists on a ctx of its own, so even a RunTimeout expiry lands a real
// terminal row (failed + refund) instead of stranding it 'running'.
func (p *PendingRun) Execute(ctx context.Context) (*RitualRun, error) {
	if p.terminal {
		return p.Run, nil // skipped_budget: nothing to run, and never re-charge
	}
	rr, run, in := p.rr, p.Run, p.in

	ctx, cancelRun := context.WithTimeout(ctx, rr.cfg.RunTimeout)
	defer cancelRun()

	// Sequence the steps: one narrowed gateway turn each; buffer outputs.
	//
	// TWO buffers, deliberately. `outputs` is the raw payload the sink receives
	// on completion; `rec` is the bounded, marker-neutralised S8 record that is
	// persisted on EVERY terminal (ADR-257 section 6 clause 2). Terminating
	// through `stop` rather than calling terminate directly is what makes that
	// clause true by construction: there is no path out of this function that
	// can forget to carry the outputs of the steps that did complete.
	outputs := make([]any, 0, len(p.steps))
	rec := NewStepOutputRecorder()
	stop := func(status RunStatus, sinkRef, errMsg string) (*RitualRun, error) {
		run.StepOutputs = rec.Outputs()
		return rr.terminate(ctx, run, status, sinkRef, errMsg)
	}
	for i, step := range p.steps {
		entry := p.catalogue[step.SkillKey]
		allowed := append([]string(nil), entry.ToolHandlerRefs...) // narrowed to THIS step only
		stepCtx, cancel := context.WithTimeout(ctx, rr.cfg.StepTimeout)
		res, execErr := rr.cfg.Steps.ExecuteStep(stepCtx, StepExecInput{
			TenantID: in.TenantID, CompanionID: in.CompanionID, OwnerGCID: in.OwnerGCID,
			RunID: run.RunID, StepIndex: i, SkillKey: step.SkillKey, Params: step.Params,
			AllowedTools: allowed, Traceparent: in.Traceparent, Tracestate: in.Tracestate,
		})
		cancel()
		if execErr != nil {
			rr.refund(run, "step_failed")
			return stop(RunStatusFailed, "",
				fmt.Sprintf("step %d (%s) failed: %v", i, step.SkillKey, execErr))
		}
		// ADR-252 D5 / ADR-257 D7: a containment denial is its own terminal.
		// Checked BEFORE Blocked so the two can never be conflated, and refunded
		// like every other non-completed terminal: the gateway's deny lands
		// before its debit, but the ritual economy reserved up front, so the
		// reservation has to come back.
		if res.Suspended {
			rr.refund(run, KennelErrCodeCompanionSuspended)
			run.PausedCompanionID = res.SuspendedCompanionID
			if run.PausedCompanionID == "" {
				// Fail INFORMATIVE rather than silent: the terminal is still
				// correct, and an unnamed pause is better than a pause misfiled
				// as a fault. Recorded in the error so it is visible to an
				// auditor and to whoever wired the executor.
				run.Error = "containment denial carried no companion id"
			}
			return stop(RunStatusCompanionSuspended, "",
				fmt.Sprintf("step %d (%s) paused: companion %s is suspended",
					i, step.SkillKey, res.SuspendedCompanionID))
		}
		if res.Blocked {
			rr.refund(run, "armor_blocked")
			return stop(RunStatusBlocked, "",
				fmt.Sprintf("step %d (%s) blocked: %s", i, step.SkillKey, res.BlockReason))
		}
		run.Stamps = append(run.Stamps, res.Stamp)
		outputs = append(outputs, res.Output)
		rec.Record(i, step, res.Output)
	}

	// Atomic sink write on completion — a failed run has already returned
	// above, having sunk nothing.
	sinkRef, serr := rr.cfg.Sink.WriteToSink(ctx, in.Ritual.Sink, SinkWriteInput{
		TenantID: in.TenantID, CompanionID: in.CompanionID, OwnerGCID: in.OwnerGCID,
		RunID: run.RunID, Outputs: outputs,
	})
	if serr != nil {
		rr.refund(run, "sink_write_failed")
		return stop(RunStatusFailed, "", fmt.Sprintf("sink write failed: %v", serr))
	}

	return stop(RunStatusCompleted, sinkRef, "")
}

// terminate finalises the run record + emits the terminal event. skipped_budget
// never created a running row (reserve failed first) → Create; every other
// terminal updates the running row.
func (rr *RitualRunner) terminate(ctx context.Context, run *RitualRun, status RunStatus, sinkRef, errMsg string) (*RitualRun, error) {
	now := rr.cfg.Clock()
	// Never persist a nil slice: step_outputs is NOT NULL DEFAULT '[]' and a
	// terminal that never entered the step loop (skipped_budget, a stale sweep)
	// must still write an empty array rather than a JSON null.
	if run.StepOutputs == nil {
		run.StepOutputs = []StepOutput{}
	}
	run.Status = status
	run.SinkRef = sinkRef
	run.Error = errMsg
	run.CompletedAt = &now
	// On any non-completed terminal the reserved units were refunded (or never
	// taken) → nothing is charged.
	if status != RunStatusCompleted {
		run.ManaCharged = 0
	}

	// Detach the terminal persistence + publish from the request ctx (CHO-2138):
	// the api-gateway 504s the run POST at 5s while the LLM steps run ~30s,
	// cancelling the request ctx mid-run. Without detaching, UpdateRun and
	// publishCompleted abort on the cancelled ctx and the row strands 'running'
	// forever with a leaked reservation (the O+ audit hole). WithoutCancel keeps
	// the ctx values (the tenant GUC source for RLS + the W3C trace span) but
	// ignores cancellation; a bounded deadline caps the detached work.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rr.cfg.TerminateTimeout)
	defer cancel()

	if status == RunStatusSkippedBudget {
		if err := rr.cfg.Runs.CreateRun(dctx, run); err != nil {
			return nil, fmt.Errorf("companion.ritualrunner: create skipped run: %w", err)
		}
	} else {
		if err := rr.cfg.Runs.UpdateRun(dctx, run); err != nil {
			return nil, fmt.Errorf("companion.ritualrunner: update run: %w", err)
		}
	}

	if err := rr.publishCompleted(dctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

// refund returns the reservation on a failed/blocked run (best-effort but
// fail-loud: a refund error is annotated onto the run, never silently dropped).
func (rr *RitualRunner) refund(run *RitualRun, reason string) {
	if run.ReservationID == "" && run.ManaCharged == 0 {
		return
	}
	if err := rr.cfg.Mana.Refund(context.Background(), RitualRefundInput{
		TenantID: run.TenantID, GCID: run.OwnerGCID, ReservationID: run.ReservationID,
		Units: run.ManaCharged, Reason: reason,
	}); err != nil {
		run.Error = strings.TrimSpace(run.Error + " ; refund failed: " + err.Error())
	}
}

// publishCompleted emits ritual_run_completed.v1 (outbox). Payload carries the
// full O+ record (stamps) + the terminal projection fields.
func (rr *RitualRunner) publishCompleted(ctx context.Context, run *RitualRun) error {
	now := rr.cfg.Clock()
	env := LoadoutEnvelope{
		EventID:        rr.cfg.NewID(),
		IdempotencyKey: "ritual_run:" + run.RunID,
		TenantID:       run.TenantID,
		GCID:           run.OwnerGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		// W3C trace context from RunInput (guarded non-blank at Run() entry) —
		// a hardcoded "" here failed outbox envelope validation and killed
		// EVERY terminal publish (CHO-2136 walk-found P0).
		Traceparent:   run.traceparent,
		Tracestate:    run.tracestate,
		SourceProject: "chora-content",
		SourceService: "chora-consumption",
		SchemaVersion: 1,
	}
	payload := map[string]any{
		"run_id":         run.RunID,
		"ritual_id":      run.RitualID,
		"companion_id":   run.CompanionID,
		"owner_gcid":     run.OwnerGCID,
		"revision_no":    run.RevisionNo,
		"trigger_source": run.TriggerSource,
		"status":         string(run.Status),
		"mana_charged":   run.ManaCharged,
		"sink_ref":       run.SinkRef,
		"error":          run.Error,
		// O+ full record (ADR-215 auditor projection) — wire-ready snake_case
		// maps (CHO-2136): the outbox binary encoder reads the flat-proto field
		// names, and raw StepStamp structs JSON-marshalled with Go field names
		// (no json tags), which the audit consumer's snake_case keys dropped.
		"stamps": stampsWirePayload(run.Stamps),
	}
	if err := rr.cfg.Outbox.PublishRitualEvent(ctx, TopicCompanionRitualRunCompleted, payload, env); err != nil {
		return fmt.Errorf("companion.ritualrunner: publish ritual_run_completed: %w", err)
	}
	return nil
}

// stampsWirePayload projects the domain stamps into wire-ready snake_case maps
// matching the ritual_run_completed flat-proto field names (CHO-2136).
func stampsWirePayload(stamps []StepStamp) []map[string]any {
	out := make([]map[string]any, 0, len(stamps))
	for _, s := range stamps {
		out = append(out, map[string]any{
			"step_index":     s.StepIndex,
			"skill_key":      s.SkillKey,
			"prompt_version": s.PromptVersion,
			"prompt_hash":    s.PromptHash,
			"tools_invoked":  s.ToolsInvoked,
			"citations":      s.Citations,
		})
	}
	return out
}

// SweepStale reclaims orphaned 'running' rows (a crashed replicas=1 runner
// left them dangling): each is refunded and marked failed. Returns the count
// swept. Fail-loud on repo/refund faults.
func (rr *RitualRunner) SweepStale(ctx context.Context, olderThan time.Time) (int, error) {
	stale, err := rr.cfg.Runs.SweepStaleRunning(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("companion.ritualrunner: sweep: %w", err)
	}
	n := 0
	for _, run := range stale {
		rr.refund(run, "stale_running_sweep")
		now := rr.cfg.Clock()
		run.Status = RunStatusFailed
		run.ManaCharged = 0
		run.Error = strings.TrimSpace("stale running run swept (replicas=1 mitigation) " + run.Error)
		run.CompletedAt = &now
		// Detach + bound the terminal write/publish (the sweep runs off a
		// short-lived tenant read ctx). WithoutCancel preserves the tenant GUC
		// source for RLS.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rr.cfg.TerminateTimeout)
		if err := rr.cfg.Runs.UpdateRun(dctx, run); err != nil {
			cancel()
			return n, fmt.Errorf("companion.ritualrunner: sweep update %s: %w", run.RunID, err)
		}
		// The orphan's originating request trace was never persisted; mint a fresh
		// root so the terminal envelope passes outbox validation (CHO-2136). AC2:
		// the O+ audit projection must see the failed terminal, not a phantom
		// 'running'.
		run.traceparent = rr.cfg.NewTraceparent()
		if err := rr.publishCompleted(dctx, run); err != nil {
			cancel()
			return n, fmt.Errorf("companion.ritualrunner: sweep publish %s: %w", run.RunID, err)
		}
		cancel()
		n++
	}
	return n, nil
}

// SweepStaleNow reclaims 'running' rows older than the configured StaleCutoff,
// using the runner clock. Opportunistic callers (a tenant-scoped ritual read)
// invoke this best-effort; the dedicated cross-tenant scheduler is a flagged
// follow-up (mirrors the WS-5 weakness-blob TTL-sweep pattern: RLS-scoped, no
// cross-tenant scan — CHO-2138).
func (rr *RitualRunner) SweepStaleNow(ctx context.Context) (int, error) {
	return rr.SweepStale(ctx, rr.cfg.Clock().Add(-rr.cfg.StaleCutoff))
}
