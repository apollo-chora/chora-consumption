// Package goal is the learner-owned Goal aggregate in chora-consumption
// (ADR-204 §2, which amends ADR-203). It is the greenfield foundation that makes
// the curious-first loop's destination LEARNER-owned rather than bound inside a
// Companion — so the whole BASE loop (goal + dose + KG + LearnerProfile) runs with
// ZERO Companions (ADR-204 Constraint-1). A Companion OPTIONALLY attaches 1:1 to a
// Goal; the bond persists on the Goal (today UI-only).
//
// SCOPE (this slice): the aggregate + its invariants only. DEFERRED and NOT in
// this package: the graduation state machine (achieved→maintenance + tier-S EXP),
// goal.* Pub/Sub events / outbox, dose / forgetting-curve scoping (ADR-202), and
// SplitCluster's Goal-follows-fragment logic (ADR-204 §9). `SetStatus` here is a
// validated setter, NOT the graduation machine.
//
// HEXAGONAL purity: stdlib-only, NO infra imports (mirrors learner_profile +
// learner_weakness). No time.Now() leak — every clock-dependent call takes a
// `now` (the constructor falls back to wall-clock only when `Now` is the zero
// value). Per ddd-enforcement: closure is soft-delete (#5, never hard delete);
// new rows use UUIDv7 (#7); gcid + the chora_target_ref / attached_companion_id
// references are opaque UUIDs/refs without FK constraints (#3, cross-aggregate).
package goal

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Kind enumerates what a Goal aims at (ADR-204 §2 / ADR-203 §3, amended by
// ADR-214 §3). The self-completing CREDENTIAL kinds (cert/course/path) are
// REMOVED: a learner's relationship to an operator credential is now the ADR-216
// aspiration link, NEVER a personal goal that graduates from personal KG-mastery
// (that was the credential-forgery hole ADR-214 closes — a personal `cert` goal
// with a free-form target could light up a credential "Arrival" from personal
// work). The remaining kinds are all personal / Discovery-axis (ADR-213).
type Kind string

const (
	// KindCuriosity is an OPEN goal with no verifiable target (target nil-ok);
	// the default learner-sovereign personal goal (ADR-214 §2).
	KindCuriosity Kind = "curiosity"
	// KindThemeMastery / KindEdge reference a verifiable theme / Growth-Edge and
	// require a chora_target_ref. Personal-axis (NOT credential — no lens flip).
	KindThemeMastery Kind = "theme_mastery"
	KindEdge         Kind = "edge"
)

// Valid reports whether k is a recognised goal kind. cert/course/path are no
// longer valid (ADR-214 §3) — creating one fails-loud with ErrInvalid.
func (k Kind) Valid() bool {
	switch k {
	case KindCuriosity, KindThemeMastery, KindEdge:
		return true
	}
	return false
}

// RequiresTarget reports whether k needs a chora_target_ref. The open
// `curiosity` goal is target-optional (its anchor is the root ConceptNode,
// ADR-214 §5); theme_mastery/edge reference a verifiable Chora object.
func (k Kind) RequiresTarget() bool {
	return k.Valid() && k != KindCuriosity
}

// Status is the Goal lifecycle state (ADR-204 §2). The transitions between them
// (the graduation state machine) are DEFERRED; this slice validates the value.
type Status string

const (
	StatusActive      Status = "active"
	StatusAchieved    Status = "achieved"
	StatusMaintenance Status = "maintenance"
	StatusRetired     Status = "retired"
)

// Valid reports whether s is a recognised status.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusAchieved, StatusMaintenance, StatusRetired:
		return true
	}
	return false
}

// Sentinel errors. Distinct so the HTTP adapter can map each to a precise status
// (validation → 400/422; bond conflicts → 409; mutation on a closed goal → 409).
var (
	ErrInvalid         = errors.New("goal: invalid")
	ErrAlreadyAttached = errors.New("goal: a Companion is already attached (detach first; 1:1 bond)")
	ErrNotAttached     = errors.New("goal: no Companion is attached")
	ErrDeleted         = errors.New("goal: goal is soft-deleted")
)

// Goal is the learner-owned aggregate root. ChoraTargetRef + AttachedCompanionID
// are nullable (curiosity has no target; the Companion bond is optional 1:1).
type Goal struct {
	GoalID         string
	TenantID       string
	LearnerGCID    string
	Kind           Kind
	ChoraTargetRef *string  // verifiable Chora object; nil for an open curiosity goal
	ConceptSet     []string // derived concepts the goal scopes (normalised)
	// MasteredConceptCount is the last-persisted count of ConceptSet members the
	// learner has mastered (grown Growth Edges). It is NOT the read-time progress
	// (that is derived live in goals_handler.go, never trusted from here) — it is
	// the goal-graduation subscriber's high-water mark, compared against a freshly
	// derived count so a redelivered weakness.grown does not re-emit a progress
	// event. Defaults to 0 (a fresh goal has mastered nothing). ADR-204 §9 / CHO-1962.
	MasteredConceptCount int
	Status               Status
	NorthStarNote        string  // free-text, motivational, non-EXP (ADR-203 #3)
	AttachedCompanionID  *string // the persisted 1:1 bond; nil when no Companion attached
	// RootConceptID anchors the Goal to its evolving root ConceptNode (ADR-214 §1
	// — the goal ≡ the map's root concept; goal-evolution = re-rooting). Nullable
	// (a legacy/plain goal may have none yet). Opaque cross-aggregate ref, no FK (#3).
	RootConceptID *string
	// PersonalCompletedAt is the learner-DEFINED personal-axis completion (ADR-213
	// §1): the learner decides when the goal is "done for me". It is ORTHOGONAL to
	// the verified Status/Graduate credentialed axis — the impermeable wall (ADR-213
	// §2/§3): personal completion NEVER sets a verified status or verified EXP. Nil
	// until the learner marks it.
	PersonalCompletedAt *time.Time
	// FocusConceptID is the goal's single campaign focus node (ADR-227 D11):
	// where the bound Companion marches. Moved freely by the learner, nil when
	// unassigned (the Companion may PROPOSE the next frontier node, never
	// assign). Opaque cross-aggregate ref, no FK (#3). WS-C1 reconciles moves
	// with companion_instances.resonant_concept_id (addendum #5).
	FocusConceptID *string
	// CampaignSealedAt is the campaign seal history (ADR-227 D3): the latest
	// moment the learner sealed a verified frontier-clear. DISTINCT from
	// PersonalCompletedAt — the bare ADR-213 declaration never touches seal
	// state, and re-seal gating (>= N new wins + weekly cap) reads THIS
	// timestamp. Nil until first sealed; never cleared (seal history is fact).
	CampaignSealedAt *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// NewGoalInput is the constructor input.
type NewGoalInput struct {
	TenantID       string
	LearnerGCID    string
	Kind           Kind
	ChoraTargetRef *string
	ConceptSet     []string
	NorthStarNote  string
	RootConceptID  *string // ADR-214 §1 anchor; trimmed, blank→nil
	Now            time.Time
}

// NewGoal constructs a learner-owned Goal, validating the kind and the
// kind→target rule (curiosity ⇒ target nil-ok; every other kind ⇒ target
// required). A blank/whitespace target pointer is normalised to nil first, so a
// credential kind still fails-loud rather than persisting an empty "" target.
func NewGoal(in NewGoalInput) (*Goal, error) {
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	learner := strings.TrimSpace(in.LearnerGCID)
	if learner == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalid, in.Kind)
	}

	target := normaliseTarget(in.ChoraTargetRef)
	if in.Kind.RequiresTarget() && target == nil {
		return nil, fmt.Errorf("%w: kind %q requires a chora_target_ref", ErrInvalid, in.Kind)
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	return &Goal{
		GoalID:         domain.NewUUIDv7(),
		TenantID:       tenant,
		LearnerGCID:    learner,
		Kind:           in.Kind,
		ChoraTargetRef: target,
		RootConceptID:  normaliseTarget(in.RootConceptID),
		ConceptSet:     normaliseConcepts(in.ConceptSet),
		Status:         StatusActive,
		NorthStarNote:  strings.TrimSpace(in.NorthStarNote),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// AttachCompanion binds a Companion to the Goal, enforcing the 1:1 invariant:
// re-attaching the SAME Companion is idempotent; attaching a DIFFERENT Companion
// while one is already bound fails (the caller must DetachCompanion first).
func (g *Goal) AttachCompanion(companionID string, now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	fid := strings.TrimSpace(companionID)
	if fid == "" {
		return fmt.Errorf("%w: companion_id required", ErrInvalid)
	}
	if g.AttachedCompanionID != nil {
		if *g.AttachedCompanionID == fid {
			return nil // idempotent re-attach
		}
		return ErrAlreadyAttached
	}
	g.AttachedCompanionID = &fid
	g.UpdatedAt = now.UTC()
	return nil
}

// DetachCompanion clears the bond. Detaching with no Companion attached fails-loud.
func (g *Goal) DetachCompanion(now time.Time) error {
	if g.AttachedCompanionID == nil {
		return ErrNotAttached
	}
	g.AttachedCompanionID = nil
	g.UpdatedAt = now.UTC()
	return nil
}

// SetStatus validates + sets the lifecycle status. NOTE: this is a guarded
// setter, not the graduation transition — `Graduate` owns active→achieved
// (ADR-204 §9); achieved→maintenance + tier-S EXP remain deferred.
func (g *Goal) SetStatus(s Status, now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	if !s.Valid() {
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, s)
	}
	g.Status = s
	g.UpdatedAt = now.UTC()
	return nil
}

// Graduate transitions an ACTIVE goal to achieved — the verified-mastery
// graduation (ADR-204 §9, the active→achieved slice only; the achieved→
// maintenance handoff + tier-S EXP remain deferred). Idempotent: re-graduating
// an already-achieved goal is a no-op (returns nil, NO UpdatedAt bump) so a
// redelivered weakness.grown event never double-fires. Guarded: graduating from
// maintenance/retired fails-loud (ErrInvalid); a soft-deleted goal returns
// ErrDeleted. Clock-free — the caller supplies `now`.
func (g *Goal) Graduate(now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	if g.Status == StatusAchieved {
		return nil // idempotent — already graduated, no state change
	}
	if g.Status != StatusActive {
		return fmt.Errorf("%w: cannot graduate from %q (only active→achieved)", ErrInvalid, g.Status)
	}
	g.Status = StatusAchieved
	g.UpdatedAt = now.UTC()
	return nil
}

// RecordMastery sets the mastered-concept high-water mark (the goal-graduation
// subscriber's idempotency marker, CHO-1962) and bumps updated_at. NOT the
// read-time progress (that is derived live, never trusted from here). Guarded:
// a soft-deleted goal returns ErrDeleted; a negative count is clamped to 0.
// Clock-free — the caller supplies `now`. Separate from Graduate: a goal can
// move its count many times before (and at) graduation.
func (g *Goal) RecordMastery(count int, now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	if count < 0 {
		count = 0
	}
	g.MasteredConceptCount = count
	g.UpdatedAt = now.UTC()
	return nil
}

// SoftDelete dismisses the goal without removing it (hard delete forbidden,
// ddd-enforcement #5). Default queries filter deleted_at IS NULL.
func (g *Goal) SoftDelete(now time.Time) {
	t := now.UTC()
	g.DeletedAt = &t
	g.UpdatedAt = t
}

// AnchorToConcept sets the Goal's evolving root ConceptNode (ADR-214 §1). The id
// is a learner-owned ConceptNode UUID (opaque cross-aggregate ref, no FK, #3).
// Guarded: a soft-deleted goal fails-loud. Clock-free.
func (g *Goal) AnchorToConcept(conceptID string, now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	id := strings.TrimSpace(conceptID)
	if id == "" {
		return fmt.Errorf("%w: root_concept_id required", ErrInvalid)
	}
	g.RootConceptID = &id
	g.UpdatedAt = now.UTC()
	return nil
}

// MarkPersonalComplete records the learner's PERSONAL-axis completion (ADR-213
// §1) — "done for me". This is the learner-sovereign personal axis: it sets
// PersonalCompletedAt + bumps updated_at, but NEVER touches Status, Graduate, or
// any verified/credentialed value — the impermeable wall (ADR-213 §2/§3: no
// personal action may forge a credentialed achievement; personal completion
// confers no verified EXP or credential). Idempotent: re-marking is a no-op
// (keeps the first completion time, no updated_at bump). Guarded on soft-delete.
func (g *Goal) MarkPersonalComplete(now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	if g.PersonalCompletedAt != nil {
		return nil // idempotent — already personally complete
	}
	t := now.UTC()
	g.PersonalCompletedAt = &t
	g.UpdatedAt = t
	return nil
}

// SetFocusConcept assigns, moves or clears (nil) the goal's campaign focus
// node (ADR-227 D11). Learner-sovereign: the Companion proposes, the learner
// assigns. The id is a learner-owned ConceptNode UUID (opaque cross-aggregate
// ref, no FK, #3); subtree membership is validated by the caller against the
// live graph. Guarded on soft-delete.
func (g *Goal) SetFocusConcept(conceptID *string, now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	if conceptID == nil {
		g.FocusConceptID = nil
		g.UpdatedAt = now.UTC()
		return nil
	}
	id := strings.TrimSpace(*conceptID)
	if id == "" {
		return fmt.Errorf("%w: focus_concept_id must be a concept id or null", ErrInvalid)
	}
	g.FocusConceptID = &id
	g.UpdatedAt = now.UTC()
	return nil
}

// SealCampaign records a campaign seal (ADR-227 D3) — called ONLY after
// campaign.EvaluateSeal verified the frontier is empty (+ re-seal gates).
// Moves CampaignSealedAt to now (re-seals move it again — seal history is the
// re-seal gate's clock) and closes the ADR-213 personal axis if still open,
// PRESERVING an earlier bare declaration (the wall stands: the personal axis
// stays learner-defined; the seal's verified value rides goal_sealed.v1, not
// this flag). Guarded on soft-delete.
func (g *Goal) SealCampaign(now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	t := now.UTC()
	g.CampaignSealedAt = &t
	if g.PersonalCompletedAt == nil {
		g.PersonalCompletedAt = &t
	}
	g.UpdatedAt = t
	return nil
}

// ReopenPersonal clears the learner's personal-axis completion (ADR-213 §1 — the
// learner may decide a goal is not "done for me" after all). Orthogonal to the
// verified axis; idempotent when already open; guarded on soft-delete.
func (g *Goal) ReopenPersonal(now time.Time) error {
	if g.DeletedAt != nil {
		return ErrDeleted
	}
	if g.PersonalCompletedAt == nil {
		return nil // idempotent — already open
	}
	g.PersonalCompletedAt = nil
	g.UpdatedAt = now.UTC()
	return nil
}

// normaliseTarget trims a target pointer and folds blank/whitespace to nil.
func normaliseTarget(ref *string) *string {
	if ref == nil {
		return nil
	}
	t := strings.TrimSpace(*ref)
	if t == "" {
		return nil
	}
	return &t
}

// normaliseConcepts trims, drops empties, and de-duplicates while preserving
// first-seen order. Always returns a non-nil slice (empty, not nil) so the wire
// shape is stable.
func normaliseConcepts(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}
