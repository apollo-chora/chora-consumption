package companion

import (
	"testing"
)

// TestNew verifies a fresh Companion is created at level 1, xp 0, no traits.
func TestNew(t *testing.T) {
	tenantID := "01970000-0000-7000-8000-000000000001"
	gcid := "01970000-0000-7000-9000-000000000001"

	f, err := New(tenantID, gcid, "Sparky")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.CompanionID == "" {
		t.Error("expected CompanionID to be set (UUIDv7)")
	}
	if f.TenantID != tenantID {
		t.Errorf("TenantID = %q, want %q", f.TenantID, tenantID)
	}
	if f.OwnerGCID != gcid {
		t.Errorf("OwnerGCID = %q, want %q", f.OwnerGCID, gcid)
	}
	if f.Name != "Sparky" {
		t.Errorf("Name = %q, want Sparky", f.Name)
	}
	if f.Level != 1 {
		t.Errorf("Level = %d, want 1", f.Level)
	}
	if f.XP != 0 {
		t.Errorf("XP = %d, want 0", f.XP)
	}
	if f.Traits == nil {
		t.Error("expected Traits to be empty slice, got nil")
	}
	if len(f.Traits) != 0 {
		t.Errorf("len(Traits) = %d, want 0", len(f.Traits))
	}
	if f.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

// TestNew_RequiresTenant ensures multi-tenancy invariants.
func TestNew_RequiresTenant(t *testing.T) {
	_, err := New("", "01970000-0000-7000-9000-000000000001", "Sparky")
	if err == nil {
		t.Error("expected error when tenant_id is empty")
	}
}

// TestNew_RequiresGCID enforces ownership.
func TestNew_RequiresGCID(t *testing.T) {
	_, err := New("01970000-0000-7000-8000-000000000001", "", "Sparky")
	if err == nil {
		t.Error("expected error when gcid is empty")
	}
}

// TestNew_RequiresName ensures non-empty name.
func TestNew_RequiresName(t *testing.T) {
	_, err := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "")
	if err == nil {
		t.Error("expected error when name is empty")
	}
}

// TestXPRequiredFor verifies the quadratic XP-to-level curve.
//
// Curve: level n requires n^2 * 100 XP CUMULATIVE total to reach level n.
//
//	level 1 → 0 XP
//	level 2 → 400 XP   (2^2 * 100)
//	level 3 → 900 XP   (3^2 * 100)
//	level 4 → 1600 XP  (4^2 * 100)
//	...
//
// This makes leveling progressively harder (gentle quadratic curve), aligned
// with engagement loop principles in CLAUDE.md (Curiosity-Reward-Social with
// Ebbinghaus). NOT linear — would either be too easy at high levels (linear
// flat) or too punishing at low levels (cubic).
func TestXPRequiredFor(t *testing.T) {
	cases := []struct {
		level int
		xp    int
	}{
		{1, 0},
		{2, 400},
		{3, 900},
		{4, 1600},
		{5, 2500},
		{10, 10000},
	}
	for _, c := range cases {
		got := XPRequiredFor(c.level)
		if got != c.xp {
			t.Errorf("XPRequiredFor(%d) = %d, want %d", c.level, got, c.xp)
		}
	}
}

// TestAwardXP_NoLevel ensures XP awarded below threshold does not level up.
// At level 1 with 0 XP, awarding 200 XP brings total to 200 — still below
// level-2 threshold of 400. Level remains 1.
func TestAwardXP_NoLevel(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	leveled := f.AwardXP(200)
	if leveled {
		t.Error("expected no level-up at 200 XP (threshold is 400)")
	}
	if f.XP != 200 {
		t.Errorf("XP = %d, want 200", f.XP)
	}
	if f.Level != 1 {
		t.Errorf("Level = %d, want 1", f.Level)
	}
}

// TestAwardXP_LevelUp ensures crossing threshold levels up.
// 0 XP + 400 XP = 400 XP total → level 2 (threshold).
func TestAwardXP_LevelUp(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	leveled := f.AwardXP(400)
	if !leveled {
		t.Error("expected level-up at 400 XP (threshold for level 2)")
	}
	if f.Level != 2 {
		t.Errorf("Level = %d, want 2", f.Level)
	}
	if f.XP != 400 {
		t.Errorf("XP = %d, want 400", f.XP)
	}
}

// TestAwardXP_MultipleLevelJump verifies a single award can cross multiple
// thresholds (e.g., 1000 XP at level 1 jumps to level 3, since 900 XP is
// the level-3 threshold and 1000 < 1600 (level 4)).
func TestAwardXP_MultipleLevelJump(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	leveled := f.AwardXP(1000)
	if !leveled {
		t.Error("expected level-up")
	}
	if f.Level != 3 {
		t.Errorf("Level = %d, want 3 (from 1000 XP, threshold L3=900, L4=1600)", f.Level)
	}
}

// TestAwardXP_BoundaryExact ensures the exact threshold triggers level.
// 1599 XP → level 3 (since 1600 is L4 threshold).
// 1600 XP → level 4 (exact threshold).
func TestAwardXP_BoundaryExact(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	f.AwardXP(1599)
	if f.Level != 3 {
		t.Errorf("at 1599 XP: Level = %d, want 3", f.Level)
	}

	f2, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky2")
	f2.AwardXP(1600)
	if f2.Level != 4 {
		t.Errorf("at 1600 XP: Level = %d, want 4", f2.Level)
	}
}

// TestAwardXP_NegativeRejected ensures negative XP is rejected.
func TestAwardXP_NegativeRejected(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	leveled := f.AwardXP(-100)
	if leveled {
		t.Error("expected no level change for negative XP")
	}
	if f.XP != 0 {
		t.Errorf("XP = %d, want 0 (negative ignored)", f.XP)
	}
}

// TestAwardXP_ZeroNoOp ensures awarding 0 XP is a no-op.
func TestAwardXP_ZeroNoOp(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	leveled := f.AwardXP(0)
	if leveled {
		t.Error("expected no level change for 0 XP")
	}
}

// TestAddTrait verifies trait deduplication.
func TestAddTrait(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Sparky")
	f.AddTrait("curious")
	f.AddTrait("brave")
	f.AddTrait("curious") // dup
	if len(f.Traits) != 2 {
		t.Errorf("len(Traits) = %d, want 2 (dedup)", len(f.Traits))
	}
}
