// suspension.go: the consumption side of the ADR-252 Learning Companion
// containment control (ADR-254 D11, ADR-252 D1 / Q5).
//
// chora-observability OWNS the two suspension tables and the operator write
// path; chora-model-gateway READS them uncached on every companion turn and is
// the CONTROL (deny-before-debit, fail-closed, ADR-252 D1 / D6). Consumption
// cannot read those tables (they live in chora_observability; cross-DB is
// forbidden) and the operator GET route is PLATFORM_OPERATOR-only, so the
// learner-safe read here is a PROJECTION of the audit event
// chora.governance.audit.companion_suspension_changed.v1 into a small local
// table, ADVISORY and cacheable, used to:
//
//   - refuse a chat turn with 403 COMPANION_SUSPENDED before any mana
//     pre-check, session work or SSE headers (an honest refusal instead of a
//     raw gateway error);
//   - skip the ADR-235 reflection CLAIM and publish while paused (ADR-252 Q5:
//     the skip must happen BEFORE the claim, never as a claim failure);
//   - render `companion_status {paused, reason, scope}` on the companion
//     profile.
//
// ⚠ This read is NOT the enforcement point. A stale or missing projection
// row means the learner sees a gateway refusal instead of the friendlier
// local one; it never means a contained turn runs. Callers treat a Status()
// error as "unknown" (log loud, let the gateway decide), not as "not paused"
// and not as a second control.
//
// Event mirror (exactly what observability emits, see
// services/chora-observability/internal/domain/companionsuspension and
// chora-contracts/proto/events/governance/audit.proto CompanionSuspensionChanged):
//
//	suspension_id  UUIDv7 of the observability row (the aggregate identity)
//	scope          "platform" | "tenant"
//	tenant_id      the contained tenant for scope tenant; EMPTY for platform
//	skill_key      the mana action code the containment names; EMPTY = every
//	               companion turn in the scope (ADR-254 D4's action_codes[] IS
//	               this single value)
//	engaged        true = engaged (contained), false = released (lifted)
//	version        per-row monotonic: engage = 1, release = 2
//	reason         mandatory human reason for THIS change
//	actor_gcid     who made the change
//	changed_at     server time of the change
//
// A LIFT is therefore the same row arriving with engaged=false and a higher
// version, never a delete and never an empty action set.
package companion

import (
	"context"
	"errors"
	"strings"
	"time"
)

// SuspensionScope is where a suspension applies.
type SuspensionScope string

const (
	// SuspensionScopePlatform contains every tenant (PLATFORM_OPERATOR only).
	SuspensionScopePlatform SuspensionScope = "platform"
	// SuspensionScopeTenant contains one tenant.
	SuspensionScopeTenant SuspensionScope = "tenant"
)

// Valid reports whether s is a known scope.
func (s SuspensionScope) Valid() bool {
	return s == SuspensionScopePlatform || s == SuspensionScopeTenant
}

// ErrCodeCompanionSuspended is the wire error code a handler returns (HTTP
// 403) when the advisory projection says the turn is paused.
const ErrCodeCompanionSuspended = "COMPANION_SUSPENDED"

// Validation sentinels for an inbound SuspensionChanged. A failure is
// FAIL-LOUD: the subscriber NACKs, the broker retries, and the message
// dead-letters, because a malformed containment event is a producer bug and
// silently dropping it would diverge this projection from the operator's
// decision.
var (
	ErrSuspensionEventIDRequired   = errors.New("companion suspension: event_id is required")
	ErrSuspensionIDRequired        = errors.New("companion suspension: suspension_id is required")
	ErrSuspensionInvalidScope      = errors.New("companion suspension: scope must be platform or tenant")
	ErrSuspensionTenantRequired    = errors.New("companion suspension: tenant_id is required for tenant scope")
	ErrSuspensionTenantOnPlatform  = errors.New("companion suspension: platform scope carries no tenant_id")
	ErrSuspensionReasonRequired    = errors.New("companion suspension: reason is required")
	ErrSuspensionActorRequired     = errors.New("companion suspension: actor_gcid is required")
	ErrSuspensionVersionInvalid    = errors.New("companion suspension: version must be >= 1")
	ErrSuspensionChangedAtRequired = errors.New("companion suspension: changed_at is required")
)

// SuspensionStatus is the advisory answer for one (tenant, action code).
type SuspensionStatus struct {
	// Paused is true when an engaged suspension covers the turn.
	Paused bool
	// Reason is the operator's reason for the covering suspension (empty when
	// not paused).
	Reason string
	// Scope is the covering suspension's scope (empty when not paused).
	Scope SuspensionScope
}

// SuspensionAdvisor is the read port the chat handler, the ADR-235 reflection
// read and the companion profile consult. Semantics: paused when a platform
// suspension is engaged and covers actionCode, or a tenant suspension for
// tenantID is; a release clears it. actionCode "" asks the whole-companion
// question (only all-skills suspensions count). A non-nil error means
// UNKNOWN; callers must not coerce it to "not paused" AND must not turn it
// into a refusal (the gateway is the control).
type SuspensionAdvisor interface {
	Status(ctx context.Context, tenantID, actionCode string) (SuspensionStatus, error)
}

// SuspensionApplier is the write port the projector uses. Apply reports
// whether the event was APPLIED (true) or discarded as STALE / duplicate
// (false). A stale event is NOT an error: the subscriber ACKs it, never NACKs
// it into a poison loop that would dead-letter a valid, merely late,
// redelivery.
type SuspensionApplier interface {
	Apply(ctx context.Context, ev SuspensionChanged) (applied bool, err error)
}

// SuspensionProjection is the full port over the local projection table.
type SuspensionProjection interface {
	SuspensionApplier
	SuspensionAdvisor
}

// SuspensionChanged is the domain view of one
// chora.governance.audit.companion_suspension_changed.v1 event.
type SuspensionChanged struct {
	EventID      string
	SuspensionID string
	Scope        SuspensionScope
	TenantID     string
	SkillKey     string
	Engaged      bool
	Version      int64
	Reason       string
	ActorGCID    string
	ChangedAt    time.Time
	// OccurredAt is the envelope occurred_at (informational; the ordering
	// guard is Version then ChangedAt).
	OccurredAt time.Time
}

// Validate rejects an event the projection must not apply.
func (e SuspensionChanged) Validate() error {
	if strings.TrimSpace(e.EventID) == "" {
		return ErrSuspensionEventIDRequired
	}
	if strings.TrimSpace(e.SuspensionID) == "" {
		return ErrSuspensionIDRequired
	}
	if !e.Scope.Valid() {
		return ErrSuspensionInvalidScope
	}
	if e.Scope == SuspensionScopeTenant && strings.TrimSpace(e.TenantID) == "" {
		return ErrSuspensionTenantRequired
	}
	if e.Scope == SuspensionScopePlatform && strings.TrimSpace(e.TenantID) != "" {
		return ErrSuspensionTenantOnPlatform
	}
	if strings.TrimSpace(e.Reason) == "" {
		return ErrSuspensionReasonRequired
	}
	if strings.TrimSpace(e.ActorGCID) == "" {
		return ErrSuspensionActorRequired
	}
	if e.Version < 1 {
		return ErrSuspensionVersionInvalid
	}
	if e.ChangedAt.IsZero() {
		return ErrSuspensionChangedAtRequired
	}
	return nil
}

// Record projects the event onto the row shape the projections store.
func (e SuspensionChanged) Record() SuspensionRecord {
	return SuspensionRecord{
		SuspensionID:  e.SuspensionID,
		Scope:         e.Scope,
		TenantID:      e.TenantID,
		SkillKey:      e.SkillKey,
		Engaged:       e.Engaged,
		Reason:        e.Reason,
		ActorGCID:     e.ActorGCID,
		SourceVersion: e.Version,
		ChangedAt:     e.ChangedAt,
		LastEventID:   e.EventID,
	}
}

// SuspensionRecord is one projected suspension row: the latest state this
// service has seen for one observability suspension id.
type SuspensionRecord struct {
	SuspensionID string
	Scope        SuspensionScope
	TenantID     string // empty for platform scope
	SkillKey     string // empty = every companion turn in the scope
	Engaged      bool
	Reason       string
	ActorGCID    string
	// SourceVersion is the emitter's per-row monotonic version; the
	// projection applies an event only when it Supersedes what it holds.
	SourceVersion int64
	ChangedAt     time.Time
	// LastEventID is the event that produced this state (traceability).
	LastEventID string
}

// Supersedes reports whether r is strictly newer than existing for the same
// suspension id: a higher version wins; an equal version yields only to a
// later changed_at (the emitter never re-emits a version with a different
// time, so this arm is a guard, not a path); anything else is stale or a
// duplicate. Pub/Sub is at-least-once and not order-preserving, so without
// this a late "engaged" could overwrite a newer "released" and pause a
// companion the operator has already lifted, or the reverse.
func (r SuspensionRecord) Supersedes(existing SuspensionRecord) bool {
	if r.SourceVersion != existing.SourceVersion {
		return r.SourceVersion > existing.SourceVersion
	}
	return r.ChangedAt.After(existing.ChangedAt)
}

// covers reports whether the engaged record contains the (tenant, action).
func (r SuspensionRecord) covers(tenantID, actionCode string) bool {
	if !r.Engaged {
		return false
	}
	switch r.Scope {
	case SuspensionScopePlatform:
		// every tenant
	case SuspensionScopeTenant:
		if tenantID == "" || r.TenantID != tenantID {
			return false
		}
	default:
		return false
	}
	if r.SkillKey == "" {
		return true // every companion turn in the scope
	}
	return actionCode != "" && r.SkillKey == actionCode
}

// rank orders covering records: platform before tenant (the wider
// containment is the honest one to report), all-skills before a named skill,
// then newest first.
func (r SuspensionRecord) outranks(o SuspensionRecord) bool {
	if r.Scope != o.Scope {
		return r.Scope == SuspensionScopePlatform
	}
	if (r.SkillKey == "") != (o.SkillKey == "") {
		return r.SkillKey == ""
	}
	return r.ChangedAt.After(o.ChangedAt)
}

// DecideSuspension is the pure advisory decision over the engaged records
// visible to the caller: paused when any engaged record covers (tenantID,
// actionCode). Released records (Engaged=false) never pause. When several
// cover the turn, the reported reason/scope come from the highest-ranked one
// (platform > tenant, all-skills > named skill, newest first).
func DecideSuspension(records []SuspensionRecord, tenantID, actionCode string) SuspensionStatus {
	var best *SuspensionRecord
	for i := range records {
		r := &records[i]
		if !r.covers(tenantID, actionCode) {
			continue
		}
		if best == nil || r.outranks(*best) {
			best = r
		}
	}
	if best == nil {
		return SuspensionStatus{}
	}
	return SuspensionStatus{Paused: true, Reason: best.Reason, Scope: best.Scope}
}
