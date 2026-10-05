// learner_weakness_test.go — pure-domain TDD for the Growth-Edge (LearnerWeakness)
// aggregate. Verifies construction + validation, concept-key normalisation,
// strength clamping, the dedup MERGE fold (source/tag union, strength take-max,
// descriptor append+cap+dedup, first-seen preservation, reactivation), the
// mastered->grown status rule, and soft-delete. No infra imports — clock is
// always passed in (hexagonal purity, mirrors topic_retention).
package learner_weakness

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var tNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

func baseInput() UpsertInput {
	return UpsertInput{
		TenantID:     "01970000-0000-7000-8000-000000000001",
		LearnerGCID:  "01970000-0000-7000-9000-000000000001",
		ConceptLabel: "Causes of Riverine Flooding",
		Embedding:    []float32{0.1, -0.2, 0.3},
		Category:     "physical-geography",
		Tags:         []string{"flooding", "causation"},
		Strength:     0.7,
		Source:       SourceExplicit,
		Descriptor: Descriptor{
			Summary:        "Confuses fluvial and pluvial flooding triggers.",
			Misconceptions: []string{"thinks all flooding is rainfall-driven"},
		},
		Now: tNow,
	}
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew_Success(t *testing.T) {
	w, err := New(baseInput())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.ID == "" {
		t.Error("expected a minted UUIDv7 id")
	}
	if w.ConceptKey != "causes-of-riverine-flooding" {
		t.Errorf("concept_key = %q; want derived slug", w.ConceptKey)
	}
	if w.Status != StatusActive {
		t.Errorf("status = %q; want active", w.Status)
	}
	if len(w.Sources) != 1 || w.Sources[0] != SourceExplicit {
		t.Errorf("sources = %v; want [explicit]", w.Sources)
	}
	if !w.FirstSeenAt.Equal(tNow) || !w.LastEvidencedAt.Equal(tNow) {
		t.Errorf("timestamps not seeded from Now: first=%v last=%v", w.FirstSeenAt, w.LastEvidencedAt)
	}
	if w.DeletedAt != nil {
		t.Error("fresh weakness must not be soft-deleted")
	}
}

func TestNew_HonoursExplicitConceptKey(t *testing.T) {
	in := baseInput()
	in.ConceptKey = "riverine-flood-causes"
	w, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.ConceptKey != "riverine-flood-causes" {
		t.Errorf("concept_key = %q; want explicit value preserved", w.ConceptKey)
	}
}

func TestNew_Validation(t *testing.T) {
	cases := map[string]func(*UpsertInput){
		"no tenant":  func(in *UpsertInput) { in.TenantID = " " },
		"no gcid":    func(in *UpsertInput) { in.LearnerGCID = "" },
		"no label":   func(in *UpsertInput) { in.ConceptLabel = "" },
		"no embed":   func(in *UpsertInput) { in.Embedding = nil },
		"bad source": func(in *UpsertInput) { in.Source = Source("bogus") },
		"empty slug": func(in *UpsertInput) { in.ConceptLabel = "!!!"; in.ConceptKey = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseInput()
			mut(&in)
			if _, err := New(in); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

func TestNew_ClampsStrength(t *testing.T) {
	in := baseInput()
	in.Strength = 1.8
	w, _ := New(in)
	if w.Strength != 1.0 {
		t.Errorf("strength = %v; want clamped to 1.0", w.Strength)
	}
	in.Strength = -0.3
	w, _ = New(in)
	if w.Strength != 0.0 {
		t.Errorf("strength = %v; want clamped to 0.0", w.Strength)
	}
}

func TestNew_MasteredIsGrown(t *testing.T) {
	in := baseInput()
	in.Strength = 0.05 // <= MasteredStrengthThreshold
	w, _ := New(in)
	if w.Status != StatusGrown {
		t.Errorf("status = %q; want grown for near-zero strength", w.Status)
	}
	if !w.IsGrown() {
		t.Error("IsGrown() = false; want true")
	}
}

func TestNew_CapsDescriptorAndCleansTags(t *testing.T) {
	in := baseInput()
	in.Tags = []string{"flooding", "Flooding", " flooding ", "", "causation"} // dups + blanks + case
	big := make([]string, MaxMisconceptions+5)
	for i := range big {
		big[i] = "m" + strings.Repeat("x", i+1)
	}
	in.Descriptor.Misconceptions = big
	w, _ := New(in)
	if len(w.Descriptor.Misconceptions) != MaxMisconceptions {
		t.Errorf("misconceptions len = %d; want capped to %d", len(w.Descriptor.Misconceptions), MaxMisconceptions)
	}
	// tags normalised: flooding + causation (case-folded, trimmed, deduped, blanks dropped)
	if len(w.Tags) != 2 {
		t.Errorf("tags = %v; want 2 unique cleaned tags", w.Tags)
	}
}

// ---------------------------------------------------------------------------
// NormalizeConceptKey
// ---------------------------------------------------------------------------

func TestNormalizeConceptKey(t *testing.T) {
	cases := map[string]string{
		"Causes of Riverine Flooding": "causes-of-riverine-flooding",
		"  multiple   spaces  ":       "multiple-spaces",
		"already-a-slug":              "already-a-slug",
		"Photosynthesis (C3 vs C4)!":  "photosynthesis-c3-vs-c4",
		"!!!":                         "",
		"UPPER_snake_Case":            "upper-snake-case",
	}
	for in, want := range cases {
		if got := NormalizeConceptKey(in); got != want {
			t.Errorf("NormalizeConceptKey(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestClampStrength(t *testing.T) {
	if ClampStrength(-1) != 0 {
		t.Error("clamp below 0")
	}
	if ClampStrength(2) != 1 {
		t.Error("clamp above 1")
	}
	if ClampStrength(0.42) != 0.42 {
		t.Error("clamp passes through in-range")
	}
}

// ---------------------------------------------------------------------------
// Merge (the dedup fold)
// ---------------------------------------------------------------------------

func TestMerge_FoldsEvidence(t *testing.T) {
	w, _ := New(baseInput())
	firstSeen := w.FirstSeenAt

	later := tNow.Add(48 * time.Hour)
	w.Merge(UpsertInput{
		ConceptLabel: "Causes of Riverine Flooding", // same concept
		Category:     "",                            // should not clobber existing
		TopicID:      "01970000-0000-7000-a000-000000000099",
		Tags:         []string{"causation", "rivers"}, // 1 new, 1 dup
		Strength:     0.9,                             // higher -> take max
		Source:       SourceDerived,                   // union
		Descriptor: Descriptor{
			Misconceptions:  []string{"thinks all flooding is rainfall-driven", "ignores snowmelt"},
			SuggestedAngles: []string{"compare fluvial vs pluvial"},
		},
		Now: later,
	})

	if w.Strength != 0.9 {
		t.Errorf("strength = %v; want take-max 0.9", w.Strength)
	}
	if !hasSource(w.Sources, SourceExplicit) || !hasSource(w.Sources, SourceDerived) {
		t.Errorf("sources = %v; want union of explicit+derived", w.Sources)
	}
	if countSource(w.Sources, SourceExplicit) != 1 {
		t.Errorf("sources has duplicate explicit: %v", w.Sources)
	}
	if w.Category != "physical-geography" {
		t.Errorf("category = %q; merge must not clobber existing with empty", w.Category)
	}
	if w.TopicID != "01970000-0000-7000-a000-000000000099" {
		t.Errorf("topic_id = %q; want filled from incoming when previously empty", w.TopicID)
	}
	if !containsTag(w.Tags, "rivers") || !containsTag(w.Tags, "causation") || !containsTag(w.Tags, "flooding") {
		t.Errorf("tags = %v; want union", w.Tags)
	}
	if countTag(w.Tags, "causation") != 1 {
		t.Errorf("tags has duplicate causation: %v", w.Tags)
	}
	// misconception dedup: "thinks all flooding..." already present, only "ignores snowmelt" is new
	if countMisc(w.Descriptor.Misconceptions, "thinks all flooding is rainfall-driven") != 1 {
		t.Errorf("misconception duplicated on merge: %v", w.Descriptor.Misconceptions)
	}
	if !containsStr(w.Descriptor.Misconceptions, "ignores snowmelt") {
		t.Errorf("new misconception not appended: %v", w.Descriptor.Misconceptions)
	}
	if !w.FirstSeenAt.Equal(firstSeen) {
		t.Errorf("first_seen_at changed on merge: %v", w.FirstSeenAt)
	}
	if !w.LastEvidencedAt.Equal(later) {
		t.Errorf("last_evidenced_at = %v; want bumped to merge time", w.LastEvidencedAt)
	}
}

func TestMerge_ReactivatesGrown(t *testing.T) {
	in := baseInput()
	in.Strength = 0.04 // starts grown
	w, _ := New(in)
	if w.Status != StatusGrown {
		t.Fatalf("precondition: want grown, got %q", w.Status)
	}
	w.Merge(UpsertInput{Strength: 0.6, Source: SourceClassroom, Now: tNow.Add(time.Hour)})
	if w.Status != StatusActive {
		t.Errorf("status = %q; want reactivated to active when strength rises", w.Status)
	}
}

func TestMerge_KeepsLowerStrengthWhenIncomingWeaker(t *testing.T) {
	w, _ := New(baseInput()) // strength 0.7
	w.Merge(UpsertInput{Strength: 0.3, Source: SourceDerived, Now: tNow.Add(time.Hour)})
	if w.Strength != 0.7 {
		t.Errorf("strength = %v; want take-max keeps 0.7", w.Strength)
	}
}

// ---------------------------------------------------------------------------
// SoftDelete
// ---------------------------------------------------------------------------

func TestSoftDelete(t *testing.T) {
	w, _ := New(baseInput())
	del := tNow.Add(time.Hour)
	w.SoftDelete(del)
	if w.DeletedAt == nil || !w.DeletedAt.Equal(del) {
		t.Errorf("DeletedAt = %v; want %v", w.DeletedAt, del)
	}
	if !w.UpdatedAt.Equal(del) {
		t.Errorf("UpdatedAt = %v; want bumped on soft-delete", w.UpdatedAt)
	}
}

func TestNew_ZeroNowUsesWallClock(t *testing.T) {
	in := baseInput()
	in.Now = time.Time{}
	w, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.FirstSeenAt.IsZero() || w.LastEvidencedAt.IsZero() {
		t.Error("zero Now should fall back to wall-clock, not stay zero")
	}
}

func TestNew_CapsSampleWrongAndPerItem(t *testing.T) {
	in := baseInput()
	sw := make([]SampleWrong, 0, MaxSampleWrong+3)
	for i := 0; i < MaxSampleWrong+2; i++ {
		sw = append(sw, SampleWrong{Prompt: fmt.Sprintf("q%d", i), WhyWrong: "off-by-one"})
	}
	sw = append(sw, sw[0]) // duplicate prompt+whyWrong
	in.Descriptor.SampleWrong = sw

	pi := make([]ItemCorrectness, 0, MaxPerItemCorrectness+3)
	for i := 0; i < MaxPerItemCorrectness+2; i++ {
		pi = append(pi, ItemCorrectness{Item: fmt.Sprintf("item-%d", i), Correct: i%2 == 0})
	}
	pi = append(pi, ItemCorrectness{Item: "item-0", Correct: false}) // dup item key
	in.Descriptor.PerItemCorrectness = pi

	w, _ := New(in)
	if len(w.Descriptor.SampleWrong) != MaxSampleWrong {
		t.Errorf("sample_wrong len = %d; want capped to %d", len(w.Descriptor.SampleWrong), MaxSampleWrong)
	}
	if len(w.Descriptor.PerItemCorrectness) != MaxPerItemCorrectness {
		t.Errorf("per_item len = %d; want capped to %d", len(w.Descriptor.PerItemCorrectness), MaxPerItemCorrectness)
	}
}

func TestMerge_AppendsSampleWrongAndPerItem(t *testing.T) {
	w, _ := New(baseInput())
	w.Merge(UpsertInput{
		Source: SourceClassroom,
		Now:    tNow.Add(time.Hour),
		Descriptor: Descriptor{
			Summary:            "should not overwrite existing summary",
			SampleWrong:        []SampleWrong{{Prompt: "q-flood", WhyWrong: "picked pluvial"}},
			PerItemCorrectness: []ItemCorrectness{{Item: "Q1", Correct: false}},
		},
	})
	if len(w.Descriptor.SampleWrong) != 1 || w.Descriptor.SampleWrong[0].Prompt != "q-flood" {
		t.Errorf("sample_wrong not appended on merge: %#v", w.Descriptor.SampleWrong)
	}
	if len(w.Descriptor.PerItemCorrectness) != 1 || w.Descriptor.PerItemCorrectness[0].Item != "Q1" {
		t.Errorf("per_item not appended on merge: %#v", w.Descriptor.PerItemCorrectness)
	}
	if w.Descriptor.Summary != "Confuses fluvial and pluvial flooding triggers." {
		t.Errorf("summary clobbered on merge: %q", w.Descriptor.Summary)
	}
}

func TestMerge_IgnoresInvalidSource(t *testing.T) {
	w, _ := New(baseInput())
	before := len(w.Sources)
	w.Merge(UpsertInput{Source: Source("bogus"), Strength: 0.8, Now: tNow.Add(time.Hour)})
	if len(w.Sources) != before {
		t.Errorf("invalid source mutated sources: %v", w.Sources)
	}
}

func TestMerge_DoesNotClobberSetTopicOrZeroNow(t *testing.T) {
	in := baseInput()
	in.TopicID = "01970000-0000-7000-a000-000000000001"
	w, _ := New(in)
	w.Merge(UpsertInput{
		TopicID: "01970000-0000-7000-a000-000000000002", // different — must NOT clobber
		Source:  SourceDerived,
		Now:     time.Time{}, // zero -> wall-clock fallback
	})
	if w.TopicID != "01970000-0000-7000-a000-000000000001" {
		t.Errorf("topic_id clobbered: %q", w.TopicID)
	}
	if w.LastEvidencedAt.IsZero() {
		t.Error("zero Now on merge should fall back to wall-clock")
	}
}

// ---------------------------------------------------------------------------
// TargetConceptID (ADR-238 — goal-scoped Diagnose; mirrors TopicID treatment)
// ---------------------------------------------------------------------------

func TestNew_RoundTripsTargetConceptID(t *testing.T) {
	in := baseInput()
	in.TargetConceptID = "01970000-0000-7000-b000-000000000001"
	w, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.TargetConceptID != "01970000-0000-7000-b000-000000000001" {
		t.Errorf("target_concept_id = %q; want round-tripped from input", w.TargetConceptID)
	}
}

func TestNew_TargetConceptIDDefaultsEmpty(t *testing.T) {
	w, err := New(baseInput()) // no TargetConceptID set
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.TargetConceptID != "" {
		t.Errorf("target_concept_id = %q; want empty (UNMATCHED) when unset", w.TargetConceptID)
	}
}

func TestMerge_FillsTargetConceptIDWhenEmpty(t *testing.T) {
	w, _ := New(baseInput()) // target_concept_id empty
	if w.TargetConceptID != "" {
		t.Fatalf("precondition: want empty target_concept_id, got %q", w.TargetConceptID)
	}
	w.Merge(UpsertInput{
		TargetConceptID: "01970000-0000-7000-b000-000000000002",
		Source:          SourceDerived,
		Now:             tNow.Add(time.Hour),
	})
	if w.TargetConceptID != "01970000-0000-7000-b000-000000000002" {
		t.Errorf("target_concept_id = %q; want filled from incoming when previously empty", w.TargetConceptID)
	}
}

func TestMerge_DoesNotClobberSetTargetConceptID(t *testing.T) {
	in := baseInput()
	in.TargetConceptID = "01970000-0000-7000-b000-000000000001"
	w, _ := New(in)
	w.Merge(UpsertInput{
		TargetConceptID: "01970000-0000-7000-b000-000000000099", // different — must NOT clobber
		Source:          SourceDerived,
		Now:             tNow.Add(time.Hour),
	})
	if w.TargetConceptID != "01970000-0000-7000-b000-000000000001" {
		t.Errorf("target_concept_id clobbered on merge: %q; want the existing value preserved", w.TargetConceptID)
	}
}

// --- tiny test helpers (avoid importing slices for readability) ---

func hasSource(ss []Source, s Source) bool { return countSource(ss, s) > 0 }
func countSource(ss []Source, s Source) (n int) {
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return
}
func containsTag(ts []string, t string) bool { return countTag(ts, t) > 0 }
func countTag(ts []string, t string) (n int) {
	for _, x := range ts {
		if x == t {
			n++
		}
	}
	return
}
func containsStr(ss []string, s string) bool { return countMisc(ss, s) > 0 }
func countMisc(ss []string, s string) (n int) {
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return
}
