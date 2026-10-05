// path_unlocker_split_test.go — CHO-2039 (CR §8 R6-3): the unlocker's
// two-phase split. MintThroughStage mints WITHOUT publishing (so it can
// join the award transaction); PublishGranted announces exactly the minted
// grants afterwards, with the same payload shape UnlockThroughStage always
// emitted.
package companion

import (
	"context"
	"testing"
)

// TestMintThroughStageMintsWithoutPublishing — the mint phase writes grants
// and returns their metadata but emits NO events (announcements belong to
// the post-commit phase).
func TestMintThroughStageMintsWithoutPublishing(t *testing.T) {
	u, grants, outbox := unlockerFixture(t)
	minted, err := u.MintThroughStage(context.Background(), unlockInput(3))
	if err != nil {
		t.Fatalf("MintThroughStage: %v", err)
	}
	if len(minted) != 3 || len(grants.minted) != 3 {
		t.Fatalf("minted %d returned / %d written, want 3 / 3 (cumulative through st3)", len(minted), len(grants.minted))
	}
	byKey := map[string]MintedGrant{}
	for _, m := range minted {
		byKey[m.SkillKey] = m
	}
	if m := byKey["explain_anew"]; m.SkillKind != SkillKindActive || m.UnlockedVia != "species_path" || m.UnlockedAtStage != 2 || m.Equipped {
		t.Errorf("explain_anew minted meta = %+v, want active/species_path/st2/unequipped", m)
	}
	if m := byKey["quiz_me"]; m.UnlockedAtStage != 3 {
		t.Errorf("quiz_me minted stage = %d, want 3", m.UnlockedAtStage)
	}
	if len(outbox.calls) != 0 {
		t.Errorf("mint phase emitted %d events, want 0 (publish is the post-commit phase)", len(outbox.calls))
	}
}

// TestPublishGrantedEmitsPerMintedGrant — the publish phase announces each
// minted grant with the canonical skill_granted payload + idempotency key.
func TestPublishGrantedEmitsPerMintedGrant(t *testing.T) {
	u, _, outbox := unlockerFixture(t)
	in := unlockInput(3)
	minted, err := u.MintThroughStage(context.Background(), in)
	if err != nil {
		t.Fatalf("MintThroughStage: %v", err)
	}
	if err := u.PublishGranted(context.Background(), in, minted); err != nil {
		t.Fatalf("PublishGranted: %v", err)
	}
	if len(outbox.calls) != 3 {
		t.Fatalf("outbox calls = %d, want 3 skill_granted", len(outbox.calls))
	}
	for i, c := range outbox.calls {
		if c.Topic != TopicCompanionSkillGranted {
			t.Errorf("topic = %q, want %q", c.Topic, TopicCompanionSkillGranted)
		}
		wantKey := minted[i].SkillKey
		if got := c.Payload["skill_key"]; got != wantKey {
			t.Errorf("payload skill_key = %v, want %q (minted order preserved)", got, wantKey)
		}
		if got := c.Payload["skill_kind"]; got != string(minted[i].SkillKind) {
			t.Errorf("payload skill_kind = %v, want %q", got, minted[i].SkillKind)
		}
		if got := c.Payload["unlocked_via"]; got != "species_path" {
			t.Errorf("payload unlocked_via = %v, want species_path", got)
		}
		if c.Env.IdempotencyKey != "skill_granted:fam-1:"+wantKey {
			t.Errorf("idempotency key = %q, want skill_granted:fam-1:%s", c.Env.IdempotencyKey, wantKey)
		}
		if c.Env.TenantID != "tenant-1" || c.Env.Traceparent != "00-trace-01" {
			t.Errorf("envelope not propagated: %+v", c.Env)
		}
	}
}

// TestPublishGrantedEmptyIsNoOp — nothing minted, nothing announced.
func TestPublishGrantedEmptyIsNoOp(t *testing.T) {
	u, _, outbox := unlockerFixture(t)
	if err := u.PublishGranted(context.Background(), unlockInput(3), nil); err != nil {
		t.Fatalf("PublishGranted(nil): %v", err)
	}
	if len(outbox.calls) != 0 {
		t.Errorf("outbox calls = %d, want 0", len(outbox.calls))
	}
}
