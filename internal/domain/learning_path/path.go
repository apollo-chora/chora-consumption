// Package learning_path defines the LearningPath aggregate (extension scope).
//
// This package is part of the M13 chora-consumption extension covering the
// Straight-Up linear cert mode (CLAUDE.md §1 bimodal delivery). It is
// distinct from the older skeleton `path` package — that one is owned by
// the Phyllis MVP track. THIS package introduces:
//
//   - LearningPath with title + atom IDs + owner gcid (TDD-driven)
//   - Enrollment with progress derivation
//
// Aligned with .claude/rules/ddd-enforcement.md:
//   - LearningAtom is the primary aggregate root; collections only QUERY
//     atoms — they don't own them.
//   - Cross-aggregate references are UUID without FK.
//   - Soft delete only (deleted_at).
//   - UUIDv7 for all new IDs.
package learning_path

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Provenance axis (ADR-233 D2) — a LearningPath is a DERIVATION of an upstream
// shared sequence. SourceType is polymorphic so a third source costs a CHECK
// value, not a column.
const (
	SourceTypeCourse     = "course"     // derived from a chora-delivery Course (via enrollment)
	SourceTypeCollection = "collection" // derived from a chora-creation Collection (WS-4 study list)
	SourceTypeAdHoc      = "ad_hoc"     // hand-rolled; no upstream source
)

// Traversal mode (ADR-233 D3) — lifts into the type a distinction the domain
// already had in behaviour.
const (
	// TraversalModeLinear — Straight-Up cert traversal; CurrentIndex advances
	// via Advance().
	TraversalModeLinear = "linear"
	// TraversalModeSpaced — dose-driven; the cursor is INERT and Advance()
	// refuses. SM-2 + the Ebbinghaus forgetting curve schedule the atoms off
	// sm2_states.
	TraversalModeSpaced = "spaced"
)

// LearningPath holds an ordered, immutable sequence of LearningAtom IDs.
// Atom IDs reference rows in chora_creation; cross-DB JOINs are forbidden.
//
// Per S4.2 (Phyllis MVP §6) the aggregate also tracks Straight-Up linear
// progression via CurrentIndex + CompletedAt. CourseID + EnrollmentID
// connect the path to a chora-delivery enrollment when the path was
// bootstrapped from `chora.delivery.enrollment.created.v1`.
//
// ADR-233 (2026-07-14) adds the provenance + traversal axes:
//
//   - SourceType / SourceID — polymorphic provenance (D2). CourseID +
//     EnrollmentID are RETAINED alongside: they are the delivery *binding* (an
//     enrollment reference with its own semantics), which is strictly more than
//     provenance.
//   - TraversalMode — linear (cursor) vs spaced (SM-2/decay) (D3).
//   - StudyListEventID — the durable delivery-dedupe anchor for the
//     collection→study-list conversion (idempotency key of the inbound event).
//
// 🔴 D1 BOUNDARY INVARIANT: this aggregate models ONE LEARNER'S PRIVATE
// TRAVERSAL. It MUST NEVER grow a `visibility` / audience field — that would
// place content distribution inside the consumption domain. Sharing a study
// list means sharing its SOURCE (a creation Collection / delivery Course, D5);
// progress sharing is a governed projection owned by chora_sharing (D6).
// Enforced by TestLearningPath_CarriesNoAudienceField_ADR233_D1.
type LearningPath struct {
	PathID       string
	TenantID     string
	OwnerGCID    string
	Title        string
	AtomIDs      []string
	CourseID     string // delivery BINDING — only for paths bootstrapped from delivery
	EnrollmentID string // delivery BINDING — only for paths bootstrapped from delivery
	CurrentIndex int    // straight-up LINEAR cursor; 0..len(AtomIDs). INERT when TraversalMode == spaced.
	CompletedAt  *time.Time

	// ADR-233 D2 — provenance.
	SourceType string // course | collection | ad_hoc
	SourceID   string // course_id | collection_id | "" (ad_hoc)

	// ADR-233 D3 — traversal semantics.
	TraversalMode string // linear | spaced

	// ADR-233 — durable idempotency anchor for a collection-derived path.
	// Empty for course-bootstrapped + ad-hoc paths.
	StudyListEventID string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// New constructs a fresh LearningPath, validating required fields.
func New(tenantID, ownerGCID, title string, atomIDs []string) (*LearningPath, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("learning_path: tenant_id required")
	}
	if strings.TrimSpace(ownerGCID) == "" {
		return nil, errors.New("learning_path: owner_gcid required")
	}
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("learning_path: title required")
	}
	if len(atomIDs) == 0 {
		return nil, errors.New("learning_path: atom_ids required (>= 1)")
	}

	now := time.Now().UTC()
	cp := make([]string, len(atomIDs))
	copy(cp, atomIDs)
	return &LearningPath{
		PathID:    domain.NewUUIDv7(),
		TenantID:  tenantID,
		OwnerGCID: ownerGCID,
		Title:     title,
		AtomIDs:   cp,
		// ADR-233 D2/D3: a hand-rolled path has no upstream source and is
		// traversed linearly. Mirrors the migration-0093 column defaults.
		SourceType:    SourceTypeAdHoc,
		TraversalMode: TraversalModeLinear,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// SoftDelete marks the path deleted without removing it. Hard delete is
// forbidden per ddd-enforcement invariant #5.
func (p *LearningPath) SoftDelete() {
	now := time.Now().UTC()
	p.DeletedAt = &now
	p.UpdatedAt = now
}
