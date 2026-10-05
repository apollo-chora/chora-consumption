// service_incubation_read_test.go — F-I2 FE seam (CHO-2089, ADR-228 D2): the
// growth READ surfaces the hatch threshold for a Stage-0 egg so the warming
// bar has a real denominator. The curve's NextThreshold(0) stays 0 (the 1→6
// spine is untouched and the hatch remains ceremony-gated, never auto) — only
// the projected State is enriched.
package growth_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestGetCompanionGrowth_EggSurfacesHatchThreshold(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 12)

	st, err := svc.GetCompanionGrowth(context.Background(), "tenant-1", "fam-egg", "user-1")
	if err != nil {
		t.Fatalf("GetCompanionGrowth: %v", err)
	}
	if st.GrowthStage != 0 {
		t.Fatalf("stage = %d, want 0", st.GrowthStage)
	}
	if st.ExpNextThreshold != 25 {
		t.Fatalf("ExpNextThreshold = %d, want the hatch threshold 25 (warming-bar denominator)", st.ExpNextThreshold)
	}
	if st.ExpCurrent != 12 {
		t.Fatalf("ExpCurrent = %d, want 12", st.ExpCurrent)
	}
}

func TestGetCompanionGrowth_HatchedStagesKeepCurveThresholds(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	repo.rows["fam-baby"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-baby", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 10,
	}

	st, err := svc.GetCompanionGrowth(context.Background(), "tenant-1", "fam-baby", "user-1")
	if err != nil {
		t.Fatalf("GetCompanionGrowth: %v", err)
	}
	if st.ExpNextThreshold != 50 {
		t.Fatalf("stage-1 ExpNextThreshold = %d, want the curve's 50 (spine untouched)", st.ExpNextThreshold)
	}
}
