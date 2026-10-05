// service_atomic_unlock_test.go — CHO-2039 (CR §8 R6-3): the species-Path
// unlock must be ATOMIC with the EXP award. A companion must never commit a
// stage-up without its Path grants (the CHO-2032 "wolf-Nimbus empty
// Grimoire" class): if the unlock fails, the ENTIRE award — EXP delta,
// stage transition, ledger row, daily counter — rolls back and the error
// surfaces to the caller (Pub/Sub NACK → redelivery retries the WHOLE
// award, not a deduped husk).
//
// RED-first: against the pre-fix service these tests fail because AwardExpTx
// commits before the unlocker runs — the fault leaves the award persisted
// (half-grown) and the events already published.
package growth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// TestAwardExp_UnlockFaultRollsBackEntireAward — (a) an injected unlock
// fault on a stage-up award rolls back EVERYTHING: no EXP delta, no stage
// change, no ledger row, no published events; the error returns to the
// caller.
func TestAwardExp_UnlockFaultRollsBackEntireAward(t *testing.T) {
	unlocker := &fakeUnlocker{err: errors.New("grants table unavailable")}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40) // 40 + 15 = 55 ≥ 50 → stage-up attempt

	_, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err == nil {
		t.Fatal("AwardExp must fail loud when the species-Path unlock fails")
	}

	row := repo.rows["fam-1"]
	if row.GrowthExp != 40 {
		t.Errorf("GrowthExp = %d after unlock fault, want 40 (EXP delta rolled back)", row.GrowthExp)
	}
	if row.GrowthStage != 1 {
		t.Errorf("GrowthStage = %d after unlock fault, want 1 (stage-up rolled back)", row.GrowthStage)
	}
	if len(repo.events) != 0 {
		t.Errorf("ledger events = %d after unlock fault, want 0 (no committed award without grants)", len(repo.events))
	}
	if got := ox.topics(); len(got) != 0 {
		t.Errorf("published topics = %v after unlock fault, want none (nothing announced for a rolled-back award)", got)
	}
}

// TestAwardExp_RetryAfterUnlockFaultCompletesFully — (b) once the fault
// clears, retrying the SAME award (same idempotency key) completes the
// whole thing FRESH — award + stage + grants — with no double-award: the
// rollback un-consumed the idempotency key and the daily counter.
func TestAwardExp_RetryAfterUnlockFaultCompletesFully(t *testing.T) {
	unlocker := &fakeUnlocker{err: errors.New("transient grants outage")}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err == nil {
		t.Fatal("first AwardExp must fail while the unlock fault is injected")
	}

	// Fault clears (e.g. species_paths back online) → Pub/Sub redelivers.
	unlocker.mu.Lock()
	unlocker.err = nil
	unlocker.mu.Unlock()

	resp, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err != nil {
		t.Fatalf("retry AwardExp: %v", err)
	}
	if resp.Duplicate {
		t.Error("retry after a rolled-back award must run FRESH, not dedupe against the aborted attempt")
	}
	if !resp.StageUpTriggered {
		t.Error("retry must re-trigger the stage-up")
	}
	if resp.ClampedDelta != 15 {
		t.Errorf("retry ClampedDelta = %d, want 15 (daily counter rolled back with the award)", resp.ClampedDelta)
	}
	row := repo.rows["fam-1"]
	if row.GrowthExp != 55 || row.GrowthStage != 2 {
		t.Errorf("row after retry = exp %d / stage %d, want 55 / 2 (award applied exactly once)", row.GrowthExp, row.GrowthStage)
	}
	if len(repo.events) != 1 {
		t.Errorf("ledger events after retry = %d, want exactly 1 (no double-award)", len(repo.events))
	}
	topics := ox.topics()
	want := []string{
		growth.TopicCompanionExpAwarded,
		growth.TopicCompanionStageUp,
		growth.TopicCompanionSkillSlotUnlocked,
	}
	if len(topics) != len(want) {
		t.Fatalf("topics after retry = %v, want exactly %v", topics, want)
	}
	for i := range want {
		if topics[i] != want[i] {
			t.Errorf("topic[%d] = %q, want %q", i, topics[i], want[i])
		}
	}
}

// TestAwardExp_HealthyStageUpEventStreamUnchanged — (c) the healthy path is
// byte-for-byte unchanged: same response shape, same three growth events in
// the same order, unlocker invoked once for the new stage.
func TestAwardExp_HealthyStageUpEventStreamUnchanged(t *testing.T) {
	unlocker := &fakeUnlocker{}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	resp, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if !resp.StageUpTriggered || resp.Duplicate {
		t.Errorf("resp = (stageUp=%v, dup=%v), want (true, false)", resp.StageUpTriggered, resp.Duplicate)
	}
	if resp.ClampedDelta != 15 || resp.Row.GrowthExp != 55 || resp.Row.GrowthStage != 2 {
		t.Errorf("resp row = delta %d / exp %d / stage %d, want 15 / 55 / 2",
			resp.ClampedDelta, resp.Row.GrowthExp, resp.Row.GrowthStage)
	}
	topics := ox.topics()
	want := []string{
		growth.TopicCompanionExpAwarded,
		growth.TopicCompanionStageUp,
		growth.TopicCompanionSkillSlotUnlocked,
	}
	if len(topics) != len(want) {
		t.Fatalf("topics = %v, want exactly %v (no new/lost growth events)", topics, want)
	}
	for i := range want {
		if topics[i] != want[i] {
			t.Errorf("topic[%d] = %q, want %q", i, topics[i], want[i])
		}
	}
	if len(unlocker.calls) != 1 || unlocker.calls[0].Stage != 2 || unlocker.calls[0].Species != "owl" {
		t.Errorf("unlocker calls = %+v, want one call for owl at stage 2", unlocker.calls)
	}
}

// TestAwardExp_MintedGrantsAnnouncedPostCommit — grants the in-transaction
// mint produced are announced (PublishGranted) AFTER the award transaction
// returns, exactly once, with exactly the minted set.
func TestAwardExp_MintedGrantsAnnouncedPostCommit(t *testing.T) {
	minted := []growth.MintedPathGrant{
		{SkillKey: "explain_anew", SkillKind: "active", UnlockedVia: "species_path", UnlockedAtStage: 2},
		{SkillKey: "recap_scribe", SkillKind: "active", UnlockedVia: "species_path", UnlockedAtStage: 2},
	}
	unlocker := &fakeUnlocker{minted: minted}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if len(unlocker.publishCalls) != 1 {
		t.Fatalf("PublishGranted calls = %d, want 1", len(unlocker.publishCalls))
	}
	if got := unlocker.publishCalls[0]; len(got) != 2 || got[0].SkillKey != "explain_anew" || got[1].SkillKey != "recap_scribe" {
		t.Errorf("published grants = %+v, want the minted pair", got)
	}
}

// TestAwardExp_NothingMintedNothingAnnounced — a stage-up whose mint had
// nothing to do (all grants already owned) publishes no skill_granted.
func TestAwardExp_NothingMintedNothingAnnounced(t *testing.T) {
	unlocker := &fakeUnlocker{} // minted stays nil
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if len(unlocker.publishCalls) != 0 {
		t.Errorf("PublishGranted calls = %d, want 0 when nothing was minted", len(unlocker.publishCalls))
	}
}

// TestAwardExp_FreshNonStageUpDoesNotMint — the pre-atomic policy is
// preserved: an ordinary award (no stage transition, not a duplicate)
// never invokes the unlocker.
func TestAwardExp_FreshNonStageUpDoesNotMint(t *testing.T) {
	unlocker := &fakeUnlocker{}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 0) // 0 + 15 = 15 < 50 → no stage-up

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if len(unlocker.calls) != 0 {
		t.Errorf("mint calls = %d on a non-stage-up award, want 0", len(unlocker.calls))
	}
}

// TestAwardExp_DuplicateRepairMintAnnouncedPostCommit — the repair lane: a
// duplicate replay whose in-transaction mint actually healed lagging grants
// announces them after the replay transaction, with no growth events.
func TestAwardExp_DuplicateRepairMintAnnouncedPostCommit(t *testing.T) {
	unlocker := &fakeUnlocker{}
	svc, repo, ox := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err != nil {
		t.Fatalf("first AwardExp: %v", err)
	}
	topicsBefore := len(ox.topics())

	// The replayed mint heals a lagging grant this time.
	healed := []growth.MintedPathGrant{{SkillKey: "quiz_me", SkillKind: "active", UnlockedVia: "species_path", UnlockedAtStage: 2}}
	unlocker.mu.Lock()
	unlocker.minted = healed
	unlocker.mu.Unlock()

	resp, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err != nil {
		t.Fatalf("duplicate AwardExp: %v", err)
	}
	if !resp.Duplicate {
		t.Fatal("expected the duplicate replay")
	}
	if len(ox.topics()) != topicsBefore {
		t.Errorf("duplicate replay emitted growth events: %v", ox.topics()[topicsBefore:])
	}
	if len(unlocker.publishCalls) != 1 || len(unlocker.publishCalls[0]) != 1 || unlocker.publishCalls[0][0].SkillKey != "quiz_me" {
		t.Errorf("publish calls = %+v, want exactly the healed quiz_me grant", unlocker.publishCalls)
	}
}

// TestAwardExp_PublishFaultAfterCommitSurfacesButAwardPersists — a publish
// failure AFTER the award+mint committed surfaces loudly (NACK →
// redelivery re-announces via the idempotent replay) but never unwinds the
// committed award: durable state beats announcements.
func TestAwardExp_PublishFaultAfterCommitSurfacesButAwardPersists(t *testing.T) {
	unlocker := &fakeUnlocker{
		minted:     []growth.MintedPathGrant{{SkillKey: "explain_anew", SkillKind: "active"}},
		publishErr: errors.New("outbox write refused"),
	}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 40)

	if _, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15)); err == nil {
		t.Fatal("AwardExp must surface the post-commit publish failure")
	}
	row := repo.rows["fam-1"]
	if row.GrowthExp != 55 || row.GrowthStage != 2 {
		t.Errorf("row = exp %d / stage %d, want 55 / 2 (award + grants committed before the publish phase)", row.GrowthExp, row.GrowthStage)
	}
	if len(repo.events) != 1 {
		t.Errorf("ledger events = %d, want 1 (commit precedes the publish phase)", len(repo.events))
	}
}

// hookDroppingRepo simulates a Repository that violates the PostAwardInTx
// contract by never running the hook.
type hookDroppingRepo struct{ *fakeRepo }

func (r *hookDroppingRepo) AwardExpTx(ctx context.Context, in growth.AwardExpTxInput) (*growth.AwardExpTxOutput, error) {
	in.PostAwardInTx = nil
	return r.fakeRepo.AwardExpTx(ctx, in)
}

// TestAwardExp_RepoSkippingHookFailsLoud — the service's contract guard: a
// Repository that skips the in-transaction hook (silently reintroducing the
// half-grown window) turns into a loud error, never a quiet success.
func TestAwardExp_RepoSkippingHookFailsLoud(t *testing.T) {
	repo := newFakeRepo()
	dropping := &hookDroppingRepo{fakeRepo: repo}
	unlocker := &fakeUnlocker{}
	svc, _ := newServiceWithRepo(t, dropping, func(c *growth.ServiceConfig) { c.SkillUnlocker = unlocker })
	seedStage1Row(repo, 0)

	_, err := svc.AwardExp(context.Background(), baseAward("junction_accepted", 15))
	if err == nil {
		t.Fatal("AwardExp must fail loud when the repository never runs PostAwardInTx")
	}
	if !strings.Contains(err.Error(), "PostAwardInTx") {
		t.Errorf("error %q does not name the broken PostAwardInTx contract", err)
	}
}
