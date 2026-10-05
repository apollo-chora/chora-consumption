// Package proofingtest is the learner-owned Virgin Proofing Test aggregate
// (CHO-2040, owner rulings R8-6 + R8-7; spec
// docs/VIRGIN-PROOFING-TEST-SKILL-SPEC-2026-07-04.md).
//
// A ProofingTest records ONE learner-triggered (via their Companion) request to
// generate a fresh MCQ+OE assessment targeting the growth edges the learner
// ticked at the goal-binding ceremony (intent-tagged ConceptNodes in the
// goal's root subtree). Per R8-6 it is a COMPOSED goal-driven runner — NO
// catalogue Skill row, NO grant/equip/stage gate (precedent: the ceremony
// edge-scout). The generation machinery is the LIVE qgen crew +
// TestSetComposer, reached by publishing ONE chora.creation.ai_assist
// .started.v2 batch event whose field 19 `target_growth_edges` carries the
// edges' CONCEPT KEYS.
//
// R8-7 (locked): the composer passes concept KEYS, never titles. A ticked
// edge without a usable key makes the whole request refuse loudly
// (ErrEdgesLackKeys → HTTP 422 EDGES_LACK_KEYS) — the KG session is adding
// key-at-mint separately; until those keys exist the runner must fail
// honestly, never silently substitute titles.
//
// HEXAGONAL purity: stdlib-only, NO infra imports (mirrors goal +
// concept_graph). No time.Now() leak — every clock-dependent call takes a
// `now` (the constructor falls back to wall-clock only when Now is zero). Per
// ddd-enforcement: soft-delete only (#5); new ids are UUIDv7 (#7); goal /
// companion / concept / assist references are opaque UUIDs without FK (#3).
package proofingtest

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Status is the ProofingTest lifecycle state.
//
//	requested → the started.v2 batch request is published; awaiting the crew
//	composing → optional intermediate (progress-driven; not yet wired)
//	ready     → the TestSetComposer's completed.v1 landed; payload stored
//	failed    → the batch was refused / the request could not be published
type Status string

const (
	StatusRequested Status = "requested"
	StatusComposing Status = "composing"
	StatusReady     Status = "ready"
	StatusFailed    Status = "failed"
)

// Valid reports whether s is a recognised status.
func (s Status) Valid() bool {
	switch s {
	case StatusRequested, StatusComposing, StatusReady, StatusFailed:
		return true
	}
	return false
}

// terminal reports whether s admits no further transitions.
func (s Status) terminal() bool { return s == StatusReady || s == StatusFailed }

// Plan bounds. Defaults follow the spec §1 mixed-set shape (a small proving
// set, MCQ-weighted); the caps keep a single request inside the qgen batch
// lane's sane envelope (orchestrator hard-caps at 50 — we stay far below).
const (
	DefaultMCQCount = 4
	DefaultOECount  = 2
	MaxPerTypeCount = 10
)

// Sentinel errors — wrapped with %w so the HTTP adapter maps each to its own
// status code.
var (
	ErrInvalid = errors.New("proofingtest: invalid")
	// ErrNoTickedEdges — the goal has no intent-tagged (remediate|explore)
	// learning edges in its root subtree; there is nothing to prove. 422.
	ErrNoTickedEdges = errors.New("proofingtest: goal has no ticked learning edges")
	// ErrEdgesLackKeys — R8-7: one or more ticked edges carry no concept key.
	// The composer passes KEYS, never titles — refuse loudly. 422 EDGES_LACK_KEYS.
	ErrEdgesLackKeys = errors.New("proofingtest: ticked learning edges lack concept keys")
	// ErrTerminal — the proofing test already reached ready|failed.
	ErrTerminal = errors.New("proofingtest: proofing test is terminal")
	// ErrDeleted — the row is soft-deleted.
	ErrDeleted = errors.New("proofingtest: proofing test is soft-deleted")
	// ErrDuplicateInFlight — a non-terminal proofing test with the same
	// (learner, goal, edge signature) already exists. The DB partial-unique
	// index (migration 0069) enforces this as the concurrency backstop behind
	// the handler's read-probe; on it the handler returns the in-flight row
	// instead of minting + charging a duplicate.
	ErrDuplicateInFlight = errors.New("proofingtest: duplicate in-flight proofing test")
)

// ErrInsufficientMana is the typed reserve refusal (mirrors
// companion.ErrInsufficientMana without importing it — the domain stays
// stdlib-only; the clients adapter translates).
type ErrInsufficientMana struct {
	RequiredUnits  int64
	CurrentBalance int64
}

func (e *ErrInsufficientMana) Error() string {
	return fmt.Sprintf("proofingtest: insufficient mana (required %d, balance %d)",
		e.RequiredUnits, e.CurrentBalance)
}

// TargetEdge is one ticked ceremony learning-edge the generated set must
// target. Key is the qgen `target_growth_edges` concept key (R8-7 — the wire
// payload; REQUIRED). Title + Intent ride for display/audit only.
type TargetEdge struct {
	ConceptID string `json:"concept_id"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	Intent    string `json:"intent"`
}

// ValidateTargetEdges enforces the R8-7 gate + structural sanity:
//   - at least one edge (else ErrNoTickedEdges);
//   - every edge names a UUID-shaped concept + a non-empty title (ErrInvalid);
//   - every edge carries a non-blank Key — else ErrEdgesLackKeys naming the
//     offending titles, so the refusal tells the learner WHICH edges are
//     un-keyed.
func ValidateTargetEdges(edges []TargetEdge) error {
	if len(edges) == 0 {
		return ErrNoTickedEdges
	}
	var unkeyed []string
	for i, e := range edges {
		if !isUUIDShaped(strings.TrimSpace(e.ConceptID)) {
			return fmt.Errorf("%w: edge[%d] concept_id %q is not a UUID", ErrInvalid, i, e.ConceptID)
		}
		title := strings.TrimSpace(e.Title)
		if title == "" {
			return fmt.Errorf("%w: edge[%d] has no title", ErrInvalid, i)
		}
		if strings.TrimSpace(e.Key) == "" {
			unkeyed = append(unkeyed, title)
		}
	}
	if len(unkeyed) > 0 {
		return fmt.Errorf("%w: %s (the KG key-at-mint change has not landed for these edges)",
			ErrEdgesLackKeys, strings.Join(unkeyed, "; "))
	}
	return nil
}

// NormalizeTitle folds a learning-edge title to its comparison key: trimmed,
// internal whitespace collapsed, lower-cased. It is the edge's learner-facing
// identity for within-request dedupe (NOT stored — the original casing is
// kept on the surviving edge).
func NormalizeTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// DedupeTargetEdges collapses ticked edges that share a NormalizeTitle,
// keeping the FIRST occurrence (its intent + key + original title win) and
// preserving order. Cross-ceremony mints can surface the same concept twice
// (distinct concept ids/keys, same display title); a proving set must probe
// each concept once — mirrors the ceremony mint's own within-batch title
// dedupe. Pure; never mutates the input (returns a fresh slice).
func DedupeTargetEdges(edges []TargetEdge) []TargetEdge {
	if len(edges) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(edges))
	out := make([]TargetEdge, 0, len(edges))
	for _, e := range edges {
		key := NormalizeTitle(e.Title)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}

// EdgeSignature is the cross-request idempotency key: the goal id joined with
// the sorted, normalized, deduped edge titles. Order- and duplicate-
// independent, so a re-submitted identical request (same goal + same ticked
// concepts, any order) maps to the same signature. The handler uses it to
// return an in-flight duplicate instead of charging mana twice. Pure.
func EdgeSignature(goalID string, edges []TargetEdge) string {
	seen := make(map[string]struct{}, len(edges))
	titles := make([]string, 0, len(edges))
	for _, e := range edges {
		key := NormalizeTitle(e.Title)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		titles = append(titles, key)
	}
	sort.Strings(titles)
	// \x1f/\x1e (unit/record separators) can't occur in a whitespace-collapsed
	// title, so the join is unambiguous.
	return strings.TrimSpace(goalID) + "\x1f" + strings.Join(titles, "\x1e")
}

// ProofingTest is the aggregate root (consumption-owned; the takeable learner
// surface — NOT delivery's instructor TestSet, cross-DB forbidden).
type ProofingTest struct {
	ID          string
	TenantID    string
	LearnerGCID string
	GoalID      string
	CompanionID string
	// AssistID is the chora.creation.ai_assist batch request ref (started.v2
	// assist_id); the terminal completed/refused events key back on it.
	AssistID    string
	Status      Status
	TargetEdges []TargetEdge
	// TestSetRef is a future cross-aggregate reference to a materialised
	// test-set object. The LIVE completed.v1 event embeds the composed set
	// inline (candidate_payload_json.proposed_test_set) — stored below — and
	// carries NO separate ref, so this stays nil today.
	TestSetRef *string
	// TestSetPayload is the composed batch payload
	// {"candidates":[...],"proposed_test_set":{...}} stored verbatim on
	// ready — the learner take-surface renders from it.
	TestSetPayload []byte
	FailureReason  string
	// ManaReserved + ReservationID stamp the reserve→settle/refund economy
	// (spec §3): the pre-flight debit's displayed price + its idempotency
	// handle, so a refused batch refunds against the exact reservation.
	ManaReserved  int64
	ReservationID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}

// NewRequestedInput is the constructor input.
type NewRequestedInput struct {
	TenantID      string
	LearnerGCID   string
	GoalID        string
	CompanionID   string
	TargetEdges   []TargetEdge
	ManaReserved  int64
	ReservationID string
	Now           time.Time
}

// NewRequested mints a requested ProofingTest. The aggregate id and the
// ai_assist batch ref are BOTH freshly minted UUIDv7s (distinct — the batch
// ref travels cross-domain; the aggregate id stays learner-local).
func NewRequested(in NewRequestedInput) (*ProofingTest, error) {
	for name, v := range map[string]string{
		"tenant_id":    in.TenantID,
		"learner_gcid": in.LearnerGCID,
		"goal_id":      in.GoalID,
		"companion_id": in.CompanionID,
	} {
		if !isUUIDShaped(strings.TrimSpace(v)) {
			return nil, fmt.Errorf("%w: %s must be a UUID, got %q", ErrInvalid, name, v)
		}
	}
	if err := ValidateTargetEdges(in.TargetEdges); err != nil {
		return nil, err
	}
	if in.ManaReserved < 0 {
		return nil, fmt.Errorf("%w: mana_reserved must be >= 0", ErrInvalid)
	}
	now := resolveNow(in.Now)
	// Validation ran on the raw set above (R8-7 stays strict); dedupe only the
	// STORED/published set so a proving request probes each concept once.
	edges := DedupeTargetEdges(in.TargetEdges)
	return &ProofingTest{
		ID:            domain.NewUUIDv7(),
		TenantID:      strings.TrimSpace(in.TenantID),
		LearnerGCID:   strings.TrimSpace(in.LearnerGCID),
		GoalID:        strings.TrimSpace(in.GoalID),
		CompanionID:   strings.TrimSpace(in.CompanionID),
		AssistID:      domain.NewUUIDv7(),
		Status:        StatusRequested,
		TargetEdges:   edges,
		ManaReserved:  in.ManaReserved,
		ReservationID: strings.TrimSpace(in.ReservationID),
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// MarkComposing advances requested → composing (progress signal; optional).
func (p *ProofingTest) MarkComposing(now time.Time) error {
	if err := p.ensureLiveNonTerminal(); err != nil {
		return err
	}
	p.Status = StatusComposing
	p.touch(now)
	return nil
}

// MarkReady lands the composed test set: requested|composing → ready. The
// payload is REQUIRED (an empty completion would fabricate a takeable test).
func (p *ProofingTest) MarkReady(testsetPayload []byte, now time.Time) error {
	if err := p.ensureLiveNonTerminal(); err != nil {
		return err
	}
	if len(testsetPayload) == 0 {
		return fmt.Errorf("%w: ready requires the composed test-set payload", ErrInvalid)
	}
	p.Status = StatusReady
	p.TestSetPayload = append([]byte(nil), testsetPayload...)
	p.touch(now)
	return nil
}

// MarkFailed lands a refusal / publish failure: requested|composing → failed.
func (p *ProofingTest) MarkFailed(reason string, now time.Time) error {
	if err := p.ensureLiveNonTerminal(); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: failed requires a reason", ErrInvalid)
	}
	p.Status = StatusFailed
	p.FailureReason = reason
	p.touch(now)
	return nil
}

// Terminal reports whether the proofing test reached ready|failed.
func (p *ProofingTest) Terminal() bool { return p.Status.terminal() }

// SoftDelete tombstones the row (idempotent; never hard-deletes, #5).
func (p *ProofingTest) SoftDelete(now time.Time) {
	if p.DeletedAt != nil {
		return
	}
	n := resolveNow(now)
	p.DeletedAt = &n
	p.UpdatedAt = n
}

func (p *ProofingTest) ensureLiveNonTerminal() error {
	if p.DeletedAt != nil {
		return ErrDeleted
	}
	if p.Status.terminal() {
		return fmt.Errorf("%w: status %q", ErrTerminal, p.Status)
	}
	return nil
}

func (p *ProofingTest) touch(now time.Time) { p.UpdatedAt = resolveNow(now) }

// ---------------------------------------------------------------------------
// Plan + prompt engines (pure).
// ---------------------------------------------------------------------------

// TypeQuota mirrors the started.v2 GenerationTypeQuota wire entry (field 21).
type TypeQuota struct {
	QuestionType string
	Count        int
}

// BuildTypePlan derives the mixed MCQ+OE plan from the (optional) requested
// counts. Both zero ⇒ the default 4 MCQ + 2 OE proving set. Either count may
// be zero (single-type set); negatives and per-type counts above
// MaxPerTypeCount refuse loudly.
func BuildTypePlan(mcq, oe int) ([]TypeQuota, error) {
	if mcq < 0 || oe < 0 {
		return nil, fmt.Errorf("%w: counts must be >= 0 (mcq=%d, oe=%d)", ErrInvalid, mcq, oe)
	}
	if mcq > MaxPerTypeCount || oe > MaxPerTypeCount {
		return nil, fmt.Errorf("%w: counts capped at %d per type (mcq=%d, oe=%d)",
			ErrInvalid, MaxPerTypeCount, mcq, oe)
	}
	if mcq == 0 && oe == 0 {
		mcq, oe = DefaultMCQCount, DefaultOECount
	}
	plan := make([]TypeQuota, 0, 2)
	if mcq > 0 {
		plan = append(plan, TypeQuota{QuestionType: "mcq", Count: mcq})
	}
	if oe > 0 {
		plan = append(plan, TypeQuota{QuestionType: "oe", Count: oe})
	}
	return plan, nil
}

// RequestedCount sums the plan (started.v2 requested_count == sum(quota)).
func RequestedCount(plan []TypeQuota) int {
	total := 0
	for _, q := range plan {
		total += q.Count
	}
	return total
}

// BuildPrompt renders the deterministic generator instruction for the qgen
// crew. The uploaded-material seam is NOT used (no source_files) — the set is
// grounded on the goal anchor + the ticked edges' concept keys, which ALSO
// ride wire field 19 so the generator's growth-edge bias seam
// (consumption-growth-edges.yaml) sees them structurally.
func BuildPrompt(goalTitle string, conceptSet []string, edges []TargetEdge, plan []TypeQuota) string {
	var b strings.Builder
	b.WriteString("Compose a Virgin Proofing Test — a fresh assessment the learner takes to prove readiness")
	if t := strings.TrimSpace(goalTitle); t != "" {
		b.WriteString(" for the goal \"")
		b.WriteString(t)
		b.WriteString("\"")
	}
	b.WriteString(".\n")
	if len(conceptSet) > 0 {
		cleaned := make([]string, 0, len(conceptSet))
		for _, c := range conceptSet {
			if c = strings.TrimSpace(c); c != "" {
				cleaned = append(cleaned, c)
			}
		}
		if len(cleaned) > 0 {
			b.WriteString("Goal theme concepts: ")
			b.WriteString(strings.Join(cleaned, ", "))
			b.WriteString(".\n")
		}
	}
	b.WriteString("Target growth edges (every question must probe one of these, by concept key):\n")
	for _, e := range edges {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(e.Key))
		b.WriteString(" (")
		b.WriteString(strings.TrimSpace(e.Title))
		if intent := strings.TrimSpace(e.Intent); intent != "" {
			b.WriteString("; intent=")
			b.WriteString(intent)
		}
		b.WriteString(")\n")
	}
	b.WriteString("Plan: ")
	parts := make([]string, 0, len(plan))
	for _, q := range plan {
		parts = append(parts, strconv.Itoa(q.Count)+" "+strings.ToUpper(q.QuestionType))
	}
	b.WriteString(strings.Join(parts, " + "))
	b.WriteString(" questions. Spread coverage across the edges; remediate-intent edges probe the misconception directly, explore-intent edges stretch one adjacent step.")
	return b.String()
}

// ---------------------------------------------------------------------------
// Shared helpers.
// ---------------------------------------------------------------------------

func resolveNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

// isUUIDShaped reports whether s is a hyphenated 8-4-4-4-12 hex UUID (mirrors
// concept_graph's validator; duplicated to keep the package stdlib-only).
func isUUIDShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(r) {
				return false
			}
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
