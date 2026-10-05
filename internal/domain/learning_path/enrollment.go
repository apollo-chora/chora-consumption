// enrollment.go — Enrollment + progress derivation.
//
// An Enrollment is the per-learner projection of progress through a
// LearningPath. Progress is derived from CompletedAtomIDs ∩ path.AtomIDs.
// Atoms completed but not in the path's atom list are ignored.
//
// CompletedAtomIDs is mutated by AtomSession.completed events flowing from
// the chora.consumption.atom_session.completed.v1 topic — but for the MVP
// we expose MarkAtomComplete() as a direct method on the aggregate; the
// real event-driven projection lands in M12.
package learning_path

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Enrollment captures one learner's enrollment in a path.
type Enrollment struct {
	EnrollmentID     string
	TenantID         string
	PathID           string
	LearnerGCID      string
	CompletedAtomIDs []string
	EnrolledAt       time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// Enroll constructs a fresh Enrollment.
func Enroll(tenantID, pathID, learnerGCID string) (*Enrollment, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("enrollment: tenant_id required")
	}
	if strings.TrimSpace(pathID) == "" {
		return nil, errors.New("enrollment: path_id required")
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return nil, errors.New("enrollment: learner_gcid required")
	}
	now := time.Now().UTC()
	return &Enrollment{
		EnrollmentID:     domain.NewUUIDv7(),
		TenantID:         tenantID,
		PathID:           pathID,
		LearnerGCID:      learnerGCID,
		CompletedAtomIDs: []string{},
		EnrolledAt:       now,
		UpdatedAt:        now,
	}, nil
}

// MarkAtomComplete adds an atom to the completed list — idempotent
// (re-marking same atom is a no-op).
func (e *Enrollment) MarkAtomComplete(atomID string) {
	for _, existing := range e.CompletedAtomIDs {
		if existing == atomID {
			return
		}
	}
	e.CompletedAtomIDs = append(e.CompletedAtomIDs, atomID)
	e.UpdatedAt = time.Now().UTC()
}

// Progress returns the % completion in [0.0, 1.0]. Atoms in
// CompletedAtomIDs that are NOT in pathAtoms are ignored — completion
// outside the path doesn't inflate the path's percent.
//
// Empty pathAtoms (no atoms in path) → 0.0 (avoids divide-by-zero).
func (e *Enrollment) Progress(pathAtoms []string) float32 {
	if len(pathAtoms) == 0 {
		return 0.0
	}
	pathSet := make(map[string]struct{}, len(pathAtoms))
	for _, a := range pathAtoms {
		pathSet[a] = struct{}{}
	}
	hits := 0
	for _, c := range e.CompletedAtomIDs {
		if _, ok := pathSet[c]; ok {
			hits++
		}
	}
	return float32(hits) / float32(len(pathAtoms))
}
