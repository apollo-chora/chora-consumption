// refresher_warmth_test.go — Phase 4 (CHO-2239, owner decision 2026-07-16
// #4 "close the refresher farming seam", mechanism (b) approved 2026-07-17):
// a refresher re-clear NEVER warms an unhatched pod.
//
// The seam: DecideServe serves refreshers by design (ADR-227 D8) and the
// goals answers door grades a CLIENT-SUPPLIED rung, so re-answering
// already-mastered sets mints campaign_rung_refreshed (3 EXP, cap 12/day)
// into the goal-bound pod — compressing the intended ~4 dose-day hatch to
// ~2. The fix suppresses exactly that source at stage 0: AwardExp skips
// (writes nothing, emits nothing), refreshers pay normally from stage 1,
// and fresh clears keep warming the pod (the 1-advance-per-dose-day drive).
package growth_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func awardRefresher(svc *growth.Service, companionID string) (*growth.AwardExpResponse, error) {
	return svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    companionID,
		OwnerGCID:      "user-1",
		Source:         growth.SourceCampaignRungRefreshed,
		RequestedDelta: 0, // resolver-priced (3), like the campaign XP subscriber
		IdempotencyKey: "evt-refresher-1",
		Traceparent:    "00-aa-bb-00",
	})
}

func TestAwardExp_RefresherNeverWarmsAnUnhatchedPod(t *testing.T) {
	svc, repo, ox := newService(t)
	seedEgg(repo, 10) // stage 0, unhatched, some warmth already

	resp, err := awardRefresher(svc, "fam-egg")
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if !resp.Skipped {
		t.Fatalf("a refresher on an unhatched pod must be Skipped, got %+v", resp)
	}
	if resp.SkipReason != "unhatched_refresher" {
		t.Errorf("SkipReason = %q, want unhatched_refresher", resp.SkipReason)
	}
	if got := repo.rows["fam-egg"].GrowthExp; got != 10 {
		t.Errorf("warmth must be untouched: GrowthExp = %d, want 10", got)
	}
	if topics := ox.topics(); len(topics) != 0 {
		t.Errorf("a suppressed award must emit nothing, got %v", topics)
	}
}

func TestAwardExp_RefresherPaysAHatchedCompanion(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0, Species: "fox",
	}

	resp, err := awardRefresher(svc, "fam-1")
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.Skipped {
		t.Fatalf("a stage-1 refresher must pay, got skipped (%s)", resp.SkipReason)
	}
	if resp.ClampedDelta != 3 {
		t.Errorf("ClampedDelta = %d, want the resolved refresher value 3", resp.ClampedDelta)
	}
	if topics := ox.topics(); len(topics) != 1 || topics[0] != "chora.consumption.companion.exp_awarded.v1" {
		t.Errorf("topics = %v, want exactly exp_awarded.v1", topics)
	}
}

func TestAwardExp_FreshClearsStillWarmTheEgg(t *testing.T) {
	// The drive mechanic is untouched: campaign_rung_cleared (the 1-fresh-
	// advance-per-dose-day lane) keeps warming the unhatched pod.
	svc, repo, _ := newService(t)
	seedEgg(repo, 0)

	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-egg",
		OwnerGCID:      "user-1",
		Source:         "campaign_rung_cleared",
		RequestedDelta: 0,
		IdempotencyKey: "evt-clear-1",
		Traceparent:    "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.Skipped {
		t.Fatalf("a fresh clear on a pod must pay, got skipped (%s)", resp.SkipReason)
	}
	if got := repo.rows["fam-egg"].GrowthExp; got != 8 {
		t.Errorf("GrowthExp = %d, want the resolved fresh-clear value 8", got)
	}
}
