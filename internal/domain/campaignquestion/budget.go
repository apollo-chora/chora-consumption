// budget.go: the remaining-taps read model (UX refactor Phase B, package B6
// item 3).
//
// Why this exists. The daily bound is already enforced, in DailyCapFor and
// CanRequestToday above, and it was completely invisible to the UI. So the
// practice lane could not show its own budget, and when a learner spent the
// last tap the button simply stopped working: no count, no reason, nothing to
// distinguish "you are done for today" from "this is broken". A learner cannot
// tell those apart, and one of them is a bug report.
//
// Nothing new is stored and no new rule is invented. This is a PROJECTION over
// the same two functions the enforcement path calls, which is the whole point:
// a budget the card renders from a second, parallel calculation would drift
// from the one that refuses the request, and the card would promise a tap the
// next call rejects. A table-driven test walks the entire boundary for both
// origins and asserts the two never disagree.
package campaignquestion

// DailyBudget is one origin's remaining allowance for the current UTC day.
//
// Used is the TRUE count and is never clamped: a learner can hold more spent
// requests than the cap allows if the owner tunes a cap down, and hiding that
// would misreport history. Remaining is clamped at zero, because it is a count
// the UI renders and a negative one is meaningless.
type DailyBudget struct {
	Origin    RequestOrigin
	Cap       int
	Used      int
	Remaining int
	Exhausted bool
}

// BudgetFor projects one origin's budget from the learner's same-origin
// request count for the current UTC day.
//
// An unknown origin inherits DailyCapFor's conservative march fallback rather
// than widening it. Showing three taps for an origin the enforcement path caps
// at one would promise something the next request refuses, which is exactly
// the broken-button experience this read model exists to remove.
func BudgetFor(origin RequestOrigin, requestsToday int) DailyBudget {
	if requestsToday < 0 {
		// A negative count is a caller or repository bug. Treating it as zero
		// keeps the card renderable; carrying it through would show MORE taps
		// remaining than the cap allows.
		requestsToday = 0
	}
	capacity := DailyCapFor(origin)
	remaining := capacity - requestsToday
	if remaining < 0 {
		remaining = 0
	}
	return DailyBudget{
		Origin:    origin,
		Cap:       capacity,
		Used:      requestsToday,
		Remaining: remaining,
		// Derived from the same comparison CanRequestToday makes, not from a
		// second rule that could drift away from it.
		Exhausted: !CanRequestToday(origin, requestsToday),
	}
}
