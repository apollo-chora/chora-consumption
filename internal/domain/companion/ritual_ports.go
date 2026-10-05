// ritual_ports.go — driven ports the deterministic Ritual runner depends on
// (hexagonal: the domain owns the interfaces; adapters in
// internal/adapter/** satisfy them). Per the owner ruling (2026-07-07) the
// sequencer stays in this domain; the per-step LLM turn sits behind
// StepExecutor, dispatched through the model-gateway chokepoint (ADR-219 D4).
package companion

import (
	"context"
	"errors"
	"time"
)

// StepExecutor runs ONE Ritual step as a gateway-routed agent turn (Armor +
// mana metering central, ADR-177) with the tool allowlist NARROWED to this
// step's Skill. The production adapter dispatches to the companion engine via
// chora-model-gateway. It is the sole non-deterministic seam; the runner
// sequences deterministically around it.
type StepExecutor interface {
	ExecuteStep(ctx context.Context, in StepExecInput) (StepExecResult, error)
}

// StepExecInput is one step's turn request. AllowedTools is the NARROWED set
// (this Skill's tool_handler_refs only — never the innate∪equipped union).
type StepExecInput struct {
	TenantID     string
	CompanionID  string
	OwnerGCID    string
	RunID        string
	StepIndex    int
	SkillKey     string
	Params       map[string]any
	AllowedTools []string
	Traceparent  string
	Tracestate   string
}

// StepExecResult is one step's outcome. Output is buffered (NOT yet sunk);
// Stamp is the ADR-197 per-turn decision stamp.
//
// THREE terminal shapes, deliberately distinct (ADR-257 D7):
//
//   - Blocked: an Armor content block. Terminal 'blocked', learner copy
//     "stopped for safety".
//   - Suspended: an ADR-252 containment denial. The gateway refused the turn
//     because an operator paused that companion. Terminal 'companion_suspended',
//     learner copy a PAUSE, because it is a deliberate act and not a fault.
//   - neither, plus a non-nil error: a generic execution failure.
//
// Collapsing the middle one into either neighbour is the defect this shape
// exists to prevent: as a failure it blames the product for an operator's
// decision, and as a block it tells the learner their own content was unsafe.
type StepExecResult struct {
	Output      any
	Stamp       StepStamp
	Blocked     bool
	BlockReason string
	// Suspended marks an ADR-252 containment denial. It is set from the
	// gateway's REJECTED reason, NEVER inferred from an empty result: ADR-252 D6
	// requires the two to be distinguished by error and never by emptiness, and
	// names that as the single most likely way to build this wrong.
	Suspended bool
	// SuspendedCompanionID names WHICH companion is paused. Under a peer step it
	// may not be the one the learner is looking at, which is the whole reason
	// the reason has to carry an id.
	SuspendedCompanionID string
}

// SinkWriter writes the buffered step outputs to the ONE declared sink,
// atomically on completion. Called exactly once per successful run, never on a
// failed/blocked/skipped run.
type SinkWriter interface {
	WriteToSink(ctx context.Context, sink string, in SinkWriteInput) (sinkRef string, err error)
}

type SinkWriteInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	RunID       string
	Outputs     []any
}

// RitualReserveInput / RitualReserveResult / RitualRefundInput shape the
// reserve→refund economy (proofingtest idiom, owner-ruled 2026-07-07): Reserve
// DEDUCTS the composed-flat price up-front (action companion_ritual_run,
// idempotency key companion_ritual_run:{tenant}:{gcid}:{run_id} — the handle
// doubles as ReservationID); success keeps the debit (= charged on
// completion); failure/block refunds it.
type RitualReserveInput struct {
	TenantID string
	GCID     string
	RunID    string
	Units    int
}

type RitualReserveResult struct {
	ReservationID string
	PriceUnits    int
}

type RitualRefundInput struct {
	TenantID      string
	GCID          string
	ReservationID string
	Units         int
	Reason        string
}

// RitualManaReserver is the reserve→refund port. Reserve returns
// *ErrInsufficientMana (errors.Is ErrInsufficientManaSentinel) on a genuine
// balance refusal — the runner maps that to skipped_budget (designed pause,
// never an error). Any other Reserve error is infrastructure and MUST surface
// fail-loud (a paid action never fails open).
type RitualManaReserver interface {
	Reserve(ctx context.Context, in RitualReserveInput) (RitualReserveResult, error)
	Refund(ctx context.Context, in RitualRefundInput) error
}

// RunRepository persists RitualRun records and powers the stale-running sweep
// (replicas=1 mitigation — orphaned 'running' rows → failed + refund).
type RunRepository interface {
	CreateRun(ctx context.Context, run *RitualRun) error
	UpdateRun(ctx context.Context, run *RitualRun) error
	SweepStaleRunning(ctx context.Context, olderThan time.Time) ([]*RitualRun, error)
	// ListRunsByRitual returns a ritual's runs newest-first (tenant-scoped),
	// capped at limit (<=0 ⇒ a sane default). Powers GET …/rituals/{id}/runs.
	ListRunsByRitual(ctx context.Context, tenantID, ritualID string, limit int) ([]*RitualRun, error)
	// GetRun returns ONE run by id, tenant-scoped. Powers the replay read
	// GET …/rituals/{id}/runs/{runId}, which the editor opens from a link and
	// which must be able to reach a run that has fallen off the capped list.
	//
	// Returns ErrRitualRunNotFound when this tenant has no such run, NEVER a
	// zero-valued run with a nil error: an empty 200 renders as a run that
	// happened and produced nothing, which is the one answer that is worse
	// than an error.
	GetRun(ctx context.Context, tenantID, runID string) (*RitualRun, error)
}

// ErrRitualRunNotFound is returned by RunRepository.GetRun when the tenant has
// no run with that id. Given its own sentinel rather than folded into a generic
// read failure, because the handler must answer 404 for this and 500 for a
// store that could not be read: a store outage rendered as "that run does not
// exist" would tell the learner their run was lost.
var ErrRitualRunNotFound = errors.New("companion.ritual_run: not found")

// RitualOutbox durably publishes Ritual lifecycle events (outbox pattern,
// envelope-compliant — reuses LoadoutEnvelope).
type RitualOutbox interface {
	PublishRitualEvent(ctx context.Context, topic string, payload map[string]any, env LoadoutEnvelope) error
}
