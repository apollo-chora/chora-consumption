// merge_split_transform.go — WS-C6 (CHO-2085, ADR-227 D14) concept
// merge/split ladder reconciliation.
//
// When the learner-sovereign concept graph is re-shaped (two concepts merge
// into one survivor, or one concept splits into children), the campaign ladder
// must follow deterministically, without ever re-firing XP:
//
//   - MERGE keeps the survivor conservative — the AND of the two sides' cleared
//     rungs (you have truly mastered a merged concept only to the depth you
//     mastered BOTH parts), the LATER pacing date (merging two nodes that each
//     advanced today must not gift a third advance today), and a win only when
//     BOTH sides had already won.
//   - SPLIT is generous — each child inherits the parent's full ladder (a won
//     parent yields won children); the learner keeps what they earned.
//
// These transforms emit NO campaign events (no rung_cleared.v1 / node_won.v1):
// the survivor/child rows materialise silently. The XP re-award refusal is a
// separate consumer-side guard, fed here only by the pure PriorLadderState fold
// over a node's tombstoned lineage history (mig 0077).
//
// Pure + clock-injected, consistent with campaign.go: no I/O, no wall-clock
// reads. Inputs are never mutated; every result row is a fresh UUIDv7 with
// deep-copied audit/pointer state (no aliasing of an input's backing array).
package campaign

import (
	"fmt"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// MergeProgress computes the AND-of-rungs merged ladder for the surviving
// concept (ADR-227 D14). survivor/absorbed are the two nodes' live ladder rows
// — either or both may be nil (ladders are created lazily). Returns (nil, nil)
// when both are nil (nothing to carry). Inputs are NOT mutated; the result is a
// FRESH row (new UUIDv7) for survivorConceptID.
func MergeProgress(survivor, absorbed *NodeProgress, tenantID, learnerGCID, survivorConceptID string, now time.Time) (*NodeProgress, error) {
	if survivor == nil && absorbed == nil {
		return nil, nil // nothing to carry
	}
	if tenantID == "" || learnerGCID == "" || survivorConceptID == "" {
		return nil, fmt.Errorf("%w: tenant, learner and survivor concept are required", ErrInvalid)
	}

	// AND of cleared rungs (nil side counts as 0/6).
	rungs := min(rungsClearedOf(survivor), rungsClearedOf(absorbed))

	merged := &NodeProgress{
		ID:           domain.NewUUIDv7(),
		TenantID:     tenantID,
		LearnerGCID:  learnerGCID,
		ConceptID:    survivorConceptID,
		RungsCleared: rungs,
		// CurrentRungCorrect resets: a fresh counter at the new frontier rung.
		CurrentRungCorrect: 0,
		// D7 anti-farm: carry the LATER of the two pacing dates so a same-day
		// merge cannot buy an extra advance.
		LastAdvanceDate: laterDate(lastAdvanceOf(survivor), lastAdvanceOf(absorbed)),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// Positional audit: the first `rungs` stamps of the survivor's own trail.
	// rungs == min <= survivor.RungsCleared whenever survivor is non-nil, and
	// the cardinality invariant guarantees survivor has that many stamps; when
	// survivor is nil, rungs == 0 so the trail is empty. Deep-copied — the
	// merged row never aliases an input's backing array.
	if survivor != nil && rungs > 0 {
		merged.RungClearedAt = make([]time.Time, rungs)
		copy(merged.RungClearedAt, survivor.RungClearedAt[:rungs])
	}

	// D9 win only when BOTH sides won (min == 6); carry the survivor's own
	// permanent stamp (deep-copied).
	if rungs == TotalRungs && survivor != nil && survivor.WonAt != nil {
		won := *survivor.WonAt
		merged.WonAt = &won
	}

	return merged, nil
}

// SplitProgress copies the parent's ladder onto each child concept (ADR-227
// D14: children inherit). A nil parent yields (nil, nil). Each child is a fresh
// UUIDv7 with the parent's rungs, positional audit, WonAt and LastAdvanceDate
// copied and the current-rung counter reset to 0. The RungClearedAt slice is
// deep-copied per child (no aliasing between children or with the parent).
func SplitProgress(parent *NodeProgress, tenantID, learnerGCID string, childConceptIDs []string, now time.Time) ([]*NodeProgress, error) {
	if parent == nil {
		return nil, nil // nothing to inherit
	}
	if tenantID == "" || learnerGCID == "" {
		return nil, fmt.Errorf("%w: tenant and learner are required", ErrInvalid)
	}
	if len(childConceptIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one child concept is required", ErrInvalid)
	}

	children := make([]*NodeProgress, 0, len(childConceptIDs))
	for _, conceptID := range childConceptIDs {
		if conceptID == "" {
			return nil, fmt.Errorf("%w: child concept id must be non-empty", ErrInvalid)
		}
		child := &NodeProgress{
			ID:                 domain.NewUUIDv7(),
			TenantID:           tenantID,
			LearnerGCID:        learnerGCID,
			ConceptID:          conceptID,
			RungsCleared:       parent.RungsCleared,
			CurrentRungCorrect: 0,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if len(parent.RungClearedAt) > 0 {
			child.RungClearedAt = make([]time.Time, len(parent.RungClearedAt))
			copy(child.RungClearedAt, parent.RungClearedAt)
		}
		if parent.WonAt != nil {
			won := *parent.WonAt
			child.WonAt = &won
		}
		if parent.LastAdvanceDate != nil {
			adv := *parent.LastAdvanceDate
			child.LastAdvanceDate = &adv
		}
		children = append(children, child)
	}
	return children, nil
}

// PriorLadderState folds a node's tombstoned lineage-history ladder rows (its
// own pre-merge/pre-split tombstones plus every lineage ancestor's rows) into
// the XP re-award refusal facts (mig 0077: "did XP already flow to an
// ancestor?"). Pure fold; nil rows are tolerated.
func PriorLadderState(rows []*NodeProgress) (maxRungsCleared int, everWon bool) {
	for _, r := range rows {
		if r == nil {
			continue
		}
		if r.RungsCleared > maxRungsCleared {
			maxRungsCleared = r.RungsCleared
		}
		if r.WonAt != nil {
			everWon = true
		}
	}
	return maxRungsCleared, everWon
}

// rungsClearedOf reads a node's cleared-rung count, treating a nil node as 0.
func rungsClearedOf(p *NodeProgress) int {
	if p == nil {
		return 0
	}
	return p.RungsCleared
}

// lastAdvanceOf reads a node's D7 pacing date, treating a nil node as no date.
func lastAdvanceOf(p *NodeProgress) *time.Time {
	if p == nil {
		return nil
	}
	return p.LastAdvanceDate
}

// laterDate returns a deep copy of the later of two optional dates (nil counts
// as "no date"): both nil -> nil, one nil -> the other, else the later one.
func laterDate(a, b *time.Time) *time.Time {
	switch {
	case a == nil && b == nil:
		return nil
	case a == nil:
		d := *b
		return &d
	case b == nil:
		d := *a
		return &d
	case b.After(*a):
		d := *b
		return &d
	default:
		d := *a
		return &d
	}
}
