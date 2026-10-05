// lens.go: the DERIVED dashboard lead (ADR-204 §3). The lead is computed from
// the learner's own state, never stored: a stored mode-enum would be a hidden
// toggle, violating the integrative-UI mandate (role-driven feature visibility,
// no toggles). It is self-explaining and changes naturally as the state changes.
//
// UX Track U (master plan section 4.1, ruling R4) replaces the single enum with
// RAW COUNTERS. The home is one ranked list ordered by urgency then warmth, so
// it needs the signals themselves; a binary enum can only express the "fixed UI
// per audience" shape ruling R2 forbids. `Lens` survives as a back-compat
// projection of those counters for the callers that still read it.
package goal

import "time"

// Lens is the derived dashboard lead.
type Lens string

const (
	// LensCuriosity is the default — map-first / discovery-led.
	LensCuriosity Lens = "curiosity"
	// LensCredential is path-first — Continue-path primary, map secondary.
	LensCredential Lens = "credential"
)

// PathLead is the minimal projection of ONE of the learner's live learning
// paths that the lead derivation reads. It is deliberately NOT the
// learning_path aggregate: the goal domain must not depend on a sibling
// aggregate, so the adapter projects each path down to the two facts the lead
// needs. Soft-deleted paths are filtered by the repository port
// (learning_path.Repo.ListByLearner returns non-deleted rows only) and MUST NOT
// be passed here.
type PathLead struct {
	// CourseBound is true when the path carries a chora-delivery course
	// binding (learning_paths.course_id, migration 0043). A collection-derived
	// study list and an ad-hoc path are NOT course-bound (ADR-233 D2).
	CourseBound bool
	// UpdatedAt is the path's last write, the course axis activity stamp.
	UpdatedAt time.Time
}

// Lead is the raw lead signal set the A+ home ranks on. Every field is a count
// or a timestamp, never a mode: the ranker weighs them, the learner never
// switches them.
//
// The two timestamps are nil when their axis has no live rows at all, which is
// the honest "no signal" state: they are NEVER defaulted to a zero time, since
// a zero time reads as "infinitely stale" and would rank a brand-new learner as
// the most neglected one on the platform.
type Lead struct {
	// ActiveGoals is the number of live goals in StatusActive.
	ActiveGoals int
	// ActiveCourseBoundPaths is the number of live learning paths bound to a
	// course, the cheapest credential-intent predicate available in the same
	// request (one repo call on the struct that already computes the lead).
	ActiveCourseBoundPaths int
	// LastCuriosityAt is the latest write across ALL live goals, whatever their
	// status: an achieved goal touched an hour ago is still curiosity activity.
	LastCuriosityAt *time.Time
	// LastCourseAt is the latest write across the live COURSE-BOUND paths.
	LastCourseAt *time.Time
}

// DeriveLead computes the counters from the learner's live goals and the
// projection of their live learning paths. Soft-deleted goals are skipped
// (ddd-enforcement #4) and stamp neither axis.
func DeriveLead(goals []Goal, paths []PathLead) Lead {
	var l Lead
	for _, g := range goals {
		if g.DeletedAt != nil {
			continue
		}
		if g.Status == StatusActive {
			l.ActiveGoals++
		}
		l.LastCuriosityAt = later(l.LastCuriosityAt, g.UpdatedAt)
	}
	for _, p := range paths {
		if !p.CourseBound {
			continue
		}
		l.ActiveCourseBoundPaths++
		l.LastCourseAt = later(l.LastCourseAt, p.UpdatedAt)
	}
	return l
}

// PrimaryLens projects the counters back onto the legacy two-value lead so the
// callers that still read `primaryLens` keep working.
//
// Credential-led IFF the learner holds at least one live course-bound path.
// ADR-214 §3/§4 removed the self-completing credential goal KINDS, so no goal
// kind drives this any more; the binding to an operator credential lives on the
// learning path, which is where enrolment puts it. The ADR-216 aspiration link,
// when it lands, narrows this predicate; it does not replace it.
//
// The zero Lead projects to LensCuriosity, which is the default the A+ FE
// already falls back to when the field is absent.
func (l Lead) PrimaryLens() Lens {
	if l.ActiveCourseBoundPaths > 0 {
		return LensCredential
	}
	return LensCuriosity
}

// later returns a COPY of whichever of cur / t is the later moment. The copy
// matters: handing back a pointer into the caller's slice would let a later
// mutation rewrite a value the adapter has already put on the wire.
func later(cur *time.Time, t time.Time) *time.Time {
	if t.IsZero() {
		return cur
	}
	if cur != nil && !t.After(*cur) {
		return cur
	}
	cp := t
	return &cp
}
