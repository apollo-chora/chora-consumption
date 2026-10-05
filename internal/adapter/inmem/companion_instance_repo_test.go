// companion_instance_repo_test.go — TDD spec for the multi-Companion (1:N)
// in-memory repository adapter. Production swap = pg.CompanionInstanceRepo
// at M14.1 (see services/chora-consumption/migrations/0006_familiar_multi.sql).
//
// Scope:
//
//   - Create persists + atomically rejects beyond roster cap
//   - Get / ListByOwner exclude soft-deleted
//   - Update upserts by CompanionID (tier promotion, skill grant, rule edit)
//   - SoftDelete is idempotent + future Gets return ErrInstanceNotFound
//   - List honours tenant isolation (defence-in-depth against handler bugs)
package inmem

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func mustNewInstance(t *testing.T, gcid, name, spec string) *companion.Instance {
	t.Helper()
	inst, err := companion.NewInstance("t1", gcid, name, spec)
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	return inst
}

func TestCompanionInstanceRepo_Create_persists(t *testing.T) {
	r := NewCompanionInstanceRepo()
	inst := mustNewInstance(t, "g1", "Newton", "math")
	ctx := context.Background()
	if err := r.Create(ctx, inst, companion.DefaultMaxCompanionsPerUser); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := r.Get(ctx, inst.CompanionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Newton" {
		t.Fatalf("expected Newton, got %s", got.Name)
	}
}

func TestCompanionInstanceRepo_Create_enforces_roster_cap(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	// Default cap = 3. Insert 3 successfully.
	for i, spec := range []string{"math", "history", "coding"} {
		inst := mustNewInstance(t, "g1", "F"+spec, spec)
		if err := r.Create(ctx, inst, companion.DefaultMaxCompanionsPerUser); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	// 4th must reject.
	over := mustNewInstance(t, "g1", "Overlimit", "music")
	err := r.Create(ctx, over, companion.DefaultMaxCompanionsPerUser)
	if err != companion.ErrRosterCapReached {
		t.Fatalf("expected ErrRosterCapReached, got %v", err)
	}
	// Different owner — cap is per-owner, NOT per-tenant.
	other := mustNewInstance(t, "g2", "Forg2", "math")
	if err := r.Create(ctx, other, companion.DefaultMaxCompanionsPerUser); err != nil {
		t.Fatalf("different owner should succeed, got %v", err)
	}
}

func TestCompanionInstanceRepo_ListByOwner_excludes_deleted(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	a := mustNewInstance(t, "g1", "A", "math")
	b := mustNewInstance(t, "g1", "B", "history")
	_ = r.Create(ctx, a, 5)
	_ = r.Create(ctx, b, 5)

	if err := r.SoftDelete(ctx, a.CompanionID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	list, err := r.ListByOwner(ctx, "t1", "g1")
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 (b only), got %d", len(list))
	}
	if list[0].CompanionID != b.CompanionID {
		t.Fatalf("expected b, got %s", list[0].Name)
	}
}

func TestCompanionInstanceRepo_ListByOwner_tenant_isolation(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	a, _ := companion.NewInstance("t1", "g1", "A", "math")
	b, _ := companion.NewInstance("t2", "g1", "B", "math") // same gcid, different tenant
	_ = r.Create(ctx, a, 5)
	_ = r.Create(ctx, b, 5)

	list1, _ := r.ListByOwner(ctx, "t1", "g1")
	list2, _ := r.ListByOwner(ctx, "t2", "g1")
	if len(list1) != 1 || list1[0].CompanionID != a.CompanionID {
		t.Fatalf("t1 expected [a], got %v", list1)
	}
	if len(list2) != 1 || list2[0].CompanionID != b.CompanionID {
		t.Fatalf("t2 expected [b], got %v", list2)
	}
}

func TestCompanionInstanceRepo_Update_upserts(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	inst := mustNewInstance(t, "g1", "Newton", "math")
	_ = r.Create(ctx, inst, 5)

	// CHO-2012 (ADR-218 D8): tier promotion + capped grants are retired —
	// Update persists plain field mutations; loadout rows live in the
	// grants bridge, not on the aggregate.
	inst.EvolutionTier = companion.TierMaster
	inst.SkillSlotsUnlocked = 5
	inst.SetRule("tone", "socratic")
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := r.Get(ctx, inst.CompanionID)
	if got.EvolutionTier != companion.TierMaster {
		t.Fatalf("update lost tier; got %s", got.EvolutionTier)
	}
	if got.SkillSlotsUnlocked != 5 {
		t.Fatalf("update lost slots; got %d", got.SkillSlotsUnlocked)
	}
	if got.ConfiguredRules["tone"] != "socratic" {
		t.Fatalf("update lost configured rule; got %v", got.ConfiguredRules)
	}
}

func TestCompanionInstanceRepo_SoftDelete_idempotent(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	inst := mustNewInstance(t, "g1", "Newton", "math")
	_ = r.Create(ctx, inst, 5)

	if err := r.SoftDelete(ctx, inst.CompanionID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := r.SoftDelete(ctx, inst.CompanionID); err != nil {
		t.Fatalf("second delete must be idempotent, got %v", err)
	}
	// Reads must report not-found post-delete.
	if _, err := r.Get(ctx, inst.CompanionID); err != companion.ErrInstanceNotFound {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestCompanionInstanceRepo_Get_unknown_returns_not_found(t *testing.T) {
	r := NewCompanionInstanceRepo()
	if _, err := r.Get(context.Background(), "missing"); err != companion.ErrInstanceNotFound {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestCompanionInstanceRepo_softdeleted_does_not_count_against_cap(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	// 3 active + 1 deleted should still allow a new one (cap=3 active).
	for _, spec := range []string{"math", "history", "coding"} {
		inst := mustNewInstance(t, "g1", "F"+spec, spec)
		_ = r.Create(ctx, inst, 3)
	}
	// Soft delete one.
	list, _ := r.ListByOwner(ctx, "t1", "g1")
	if err := r.SoftDelete(ctx, list[0].CompanionID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// New Create should succeed.
	revived := mustNewInstance(t, "g1", "Revived", "music")
	if err := r.Create(ctx, revived, 3); err != nil {
		t.Fatalf("after soft-delete a 4th should fit (cap counts active only), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListRosterByOwner — debt #44 / A24 / B5 enrichment.
//
// The in-memory adapter does NOT carry the ADR-149 growth-axis schema (the
// pg adapter does — additive 0032 ALTER TABLE). For tests + dev wiring, the
// in-memory adapter degrades cleanly: it returns RosterEntry with a populated
// Instance + empty/zero GrowthSnapshot + CosmeticSnapshot. Handlers + the
// FE consumer treat empty Growth/Cosmetic projections as "pre-hatch" — the
// existing fallback path stays valid.
// ---------------------------------------------------------------------------

func TestCompanionInstanceRepo_ListRosterByOwner_PopulatesInstance(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	a := mustNewInstance(t, "g1", "Newton", "math")
	b := mustNewInstance(t, "g1", "Galileo", "physics")
	_ = r.Create(ctx, a, companion.DefaultMaxCompanionsPerUser)
	_ = r.Create(ctx, b, companion.DefaultMaxCompanionsPerUser)

	out, err := r.ListRosterByOwner(ctx, "t1", "g1")
	if err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d; want 2", len(out))
	}
	for _, entry := range out {
		if entry == nil || entry.Instance == nil {
			t.Fatal("entry/Instance nil")
		}
		if entry.Growth == nil {
			t.Error("Growth nil; want zero-value GrowthSnapshot")
		}
		if entry.Cosmetic == nil {
			t.Error("Cosmetic nil; want zero-value CosmeticSnapshot")
		}
	}
}

func TestCompanionInstanceRepo_ListRosterByOwner_ExcludesDeleted(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	a := mustNewInstance(t, "g1", "Newton", "math")
	b := mustNewInstance(t, "g1", "Galileo", "physics")
	_ = r.Create(ctx, a, companion.DefaultMaxCompanionsPerUser)
	_ = r.Create(ctx, b, companion.DefaultMaxCompanionsPerUser)
	_ = r.SoftDelete(ctx, a.CompanionID)

	out, err := r.ListRosterByOwner(ctx, "t1", "g1")
	if err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d; want 1 (Newton soft-deleted)", len(out))
	}
	if out[0].Instance.Name != "Galileo" {
		t.Errorf("expected Galileo, got %s", out[0].Instance.Name)
	}
}

func TestCompanionInstanceRepo_ListRosterByOwner_EmptyResult_IsNotAnError(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	out, err := r.ListRosterByOwner(ctx, "t1", "unknown-gcid")
	if err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len(out) = %d; want 0", len(out))
	}
}
