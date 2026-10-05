// seen.go: the unseen-result flag on a transcript entry
// (UX refactor Phase B, package B6 item 2).
//
// Why this exists. The learner's home wants to say "your result is in", and
// until now it could not: the FE model carried a result STATE but nothing
// distinguished a result the learner had read from one they had not. So the
// card could be neither ranked nor dismissed, and a learner who had already
// opened a grade kept being told about it.
//
// Where it lives. The transcript is the consumption-side, learner-owned
// projection of graded submissions, fed from the same verified
// chora.delivery.submission.graded.v1 deliveries the LearnerProfile
// projection consumes. "Has this learner opened this result" is a
// learner-private fact about their own reading, not a fact about the
// assessment, so it belongs here rather than in delivery.
//
// The one-way rule. SeenAt records when the learner FIRST opened the result
// and is never moved afterwards. Re-reading an old grade must not resurface
// it or re-sort the card, and there is deliberately no un-see: a flag that
// could be cleared would be a preference, and this is a fact.
package student_transcript

import "time"

// Unseen reports whether the learner has never opened this result.
//
// A nil entry is NOT unseen: a missing row must not inflate the unseen count
// that the home card badges.
func (e *TranscriptEntry) Unseen() bool {
	return e != nil && e.SeenAt == nil
}

// MarkSeen stamps the first-seen moment and reports whether it changed
// anything, so a caller can skip a write when there is nothing to persist.
//
// Refuses a zero timestamp: that is a caller bug, and accepting it would mark
// the result seen in year 1 and sort it before every real entry. Idempotent by
// construction, so a double-tap on the result screen writes once.
func (e *TranscriptEntry) MarkSeen(at time.Time) bool {
	if e == nil || e.SeenAt != nil || at.IsZero() {
		return false
	}
	utc := at.UTC()
	e.SeenAt = &utc
	return true
}

// CountUnseen counts the entries the learner has not opened. Nil entries are
// skipped rather than counted, for the same reason Unseen refuses them.
func CountUnseen(entries []*TranscriptEntry) int {
	n := 0
	for _, e := range entries {
		if e.Unseen() {
			n++
		}
	}
	return n
}
