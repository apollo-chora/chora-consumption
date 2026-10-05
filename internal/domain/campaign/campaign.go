// Package campaign — the ADR-227 Companion Campaign domain core (WS-C1,
// CHO-2080): fog-of-war conquest of the learner-sovereign concept graph.
//
// The campaign is a PROJECTION over the existing concept hierarchy (D1):
// per-(tenant, learner, concept) ladder state climbs the 6-rung Bloom ladder
// (D6), advances at most one rung per dose day (D7 — the doseclock bucket:
// real UTC calendar days at product speed, env-shrinkable for accelerated
// E2E testing), demands warmth before advancement — never before holdings
// (D8, ADR-207 stickiness), and wins forever at 6/6 (D9). Deterministic by
// construction: the LLM never schedules or scores here (ADR-202); every
// transition follows server-graded work injected by the caller.
//
// The aggregate deliberately carries NO goal id (D5): campaign state is
// per-(learner, node) and survives ADR-214 re-roots untouched; emitters
// derive goal_id at event time.
//
// Pure + clock-injected: no I/O, no wall-clock reads; the dose-day bucket
// length is boot-configured process-wide (doseclock).
package campaign

import (
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
)

// Rung is a LADDER POSITION (1..6) in the ADR-227 D6 campaign order, spoken
// in the atom-domain original-Bloom vocabulary:
//
//	1 knowledge -> 2 comprehension -> 3 application -> 4 analysis ->
//	5 evaluation -> 6 synthesis   (the revised-Bloom "create" summit)
//
// DELIBERATELY NOT numerically aligned with chora-creation's CognitiveLevel
// enum (original-Bloom sequence: synthesis=5, evaluation=6). This constant
// owns the mapping; the FE maps labels to revised-Bloom copy; the campaign
// events carry these ladder positions (contracts CampaignRung mirrors them).
type Rung int

const (
	RungKnowledge Rung = iota + 1
	RungComprehension
	RungApplication
	RungAnalysis
	RungEvaluation
	RungSynthesis
)

// TotalRungs is the ladder height — winning a node = clearing all 6 (D6).
const TotalRungs = 6

// rungLabels maps ladder position -> original-Bloom label (index 0 = rung 1).
var rungLabels = [TotalRungs]string{
	"knowledge", "comprehension", "application", "analysis", "evaluation", "synthesis",
}

// Valid reports whether r is a real ladder position.
func (r Rung) Valid() bool { return r >= RungKnowledge && r <= RungSynthesis }

// Label returns the original-Bloom vocabulary label for the rung ("" when
// invalid) — the value the question lane filters atoms by (atom-domain
// CognitiveLevel vocabulary, ADR-227 addendum #4).
func (r Rung) Label() string {
	if !r.Valid() {
		return ""
	}
	return rungLabels[r-1]
}

// Tunables (defaults; cmd/server may override via env per the
// no-inline-config rule — the domain never reads env itself).
const (
	// DefaultRungClearCorrect — server-graded correct answers at the current
	// rung required to clear it (ADR-227 D6, CAMPAIGN_RUNG_CLEAR_CORRECT).
	DefaultRungClearCorrect = 2

	// DefaultResealMinNewWins — newly-won nodes required since the last seal
	// before a re-seal is allowed (ADR-227 D3, ">= N newly-won nodes").
	DefaultResealMinNewWins = 3

	// ResealMinInterval — the D3/D10 weekly cap on re-seals, action-side
	// (mirrors the tier-S ~1/week resolver cap chora-identity enforces).
	ResealMinInterval = 7 * 24 * time.Hour

	// RefreshThreshold — retention below this gates advancement with a
	// refresher (ADR-227 D8). Pinned to topic_retention.DueThreshold (0.6):
	// the campaign reads the SAME per-concept-key retention aggregate the
	// map's due-terrain uses.
	RefreshThreshold = 0.6
)

// Sentinel errors (fail-loud; handlers map to HTTP statuses).
var (
	ErrInvalid          = errors.New("campaign: invalid")
	ErrDeleted          = errors.New("campaign: node progress is soft-deleted")
	ErrAlreadyWon       = errors.New("campaign: node already won (D9 ratchet — it left the game)")
	ErrRungNotUnlocked  = errors.New("campaign: rung above the ladder frontier")
	ErrFrontierNotEmpty = errors.New("campaign: frontier not empty — win every claimed node first")
	ErrSealTooSoon      = errors.New("campaign: re-seal weekly cap not elapsed")
	ErrSealNoNewWins    = errors.New("campaign: re-seal requires enough newly-won nodes")
)

// NodeProgress is the per-(tenant, learner, concept) campaign ladder state —
// one row in campaign_node_progress (migration 0077/0078).
type NodeProgress struct {
	ID          string
	TenantID    string
	LearnerGCID string
	ConceptID   string

	// RungsCleared counts strictly-ordered cleared rungs (0..6).
	RungsCleared int
	// RungClearedAt is the positional audit: element i = cleared-at of rung
	// i+1. Cardinality always equals RungsCleared (DB CHECK enforces too).
	RungClearedAt []time.Time
	// CurrentRungCorrect counts server-graded corrects at the CURRENT (next
	// uncleared) rung since it became current. Never reset by a wrong answer
	// — the retention penalty is the Ebbinghaus side's job; the anti-farm
	// defence is calendar time (D7), not counter resets.
	CurrentRungCorrect int
	// WonAt is the D9 permanent ratchet — set once at 6/6, never unset.
	WonAt *time.Time
	// LastAdvanceDate backs the D7 gate: at most one rung advance per dose
	// day (doseclock bucket start; stored as timestamptz since mig 0090 —
	// midnight UTC at product speed, sub-day at accelerated test speed).
	LastAdvanceDate *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewNodeProgress mints a fresh unstarted ladder for a claimed node. Rows are
// created lazily — the first graded campaign answer for a node creates one.
func NewNodeProgress(tenantID, learnerGCID, conceptID string, now time.Time) (*NodeProgress, error) {
	if tenantID == "" || learnerGCID == "" || conceptID == "" {
		return nil, fmt.Errorf("%w: tenant, learner and concept are required", ErrInvalid)
	}
	return &NodeProgress{
		ID:          domain.NewUUIDv7(),
		TenantID:    tenantID,
		LearnerGCID: learnerGCID,
		ConceptID:   conceptID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Won reports the D9 ratchet.
func (p *NodeProgress) Won() bool { return p.WonAt != nil }

// HighestCleared returns the highest cleared rung (0 when none — not a valid
// Rung; callers check RungsCleared or Valid()).
func (p *NodeProgress) HighestCleared() Rung { return Rung(p.RungsCleared) }

// NextRung returns the next rung to clear; ok=false once the node is won.
func (p *NodeProgress) NextRung() (Rung, bool) {
	if p.RungsCleared >= TotalRungs {
		return 0, false
	}
	return Rung(p.RungsCleared + 1), true
}

// AdvancedOn reports whether the ladder already advanced in day's dose day
// (the D7 pacing gate; doseclock bucket).
func (p *NodeProgress) AdvancedOn(day time.Time) bool {
	if p.LastAdvanceDate == nil {
		return false
	}
	return doseclock.SameBucket(*p.LastAdvanceDate, day)
}

// GradeOutcome describes what one server-graded answer did to the ladder.
// The orchestrator maps it to side effects: retention Review (always, via
// topic_retention keyed by the node's concept_key), rung_cleared.v1 (on
// ClearedRung, or IsRefresher+correct with is_refresher=true), node_won.v1
// (on Won).
type GradeOutcome struct {
	// IsRefresher — the answer landed at an already-cleared rung (D8
	// defence). Cleared rungs are never lost; nothing else changes.
	IsRefresher bool
	// Counted — a correct at the current rung advanced the counter.
	Counted bool
	// ClearedRung — the rung this answer cleared (0 = none).
	ClearedRung Rung
	// Won — this answer completed the ladder (6/6, D9 ratchet).
	Won bool
	// PacedToday — the counter met the threshold but the D7 gate held the
	// clear until the next calendar day (AC: "retention S grows but the next
	// rung unlocks only tomorrow").
	PacedToday bool
}

// ApplyGraded folds one server-graded campaign answer at atRung into the
// ladder. threshold is the resolved CAMPAIGN_RUNG_CLEAR_CORRECT tunable.
// Deterministic; no I/O. The caller persists p and emits events per the
// outcome.
func (p *NodeProgress) ApplyGraded(atRung Rung, correct bool, threshold int, now time.Time) (GradeOutcome, error) {
	if p.DeletedAt != nil {
		return GradeOutcome{}, ErrDeleted
	}
	if !atRung.Valid() {
		return GradeOutcome{}, fmt.Errorf("%w: rung %d outside ladder 1..6", ErrInvalid, int(atRung))
	}
	if threshold < 1 {
		return GradeOutcome{}, fmt.Errorf("%w: threshold %d must be >= 1", ErrInvalid, threshold)
	}
	if p.Won() {
		return GradeOutcome{}, ErrAlreadyWon
	}

	next, _ := p.NextRung() // not won, so a next rung always exists

	// D8 refresher: an answer at an already-cleared rung re-warms retention
	// (orchestrator-side) and never touches the ladder.
	if atRung < next {
		return GradeOutcome{IsRefresher: true}, nil
	}
	if atRung > next {
		return GradeOutcome{}, fmt.Errorf("%w: rung %d served while frontier is %d", ErrRungNotUnlocked, int(atRung), int(next))
	}

	// atRung == next: the climb.
	if !correct {
		return GradeOutcome{}, nil
	}

	p.CurrentRungCorrect++
	p.UpdatedAt = now
	out := GradeOutcome{Counted: true}

	if p.CurrentRungCorrect < threshold {
		return out, nil
	}
	if p.AdvancedOn(now) {
		// Threshold met but the ladder already advanced today (D7): the
		// clear waits for the next calendar day's first correct.
		out.PacedToday = true
		return out, nil
	}

	// CLEAR.
	p.RungsCleared++
	p.RungClearedAt = append(p.RungClearedAt, now)
	p.CurrentRungCorrect = 0
	advDay := doseclock.Bucket(now)
	p.LastAdvanceDate = &advDay
	out.ClearedRung = next

	if p.RungsCleared == TotalRungs {
		won := now
		p.WonAt = &won // D9: forever
		out.Won = true
	}
	return out, nil
}
