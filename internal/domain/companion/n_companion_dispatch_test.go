// Package companion — RED tests for N-Companion dispatch (CHO-1577).
//
// Given Phyllis's N=4 Companion roster:
//
//	Eira    (…e1a0, dragon,  growth_stage=2, specialization=cspo,    growth_exp=200)
//	Aria    (…f1a1, phoenix, growth_stage=1, specialization=music,   growth_exp=150)
//	Mystery (…f1a2, dragon,  growth_stage=0, specialization=unbound, growth_exp=0)   ← pre-hatch, MUST be filtered
//	Ignis   (…f1a3, dragon,  growth_stage=4, specialization=physics, growth_exp=9500)
//
// Dispatch algorithm: given a DailyDose (with topic-labelled entries),
// count how many atoms' topics align with each hatched Companion's
// specialization. The Companion whose specialization best covers the dose
// greets the learner. Ties break on growth_exp descending.
//
// Invariants:
//   - Mystery Egg (growth_stage == 0) is NEVER a dispatch candidate.
//   - When no Companion's specialization matches any dose topic, fall back
//     to the highest-growth_exp non-pre-hatch Companion.
//   - Returns (companion_id, tie_break_reason) — tie_break_reason is empty
//     when no tie occurred.
//
// TDD strict — RED → GREEN → REFACTOR.
// These tests MUST fail (undefined: SelectGreetingCompanion) before the
// implementation is written.
package companion

import (
	"testing"
)

// ── Phyllis fixture roster ────────────────────────────────────────────────

// phyllisRoster returns the canonical Phyllis N=4 Companion fixture from
// docs/m13/companion-buildout-audit-2026-05-26.md §3.
func phyllisRoster() []RosterCompanion {
	return []RosterCompanion{
		{
			CompanionID:    "00000000-0000-7000-8000-00000000e1a0",
			Name:           "Eira",
			Specialization: "cspo",
			GrowthStage:    2, // Fledgling
			GrowthExp:      200,
		},
		{
			CompanionID:    "00000000-0000-7000-8000-00000000f1a1",
			Name:           "Aria",
			Specialization: "music",
			GrowthStage:    1, // Baby
			GrowthExp:      150,
		},
		{
			CompanionID:    "00000000-0000-7000-8000-00000000f1a2",
			Name:           "Mystery Egg",
			Specialization: "unbound",
			GrowthStage:    0, // Egg — PRE-HATCH, must be filtered
			GrowthExp:      0,
		},
		{
			CompanionID:    "00000000-0000-7000-8000-00000000f1a3",
			Name:           "Ignis",
			Specialization: "physics",
			GrowthStage:    4, // Structural
			GrowthExp:      9500,
		},
	}
}

// ── Dose builder helpers ──────────────────────────────────────────────────

// buildDoseEntries creates DailyDoseEntry slice with topic distribution
// matching the count slice.  topics[i] appears counts[i] times.
func buildDoseEntries(topicCounts map[string]int) []DailyDoseEntry {
	var entries []DailyDoseEntry
	seq := 0
	for topic, n := range topicCounts {
		for i := 0; i < n; i++ {
			entries = append(entries, DailyDoseEntry{
				AtomID:     "00000000-0000-7000-" + topic[:4] + "-" + padN(seq),
				Topic:      topic,
				Title:      topic + " atom",
				DoseReason: DoseReasonFresh,
			})
			seq++
		}
	}
	return entries
}

// padN pads n into a 12-char hex atom-id suffix (for unique fixture IDs).
func padN(n int) string {
	const h = "0123456789abcdef"
	buf := make([]byte, 12)
	for i := 11; i >= 0; i-- {
		buf[i] = h[n&0xf]
		n >>= 4
	}
	return string(buf)
}

// ── Test cases ────────────────────────────────────────────────────────────

// TestSelectGreetingCompanion_CspoDominates — 3 cspo + 2 music → Eira wins.
func TestSelectGreetingCompanion_CspoDominates(t *testing.T) {
	roster := phyllisRoster()
	dose := DailyDose{
		Entries: buildDoseEntries(map[string]int{
			"cspo":  3,
			"music": 2,
		}),
	}

	got, tieReason := SelectGreetingCompanion(roster, dose)

	if got != "00000000-0000-7000-8000-00000000e1a0" {
		t.Errorf("SelectGreetingCompanion = %q, want Eira (...e1a0); cspo majority (3 vs 2)", got)
	}
	// No tie when majority is clear — tie_break_reason must be empty.
	if tieReason != "" {
		t.Errorf("tie_break_reason = %q, want empty (clear majority, no tie)", tieReason)
	}
}

// TestSelectGreetingCompanion_MusicDominates — 2 cspo + 3 music → Aria wins.
func TestSelectGreetingCompanion_MusicDominates(t *testing.T) {
	roster := phyllisRoster()
	dose := DailyDose{
		Entries: buildDoseEntries(map[string]int{
			"cspo":  2,
			"music": 3,
		}),
	}

	got, tieReason := SelectGreetingCompanion(roster, dose)

	if got != "00000000-0000-7000-8000-00000000f1a1" {
		t.Errorf("SelectGreetingCompanion = %q, want Aria (...f1a1); music majority (3 vs 2)", got)
	}
	if tieReason != "" {
		t.Errorf("tie_break_reason = %q, want empty (clear majority)", tieReason)
	}
}

// TestSelectGreetingCompanion_TieBreakToHighestExp — 2 cspo + 2 music + 1
// physics → tie between Eira(200), Aria(150), Ignis(9500 but physics count=1).
//
// Topic-count map: cspo=2, music=2, physics=1.
// Eira (cspo) score=2, Aria (music) score=2, Ignis (physics) score=1.
// Eira and Aria are tied at 2. Tie-break by highest growth_exp: Eira(200) >
// Aria(150) → Eira wins.
//
// NOTE: Ignis is NOT in the tie because its physics score (1) is lower than
// the tied maximum (2).  The tie-break resolves within the tied group.
func TestSelectGreetingCompanion_TieBreakToHighestExp(t *testing.T) {
	roster := phyllisRoster()
	dose := DailyDose{
		Entries: buildDoseEntries(map[string]int{
			"cspo":    2,
			"music":   2,
			"physics": 1,
		}),
	}

	got, tieReason := SelectGreetingCompanion(roster, dose)

	if got != "00000000-0000-7000-8000-00000000e1a0" {
		t.Errorf("SelectGreetingCompanion = %q, want Eira (...e1a0); tie at 2 → highest exp (Eira 200 > Aria 150)", got)
	}
	if tieReason == "" {
		t.Error("tie_break_reason should be non-empty when tie was resolved by growth_exp")
	}
}

// TestSelectGreetingCompanion_MysteryEggFiltered — a roster where Mystery Egg
// would otherwise win (only unbound-topic atoms + 4 other atoms); Mystery Egg
// MUST be skipped and Ignis wins as highest-exp non-pre-hatch.
func TestSelectGreetingCompanion_MysteryEggFiltered(t *testing.T) {
	roster := phyllisRoster()
	// Dose entirely in "unbound" topic — only the Mystery Egg's specialization
	// matches. Since Mystery Egg is Stage 0 it is filtered. No other Companion's
	// specialization matches "unbound". Fallback = highest-exp non-pre-hatch.
	dose := DailyDose{
		Entries: buildDoseEntries(map[string]int{
			"unbound": 5,
		}),
	}

	got, tieReason := SelectGreetingCompanion(roster, dose)

	// Mystery Egg MUST NOT win.
	if got == "00000000-0000-7000-8000-00000000f1a2" {
		t.Error("SelectGreetingCompanion returned Mystery Egg (...f1a2) — pre-hatch Companion MUST be filtered")
	}
	// Fallback = highest-exp non-pre-hatch = Ignis (9500).
	if got != "00000000-0000-7000-8000-00000000f1a3" {
		t.Errorf("SelectGreetingCompanion = %q, want Ignis (...f1a3); fallback to highest-exp non-pre-hatch", got)
	}
	_ = tieReason // acceptable either way for fallback
}

// TestSelectGreetingCompanion_FallbackToHighestExpWhenNoMatch — all dose atoms
// in topics outside cspo/music/physics → no Companion matches → fallback to
// highest-exp non-pre-hatch (Ignis, exp 9500).
func TestSelectGreetingCompanion_FallbackToHighestExpWhenNoMatch(t *testing.T) {
	roster := phyllisRoster()
	dose := DailyDose{
		Entries: buildDoseEntries(map[string]int{
			"history":    2,
			"philosophy": 2,
			"geography":  1,
		}),
	}

	got, tieReason := SelectGreetingCompanion(roster, dose)

	if got == "00000000-0000-7000-8000-00000000f1a2" {
		t.Error("Mystery Egg must never be selected even in fallback path")
	}
	if got != "00000000-0000-7000-8000-00000000f1a3" {
		t.Errorf("SelectGreetingCompanion = %q, want Ignis (...f1a3); fallback to highest growth_exp (9500)", got)
	}
	_ = tieReason
}

// TestSelectGreetingCompanion_EmptyRoster — empty roster returns ("", "").
func TestSelectGreetingCompanion_EmptyRoster(t *testing.T) {
	dose := DailyDose{Entries: buildDoseEntries(map[string]int{"cspo": 3})}
	got, _ := SelectGreetingCompanion(nil, dose)
	if got != "" {
		t.Errorf("empty roster: got %q, want empty string", got)
	}
}

// TestSelectGreetingCompanion_AllPreHatch — all Companions at Stage 0 → returns
// ("", "") since nobody can greet.
func TestSelectGreetingCompanion_AllPreHatch(t *testing.T) {
	eggs := []RosterCompanion{
		{CompanionID: "aaa", Name: "Egg1", Specialization: "cspo", GrowthStage: 0, GrowthExp: 100},
		{CompanionID: "bbb", Name: "Egg2", Specialization: "music", GrowthStage: 0, GrowthExp: 200},
	}
	dose := DailyDose{Entries: buildDoseEntries(map[string]int{"cspo": 3})}
	got, _ := SelectGreetingCompanion(eggs, dose)
	if got != "" {
		t.Errorf("all-pre-hatch roster: got %q, want empty string", got)
	}
}

// TestSelectGreetingCompanion_EmptyDose — empty dose entries → fallback to
// highest-exp non-pre-hatch (no topic scores → tie → highest exp = Ignis).
func TestSelectGreetingCompanion_EmptyDose(t *testing.T) {
	roster := phyllisRoster()
	dose := DailyDose{Entries: nil}

	got, _ := SelectGreetingCompanion(roster, dose)

	if got == "00000000-0000-7000-8000-00000000f1a2" {
		t.Error("Mystery Egg must never be selected")
	}
	if got != "00000000-0000-7000-8000-00000000f1a3" {
		t.Errorf("empty dose: got %q, want Ignis (...f1a3); fallback highest exp (9500)", got)
	}
}

// TestSelectGreetingCompanion_SingleHatchedCompanion — only one non-pre-hatch
// Companion → always picks that one regardless of topic match.
func TestSelectGreetingCompanion_SingleHatchedCompanion(t *testing.T) {
	single := []RosterCompanion{
		{CompanionID: "aaa", Name: "Eira", Specialization: "cspo", GrowthStage: 2, GrowthExp: 200},
		{CompanionID: "bbb", Name: "Egg", Specialization: "unbound", GrowthStage: 0, GrowthExp: 0},
	}
	dose := DailyDose{Entries: buildDoseEntries(map[string]int{"music": 5})}
	got, _ := SelectGreetingCompanion(single, dose)
	if got != "aaa" {
		t.Errorf("single-hatched: got %q, want 'aaa' (only non-pre-hatch)", got)
	}
}

// TestRosterCompanionPreHatchFilter — IsPreHatch helper returns true only for
// Stage 0.
func TestRosterCompanionPreHatchFilter(t *testing.T) {
	egg := RosterCompanion{GrowthStage: 0}
	hatched := RosterCompanion{GrowthStage: 1}
	if !egg.IsPreHatch() {
		t.Error("Stage 0 must report IsPreHatch() == true")
	}
	if hatched.IsPreHatch() {
		t.Error("Stage 1 must report IsPreHatch() == false")
	}
}
