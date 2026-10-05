// ritual.go — the Grimoire Rituals v1 DESIGNER aggregate (CHO-2016 P4,
// ADR-219 D3 + docs/FAMILIAR-SKILL-SPECS-2026-07-03.md §5–7). A CompanionRitual
// is a learner-composed, deterministic pipeline of 1..5 (⇢8 with long_weaving)
// equipped-ACTIVE Skill invocations landing in exactly ONE closed sink,
// unlocked at st4 (structural). Revisions are APPEND-ONLY (AtomRevision
// discipline); the publish price is composed-flat and frozen at publish.
//
// Pure domain: no LLM, no infra imports. The growth stage arrives as a plain
// int in RitualCapabilityContext (the loadout.go slotCap idiom) so the package
// never imports growth — avoiding an import cycle (growth's *_test.go imports
// companion).
package companion

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// RitualUnlockStage is the growth stage at which Rituals unlock — st4
// (structural). Mirrors growth.StageStructural (kept as a literal to avoid the
// growth import; growth/curve.go is the source of truth).
const RitualUnlockStage = 4

// Grammar limits (ADR-219 D3 + spec §3 craft Skills).
const (
	RitualNameMaxLen         = 60
	RitualStepCapBase        = 5 // innate at st4
	RitualStepCapLongWeaving = 8 // craft long_weaving lifts 5→8
	RitualEnabledQuotaBase   = 2 // ⚙ base enabled-Rituals quota
	RitualEnabledQuotaTwin   = 4 // craft twin_rituals lifts 2→4
)

// Composed-flat publish pricing (spec §7): base + per-premium-step uplift,
// frozen at publish, then flat per run.
const (
	RitualBasePriceUnits        = 20
	RitualGenerativeUpliftUnits = 15
	RitualEgressUpliftUnits     = 40
)

// Ritual run mana action code + event topics (envelope-compliant, outbox).
const (
	ActionCodeRitualRun              = "companion_ritual_run"
	TopicCompanionRitualPublished    = "chora.consumption.companion.ritual_published.v1"
	TopicCompanionRitualRunCompleted = "chora.consumption.companion.ritual_run_completed.v1"
)

// RitualTrigger enumerates the ways a Ritual fires. manual/on_map_open/
// on_dose_completed are v1-runnable; schedule/on_event are schema-carried so
// P6 autonomy adds no breaking change, but refuse to publish/run in v1.
type RitualTrigger string

const (
	TriggerManual          RitualTrigger = "manual"
	TriggerOnMapOpen       RitualTrigger = "on_map_open"
	TriggerOnDoseCompleted RitualTrigger = "on_dose_completed"
	TriggerSchedule        RitualTrigger = "schedule" // P6 (autonomy)
	TriggerOnEvent         RitualTrigger = "on_event" // P6 (autonomy)
)

// Sentinel errors (mapped to 4xx at the HTTP edge in G5).
var (
	ErrRitualNameRequired         = errors.New("companion.ritual: name required")
	ErrRitualNameTooLong          = errors.New("companion.ritual: name too long (max 60)")
	ErrRitualUnknownTrigger       = errors.New("companion.ritual: unknown trigger")
	ErrRitualTriggerNotV1         = errors.New("companion.ritual: trigger not runnable in v1 (needs P6 autonomy)")
	ErrRitualUnknownSink          = errors.New("companion.ritual: sink not in the closed list")
	ErrRitualNoSteps              = errors.New("companion.ritual: at least one step required")
	ErrRitualTooManySteps         = errors.New("companion.ritual: step count exceeds the cap")
	ErrRitualStepSkillNotEquipped = errors.New("companion.ritual: step Skill is not equipped-active on this companion")
	ErrRitualStepSkillInactive    = errors.New("companion.ritual: step Skill is not active in the catalogue")
	ErrRitualEnabledQuotaReached  = errors.New("companion.ritual: enabled-Rituals quota reached")
	ErrRitualUnlockStage          = errors.New("companion.ritual: Rituals unlock at st4 (structural)")
	ErrRitualNoPublishedRevision  = errors.New("companion.ritual: no published revision")

	// ADR-257 reserved-shape sentinels (mapped to 4xx at the HTTP edge).
	ErrRitualStepKindUnknown = errors.New("companion.ritual: unknown step kind")
	ErrRitualStepKindNotV1   = errors.New("companion.ritual: step kind reserved, not runnable in v1")
	// ErrRitualStepFieldReserved fires when a caller sets an ADR-257 field the
	// runner does not yet honour. Refusing is deliberate: accepting it would
	// persist a claim about the ritual's behaviour that is not true.
	ErrRitualStepFieldReserved = errors.New("companion.ritual: reserved step field set but not yet honoured")
)

// RitualStepKind discriminates what a step IS (ADR-257 D2 / a.3). `skill` is
// the only kind v1 runs; `roster_peer` and `sink` are RESERVED enum values that
// refuse, mirroring the schedule/on_event trigger idiom above. An ABSENT kind
// means `skill`, which is what keeps every pre-ADR-257 revision valid.
type RitualStepKind string

const (
	// StepKindSkill — one equipped-active catalogue Skill invocation.
	StepKindSkill RitualStepKind = "skill"
	// StepKindRosterPeer — RESERVED: the same turn executed against another
	// companion from the learner's OWN roster (ADR-257 D6). Needs the peer
	// executor AND the ADR-254 D1 exception ruling (ADR-257 §8) first.
	StepKindRosterPeer RitualStepKind = "roster_peer"
	// StepKindSink — RESERVED: a terminal write step. Owner ruling R14 holds
	// rituals at ONE sink per run until a separate ADR rules on partial writes,
	// because the atomic sink-write-on-completion is what makes a failed run
	// write nothing.
	StepKindSink RitualStepKind = "sink"
)

// RitualStep is one equipped-ACTIVE Skill invocation with bounded params, plus
// the ADR-257 inter-step contract fields, RESERVED: carried and refused, never
// accepted-and-ignored (a stored from_step_id the runner does not read is a lie
// about what the ritual does).
//
// ⚠ WIRE SHAPE IS PINNED. The pg repo persists these with a plain
// json.Marshal of []RitualStep into companion_ritual_revisions.steps
// (companion_ritual_repo.go:162), so the stored keys are the Go FIELD NAMES:
// {"SkillKey", "Params"}. The 0071 migration comment says "[{skill_key,
// params}]" and is WRONG about the live bytes; 0071 is applied and frozen, so
// it is not re-authored to match. The tags below pin the two original keys at
// their live spelling deliberately: encoding/json folds case but not
// underscores, so retagging SkillKey to `skill_key` would read every stored
// revision back with an EMPTY SkillKey, which ValidateSteps then rejects as
// not-equipped, i.e. every published ritual silently becomes unrunnable.
// ritual_reserved_fields_test.go pins both halves of that.
//
// The reserved fields are `omitempty` so a step that sets none of them marshals
// to exactly the pre-ADR-257 bytes.
type RitualStep struct {
	SkillKey string         `json:"SkillKey"`
	Params   map[string]any `json:"Params,omitempty"`

	// StepID is the step's stable identity (UUIDv7), so a connector, a stamp
	// reference or a run-story line can name a step that survives reordering.
	// Absent on a legacy revision; readers derive it positionally there.
	StepID string `json:"step_id,omitempty"`
	// StepKind is the RitualStepKind discriminator; absent means skill.
	StepKind RitualStepKind `json:"step_kind,omitempty"`
	// FromStepID names the EARLIER step whose typed output feeds this one.
	// RESERVED: refused until chaining ships behind the ADR-257 §5 ToolFilter
	// enforcement gate.
	FromStepID string `json:"from_step_id,omitempty"`
	// Actor names the companion that executes this step; absent means "this
	// companion". RESERVED for StepKindRosterPeer.
	Actor string `json:"actor,omitempty"`
	// CapabilityScope is where a future CompanionExposureGrant scope binds
	// (mirrors agentcard.Capability.ConsentScopeRequired). RESERVED.
	CapabilityScope string `json:"capability_scope,omitempty"`
}

// RitualRevision is an append-only snapshot of the step pipeline. Never
// mutated in place (AtomRevision discipline; the pg table has no UPDATE/DELETE).
type RitualRevision struct {
	RevisionNo   int
	Steps        []RitualStep
	ArmorVerdict string
	CreatedAt    time.Time
}

// Ritual is the designer aggregate root (companion_rituals row).
type Ritual struct {
	RitualID            string
	TenantID            string
	CompanionID         string
	Name                string
	Trigger             RitualTrigger
	TriggerConfig       map[string]any
	Sink                string
	Enabled             bool
	PublishedPriceUnits int
	CurrentRevision     int
	Revisions           []RitualRevision
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

// RitualCapabilityContext carries the companion's earned capability state at
// the moment of a designer/run action. Stage + equipped set + craft flags are
// resolved by the caller (from growth + Loadout) — the domain stays growth-free.
type RitualCapabilityContext struct {
	Stage                int
	EquippedActiveSkills map[string]bool
	HasLongWeaving       bool
	HasTwinRituals       bool
	HasWeaveMastery      bool // v2 branch grammar (unused in v1 validation)
	Catalogue            map[string]CatalogEntry
	OtherEnabledCount    int // count of the companion's OTHER enabled rituals
}

// StepCap is the effective step ceiling: 5, or 8 with the long_weaving craft.
func (c RitualCapabilityContext) StepCap() int {
	if c.HasLongWeaving {
		return RitualStepCapLongWeaving
	}
	return RitualStepCapBase
}

// EnabledQuota is the effective concurrent-enabled ceiling: 2, or 4 with twin_rituals.
func (c RitualCapabilityContext) EnabledQuota() int {
	if c.HasTwinRituals {
		return RitualEnabledQuotaTwin
	}
	return RitualEnabledQuotaBase
}

// RitualsUnlockedForStage reports whether Rituals are available at a stage
// (st4 structural+). The base drag-pipeline is innate at st4 (ADR-218 D1).
func RitualsUnlockedForStage(stage int) bool { return stage >= RitualUnlockStage }

func isKnownTrigger(t RitualTrigger) bool {
	switch t {
	case TriggerManual, TriggerOnMapOpen, TriggerOnDoseCompleted, TriggerSchedule, TriggerOnEvent:
		return true
	}
	return false
}

func isV1RunnableTrigger(t RitualTrigger) bool {
	switch t {
	case TriggerManual, TriggerOnMapOpen, TriggerOnDoseCompleted:
		return true
	}
	return false
}

// isKnownStepKind / isV1RunnableStepKind mirror the trigger pair above: a
// RESERVED kind is a KNOWN value that refuses, which is a different error from
// a typo, and the caller deserves to be told which.
func isKnownStepKind(k RitualStepKind) bool {
	switch k {
	case "", StepKindSkill, StepKindRosterPeer, StepKindSink:
		return true
	}
	return false
}

func isV1RunnableStepKind(k RitualStepKind) bool {
	switch k {
	case "", StepKindSkill:
		return true
	}
	return false
}

func isClosedSink(sink string) bool {
	switch sink {
	case SinkChat, SinkMemoryNote, SinkSuggestionInbox, SinkQuestionBank, SinkNotification, SinkCalendarArtifact:
		return true
	}
	return false
}

// NewRitual constructs a DRAFT ritual (disabled, no revisions). Validates the
// name, trigger (known + v1-runnable), and sink (closed list). The st4 gate is
// enforced at Publish/Enable, not construction (a learner can draft the shell
// before structural — the FE previews it during the st3 Aha window).
func NewRitual(tenantID, companionID, name string, trigger RitualTrigger, sink string) (*Ritual, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("companion.ritual: tenant_id required")
	}
	if strings.TrimSpace(companionID) == "" {
		return nil, errors.New("companion.ritual: companion_id required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrRitualNameRequired
	}
	if len(name) > RitualNameMaxLen {
		return nil, fmt.Errorf("%w: %d chars", ErrRitualNameTooLong, len(name))
	}
	if !isKnownTrigger(trigger) {
		return nil, fmt.Errorf("%w: %q", ErrRitualUnknownTrigger, trigger)
	}
	if !isV1RunnableTrigger(trigger) {
		return nil, fmt.Errorf("%w: %q", ErrRitualTriggerNotV1, trigger)
	}
	if !isClosedSink(sink) {
		return nil, fmt.Errorf("%w: %q", ErrRitualUnknownSink, sink)
	}
	now := time.Now().UTC()
	return &Ritual{
		RitualID:    domain.NewUUIDv7(),
		TenantID:    tenantID,
		CompanionID: companionID,
		Name:        name,
		Trigger:     trigger,
		Sink:        sink,
		Enabled:     false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// ValidateSteps checks the step pipeline against the v1 grammar: 1..StepCap
// steps, each a Skill that is BOTH equipped-active on the companion AND active
// in the catalogue. No branching in v1 (a branch grammar arrives with the
// weave_mastery craft at st6 — v2). Fail-loud with named errors.
func (r *Ritual) ValidateSteps(steps []RitualStep, cctx RitualCapabilityContext) error {
	if len(steps) == 0 {
		return ErrRitualNoSteps
	}
	if len(steps) > cctx.StepCap() {
		return fmt.Errorf("%w: %d steps, cap %d", ErrRitualTooManySteps, len(steps), cctx.StepCap())
	}
	for i, s := range steps {
		// ADR-257 reserved shape, checked BEFORE the capability guards so a
		// caller reaching for an unbuilt feature hears why, not "not equipped".
		if err := validateReservedStepShape(s); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		key := strings.TrimSpace(s.SkillKey)
		if !cctx.EquippedActiveSkills[key] {
			return fmt.Errorf("%w: step %d %q", ErrRitualStepSkillNotEquipped, i, key)
		}
		entry, ok := cctx.Catalogue[key]
		if !ok || !entry.Active {
			return fmt.Errorf("%w: step %d %q", ErrRitualStepSkillInactive, i, key)
		}
		// CHO-2362 param VALUE gate — present params must be valid (they ride
		// into the step prompt); absent params stay fine (ritual-context
		// default behaviour). Shared by publish AND the run-time re-check.
		if err := ValidateSkillParamValues(key, s.Params); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
	}
	return nil
}

// validateReservedStepShape enforces the ADR-257 reserved shape: the step kind
// is inside the closed set AND v1-runnable, and no inter-step contract field is
// set. StepID is deliberately NOT refused: it is inert identity, it changes no
// behaviour, and reserving it now is what lets a connector, a stamp reference
// or a run-story line name a step that survives reordering.
func validateReservedStepShape(s RitualStep) error {
	if !isKnownStepKind(s.StepKind) {
		return fmt.Errorf("%w: %q", ErrRitualStepKindUnknown, s.StepKind)
	}
	if !isV1RunnableStepKind(s.StepKind) {
		return fmt.Errorf("%w: %q (see ADR-257 §8 for roster_peer, owner ruling R14 for sink)",
			ErrRitualStepKindNotV1, s.StepKind)
	}
	for _, f := range []struct{ name, value string }{
		{"from_step_id", s.FromStepID},
		{"actor", s.Actor},
		{"capability_scope", s.CapabilityScope},
	} {
		if strings.TrimSpace(f.value) != "" {
			return fmt.Errorf("%w: %s (chaining ships with the ADR-257 §5 tool-allowlist enforcement gate)",
				ErrRitualStepFieldReserved, f.name)
		}
	}
	return nil
}

// ComposePriceUnits computes the composed-flat publish price (spec §7): the
// base plus a per-step uplift for each premium (generative / external_egress)
// Skill. standard/autonomy add nothing. Frozen into PublishedPriceUnits at
// publish and charged flat per run.
func ComposePriceUnits(steps []RitualStep, catalogue map[string]CatalogEntry) (int, error) {
	price := RitualBasePriceUnits
	for _, s := range steps {
		entry, ok := catalogue[strings.TrimSpace(s.SkillKey)]
		if !ok {
			return 0, fmt.Errorf("companion.ritual: compose price: unknown catalogue Skill %q", s.SkillKey)
		}
		switch entry.PolicyClass {
		case PolicyGenerative:
			price += RitualGenerativeUpliftUnits
		case PolicyExternalEgress:
			price += RitualEgressUpliftUnits
		}
	}
	return price, nil
}

// Publish validates the steps against the current capability, composes + freezes
// the price, and APPENDS an immutable revision (bumping CurrentRevision). It
// does NOT enable the ritual. Gated on the st4 unlock. Emits ritual_published.v1
// at the service layer after the atomic write.
func (r *Ritual) Publish(steps []RitualStep, armorVerdict string, cctx RitualCapabilityContext, clock func() time.Time) (RitualRevision, error) {
	if !RitualsUnlockedForStage(cctx.Stage) {
		return RitualRevision{}, fmt.Errorf("%w: stage %d", ErrRitualUnlockStage, cctx.Stage)
	}
	if err := r.ValidateSteps(steps, cctx); err != nil {
		return RitualRevision{}, err
	}
	price, err := ComposePriceUnits(steps, cctx.Catalogue)
	if err != nil {
		return RitualRevision{}, err
	}
	// Defensive deep-copy so a later mutation of the caller's slice can never
	// reach a stored (append-only) revision.
	cp := make([]RitualStep, len(steps))
	copy(cp, steps)
	now := clock()
	rev := RitualRevision{
		RevisionNo:   len(r.Revisions) + 1,
		Steps:        cp,
		ArmorVerdict: armorVerdict,
		CreatedAt:    now,
	}
	r.Revisions = append(r.Revisions, rev)
	r.CurrentRevision = rev.RevisionNo
	r.PublishedPriceUnits = price
	r.UpdatedAt = now
	return rev, nil
}

// Enable turns a published ritual on, enforcing the enabled-quota over the
// companion's OTHER enabled rituals (roster-cap idiom — the count is a
// parameter, checked atomically by the caller/repo). Requires a published
// revision.
func (r *Ritual) Enable(cctx RitualCapabilityContext) error {
	if r.CurrentRevision == 0 || len(r.Revisions) == 0 {
		return ErrRitualNoPublishedRevision
	}
	if cctx.OtherEnabledCount+1 > cctx.EnabledQuota() {
		return fmt.Errorf("%w: %d enabled, quota %d", ErrRitualEnabledQuotaReached, cctx.OtherEnabledCount+1, cctx.EnabledQuota())
	}
	r.Enabled = true
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// Disable turns a ritual off (idempotent).
func (r *Ritual) Disable() {
	r.Enabled = false
	r.UpdatedAt = time.Now().UTC()
}

// SoftDelete marks the ritual deleted (never hard-delete). Idempotent.
func (r *Ritual) SoftDelete(clock func() time.Time) {
	now := clock()
	r.DeletedAt = &now
	r.Enabled = false
	r.UpdatedAt = now
}

// CurrentSteps returns the steps of the current (latest) published revision, or
// nil when nothing is published yet.
func (r *Ritual) CurrentSteps() []RitualStep {
	if r.CurrentRevision == 0 || len(r.Revisions) == 0 {
		return nil
	}
	return r.Revisions[r.CurrentRevision-1].Steps
}
