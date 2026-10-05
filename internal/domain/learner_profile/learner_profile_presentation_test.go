package learner_profile

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// uuidRE mirrors the leak shape the presenter must never emit to a learner.
const sampleCourseUUID = "05000000-0000-7000-8000-0000000c5301"

func containsUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

func TestSanitizeLearnerText_stripsUUID(t *testing.T) {
	cases := map[string]string{
		"Enrolled in course " + sampleCourseUUID: "Enrolled in course",
		sampleCourseUUID:                         "",
		"no uuid here":                           "no uuid here",
		"Earned a certification (course " + strings.ToUpper(sampleCourseUUID) + ")": "Earned a certification (course )",
	}
	for in, want := range cases {
		if got := SanitizeLearnerText(in); got != want {
			t.Errorf("SanitizeLearnerText(%q) = %q, want %q", in, got, want)
		}
		if containsUUID(SanitizeLearnerText(in)) {
			t.Errorf("SanitizeLearnerText(%q) still contains a UUID", in)
		}
	}
}

func TestFactDisplayLabel_neverLeaksUUID(t *testing.T) {
	certLabel := sampleCourseUUID // a cert fact's label IS the course UUID (the leak)
	cases := []struct {
		name     string
		f        *Fact
		resolved string
		want     string
	}{
		// --- no resolved name: EXISTING behavior must be byte-identical ---
		{"enrollment empty label", &Fact{Type: FactEnrollment, RefID: sampleCourseUUID}, "", "a course"},
		{"course completed empty label", &Fact{Type: FactCourseCompleted, RefID: sampleCourseUUID}, "", "a course"},
		{"path completed empty label", &Fact{Type: FactPathCompleted, RefID: sampleCourseUUID}, "", "a learning path"},
		{"cert label is a UUID", &Fact{Type: FactCertificationIssued, RefID: sampleCourseUUID, Detail: Detail{Label: certLabel}}, "", "a certification"},
		{"assessment empty label", &Fact{Type: FactAssessmentGraded, RefID: sampleCourseUUID}, "", "an assessment"},
		{"preference key+value", &Fact{Type: FactPreference, RefID: "dose_excluded_topics", Detail: Detail{Label: "mathematics"}}, "", "dose_excluded_topics: mathematics"},
		{"preference key embeds a UUID (must strip)", &Fact{Type: FactPreference, RefID: "dose.map.019f2de5-a2ca-7ea7-bc08-671a36597a4b"}, "", "dose.map."},
		{"human label passes through", &Fact{Type: FactCourseCompleted, RefID: sampleCourseUUID, Detail: Detail{Label: "Algebra I"}}, "", "Algebra I"},
		// --- resolved name wins (non-UUID) over the generic noun / cert UUID ---
		{"resolved name wins for enrollment", &Fact{Type: FactEnrollment, RefID: sampleCourseUUID}, "Algebra I", "Algebra I"},
		{"resolved name wins for completed course", &Fact{Type: FactCourseCompleted, RefID: sampleCourseUUID}, "Data Science 101", "Data Science 101"},
		{"resolved name wins for certification", &Fact{Type: FactCertificationIssued, RefID: sampleCourseUUID, Detail: Detail{Label: certLabel}}, "Cloud Practitioner", "Cloud Practitioner"},
		// --- a UUID resolvedName is ignored → falls back to generic noun ---
		{"uuid resolvedName ignored for enrollment", &Fact{Type: FactEnrollment, RefID: sampleCourseUUID}, sampleCourseUUID, "a course"},
		{"uuid resolvedName ignored for cert", &Fact{Type: FactCertificationIssued, RefID: sampleCourseUUID, Detail: Detail{Label: certLabel}}, strings.ToUpper(sampleCourseUUID), "a certification"},
		// --- resolvedName is sanitized (embedded UUID token stripped) ---
		{"resolved name sanitized", &Fact{Type: FactEnrollment, RefID: sampleCourseUUID}, "Algebra " + sampleCourseUUID, "Algebra"},
	}
	for _, c := range cases {
		got := FactDisplayLabel(c.f, c.resolved)
		if got != c.want {
			t.Errorf("%s: FactDisplayLabel = %q, want %q", c.name, got, c.want)
		}
		if containsUUID(got) {
			t.Errorf("%s: FactDisplayLabel leaked a UUID: %q", c.name, got)
		}
	}
	if got := FactDisplayLabel(nil, ""); got != "" {
		t.Errorf("FactDisplayLabel(nil) = %q, want empty", got)
	}
	// A nil fact with a resolved name still returns empty (nil guard first).
	if got := FactDisplayLabel(nil, "Algebra I"); got != "" {
		t.Errorf("FactDisplayLabel(nil, resolved) = %q, want empty", got)
	}
}

func TestActivityDisplay_regeneratesFromKind_noLeak(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		kind    string
		summary string // legacy summary baked with the raw UUID
		want    string
	}{
		{"enrolled", "Enrolled in course " + sampleCourseUUID, "Enrolled in a course"},
		{"completed_course", "Completed course " + sampleCourseUUID, "Completed a course"},
		{"earned_certification", "Earned a certification (course " + sampleCourseUUID + ")", "Earned a certification"},
		{"scored_assessment", "Was graded on assessment submission " + sampleCourseUUID, "Was graded on an assessment"},
		{"completed_path", "Completed learning path " + sampleCourseUUID, "Completed a learning path"},
	}
	for _, c := range cases {
		a := &ActivityEntry{Kind: c.kind, Summary: c.summary, RefID: sampleCourseUUID, OccurredAt: now}
		got := ActivityDisplay(a, "")
		if got != c.want {
			t.Errorf("kind=%s: ActivityDisplay = %q, want %q", c.kind, got, c.want)
		}
		if containsUUID(got) {
			t.Errorf("kind=%s: ActivityDisplay leaked a UUID: %q", c.kind, got)
		}
	}
	// Unknown kind → sanitized summary, still leak-free.
	unknown := &ActivityEntry{Kind: "mystery", Summary: "did a thing with " + sampleCourseUUID, OccurredAt: now}
	if got := ActivityDisplay(unknown, ""); containsUUID(got) {
		t.Errorf("unknown kind leaked a UUID: %q", got)
	}
	if got := ActivityDisplay(nil, ""); got != "" {
		t.Errorf("ActivityDisplay(nil) = %q, want empty", got)
	}
}

func TestActivityDisplay_resolvedNameWins(t *testing.T) {
	now := time.Now().UTC()
	// A resolved course name replaces the generic activity line (the
	// [activity: enrolled] tag carries the verb context at the call site).
	enrolled := &ActivityEntry{Kind: "enrolled", RefID: sampleCourseUUID, OccurredAt: now}
	if got := ActivityDisplay(enrolled, "Algebra I"); got != "Algebra I" {
		t.Errorf("resolved name should win for enrolled: got %q, want Algebra I", got)
	}
	completed := &ActivityEntry{Kind: "completed_course", RefID: sampleCourseUUID, OccurredAt: now}
	if got := ActivityDisplay(completed, "Data Science 101"); got != "Data Science 101" {
		t.Errorf("resolved name should win for completed_course: got %q, want Data Science 101", got)
	}
	// A UUID resolvedName is ignored → falls back to the generic line.
	if got := ActivityDisplay(enrolled, sampleCourseUUID); got != "Enrolled in a course" {
		t.Errorf("uuid resolvedName must be ignored: got %q, want Enrolled in a course", got)
	}
	if containsUUID(ActivityDisplay(enrolled, sampleCourseUUID)) {
		t.Error("uuid resolvedName leaked through ActivityDisplay")
	}
}

func TestSafeFactRef_dropsRawUUID(t *testing.T) {
	if got := SafeFactRef(&Fact{Type: FactEnrollment, RefID: sampleCourseUUID}); got != "" {
		t.Errorf("SafeFactRef(enrollment UUID) = %q, want empty", got)
	}
	if got := SafeFactRef(&Fact{Type: FactPreference, RefID: "dose_excluded_topics"}); got != "dose_excluded_topics" {
		t.Errorf("SafeFactRef(preference key) = %q, want the human key", got)
	}
}

func TestCourseIDForFact(t *testing.T) {
	cases := []struct {
		name string
		f    *Fact
		want string
	}{
		{"enrollment → RefID", &Fact{Type: FactEnrollment, RefID: sampleCourseUUID}, sampleCourseUUID},
		{"course_completed → RefID", &Fact{Type: FactCourseCompleted, RefID: sampleCourseUUID}, sampleCourseUUID},
		{"certification → Detail.Label", &Fact{Type: FactCertificationIssued, RefID: "cert-id", Detail: Detail{Label: sampleCourseUUID}}, sampleCourseUUID},
		{"path_completed → none", &Fact{Type: FactPathCompleted, RefID: sampleCourseUUID}, ""},
		{"assessment → none", &Fact{Type: FactAssessmentGraded, RefID: sampleCourseUUID}, ""},
		{"preference → none", &Fact{Type: FactPreference, RefID: "dose_excluded_topics"}, ""},
		{"nil → none", nil, ""},
	}
	for _, c := range cases {
		if got := CourseIDForFact(c.f); got != c.want {
			t.Errorf("%s: CourseIDForFact = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCourseIDForActivity(t *testing.T) {
	cases := []struct {
		name string
		a    *ActivityEntry
		want string
	}{
		{"enrolled → RefID", &ActivityEntry{Kind: "enrolled", RefID: sampleCourseUUID}, sampleCourseUUID},
		{"completed_course → RefID", &ActivityEntry{Kind: "completed_course", RefID: sampleCourseUUID}, sampleCourseUUID},
		{"earned_certification → none", &ActivityEntry{Kind: "earned_certification", RefID: sampleCourseUUID}, ""},
		{"scored_assessment → none", &ActivityEntry{Kind: "scored_assessment", RefID: sampleCourseUUID}, ""},
		{"nil → none", nil, ""},
	}
	for _, c := range cases {
		if got := CourseIDForActivity(c.a); got != c.want {
			t.Errorf("%s: CourseIDForActivity = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCollectCourseIDs_dedupesAndDropsEmpties(t *testing.T) {
	facts := []*Fact{
		{Type: FactEnrollment, RefID: sampleCourseUUID},
		{Type: FactCourseCompleted, RefID: sampleCourseUUID}, // dup of the above course
		{Type: FactCertificationIssued, Detail: Detail{Label: "05000000-0000-7000-8000-0000000c5302"}},
		{Type: FactPathCompleted, RefID: "05000000-0000-7000-8000-0000000c5399"}, // not a course → dropped
		{Type: FactPreference, RefID: "dose_excluded_topics"},                    // not a course → dropped
		nil,
	}
	activity := []*ActivityEntry{
		{Kind: "enrolled", RefID: "05000000-0000-7000-8000-0000000c5303"},
		{Kind: "completed_course", RefID: sampleCourseUUID}, // dup again
		{Kind: "earned_certification", RefID: "should-not-collect"},
		nil,
	}
	got := CollectCourseIDs(facts, activity)
	sort.Strings(got)
	want := []string{
		"05000000-0000-7000-8000-0000000c5301",
		"05000000-0000-7000-8000-0000000c5302",
		"05000000-0000-7000-8000-0000000c5303",
	}
	if len(got) != len(want) {
		t.Fatalf("CollectCourseIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CollectCourseIDs[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	// Empty inputs → empty (nil) slice, no panic.
	if got := CollectCourseIDs(nil, nil); len(got) != 0 {
		t.Errorf("CollectCourseIDs(nil,nil) = %v, want empty", got)
	}
}
