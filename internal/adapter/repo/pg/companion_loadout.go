// companion_loadout.go — pg adapters for the CHO-2012 capability surfaces
// (ADR-218 D3/D4): the platform Skill catalogue (companion_skill_catalog v2),
// the species Paths (species_paths) and the grants bridge with its loadout
// columns (companion_skill_grants.equipped/unlocked_via/unlocked_at_stage —
// consumption migration 0061).
//
// Implements companion.CatalogReader + companion.SpeciesPathReader +
// companion.LoadoutRepository. Catalogue + paths are PLATFORM tables (no
// RLS); grants are tenant-scoped so every grants statement runs after
// rls.ApplySession (tenant + gcid from ctx, matching the other repos).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const listSkillCatalogSQL = `
	SELECT skill_key, name, skill_kind, min_growth_stage, slot_cost,
	       COALESCE(family, ''), policy_class, COALESCE(output_sink, ''),
	       COALESCE(price_key, ''), tool_handler_ref, active
	  FROM companion_skill_catalog
	 WHERE deleted_at IS NULL`

const activeSpeciesPathSQL = `
	SELECT path_id, species, version, entries
	  FROM species_paths
	 WHERE species = $1
	   AND active
	 LIMIT 1`

const listGrantsSQL = `
	SELECT c.skill_key, c.skill_kind, c.slot_cost, g.equipped,
	       COALESCE(g.unlocked_via, ''), COALESCE(g.unlocked_at_stage, 0)
	  FROM companion_skill_grants g
	  JOIN companion_skill_catalog c ON c.skill_id = g.skill_id
	 WHERE g.companion_id = $1
	   AND g.deleted_at IS NULL
	   AND c.deleted_at IS NULL`

const listGrantedKeysSQL = `
	SELECT c.skill_key
	  FROM companion_skill_grants g
	  JOIN companion_skill_catalog c ON c.skill_id = g.skill_id
	 WHERE g.companion_id = $1
	   AND g.deleted_at IS NULL
	   AND c.deleted_at IS NULL`

// mintGrantSQL inserts one grant by skill_key (catalogue-resolved skill_id),
// idempotent on UNIQUE (companion_id, skill_id). grant_id is minted in Go
// (UUIDv7 per the new-rows invariant). RowsAffected 0 = already granted.
const mintGrantSQL = `
	INSERT INTO companion_skill_grants
	       (grant_id, tenant_id, companion_id, skill_id, equipped,
	        unlocked_via, unlocked_at_stage)
	SELECT $1, $2, $3, c.skill_id, $5, $6, $7
	  FROM companion_skill_catalog c
	 WHERE c.skill_key = $4
	   AND c.deleted_at IS NULL
	ON CONFLICT (companion_id, skill_id) DO NOTHING`

const resolveSkillIDSQL = `
	SELECT skill_id
	  FROM companion_skill_catalog
	 WHERE skill_key = $1
	   AND deleted_at IS NULL`

const setGrantEquippedSQL = `
	UPDATE companion_skill_grants g
	   SET equipped = $3
	  FROM companion_skill_catalog c
	 WHERE c.skill_id = g.skill_id
	   AND g.companion_id = $1
	   AND c.skill_key = $2
	   AND g.deleted_at IS NULL`

// CompanionLoadoutRepo is the pg adapter for the loadout surfaces.
type CompanionLoadoutRepo struct {
	tx TxRunner
}

// NewCompanionLoadoutRepo constructs the repo over the shared TxRunner.
func NewCompanionLoadoutRepo(tx TxRunner) *CompanionLoadoutRepo {
	return &CompanionLoadoutRepo{tx: tx}
}

// ListCatalogue implements companion.CatalogReader.
func (r *CompanionLoadoutRepo) ListCatalogue(ctx context.Context) (map[string]companion.CatalogEntry, error) {
	out := map[string]companion.CatalogEntry{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		rows, err := q.Query(ctx, listSkillCatalogSQL)
		if err != nil {
			return fmt.Errorf("pg: query companion_skill_catalog: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e           companion.CatalogEntry
				kind        string
				handlerRefs string
			)
			if err := rows.Scan(&e.SkillKey, &e.Name, &kind, &e.MinGrowthStage, &e.SlotCost,
				&e.Family, &e.PolicyClass, &e.OutputSink, &e.PriceKey, &handlerRefs, &e.Active); err != nil {
				return fmt.Errorf("pg: scan companion_skill_catalog: %w", err)
			}
			e.SkillKind = companion.SkillKind(kind)
			e.ToolHandlerRefs = parseToolHandlerRefs(handlerRefs)
			out[e.SkillKey] = e
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// parseToolHandlerRefs decodes the tool_handler_ref column: a JSON array
// (the v2 seed shape) or a single legacy plain ref.
func parseToolHandlerRefs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return []string{}
	}
	if strings.HasPrefix(raw, "[") {
		var refs []string
		if err := json.Unmarshal([]byte(raw), &refs); err == nil {
			return refs
		}
		// Malformed JSON — surface the raw value rather than dropping it.
	}
	return []string{raw}
}

// ActivePathForSpecies implements companion.SpeciesPathReader.
func (r *CompanionLoadoutRepo) ActivePathForSpecies(ctx context.Context, species string) (*companion.SpeciesPath, error) {
	var out *companion.SpeciesPath
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		row := q.QueryRow(ctx, activeSpeciesPathSQL, species)
		if row == nil {
			return errors.New("pg: species_paths QueryRow returned nil")
		}
		var (
			p       companion.SpeciesPath
			entries []byte
		)
		if err := row.Scan(&p.PathID, &p.Species, &p.Version, &entries); err != nil {
			if errors.Is(err, ErrNoRows) {
				return fmt.Errorf("%w: %q", companion.ErrSpeciesPathNotFound, species)
			}
			return fmt.Errorf("pg: scan species_paths: %w", err)
		}
		if err := json.Unmarshal(entries, &p.Entries); err != nil {
			return fmt.Errorf("pg: species_paths.entries for %q is not a JSON string array: %w", species, err)
		}
		p.Active = true
		out = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListGrants implements companion.LoadoutRepository.
func (r *CompanionLoadoutRepo) ListGrants(ctx context.Context, tenantID, companionID string) ([]companion.SkillGrant, error) {
	var out []companion.SkillGrant
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listGrantsSQL, companionID)
		if err != nil {
			return fmt.Errorf("pg: query companion_skill_grants: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var (
				g    companion.SkillGrant
				kind string
			)
			if err := rows.Scan(&g.SkillKey, &kind, &g.SlotCost, &g.Equipped, &g.UnlockedVia, &g.UnlockedAtStage); err != nil {
				return fmt.Errorf("pg: scan companion_skill_grants: %w", err)
			}
			g.SkillKind = companion.SkillKind(kind)
			out = append(out, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListGrantedSkillKeys implements companion.GrantWriter.
func (r *CompanionLoadoutRepo) ListGrantedSkillKeys(ctx context.Context, tenantID, companionID string) ([]string, error) {
	var out []string
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listGrantedKeysSQL, companionID)
		if err != nil {
			return fmt.Errorf("pg: query granted keys: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return fmt.Errorf("pg: scan granted key: %w", err)
			}
			out = append(out, key)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MintGrants implements companion.GrantWriter: idempotent per-mint INSERT
// (ON CONFLICT DO NOTHING); returns the keys ACTUALLY inserted. A mint for
// a key the catalogue does not carry fails loud (seed corruption — never a
// silent skip): the skill_id pre-resolve disambiguates "unknown key" from
// "already granted" (both would report 0 rows on the bare INSERT..SELECT).
func (r *CompanionLoadoutRepo) MintGrants(ctx context.Context, tenantID, companionID string, mints []companion.GrantMint) ([]string, error) {
	var inserted []string
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		for _, m := range mints {
			idRow := q.QueryRow(ctx, resolveSkillIDSQL, m.SkillKey)
			if idRow == nil {
				return errors.New("pg: resolveSkillID QueryRow returned nil")
			}
			var skillID string
			if err := idRow.Scan(&skillID); err != nil {
				if errors.Is(err, ErrNoRows) {
					return fmt.Errorf("%w: %q", companion.ErrSkillNotInCatalog, m.SkillKey)
				}
				return fmt.Errorf("pg: resolve skill_id for %q: %w", m.SkillKey, err)
			}
			tag, err := q.Exec(ctx, mintGrantSQL,
				domain.NewUUIDv7(), tenantID, companionID, m.SkillKey,
				m.Equipped, m.UnlockedVia, m.UnlockedAtStage)
			if err != nil {
				return fmt.Errorf("pg: mint grant %q: %w", m.SkillKey, err)
			}
			if tag.RowsAffected > 0 {
				inserted = append(inserted, m.SkillKey)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// SetEquipped implements companion.LoadoutRepository.
func (r *CompanionLoadoutRepo) SetEquipped(ctx context.Context, tenantID, companionID, skillKey string, equipped bool) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, setGrantEquippedSQL, companionID, skillKey, equipped)
		if err != nil {
			return fmt.Errorf("pg: set grant equipped: %w", err)
		}
		if tag.RowsAffected == 0 {
			return fmt.Errorf("%w: %q", companion.ErrSkillNotOwned, skillKey)
		}
		return nil
	})
}

// Compile-time port guarantees.
var (
	_ companion.CatalogReader     = (*CompanionLoadoutRepo)(nil)
	_ companion.SpeciesPathReader = (*CompanionLoadoutRepo)(nil)
	_ companion.LoadoutRepository = (*CompanionLoadoutRepo)(nil)
)
