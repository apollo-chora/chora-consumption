package companion

import "testing"

// TestLevelForXP_SafetyBoundAt1000 — levelForXP iterates upward until the next
// threshold exceeds the awarded XP, with a hard ceiling at level 1000 to
// guard against runaway iteration on absurd inputs. XPRequiredFor(1000) =
// 1000^2*100 = 100,000,000, so awarding exactly that must land at the bound.
func TestLevelForXP_SafetyBoundAt1000(t *testing.T) {
	f, err := New(tTenant, tGCID, "Eira")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Award enough to reach the ceiling: cumulative XP for level 1000.
	const xpForLevel1000 = 1000 * 1000 * 100 // 100,000,000
	leveled := f.AwardXP(xpForLevel1000)
	if !leveled {
		t.Fatal("expected level-up from awarding 100M XP")
	}
	if f.Level != 1000 {
		t.Fatalf("Level = %d, want 1000 (safety ceiling)", f.Level)
	}
	// Awarding even more must NOT exceed the ceiling.
	f.AwardXP(xpForLevel1000)
	if f.Level != 1000 {
		t.Fatalf("Level = %d after second mega-award, want capped at 1000", f.Level)
	}
}

// TestLevelForXP_MultiLevelJump — a single large award crosses several
// thresholds in one call (the comment-documented invariant: 1000 XP from
// level 1 jumps straight to level 3, since XPRequiredFor(3)=900 ≤ 1000 <
// 1600=XPRequiredFor(4)).
func TestLevelForXP_MultiLevelJump(t *testing.T) {
	f, _ := New(tTenant, tGCID, "Eira")
	leveled := f.AwardXP(1000)
	if !leveled {
		t.Fatal("expected level change")
	}
	if f.Level != 3 {
		t.Fatalf("Level = %d, want 3 (900 ≤ 1000 < 1600)", f.Level)
	}
}

// TestAddTrait_IgnoresBlankAndDedupes covers the two early-return branches of
// AddTrait: blank input is dropped, and an already-present trait is not
// re-appended (case-sensitive). The happy add is included to anchor the
// length expectations.
func TestAddTrait_IgnoresBlankAndDedupes(t *testing.T) {
	f, _ := New(tTenant, tGCID, "Eira")

	f.AddTrait("")    // blank → no-op
	f.AddTrait("   ") // whitespace-only → trimmed to blank → no-op
	if len(f.Traits) != 0 {
		t.Fatalf("blank traits appended: %v", f.Traits)
	}

	f.AddTrait("Curious")
	f.AddTrait("Curious") // exact dup → no-op
	if len(f.Traits) != 1 {
		t.Fatalf("Traits = %v, want exactly one 'Curious' (dedup)", f.Traits)
	}

	// Case-sensitivity: "curious" is a distinct trait from "Curious".
	f.AddTrait("curious")
	if len(f.Traits) != 2 {
		t.Fatalf("Traits = %v, want case-sensitive distinct entries", f.Traits)
	}

	// Trimmed input that equals an existing trait must still dedup.
	f.AddTrait("  Curious  ")
	if len(f.Traits) != 2 {
		t.Fatalf("Traits = %v, want trimmed-input dedup against 'Curious'", f.Traits)
	}
}
