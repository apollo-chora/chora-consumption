// companion_pure_helpers_cover_test.go — white-box table-driven tests for the
// deterministic pure helpers in companion_handlers.go. These functions carry
// real branch logic (category mapping, XP tables, composition math, name→UUID
// resolution, level-up nudge templating) and each test would catch a real
// regression in that mapping.
package http

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestDoseReasonToCategory(t *testing.T) {
	cases := []struct {
		in   companion.DoseReason
		want string
	}{
		{companion.DoseReasonEbbinghausReview, "review"},
		{companion.DoseReasonWeakness, "stretch"},
		{companion.DoseReasonFresh, "new"},
		{companion.DoseReasonCuriosity, "new"},
		{companion.DoseReason("garbage"), "new"}, // default branch
	}
	for _, c := range cases {
		if got := doseReasonToCategory(c.in); got != c.want {
			t.Errorf("doseReasonToCategory(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestXPForCategory(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"review", 20},
		{"stretch", 50},
		{"new", 30},
		{"anything-else", 30}, // default
	}
	for _, c := range cases {
		if got := xpForCategory(c.in); got != c.want {
			t.Errorf("xpForCategory(%q) = %d; want %d", c.in, got, c.want)
		}
	}
}

func TestComposeDoseComposition(t *testing.T) {
	t.Run("empty returns zero composition", func(t *testing.T) {
		got := composeDoseComposition(nil)
		if got != (dailyDoseComposition{}) {
			t.Errorf("empty = %+v; want zero", got)
		}
	})

	t.Run("40/40/20-ish split with truncating division", func(t *testing.T) {
		entries := []companion.DailyDoseEntry{
			{DoseReason: companion.DoseReasonEbbinghausReview}, // review
			{DoseReason: companion.DoseReasonEbbinghausReview}, // review
			{DoseReason: companion.DoseReasonFresh},            // new
			{DoseReason: companion.DoseReasonCuriosity},        // new
			{DoseReason: companion.DoseReasonWeakness},         // stretch
		}
		got := composeDoseComposition(entries)
		// total=5 → review 2/5=40, new 2/5=40, stretch 1/5=20 (integer division).
		if got.ReviewPercent != 40 || got.NewPercent != 40 || got.StretchPercent != 20 {
			t.Errorf("got %+v; want {40 40 20}", got)
		}
		// Sum must be <= 100 per the doc contract.
		if got.ReviewPercent+got.NewPercent+got.StretchPercent > 100 {
			t.Errorf("sum %d > 100", got.ReviewPercent+got.NewPercent+got.StretchPercent)
		}
	})

	t.Run("truncation can sum to < 100", func(t *testing.T) {
		// 3 categories each 1/3 → 33+33+33 = 99 (integer floor).
		entries := []companion.DailyDoseEntry{
			{DoseReason: companion.DoseReasonEbbinghausReview},
			{DoseReason: companion.DoseReasonFresh},
			{DoseReason: companion.DoseReasonWeakness},
		}
		got := composeDoseComposition(entries)
		if got.ReviewPercent != 33 || got.NewPercent != 33 || got.StretchPercent != 33 {
			t.Errorf("got %+v; want {33 33 33}", got)
		}
	})
}

func TestCourseCodeFromTopic(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "T-LEARN"},
		{"   ", "T-LEARN"},
		{"scrum", "T-SCRUM"},
		{"agile basics", "T-AGILE-BASICS"},
		// >16 chars after uppercasing is truncated to 16 then prefixed.
		{"verylongtopicnamehere", "T-" + "VERYLONGTOPICNAM"},
	}
	for _, c := range cases {
		if got := courseCodeFromTopic(c.in); got != c.want {
			t.Errorf("courseCodeFromTopic(%q) = %q; want %q", c.in, got, c.want)
		}
		if len(strings.TrimPrefix(courseCodeFromTopic(c.in), "T-")) > 16 {
			t.Errorf("courseCodeFromTopic(%q) body exceeds 16 chars", c.in)
		}
	}
}

func TestComposeDeterministicGreeting(t *testing.T) {
	cases := []struct {
		name  string
		level int
		want  string
	}{
		{"", 1, "Eira here. Let's tackle today's dose."},                // default name + level<=1
		{"Newton", 1, "Newton here. Let's tackle today's dose."},        // level<=1
		{"Newton", 0, "Newton here. Let's tackle today's dose."},        // level<=1 boundary
		{"Curie", 5, "Curie here, Level 5 and ready for today's dose."}, // level>1
	}
	for _, c := range cases {
		if got := composeDeterministicGreeting(c.name, c.level); got != c.want {
			t.Errorf("composeDeterministicGreeting(%q,%d) = %q; want %q", c.name, c.level, got, c.want)
		}
	}
}

func TestComposeDeterministicNarrative(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := composeDeterministicNarrative(nil)
		if !strings.Contains(got, "Explore the discovery graph") {
			t.Errorf("empty narrative = %q", got)
		}
	})
	t.Run("all three buckets present", func(t *testing.T) {
		entries := []companion.DailyDoseEntry{
			{DoseReason: companion.DoseReasonEbbinghausReview},
			{DoseReason: companion.DoseReasonFresh},
			{DoseReason: companion.DoseReasonWeakness},
		}
		got := composeDeterministicNarrative(entries)
		for _, want := range []string{"revisit 1 from earlier", "explore 1 new", "stretch on 1 weak spots"} {
			if !strings.Contains(got, want) {
				t.Errorf("narrative %q missing %q", got, want)
			}
		}
	})
	t.Run("only review bucket", func(t *testing.T) {
		entries := []companion.DailyDoseEntry{
			{DoseReason: companion.DoseReasonEbbinghausReview},
			{DoseReason: companion.DoseReasonEbbinghausReview},
		}
		got := composeDeterministicNarrative(entries)
		if !strings.Contains(got, "revisit 2 from earlier") {
			t.Errorf("narrative %q missing review part", got)
		}
		if strings.Contains(got, "explore") || strings.Contains(got, "stretch") {
			t.Errorf("narrative %q should only mention review", got)
		}
	})
}

func TestCompanionNameToCanonicalID(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Curie", stubCompanionIDCurie},
		{"curie", stubCompanionIDCurie},
		{"  LOVELACE ", stubCompanionIDLovelace},
		{"Newton", stubCompanionIDNewton},
		{"Eira", stubCompanionIDNewton}, // unknown → Newton default
		{"", stubCompanionIDNewton},     // empty → Newton default
	}
	for _, c := range cases {
		if got := companionNameToCanonicalID(c.name); got != c.want {
			t.Errorf("companionNameToCanonicalID(%q) = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestPickCompanionID(t *testing.T) {
	t.Run("nil server falls back to Newton", func(t *testing.T) {
		if got := pickCompanionID(nil, "tnt", "gcid"); got != stubCompanionIDNewton {
			t.Errorf("nil server = %q; want Newton", got)
		}
	})
	t.Run("unknown companion falls back to Newton", func(t *testing.T) {
		s := NewServer()
		// No companion bonded → GetByGCID errors → Newton fallback.
		if got := pickCompanionID(s, "tnt", "no-such-gcid"); got != stubCompanionIDNewton {
			t.Errorf("unbonded = %q; want Newton", got)
		}
	})
	t.Run("bonded named companion resolves via name map", func(t *testing.T) {
		s := NewServer()
		f, _ := companion.New("tnt", "gcid-1", "Curie")
		s.Companions.Save(f)
		if got := pickCompanionID(s, "tnt", "gcid-1"); got != stubCompanionIDCurie {
			t.Errorf("bonded Curie = %q; want %q", got, stubCompanionIDCurie)
		}
	})
}

func TestXPForGrade(t *testing.T) {
	cases := []struct {
		in   companion.Grade
		want int
	}{
		{companion.GradeIRemember, 100},
		{companion.GradeNotSure, 50},
		{companion.GradeForgot, 10},
		{companion.Grade("bogus"), 0}, // default
	}
	for _, c := range cases {
		if got := xpForGrade(c.in); got != c.want {
			t.Errorf("xpForGrade(%q) = %d; want %d", c.in, got, c.want)
		}
	}
}

func TestNudgeForLevelUp(t *testing.T) {
	cases := []struct {
		name  string
		level int
		trait string
		want  string
	}{
		{"Eira", 5, "Pattern Recognition", "Eira leveled up to 5! New trait: Pattern Recognition"},
		{"Eira", 3, "", "Eira leveled up to 3!"},                             // no trait
		{"", 2, "Focus", "Your Companion leveled up to 2! New trait: Focus"}, // default name
	}
	for _, c := range cases {
		if got := nudgeForLevelUp(c.name, c.level, c.trait); got != c.want {
			t.Errorf("nudgeForLevelUp(%q,%d,%q) = %q; want %q", c.name, c.level, c.trait, got, c.want)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{42, "42"},
		{1000, "1000"},
		{-5, "-5"},
		{-12345, "-12345"},
	}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Errorf("itoa(%d) = %q; want %q", c.in, got, c.want)
		}
	}
}
