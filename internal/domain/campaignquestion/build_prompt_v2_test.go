package campaignquestion

import (
	"strings"
	"testing"
)

// ADR-247 Cap A: BuildPromptV2 enriches the campaign question prompt with the
// learner's goal framing, per-node sub-goal, and diagnosed weakness. With an
// empty PromptContext it MUST degrade byte-for-byte to today's BuildPrompt.
func TestBuildPromptV2_EmptyContextEqualsV1(t *testing.T) {
	v1 := BuildPrompt("story-point calculation", "story-point-calculation", "application", QuestionsPerRequest)
	v2 := BuildPromptV2("story-point calculation", "story-point-calculation", "application", QuestionsPerRequest, PromptContext{})
	if v1 != v2 {
		t.Errorf("empty PromptContext must equal v1.\n v1=%q\n v2=%q", v1, v2)
	}
}

func TestBuildPromptV2_GoalAndAncestorsFraming(t *testing.T) {
	got := BuildPromptV2("u-substitution", "u-substitution", "application", 2, PromptContext{
		GoalTitle: "Calculus",
		Ancestors: []string{"Definite Integrals", "Integration"},
	})
	if !strings.Contains(got, "Calculus") {
		t.Errorf("prompt must name the goal Calculus: %q", got)
	}
	if !strings.Contains(got, "Definite Integrals") || !strings.Contains(got, "Integration") {
		t.Errorf("prompt must name the ancestor chain: %q", got)
	}
	// the base MCQ / single-correct contract survives.
	if !strings.Contains(got, "multiple-choice") || !strings.Contains(got, "one correct option") {
		t.Errorf("prompt must keep the base MCQ contract: %q", got)
	}
}

func TestBuildPromptV2_SubGoalObjective(t *testing.T) {
	got := BuildPromptV2("Limits", "limits", "comprehension", 2, PromptContext{
		SubGoal: "explain why a limit can exist where the function is undefined",
	})
	if !strings.Contains(got, "explain why a limit can exist where the function is undefined") {
		t.Errorf("prompt must carry the sub-goal objective: %q", got)
	}
}

func TestBuildPromptV2_WeaknessDescriptorAndEvidence(t *testing.T) {
	got := BuildPromptV2("Fractions", "fractions", "application", 2, PromptContext{
		Weakness: &WeaknessInput{
			Descriptor:     "confuses numerator and denominator when adding unlike fractions",
			Misconceptions: []string{"adds denominators directly"},
			Evidence:       []string{"1/2 + 1/3 = 2/5: added tops and bottoms"},
		},
	})
	if !strings.Contains(got, "confuses numerator and denominator when adding unlike fractions") {
		t.Errorf("prompt must carry the weakness descriptor: %q", got)
	}
	if !strings.Contains(got, "adds denominators directly") {
		t.Errorf("prompt must carry the misconception: %q", got)
	}
	if !strings.Contains(got, "1/2 + 1/3 = 2/5") {
		t.Errorf("prompt must carry the evidence: %q", got)
	}
}

// A blank-fielded weakness pointer must not inject an empty "diagnosis found:"
// stub (it degrades like no weakness).
func TestBuildPromptV2_BlankWeaknessDoesNotInject(t *testing.T) {
	base := BuildPrompt("Fractions", "fractions", "application", 2)
	got := BuildPromptV2("Fractions", "fractions", "application", 2, PromptContext{Weakness: &WeaknessInput{}})
	if got != base {
		t.Errorf("a blank weakness must degrade to v1.\n base=%q\n got=%q", base, got)
	}
}
