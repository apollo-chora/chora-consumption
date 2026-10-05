// learner_profile_presentation.go — learner-SAFE rendering of the projected
// profile for the one surface that shows it to a human: the Companion prompt.
//
// A Fact / ActivityEntry carries raw cross-domain reference ids — course_id,
// cert_id, path_id, submission_id — as opaque UUIDs (ddd-enforcement #3: cross-
// domain refs are UUIDs without FK; chora-consumption owns none of those
// aggregates, so it has no human name for them here). The Companion reads these
// projections and would otherwise parrot a raw UUID straight into its
// reflection ("a course identified as '05000000-…'"), violating the domain-
// vocabulary rule that a learner is NEVER shown a platform identifier.
//
// This presenter is the single, DDD-owned place that answers "how do we say
// this fact to a learner": it renders from the STRUCTURED fields (type / kind /
// human label) and, by construction, never emits a UUID. SanitizeLearnerText is
// the defense-in-depth backstop so even a legacy activity summary (which baked
// the ref UUID into free text) or an unexpected label cannot leak one.
//
// Real course/cert/path NAMES now arrive via the course_directory projection
// (mirroring chora-delivery's user_directory), fed by delivery.course.* events.
// The two consumers (buildProgressMirrorTurn + gRPC ReadLearnerProfile) batch-
// resolve a `resolvedName` per fact/activity and pass it in; a present, non-UUID
// name WINS over the type-generic noun. When no name is resolved (reader unwired,
// course unknown, or a non-course fact) the presenter is byte-identical to before:
// an un-named ref reads as a type-generic noun ("a course"), honest + leak-free.
// The presenter still NEVER emits a raw identifier: a resolvedName that itself
// looks like a UUID (bad data) is ignored, and SanitizeLearnerText backstops any
// embedded UUID token in the name.
package learner_profile

import (
	"regexp"
	"strings"
)

// uuidPattern matches a hyphenated 8-4-4-4-12 hex UUID — the shape of every
// cross-domain reference id a Fact/ActivityEntry can carry.
var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// looksLikeUUID reports whether s is exactly a hyphenated UUID (a raw ref, not
// human text that merely contains one).
func looksLikeUUID(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) == 36 && uuidPattern.FindString(s) == s
}

// SanitizeLearnerText strips any raw UUID token from free text bound for a
// learner-facing surface and collapses the whitespace the removal leaves. It is
// the backstop: no UUID survives to the Companion even if a label or a legacy
// summary carries one.
func SanitizeLearnerText(s string) string {
	if s == "" {
		return s
	}
	out := uuidPattern.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(out), " ")
}

// genericNoun is the learner-facing noun for a fact type when no human name is
// available. It NEVER returns an identifier.
func (t FactType) genericNoun() string {
	switch t {
	case FactEnrollment, FactCourseCompleted:
		return "a course"
	case FactPathCompleted:
		return "a learning path"
	case FactCertificationIssued:
		return "a certification"
	case FactAssessmentGraded:
		return "an assessment"
	case FactPreference:
		return "a preference"
	}
	return "an item"
}

// FactDisplayLabel renders the learner-safe label for one fact.
//
// resolvedName is the course title the caller resolved from the course_directory
// projection for this fact's course_id (see CourseIDForFact), or "" when none was
// resolved (reader unwired / course unknown / a non-course fact). A present,
// non-UUID resolvedName WINS — it is returned (sanitized) as the label. Otherwise
// the behaviour is UNCHANGED from the pre-projection presenter: the projected
// human label when present and non-UUID; a cert's label (which IS the course
// UUID) and an enrollment/path's empty label both fall back to a type-generic
// noun; a preference names its (human) key. The result never contains a raw
// cross-domain identifier — a resolvedName that itself looks like a UUID is
// ignored (falls through to the existing behaviour).
func FactDisplayLabel(f *Fact, resolvedName string) string {
	if f == nil {
		return ""
	}
	if rn := strings.TrimSpace(resolvedName); rn != "" && !looksLikeUUID(rn) {
		return SanitizeLearnerText(rn)
	}
	label := strings.TrimSpace(f.Detail.Label)
	if label == "" || looksLikeUUID(label) {
		if f.Type == FactPreference {
			// SanitizeLearnerText: a preference KEY can embed a UUID (e.g.
			// "dose.map.<map_uuid>") — strip it so the key never leaks an id.
			if k := SanitizeLearnerText(f.RefID); k != "" {
				return k
			}
		}
		return f.Type.genericNoun()
	}
	label = SanitizeLearnerText(label)
	if f.Type == FactPreference {
		if k := SanitizeLearnerText(f.RefID); k != "" {
			return k + ": " + label
		}
	}
	return label
}

// ActivityDisplay renders one activity-log line for the learner from its
// structured Kind — never from the stored free-text Summary (legacy rows baked
// the raw ref UUID into it) or the RefID. Leak-proof by construction; an
// unknown kind falls back to the sanitized summary.
//
// resolvedName is the course title the caller resolved for this activity's
// course_id (see CourseIDForActivity), or "" when none was resolved. A present,
// non-UUID resolvedName WINS — the [activity: <kind>] tag at the call site
// carries the verb, so the line surfaces the real course name. Otherwise the
// behaviour is UNCHANGED (generic kind line / sanitized summary). A UUID
// resolvedName is ignored.
func ActivityDisplay(a *ActivityEntry, resolvedName string) string {
	if a == nil {
		return ""
	}
	if rn := strings.TrimSpace(resolvedName); rn != "" && !looksLikeUUID(rn) {
		return SanitizeLearnerText(rn)
	}
	switch a.Kind {
	case "enrolled":
		return "Enrolled in a course"
	case "completed_course":
		return "Completed a course"
	case "earned_certification":
		return "Earned a certification"
	case "scored_assessment":
		return "Was graded on an assessment"
	case "completed_path":
		return "Completed a learning path"
	}
	return SanitizeLearnerText(a.Summary)
}

// SafeFactRef returns a learner-safe reference for a fact: the human key for a
// preference, empty for any raw cross-domain UUID (a course/cert/path/submission
// id must never reach the Companion).
func SafeFactRef(f *Fact) string {
	if f == nil || looksLikeUUID(f.RefID) {
		return ""
	}
	return strings.TrimSpace(f.RefID)
}

// CourseIDForFact returns the cross-domain course_id a fact references, or "" for
// a fact that is not about a course. It is the single DDD-owned place that knows
// WHICH structured field carries the course id per fact type — so both consumers
// (progress_mirror + ReadLearnerProfile) resolve titles identically:
//   - enrollment / course_completed → RefID (the course_id)
//   - certification_issued          → Detail.Label (the cert's label IS the
//     course UUID — the very field the presenter otherwise suppresses)
//
// path_completed / assessment_graded / preference reference no course → "".
func CourseIDForFact(f *Fact) string {
	if f == nil {
		return ""
	}
	switch f.Type {
	case FactEnrollment, FactCourseCompleted:
		return strings.TrimSpace(f.RefID)
	case FactCertificationIssued:
		return strings.TrimSpace(f.Detail.Label)
	}
	return ""
}

// CourseIDForActivity returns the course_id an activity-log entry references, or
// "" when it is not a course activity. Only the enrolled / completed_course kinds
// carry a course_id in RefID (earned_certification / scored_assessment /
// completed_path reference a cert / submission / path, not a course).
func CourseIDForActivity(a *ActivityEntry) string {
	if a == nil {
		return ""
	}
	switch a.Kind {
	case "enrolled", "completed_course":
		return strings.TrimSpace(a.RefID)
	}
	return ""
}

// CollectCourseIDs gathers the distinct, non-empty course_ids referenced by the
// supplied facts + activities (via CourseIDForFact / CourseIDForActivity), in
// first-seen order. The caller batch-resolves these through the course_directory
// reader in ONE LookupTitles call. Returns nil when nothing references a course.
func CollectCourseIDs(facts []*Fact, activities []*ActivityEntry) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, f := range facts {
		add(CourseIDForFact(f))
	}
	for _, a := range activities {
		add(CourseIDForActivity(a))
	}
	return out
}
