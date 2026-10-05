//go:build integration

// companion_pod_mystery_integration_test.go — CHO-2227.
//
// A pod is a MYSTERY ARTIFACT: it has no species until it opens, and the type
// is rolled at the moment of opening. The schema said so first —
// 0032_familiar_growth.sql:36-38: "Breed/species (lootbox-rolled at HatchEgg,
// NOT at purchase). NULL while in Stage 0 (Egg); set permanently at hatching."
//
// 0046 then gave `species` a random-hero column DEFAULT, and
// provisionEggInsertSQL omits the column — so every purchased pod silently
// carries a hidden random species. That decoy is what the owner-species query
// then reads. Under the retired one-species directive it committed the
// account's PERMANENT species to a roll nobody made and nobody saw; under the
// 2026-08-07 no-repeat ruling it does the mirror-image damage - it EXCLUDES an
// unseen species from the learner's own odds on the strength of a pod they have
// not opened. Same decoy, same test, inverted consequence.
//
// These drive the REAL adapter over a REAL pgx connection because the unit
// suite stubs Exec and a fake repo returns a canned string — neither can see a
// column DEFAULT firing. There were ZERO tests referencing either SQL.
//
// Run against a NOBYPASSRLS role (the chora_consumption_app_rw shape):
//
//	export CHORA_TEST_DSN=postgres://...
//	go test -tags integration -run TestIntegration_PodMystery ./internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// TestIntegration_PodMystery_ProvisionedPodHasNoSpecies is the root assertion:
// a freshly purchased pod must carry NO species. Today the 0046 column DEFAULT
// fires (provisionEggInsertSQL omits the column) and this FAILS with a random
// hero — the hidden roll the learner is never shown.
func TestIntegration_PodMystery_ProvisionedPodHasNoSpecies(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewGrowthRepo(pg.NewPgxTxRunner(pool))

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	purchase, _ := uuid.NewV7()
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), tenant.String()), learner.String())

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx) //nolint:errcheck // best-effort cleanup
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM companion_instances WHERE tenant_id = $1`, tenant.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	row, err := repo.ProvisionEgg(ctx, growth.ProvisionEggInput{
		TenantID:      tenant.String(),
		OwnerGCID:     learner.String(),
		EggSku:        "egg.standard.v1",
		EggPurchaseID: purchase.String(),
		EggSource:     "purchase",
		Now:           time.Now().UTC(),
		Traceparent:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}
	if row.GrowthStage != 0 {
		t.Fatalf("provisioned pod is not stage 0: got %d", row.GrowthStage)
	}

	// Read the RAW column, not the domain projection — scanGrowthRow coerces
	// NULL to "" and would hide a real value behind a nil-guard either way.
	var species *string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only probe
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String())); err != nil {
		t.Fatalf("set tenant guc: %v", err)
	}
	// POSITIVE CONTROL: the row must be visible at all. An RLS-scoped read that
	// returns zero rows is a FALSE NEGATIVE, not a clean pod.
	var found int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM companion_instances WHERE companion_id = $1`,
		row.CompanionID).Scan(&found); err != nil {
		t.Fatalf("positive control count: %v", err)
	}
	if found != 1 {
		t.Fatalf("positive control FAILED: the provisioned pod is not readable (found=%d) — "+
			"a NULL-species assertion against an invisible row proves nothing", found)
	}
	if err := tx.QueryRow(ctx, `SELECT species FROM companion_instances WHERE companion_id = $1`,
		row.CompanionID).Scan(&species); err != nil {
		t.Fatalf("read species: %v", err)
	}

	if species != nil {
		t.Fatalf("a purchased pod carries a HIDDEN species %q — a pod is a mystery artifact and "+
			"must have species IS NULL until it opens (0032:36-38: \"lootbox-rolled at HatchEgg, "+
			"NOT at purchase\"). The 0046 column DEFAULT fired because provisionEggInsertSQL "+
			"omits the column; ownerSpeciesSetSQL then reads this roll as a species the learner "+
			"owns, excluding it from their next ceremony, though it was never shown.", *species)
	}
}

// TestIntegration_PodMystery_UnopenedPodDoesNotCommitSpecies is the bug's
// actual damage: an unopened pod must NOT contribute a species to the owner's
// owned set. Once the pod's species is NULL the EXISTING NULLIF(species,”) IS
// NOT NULL guard in ownerSpeciesSetSQL excludes it and this passes - no new
// predicate is needed, and deliberately NOT growth_stage > 0, because under
// reveal-before-naming a REVEALED-but-uncommitted pod HAS been met and should
// count.
func TestIntegration_PodMystery_UnopenedPodDoesNotCommitSpecies(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewGrowthRepo(pg.NewPgxTxRunner(pool))

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	purchase, _ := uuid.NewV7()
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), tenant.String()), learner.String())

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx) //nolint:errcheck // best-effort cleanup
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM companion_instances WHERE tenant_id = $1`, tenant.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	pod, err := repo.ProvisionEgg(ctx, growth.ProvisionEggInput{
		TenantID:      tenant.String(),
		OwnerGCID:     learner.String(),
		EggSku:        "egg.standard.v1",
		EggPurchaseID: purchase.String(),
		EggSource:     "purchase",
		Now:           time.Now().UTC(),
		Traceparent:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}

	// The learner now acquires a companion. Exclude a DIFFERENT companion id, so
	// the only candidate row is the unopened pod itself.
	other, _ := uuid.NewV7()
	owned, err := repo.OwnerSpeciesSet(ctx, tenant.String(), learner.String(), other.String())
	if err != nil {
		t.Fatalf("OwnerSpeciesSet: %v", err)
	}

	if len(owned) != 0 {
		t.Fatalf("an UNOPENED pod (%s) put %v into the owner's owned set - a species rolled by "+
			"nobody and revealed to no one. That species is then EXCLUDED from the learner's next "+
			"roll, and an explicit pick of it is 409'd, against a breed the incubation card "+
			"deliberately conceals.", pod.CompanionID, owned)
	}
}

// TestIntegration_PodMystery_LegacyCreatePathLeavesNoSpecies closes the OTHER
// reachable INSERT (owner ruling 2026-08-07; ADR-248 D6 and §5).
//
// `insertCompanionInstanceSQL` (companion_instance.go) serves BOTH
// POST /v1/me/companions (router.go:890 -> createCompanionInstance) AND the
// sovereign acquire lane (companion_acquire_handler.go -> CompanionInstances.Create).
// It named no `species`, so the migration-0046 random-hero DEFAULT fired and every
// row minted through it carried a hidden species.
//
// The damage is the mirror of the pod bug above and is NOT caught by the
// NULLIF(species,”) guard, because the DEFAULT writes a REAL non-blank value
// ('dragon', 'owl', ...) which passes NULLIF and enters the owned set. Companion A's
// decoy then narrows Companion B's odds and 409s an explicit pick of a breed the
// learner has never been shown.
//
// This drives the REAL adapter over a REAL pgx connection: the unit suite stubs
// Exec and cannot see a column DEFAULT fire at all.
func TestIntegration_PodMystery_LegacyCreatePathLeavesNoSpecies(t *testing.T) {
	pool := liveDB(t)
	instances := pg.NewCompanionInstanceRepo(pg.NewPgxTxRunner(pool))
	growthRepo := pg.NewGrowthRepo(pg.NewPgxTxRunner(pool))

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), tenant.String()), learner.String())

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx) //nolint:errcheck // best-effort cleanup
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM companion_instances WHERE tenant_id = $1`, tenant.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	inst, err := companion.NewInstance(tenant.String(), learner.String(), "Newton", "math")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := instances.Create(ctx, inst, companion.DefaultMaxCompanionsPerUser); err != nil {
		t.Fatalf("Create (legacy path): %v", err)
	}

	// Read the RAW column: scanGrowthRow coerces NULL to "" and would hide a real
	// value behind the same nil-guard either way.
	var species *string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only probe
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String())); err != nil {
		t.Fatalf("set tenant guc: %v", err)
	}
	// POSITIVE CONTROL: the row must be visible at all. An RLS-scoped read that
	// returns zero rows is a FALSE NEGATIVE, not a species-free companion.
	var found int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM companion_instances WHERE companion_id = $1`,
		inst.CompanionID).Scan(&found); err != nil {
		t.Fatalf("positive control count: %v", err)
	}
	if found != 1 {
		t.Fatalf("positive control FAILED: the created companion is not readable (found=%d) - "+
			"a NULL-species assertion against an invisible row proves nothing", found)
	}
	if err := tx.QueryRow(ctx, `SELECT species FROM companion_instances WHERE companion_id = $1`,
		inst.CompanionID).Scan(&species); err != nil {
		t.Fatalf("read species: %v", err)
	}
	if species != nil {
		t.Fatalf("a companion created through insertCompanionInstanceSQL carries a HIDDEN species %q. "+
			"The migration-0046 column DEFAULT fired because the INSERT does not name the column. "+
			"Nobody rolled this species and the learner was never shown it.", *species)
	}

	// The damage assertion: this row must not narrow a SIBLING's odds. Exclude a
	// different companion id so the only candidate row is the one just created.
	sibling, _ := uuid.NewV7()
	owned, err := growthRepo.OwnerSpeciesSet(ctx, tenant.String(), learner.String(), sibling.String())
	if err != nil {
		t.Fatalf("OwnerSpeciesSet: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("a legacy-path companion (%s) put %v into the owner's owned set. Those species are "+
			"now EXCLUDED from the learner's next ceremony and an explicit pick of one is 409'd "+
			"SPECIES_ALREADY_OWNED, on the strength of a roll nobody made.", inst.CompanionID, owned)
	}
}
