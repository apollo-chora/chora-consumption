// instance.go — multi-Companion (1:N) aggregate root.
//
// Each learner (owner_gcid) can own multiple specialised Companion Instances
// (math / history / coding / music / ...). Per ADR-116 amendment 2026-05-12 +
// ADR-147 §7 + docs/architecture/multi-companion-per-user-2026-05-11.md +
// .claude/skills/domain-content-consumption/SKILL.md §Multi-Companion.
//
// CRITICAL: This is a DOMAIN ENTITY (RPG companion game mechanics). The AI
// agent that POWERS Companion responses lives separately in
// services/chora-ai-kernel-orchestrator/agents/companion_chat_adk_go/ as a P1 ADK Go
// crew (per crew-composition skill). Per the feedback_companion_vs_agent
// memory: NEVER conflate the entity with the agent.
//
// Per-Companion variance (skill set, behavioural rules, RAG slice, Memory
// Bank scope) lives in DATA on this aggregate — NOT in agent code. The
// session-time per-instance bootstrap in the Companion Companion crew reads
// this entity at session start and constructs the per-Companion agent
// instance (per ADR-147 §7).
//
// Evolution tier is a DERIVED presentation band since CHO-2012 (ADR-218
// D8): {growth stages 0-1 apprentice · 2-3 adept · 4-5 master · 6 sage},
// computed by growth.TierForStage. The former per-user-XP gate
// (PromoteByUserXP over a `LearnerXPBalanceView` fed by
// `chora.sharing.xp.credited.v1`) is RETIRED — that feeder event never
// existed in contracts, the gate had no production caller, and slots now
// derive from the companion's own growth stage (+1 per stage,
// growth.SlotsForStage). The companion_progression_tiers table remains as
// dormant reference data (kept like xp_unlock_threshold; nothing reads it).
//
// Roster cap (max_companions_per_user) is enforced at the repository layer,
// not at the entity constructor — see InstanceRepository.Create.
package companion

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// EvolutionTier is the derived, never-shown presentation band (L6 one
// visible ladder; ADR-218 D8). Persisted for projection compatibility;
// derived from growth_stage, never a gate.
type EvolutionTier string

// EvolutionTier constants. Order matters: apprentice < adept < master < sage.
const (
	TierApprentice EvolutionTier = "apprentice"
	TierAdept      EvolutionTier = "adept"
	TierMaster     EvolutionTier = "master"
	TierSage       EvolutionTier = "sage"
)

// Instance is one Companion in the multi-Companion (1:N) roster.
//
// Schema mirror (M14.1 migration target):
//
//	CREATE TABLE companion_instances (
//	    companion_id              UUID PRIMARY KEY,
//	    tenant_id                UUID NOT NULL,
//	    owner_gcid               UUID NOT NULL,
//	    name                     TEXT NOT NULL,
//	    specialization           TEXT NOT NULL,
//	    evolution_tier           TEXT NOT NULL DEFAULT 'apprentice',
//	    skill_slots_unlocked     INT  NOT NULL DEFAULT 1,
//	    memory_context_capacity  INT  NOT NULL DEFAULT 1000,
//	    persona_summary          TEXT,
//	    configured_rules         JSONB NOT NULL DEFAULT '{}',
//	    created_at, updated_at, deleted_at
//	);
//
// `SkillGrants` mirrors `companion_skill_grants` (the bridge to the platform
// `companion_skill_catalog`) as a flat OWNED-skill-key projection. Since
// CHO-2012 the bridge rows carry equip/unlock metadata — loadout mutations
// (equip/unequip, Path minting) go through the Loadout value object +
// GrantWriter/LoadoutRepository ports, NOT through this aggregate; the base
// repository reads leave the slice empty (lazy-load convention).
type Instance struct {
	CompanionID           string
	TenantID              string
	OwnerGCID             string
	Name                  string
	Specialization        string
	EvolutionTier         EvolutionTier
	SkillSlotsUnlocked    int
	MemoryContextCapacity int
	PersonaSummary        string
	ConfiguredRules       map[string]string // tone / hint_policy / difficulty_cap / language / citation_strictness / address_style / interest_chips
	SkillGrants           []string          // skill_keys from companion_skill_catalog
	// GuidanceNote is the learner-authored fenced free-text persona note
	// (≤280 chars, ADR-219 D2 — the single sanctioned exception to ADR-205's
	// zero-free-form rule). Armor-INSPECTed at save + runtime; injected into a
	// locked-frame agent segment. Empty until the learner writes one.
	GuidanceNote string
	// PersonaVersion is a monotonic counter bumped on every persona save
	// (ADR-219 D2 "persona changes are versioned"); carried on
	// companion.persona_updated.v1 so O+ can attribute a decision to a version.
	PersonaVersion int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

// Sentinel errors so callers (handlers, services) can map to HTTP status.
// ErrSkillSlotsFull fires at EQUIP time since CHO-2012 (ADR-218 D3: the
// slot cap binds the equipped set only; grants are owned forever).
var (
	ErrInstanceInvalid   = errors.New("companion.instance: invalid")
	ErrSkillSlotsFull    = errors.New("companion.instance: skill slots full (stage up to widen the loadout)")
	ErrSpecializationBad = errors.New("companion.instance: specialization must be non-empty")
)

// NewInstance constructs a fresh Companion Instance at apprentice tier
// (1 skill slot, 1000 Memory Bank events). Roster cap (max_companions_per_user)
// is enforced at the repository layer — this constructor only validates the
// entity's own invariants.
func NewInstance(tenantID, ownerGCID, name, specialization string) (*Instance, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("companion.instance: tenant_id required")
	}
	if strings.TrimSpace(ownerGCID) == "" {
		return nil, errors.New("companion.instance: owner_gcid required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("companion.instance: name required")
	}
	if strings.TrimSpace(specialization) == "" {
		return nil, errors.New("companion.instance: specialization required")
	}

	now := time.Now().UTC()
	return &Instance{
		CompanionID:    domain.NewUUIDv7(),
		TenantID:       tenantID,
		OwnerGCID:      ownerGCID,
		Name:           strings.TrimSpace(name),
		Specialization: strings.TrimSpace(specialization),
		// Stage-0 (egg) values per ADR-218 D2: 1 slot at Egg; the award tx
		// keeps skill_slots_unlocked = growth_stage + 1 from then on.
		// Tier is the derived band for stage 0 (growth.TierForStage).
		EvolutionTier:         TierApprentice,
		SkillSlotsUnlocked:    1,
		MemoryContextCapacity: 1_000,
		ConfiguredRules:       map[string]string{},
		SkillGrants:           []string{},
		CreatedAt:             now,
		UpdatedAt:             now,
	}, nil
}

// ChangeSpecialization swaps the Companion's topic specialization (math →
// coding, etc.). Identity (CompanionID + CreatedAt) MUST be preserved — this
// is a re-tag, not a destroy-and-recreate. Returns the previous
// specialization so the caller can emit
// `chora.consumption.companion.specialization_changed.v1` with both values.
//
// Per multi-companion-per-user-2026-05-11.md §6 Q4: specialization change
// may invalidate Memory Bank context. The caller decides purge vs carryover
// via the published event payload.
func (inst *Instance) ChangeSpecialization(newSpec string) (oldSpec string, err error) {
	newSpec = strings.TrimSpace(newSpec)
	if newSpec == "" {
		return "", ErrSpecializationBad
	}
	old := inst.Specialization
	inst.Specialization = newSpec
	inst.UpdatedAt = time.Now().UTC()
	return old, nil
}

// SetRule writes a single configured_rules entry. Idempotent — same
// (key,value) re-write is a no-op. Empty key / value silently ignored.
func (inst *Instance) SetRule(key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return
	}
	if inst.ConfiguredRules == nil {
		inst.ConfiguredRules = map[string]string{}
	}
	if inst.ConfiguredRules[key] == value {
		return
	}
	inst.ConfiguredRules[key] = value
	inst.UpdatedAt = time.Now().UTC()
}

// SoftDelete marks the Instance as deleted without removing the row.
// Per ddd-enforcement.md #5: every entity uses soft delete. Hard delete is
// reserved for crypto-shred via the account-closure saga (per-tenant DEK
// deletion). Idempotent — second call is a no-op.
func (inst *Instance) SoftDelete() {
	if inst.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	inst.DeletedAt = &now
	inst.UpdatedAt = now
}

// IsDeleted reports whether the Instance is soft-deleted. Repository
// default-reads exclude these per the soft-delete invariant.
func (inst *Instance) IsDeleted() bool {
	return inst.DeletedAt != nil
}

// MemoryBankAppName returns the Vertex AI Memory Bank `app_name` for this
// Instance. Per multi-companion-per-user-2026-05-11.md §2.4 the format is
// `companion:{companion_id}` (NOT `companion_chat` singular — that
// conflates all Companions' memories per the anti-pattern in the
// domain-content-consumption skill).
func (inst *Instance) MemoryBankAppName() string {
	return "companion:" + inst.CompanionID
}
