// Package companiongoalknowledge is the pure-domain read-model cache behind the
// Companion tab's per-goal reflection (CHO-2118, tier-2): a short first-person
// narrative of what the learner's Companion remembers about them on ONE goal.
//
// It is a DERIVED PROJECTION, never a source of truth. The sources are the
// append-only per-Companion memory (companion_memory_recall, ADR-173), the goal +
// its concept subtree, and the learner's active weaknesses — all owned by
// chora_consumption. This package caches the LLM synthesis over them so the tab
// read is sub-millisecond and LLM-free on the hot path.
//
// Lifecycle (the kg_hexagon_nodes pattern, ADR-143 — materialise → cache-hit-on-
// read → event-invalidate → lazy-regen → decision-stamp):
//
//	never generated ──MarkRequested──▶ pending ──RecordSynthesis──▶ fresh
//	                                     ▲                            │
//	                                     └────── NeedsSynthesis ──────┤ Invalidate
//	                                                                  ▼
//	                                                                stale
//	                                                        (still SERVABLE)
//
// Two properties earn their keep here:
//
//   - A stale row KEEPS its last good text (Servable stays true). The learner
//     sees the previous reflection marked "reflecting…" rather than a blank —
//     serve-stale-while-regen. An empty synthesis is REFUSED outright rather
//     than cached (fail-loud; a blank reflection is a fabricated success).
//   - The TTL floor bounds cost. `chat_turn_completed` fires on EVERY chat turn,
//     so an invalidation alone must not authorise an LLM call; the floor means a
//     goal regenerates at most once per window no matter how chatty the learner
//     is. Max-age is the opposite guard: refresh eventually even if nothing
//     invalidated it.
//
// Hexagonal: pure domain. No SQL, no SDK, no prompt strings, no URLs. The prompt
// itself is registered in the synthesising orchestrator (ADR-197), never here.
package companiongoalknowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Tunable defaults. Real values are wired from env at cmd/server (no inline
// config) — these are the fallbacks the domain reasons about.
const (
	// DefaultRegenFloor is the minimum interval between two syntheses of the same
	// (companion, goal). It is the cost bound against chat_turn_completed churn.
	DefaultRegenFloor = 6 * time.Hour
	// DefaultMaxAge forces a refresh even when nothing invalidated the row.
	DefaultMaxAge = 14 * 24 * time.Hour
	// DefaultPendingTTL is how long an in-flight synthesis request is trusted
	// before it is presumed lost (crash window) and re-issued.
	DefaultPendingTTL = 15 * time.Minute
	// MaxSynthesisChars bounds the stored reflection. The contract is 2-3
	// sentences; anything materially longer means the model ignored the output
	// contract and must be refused rather than cached.
	MaxSynthesisChars = 600
)

// Status is the cache row's lifecycle state.
type Status string

const (
	StatusPending Status = "pending" // never generated, or a synthesis is in flight
	StatusFresh   Status = "fresh"   // synthesised and not invalidated
	StatusStale   Status = "stale"   // invalidated; last good text still servable
	// StatusSilent — the synthesis RAN and concluded that there is nothing true to
	// say about this goal yet. It is a terminal ANSWER, not a waiting state, and
	// that distinction is the whole of CHO-2180: while a declined synthesis left
	// the row looking never-generated, every read re-bought the same doomed LLM
	// call and the learner sat on a "reflecting…" marker that could never resolve.
	StatusSilent Status = "silent"
)

// InvalidationReason records WHY a row went stale. Retained (not overwritten by
// a weaker later signal) so the audit trail says what actually changed.
type InvalidationReason string

const (
	ReasonNeverGenerated      InvalidationReason = "never_generated"
	ReasonGoalRerooted        InvalidationReason = "goal_rerooted"
	ReasonGoalGraduated       InvalidationReason = "goal_graduated"
	ReasonWeaknessGrown       InvalidationReason = "weakness_grown"
	ReasonWeaknessAnalyzed    InvalidationReason = "weakness_analyzed"
	ReasonGoalProgressUpdated InvalidationReason = "goal_progress_updated"
	ReasonMemoryEvicted       InvalidationReason = "memory_evicted"
	ReasonChatTurnCompleted   InvalidationReason = "chat_turn_completed"
	ReasonMaxAgeExpired       InvalidationReason = "max_age_expired"
	ReasonManualAdmin         InvalidationReason = "manual_admin"
)

// reasonPriority ranks invalidation signals. Higher = stronger. A stronger
// reason is never overwritten by a weaker one (mirrors the hexagon fog cache),
// so a noisy chat turn cannot mask "the goal was re-rooted" in the audit trail.
//
// Order rationale: a re-root changes WHICH subtree the reflection is even about
// (strongest); graduation and real mastery movement change what there is to say;
// progress and memory changes are weaker; a chat turn is the highest-churn,
// lowest-signal event and sits at the bottom of the real signals.
var reasonPriority = map[InvalidationReason]int{
	ReasonGoalRerooted:        100,
	ReasonGoalGraduated:       95,
	ReasonWeaknessGrown:       90,
	ReasonWeaknessAnalyzed:    85,
	ReasonGoalProgressUpdated: 80,
	ReasonMemoryEvicted:       70,
	ReasonChatTurnCompleted:   60,
	ReasonMaxAgeExpired:       50,
	ReasonManualAdmin:         40,
	ReasonNeverGenerated:      0,
}

var (
	ErrTenantIDRequired    = errors.New("companion_goal_knowledge: tenant_id required")
	ErrLearnerGCIDRequired = errors.New("companion_goal_knowledge: learner_gcid required")
	ErrCompanionIDRequired = errors.New("companion_goal_knowledge: companion_id required")
	ErrGoalIDRequired      = errors.New("companion_goal_knowledge: goal_id required")
	ErrSynthesisEmpty      = errors.New("companion_goal_knowledge: synthesis text must not be empty")
	ErrSynthesisTooLong    = fmt.Errorf("companion_goal_knowledge: synthesis text exceeds %d chars", MaxSynthesisChars)
	ErrDecisionStamp       = errors.New("companion_goal_knowledge: run_id, model_id and prompt_version are all required")
)

// GoalKnowledge is one cached reflection, unique per (tenant, learner, companion,
// goal). Keyed by goal_id (the reflection IS about the goal); RootConceptID is
// carried alongside purely as the ADR-214 re-root guard.
type GoalKnowledge struct {
	ID          string
	TenantID    string
	LearnerGCID string
	CompanionID string
	GoalID      string

	// SynthesisText is the last good reflection. Empty ONLY before the first
	// successful synthesis — it is never blanked by an invalidation.
	SynthesisText string
	Status        Status

	// Decision stamp (ADR-197): a cached row that cannot say which prompt and
	// model produced it is not explainable, so these are mandatory on synthesis.
	GeneratedByRunID   string
	GeneratedByModelID string
	PromptVersion      string
	ContentHash        string // hash of the synthesis INPUTS, for drift detection
	GeneratedAt        *time.Time

	// RootConceptID is the goal root the reflection was generated against. A goal
	// that has since re-rooted (ADR-214) describes a different subtree, so the
	// cached text must not be served — see RootMatches.
	RootConceptID *string

	RequestedAt        *time.Time // when a synthesis request was published (in-flight claim)
	InvalidatedAt      *time.Time
	InvalidationReason InvalidationReason

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewGoalKnowledge mints a never-generated row for (tenant, learner, companion,
// goal). It starts PENDING with no text: there is nothing to show until the
// first synthesis lands, and the caller serves the tier-1 deterministic block
// meanwhile.
func NewGoalKnowledge(tenantID, learnerGCID, companionID, goalID string, rootConceptID *string) (*GoalKnowledge, error) {
	tenantID = strings.TrimSpace(tenantID)
	learnerGCID = strings.TrimSpace(learnerGCID)
	companionID = strings.TrimSpace(companionID)
	goalID = strings.TrimSpace(goalID)

	if tenantID == "" {
		return nil, ErrTenantIDRequired
	}
	if learnerGCID == "" {
		return nil, ErrLearnerGCIDRequired
	}
	if companionID == "" {
		return nil, ErrCompanionIDRequired
	}
	if goalID == "" {
		return nil, ErrGoalIDRequired
	}

	now := time.Now().UTC()
	return &GoalKnowledge{
		ID:                 domain.NewUUIDv7(),
		TenantID:           tenantID,
		LearnerGCID:        learnerGCID,
		CompanionID:        companionID,
		GoalID:             goalID,
		Status:             StatusPending,
		RootConceptID:      rootConceptID,
		InvalidationReason: ReasonNeverGenerated,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

// RecordSynthesis stores a completed reflection and makes the row fresh.
//
// It REFUSES an empty or oversize synthesis rather than caching it: a blank
// reflection is a fabricated success, and an oversize one means the model
// ignored the locked output contract. Both must fail loud so the caller leaves
// the row pending and keeps serving tier-1.
func (k *GoalKnowledge) RecordSynthesis(text, runID, modelID, promptVersion, contentHash string, generatedAt time.Time) error {
	text = strings.TrimSpace(text)
	runID = strings.TrimSpace(runID)
	modelID = strings.TrimSpace(modelID)
	promptVersion = strings.TrimSpace(promptVersion)

	if text == "" {
		return ErrSynthesisEmpty
	}
	if len([]rune(text)) > MaxSynthesisChars {
		return ErrSynthesisTooLong
	}
	if runID == "" || modelID == "" || promptVersion == "" {
		return ErrDecisionStamp
	}

	at := generatedAt.UTC()
	k.SynthesisText = text
	k.GeneratedByRunID = runID
	k.GeneratedByModelID = modelID
	k.PromptVersion = promptVersion
	k.ContentHash = contentHash
	k.GeneratedAt = &at
	k.Status = StatusFresh
	k.InvalidatedAt = nil
	k.InvalidationReason = ReasonNeverGenerated
	k.RequestedAt = nil
	k.UpdatedAt = time.Now().UTC()
	return nil
}

// RecordNoReflection records that the synthesis RAN and honestly had nothing to
// say about this goal — the model's NO_MEMORY_YET verdict (CHO-2180).
//
// This is the counterpart to RecordSynthesis, and it exists because "the model
// declined" and "the synthesis never happened" are completely different facts
// that used to look identical on the row. A declined synthesis published nothing,
// so GeneratedAt stayed nil — i.e. "never generated" — and NeedsSynthesis retries
// that unconditionally. The learner sat on "reflecting…" forever while every
// tab-open past the claim window re-bought the same guaranteed-to-decline call.
//
// So a silence is stamped like any other conclusion: it carries the FULL ADR-197
// decision stamp (silence is a model decision — a row that cannot name the prompt
// and model that concluded it is exactly as unexplainable as an unattributable
// reflection), it stamps GeneratedAt, and it releases the in-flight claim.
//
// It carries no text, and it SUPERSEDES any previous reflection: the model looked
// at the current view and declined to stand behind one. Keeping the old prose
// would have the Companion "remember" what the record no longer supports — which
// is exactly the eviction case the read policy already guards.
func (k *GoalKnowledge) RecordNoReflection(runID, modelID, promptVersion, contentHash string, generatedAt time.Time) error {
	runID = strings.TrimSpace(runID)
	modelID = strings.TrimSpace(modelID)
	promptVersion = strings.TrimSpace(promptVersion)

	if runID == "" || modelID == "" || promptVersion == "" {
		return ErrDecisionStamp
	}

	at := generatedAt.UTC()
	k.SynthesisText = ""
	k.GeneratedByRunID = runID
	k.GeneratedByModelID = modelID
	k.PromptVersion = promptVersion
	k.ContentHash = contentHash
	k.GeneratedAt = &at
	k.Status = StatusSilent
	k.InvalidatedAt = nil
	k.InvalidationReason = ReasonNeverGenerated
	k.RequestedAt = nil
	k.UpdatedAt = time.Now().UTC()
	return nil
}

// ConcludedSilent reports that a synthesis ran and returned an honest nothing —
// as opposed to a row that is merely CLAIMED and whose synthesis is still coming
// (or was lost). The read path owes those two very different answers.
//
// Deliberately derived from the FACTS (a synthesis completed, and it left no
// text) rather than from the Status label. RecordSynthesis structurally cannot
// produce that combination — it refuses an empty synthesis outright — so
// "generated, yet nothing to serve" can only mean the model declined. Leaning on
// the label instead would put this guard one careless status assignment away
// from silently reopening CHO-2180.
func (k *GoalKnowledge) ConcludedSilent() bool {
	return k.GeneratedAt != nil && !k.Servable()
}

// MarkRequested claims the row for an in-flight synthesis. The claim is what
// stops N concurrent tab reads from each firing their own LLM call.
func (k *GoalKnowledge) MarkRequested(at time.Time) {
	t := at.UTC()
	k.RequestedAt = &t
	if k.Status != StatusStale && k.Status != StatusSilent {
		// A stale row stays STALE so it keeps serving its last good text, and a
		// silent row stays SILENT so it keeps serving its last good ANSWER ("there
		// is nothing to say yet"). Only a never-generated row sits visibly pending.
		k.Status = StatusPending
	}
	k.UpdatedAt = time.Now().UTC()
}

// Invalidate marks the row stale, preserving the strongest reason seen. The text
// is deliberately KEPT so the row stays servable while a regen is pending.
func (k *GoalKnowledge) Invalidate(reason InvalidationReason) {
	if k.InvalidatedAt != nil && reasonPriority[reason] <= reasonPriority[k.InvalidationReason] {
		return // an equal-or-stronger signal already recorded — do not weaken it
	}
	now := time.Now().UTC()
	k.InvalidatedAt = &now
	k.InvalidationReason = reason
	if k.GeneratedAt != nil && k.Servable() {
		// Only a row with text to serve goes STALE. A row whose synthesis concluded
		// SILENT has no prose to serve stale — it keeps saying "silent", which is
		// still its last good answer, while InvalidatedAt records that the inputs
		// it looked at have since moved (that is what re-authorises a synthesis).
		k.Status = StatusStale
	}
	k.UpdatedAt = now
}

// IsCacheFresh reports a genuine cache hit: synthesised, not invalidated, and
// actually carrying text.
func (k *GoalKnowledge) IsCacheFresh() bool {
	return k.Status == StatusFresh && k.InvalidatedAt == nil && strings.TrimSpace(k.SynthesisText) != ""
}

// Servable reports whether there is any text worth showing the learner — true
// for a fresh row AND for a stale one (serve-stale-while-regen).
func (k *GoalKnowledge) Servable() bool {
	return strings.TrimSpace(k.SynthesisText) != ""
}

// SynthesisInFlight reports whether a synthesis request is already outstanding
// and has not yet timed out. It is the anti-stampede guard: N concurrent tab
// reads must not each spend an LLM call on the same (companion, goal).
func (k *GoalKnowledge) SynthesisInFlight(now time.Time, pendingTTL time.Duration) bool {
	return k.RequestedAt != nil && now.Sub(*k.RequestedAt) < pendingTTL
}

// NeedsSynthesis reports whether a synthesis should be REQUESTED right now.
//
// The order of the guards is the whole cost model:
//  1. A request already in flight (and not timed out) → no. This is what stops
//     concurrent tab reads from each spending an LLM call.
//  2. Never generated → yes, unconditionally.
//  3. Invalidated → only once the TTL floor has elapsed since the last
//     generation. chat_turn_completed fires every single turn, so without this
//     the floor the reflection would regenerate on every message.
//  4. A CONCLUDED SILENCE, never invalidated → no. See below.
//  5. Otherwise → only once max-age has elapsed.
func (k *GoalKnowledge) NeedsSynthesis(now time.Time, floor, maxAge, pendingTTL time.Duration) bool {
	if k.RequestedAt != nil && now.Sub(*k.RequestedAt) < pendingTTL {
		return false // in flight
	}
	if k.GeneratedAt == nil {
		return true // never generated
	}
	age := now.Sub(*k.GeneratedAt)
	if k.InvalidatedAt != nil {
		return age >= floor
	}
	// A concluded silence is an ANSWER, not a stale cache entry (CHO-2180). The
	// only thing that can change "there is nothing true to say about this goal" is
	// a change to what the model LOOKED AT — and every such change (a memory, a
	// weakness, progress, a chat turn, an eviction, a re-root) arrives as an
	// invalidation, which the branch above already handles. Re-running max-age over
	// a view that has not moved would spend a model call to be told the same thing
	// again, on a timer, forever.
	if k.ConcludedSilent() {
		return false
	}
	return age >= maxAge
}

// RootMatches reports whether the row was generated against the goal's CURRENT
// root concept. A re-rooted goal (ADR-214) is, for reflection purposes, about a
// different subtree — its cached text must be treated as invalid regardless of
// freshness.
func (k *GoalKnowledge) RootMatches(currentRootConceptID *string) bool {
	if k.RootConceptID == nil || currentRootConceptID == nil {
		return k.RootConceptID == nil && currentRootConceptID == nil
	}
	return strings.TrimSpace(*k.RootConceptID) == strings.TrimSpace(*currentRootConceptID)
}
