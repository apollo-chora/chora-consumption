// dose_composer_v2_test.go — CHO-2045 / ADR-224: daily-dose composer v2.
//
// Evolves the SELECTION stage (not the 5-atom budget, not the generation
// contract) with three composable changes, all byte-identical when the learner
// has one KG / one subject (the degenerate case):
//
//  1. Subject/KG allocation — bucket the eligible weakness pool by subject
//     (learner_weakness.category); at most one weakness slot per subject when
//     >=2 subjects have eligible edges. Curiosity likewise spreads across the
//     learner's active-path topics.
//  2. Weighted-random sampling within each bucket, seeded by hash(date, gcid)
//     so the dose is stable within a day (mana idempotency intact) but varies
//     day-to-day and reaches the long tail.
//  3. (sub-phase B) per-KG include/exclude preference filter.
//
// Everything here is gated behind DailyDoseInput.ComposerV2; the zero value
// (false) runs the v1 path unchanged, so every existing dose test is untouched.
package companion

import (
	"fmt"
	"testing"
	"time"
)

var v2Day = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

// v2Seeds — a multi-subject atom universe. Cached-drill resolution is by AtomID,
// so each edge below deterministically resolves to a known atom.
func v2Seeds() []AtomSeed {
	return []AtomSeed{
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-frac-2", Topic: "Fractions", Title: "Fractions II"},
		{AtomID: "atom-mult-1", Topic: "Multiplication Tables", Title: "Times Tables"},
		{AtomID: "atom-sci-1", Topic: "Photosynthesis", Title: "Plants"},
		{AtomID: "atom-eng-1", Topic: "Grammar", Title: "Tenses"},
	}
}

func doseEqual(a, b DailyDose) bool {
	if a.Message != b.Message || len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			return false
		}
	}
	return true
}

func curiosityEntries(d DailyDose) []DailyDoseEntry {
	var out []DailyDoseEntry
	for _, e := range d.Entries {
		if e.DoseReason == DoseReasonCuriosity {
			out = append(out, e)
		}
	}
	return out
}

func entryAtomIDs(es []DailyDoseEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.AtomID
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// catOfAtom maps a served atom back to the subject (category) of the edge that
// drilled it, via that edge's cached drill atoms.
func catOfAtom(atomID string, edges []GrowthEdgeInput) string {
	for _, e := range edges {
		for _, a := range e.CachedDrillAtomIDs {
			if a == atomID {
				return e.Category
			}
		}
	}
	return ""
}

// ── DoseSeed — deterministic per (day, gcid) ─────────────────────────────────

func TestDoseSeed_StableAndVaries(t *testing.T) {
	if DoseSeed("2026-07-06", "g1") != DoseSeed("2026-07-06", "g1") {
		t.Fatal("DoseSeed must be stable for the same (day, gcid)")
	}
	if DoseSeed("2026-07-06", "g1") == DoseSeed("2026-07-07", "g1") {
		t.Fatal("DoseSeed must differ day-to-day")
	}
	if DoseSeed("2026-07-06", "g1") == DoseSeed("2026-07-06", "g2") {
		t.Fatal("DoseSeed must differ across learners")
	}
}

// ── #1 regression guard: single-subject dose is byte-identical to v1 ─────────

func TestComposeV2_SingleSubject_ByteIdenticalToV1(t *testing.T) {
	edges := []GrowthEdgeInput{
		{EdgeID: "e1", ConceptKey: "fractions", Category: "Math", Strength: 0.8, CachedDrillAtomIDs: []string{"atom-frac-1"}},
		{EdgeID: "e2", ConceptKey: "multiplication", Category: "Math", Strength: 0.6, CachedDrillAtomIDs: []string{"atom-mult-1"}},
		{EdgeID: "e3", ConceptKey: "fractions-2", Category: "Math", Strength: 0.5, CachedDrillAtomIDs: []string{"atom-frac-2"}},
	}
	base := DailyDoseInput{Seeds: v2Seeds(), States: map[string]SM2State{}, GrowthEdges: edges, Now: v2Day}

	v1 := base
	v1.ComposerV2 = false

	v2 := base
	v2.ComposerV2 = true
	v2.RandSeed = DoseSeed("2026-07-06", "gcid-1")

	if !doseEqual(ComposeDailyDose(v1), ComposeDailyDose(v2)) {
		t.Fatalf("single-subject v2 not byte-identical to v1:\n v1=%+v\n v2=%+v",
			ComposeDailyDose(v1).Entries, ComposeDailyDose(v2).Entries)
	}
}

// ── #1 guard also holds for a single curiosity topic ─────────────────────────

func TestComposeV2_SingleCuriosityTopic_ByteIdenticalToV1(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "c-a1", Topic: "Algebra", Title: "A1"},
		{AtomID: "c-a2", Topic: "Algebra", Title: "A2"},
	}
	base := DailyDoseInput{
		Seeds:            seeds,
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"Algebra": true},
		Now:              v2Day,
	}
	v1 := base
	v1.ComposerV2 = false
	v2 := base
	v2.ComposerV2 = true
	v2.RandSeed = DoseSeed("2026-07-06", "gcid-1")

	if !doseEqual(ComposeDailyDose(v1), ComposeDailyDose(v2)) {
		t.Fatalf("single-topic curiosity v2 not byte-identical to v1")
	}
}

// ── Cross-subject balance: <=1 weakness slot per subject when >=2 eligible ───

func TestComposeV2_TwoSubjects_OnePerSubject(t *testing.T) {
	edges := []GrowthEdgeInput{
		{EdgeID: "m1", ConceptKey: "fractions", Category: "Math", Strength: 0.85, CachedDrillAtomIDs: []string{"atom-frac-1"}},
		{EdgeID: "m2", ConceptKey: "mult", Category: "Math", Strength: 0.80, CachedDrillAtomIDs: []string{"atom-mult-1"}},
		{EdgeID: "s1", ConceptKey: "photosynthesis", Category: "Science", Strength: 0.60, CachedDrillAtomIDs: []string{"atom-sci-1"}},
	}
	in := DailyDoseInput{
		Seeds: v2Seeds(), States: map[string]SM2State{}, GrowthEdges: edges, Now: v2Day,
		ComposerV2: true, RandSeed: DoseSeed("2026-07-06", "gcid-1"),
	}
	weak := weaknessEntries(ComposeDailyDose(in))
	if len(weak) != DoseWeaknessCount {
		t.Fatalf("want %d weakness picks, got %d: %+v", DoseWeaknessCount, len(weak), weak)
	}
	ids := entryAtomIDs(weak)
	// Both Math edges are shakier than Science, but the spread cap forces the two
	// slots to span two subjects — so Science IS represented, and exactly one of
	// the two Math atoms appears (never both, since neither is critical).
	if !containsStr(ids, "atom-sci-1") {
		t.Fatalf("Science must be represented under the spread cap, got %v", ids)
	}
	mathCount := 0
	if containsStr(ids, "atom-frac-1") {
		mathCount++
	}
	if containsStr(ids, "atom-mult-1") {
		mathCount++
	}
	if mathCount != 1 {
		t.Fatalf("want exactly 1 Math atom (>=2 subjects => <=1 slot/subject), got %d in %v", mathCount, ids)
	}
}

// ── A fresh multi-subject learner (ALL edges maximally shaky) STILL spreads ───
// The urgency override is for urgency CONCENTRATED in one subject — not for a
// learner shaky across many subjects at once (the exact monoculture case v2
// exists to fix). Regression guard for the live daleleung76 data (all 1.0).

func TestComposeV2_AllCriticalMultiSubject_StillSpreads(t *testing.T) {
	// Math has TWO critical edges — under the old "any critical ⇒ v1" override the
	// two globally-shakiest (both Math) would take both slots (monoculture). With
	// ≥2 critical subjects the spread must win.
	edges := []GrowthEdgeInput{
		{EdgeID: "m1", ConceptKey: "math-a", Category: "Math", Strength: 1.0, CachedDrillAtomIDs: []string{"atom-frac-1"}},
		{EdgeID: "m2", ConceptKey: "math-b", Category: "Math", Strength: 1.0, CachedDrillAtomIDs: []string{"atom-frac-2"}},
		{EdgeID: "s", ConceptKey: "sci", Category: "Science", Strength: 0.95, CachedDrillAtomIDs: []string{"atom-sci-1"}},
		{EdgeID: "e", ConceptKey: "eng", Category: "English", Strength: 0.95, CachedDrillAtomIDs: []string{"atom-eng-1"}},
	}
	in := DailyDoseInput{
		Seeds: v2Seeds(), States: map[string]SM2State{}, GrowthEdges: edges, Now: v2Day,
		ComposerV2: true, RandSeed: DoseSeed("2026-07-06", "gcid-1"),
	}
	weak := weaknessEntries(ComposeDailyDose(in))
	if len(weak) != DoseWeaknessCount {
		t.Fatalf("want %d weakness picks, got %d", DoseWeaknessCount, len(weak))
	}
	cats := map[string]bool{}
	for _, e := range weak {
		cats[catOfAtom(e.AtomID, edges)] = true
	}
	if len(cats) != 2 {
		t.Fatalf("all-critical multi-subject dose must STILL spread across 2 subjects, got %+v", cats)
	}
}

// ── Critical-edge urgency override: may take BOTH slots despite the spread ────

func TestComposeV2_CriticalEdge_TakesBothSlots(t *testing.T) {
	edges := []GrowthEdgeInput{
		// Two critically-shaky Math edges (Strength >= CriticalWeaknessStrength).
		{EdgeID: "m1", ConceptKey: "fractions", Category: "Math", Strength: 0.97, CachedDrillAtomIDs: []string{"atom-frac-1"}},
		{EdgeID: "m2", ConceptKey: "mult", Category: "Math", Strength: 0.93, CachedDrillAtomIDs: []string{"atom-mult-1"}},
		{EdgeID: "s1", ConceptKey: "photosynthesis", Category: "Science", Strength: 0.60, CachedDrillAtomIDs: []string{"atom-sci-1"}},
	}
	in := DailyDoseInput{
		Seeds: v2Seeds(), States: map[string]SM2State{}, GrowthEdges: edges, Now: v2Day,
		ComposerV2: true, RandSeed: DoseSeed("2026-07-06", "gcid-1"),
	}
	weak := weaknessEntries(ComposeDailyDose(in))
	if len(weak) != DoseWeaknessCount {
		t.Fatalf("want %d weakness picks, got %d: %+v", DoseWeaknessCount, len(weak), weak)
	}
	ids := entryAtomIDs(weak)
	if !(containsStr(ids, "atom-frac-1") && containsStr(ids, "atom-mult-1")) {
		t.Fatalf("critical Math subject must take BOTH slots (urgency override), got %v", ids)
	}
}

// ── Sampling determinism: same seed => identical dose ────────────────────────

func TestComposeV2_SameSeed_Deterministic(t *testing.T) {
	in := threeSubjectInput(DoseSeed("2026-07-06", "gcid-1"))
	if !doseEqual(ComposeDailyDose(in), ComposeDailyDose(in)) {
		t.Fatal("same seed must yield an identical dose (deterministic-per-day)")
	}
}

// ── Day-to-day variation over a 2-week window ────────────────────────────────

func TestComposeV2_DifferentDay_CanDiffer(t *testing.T) {
	day0 := ComposeDailyDose(threeSubjectInput(DoseSeed("2026-07-06", "gcid-1")))
	differ := false
	for d := 7; d <= 20; d++ {
		day := fmt.Sprintf("2026-07-%02d", d)
		if !doseEqual(day0, ComposeDailyDose(threeSubjectInput(DoseSeed(day, "gcid-1")))) {
			differ = true
			break
		}
	}
	if !differ {
		t.Fatal("expected day-to-day dose variation across 2 weeks (weighted sampling)")
	}
}

// ── Weighted sampling: shakiest most likely, long tail still reached ─────────

func TestComposeV2_WeightedSampling_ShakiestMostLikely_LongTailReached(t *testing.T) {
	counts := map[string]int{}
	const trials = 600
	edges := threeSubjectEdges()
	for i := 0; i < trials; i++ {
		in := threeSubjectInput(int64(i)*2654435761 + 1)
		for _, e := range weaknessEntries(ComposeDailyDose(in)) {
			counts[catOfAtom(e.AtomID, edges)]++
		}
	}
	if !(counts["Math"] > counts["Science"] && counts["Science"] > counts["English"]) {
		t.Fatalf("want Math > Science > English pick frequency, got %+v", counts)
	}
	if counts["English"] == 0 {
		t.Fatalf("long tail (English, weakest subject) never surfaced over %d trials", trials)
	}
}

// ── Budget: never more than 5 atoms ──────────────────────────────────────────

func TestComposeV2_NeverExceedsBudget(t *testing.T) {
	in := threeSubjectInput(DoseSeed("2026-07-06", "gcid-1"))
	in.ActivePathTopics = map[string]bool{"Fractions": true, "Grammar": true}
	if got := len(ComposeDailyDose(in).Entries); got > DoseTargetSize {
		t.Fatalf("dose exceeded the %d-atom budget: %d", DoseTargetSize, got)
	}
}

// ── Fail-soft: subject-known / KG-unknown (empty category) edges still drill ──

func TestComposeV2_EmptyCategory_StillDrilled(t *testing.T) {
	// Upload-born edges may have a subject but no map node; the allocator must
	// bucket them as a virtual "unmapped" KG and never drop them.
	edges := []GrowthEdgeInput{
		{EdgeID: "u1", ConceptKey: "x", Category: "", Strength: 0.80, CachedDrillAtomIDs: []string{"atom-frac-1"}},
		{EdgeID: "u2", ConceptKey: "y", Category: "", Strength: 0.70, CachedDrillAtomIDs: []string{"atom-mult-1"}},
	}
	in := DailyDoseInput{
		Seeds: v2Seeds(), States: map[string]SM2State{}, GrowthEdges: edges, Now: v2Day,
		ComposerV2: true, RandSeed: DoseSeed("2026-07-06", "gcid-1"),
	}
	if got := len(weaknessEntries(ComposeDailyDose(in))); got == 0 {
		t.Fatal("empty-category (unmapped) edges must still be drilled, never dropped")
	}
}

// ── Curiosity spreads across the learner's active-path topics ────────────────

func TestComposeV2_Curiosity_SpreadsAcrossTopics(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "c-a1", Topic: "Algebra", Title: "A1"},
		{AtomID: "c-a2", Topic: "Algebra", Title: "A2"},
		{AtomID: "c-b1", Topic: "Biology", Title: "B1"},
		{AtomID: "c-b2", Topic: "Biology", Title: "B2"},
	}
	in := DailyDoseInput{
		Seeds:            seeds,
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"Algebra": true, "Biology": true},
		Now:              v2Day,
		ComposerV2:       true,
		RandSeed:         DoseSeed("2026-07-06", "gcid-1"),
	}
	cur := curiosityEntries(ComposeDailyDose(in))
	topics := map[string]int{}
	for _, e := range cur {
		topics[e.Topic]++
	}
	if topics["Algebra"] != 1 || topics["Biology"] != 1 {
		t.Fatalf("want curiosity spread of 1 Algebra + 1 Biology, got %+v", topics)
	}
}

// ── shared 3-subject builders (Math 0.8 > Science 0.5 > English 0.2) ──────────

func threeSubjectEdges() []GrowthEdgeInput {
	return []GrowthEdgeInput{
		{EdgeID: "m", ConceptKey: "math", Category: "Math", Strength: 0.80, CachedDrillAtomIDs: []string{"atom-frac-1"}},
		{EdgeID: "s", ConceptKey: "sci", Category: "Science", Strength: 0.50, CachedDrillAtomIDs: []string{"atom-sci-1"}},
		{EdgeID: "e", ConceptKey: "eng", Category: "English", Strength: 0.20, CachedDrillAtomIDs: []string{"atom-eng-1"}},
	}
}

func threeSubjectInput(seed int64) DailyDoseInput {
	return DailyDoseInput{
		Seeds:       v2Seeds(),
		States:      map[string]SM2State{},
		GrowthEdges: threeSubjectEdges(),
		Now:         v2Day,
		ComposerV2:  true,
		RandSeed:    seed,
	}
}
