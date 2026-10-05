// Package student_transcript models the per-learner StudentTranscript
// read-model (W6 Slice 1, Four-Mode plan "Outcome spine").
//
// It is a PROJECTION, never a write-model: chora-consumption owns none of the
// underlying graded outcomes — assessments + certifications live in
// chora_delivery and cross-DB queries are FORBIDDEN (ddd-enforcement #1). The
// transcript is fed ONLY by verified Pub/Sub events
// (chora.delivery.submission.graded.v1, chora.delivery.certification.issued.v1)
// fanned out from the SAME push endpoints the LearnerProfile projection uses
// (see adapter/http/learner_profile_push_handler.go) — no new Pub/Sub
// subscriptions were provisioned for this slice.
//
// One TranscriptEntry = one graded outcome (an assessment submission or an
// issued certification). IdempotencyKey is a deterministic natural key
// derived from the source aggregate (e.g. "submission:<id>:graded" or
// "cert:<id>:issued") — NOT the volatile event_id — so both a true Pub/Sub
// redelivery AND a legitimate re-grade of the same submission converge on the
// SAME row (upsert), matching the unique index the migration declares.
//
// HEXAGONAL purity: stdlib-only, NO infra imports, NO time.Now() leaks — every
// clock-dependent call takes a `Now` (mirrors internal/domain/learner_profile +
// topic_retention). Per ddd-enforcement: new rows use UUIDv7 (#7); gcid +
// cross-domain ref ids (source_ref/course_id) are opaque UUIDs without FK
// constraints (#3). This read-model has no delete path (it's an append/upsert
// projection); soft-delete (#5) is not applicable here.
package student_transcript

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Kind enumerates the two graded-outcome shapes the transcript projects.
type Kind string

const (
	KindAssessment    Kind = "assessment"
	KindCertification Kind = "certification"
)

// Valid reports whether k is a recognised transcript-entry kind.
func (k Kind) Valid() bool {
	switch k {
	case KindAssessment, KindCertification:
		return true
	}
	return false
}

// DeliveryType is the delivery MODE that produced an outcome, snapshotted at
// grade time from the parent Offering (ADR-190 D1 puts delivery_type on the
// Offering so one Course stays reusable across modes).
//
// It is ORTHOGONAL to Kind, not a widening of it: Kind says WHAT the outcome is
// (an assessment or a certification), DeliveryType says WHICH MODE delivered it.
// A certification issued off a graduate offering is both. Collapsing them into
// one column would lose one of the two facts.
//
// This is the axis §10.6 criterion 1 turns on. Without it a graduate assessment
// and a short-course grade are both kind='assessment' and indistinguishable, so
// the unified transcript can evidence 2 kinds but never 2 modes.
//
// ⚠ These values MIRROR a vocabulary chora_delivery owns; consumption does not
// control them (cross-DB reads are forbidden, so this is the only copy this side
// of the boundary). DeliveryType_MirrorsTheContract pins them as the tripwire if
// delivery ever renames one.
type DeliveryType string

const (
	// DeliveryTypeGraduate — multi-section graduate-programme cohort.
	DeliveryTypeGraduate DeliveryType = "graduate"
	// DeliveryTypeShort — short-course / single-cohort run.
	DeliveryTypeShort DeliveryType = "short"
	// DeliveryTypeAsync — self-paced async offering.
	DeliveryTypeAsync DeliveryType = "async"
)

// Valid reports whether dt is a mode this contract recognises.
//
// "" is deliberately INVALID: it is the ABSENCE of a mode (a freestanding
// assessment has no Offering), not a mode of its own. Callers normalise an
// invalid value to "" rather than reject it — see New.
func (dt DeliveryType) Valid() bool {
	switch dt {
	case DeliveryTypeGraduate, DeliveryTypeShort, DeliveryTypeAsync:
		return true
	}
	return false
}

// TranscriptEntry is one projected graded outcome for a learner.
type TranscriptEntry struct {
	EntryID        string
	TenantID       string
	GCID           string
	Kind           Kind
	SourceRef      string // assessment_id (KindAssessment) or cert_id (KindCertification); no FK
	Title          string
	DeliveryType   DeliveryType // "" = unattributable (freestanding assessment, or an event predating field 14)
	ScoreEarned    *float64
	ScorePossible  *float64
	ScorePercent   *float64 // derived: ScoreEarned/ScorePossible*100 when ScorePossible > 0
	Passed         *bool
	CourseID       string // nullable; populated from certification events only
	OccurredAt     time.Time
	IdempotencyKey string // UNIQUE (tenant_id, idempotency_key) — the idempotent-upsert identity
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// SeenAt is when the learner FIRST opened this result; nil means never
	// (B6 item 2, see seen.go). One-way: set once on open, never moved and
	// never cleared. The projection subscriber does not write it, so a
	// re-grade of an already-read result leaves it read.
	SeenAt *time.Time
}

// ErrInvalid is the sentinel returned by New for any validation failure.
var ErrInvalid = errors.New("student_transcript: invalid")

// NewEntryInput is the constructor input for New.
type NewEntryInput struct {
	TenantID       string
	GCID           string
	Kind           Kind
	SourceRef      string
	Title          string
	DeliveryType   DeliveryType
	ScoreEarned    *float64
	ScorePossible  *float64
	Passed         *bool
	CourseID       string
	OccurredAt     time.Time
	IdempotencyKey string
	Now            time.Time
}

// New constructs a TranscriptEntry, computing ScorePercent from
// ScoreEarned/ScorePossible when ScorePossible is present and > 0 (guards a
// divide-by-zero and leaves certification entries — which carry no score —
// with a nil percent).
func New(in NewEntryInput) (*TranscriptEntry, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	gcid := strings.TrimSpace(in.GCID)
	if gcid == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalid)
	}
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalid, in.Kind)
	}
	sourceRef := strings.TrimSpace(in.SourceRef)
	if sourceRef == "" {
		return nil, fmt.Errorf("%w: source_ref required", ErrInvalid)
	}
	idempotencyKey := strings.TrimSpace(in.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key required", ErrInvalid)
	}
	if in.OccurredAt.IsZero() {
		return nil, fmt.Errorf("%w: occurred_at required", ErrInvalid)
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var percent *float64
	if in.ScorePossible != nil && *in.ScorePossible > 0 && in.ScoreEarned != nil {
		p := *in.ScoreEarned / *in.ScorePossible * 100
		percent = &p
	}

	// DeliveryType NORMALISES; it never rejects. This is deliberate and is the
	// single most important decision on this field.
	//
	// chora_delivery owns this vocabulary and offerings.delivery_type is bare
	// TEXT with NO database CHECK, so an unrecognised value is genuinely
	// reachable (a 4th mode ships, or a typo lands). Returning an error here
	// would fail the projection -> NACK -> redeliver -> DEAD-LETTER, and the
	// learner would lose a real GRADE over a metadata label. Propagating the
	// garbage instead would let a typo render as a "mode" in a transcript.
	//
	// So: keep the row, drop the label. "" reads as "not attributable", which is
	// the honest answer. The adapter warns LOUDLY on the way past so the drift is
	// visible rather than silent.
	deliveryType := in.DeliveryType
	if !deliveryType.Valid() {
		deliveryType = ""
	}

	return &TranscriptEntry{
		EntryID:        domain.NewUUIDv7(),
		TenantID:       tenantID,
		GCID:           gcid,
		Kind:           in.Kind,
		SourceRef:      sourceRef,
		Title:          in.Title,
		DeliveryType:   deliveryType,
		ScoreEarned:    in.ScoreEarned,
		ScorePossible:  in.ScorePossible,
		ScorePercent:   percent,
		Passed:         in.Passed,
		CourseID:       in.CourseID,
		OccurredAt:     in.OccurredAt.UTC(),
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}
