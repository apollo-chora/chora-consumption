// Package growth_edge_output is the generated-artifact aggregate for the
// Growth-Edge layer (ADR-205 WS-7, CHO-2348).
//
// When a learner confirms a HITL Growth-Edge review they may select METERED
// outputs: study_aids (advice / glossary / cheat-sheet prose) and practice_test
// (a critic-gated, edge-scoped set). The ai-kernel weakness-analyser crew really
// does generate them and really does charge the learner's mana. Until this
// package existed they then reached NO learner surface: the crew's detached
// generation awaited the generator and discarded the result. The learner paid
// and received nothing.
//
// These artifacts are READ-ONLY per the owner decision of 2026-07-23: a
// practice_test is displayed, never attempted. Nothing here couples to the
// ADR-246 atom_attempt contracts, and no mastery signal is derived from them.
//
// Fed by chora.consumption.weakness.outputs_generated.v1; read by A+.
package growth_edge_output

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind discriminates the artifact for rendering. The set is TOTAL over the
// producer's METERED_OUTPUT_KINDS (orchestrator
// domain/weakness_analyser_crew/panel.py); a kind added there and not here is
// silently dropped by the projection, which is how the learner ends up paying
// for something invisible.
type Kind string

const (
	// KindStudyAids is prose: advice / glossary / cheat-sheet.
	KindStudyAids Kind = "study_aids"
	// KindPracticeTest is the critic-gated, edge-scoped composed set.
	KindPracticeTest Kind = "practice_test"
	// KindCompanionVoice is the Companion's spoken-register reflection on the
	// diagnosis (ADR-254 D4: the diagnosis crew's voice step, dispatched as a
	// companion_chat turn_kind voice; migration 0113 widens the DB CHECK).
	KindCompanionVoice Kind = "companion_voice"
)

// AllKinds returns every artifact kind this domain accepts. Derived from the
// constants rather than hand-listed at each call site so a new kind cannot be
// half-added.
func AllKinds() []Kind { return []Kind{KindStudyAids, KindPracticeTest, KindCompanionVoice} }

// ErrUnknownKind is returned for a kind outside AllKinds.
var ErrUnknownKind = errors.New("growth_edge_output: unknown kind")

// ParseKind validates a wire kind string. Deliberately exact-match: the DB
// CHECK is exact too, so a lenient parse here would only move a 23514 later.
func ParseKind(s string) (Kind, error) {
	for _, k := range AllKinds() {
		if string(k) == s {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownKind, s)
}

// Output is one generated artifact belonging to one learner's upload.
type Output struct {
	OutputID    string
	TenantID    string
	LearnerGCID string
	UploadID    string
	Kind        Kind
	// Content is the artifact as JSON: a JSON string for study_aids prose, a
	// JSON object for practice_test. Stored in one JSONB column so an artifact
	// gaining a field is not a migration.
	Content json.RawMessage
	// Metered records whether the learner spent mana on this kind, so the
	// surface can be honest about what was paid for.
	Metered bool
	// SourceEventID records WHICH delivery produced this row, so a duplicate is
	// diagnosable rather than merely absent.
	SourceEventID string
	GeneratedAt   time.Time
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

// NewArgs is the constructor input.
type NewArgs struct {
	OutputID      string
	TenantID      string
	LearnerGCID   string
	UploadID      string
	Kind          Kind
	Content       json.RawMessage
	Metered       bool
	SourceEventID string
	GeneratedAt   time.Time
	CreatedAt     time.Time
}

// New validates and constructs an Output.
//
// Fail-loud at the domain boundary rather than at the INSERT: an unknown kind
// would 23514 and malformed content would 22P02, both surfacing as an opaque
// projection failure that NACKs and redelivers forever.
func New(a NewArgs) (Output, error) {
	outputID := strings.TrimSpace(a.OutputID)
	tenantID := strings.TrimSpace(a.TenantID)
	learnerGCID := strings.TrimSpace(a.LearnerGCID)
	uploadID := strings.TrimSpace(a.UploadID)

	// Each of these makes the artifact unattributable: unscopable by RLS,
	// uncorrelatable to its upload, or unshowable to its owner.
	for _, f := range []struct {
		name, val string
	}{
		{"output_id", outputID},
		{"tenant_id", tenantID},
		{"learner_gcid", learnerGCID},
		{"upload_id", uploadID},
	} {
		if f.val == "" {
			return Output{}, fmt.Errorf("growth_edge_output: %s required", f.name)
		}
	}

	kind, err := ParseKind(string(a.Kind))
	if err != nil {
		return Output{}, err
	}

	// An artifact with no content is the WS-7 gap wearing a row: it looks
	// delivered and shows the learner nothing.
	if len(strings.TrimSpace(string(a.Content))) == 0 {
		return Output{}, errors.New("growth_edge_output: content required")
	}
	if !json.Valid(a.Content) {
		return Output{}, errors.New("growth_edge_output: content is not valid JSON")
	}

	createdAt := a.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	generatedAt := a.GeneratedAt
	if generatedAt.IsZero() {
		// The column is NOT NULL and a zero time renders as year 1 on the
		// surface, so fall back to the projection stamp rather than write it.
		generatedAt = createdAt
	}

	return Output{
		OutputID:      outputID,
		TenantID:      tenantID,
		LearnerGCID:   learnerGCID,
		UploadID:      uploadID,
		Kind:          kind,
		Content:       a.Content,
		Metered:       a.Metered,
		SourceEventID: strings.TrimSpace(a.SourceEventID),
		GeneratedAt:   generatedAt,
		CreatedAt:     createdAt,
	}, nil
}
