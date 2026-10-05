// study_list.go — the collection-derived LearningPath constructor (ADR-233).
//
// WS-4 (spec-001 US5, FR-030/FR-031): a learner converts a curated
// chora-creation `Collection` into a spaced-repetition study list. chora-creation
// owns the Collection aggregate and therefore owns the conversion transition; it
// publishes `chora.creation.collection.converted_to_study_list.v1` carrying the
// atom_ids. chora-consumption derives a LearningPath from that payload.
//
// Boundary notes (why this file is small on purpose):
//
//   - NO consent re-gate. The atom_ids on the event are ALREADY consent-gated by
//     chora-creation (ADR-233 D10/D11: add-time gate + convert-time gate, with
//     per-atom exclusions named in the convert response). The payload is
//     authoritative. Re-evaluating entitlement here would require reading
//     chora_creation — FORBIDDEN (cross-DB), and would duplicate a decision that
//     has already been made and audited upstream.
//
//   - NO sm2_states seeding. Explicitly rejected in ADR-233's alternatives:
//     seeding review history for atoms the learner has never seen fabricates a
//     past and corrupts SM-2's easiness-factor / interval semantics. New material
//     correctly enters via the daily dose's CURIOSITY slot (fed by
//     active_path_topics ← learning_path.bootstrapped.v1) and earns its SM-2 row
//     on first answer.
//
//   - NO audience field. See the D1 invariant on the LearningPath struct.
package learning_path

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// StudyListParams captures everything needed to derive a study-list path from
// `chora.creation.collection.converted_to_study_list.v1`.
type StudyListParams struct {
	TenantID     string
	OwnerGCID    string
	CollectionID string
	Title        string
	// AtomIDs is the collection's ENTITLED atom set, already consent-gated by
	// chora-creation. Order is preserved from the collection. May be empty:
	// atoms can be excluded per-atom at convert time (ADR-233 D11), and a
	// zero-survivor conversion is refused UPSTREAM (409), never here.
	AtomIDs []string
	// StudyListEventID is the durable dedupe anchor carried by the event.
	StudyListEventID string
	Now              time.Time
}

// NewFromCollection derives a spaced-repetition LearningPath from a converted
// Collection (ADR-233 D2 + D3).
//
// The path is stamped source_type='collection', source_id=<collection_id> and
// traversal_mode='spaced' — the linear CurrentIndex cursor is INERT for it and
// Advance() refuses (D3); SM-2 + Ebbinghaus schedule the atoms off sm2_states.
func NewFromCollection(p StudyListParams) (*LearningPath, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("learning_path: tenant_id required")
	}
	if strings.TrimSpace(p.OwnerGCID) == "" {
		return nil, errors.New("learning_path: owner_gcid required")
	}
	if strings.TrimSpace(p.CollectionID) == "" {
		return nil, errors.New("learning_path: collection_id required (source_id for source_type=collection)")
	}

	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "Study list " + p.CollectionID
	}
	atomCopy := make([]string, len(p.AtomIDs))
	copy(atomCopy, p.AtomIDs)

	return &LearningPath{
		PathID:    domain.NewUUIDv7(),
		TenantID:  p.TenantID,
		OwnerGCID: p.OwnerGCID,
		Title:     title,
		AtomIDs:   atomCopy,
		// No CourseID / EnrollmentID: a collection-derived path is not a
		// delivery enrolment.
		CurrentIndex:     0, // inert for spaced; persisted for schema uniformity
		SourceType:       SourceTypeCollection,
		SourceID:         p.CollectionID,
		TraversalMode:    TraversalModeSpaced,
		StudyListEventID: strings.TrimSpace(p.StudyListEventID),
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// IsSpaced reports whether the path is scheduled by decay/due (SM-2) rather
// than by the linear cursor.
func (p *LearningPath) IsSpaced() bool {
	return p.TraversalMode == TraversalModeSpaced
}
