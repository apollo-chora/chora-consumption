// service_hatch_gate_test.go — F-I1.3 (CHO-2088, ADR-228 incubation): an egg
// may only hatch once it is "stirring" — it has accrued at least the hatch
// EXP threshold. A pre-threshold hatch attempt is rejected with
// ErrNotStirring; the learner still explicitly taps to hatch once stirring.
package growth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func hatchEgg(svc *growth.Service) error {
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-aa-bb-00",
	})
	return err
}

func TestHatchEgg_RejectsWhenNotStirring(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 24) // one short of the threshold

	if err := hatchEgg(svc); !errors.Is(err, growth.ErrNotStirring) {
		t.Fatalf("expected ErrNotStirring for a pre-threshold egg, got %v", err)
	}
}

func TestHatchEgg_RejectsFreshEggAtZeroExp(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 0)

	if err := hatchEgg(svc); !errors.Is(err, growth.ErrNotStirring) {
		t.Fatalf("expected ErrNotStirring for a 0-EXP egg, got %v", err)
	}
}

func TestHatchEgg_AllowsWhenStirring(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 25) // exactly at the threshold → stirring

	// CHO-2229: the ceremony reveals first — a stirring pod may reveal,
	// and a revealed pod may commit.
	if _, err := revealEgg(svc); err != nil {
		t.Fatalf("stirring egg (exp==threshold) must reveal, got %v", err)
	}
	if err := hatchEgg(svc); err != nil {
		t.Fatalf("revealed stirring egg (exp==threshold) must hatch, got %v", err)
	}
}

func TestHatchEgg_AllowsWellPastThreshold(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 40)

	if _, err := revealEgg(svc); err != nil {
		t.Fatalf("stirring egg (exp>threshold) must reveal, got %v", err)
	}
	if err := hatchEgg(svc); err != nil {
		t.Fatalf("revealed stirring egg (exp>threshold) must hatch, got %v", err)
	}
}
