// Package campaignquestion — the WS-C3 campaign question-set aggregate
// (CHO-2082, ADR-227 D13 + addendum #4).
//
// ADR-202 §4's per-learner QuestionBank is UNBUILT, so the campaign's
// generated ladder questions persist here: one live set per
// (tenant, learner, concept, rung) carrying
//
//   - the level-matched RETRIEVAL cache (retrieved atom ids — the
//     drillcache idiom applied to the focus node's theme), and
//   - the qgen GAP-FILL state machine: idle → requested → ready | failed.
//     ready is permanent ("generate once, retrieve forever" — refreshers
//     and re-climbs serve the stored payload at zero LLM cost); failed may
//     honestly re-request on a later day (the daily cap decides when).
//
// The lane is mana-EXEMPT (D13 — the Campaign is core game, not premium)
// and deterministic on the consumption side: the LLM authors questions,
// never schedules (ADR-202). Rungs and prompts speak the atom-domain
// ORIGINAL-Bloom vocabulary (addendum #4's two-vocabulary rule).
package campaignquestion

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
)

// ErrInvalid is the package's validation sentinel.
var ErrInvalid = errors.New("campaignquestion: invalid")

// Status is the qgen gap-fill lane state.
type Status string

const (
	// StatusIdle — no generation attempted (retrieval may still cache).
	StatusIdle Status = "idle"
	// StatusRequested — a batch request is in flight (assist_id keys the
	// shared ai_assist terminal topics; no second publish until terminal —
	// except a STALE request from an earlier UTC day, whose lost terminal
	// may be superseded via MarkRequested).
	StatusRequested Status = "requested"
	// StatusReady — the payload landed; served forever (permanent).
	StatusReady Status = "ready"
	// StatusFailed — the crew refused/errored; may re-request later.
	StatusFailed Status = "failed"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusIdle, StatusRequested, StatusReady, StatusFailed:
		return true
	}
	return false
}

// RequestOrigin records WHICH entrance requested a generation, so the two
// entrances draw on SEPARATE daily budgets (ADR-227 D13):
//   - OriginMarch: the automatic daily dose-march feeder (one node/day).
//   - OriginTap:   an explicit "Practice this hex" tap (learner intent).
//
// The origin is a property of the REQUEST cycle: New seeds the march default
// (an idle row is never counted), and the sole requester stamps the authoritative
// origin at request time alongside the request day. The persisted CHECK mirrors
// this value set.
type RequestOrigin string

const (
	OriginMarch RequestOrigin = "march"
	OriginTap   RequestOrigin = "tap"
)

// Valid reports a known request origin.
func (o RequestOrigin) Valid() bool {
	switch o {
	case OriginMarch, OriginTap:
		return true
	}
	return false
}

// D13 generation bounds, split per request origin so an explicit hex tap is
// never starved by the automatic dose-march. One request asks
// QuestionsPerRequest questions; the caps bound requests, so LLM spend is
// bounded at (march+tap) requests/learner/UTC day.
//   - march: the automatic daily dose feeder, one request/learner/day.
//   - tap:   an explicit practice tap, a modest, separate budget/day. It is a
//     domain constant (surface it to the owner to tune).
const (
	QuestionsPerRequest  = 2
	DefaultDailyMarchCap = 1
	DefaultDailyTapCap   = 3
)

// DailyCapFor returns the per-UTC-day request cap for one origin (an unknown
// origin is treated as march, the conservative default).
func DailyCapFor(origin RequestOrigin) int {
	if origin == OriginTap {
		return DefaultDailyTapCap
	}
	return DefaultDailyMarchCap
}

// CanRequestToday applies the D13 daily bound for one origin to a learner's
// SAME-ORIGIN request count for the current UTC day. The tap budget is
// independent of the march budget; a spent march never blocks a tap.
func CanRequestToday(origin RequestOrigin, requestsToday int) bool {
	return requestsToday < DailyCapFor(origin)
}

// QuestionSet is the aggregate root (table campaign_question_sets).
type QuestionSet struct {
	ID          string
	TenantID    string
	LearnerGCID string
	ConceptID   string
	ConceptKey  string
	Rung        int // 1..6 ORIGINAL-Bloom ladder position (D6)

	RetrievedAtomIDs   []string
	RetrievalCheckedAt *time.Time

	GenerationStatus Status
	AssistID         string
	RequestedOn      *time.Time    // DATE, daily-cap accounting
	RequestOrigin    RequestOrigin // WHICH entrance requested (daily-budget axis)
	QuestionsPayload []byte        // BatchCandidatePayload JSON verbatim
	FailureReason    string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// New mints a fresh idle set for one (learner, concept, rung).
func New(tenantID, learnerGCID, conceptID, conceptKey string, rung int, now time.Time) (*QuestionSet, error) {
	for name, v := range map[string]string{
		"tenant_id": tenantID, "learner_gcid": learnerGCID,
		"concept_id": conceptID, "concept_key": conceptKey,
	} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%w: %s required", ErrInvalid, name)
		}
	}
	if rung < 1 || rung > 6 {
		return nil, fmt.Errorf("%w: rung %d outside the 1..6 ladder", ErrInvalid, rung)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &QuestionSet{
		ID:               domain.NewUUIDv7(),
		TenantID:         tenantID,
		LearnerGCID:      learnerGCID,
		ConceptID:        conceptID,
		ConceptKey:       conceptKey,
		Rung:             rung,
		GenerationStatus: StatusIdle,
		RequestOrigin:    OriginMarch, // default; the requester restamps at request time
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// SetRetrieved replaces the level-matched retrieval cache.
func (s *QuestionSet) SetRetrieved(atomIDs []string, now time.Time) {
	s.RetrievedAtomIDs = append([]string(nil), atomIDs...)
	t := now
	s.RetrievalCheckedAt = &t
	s.touch(now)
}

// MinGenRealAge floors how long an in-flight generation must have actually run
// (real wall-clock) before a dose-day rollover may declare it lost. qgen takes
// ~2min; the dose day, under the accelerated test clock (CHORA_DOSE_DAY_SECONDS),
// can be seconds, so without this floor the practice-lane poll loop would
// supersede a healthy run on every tick and its terminal would never match. At
// product speed it is inert for a genuinely day-old request, and it also fixes
// the near-midnight edge (a request stamped at 23:59 is not "lost" at 00:00).
const MinGenRealAge = 10 * time.Minute

// StaleRequested reports an in-flight request whose terminal was lost (pod
// death, lost subscription, mesh-403 window), so the set would otherwise block
// its (concept, rung) forever. Lost requires BOTH the dose day to be over AND
// the request to have actually run past MinGenRealAge: a same-dose-day or
// still-young request is honestly in flight. A requested set with no day stamp
// is an orphan and counts as stale. UpdatedAt is the request stamp for a
// StatusRequested set (MarkRequested touches it).
func (s *QuestionSet) StaleRequested(now time.Time) bool {
	if s.GenerationStatus != StatusRequested {
		return false
	}
	if s.RequestedOn == nil {
		return true
	}
	return s.RequestedOn.Before(doseclock.Bucket(now)) && now.Sub(s.UpdatedAt) >= MinGenRealAge
}

// MarkRequested moves idle|failed → requested, keying the set to the batch
// assist id and stamping the request day (daily-cap accounting). A same-day
// in-flight or ready set refuses — no duplicate publishes, no re-spend on a
// permanently-served set. A STALE in-flight set (StaleRequested — its
// terminal was lost on an earlier day) may be superseded: the new assist id
// replaces the lost one, and the old terminal, if it ever arrives, no longer
// matches any set (ack-skip).
func (s *QuestionSet) MarkRequested(assistID string, now time.Time) error {
	if strings.TrimSpace(assistID) == "" {
		return fmt.Errorf("%w: assist_id required", ErrInvalid)
	}
	switch s.GenerationStatus {
	case StatusIdle, StatusFailed:
		// legal
	case StatusRequested:
		if !s.StaleRequested(now) {
			return fmt.Errorf("%w: generation already in flight (assist %s)", ErrInvalid, s.AssistID)
		}
		// stale — supersede the lost request
	case StatusReady:
		return fmt.Errorf("%w: set is ready — reuse it, never regenerate", ErrInvalid)
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, s.GenerationStatus)
	}
	day := doseclock.Bucket(now)
	s.GenerationStatus = StatusRequested
	s.AssistID = assistID
	s.RequestedOn = &day
	s.FailureReason = ""
	s.touch(now)
	return nil
}

// MarkReady lands the composed payload (requested → ready). A blank payload
// refuses — never a fabricated ready (AC4 fail-loud).
func (s *QuestionSet) MarkReady(payload []byte, now time.Time) error {
	if s.GenerationStatus != StatusRequested {
		return fmt.Errorf("%w: ready only lands on a requested set (status %q)", ErrInvalid, s.GenerationStatus)
	}
	if strings.TrimSpace(string(payload)) == "" {
		return fmt.Errorf("%w: ready requires a non-empty payload", ErrInvalid)
	}
	s.GenerationStatus = StatusReady
	s.QuestionsPayload = append([]byte(nil), payload...)
	s.touch(now)
	return nil
}

// MarkFailed records a crew refusal/error (requested → failed).
func (s *QuestionSet) MarkFailed(reason string, now time.Time) error {
	if s.GenerationStatus != StatusRequested {
		return fmt.Errorf("%w: failed only lands on a requested set (status %q)", ErrInvalid, s.GenerationStatus)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "qgen batch failed"
	}
	s.GenerationStatus = StatusFailed
	s.FailureReason = reason
	s.touch(now)
	return nil
}

// CanServe reports whether the set carries a servable payload.
func (s *QuestionSet) CanServe() bool {
	return s.GenerationStatus == StatusReady && len(s.QuestionsPayload) > 0
}

func (s *QuestionSet) touch(now time.Time) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.UpdatedAt = now
}

// BuildPrompt writes the campaign generation prompt: the focus node's theme
// (title + concept key) at ONE original-Bloom level, count questions, MCQ
// with a single correct option. Mirrors proofingtest.BuildPrompt's register
// (the qgen_question/qgen_critic pair reads the same contract; the level
// also rides metadata + the type plan on the wire).
func BuildPrompt(nodeTitle, conceptKey, rungLabel string, count int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Compose %d multiple-choice questions drilling the learner's campaign node %q (concept key: %s).\n", count, nodeTitle, conceptKey)
	fmt.Fprintf(&b, "Every question MUST target the %q cognitive level (original Bloom taxonomy) — do not mix levels.\n", rungLabel)
	b.WriteString("Each question needs exactly one correct option and plausible distractors, grounded in the node's theme.\n")
	return b.String()
}
