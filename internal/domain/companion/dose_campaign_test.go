// dose_campaign_test.go — CHO-2081 / ADR-227 D12 (WS-C2): dose campaign axis.
//
// Composer v2 gains a campaign slot type: on a campaign day the caps become
// campaign 2 · review 1 · weakness 1 · curiosity 1 (+ cascade top-up), the
// campaign slot serves the focused goal's focus node at the rung decided by
// the campaign grader, and with N goals the seeded daily RNG picks ONE
// campaign to advance (weighted by due-ness/recency). Everything stays
// deterministic (ADR-202 — the LLM never schedules) and degenerates
// byte-identically when no campaign exists, mirroring the v2 guard.
package companion

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// campEbbDue builds an SM2 state that is unambiguously due at v2Day
// (EF 1.3 × 1-day interval, last reviewed 10 days ago ⇒ decay ≈ 1 > 0.4).
func campEbbDue(atomID string) SM2State {
	return SM2State{
		AtomID:         atomID,
		Repetitions:    1,
		IntervalDays:   1,
		EasinessFactor: 1.3,
		LastReviewedAt: v2Day.Add(-10 * 24 * time.Hour),
		NextReviewAt:   v2Day.Add(-9 * 24 * time.Hour),
	}
}

func campaignEntries(d DailyDose) []DailyDoseEntry {
	var out []DailyDoseEntry
	for _, e := range d.Entries {
		if e.DoseReason == DoseReasonCampaign {
			out = append(out, e)
		}
	}
	return out
}

// campaignDayInput builds a campaign-day input with rich, DISJOINT pools for
// every slot so cap assertions are unambiguous:
//   - campaign: camp-1/camp-2 via explicit candidates
//   - ebbinghaus: ebb-1 due
//   - weakness: threeSubjectEdges (cached drills atom-frac-1/sci-1/eng-1)
//   - curiosity: cur-1 (Geometry on the active path)
func campaignDayInput(seed int64) DailyDoseInput {
	seeds := []AtomSeed{
		{AtomID: "camp-1", Topic: "Addition", Title: "Addition I"},
		{AtomID: "camp-2", Topic: "Addition", Title: "Addition II"},
		{AtomID: "ebb-1", Topic: "History", Title: "H1"},
		{AtomID: "cur-1", Topic: "Geometry", Title: "G1"},
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-sci-1", Topic: "Photosynthesis", Title: "Plants"},
		{AtomID: "atom-eng-1", Topic: "Grammar", Title: "Tenses"},
	}
	return DailyDoseInput{
		Seeds:            seeds,
		States:           map[string]SM2State{"ebb-1": campEbbDue("ebb-1")},
		ActivePathTopics: map[string]bool{"Geometry": true},
		GrowthEdges:      threeSubjectEdges(),
		Now:              v2Day,
		ComposerV2:       true,
		RandSeed:         seed,
		Campaign: &CampaignInput{
			GoalID:           "goal-1",
			ConceptID:        "concept-uuid-1",
			ConceptKey:       "addition",
			ConceptLabel:     "Addition",
			Rung:             3,
			IsRefresher:      false,
			CandidateAtomIDs: []string{"camp-1", "camp-2"},
		},
	}
}

// ── Degeneration guards (owner-locked pins) ──────────────────────────────────

// No campaign ⇒ the v2 caps stay 2/2/2 and no campaign entry/JSON key appears.
func TestComposeCampaign_NilCampaign_KeepsV2Shape(t *testing.T) {
	in := threeSubjectInput(DoseSeed("2026-07-06", "gcid-1"))
	in.ActivePathTopics = map[string]bool{"Fractions": true, "Grammar": true}
	dose := ComposeDailyDose(in)

	if got := len(campaignEntries(dose)); got != 0 {
		t.Fatalf("nil campaign must yield zero campaign entries, got %d", got)
	}
	if got := len(weaknessEntries(dose)); got != DoseWeaknessCount {
		t.Fatalf("nil campaign must keep the v2 weakness cap %d, got %d", DoseWeaknessCount, got)
	}
	if got := len(curiosityEntries(dose)); got != DoseCuriosityCount {
		t.Fatalf("nil campaign must keep the v2 curiosity cap %d, got %d", DoseCuriosityCount, got)
	}
}

// The v1 path ignores a campaign input entirely (campaign is a v2 feature).
func TestComposeCampaign_V1Flag_CampaignIgnored(t *testing.T) {
	base := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	base.ComposerV2 = false

	noCampaign := base
	noCampaign.Campaign = nil

	if !doseEqual(ComposeDailyDose(base), ComposeDailyDose(noCampaign)) {
		t.Fatal("v1 path must ignore Campaign input (byte-identical to v1 without it)")
	}
}

// Focused practice (Phase 2A) wins over the campaign axis — the focused
// bypass composes exactly as if no campaign existed.
func TestComposeCampaign_FocusedPractice_WinsOverCampaign(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	in.FocusEdgeID = "m" // threeSubjectEdges: Math edge, cached atom-frac-1

	noCampaign := in
	noCampaign.Campaign = nil

	if !doseEqual(ComposeDailyDose(in), ComposeDailyDose(noCampaign)) {
		t.Fatal("focused practice must bypass the campaign axis unchanged")
	}
}

// The composed-dose JSON grows NO new keys when no campaign exists — the
// degeneration is byte-identical on the wire, not just structurally.
func TestComposeCampaign_JSON_NoCampaignKeysWhenAbsent(t *testing.T) {
	in := threeSubjectInput(DoseSeed("2026-07-06", "gcid-1"))
	dose := ComposeDailyDose(in)

	raw, err := json.Marshal(dose)
	if err != nil {
		t.Fatalf("marshal dose: %v", err)
	}
	if strings.Contains(string(raw), "campaign") {
		t.Fatalf("nil-campaign dose JSON must carry no campaign keys, got %s", raw)
	}

	breakdown, err := json.Marshal(BuildAtomBreakdown(dose.Entries))
	if err != nil {
		t.Fatalf("marshal breakdown: %v", err)
	}
	if strings.Contains(string(breakdown), "campaign") {
		t.Fatalf("nil-campaign breakdown JSON must carry no campaign key, got %s", breakdown)
	}
}

// ── Campaign-day composition (ADR-227 D12 caps 2/1/1/1 + cascade) ────────────

func TestComposeCampaign_CampaignDay_Caps2111(t *testing.T) {
	dose := ComposeDailyDose(campaignDayInput(DoseSeed("2026-07-06", "gcid-1")))

	if got := len(dose.Entries); got != DoseTargetSize {
		t.Fatalf("campaign day with rich pools must fill the %d budget, got %d: %+v",
			DoseTargetSize, got, dose.Entries)
	}
	b := BuildAtomBreakdown(dose.Entries)
	if b.Campaign != DoseCampaignCount {
		t.Fatalf("want %d campaign picks, got %d (%+v)", DoseCampaignCount, b.Campaign, dose.Entries)
	}
	if b.Ebbinghaus != CampaignDayEbbinghausCount {
		t.Fatalf("campaign day must cap ebbinghaus at %d, got %d", CampaignDayEbbinghausCount, b.Ebbinghaus)
	}
	if b.Weakness != CampaignDayWeaknessCount {
		t.Fatalf("campaign day must cap weakness at %d, got %d", CampaignDayWeaknessCount, b.Weakness)
	}
	if b.Curiosity != CampaignDayCuriosityCount {
		t.Fatalf("campaign day must cap curiosity at %d, got %d", CampaignDayCuriosityCount, b.Curiosity)
	}
	// The march leads the dose: campaign picks come first.
	for i := 0; i < DoseCampaignCount; i++ {
		if dose.Entries[i].DoseReason != DoseReasonCampaign {
			t.Fatalf("entry %d must be a campaign pick, got %s", i, dose.Entries[i].DoseReason)
		}
	}
}

func TestComposeCampaign_MetadataStamped(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	in.Campaign.IsRefresher = true
	camp := campaignEntries(ComposeDailyDose(in))
	if len(camp) == 0 {
		t.Fatal("expected campaign entries")
	}
	for _, e := range camp {
		if e.CampaignGoalID != "goal-1" || e.CampaignConceptID != "concept-uuid-1" ||
			e.CampaignConceptKey != "addition" || e.CampaignRung != 3 || !e.CampaignRefresher {
			t.Fatalf("campaign metadata not stamped: %+v", e)
		}
	}
	// Non-campaign entries stay clean.
	for _, e := range ComposeDailyDose(in).Entries {
		if e.DoseReason != DoseReasonCampaign && e.CampaignGoalID != "" {
			t.Fatalf("non-campaign entry carries campaign metadata: %+v", e)
		}
	}
}

// Explicit candidates resolve first (in order); the slug fallback fills the
// remainder from seeds whose topic matches the focus concept, AtomID order.
func TestComposeCampaign_CandidatesFirst_ThenSlugFallback(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	in.Seeds = append(in.Seeds, AtomSeed{AtomID: "add-a", Topic: "Addition", Title: "A"})
	in.Campaign.CandidateAtomIDs = []string{"camp-2"} // one explicit candidate only

	camp := campaignEntries(ComposeDailyDose(in))
	ids := entryAtomIDs(camp)
	if len(ids) != DoseCampaignCount || ids[0] != "camp-2" {
		t.Fatalf("want explicit candidate first, got %v", ids)
	}
	// Fallback picks the slug-matched seed with the smallest AtomID that is
	// not already picked: add-a < camp-1.
	if ids[1] != "add-a" {
		t.Fatalf("want slug fallback add-a second, got %v", ids)
	}
}

func TestComposeCampaign_LabelSlugAlsoMatches(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	in.Campaign.ConceptKey = "addition-facts" // no topic matches this key
	in.Campaign.ConceptLabel = "Addition"     // …but the label slug matches
	in.Campaign.CandidateAtomIDs = nil

	ids := entryAtomIDs(campaignEntries(ComposeDailyDose(in)))
	if len(ids) != 2 || ids[0] != "camp-1" || ids[1] != "camp-2" {
		t.Fatalf("label slug must drive the fallback, got %v", ids)
	}
}

// A short campaign pool fails soft: the cascade + top-up still fill the budget.
func TestComposeCampaign_ShortPool_CascadesToBudget(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	in.Campaign.CandidateAtomIDs = []string{"camp-1"}
	// Remove the slug-matched atoms so exactly ONE campaign atom is servable.
	seeds := in.Seeds[:0:0]
	for _, s := range in.Seeds {
		if s.AtomID == "camp-2" {
			continue
		}
		seeds = append(seeds, s)
	}
	in.Seeds = seeds
	in.Campaign.ConceptKey = "no-such-topic"
	in.Campaign.ConceptLabel = "No Such Topic"

	dose := ComposeDailyDose(in)
	if got := BuildAtomBreakdown(dose.Entries).Campaign; got != 1 {
		t.Fatalf("want 1 servable campaign pick, got %d", got)
	}
	if got := len(dose.Entries); got != DoseTargetSize {
		t.Fatalf("cascade must still fill the %d budget, got %d: %+v", DoseTargetSize, got, dose.Entries)
	}
}

func TestComposeCampaign_NeverExceedsCapOrBudget(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("flood-%02d", i)
		in.Seeds = append(in.Seeds, AtomSeed{AtomID: id, Topic: "Addition", Title: id})
		in.Campaign.CandidateAtomIDs = append(in.Campaign.CandidateAtomIDs, id)
	}
	dose := ComposeDailyDose(in)
	if got := BuildAtomBreakdown(dose.Entries).Campaign; got > DoseCampaignCount {
		t.Fatalf("campaign cap %d exceeded: %d", DoseCampaignCount, got)
	}
	if got := len(dose.Entries); got > DoseTargetSize {
		t.Fatalf("budget %d exceeded: %d", DoseTargetSize, got)
	}
}

// Same seed ⇒ identical campaign-day dose (deterministic within a day).
func TestComposeCampaign_SameSeed_Deterministic(t *testing.T) {
	in := campaignDayInput(DoseSeed("2026-07-06", "gcid-1"))
	if !doseEqual(ComposeDailyDose(in), ComposeDailyDose(in)) {
		t.Fatal("same-seed campaign dose must be identical")
	}
}

func TestBuildAtomBreakdown_CountsCampaign(t *testing.T) {
	entries := []DailyDoseEntry{
		{AtomID: "a", DoseReason: DoseReasonCampaign},
		{AtomID: "b", DoseReason: DoseReasonCampaign},
		{AtomID: "c", DoseReason: DoseReasonFresh},
	}
	b := BuildAtomBreakdown(entries)
	if b.Campaign != 2 || b.Fresh != 1 {
		t.Fatalf("breakdown must count campaign picks, got %+v", b)
	}
}

// ── One-campaign-per-day rotation (ADR-227 D12) ──────────────────────────────

func rotationCandidates() []CampaignCandidate {
	cold, warm, mid := 0.1, 0.9, 0.5
	return []CampaignCandidate{
		{GoalID: "goal-a", ConceptID: "ca", ConceptKey: "addition", RetentionR: &cold, DaysSinceAdvance: 7},
		{GoalID: "goal-b", ConceptID: "cb", ConceptKey: "biology", RetentionR: &warm, DaysSinceAdvance: 0},
		{GoalID: "goal-c", ConceptID: "cc", ConceptKey: "chemistry", RetentionR: &mid, DaysSinceAdvance: 3},
	}
}

func TestPickCampaignGoal_Empty_False(t *testing.T) {
	if _, ok := PickCampaignGoal(nil, 42); ok {
		t.Fatal("no candidates must yield ok=false")
	}
}

func TestPickCampaignGoal_Single_Returned(t *testing.T) {
	one := rotationCandidates()[:1]
	got, ok := PickCampaignGoal(one, 42)
	if !ok || got.GoalID != "goal-a" {
		t.Fatalf("single candidate must be picked, got %+v ok=%v", got, ok)
	}
}

func TestPickCampaignGoal_Deterministic_And_OrderIndependent(t *testing.T) {
	seed := DoseSeed("2026-07-06", "gcid-1")
	cands := rotationCandidates()
	first, ok := PickCampaignGoal(cands, seed)
	if !ok {
		t.Fatal("expected a pick")
	}
	again, _ := PickCampaignGoal(cands, seed)
	if first.GoalID != again.GoalID {
		t.Fatal("same seed must pick the same goal")
	}
	// Shuffled input order must not change the pick (sorted internally).
	shuffled := []CampaignCandidate{cands[2], cands[0], cands[1]}
	reordered, _ := PickCampaignGoal(shuffled, seed)
	if reordered.GoalID != first.GoalID {
		t.Fatalf("candidate order must not affect the pick: %s vs %s", reordered.GoalID, first.GoalID)
	}
}

func TestPickCampaignGoal_DayToDay_Rotates(t *testing.T) {
	cands := rotationCandidates()
	day0, _ := PickCampaignGoal(cands, DoseSeed("2026-07-06", "gcid-1"))
	rotated := false
	for d := 7; d <= 20; d++ {
		day := fmt.Sprintf("2026-07-%02d", d)
		if got, _ := PickCampaignGoal(cands, DoseSeed(day, "gcid-1")); got.GoalID != day0.GoalID {
			rotated = true
			break
		}
	}
	if !rotated {
		t.Fatal("expected the daily pick to rotate across a 2-week window")
	}
}

// Weighted by due-ness/recency: the cold+stale goal leads, the warm+fresh
// goal trails, and every goal stays reachable (fat tail).
func TestPickCampaignGoal_WeightedByDuenessAndRecency(t *testing.T) {
	counts := map[string]int{}
	const trials = 600
	for i := 0; i < trials; i++ {
		got, ok := PickCampaignGoal(rotationCandidates(), int64(i)*2654435761+1)
		if !ok {
			t.Fatal("expected a pick")
		}
		counts[got.GoalID]++
	}
	if !(counts["goal-a"] > counts["goal-c"] && counts["goal-c"] > counts["goal-b"]) {
		t.Fatalf("want cold+stale > mid > warm+fresh pick frequency, got %+v", counts)
	}
	if counts["goal-b"] == 0 {
		t.Fatalf("warm goal never surfaced over %d trials (fat tail broken): %+v", trials, counts)
	}
}

// Unknown retention (no row yet) is mid-weighted — never dropped, never dominant.
func TestPickCampaignGoal_NilRetention_MidWeighted(t *testing.T) {
	cold := 0.1
	cands := []CampaignCandidate{
		{GoalID: "goal-cold", ConceptKey: "k1", RetentionR: &cold, DaysSinceAdvance: 0},
		{GoalID: "goal-new", ConceptKey: "k2", RetentionR: nil, DaysSinceAdvance: 0},
	}
	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		got, _ := PickCampaignGoal(cands, int64(i)*7919+3)
		counts[got.GoalID]++
	}
	if counts["goal-new"] == 0 {
		t.Fatalf("nil-retention goal must stay reachable, got %+v", counts)
	}
	if counts["goal-cold"] <= counts["goal-new"] {
		t.Fatalf("colder retention must outweigh unknown retention, got %+v", counts)
	}
}

// campaignWeight clamps out-of-range retention and saturates staleness at a
// week, so a poisoned retention read can never produce a negative or runaway
// weight (fail-soft ranking).
func TestCampaignWeight_ClampsAndSaturates(t *testing.T) {
	under, zero := -1.0, 0.0
	if campaignWeight(CampaignCandidate{RetentionR: &under}) != campaignWeight(CampaignCandidate{RetentionR: &zero}) {
		t.Fatal("retention below 0 must clamp to 0")
	}
	over, one := 2.0, 1.0
	if campaignWeight(CampaignCandidate{RetentionR: &over}) != campaignWeight(CampaignCandidate{RetentionR: &one}) {
		t.Fatal("retention above 1 must clamp to 1")
	}
	if campaignWeight(CampaignCandidate{DaysSinceAdvance: 30}) != campaignWeight(CampaignCandidate{DaysSinceAdvance: 7}) {
		t.Fatal("staleness must saturate at 7 days")
	}
}
