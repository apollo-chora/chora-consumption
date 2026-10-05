package http

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func podEntry(exp int) *companion.RosterEntry {
	now := time.Now().UTC()
	return &companion.RosterEntry{
		Instance: &companion.Instance{
			CompanionID: "pod-1", TenantID: testTenant, OwnerGCID: testGCID,
			Name: "Pod", EvolutionTier: companion.EvolutionTier("apprentice"),
			CreatedAt: now, UpdatedAt: now,
		},
		// A Stage-0 pod: no species, no hatched_at. This is exactly the shape the
		// pg projection builds, where ExpToNextStage came off the growth curve.
		Growth: &companion.GrowthSnapshot{
			Stage:          0,
			StageName:      "egg",
			Exp:            exp,
			ExpToNextStage: companion.ExpToNextStage(0), // 0 by design
		},
		Cosmetic: &companion.CosmeticSnapshot{},
	}
}

// D3 (RED). The roster wire must warm a pod towards the SAME denominator the
// per-companion growth read surfaces, or the incubation card renders a bar that
// can never fill and a hatch CTA that can never free.
func TestToRosterItemResp_PodWarmsTowardsTheHatchGate(t *testing.T) {
	resp := toRosterItemResp(podEntry(32), 25, nil)
	if resp.GrowthState == nil {
		t.Fatal("GrowthState must be populated for a Stage-0 pod")
	}
	if resp.GrowthState.ExpToNextStage != 25 {
		t.Errorf("ExpToNextStage = %d; want the configured hatch gate 25 (the growth read's answer)",
			resp.GrowthState.ExpToNextStage)
	}
	if resp.GrowthState.Exp != 32 {
		t.Errorf("Exp = %d; want 32 carried through untouched", resp.GrowthState.Exp)
	}
}

// The gate is CONFIGURABLE (COMPANION_HATCH_EXP_THRESHOLD), so the projection
// must carry the resolved value rather than bake the domain default.
func TestToRosterItemResp_PodHonoursAnOverriddenGate(t *testing.T) {
	resp := toRosterItemResp(podEntry(10), 40, nil)
	if resp.GrowthState.ExpToNextStage != 40 {
		t.Errorf("ExpToNextStage = %d; want the overridden gate 40", resp.GrowthState.ExpToNextStage)
	}
}

// A hatched row is untouched: its denominator is the growth curve, and widening
// the stage-0 special case into stage 1+ would break stage-up arithmetic.
func TestToRosterItemResp_HatchedRowKeepsTheCurve(t *testing.T) {
	e := podEntry(300)
	e.Growth.Stage = 2
	e.Growth.StageName = "fledgling"
	e.Growth.ExpToNextStage = companion.ExpToNextStage(2)
	resp := toRosterItemResp(e, 25, nil)
	if resp.GrowthState.ExpToNextStage != companion.ExpToNextStage(2) {
		t.Errorf("ExpToNextStage = %d; want the curve value %d",
			resp.GrowthState.ExpToNextStage, companion.ExpToNextStage(2))
	}
}

// Fail-closed: a server whose gate was never wired must still answer with the
// domain default, exactly as growth.NewService does, so the two reads cannot
// diverge in a unit server either.
func TestHatchExpThreshold_UnwiredFallsBackToTheDomainDefault(t *testing.T) {
	s := &Server{}
	if got := s.hatchExpThreshold(); got != growth.DefaultHatchExpThreshold {
		t.Errorf("unwired hatchExpThreshold() = %d; want the domain default %d",
			got, growth.DefaultHatchExpThreshold)
	}
	s.HatchExpThreshold = 40
	if got := s.hatchExpThreshold(); got != 40 {
		t.Errorf("wired hatchExpThreshold() = %d; want 40", got)
	}
}
