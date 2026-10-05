package growth_test

// resonance_test.go — CHO-2013 P1 "Summoning & Awakening": the awakening
// resonant-concept pick (R3-1). The learner elects a ConceptNode inside the
// attached Goal's subgraph as the Companion's ring-radius centre
// (companion_instances.resonant_concept_id). Rules under test:
//
//   - pick requires a hatched companion (stage >= 1), owned by the caller,
//     currently BOUND to a Goal (Goal.AttachedCompanionID == companion)
//   - the concept must exist for the learner (RLS-scoped existence check);
//     subgraph membership is enforced by the FE picker in P1 and re-checked
//     server-side by the P2 ring reads (kg.read_map)
//   - re-pick overwrites (rebind clears via the Summoner — separate unit)
//   - no ports wired ⇒ fail-loud ErrResonanceNotWired (never silent)

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// fakeGoalBinding satisfies growth.GoalBinding.
type fakeGoalBinding struct {
	bound map[string]*growth.AttachedGoal // companionID → goal
	err   error
}

func (f *fakeGoalBinding) FindByAttachedCompanion(_ context.Context, _, _, companionID string) (*growth.AttachedGoal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.bound[companionID], nil
}

// fakeConceptChecker satisfies growth.ConceptChecker.
type fakeConceptChecker struct {
	known map[string]bool
	err   error
}

func (f *fakeConceptChecker) Exists(_ context.Context, _, _, conceptID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.known[conceptID], nil
}

// SetResonantConcept — resonance persistence on the shared fakeRepo
// (service_test.go). Defined here so the resonance unit owns its surface.
func (r *fakeRepo) SetResonantConcept(_ context.Context, tenantID, companionID string, conceptID *string) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[companionID]
	if !ok || row.TenantID != tenantID {
		return nil, growth.ErrCompanionNotFound
	}
	if conceptID == nil {
		row.ResonantConceptID = ""
	} else {
		row.ResonantConceptID = *conceptID
	}
	cp := *row
	return &cp, nil
}

const (
	resonanceTenant  = "019eb1b4-0000-7000-8000-00000000c0de"
	resonanceOwner   = "019eb1b4-0000-7000-8000-00000000face"
	resonanceFam     = "019f2620-0000-7000-8000-00000000f001"
	resonanceConcept = "019f2620-0000-7000-8000-00000000cc01"
	resonanceGoal    = "019f2620-0000-7000-8000-000000009001"
)

func resonanceService(t *testing.T, repo *fakeRepo, gb growth.GoalBinding, cc growth.ConceptChecker) *growth.Service {
	t.Helper()
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:        repo,
		Outbox:      &fakeOutbox{},
		Dist:        stubDist{dist: defaultDistribution()},
		Clock:       func() time.Time { return time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC) },
		NewID:       func() string { return "evt-fixed" },
		GoalBinding: gb,
		Concepts:    cc,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func seedHatchedBoundCompanion(repo *fakeRepo, stage int) {
	hatched := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	repo.rows[resonanceFam] = &growth.CompanionGrowthRow{
		CompanionID: resonanceFam,
		TenantID:    resonanceTenant,
		OwnerGCID:   resonanceOwner,
		GrowthStage: stage,
		Species:     "penguin",
		GrowthExp:   50,
		HatchedAt:   &hatched,
	}
}

func boundTo(goalID string) *fakeGoalBinding {
	return &fakeGoalBinding{bound: map[string]*growth.AttachedGoal{
		resonanceFam: {GoalID: goalID, RootConceptID: "root-1", Title: "Astronomy"},
	}}
}

func TestPickResonantConcept_HappyPath_PersistsAndReturnsState(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	resp, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   resonanceConcept,
	})
	if err != nil {
		t.Fatalf("PickResonantConcept: %v", err)
	}
	if resp.State.ResonantConceptID != resonanceConcept {
		t.Fatalf("state.resonant_concept_id: got %q want %q", resp.State.ResonantConceptID, resonanceConcept)
	}
	if got := repo.rows[resonanceFam].ResonantConceptID; got != resonanceConcept {
		t.Fatalf("persisted resonant_concept_id: got %q", got)
	}
}

func TestPickResonantConcept_RePickOverwrites(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 3)
	repo.rows[resonanceFam].ResonantConceptID = "019f2620-0000-7000-8000-00000000cc99"
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	resp, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   resonanceConcept,
	})
	if err != nil {
		t.Fatalf("PickResonantConcept: %v", err)
	}
	if resp.State.ResonantConceptID != resonanceConcept {
		t.Fatalf("re-pick did not overwrite: %q", resp.State.ResonantConceptID)
	}
}

func TestPickResonantConcept_EggStageRejected(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 0)
	repo.rows[resonanceFam].HatchedAt = nil
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	_, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   resonanceConcept,
	})
	if !errors.Is(err, growth.ErrCompanionNotHatched) {
		t.Fatalf("want ErrCompanionNotHatched, got %v", err)
	}
}

func TestPickResonantConcept_UnboundRejected(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	svc := resonanceService(t, repo, &fakeGoalBinding{bound: map[string]*growth.AttachedGoal{}}, &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	_, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   resonanceConcept,
	})
	if !errors.Is(err, growth.ErrCompanionNotBound) {
		t.Fatalf("want ErrCompanionNotBound, got %v", err)
	}
}

func TestPickResonantConcept_UnknownConceptRejected(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{}})

	_, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   resonanceConcept,
	})
	if !errors.Is(err, growth.ErrConceptNotFound) {
		t.Fatalf("want ErrConceptNotFound, got %v", err)
	}
}

func TestPickResonantConcept_ForeignOwnerHidden(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	_, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   "019eb1b4-0000-7000-8000-000000000bad",
		ConceptID:   resonanceConcept,
	})
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Fatalf("want ErrCompanionNotFound (no cross-owner disclosure), got %v", err)
	}
}

func TestPickResonantConcept_MalformedConceptIDRejected(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	_, err := svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   "not-a-uuid",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Fatalf("want ErrInvalidArguments, got %v", err)
	}
}

func TestPickResonantConcept_PortsUnwiredFailLoud(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: &fakeOutbox{},
		Dist:   stubDist{dist: defaultDistribution()},
		NewID:  func() string { return "019f6a1c-89f8-7089-8142-e3af46113b6b" },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = svc.PickResonantConcept(context.Background(), growth.PickResonantConceptInput{
		TenantID:    resonanceTenant,
		CompanionID: resonanceFam,
		OwnerGCID:   resonanceOwner,
		ConceptID:   resonanceConcept,
	})
	if !errors.Is(err, growth.ErrResonanceNotWired) {
		t.Fatalf("want ErrResonanceNotWired, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "resonance") {
		t.Fatalf("error should name the unwired surface: %v", err)
	}
}

func TestGetCompanionGrowth_StateCarriesResonantConcept(t *testing.T) {
	repo := newFakeRepo()
	seedHatchedBoundCompanion(repo, 2)
	repo.rows[resonanceFam].ResonantConceptID = resonanceConcept
	svc := resonanceService(t, repo, boundTo(resonanceGoal), &fakeConceptChecker{known: map[string]bool{resonanceConcept: true}})

	st, err := svc.GetCompanionGrowth(context.Background(), resonanceTenant, resonanceFam, resonanceOwner)
	if err != nil {
		t.Fatalf("GetCompanionGrowth: %v", err)
	}
	if st.ResonantConceptID != resonanceConcept {
		t.Fatalf("state.resonant_concept_id not projected: %q", st.ResonantConceptID)
	}
}
