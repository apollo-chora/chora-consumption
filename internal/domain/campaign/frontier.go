// frontier.go — the D3 frontier campaign + seal state machine (ADR-227).
//
// "Claimed" territory = every live node in the goal subtree (D1 — an
// accepted proposal mints a real node; a manually-authored node is
// implicitly claimed). The frontier is the claimed-but-unwon set; sealing is
// the learner's sovereign declaration, allowed ONLY while the frontier is
// system-verified empty. The seal's XP is earned by that verification — a
// bare PersonalCompletedAt never awards (D10).
package campaign

import (
	"fmt"
	"sort"
	"time"
)

// FrontierState summarises a goal's campaign territory.
type FrontierState struct {
	// Total live claimed nodes in the goal subtree (root included).
	Total int
	// Won of those (D9 ratchet set).
	Won int
	// Remaining concept ids not yet won, sorted for determinism.
	Remaining []string
}

// ComputeFrontier folds the goal subtree (conceptgraph.SubtreeConceptIDs
// output) against the set of won concept ids.
func ComputeFrontier(subtree map[string]bool, won map[string]bool) FrontierState {
	f := FrontierState{}
	for id, in := range subtree {
		if !in {
			continue
		}
		f.Total++
		if won[id] {
			f.Won++
			continue
		}
		f.Remaining = append(f.Remaining, id)
	}
	sort.Strings(f.Remaining)
	return f
}

// SealDecision is a permitted seal.
type SealDecision struct {
	// IsReseal — a re-seal after genuine expansion (D3): the goal was sealed
	// before, and >= minNewWins nodes were won since.
	IsReseal bool
	// NodesWon at seal time (rides goal_sealed.v1).
	NodesWon int
}

// EvaluateSeal gates the learner's seal request (D3).
//
//   - The frontier must be empty (every claimed node won) — ErrFrontierNotEmpty.
//   - An empty campaign (no territory at all) cannot seal — ErrInvalid.
//   - First seal (lastSealedAt nil): allowed.
//   - Re-seal: requires >= minNewWins newly-won nodes since the last seal
//     (winsSinceLastSeal, computed by the caller as won_at > lastSealedAt)
//     AND >= minInterval since the last seal (the weekly cap, action-side).
//
// lastSealedAt is the goal's campaign_sealed_at — NOT PersonalCompletedAt:
// the ADR-213 personal axis can be set by a bare learner declaration that
// never touches the campaign; seal history is campaign state.
func EvaluateSeal(frontier FrontierState, lastSealedAt *time.Time, winsSinceLastSeal, minNewWins int, minInterval time.Duration, now time.Time) (SealDecision, error) {
	if minNewWins < 1 || minInterval <= 0 {
		return SealDecision{}, fmt.Errorf("%w: reseal tunables out of range", ErrInvalid)
	}
	if frontier.Total == 0 {
		return SealDecision{}, fmt.Errorf("%w: no claimed territory to seal", ErrInvalid)
	}
	if len(frontier.Remaining) > 0 {
		return SealDecision{}, fmt.Errorf("%w: %d node(s) unwon", ErrFrontierNotEmpty, len(frontier.Remaining))
	}
	if lastSealedAt == nil {
		return SealDecision{NodesWon: frontier.Won}, nil
	}
	if now.Sub(*lastSealedAt) < minInterval {
		return SealDecision{}, fmt.Errorf("%w: last sealed %s", ErrSealTooSoon, lastSealedAt.UTC().Format(time.RFC3339))
	}
	if winsSinceLastSeal < minNewWins {
		return SealDecision{}, fmt.Errorf("%w: %d since last seal, need >= %d", ErrSealNoNewWins, winsSinceLastSeal, minNewWins)
	}
	return SealDecision{IsReseal: true, NodesWon: frontier.Won}, nil
}
