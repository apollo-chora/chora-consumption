package growth

// resonance.go — CHO-2013 P1 "Summoning & Awakening" (R3-1): the awakening
// resonant-concept pick. The learner elects a ConceptNode inside the attached
// Goal's subgraph as the Companion's ring-radius centre; it persists on
// companion_instances.resonant_concept_id (UUID, no FK per DDD cross-aggregate
// rules) and falls back to the Goal root while NULL. Rebinding clears it (the
// companion.Summoner owns that path); re-picking overwrites.
//
// Subgraph membership is NOT re-verified here in P1: the FE picker only
// offers nodes of the bound Goal's subgraph, and the P2 ring reads
// (kg.read_map) recompute rings server-side from the stored centre. The
// server-side invariant enforced now: the concept EXISTS for this learner
// (RLS-scoped) and the companion is hatched + bound.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AttachedGoal is the narrow projection of a Goal the growth domain needs:
// identity + the root-concept fallback + the display theme. Kept primitive so
// growth does not import the goal aggregate package.
type AttachedGoal struct {
	GoalID        string
	RootConceptID string
	Title         string
}

// GoalBinding resolves which Goal (if any) a companion is currently attached
// to (Goal.AttachedCompanionID == companionID). Implemented over the goal repo.
type GoalBinding interface {
	FindByAttachedCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) (*AttachedGoal, error)
}

// ConceptChecker reports whether a ConceptNode exists for the learner
// (RLS-scoped). Implemented over the concept-node repo.
type ConceptChecker interface {
	Exists(ctx context.Context, tenantID, learnerGCID, conceptID string) (bool, error)
}

// Resonance sentinels. Mapped to HTTP codes in the adapter.
var (
	// ErrResonanceNotWired — the GoalBinding / ConceptChecker ports are not
	// configured. Fail-loud: the resonance surface refuses rather than
	// skipping validation.
	ErrResonanceNotWired = errors.New("growth: resonance ports not wired")
	// ErrCompanionNotBound — the companion is not attached to any Goal; the
	// awakening pick only exists inside a binding (R3-1).
	ErrCompanionNotBound = errors.New("growth: companion is not bound to a goal")
	// ErrConceptNotFound — the concept does not exist for this learner.
	ErrConceptNotFound = errors.New("growth: concept not found")
	// ErrCompanionNotHatched — the companion is still an egg (stage 0).
	ErrCompanionNotHatched = errors.New("growth: companion not hatched")
)

// PickResonantConceptInput identifies the pick.
type PickResonantConceptInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	ConceptID   string
	Traceparent string
	Tracestate  string
}

// PickResonantConceptResponse returns the refreshed growth state.
type PickResonantConceptResponse struct {
	State State
}

// PickResonantConcept validates and persists the awakening resonant-concept
// pick. No event is emitted in P1 (none is defined for the pick; the ring
// centre is read state).
func (s *Service) PickResonantConcept(ctx context.Context, in PickResonantConceptInput) (*PickResonantConceptResponse, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" || strings.TrimSpace(in.OwnerGCID) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/owner required", ErrInvalidArguments)
	}
	in.ConceptID = strings.TrimSpace(in.ConceptID)
	if in.ConceptID == "" || !isCanonicalUUID(in.ConceptID) {
		return nil, fmt.Errorf("%w: concept_id must be a canonical UUID", ErrInvalidArguments)
	}
	if s.cfg.GoalBinding == nil || s.cfg.Concepts == nil {
		return nil, ErrResonanceNotWired
	}

	row, err := s.cfg.Repo.GetGrowthRow(ctx, in.TenantID, in.CompanionID)
	if err != nil {
		return nil, err
	}
	if row.OwnerGCID != in.OwnerGCID {
		// No cross-owner disclosure — indistinguishable from missing.
		return nil, ErrCompanionNotFound
	}
	if row.GrowthStage < 1 || row.HatchedAt == nil {
		return nil, ErrCompanionNotHatched
	}

	bound, err := s.cfg.GoalBinding.FindByAttachedCompanion(ctx, in.TenantID, in.OwnerGCID, in.CompanionID)
	if err != nil {
		return nil, fmt.Errorf("growth: resolve goal binding: %w", err)
	}
	if bound == nil {
		return nil, ErrCompanionNotBound
	}

	ok, err := s.cfg.Concepts.Exists(ctx, in.TenantID, in.OwnerGCID, in.ConceptID)
	if err != nil {
		return nil, fmt.Errorf("growth: concept existence check: %w", err)
	}
	if !ok {
		return nil, ErrConceptNotFound
	}

	updated, err := s.cfg.Repo.SetResonantConcept(ctx, in.TenantID, in.CompanionID, &in.ConceptID)
	if err != nil {
		return nil, err
	}
	st := s.projectState(updated, "")
	return &PickResonantConceptResponse{State: st}, nil
}
