// bootstrap.go — course-bound LearningPath bootstrap + advance lifecycle.
//
// On `chora.delivery.enrollment.created.v1` we bootstrap a Straight-Up
// LearningPath for one (course_id, learner_gcid) pair. The atom list comes
// from the LOCAL atom_index projection (chora-consumption never queries
// chora_creation directly per ddd-enforcement HARD RULE).
//
// On each `chora.consumption.atom_session.completed.v1` for atoms in the
// path, we advance the current_atom_index. When the cursor reaches the end
// the path emits `learning_path.completed.v1` (idempotent — completion
// stamp is set once).
package learning_path

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// ErrAtomNotInPath is returned by Advance when the atom_id is not part of
// the path's ordered list. Out-of-order atoms (in the list but skipped)
// return (advanced=false, nil) — the caller treats those as a no-op.
var ErrAtomNotInPath = errors.New("learning_path: atom not in path")

// ErrSpacedPathNoCursor is returned by Advance on a spaced-traversal path
// (ADR-233 D3).
//
// `current_index` is a LINEAR cursor. A spaced-repetition study list is
// scheduled by decay/due off `sm2_states` (daily_dose.go + sm2.go), never by a
// cursor — so advancing it would move a meaningless integer and, worse, could
// stamp a bogus `completed_at`. Refusing LOUDLY is the whole point of lifting
// traversal_mode into the type: the domain had two traversal semantics in
// behaviour and zero in the type.
//
// Callers that legitimately fan out over ALL of a learner's paths (e.g. the
// atom_session.completed handler) MUST tolerate this sentinel and skip the
// path — it is a correct no-op, not a failure. See
// subscribers/session_completion.go.
var ErrSpacedPathNoCursor = errors.New("learning_path: spaced traversal has no linear cursor — Advance() refused (ADR-233 D3)")

// CourseID + EnrollmentID + CurrentIndex + CompletedAt fields are added
// to the LearningPath struct via this file's BootstrapFromEnrollment
// constructor. We re-open the LearningPath type by attaching them as
// methods + keep the canonical struct definition in path.go.

// BootstrapParams captures everything needed to bootstrap a course-bound
// path on `enrollment.created.v1`.
type BootstrapParams struct {
	TenantID     string
	LearnerGCID  string
	CourseID     string
	EnrollmentID string
	AtomIDs      []string
	Title        string
	Now          time.Time
}

// AdvanceResult is returned by Advance for handler-side telemetry.
type AdvanceResult struct {
	Advanced  bool // true when the cursor moved
	Completed bool // true when this advance completed the path
}

// BootstrapFromEnrollment is the consumer-side handler for
// `chora.delivery.enrollment.created.v1`. Validates required fields +
// builds a fresh LearningPath at CurrentIndex=0.
//
// Per audit-content-fillgaps.md §3.2: this is the missing wiring that
// connects chora-delivery → chora-consumption.
func BootstrapFromEnrollment(p BootstrapParams) (*LearningPath, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("learning_path: tenant_id required")
	}
	if strings.TrimSpace(p.LearnerGCID) == "" {
		return nil, errors.New("learning_path: learner_gcid required")
	}
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, errors.New("learning_path: course_id required")
	}
	// OPEN-1: an empty atom list is VALID. A PUBLISHED course may carry no
	// atoms yet (e.g. test-set-only, or atoms not yet projected into the
	// local atom_index). Bootstrapping a 0-atom path still records the
	// enrolment so the learner sees the course instead of "not enrolled";
	// atoms are appended retroactively via AppendAtom as
	// chora.creation.atom.created.v1 events arrive.
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "Course " + p.CourseID
	}
	atomCopy := make([]string, len(p.AtomIDs))
	copy(atomCopy, p.AtomIDs)
	return &LearningPath{
		PathID:       domain.NewUUIDv7(),
		TenantID:     p.TenantID,
		OwnerGCID:    p.LearnerGCID,
		CourseID:     p.CourseID,
		EnrollmentID: p.EnrollmentID,
		Title:        title,
		AtomIDs:      atomCopy,
		CurrentIndex: 0,
		// ADR-233 D2/D3: the delivery lane is course-provenanced + linear.
		// CourseID/EnrollmentID above are the delivery BINDING (an enrollment
		// reference) and are RETAINED — provenance does not replace them.
		SourceType:    SourceTypeCourse,
		SourceID:      p.CourseID,
		TraversalMode: TraversalModeLinear,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// AppendAtom appends atomID to the END of the path's ordered atom list when
// it is not already present, preserving the Straight-Up cursor. Returns true
// when the atom was added, false on a duplicate or blank id (idempotent).
//
// R2: this is the retroactive counterpart to a content-tolerant bootstrap —
// atoms authored after the learner enrolled (chora.creation.atom.created.v1)
// extend the existing path rather than being lost. New trailing work reopens
// a previously-completed path (clears CompletedAt) since the cursor no longer
// sits at the end.
func (p *LearningPath) AppendAtom(atomID string, now time.Time) bool {
	atomID = strings.TrimSpace(atomID)
	if atomID == "" {
		return false
	}
	for _, a := range p.AtomIDs {
		if a == atomID {
			return false
		}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p.AtomIDs = append(p.AtomIDs, atomID)
	p.UpdatedAt = now
	if p.CompletedAt != nil && p.CurrentIndex < len(p.AtomIDs) {
		p.CompletedAt = nil
	}
	return true
}

// Advance moves the cursor past the supplied atom_id when:
//
//   - the atom is in AtomIDs
//   - the atom is at AtomIDs[CurrentIndex] (Straight-Up linear order)
//
// Returns (Advanced=true, Completed=true) on the final atom; Completed is
// idempotent — re-advancing past the end is a no-op. Out-of-order atoms
// (e.g., advancing atom3 when CurrentIndex=0) return Advanced=false with
// no error so subscribers can replay safely.
//
// REFUSES with ErrSpacedPathNoCursor when TraversalMode == spaced (ADR-233 D3):
// a spaced study list is scheduled by SM-2 decay/due, and its cursor is inert.
func (p *LearningPath) Advance(atomID string, now time.Time) (AdvanceResult, error) {
	if p.IsSpaced() {
		return AdvanceResult{}, ErrSpacedPathNoCursor
	}
	if p.IsCompleted() {
		return AdvanceResult{}, nil
	}
	// Verify atom is in path.
	inPath := false
	for _, a := range p.AtomIDs {
		if a == atomID {
			inPath = true
			break
		}
	}
	if !inPath {
		return AdvanceResult{}, ErrAtomNotInPath
	}
	// Linear straight-up: only advance if at the cursor position.
	if p.CurrentIndex >= len(p.AtomIDs) {
		return AdvanceResult{}, nil
	}
	if p.AtomIDs[p.CurrentIndex] != atomID {
		// Out-of-order or already-passed atom — no-op.
		return AdvanceResult{Advanced: false}, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p.CurrentIndex++
	p.UpdatedAt = now
	if p.CurrentIndex >= len(p.AtomIDs) {
		stamp := now
		p.CompletedAt = &stamp
		return AdvanceResult{Advanced: true, Completed: true}, nil
	}
	return AdvanceResult{Advanced: true}, nil
}

// IsCompleted returns true when every atom in the path has been advanced.
func (p *LearningPath) IsCompleted() bool {
	return p.CompletedAt != nil
}

// ProgressPercent returns CurrentIndex / len(AtomIDs) in [0.0, 1.0].
// Empty AtomIDs returns 0.0 (defensive — bootstrap rejects empty lists,
// but path.go's New also accepts paths so we guard for safety).
func (p *LearningPath) ProgressPercent() float32 {
	total := len(p.AtomIDs)
	if total == 0 {
		return 0.0
	}
	return float32(p.CurrentIndex) / float32(total)
}
