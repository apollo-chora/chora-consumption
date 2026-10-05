// service_stirring_test.go — F-I1.2 (CHO-2088, ADR-228 incubation): a
// Stage-0 egg that accrues EXP across the hatch threshold emits a one-shot
// chora.consumption.companion.stirring.v1 event. The crossing is derived
// in-tx from prevExp/newExp (no persisted flag, no migration) so only the
// single award that satisfies prevExp < threshold <= newExp stirs.
package growth_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// withThreshold seeds a deterministic hatch threshold on the service under test.
func withThreshold(t int) func(*growth.ServiceConfig) {
	return func(c *growth.ServiceConfig) { c.HatchExpThreshold = t }
}

func seedEgg(repo *fakeRepo, exp int) {
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: exp, EggSku: "egg.standard.v1",
	}
}

func awardToEgg(t *testing.T, svc *growth.Service, delta int, idem string) *growth.AwardExpResponse {
	t.Helper()
	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Source: "admin_grant", RequestedDelta: delta, IdempotencyKey: idem,
		Traceparent: "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("AwardExp(delta=%d): %v", delta, err)
	}
	return resp
}

func TestAwardExp_Stage0EggCrossingThresholdEmitsStirring(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 20) // one 10-EXP award crosses 25

	resp := awardToEgg(t, svc, 10, "k1")
	if resp.StageUpTriggered {
		t.Errorf("egg must not stage up on award")
	}
	if resp.Row.GrowthStage != 0 {
		t.Errorf("egg stage = %d, want 0 (still pre-hatch)", resp.Row.GrowthStage)
	}

	topics := ox.topics()
	// exp_awarded THEN stirring (in that order).
	want := []string{
		"chora.consumption.companion.exp_awarded.v1",
		"chora.consumption.companion.stirring.v1",
	}
	if len(topics) != len(want) {
		t.Fatalf("topics = %v, want %v", topics, want)
	}
	for i, w := range want {
		if topics[i] != w {
			t.Errorf("topic[%d] = %q, want %q", i, topics[i], w)
		}
	}

	// Assert the stirring payload + stirring-scoped idempotency key.
	var stir *outboxCall
	for i := range ox.calls {
		if ox.calls[i].Topic == growth.TopicCompanionStirring {
			stir = &ox.calls[i]
		}
	}
	if stir == nil {
		t.Fatal("no stirring call captured")
	}
	if stir.Payload["companion_id"] != "fam-egg" {
		t.Errorf("stirring companion_id = %v", stir.Payload["companion_id"])
	}
	if stir.Payload["owner_gcid"] != "user-1" {
		t.Errorf("stirring owner_gcid = %v", stir.Payload["owner_gcid"])
	}
	if stir.Payload["growth_exp"] != 30 {
		t.Errorf("stirring growth_exp = %v, want 30", stir.Payload["growth_exp"])
	}
	if stir.Payload["hatch_threshold"] != 25 {
		t.Errorf("stirring hatch_threshold = %v, want 25", stir.Payload["hatch_threshold"])
	}
	if stir.Env.IdempotencyKey != "stirring:fam-egg" {
		t.Errorf("stirring idempotency key = %q, want stirring:fam-egg", stir.Env.IdempotencyKey)
	}
}

func TestAwardExp_Stage0EggIsOneShot_NoReStirAboveThreshold(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 20)

	awardToEgg(t, svc, 10, "k1") // 20 -> 30, crosses -> stirs
	awardToEgg(t, svc, 10, "k2") // 30 -> 40, already past -> no re-stir

	stirs := 0
	for _, c := range ox.calls {
		if c.Topic == growth.TopicCompanionStirring {
			stirs++
		}
	}
	if stirs != 1 {
		t.Errorf("stirring emitted %d times, want exactly 1 (one-shot)", stirs)
	}
}

func TestAwardExp_Stage0EggBelowThreshold_NoStirring(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 0)

	awardToEgg(t, svc, 10, "k1") // 0 -> 10, still below 25

	for _, c := range ox.calls {
		if c.Topic == growth.TopicCompanionStirring {
			t.Fatalf("stirring emitted below threshold (exp=10 < 25)")
		}
	}
}
