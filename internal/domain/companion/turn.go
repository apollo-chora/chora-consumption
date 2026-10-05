// turn.go - ADR-254 D4/D8: the Turn aggregate, the per-request state of every
// agent turn consumption dispatches over the bus.
//
// Flow (every caller, no direct agent dial anywhere):
//
//	caller  --companion_turn.requested.v1 (outbox, SAME tx as the turn row)-->  kennel
//	caller  <--companion_turn.completed.v1 (pull sub, any pod)---------------  kennel
//
// A turn is ACCEPTED the moment its row and its outbox request are durable
// (that is when the SSE chat sends the `accepted` frame); the pull consumer
// completes it from the wire result exactly once; the waiter marks it TIMEOUT
// if nothing arrives by deadline_at; a retried turn_id is answered from the
// stored result (the kennel dedupes the request key for seven days and never
// re-dispatches). The same aggregate carries the daily-dose recommendation lane
// (kind dose, lane dose_recommendation) so one store, one waiter and one
// consumer shape serve both caller-facing lanes.
//
// The caller-facing error codes are the contract promised to the SPA (WP-X):
// COMPANION_SUSPENDED (kennel REJECTED with error_code companion_suspended),
// COMPANION_TURN_REJECTED (any other REJECTED: surface_unstamped,
// tenant_in_flight_cap, ...), COMPANION_TURN_FAILED (kennel FAILED, incl. the
// reaper arms), COMPANION_TURN_TIMEOUT (consumption's own deadline).
package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TurnKind discriminates what the turn is for. The wire payload carries
// turn_kind "typed" for every companion_turn lane turn (ADR-254 D6
// discriminator); Kind is consumption's own finer bookkeeping.
type TurnKind string

const (
	TurnKindTyped    TurnKind = "typed"
	TurnKindSkill    TurnKind = "skill"
	TurnKindCeremony TurnKind = "ceremony"
	TurnKindRitual   TurnKind = "ritual"
	TurnKindGreeting TurnKind = "greeting"
	TurnKindDose     TurnKind = "dose"
)

// TurnLane names the caller-facing workflow pair the turn rides.
type TurnLane string

const (
	TurnLaneCompanionTurn      TurnLane = "companion_turn"
	TurnLaneDoseRecommendation TurnLane = "dose_recommendation"
)

// TurnStatus is the aggregate state.
type TurnStatus string

const (
	TurnStatusAccepted  TurnStatus = "accepted"
	TurnStatusCompleted TurnStatus = "completed"
	TurnStatusFailed    TurnStatus = "failed"
	TurnStatusRejected  TurnStatus = "rejected"
	TurnStatusTimeout   TurnStatus = "timeout"
)

// Wire result statuses (ADR-254 D4: every result carries status in
// {OK, FAILED, REJECTED}).
const (
	TurnWireStatusOK       = "OK"
	TurnWireStatusFailed   = "FAILED"
	TurnWireStatusRejected = "REJECTED"
)

// Caller-facing error codes (SSE `error` frame / HTTP body code).
const (
	TurnErrCodeSuspended = "COMPANION_SUSPENDED"
	TurnErrCodeRejected  = "COMPANION_TURN_REJECTED"
	TurnErrCodeFailed    = "COMPANION_TURN_FAILED"
	TurnErrCodeTimeout   = "COMPANION_TURN_TIMEOUT"
	// TurnErrCodeInvalidTurnID is the 400 for a client-minted turn_id that is
	// not a UUID (ADR-254 D8: the SPA mints it, so this is caller input).
	TurnErrCodeInvalidTurnID = "INVALID_TURN_ID"
)

// ErrInvalidTurnID is returned by ValidateTurnID.
var ErrInvalidTurnID = errors.New("companion: turn_id must be a UUID")

// ValidateTurnID normalises and checks a CLIENT-MINTED turn id, returning the
// trimmed id or ErrInvalidTurnID.
//
// Why this exists at all: migration 0111 declares `turn_id UUID PRIMARY KEY`,
// so a malformed id was already impossible to STORE. What it was not was
// impossible to SEND. Without this check the Postgres cast is the only thing
// rejecting it, which is fail-loud (nothing corrupt lands, and no kennel lane
// ever sees a non-UUID) but blames the wrong actor: the caller's own bad input
// surfaces as an internal error rather than a 400 they can act on.
//
// Surrounding whitespace is the client's formatting and is trimmed, not
// rejected. A value that is ONLY whitespace is rejected rather than treated as
// absent: present-but-unusable is a mistake worth naming, while a genuinely
// absent id is the documented path where consumption mints one.
//
// Callers must not "helpfully" fall back to a minted id when this fails. The
// whole point of a client-minted id is that a retried POST replays the stored
// result; silently substituting a fresh id would turn a caller's typo into a
// duplicate dispatch, which is the exact double-charge D8 exists to prevent.
func ValidateTurnID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", ErrInvalidTurnID
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidTurnID, id)
	}
	return id, nil
}

// KennelErrCodeCompanionSuspended is the kennel's REJECTED error_code when the
// gateway's ADR-252 suspension gate refused the dispatch.
const KennelErrCodeCompanionSuspended = "companion_suspended"

var (
	ErrTurnInvalid         = errors.New("companion.turn: invalid")
	ErrTurnNotFound        = errors.New("companion.turn: not found")
	ErrTurnExists          = errors.New("companion.turn: already exists")
	ErrTurnAlreadyTerminal = errors.New("companion.turn: already terminal")
	ErrTurnBadResult       = errors.New("companion.turn: bad result")
)

// Turn is the aggregate.
type Turn struct {
	TurnID         string
	TenantID       string
	OwnerGCID      string
	CompanionID    string // empty for a dose recommendation
	ConversationID string // typed chat only
	Kind           TurnKind
	Lane           TurnLane
	Status         TurnStatus

	Request json.RawMessage // the wire request body as published
	Result  json.RawMessage // the wire result body as received (nil until terminal)

	ErrorCode          string // the KENNEL's error_code (not the caller code)
	WorkflowID         string
	GeneratedByModelID string

	RequestedAt time.Time
	DeadlineAt  time.Time
	CompletedAt *time.Time

	// SettledBy names WHAT made this turn terminal, so a reaped turn is never
	// mistaken for an answered one. Empty while accepted. Mirrors the kernel's
	// ai_kernel_agent_dispatch_parks.settled_by vocabulary deliberately: the two
	// ledgers describe the same event from either end and should read alike.
	SettledBy TurnSettledBy

	// LateCompletionAt records that a real result arrived AFTER the turn was
	// already terminal. Without it the completion is ACKed and lost, and a
	// reaped-then-answered turn is indistinguishable from one that was never
	// answered at all. The status never flips back; this is evidence, not state.
	LateCompletionAt *time.Time
}

// TurnSettledBy is the terminal cause. A turn that timed out and a turn that
// was answered are BOTH terminal with a completed_at, so the status alone
// cannot carry the distinction the audit gates on.
type TurnSettledBy string

const (
	// TurnSettledByCompletion: a real result arrived before the deadline.
	TurnSettledByCompletion TurnSettledBy = "completion"
	// TurnSettledByTimeout: the deadline passed and the turn was reaped. No
	// result was applied, so Result stays nil and GeneratedByModelID stays empty
	// until and unless a late completion arrives.
	TurnSettledByTimeout TurnSettledBy = "timeout"
)

// NewTurnInput is what the caller knows at dispatch time.
type NewTurnInput struct {
	TurnID         string
	TenantID       string
	OwnerGCID      string
	CompanionID    string
	ConversationID string
	Kind           TurnKind
	Lane           TurnLane
	Request        json.RawMessage
	Now            time.Time
	Deadline       time.Duration
}

func (k TurnKind) valid() bool {
	switch k {
	case TurnKindTyped, TurnKindSkill, TurnKindCeremony, TurnKindRitual, TurnKindGreeting, TurnKindDose:
		return true
	}
	return false
}

func (l TurnLane) valid() bool {
	return l == TurnLaneCompanionTurn || l == TurnLaneDoseRecommendation
}

// NewTurn validates and mints an ACCEPTED turn. The turn id is supplied by the
// caller (a client-minted chat turn_id or a consumption UUIDv7) so that a retry
// of the same id can be recognised.
func NewTurn(in NewTurnInput) (*Turn, error) {
	switch {
	case strings.TrimSpace(in.TurnID) == "":
		return nil, fmt.Errorf("%w: turn_id required", ErrTurnInvalid)
	case strings.TrimSpace(in.TenantID) == "":
		return nil, fmt.Errorf("%w: tenant_id required", ErrTurnInvalid)
	case strings.TrimSpace(in.OwnerGCID) == "":
		return nil, fmt.Errorf("%w: owner_gcid required", ErrTurnInvalid)
	case !in.Kind.valid():
		return nil, fmt.Errorf("%w: unknown kind %q", ErrTurnInvalid, in.Kind)
	case !in.Lane.valid():
		return nil, fmt.Errorf("%w: unknown lane %q", ErrTurnInvalid, in.Lane)
	case len(in.Request) == 0:
		return nil, fmt.Errorf("%w: request body required", ErrTurnInvalid)
	case in.Deadline <= 0:
		return nil, fmt.Errorf("%w: deadline must be positive", ErrTurnInvalid)
	}
	if (in.Kind == TurnKindDose) != (in.Lane == TurnLaneDoseRecommendation) {
		return nil, fmt.Errorf("%w: kind %q does not ride lane %q", ErrTurnInvalid, in.Kind, in.Lane)
	}
	if in.Kind != TurnKindDose && strings.TrimSpace(in.CompanionID) == "" {
		return nil, fmt.Errorf("%w: companion_id required for kind %q", ErrTurnInvalid, in.Kind)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	return &Turn{
		TurnID:         in.TurnID,
		TenantID:       in.TenantID,
		OwnerGCID:      in.OwnerGCID,
		CompanionID:    in.CompanionID,
		ConversationID: in.ConversationID,
		Kind:           in.Kind,
		Lane:           in.Lane,
		Status:         TurnStatusAccepted,
		Request:        append(json.RawMessage(nil), in.Request...),
		RequestedAt:    now,
		DeadlineAt:     now.Add(in.Deadline),
	}, nil
}

// IsTerminal reports whether the turn will never change again.
func (t *Turn) IsTerminal() bool { return t.Status != TurnStatusAccepted }

// TurnGrounding is one disclosed source behind the reply (ADR-231 D4/D5).
type TurnGrounding struct {
	AtomID     string `json:"atom_id"`
	RevisionID string `json:"revision_id,omitempty"`
	Title      string `json:"title,omitempty"`
	Snippet    string `json:"snippet,omitempty"`
}

// TurnToolCall is one tool the agent used (name + learner-safe summary).
type TurnToolCall struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
}

// TurnResult is the decoded wire result (companion_turn.completed.v1 or
// dose_recommendation.completed.v1; the lane decides which fields are
// meaningful). Raw keeps the body as received for replay.
type TurnResult struct {
	WireStatus         string
	ErrorCode          string
	WorkflowID         string
	GeneratedByModelID string

	// companion_turn lane
	ReplyText     string
	Grounding     []TurnGrounding
	ToolCalls     []TurnToolCall
	RefusalReason string

	// PromptVersion + PromptSource are the ADR-197 prompt provenance the AGENT
	// stamps on its completion envelope. Both were dark until 2026-09-02: the
	// kennel lane dropped them when it built companion_turn.completed.v1 and
	// this struct had no field for them, so the ritual step stamp read a key
	// nothing sent, from CHO-2368 until N9.
	//
	// The SOURCE is worth as much as the version. "registry" means a resolved
	// override shaped the prompt; "embedded_fallback" means the built-in
	// default did because no override resolved. A version without its source
	// cannot tell an operator whether their override was actually in effect.
	//
	// Empty is the honest value for a kennel image that predates the
	// forwarding. Never fabricated: an invented version is a false provenance
	// claim and is indistinguishable from a real one.
	PromptVersion string
	PromptSource  string
	// PromptHash is the SHA-256 of the instruction the AGENT actually composed
	// and ran, taken at the compose site by promptstamping.WithStamping and
	// carried here. Consumption deliberately has NO code that can produce one:
	// it knows the prompt version it injected, never the prompt the agent
	// composed, so a locally computed hash would be a true hash of the wrong
	// string and indistinguishable from a real one.
	PromptHash string

	// dose_recommendation lane
	RecommendedAtomIDs []string
	Rationale          string

	CompletedAt time.Time
	Raw         json.RawMessage
}

// Complete applies a wire result exactly once. A second result for a terminal
// turn is refused with ErrTurnAlreadyTerminal (the store records it as late and
// ACKs; the status never flips back). An unknown wire status is
// ErrTurnBadResult and leaves the turn untouched.
func (t *Turn) Complete(res TurnResult) error {
	if t.IsTerminal() {
		return fmt.Errorf("%w: turn %s is %s", ErrTurnAlreadyTerminal, t.TurnID, t.Status)
	}
	var next TurnStatus
	switch strings.ToUpper(strings.TrimSpace(res.WireStatus)) {
	case TurnWireStatusOK:
		next = TurnStatusCompleted
	case TurnWireStatusFailed:
		next = TurnStatusFailed
	case TurnWireStatusRejected:
		next = TurnStatusRejected
	default:
		return fmt.Errorf("%w: unknown wire status %q", ErrTurnBadResult, res.WireStatus)
	}
	done := res.CompletedAt
	if done.IsZero() {
		done = time.Now()
	}
	done = done.UTC()
	t.Status = next
	t.ErrorCode = res.ErrorCode
	t.WorkflowID = res.WorkflowID
	t.GeneratedByModelID = res.GeneratedByModelID
	// Persist the CANONICAL projection of the result (the keys ParseTurnResultJSON
	// reads back), not whatever extras rode the wire: the replay/emit path must
	// never depend on the kennel's body beyond the contract.
	t.Result = storedTurnResultJSON(t.Lane, t.TurnID, next, res, done)
	t.CompletedAt = &done
	// A real result settled this turn. Recorded even for FAILED and REJECTED:
	// the question settled_by answers is "did an answerer speak", not "did it
	// succeed". A reaper never speaks, which is the whole distinction.
	t.SettledBy = TurnSettledByCompletion
	return nil
}

// storedTurnResultJSON renders the canonical result body kept on the turn row
// (readable by ParseTurnResultJSON for the same lane).
func storedTurnResultJSON(lane TurnLane, id string, status TurnStatus, res TurnResult, done time.Time) json.RawMessage {
	wire := strings.ToUpper(strings.TrimSpace(res.WireStatus))
	body := map[string]any{
		"status":                wire,
		"error_code":            res.ErrorCode,
		"workflow_id":           res.WorkflowID,
		"generated_by_model_id": res.GeneratedByModelID,
		"completed_at":          done.UTC().Format(time.RFC3339Nano),
		"stored_status":         string(status),
	}
	if lane == TurnLaneDoseRecommendation {
		body["dose_request_id"] = id
		body["recommended_atom_ids"] = nonNilStrings(res.RecommendedAtomIDs)
		body["rationale"] = res.Rationale
	} else {
		body["turn_id"] = id
		body["reply_text"] = res.ReplyText
		body["refusal_reason"] = res.RefusalReason
		if res.Grounding == nil {
			body["grounding"] = []TurnGrounding{}
		} else {
			body["grounding"] = res.Grounding
		}
		if res.ToolCalls == nil {
			body["tool_calls"] = []TurnToolCall{}
		} else {
			body["tool_calls"] = res.ToolCalls
		}
	}
	bz, err := json.Marshal(body)
	if err != nil {
		// Only a non-marshalable value could land here (none of the fields can
		// be); keep the raw wire body rather than lose the result.
		return append(json.RawMessage(nil), res.Raw...)
	}
	return bz
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// Timeout reaps an accepted turn whose result never arrived. Returns false
// (no-op) when the turn is already terminal.
//
// ⚠ AUDIT-G5 RESIDUE. THE GAP IS NARROWED, NOT CLOSED. Do not read the presence
// of this reaper as "expired turns are handled".
//
// Timeout only runs when SOMEONE IS STILL WAITING: BusTurnEngine and
// BusDoseRecommender call it from their poll loops when they pass DeadlineAt.
// A turn whose waiter has gone away (the HTTP request returned, or the pod
// restarted mid-wait) is never read again, so nothing ever calls this, and the
// row sits `accepted` past its deadline forever.
//
// BOTH observed instances were exactly that case:
//
//	01a02e7d-3166-7d05-89cd-c53e3c616eb7  companion_turn/greeting  deadline 12:01:21Z
//	01a02e86-f3d2-7cdb-865c-2293be6525f2  dose_recommendation/dose deadline 12:10:21Z
//
// both untouched since insert. So the read-time reap would NOT have caught the
// two defects that opened this gap.
//
// Closing it needs a sweep that can see open turns across tenants, and
// companion_turns is FORCE RLS with a fail-closed policy
// (tenant_id = current_setting('chora.tenant_id')), so an unscoped sweeper sees
// ZERO rows and reaps nothing while testing green. Measured: 0 rows visible
// without the lift, 3 with it. That needs an opt-in GUC widening on ADR-192's
// pattern, which is a FOURTH RLS-widening surface and requires its own ADR
// amending the ADR-165/184/192 chain. Approved in principle, not yet built.
func (t *Turn) Timeout(now time.Time) bool {
	if t.IsTerminal() {
		return false
	}
	done := now.UTC()
	t.Status = TurnStatusTimeout
	t.CompletedAt = &done
	// Sign the reap. Without this a timed-out turn carries a terminal status and
	// a completed_at exactly like an answered one, and the audit cannot tell a
	// lane that answered from a lane that never did (AUDIT-G5).
	t.SettledBy = TurnSettledByTimeout
	return true
}

// RecordLateCompletion applies the EVIDENCE of a result that arrived after the
// turn was already terminal. It never flips the status back: the caller was
// answered with a timeout and that history stays true. It returns false when
// there is nothing to record (the turn is still open, or lateness was already
// recorded), so the caller can tell a genuine late arrival from a redelivery.
//
// This exists because the alternative is what the code did before AUDIT-G5:
// swallow ErrTurnAlreadyTerminal, ACK, and discard the answer. A reaped turn
// that WAS eventually answered is a different fact from one that never was, and
// only this records it.
func (t *Turn) RecordLateCompletion(res TurnResult, now time.Time) bool {
	if !t.IsTerminal() || t.LateCompletionAt != nil {
		return false
	}
	at := res.CompletedAt
	if at.IsZero() {
		at = now
	}
	at = at.UTC()
	t.LateCompletionAt = &at
	// Name the answerer. GeneratedByModelID is empty on a reaped turn, so
	// filling it here adds the one fact the timeout could not carry without
	// claiming the turn was completed.
	if res.GeneratedByModelID != "" {
		t.GeneratedByModelID = res.GeneratedByModelID
	}
	return true
}

// CallerErrorCode maps the terminal state to the caller-facing code; empty for
// an accepted or completed turn.
func (t *Turn) CallerErrorCode() string {
	switch t.Status {
	case TurnStatusRejected:
		if strings.EqualFold(strings.TrimSpace(t.ErrorCode), KennelErrCodeCompanionSuspended) {
			return TurnErrCodeSuspended
		}
		return TurnErrCodeRejected
	case TurnStatusFailed:
		return TurnErrCodeFailed
	case TurnStatusTimeout:
		return TurnErrCodeTimeout
	}
	return ""
}

// CallerErrorMessage is the learner-safe message for CallerErrorCode (the
// kennel's error_code is appended for the log trail, never a model string).
func (t *Turn) CallerErrorMessage() string {
	switch t.CallerErrorCode() {
	case TurnErrCodeSuspended:
		return "Your Companion is paused right now; please try again later."
	case TurnErrCodeRejected:
		if t.ErrorCode != "" {
			return "The Companion could not take this turn (" + t.ErrorCode + ")."
		}
		return "The Companion could not take this turn."
	case TurnErrCodeFailed:
		return "The Companion could not finish this turn."
	case TurnErrCodeTimeout:
		return "The Companion did not answer in time."
	}
	return ""
}

// ParsedTurnResult is a decoded result body plus the id it belongs to
// (turn_id on the companion_turn lane, dose_request_id on the dose lane).
type ParsedTurnResult struct {
	ID     string
	Result TurnResult
}

// ParseTurnResultJSON decodes a caller-facing result body (JSON-wire, keys =
// the proto field names of CompanionTurnCompleted / DoseRecommendationCompleted
// in chora-contracts). Fail-loud on malformed JSON, a missing id or a missing
// status; every other field is optional (the kennel omits empty ones).
func ParseTurnResultJSON(lane TurnLane, body []byte) (ParsedTurnResult, error) {
	var w struct {
		TurnID             string          `json:"turn_id"`
		DoseRequestID      string          `json:"dose_request_id"`
		Status             string          `json:"status"`
		ErrorCode          string          `json:"error_code"`
		WorkflowID         string          `json:"workflow_id"`
		GeneratedByModelID string          `json:"generated_by_model_id"`
		ReplyText          string          `json:"reply_text"`
		Grounding          []TurnGrounding `json:"grounding"`
		ToolCalls          []TurnToolCall  `json:"tool_calls"`
		RefusalReason      string          `json:"refusal_reason"`
		PromptVersion      string          `json:"prompt_version"`
		PromptSource       string          `json:"prompt_source"`
		PromptHash         string          `json:"prompt_hash"`
		RecommendedAtomIDs []string        `json:"recommended_atom_ids"`
		Rationale          string          `json:"rationale"`
		CompletedAt        string          `json:"completed_at"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return ParsedTurnResult{}, fmt.Errorf("%w: decode: %v", ErrTurnBadResult, err)
	}
	id := w.TurnID
	if lane == TurnLaneDoseRecommendation {
		id = w.DoseRequestID
	}
	if strings.TrimSpace(id) == "" {
		return ParsedTurnResult{}, fmt.Errorf("%w: result carries no id for lane %s", ErrTurnBadResult, lane)
	}
	if strings.TrimSpace(w.Status) == "" {
		return ParsedTurnResult{}, fmt.Errorf("%w: result %s carries no status", ErrTurnBadResult, id)
	}
	var done time.Time
	if strings.TrimSpace(w.CompletedAt) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, w.CompletedAt)
		if err != nil {
			return ParsedTurnResult{}, fmt.Errorf("%w: completed_at %q: %v", ErrTurnBadResult, w.CompletedAt, err)
		}
		done = parsed
	}
	return ParsedTurnResult{
		ID: id,
		Result: TurnResult{
			WireStatus:         w.Status,
			ErrorCode:          w.ErrorCode,
			WorkflowID:         w.WorkflowID,
			GeneratedByModelID: w.GeneratedByModelID,
			ReplyText:          w.ReplyText,
			Grounding:          w.Grounding,
			PromptVersion:      w.PromptVersion,
			PromptSource:       w.PromptSource,
			PromptHash:         w.PromptHash,
			ToolCalls:          w.ToolCalls,
			RefusalReason:      w.RefusalReason,
			RecommendedAtomIDs: w.RecommendedAtomIDs,
			Rationale:          w.Rationale,
			CompletedAt:        done,
			Raw:                append(json.RawMessage(nil), body...),
		},
	}, nil
}

// TurnStore is the persistence port. Begin writes the turn row AND runs publish
// inside the SAME transaction (the outbox request row), so "accepted" is never
// claimed for a turn that was not durably requested. Complete and Timeout are
// idempotent state transitions that report whether they applied.
type TurnStore interface {
	Begin(ctx context.Context, turn *Turn, publish func(ctx context.Context) error) error
	Get(ctx context.Context, tenantID, turnID string) (*Turn, error)
	Complete(ctx context.Context, tenantID, turnID string, res TurnResult) (applied bool, err error)
	Timeout(ctx context.Context, tenantID, turnID string, now time.Time) (applied bool, err error)
}
