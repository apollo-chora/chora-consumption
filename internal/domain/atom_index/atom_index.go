// Package atom_index models a skinny per-tenant projection of LearningAtom
// metadata, hydrated from `chora.creation.atom.created.v1` events.
//
// Why a projection?
//
// chora-consumption MUST NOT cross-DB-query chora_creation (per ddd-
// enforcement HARD RULE). To grade an MCQ submission, we need to know the
// correct option id — but we don't want to denormalize the full atom
// content (per the atom-centric rule: collections query atoms; collections
// do NOT own them, and chora-consumption does not author content).
//
// The compromise (per `domain-content-consumption` skill anti-pattern list:
// "Fetching atom content synchronously from chora-creation (denormalize)")
// is a SKINNY index — only the fields chora-consumption actually needs:
//
//   - AtomID, TenantID, CourseID (cross-domain UUIDs, no FK)
//   - Title (display + audit)
//   - AtomType, Difficulty (drives composition + UI)
//   - TopicTags (drives TopicRetention + curiosity discovery)
//   - CorrectOptionID + AnswerCount (drives identity-based MCQ auto-grade —
//     CHO-1627: grade by stable option_id, NOT positional index)
//   - PublishedAt (recency)
//
// No body content, no rich media, no rendering hints. The full atom lives
// in chora_creation; the player fetches the body via HTTP at render time.
//
// Aggregate invariants:
//
//   - UUIDv7 atom_id (no FK)
//   - Soft-delete only (DeletedAt) — never hard delete (#5)
//   - Append-only on (AtomID, TenantID) — incoming `atom.revised.v1` events
//     replace this projection in-place; the canonical revision history
//     remains in chora_creation.
package atom_index

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors.
var (
	ErrInvalidAtom = errors.New("atom_index: invalid atom")
	ErrNotGradable = errors.New("atom_index: atom is not gradable (not an MCQ or correct option id unset)")
)

// Status is the projected lifecycle state of the source LearningAtom. It mirrors
// the creation-domain AtomStatus enum (draft → published → archived) but is
// stored as a stable lowercase string (the codebase string-enum convention —
// see user_knowledge_graph.NeighborRelation). Only a PUBLISHED, non-deleted atom
// is Playable() (serveable in the daily dose). atom.created seeds a row as
// StatusDraft; atom.published flips it to StatusPublished (CHO-1968).
type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

// AtomIndex is the read-side projection of one LearningAtom.
type AtomIndex struct {
	AtomID          string
	TenantID        string
	CourseID        string
	Title           string
	AtomType        string
	Difficulty      int
	TopicTags       []string
	CorrectOptionID string // "" for non-MCQ; stable option id for MCQ answer key
	AnswerCount     int    // total answer options for MCQ; 0 for non-MCQ
	// CognitiveLevel is the atom's ORIGINAL-Bloom label (knowledge ..
	// synthesis — the campaign Rung.Label() vocabulary), projected from
	// creation atom events (WS-C3, CHO-2082). "" = unknown (pre-0079 rows or
	// unlevelled atoms) and NEVER level-matches — the campaign question lane
	// honestly falls through to gap generation instead of serving a guess.
	CognitiveLevel string
	Status         Status // lifecycle state; only StatusPublished is Playable()
	PublishedAt    time.Time
	DeletedAt      *time.Time
}

// NewParams collects optional + required fields for projection construction.
type NewParams struct {
	AtomID          string
	TenantID        string
	CourseID        string
	Title           string
	AtomType        string
	Difficulty      int
	TopicTags       []string
	CorrectOptionID string
	AnswerCount     int
	Status          Status
	PublishedAt     time.Time
}

// New constructs a fresh AtomIndex projection.
//
// AtomID + TenantID + PublishedAt are required. TopicTags defaults to a
// non-nil empty slice so callers can range without nil-checks. Status defaults
// to StatusDraft when empty (the same nil-default idiom as TopicTags) — an
// atom is NOT playable until an atom.published event flips it (CHO-1968).
func New(p NewParams) (*AtomIndex, error) {
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, fmt.Errorf("%w: atom_id required", ErrInvalidAtom)
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidAtom)
	}
	if p.TopicTags == nil {
		p.TopicTags = []string{}
	}
	if p.Status == "" {
		p.Status = StatusDraft
	}
	tags := make([]string, len(p.TopicTags))
	copy(tags, p.TopicTags)
	return &AtomIndex{
		AtomID:          p.AtomID,
		TenantID:        p.TenantID,
		CourseID:        p.CourseID,
		Title:           p.Title,
		AtomType:        p.AtomType,
		Difficulty:      p.Difficulty,
		TopicTags:       tags,
		CorrectOptionID: p.CorrectOptionID,
		AnswerCount:     p.AnswerCount,
		Status:          p.Status,
		PublishedAt:     p.PublishedAt,
	}, nil
}

// IsMCQ returns true when the atom is an MCQ AND has a CorrectOptionID set
// (gradable server-side). Authoring-time MCQs without an answer key are
// treated as non-gradable until a revision adds one.
func (a *AtomIndex) IsMCQ() bool {
	return a.AtomType == "mcq" && a.CorrectOptionID != ""
}

// Playable reports whether the atom may be served to a learner: it must be
// PUBLISHED (the source LearningAtom transitioned DRAFT→PUBLISHED, mirrored
// here by an atom.published event) and not soft-deleted. A draft/archived or
// soft-deleted atom is never served in the daily dose (CHO-1968).
func (a *AtomIndex) Playable() bool {
	return a.Status == StatusPublished && a.DeletedAt == nil
}

// HasOpenEndedQuestion reports whether the atom carries an open-ended (non-MCQ,
// model-answer / rubric graded) question — i.e. it is answerable WITHOUT an MCQ
// answer key. The projection's atom_type is the canonical short type string
// emitted by protodecode.atomTypeToDomain, which maps every non-MCQ atom type to
// a DISTINCT non-empty string (fill_blank / true_false / short_answer / matching
// / ordering / code / essay / multimedia / simulation) — there is no single "oe"
// bucket. So "open-ended" = any non-empty type that is not "mcq".
func (a *AtomIndex) HasOpenEndedQuestion() bool {
	return a.AtomType != "" && a.AtomType != "mcq"
}

// IsAnswerable reports whether a learner can actually answer the atom: a gradable
// MCQ (type mcq + answer key) OR an open-ended question. A keyless MCQ or an
// empty/unknown type is NOT answerable, so it is filtered out of the daily dose
// (guards against serving an un-gradable dead-end atom).
func (a *AtomIndex) IsAnswerable() bool {
	return a.IsMCQ() || a.HasOpenEndedQuestion()
}

// Grade evaluates a server-side answer for an MCQ atom by stable option id
// identity (CHO-1627), NOT positional index. Returns (correct, nil) when
// gradable; (false, ErrNotGradable) when the atom is not an MCQ or has no
// correct option id.
//
// An empty selectedOptionID is never correct. AnswerCount is retained as
// projection metadata (display / audit) but no longer gates grading — the
// option id is the canonical source of truth.
func (a *AtomIndex) Grade(selectedOptionID string) (bool, error) {
	if !a.IsMCQ() {
		return false, ErrNotGradable
	}
	return selectedOptionID != "" && selectedOptionID == a.CorrectOptionID, nil
}

// SoftDelete marks the projection deleted without removing it. Hard delete
// is forbidden per ddd-enforcement invariant #5.
func (a *AtomIndex) SoftDelete(now time.Time) {
	a.DeletedAt = &now
}

// PrimaryTopic returns the first topic tag, or "" when none. Used by
// TopicRetention scoring to attribute a session-completed event to one
// canonical topic.
func (a *AtomIndex) PrimaryTopic() string {
	if len(a.TopicTags) == 0 {
		return ""
	}
	return a.TopicTags[0]
}

// bloomLevels is the ORIGINAL-Bloom 6-enum label set (ADR-156; the campaign
// D6 ladder speaks the same vocabulary — order differs from the proto enum
// NUMBERING, which is why levels travel as labels, never numbers).
var bloomLevels = map[string]bool{
	"knowledge": true, "comprehension": true, "application": true,
	"analysis": true, "synthesis": true, "evaluation": true,
}

// NormalizeCognitiveLevel maps any known representation — the lowercase
// label ("application") or the proto enum name
// ("COGNITIVE_LEVEL_APPLICATION") — onto the canonical lowercase label.
// Unknown/unspecified values normalize to "" (level unknown).
func NormalizeCognitiveLevel(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	v = strings.TrimPrefix(v, "cognitive_level_")
	if !bloomLevels[v] {
		return ""
	}
	return v
}

// MatchesCognitiveLevel reports whether the atom is KNOWN to sit at the
// target label. Unknown atom level or a blank target never matches.
func (a *AtomIndex) MatchesCognitiveLevel(label string) bool {
	want := NormalizeCognitiveLevel(label)
	if want == "" {
		return false
	}
	return NormalizeCognitiveLevel(a.CognitiveLevel) == want
}
