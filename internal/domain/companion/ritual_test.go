// ritual_test.go — RED-first for CHO-2016 P4 (Rituals v1, ADR-219 D3 +
// spec pack docs/FAMILIAR-SKILL-SPECS-2026-07-03.md §5–7). Exercises the
// CompanionRitual DESIGNER aggregate: construction, grammar validation, the
// st4 unlock gate, composed-flat publish pricing, append-only revisions, the
// enabled-quota, soft-delete, and the ADR-215 learner-story redaction.
//
// These reference symbols that do NOT exist yet (Ritual, NewRitual,
// ComposePriceUnits, …) — the package fails to COMPILE, which is the RED
// signal (same convention as loadout_test.go was written RED-first for P0).
// GREEN slice G1 (ritual.go) makes it compile + pass. Do NOT add impl here.
package companion

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedClock is a deterministic Clock for revision timestamps.
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// ritualTestCatalogue is the shared platform-catalogue fixture (a slice of the
// Launch set). Shared with ritual_runner_test.go (same package). socratic_drill
// is seeded active=false to exercise the SKILL_NOT_ACTIVE guard; long_weaving
// is a craft passive (never a valid executable step).
func ritualTestCatalogue() map[string]CatalogEntry {
	return map[string]CatalogEntry{
		"explain_anew": {SkillKey: "explain_anew", Name: "Explain It Differently",
			SkillKind: SkillKindActive, Family: FamilyScholar, PolicyClass: PolicyStandard,
			OutputSink: SinkChat, ToolHandlerRefs: []string{"atom.search", "atom.cite"}, Active: true},
		"quiz_me": {SkillKey: "quiz_me", Name: "Quick Quiz",
			SkillKind: SkillKindActive, Family: FamilyScholar, PolicyClass: PolicyStandard,
			OutputSink: SinkChat, ToolHandlerRefs: []string{"bank.query"}, Active: true},
		"map_sight": {SkillKey: "map_sight", Name: "Map Sight",
			SkillKind: SkillKindActive, Family: FamilySight, PolicyClass: PolicyStandard,
			OutputSink: SinkChat, ToolHandlerRefs: []string{"kg.read_map"}, Active: true},
		"flashcard_forge": {SkillKey: "flashcard_forge", Name: "Flashcard Forge",
			SkillKind: SkillKindActive, Family: FamilyWeaver, PolicyClass: PolicyGenerative,
			OutputSink: SinkQuestionBank, ToolHandlerRefs: []string{"qgen.invoke"}, Active: true},
		"web_research": {SkillKey: "web_research", Name: "Far Sight",
			SkillKind: SkillKindActive, Family: FamilySeeker, PolicyClass: PolicyExternalEgress,
			OutputSink: SinkMemoryNote, ToolHandlerRefs: []string{"gateway.search", "kg.suggest"}, Active: true},
		"socratic_drill": {SkillKey: "socratic_drill", Name: "Socratic Drill",
			SkillKind: SkillKindActive, Family: FamilyScholar, PolicyClass: PolicyStandard,
			OutputSink: SinkChat, ToolHandlerRefs: []string{"bank.query"}, Active: false}, // eval-gate dark
		"long_weaving": {SkillKey: "long_weaving", Name: "Long Weaving",
			SkillKind: SkillKindCraft, Family: FamilyCraft, PolicyClass: PolicyStandard,
			OutputSink: "", ToolHandlerRefs: []string{}, Active: true},
	}
}

// ritualTestCaps is a st4 capability context: five equipped ACTIVE Skills, no
// craft grammar upgrades (StepCap 5, EnabledQuota 2), no other enabled rituals.
func ritualTestCaps() RitualCapabilityContext {
	return RitualCapabilityContext{
		Stage: 4,
		EquippedActiveSkills: map[string]bool{
			"explain_anew": true, "quiz_me": true, "map_sight": true,
			"flashcard_forge": true, "web_research": true,
		},
		HasLongWeaving:    false,
		HasTwinRituals:    false,
		HasWeaveMastery:   false,
		Catalogue:         ritualTestCatalogue(),
		OtherEnabledCount: 0,
	}
}

func steps(keys ...string) []RitualStep {
	out := make([]RitualStep, 0, len(keys))
	for _, k := range keys {
		out = append(out, RitualStep{SkillKey: k, Params: map[string]any{}})
	}
	return out
}

// ---- construction + field validation --------------------------------------

func TestNewRitualHappyPath(t *testing.T) {
	r, err := NewRitual("t-1", "fam-1", "Morning Review", TriggerManual, SinkChat)
	if err != nil {
		t.Fatalf("NewRitual: %v", err)
	}
	if r.RitualID == "" {
		t.Error("RitualID must be minted")
	}
	if r.Enabled {
		t.Error("a fresh ritual must be disabled until published + enabled")
	}
	if len(r.Revisions) != 0 || r.CurrentRevision != 0 {
		t.Error("a fresh ritual has no revisions")
	}
}

func TestNewRitualNameGuards(t *testing.T) {
	if _, err := NewRitual("t-1", "fam-1", "  ", TriggerManual, SinkChat); !errors.Is(err, ErrRitualNameRequired) {
		t.Errorf("blank name = %v, want ErrRitualNameRequired", err)
	}
	long := strings.Repeat("x", 61)
	if _, err := NewRitual("t-1", "fam-1", long, TriggerManual, SinkChat); !errors.Is(err, ErrRitualNameTooLong) {
		t.Errorf("61-char name = %v, want ErrRitualNameTooLong", err)
	}
}

func TestNewRitualTriggerGuards(t *testing.T) {
	if _, err := NewRitual("t-1", "fam-1", "R", RitualTrigger("whenever"), SinkChat); !errors.Is(err, ErrRitualUnknownTrigger) {
		t.Errorf("bogus trigger = %v, want ErrRitualUnknownTrigger", err)
	}
	// schedule/on_event are schema-valid but NOT runnable in v1 (P6 autonomy).
	if _, err := NewRitual("t-1", "fam-1", "R", TriggerSchedule, SinkChat); !errors.Is(err, ErrRitualTriggerNotV1) {
		t.Errorf("schedule trigger in v1 = %v, want ErrRitualTriggerNotV1", err)
	}
}

func TestNewRitualSinkGuard(t *testing.T) {
	if _, err := NewRitual("t-1", "fam-1", "R", TriggerManual, "webhook"); !errors.Is(err, ErrRitualUnknownSink) {
		t.Errorf("out-of-list sink = %v, want ErrRitualUnknownSink", err)
	}
	// every closed sink is accepted.
	for _, s := range []string{SinkChat, SinkMemoryNote, SinkSuggestionInbox, SinkQuestionBank, SinkNotification, SinkCalendarArtifact} {
		if _, err := NewRitual("t-1", "fam-1", "R", TriggerManual, s); err != nil {
			t.Errorf("closed sink %q rejected: %v", s, err)
		}
	}
}

// ---- st4 unlock gate ------------------------------------------------------

func TestRitualsUnlockedForStage(t *testing.T) {
	if RitualUnlockStage != 4 {
		t.Fatalf("RitualUnlockStage = %d, want 4 (growth.StageStructural)", RitualUnlockStage)
	}
	for s := 0; s < 4; s++ {
		if RitualsUnlockedForStage(s) {
			t.Errorf("stage %d must NOT unlock Rituals", s)
		}
	}
	for s := 4; s <= 6; s++ {
		if !RitualsUnlockedForStage(s) {
			t.Errorf("stage %d must unlock Rituals", s)
		}
	}
}

func TestPublishRefusedBeforeStructural(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()
	caps.Stage = 3 // awakened — one below structural
	if _, err := r.Publish(steps("explain_anew"), "allow", caps, fixedClock()); !errors.Is(err, ErrRitualUnlockStage) {
		t.Errorf("publish at st3 = %v, want ErrRitualUnlockStage", err)
	}
}

// ---- step grammar validation ----------------------------------------------

func TestValidateStepsEmpty(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	if err := r.ValidateSteps(nil, ritualTestCaps()); !errors.Is(err, ErrRitualNoSteps) {
		t.Errorf("0 steps = %v, want ErrRitualNoSteps", err)
	}
}

func TestValidateStepsCapDefault(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps() // StepCap 5
	if caps.StepCap() != 5 {
		t.Fatalf("StepCap = %d, want 5", caps.StepCap())
	}
	six := steps("explain_anew", "quiz_me", "map_sight", "explain_anew", "quiz_me", "map_sight")
	if err := r.ValidateSteps(six, caps); !errors.Is(err, ErrRitualTooManySteps) {
		t.Errorf("6 steps without long_weaving = %v, want ErrRitualTooManySteps", err)
	}
}

func TestValidateStepsCapLongWeaving(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()
	caps.HasLongWeaving = true // craft grant lifts the cap 5 → 8
	if caps.StepCap() != 8 {
		t.Fatalf("StepCap with long_weaving = %d, want 8", caps.StepCap())
	}
	eight := steps("explain_anew", "quiz_me", "map_sight", "explain_anew",
		"quiz_me", "map_sight", "explain_anew", "quiz_me")
	if err := r.ValidateSteps(eight, caps); err != nil {
		t.Errorf("8 steps WITH long_weaving = %v, want nil", err)
	}
	nine := append(eight, RitualStep{SkillKey: "map_sight"})
	if err := r.ValidateSteps(nine, caps); !errors.Is(err, ErrRitualTooManySteps) {
		t.Errorf("9 steps even with long_weaving = %v, want ErrRitualTooManySteps", err)
	}
}

func TestValidateStepsUnequippedSkill(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()
	// atom_forge is not in the equipped-active set.
	if err := r.ValidateSteps(steps("explain_anew", "atom_forge"), caps); !errors.Is(err, ErrRitualStepSkillNotEquipped) {
		t.Errorf("unequipped Skill step = %v, want ErrRitualStepSkillNotEquipped", err)
	}
	// craft Skills are never in EquippedActiveSkills → same guard fires.
	if err := r.ValidateSteps(steps("long_weaving"), caps); !errors.Is(err, ErrRitualStepSkillNotEquipped) {
		t.Errorf("craft Skill as a step = %v, want ErrRitualStepSkillNotEquipped", err)
	}
}

func TestValidateStepsInactiveCatalogue(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()
	// Learner somehow has socratic_drill equipped, but its catalogue row is dark
	// (active=false) — the eval gate hasn't flipped it. A step MUST refuse.
	caps.EquippedActiveSkills["socratic_drill"] = true
	if err := r.ValidateSteps(steps("socratic_drill"), caps); !errors.Is(err, ErrRitualStepSkillInactive) {
		t.Errorf("inactive-catalogue Skill step = %v, want ErrRitualStepSkillInactive", err)
	}
}

// ---- composed-flat publish pricing (§7) -----------------------------------

func TestComposePriceUnits(t *testing.T) {
	cat := ritualTestCatalogue()
	cases := []struct {
		name string
		keys []string
		want int
	}{
		{"base only (all standard)", []string{"explain_anew", "quiz_me", "map_sight"}, RitualBasePriceUnits},                // 20
		{"one generative", []string{"explain_anew", "flashcard_forge"}, RitualBasePriceUnits + RitualGenerativeUpliftUnits}, // 35
		{"one egress", []string{"web_research"}, RitualBasePriceUnits + RitualEgressUpliftUnits},                            // 60
		{"generative + egress", []string{"flashcard_forge", "web_research"},
			RitualBasePriceUnits + RitualGenerativeUpliftUnits + RitualEgressUpliftUnits}, // 75
	}
	for _, c := range cases {
		got, err := ComposePriceUnits(steps(c.keys...), cat)
		if err != nil {
			t.Fatalf("%s: ComposePriceUnits: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: price = %d, want %d", c.name, got, c.want)
		}
	}
}

// ---- publish appends immutable revisions + freezes price ------------------

func TestPublishAppendsRevisionAndFreezesPrice(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()

	rev1, err := r.Publish(steps("explain_anew", "flashcard_forge"), "allow", caps, fixedClock())
	if err != nil {
		t.Fatalf("Publish#1: %v", err)
	}
	if rev1.RevisionNo != 1 || r.CurrentRevision != 1 || len(r.Revisions) != 1 {
		t.Errorf("first publish: rev=%d current=%d n=%d, want 1/1/1", rev1.RevisionNo, r.CurrentRevision, len(r.Revisions))
	}
	if r.PublishedPriceUnits != RitualBasePriceUnits+RitualGenerativeUpliftUnits {
		t.Errorf("frozen price = %d, want 35", r.PublishedPriceUnits)
	}

	// A second publish appends rev 2 WITHOUT mutating rev 1 (append-only).
	rev2, err := r.Publish(steps("web_research"), "allow", caps, fixedClock())
	if err != nil {
		t.Fatalf("Publish#2: %v", err)
	}
	if rev2.RevisionNo != 2 || r.CurrentRevision != 2 || len(r.Revisions) != 2 {
		t.Errorf("second publish: rev=%d current=%d n=%d, want 2/2/2", rev2.RevisionNo, r.CurrentRevision, len(r.Revisions))
	}
	if r.Revisions[0].RevisionNo != 1 || len(r.Revisions[0].Steps) != 2 || r.Revisions[0].Steps[0].SkillKey != "explain_anew" {
		t.Error("append-only violated: revision 1 was mutated by the second publish")
	}
	if r.PublishedPriceUnits != RitualBasePriceUnits+RitualEgressUpliftUnits {
		t.Errorf("re-frozen price = %d, want 60", r.PublishedPriceUnits)
	}
}

// ---- enabled quota --------------------------------------------------------

func TestEnableQuota(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()
	if _, err := r.Publish(steps("explain_anew"), "allow", caps, fixedClock()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// One other ritual already enabled; base quota 2 → this one still fits.
	caps.OtherEnabledCount = 1
	if err := r.Enable(caps); err != nil {
		t.Fatalf("Enable within quota: %v", err)
	}
	if !r.Enabled {
		t.Error("ritual not enabled after Enable")
	}
	// Two others already enabled; base quota 2 → the 3rd is rejected.
	r2, _ := NewRitual("t-1", "fam-1", "R2", TriggerManual, SinkChat)
	_, _ = r2.Publish(steps("quiz_me"), "allow", caps, fixedClock())
	caps.OtherEnabledCount = 2
	if err := r2.Enable(caps); !errors.Is(err, ErrRitualEnabledQuotaReached) {
		t.Errorf("3rd enabled ritual without twin_rituals = %v, want ErrRitualEnabledQuotaReached", err)
	}
	// twin_rituals lifts the quota 2 → 4.
	caps.HasTwinRituals = true
	if caps.EnabledQuota() != 4 {
		t.Fatalf("EnabledQuota with twin_rituals = %d, want 4", caps.EnabledQuota())
	}
	if err := r2.Enable(caps); err != nil {
		t.Errorf("3rd enabled ritual WITH twin_rituals = %v, want nil", err)
	}
}

func TestEnableRequiresPublishedRevision(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	if err := r.Enable(ritualTestCaps()); err == nil {
		t.Error("enabling a never-published ritual must fail-loud")
	}
}

// ---- soft-delete ----------------------------------------------------------

func TestSoftDelete(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	r.SoftDelete(fixedClock())
	if r.DeletedAt == nil {
		t.Error("SoftDelete must set DeletedAt (never hard-delete)")
	}
}

func TestNewRitualRequiresIdentifiers(t *testing.T) {
	if _, err := NewRitual("", "fam-1", "R", TriggerManual, SinkChat); err == nil {
		t.Error("empty tenant_id must fail loud")
	}
	if _, err := NewRitual("t-1", "", "R", TriggerManual, SinkChat); err == nil {
		t.Error("empty companion_id must fail loud")
	}
}

func TestDisable(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	caps := ritualTestCaps()
	_, _ = r.Publish(steps("explain_anew"), "allow", caps, fixedClock())
	_ = r.Enable(caps)
	r.Disable()
	if r.Enabled {
		t.Error("Disable must clear Enabled")
	}
}

func TestCurrentStepsEmptyBeforePublish(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	if r.CurrentSteps() != nil {
		t.Error("CurrentSteps must be nil before any publish")
	}
}

// friendlyRunStatus (via the learner projection) covers every RunStatus.
func TestLearnerStoryFriendlyStatuses(t *testing.T) {
	want := map[RunStatus]string{
		RunStatusCompleted:     "done",
		RunStatusFailed:        "stopped early",
		RunStatusBlocked:       "stopped for safety",
		RunStatusSkippedBudget: "paused, top up mana",
		RunStatusRunning:       "running",
	}
	for st, label := range want {
		story := LearnerRunStory(&RitualRun{Status: st}, ritualTestCatalogue())
		if story.Status != label {
			t.Errorf("status %q → %q, want %q", st, story.Status, label)
		}
	}
}

// ---- ADR-215 learner-story projection (redaction invariant) ---------------

func TestLearnerRunStoryRedactsModelInternals(t *testing.T) {
	run := &RitualRun{
		RunID: "run-1", RitualID: "rit-1", RevisionNo: 1,
		Status: RunStatusCompleted, ManaCharged: 35,
		Stamps: []StepStamp{
			{StepIndex: 0, SkillKey: "explain_anew", PromptVersion: "explain_anew@v3",
				PromptHash: "sha256:DEADBEEFdeadbeef", ToolsInvoked: []string{"atom.search"},
				Citations: []string{"Fractions basics"}},
			{StepIndex: 1, SkillKey: "flashcard_forge", PromptVersion: "flashcard_forge@v2",
				PromptHash: "sha256:CAFEBABEcafebabe", ToolsInvoked: []string{"qgen.invoke"}, Citations: nil},
		},
	}
	story := LearnerRunStory(run, ritualTestCatalogue())

	if story.ManaCost != 35 {
		t.Errorf("story mana = %d, want 35", story.ManaCost)
	}
	if len(story.Steps) != 2 {
		t.Fatalf("story steps = %d, want 2", len(story.Steps))
	}
	// Steps read in learner language: the catalogue display Name, not the key.
	if story.Steps[0].Title != "Explain It Differently" {
		t.Errorf("step 0 title = %q, want the catalogue Name", story.Steps[0].Title)
	}
	// ADR-215 D5: NEVER surface prompt hashes/versions or raw tool ids to the learner.
	blob, _ := json.Marshal(story)
	for _, leak := range []string{"DEADBEEF", "CAFEBABE", "sha256:", "explain_anew@v3", "qgen.invoke"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("learner story leaked model internal %q: %s", leak, blob)
		}
	}
}
