// service_display_name_test.go — CHO-2266: the growth service denormalises the
// learner-chosen Companion name onto the stage_up + source_revelation payloads so
// the C+ milestone auto-share copy can NAME the Companion without a cross-DB join
// into chora_consumption (cross-DB queries forbidden — the event payload is the
// lawful carrier).
package growth_test

import (
	"context"
	"testing"
	"time"

	growth "github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// findCall returns the first captured outbox call for topic, or nil.
func findCall(ox *fakeOutbox, topic string) *outboxCall {
	for i := range ox.calls {
		if ox.calls[i].Topic == topic {
			return &ox.calls[i]
		}
	}
	return nil
}

// TestAwardExp_StageUp_CarriesCompanionDisplayName — a mid-life stage transition
// (2→3) publishes stage_up carrying the name snapshotted from the growth row.
func TestAwardExp_StageUp_CarriesCompanionDisplayName(t *testing.T) {
	svc, repo, ox := newService(t)
	seedStage1Row(repo, 40) // 40 + 15 = 55 >= 50 → stage 2
	repo.rows["fam-1"].DisplayName = "Tempo"

	resp, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if !resp.StageUpTriggered {
		t.Fatal("expected a stage-up")
	}
	call := findCall(ox, growth.TopicCompanionStageUp)
	if call == nil {
		t.Fatalf("no %s published; topics = %v", growth.TopicCompanionStageUp, ox.topics())
	}
	if got := call.Payload["companion_display_name"]; got != "Tempo" {
		t.Errorf("stage_up companion_display_name = %v, want Tempo", got)
	}
}

// TestTriggerSourceRevelation_CarriesCompanionDisplayName — the Stage-3 Aha
// ceremony publishes source_revelation carrying the name from the growth row.
func TestTriggerSourceRevelation_CarriesCompanionDisplayName(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 3, DisplayName: "Tempo",
	}
	if _, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		ManaTier: "premium", Traceparent: "00-aa-bb-00",
	}); err != nil {
		t.Fatalf("TriggerSourceRevelation: %v", err)
	}
	call := findCall(ox, growth.TopicCompanionSourceRevelation)
	if call == nil {
		t.Fatalf("no %s published; topics = %v", growth.TopicCompanionSourceRevelation, ox.topics())
	}
	if got := call.Payload["companion_display_name"]; got != "Tempo" {
		t.Errorf("source_revelation companion_display_name = %v, want Tempo", got)
	}
}

// TestHatchEgg_StageUp_CarriesCompanionDisplayName — the 0→1 hatch transition
// publishes its stage_up carrying the name the learner just chose at hatch.
func TestHatchEgg_StageUp_CarriesCompanionDisplayName(t *testing.T) {
	svc, repo, ox := newService(t)
	revealedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: 30, EggSku: "starter", // stirring (>= hatch threshold)
		Species: "owl", SpeciesRarity: "common", RolledProb: 50, RevealedAt: &revealedAt,
	}
	if _, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		DisplayName: "Blinky", Tone: "encouraging", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000009", Traceparent: "00-trace-01",
	}); err != nil {
		t.Fatalf("HatchEgg: %v", err)
	}
	call := findCall(ox, growth.TopicCompanionStageUp)
	if call == nil {
		t.Fatalf("no %s published; topics = %v", growth.TopicCompanionStageUp, ox.topics())
	}
	if got := call.Payload["companion_display_name"]; got != "Blinky" {
		t.Errorf("hatch stage_up companion_display_name = %v, want Blinky", got)
	}
}
