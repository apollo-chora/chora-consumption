// legacy_names.go - the pre-ADR-254 identifiers the HISTORICAL migrations were
// applied with (0001..0109), so the migration-content gates can keep comparing
// the frozen files against the Go mirror after the Familiar -> Companion rename.
//
// The historical files are frozen at the bytes the live lane applied (the
// runner's tracker is filename-keyed; its backdated gate refuses renamed pending
// files, and a re-authored applied file is a lie about history). Migration
// 0110_companion_rename renames the live objects + seeded values afterwards, so
// "0061 .. 0109 then 0110" yields exactly what seedspec describes today.
//
// A migration-content test that regenerates a frozen file's block from seedspec
// therefore compares LegacyPre0110(generated) with the file; a test that pins a
// key list into a frozen UPDATE translates each key with LegacyPre0110. The
// 0110 gate (TestMigration0110_RenamesEveryLegacyName) pins that 0110 carries
// every pair below, which is what keeps the two halves honest.
package seedspec

import (
	"strconv"
	"strings"
)

// LegacyNamePair maps a post-rename identifier to the name it had before 0110.
type LegacyNamePair struct {
	New string
	Old string
}

// LegacyNamesBefore0110 is ORDERED: longer/more specific names first so a
// replacement never eats a prefix of a later pair (price_key before skill_key,
// CamelCase before snake_case, the whole-word family before the bare stem).
var LegacyNamesBefore0110 = []LegacyNamePair{
	// ADR-254 D9 special case: the fog scout Skill becomes kg_explore; its
	// learner-facing display name is "Knowledge Explorer" (owner ruling
	// 2026-08-23 on the ADR-254 copy row); 0061 seeded it as 'Fog Scout'.
	{New: "companion_skill_kg_explore", Old: "familiar_skill_fog_scout"},
	{New: "Knowledge Explorer", Old: "Fog Scout"},
	{New: "KgExplore", Old: "FogScout"},
	{New: "kg_explore", Old: "fog_scout"},
	// The blanket rename, every casing (the frozen files never said "companion"
	// except the protected family literal below).
	{New: "Companions", Old: "Familiars"},
	{New: "companions", Old: "familiars"},
	{New: "COMPANIONS", Old: "FAMILIARS"},
	{New: "Companion", Old: "Familiar"},
	{New: "companion", Old: "familiar"},
	{New: "COMPANION", Old: "FAMILIAR"},
}

// legacyProtectedLiterals are SQL literals that already said "companion" BEFORE
// the rename and must survive the inverse map untouched: the Skill FAMILY value
// 'companion' (companion.FamilyCompanion, the duel_second family) predates
// ADR-254 and was never a Familiar name.
var legacyProtectedLiterals = []string{"'companion'"}

// LegacyPre0110 renders s as the frozen migrations spelled it. It is a plain
// ordered text substitution (no SQL awareness): it is only ever applied to
// seedspec-generated SQL and to catalogue keys, both of which are built from
// the identifiers above and nothing else.
func LegacyPre0110(s string) string {
	for i, lit := range legacyProtectedLiterals {
		s = strings.ReplaceAll(s, lit, "\x00LEGACY_KEEP_"+strconv.Itoa(i)+"\x00")
	}
	for _, p := range LegacyNamesBefore0110 {
		s = strings.ReplaceAll(s, p.New, p.Old)
	}
	for i, lit := range legacyProtectedLiterals {
		s = strings.ReplaceAll(s, "\x00LEGACY_KEEP_"+strconv.Itoa(i)+"\x00", lit)
	}
	return s
}

// LegacyKeys maps a slice of catalogue keys through LegacyPre0110.
func LegacyKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, LegacyPre0110(k))
	}
	return out
}
