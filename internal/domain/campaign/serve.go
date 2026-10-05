// serve.go — the deterministic serve decision for a campaign slot (ADR-227
// D8 refresh-before-advance). The scheduler stays LLM-free (ADR-202): given
// the ladder state and the node's retention R(now), the next serve is a pure
// function.
package campaign

import "time"

// ServeDecision says what the campaign slot should serve for a node.
type ServeDecision struct {
	// Rung to draw questions at (the atom-domain CognitiveLevel vocabulary
	// via Rung.Label()).
	Rung Rung
	// IsRefresher — serve a refresher at the highest cleared rung instead of
	// the next rung ("secure your supply lines before advancing", D8).
	IsRefresher bool
}

// DecideServe picks the rung for a node's campaign slot.
//
//   - p == nil means the node has no ladder yet (unstarted claimed
//     territory): serve rung 1.
//   - retentionR is R(now) of the node's topic_retention row (keyed by the
//     node's concept_key — addendum #2). nil = row absent.
//   - A node with cleared rungs but NO retention row is served conservatively
//     as a refresher: campaign grading plants the row, so absence on a
//     climbing node is an anomaly — refreshing is the safe deterministic
//     answer, never a silent skip.
//
// Won nodes refuse (D9 — they exit the game; the monument pool owns them).
func DecideServe(p *NodeProgress, retentionR *float64, now time.Time) (ServeDecision, error) {
	if p == nil {
		return ServeDecision{Rung: RungKnowledge}, nil
	}
	if p.DeletedAt != nil {
		return ServeDecision{}, ErrDeleted
	}
	if p.Won() {
		return ServeDecision{}, ErrAlreadyWon
	}

	next, _ := p.NextRung()
	if p.RungsCleared == 0 {
		// Nothing cleared yet — nothing to refresh; retention is irrelevant.
		return ServeDecision{Rung: next}, nil
	}
	if retentionR == nil || *retentionR < RefreshThreshold {
		return ServeDecision{Rung: p.HighestCleared(), IsRefresher: true}, nil
	}
	return ServeDecision{Rung: next}, nil
}
