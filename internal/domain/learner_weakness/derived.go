// derived.go — W3-derived scoring for performance-derived Growth Edges.
//
// Two pure pieces:
//   - DerivedStrength turns the (topic_accuracy, Ebbinghaus retention) pair
//     into a 0..1 shakiness for derived/classroom evidence.
//   - Recover is the SEPARATE lowering path Merge's docstring reserves: new
//     evidence folded via Merge can only confirm/raise shakiness; sustained
//     good performance flows through Recover, which only ever lowers it
//     ("derived auto-recovers via Ebbinghaus" per the locked Epic-1b lifecycle).
package learner_weakness

import "time"

// DerivedAccuracyThreshold is the topic-accuracy floor below which performance
// evidence mints/raises a derived Growth Edge; at or above it the same evidence
// recovers (lowers) the matching edge instead. Mirrors the daily-dose WEAKNESS
// slot default (companion.DefaultWeaknessThreshold).
const DerivedAccuracyThreshold = 0.70

// Blend weights: the miss-rate carries most of the signal; Ebbinghaus
// forgetting sharpens it when a retention score exists for the topic.
const (
	derivedMissWeight   = 0.7
	derivedForgetWeight = 0.3
)

// DerivedStrength blends the learner's miss-rate on a topic (1 - accuracy, 70%)
// with Ebbinghaus forgetting (1 - retention, 30%). Without a retention score
// (hasRetention=false) the miss-rate stands alone. Inputs and output are
// clamped into [0,1].
func DerivedStrength(accuracy, retention float64, hasRetention bool) float64 {
	miss := 1 - ClampStrength(accuracy)
	if !hasRetention {
		return ClampStrength(miss)
	}
	forget := 1 - ClampStrength(retention)
	return ClampStrength(derivedMissWeight*miss + derivedForgetWeight*forget)
}

// Recover lowers the edge's strength to the supplied (clamped) value when —
// and only when — it is lower than the current strength, recomputing the
// lifecycle status (an edge recovered to <= MasteredStrengthThreshold flips
// to "grown") and advancing last_evidenced_at + updated_at. Returns whether
// the recovery applied; a would-be raise mutates nothing (raising is Merge's
// job, fed by fresh evidence).
func (w *LearnerWeakness) Recover(strength float64, now time.Time) bool {
	s := ClampStrength(strength)
	if s >= w.Strength {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	w.Strength = s
	w.LastEvidencedAt = now
	w.UpdatedAt = now
	w.applyStatus()
	return true
}
