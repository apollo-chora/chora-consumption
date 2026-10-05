// progress.go — verified-progress computation for the learner-owned Goal
// (CHO-1921). The A+ Goal %-ring shows how much of a Goal's concept set the
// learner has VERIFIABLY mastered — derived intra-domain at read time from
// mastered (grown) Growth Edges, never stored. This is the pure, clock-free
// share calculation; the adapter (goals_handler.go) supplies the counts.
package goal

import "math"

// ProgressPercent returns round(100*mastered/total) clamped to [0,100]. It is 0
// when there is nothing to verify (total <= 0) and guards a non-positive
// mastered count to 0, so a malformed input can never surface a negative ring.
// Pure: math only — no clock, no I/O.
func ProgressPercent(masteredCount, totalConcepts int) int {
	if totalConcepts <= 0 || masteredCount <= 0 {
		return 0
	}
	pct := int(math.Round(100 * float64(masteredCount) / float64(totalConcepts)))
	if pct > 100 {
		return 100
	}
	return pct
}
