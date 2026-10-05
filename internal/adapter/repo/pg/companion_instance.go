// companion_instance.go — Postgres-backed adapter for the multi-Companion
// (1:N) repository port (`companion.InstanceRepository`).
//
// Schema mirrors migrations/0006_familiar_multi.sql (additive 0032+ growth
// columns are read via repo/pg/growth.go — NOT projected here):
//
//	companion_instances (
//	    companion_id UUID PRIMARY KEY,
//	    tenant_id, owner_gcid, name, specialization,
//	    evolution_tier, skill_slots_unlocked, memory_context_capacity,
//	    persona_summary, configured_rules JSONB,
//	    created_at, updated_at, deleted_at
//	);
//
// Per multi-tenant-rls SKILL: every read/write runs rls.ApplySession to
// set chora.tenant_id session GUC inside the transaction (defence-in-
// depth complementing the per-database domain isolation).
//
// pgx wiring landed 2026-05-16 (closes M14.1 paydown #M14.1-famrepo-scan —
// stub Scan() calls were returning empty rows even when the DB had data,
// blocking the Phyllis demo `GET /v1/me/companions` route). The Get +
// ListByOwner Scan paths are real now; ErrNotImplemented is no longer
// returned by this file.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// SQL templates — reviewed before production codegen lands. Kept as
// package-level constants so tests can assert against them.

// insertCompanionInstanceSQL is the Instance INSERT, reachable from BOTH
// POST /v1/me/companions (createCompanionInstance) and the sovereign acquire lane
// (companion_acquire_handler.go).
//
// `species` is named EXPLICITLY as NULL and must stay that way (owner ruling
// 2026-08-07, ADR-248 D6). A companion.Instance carries no species: the growth
// axis is owned by growth.go and deliberately not projected here, so at create
// time there is genuinely nothing to write. But omitting the column does NOT
// leave it empty: migration 0046 gave `species` a random-hero column DEFAULT,
// so an omitted column mints a species nobody rolled and nobody sees.
//
// That decoy is then read by ownerSpeciesSetSQL as a species the learner OWNS,
// which under the no-repeat ruling EXCLUDES it from the odds of their next
// ceremony and 409s an explicit pick of it, against a breed they have never
// been shown. The NULLIF(species,”) guard does not catch it: the DEFAULT
// writes a real non-blank value that passes NULLIF cleanly.
//
// This is the same fix provisionEggInsertSQL carries for the egg lane
// (CHO-2227); these are the only two INSERTs into the table in service code.
// An empty species is safe downstream: the FE renders a species-less row as the
// breed-neutral Pod, never the Dragon (breed-art.component.ts `isPrehatch`).
const insertCompanionInstanceSQL = `
INSERT INTO companion_instances (
    companion_id, tenant_id, owner_gcid, name, specialization,
    evolution_tier, skill_slots_unlocked, memory_context_capacity,
    persona_summary, configured_rules, guidance_note, persona_version,
    species,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULL, $13, $14)
`

const upsertCompanionInstanceSQL = `
UPDATE companion_instances
   SET name                    = $4,
       specialization          = $5,
       evolution_tier          = $6,
       skill_slots_unlocked    = $7,
       memory_context_capacity = $8,
       persona_summary         = $9,
       configured_rules        = $10,
       guidance_note           = $11,
       persona_version         = $12,
       updated_at              = $13
 WHERE companion_id = $1
   AND tenant_id   = $2
   AND owner_gcid  = $3
   AND deleted_at IS NULL
`

const loadCompanionInstanceSQL = `
SELECT companion_id, tenant_id, owner_gcid, name, specialization,
       evolution_tier, skill_slots_unlocked, memory_context_capacity,
       COALESCE(persona_summary, ''),
       configured_rules, guidance_note, persona_version, created_at, updated_at, deleted_at
  FROM companion_instances
 WHERE companion_id = $1
   AND deleted_at IS NULL
`

const listCompanionInstancesByOwnerSQL = `
SELECT companion_id, tenant_id, owner_gcid, name, specialization,
       evolution_tier, skill_slots_unlocked, memory_context_capacity,
       COALESCE(persona_summary, ''),
       configured_rules, guidance_note, persona_version, created_at, updated_at, deleted_at
  FROM companion_instances
 WHERE tenant_id  = $1
   AND owner_gcid = $2
   AND deleted_at IS NULL
 ORDER BY created_at ASC
`

// listCompanionRosterByOwnerSQL projects the base 0006 columns + the
// additive 0032 growth-axis columns in a single SELECT per debt #44
// (single-query enrichment; no JOIN needed since 0032 ALTER TABLE placed
// the growth-axis columns on companion_instances itself).
//
// Column order MUST mirror scanCompanionRosterRow + the test fixture
// `canonicalEiraRosterValues` in companion_roster_test.go. Adding a column
// requires updating both — the SQL self-check test
// TestPGCompanionInstanceRepo_ListRosterByOwner_SQLProjectsGrowthAxisColumns
// will fail if a required column is dropped.
const listCompanionRosterByOwnerSQL = `
SELECT companion_id, tenant_id, owner_gcid, name, specialization,
       evolution_tier, skill_slots_unlocked, memory_context_capacity,
       COALESCE(persona_summary, ''),
       configured_rules, created_at, updated_at, deleted_at,
       growth_stage, species, shiny_variant, species_rarity,
       growth_exp, resonant_atom_id, hatched_at,
       aha_moment_consumed, aha_moment_active_until,
       effective_llm_tier_cached, last_stage_up_at
  FROM companion_instances
 WHERE tenant_id  = $1
   AND owner_gcid = $2
   AND deleted_at IS NULL
 ORDER BY created_at ASC
`

const countCompanionInstancesByOwnerSQL = `
SELECT count(*)
  FROM companion_instances
 WHERE tenant_id  = $1
   AND owner_gcid = $2
   AND deleted_at IS NULL
`

const softDeleteCompanionInstanceSQL = `
UPDATE companion_instances
   SET deleted_at = now(),
       updated_at = now()
 WHERE companion_id = $1
   AND deleted_at IS NULL
`

// CompanionInstanceRepo is the Postgres-backed multi-Companion repo.
type CompanionInstanceRepo struct {
	tx TxRunner
}

// NewCompanionInstanceRepo constructs the repo.
func NewCompanionInstanceRepo(tx TxRunner) *CompanionInstanceRepo {
	return &CompanionInstanceRepo{tx: tx}
}

// Create inserts a new Instance after atomically counting the owner's
// active rows against maxPerUser. Returns companion.ErrRosterCapReached
// when the cap is exceeded.
//
// Atomicity: count + insert run in the same RunInTx call so two
// concurrent Creates can't both pass the cap check.
func (r *CompanionInstanceRepo) Create(ctx context.Context, inst *companion.Instance, maxPerUser int) error {
	if inst == nil {
		return errors.New("pg: companion instance is nil")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		// Cap check.
		row := q.QueryRow(ctx, countCompanionInstancesByOwnerSQL, inst.TenantID, inst.OwnerGCID)
		if row == nil {
			return errors.New("pg: count companion_instances QueryRow returned nil")
		}
		var active int
		if err := row.Scan(&active); err != nil {
			if errors.Is(err, ErrNoRows) {
				active = 0
			} else {
				return fmt.Errorf("pg: scan count(companion_instances): %w", err)
			}
		}
		if maxPerUser > 0 && active >= maxPerUser {
			return companion.ErrRosterCapReached
		}

		// Insert. configured_rules is encoded as JSONB — pgx accepts
		// map[string]string natively (converted to JSON server-side).
		_, err := q.Exec(ctx, insertCompanionInstanceSQL,
			inst.CompanionID,
			inst.TenantID,
			inst.OwnerGCID,
			inst.Name,
			inst.Specialization,
			string(inst.EvolutionTier),
			inst.SkillSlotsUnlocked,
			inst.MemoryContextCapacity,
			inst.PersonaSummary,
			inst.ConfiguredRules,
			inst.GuidanceNote,
			inst.PersonaVersion,
			inst.CreatedAt,
			inst.UpdatedAt,
		)
		return err
	})
}

// Get returns the Instance or companion.ErrInstanceNotFound on miss.
//
// NOTE: skill_grants are NOT loaded here — they live in
// companion_skill_grants and require a separate JOIN. The base SELECT
// returns an empty SkillGrants slice and the service layer loads grants
// lazily when needed (per the lazy-load convention used by user_kg's
// MapCluster + Hexagon split). Growth-axis columns (0032+) are read via
// growth.go — NOT projected by loadCompanionInstanceSQL.
func (r *CompanionInstanceRepo) Get(ctx context.Context, companionID string) (*companion.Instance, error) {
	var out *companion.Instance
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadCompanionInstanceSQL, companionID)
		if row == nil {
			return errors.New("pg: load companion_instance QueryRow returned nil")
		}
		inst, scanErr := scanCompanionInstanceRow(row.Scan)
		if scanErr != nil {
			if errors.Is(scanErr, ErrNoRows) {
				return companion.ErrInstanceNotFound
			}
			return scanErr
		}
		out = inst
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListByOwner returns all non-deleted Instances for (tenantID, ownerGCID).
// Order: created_at ASC. Empty result is NOT an error — returns
// `(nil, nil)` so the handler can render an empty `items[]`.
func (r *CompanionInstanceRepo) ListByOwner(ctx context.Context, tenantID, ownerGCID string) ([]*companion.Instance, error) {
	var out []*companion.Instance
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listCompanionInstancesByOwnerSQL, tenantID, ownerGCID)
		if err != nil {
			return fmt.Errorf("pg: query companion_instances by owner: %w", err)
		}
		if rows == nil {
			// Empty result-set sentinel from the stub; treat as zero rows.
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			inst, scanErr := scanCompanionInstanceRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, inst)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListRosterByOwner returns enriched `*companion.RosterEntry` projections
// per debt #44 / A24 / B5 (2026-05-16) — single fetch eliminates the FE's
// prior N+1 (`GET /v1/me/companions` + per-Companion `GET /v1/me/companions/
// {id}/growth`). The projection JOIN-lessly combines the base 0006 columns
// with the additive 0032 growth-axis columns living on the same row.
//
// Order: created_at ASC (matches ListByOwner so callers + tests can pin
// row order). Empty result is NOT an error.
func (r *CompanionInstanceRepo) ListRosterByOwner(ctx context.Context, tenantID, ownerGCID string) ([]*companion.RosterEntry, error) {
	var out []*companion.RosterEntry
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listCompanionRosterByOwnerSQL, tenantID, ownerGCID)
		if err != nil {
			return fmt.Errorf("pg: query companion_roster by owner: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			entry, scanErr := scanCompanionRosterRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, entry)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanCompanionRosterRow scans one companion_instances row enriched with
// the 0032 growth-axis columns. Column order MUST mirror
// listCompanionRosterByOwnerSQL exactly.
//
// Nullable columns (`species`, `species_rarity`, `resonant_atom_id`,
// `effective_llm_tier_cached`, `hatched_at`, `aha_moment_active_until`,
// `last_stage_up_at`) scan into pointer locals — Stage-0 (Egg) rows
// surface them as NULLs which produce empty/zero fields on the
// projection.
func scanCompanionRosterRow(scan func(dest ...any) error) (*companion.RosterEntry, error) {
	var (
		// Base 0006 columns
		companionID           string
		tenantID              string
		ownerGCID             string
		name                  string
		specialization        string
		evolutionTier         string
		skillSlotsUnlocked    int
		memoryContextCapacity int
		personaSummary        string
		configuredRulesRaw    []byte
		createdAt             time.Time
		updatedAt             time.Time
		deletedAt             *time.Time
		// 0032 growth axis (additive on the same row)
		growthStage            int
		species                *string
		shinyVariant           bool
		speciesRarity          *string
		growthExp              int
		resonantAtomID         *string
		hatchedAt              *time.Time
		ahaMomentConsumed      bool
		ahaMomentActiveUntil   *time.Time
		effectiveLLMTierCached *string
		lastStageUpAt          *time.Time
	)
	if err := scan(
		&companionID,
		&tenantID,
		&ownerGCID,
		&name,
		&specialization,
		&evolutionTier,
		&skillSlotsUnlocked,
		&memoryContextCapacity,
		&personaSummary,
		&configuredRulesRaw,
		&createdAt,
		&updatedAt,
		&deletedAt,
		&growthStage,
		&species,
		&shinyVariant,
		&speciesRarity,
		&growthExp,
		&resonantAtomID,
		&hatchedAt,
		&ahaMomentConsumed,
		&ahaMomentActiveUntil,
		&effectiveLLMTierCached,
		&lastStageUpAt,
	); err != nil {
		return nil, err
	}
	rules, err := unmarshalConfiguredRules(configuredRulesRaw)
	if err != nil {
		return nil, fmt.Errorf("pg: scan companion_instances.configured_rules: %w", err)
	}
	inst := &companion.Instance{
		CompanionID:           companionID,
		TenantID:              tenantID,
		OwnerGCID:             ownerGCID,
		Name:                  name,
		Specialization:        specialization,
		EvolutionTier:         companion.EvolutionTier(evolutionTier),
		SkillSlotsUnlocked:    skillSlotsUnlocked,
		MemoryContextCapacity: memoryContextCapacity,
		PersonaSummary:        personaSummary,
		ConfiguredRules:       rules,
		SkillGrants:           []string{},
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
		DeletedAt:             deletedAt,
	}
	growth := &companion.GrowthSnapshot{
		Stage:                growthStage,
		StageName:            companion.StageName(growthStage),
		Exp:                  growthExp,
		ExpToNextStage:       companion.ExpToNextStage(growthStage),
		BreedRevealedAt:      hatchedAt,
		AhaMomentConsumed:    ahaMomentConsumed,
		AhaMomentActiveUntil: ahaMomentActiveUntil,
		LastStageUpAt:        lastStageUpAt,
	}
	if species != nil {
		growth.CurrentBreed = *species
	}
	if effectiveLLMTierCached != nil {
		growth.EffectiveLLMTier = *effectiveLLMTierCached
	}
	if resonantAtomID != nil {
		growth.ResonantAtomID = *resonantAtomID
	}
	cosmetic := &companion.CosmeticSnapshot{
		Shiny: shinyVariant,
	}
	if speciesRarity != nil {
		cosmetic.Rarity = *speciesRarity
	}
	return &companion.RosterEntry{
		Instance: inst,
		Growth:   growth,
		Cosmetic: cosmetic,
	}, nil
}

// scanCompanionInstanceRow scans one companion_instances row in the order
// projected by loadCompanionInstanceSQL + listCompanionInstancesByOwnerSQL.
// Reused by Get + ListByOwner so the column-to-field mapping has one
// canonical definition.
//
// configured_rules is JSONB and may carry non-string values (the seed
// migration 0036 inserts integers like `"max_hint_count": 3`). We scan into
// raw bytes and stringify each value so the domain map[string]string never
// drops information.
//
// scan signature: func(dest ...any) error — pgx.Row.Scan + pgx.Rows.Scan
// both satisfy this contract.
func scanCompanionInstanceRow(scan func(dest ...any) error) (*companion.Instance, error) {
	var (
		companionID           string
		tenantID              string
		ownerGCID             string
		name                  string
		specialization        string
		evolutionTier         string
		skillSlotsUnlocked    int
		memoryContextCapacity int
		personaSummary        string
		configuredRulesRaw    []byte
		guidanceNote          string
		personaVersion        int
		createdAt             time.Time
		updatedAt             time.Time
		deletedAt             *time.Time
	)
	if err := scan(
		&companionID,
		&tenantID,
		&ownerGCID,
		&name,
		&specialization,
		&evolutionTier,
		&skillSlotsUnlocked,
		&memoryContextCapacity,
		&personaSummary,
		&configuredRulesRaw,
		&guidanceNote,
		&personaVersion,
		&createdAt,
		&updatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}
	rules, err := unmarshalConfiguredRules(configuredRulesRaw)
	if err != nil {
		return nil, fmt.Errorf("pg: scan companion_instances.configured_rules: %w", err)
	}
	return &companion.Instance{
		CompanionID:           companionID,
		TenantID:              tenantID,
		OwnerGCID:             ownerGCID,
		Name:                  name,
		Specialization:        specialization,
		EvolutionTier:         companion.EvolutionTier(evolutionTier),
		SkillSlotsUnlocked:    skillSlotsUnlocked,
		MemoryContextCapacity: memoryContextCapacity,
		PersonaSummary:        personaSummary,
		ConfiguredRules:       rules,
		GuidanceNote:          guidanceNote,
		PersonaVersion:        personaVersion,
		// SkillGrants intentionally empty — loaded lazily via the
		// companion_skill_grants bridge in a follow-up.
		SkillGrants: []string{},
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		DeletedAt:   deletedAt,
	}, nil
}

// unmarshalConfiguredRules parses JSONB bytes into map[string]string.
//
// JSON values that are not strings (numbers, booleans) are stringified so
// the domain's map[string]string contract never silently drops data. NULL
// JSONB or an empty body produces an empty (non-nil) map so handlers can
// safely range over it.
func unmarshalConfiguredRules(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	var anyMap map[string]any
	if err := json.Unmarshal(raw, &anyMap); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(anyMap))
	for k, v := range anyMap {
		out[k] = stringifyJSONValue(v)
	}
	return out, nil
}

// stringifyJSONValue coerces an any-typed JSON value into its canonical
// string form. Numbers are formatted without trailing `.0` for integer
// values so the seed payload (`{"max_hint_count": 3}`) round-trips as `"3"`
// not `"3.000000"`.
func stringifyJSONValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// json.Unmarshal decodes all numbers as float64; if it is an integer,
		// drop the fractional part for a cleaner stringified form.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		// Slices / nested maps — re-marshal for a deterministic string form.
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// Update upserts an Instance by CompanionID. Honours tenant_id +
// owner_gcid as a defence-in-depth match (RLS is the primary control).
func (r *CompanionInstanceRepo) Update(ctx context.Context, inst *companion.Instance) error {
	if inst == nil {
		return errors.New("pg: companion instance is nil")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertCompanionInstanceSQL,
			inst.CompanionID,
			inst.TenantID,
			inst.OwnerGCID,
			inst.Name,
			inst.Specialization,
			string(inst.EvolutionTier),
			inst.SkillSlotsUnlocked,
			inst.MemoryContextCapacity,
			inst.PersonaSummary,
			inst.ConfiguredRules,
			inst.GuidanceNote,
			inst.PersonaVersion,
			inst.UpdatedAt,
		)
		return err
	})
}

// SoftDelete marks deleted_at = now(). Idempotent — repeating the call
// on a soft-deleted row is a no-op (WHERE deleted_at IS NULL).
func (r *CompanionInstanceRepo) SoftDelete(ctx context.Context, companionID string) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, softDeleteCompanionInstanceSQL, companionID)
		return err
	})
}

// Compile-time guarantee that *CompanionInstanceRepo satisfies the port.
var _ companion.InstanceRepository = (*CompanionInstanceRepo)(nil)
