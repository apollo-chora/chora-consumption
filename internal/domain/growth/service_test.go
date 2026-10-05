package growth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ----- fakes -----

type fakeRepo struct {
	mu                 sync.Mutex
	rows               map[string]*growth.CompanionGrowthRow
	events             []*growth.GrowthEventRow
	dailyCounters      map[string]int
	idempotencyHits    map[string]bool
	awardErr           error
	hatchErr           error
	speciesRosterCount int
	awardDuplicateFlag bool
	provisionedSamePID *growth.CompanionGrowthRow
	ownedSpecies       map[string]bool // species the learner already has (2026-08-07)
	ownedSpeciesErr    error
}

// OwnerSpeciesSet returns the configured owned-species set (owner ruling
// 2026-08-07, the no-repeat inversion). Tests populate ownedSpecies to simulate
// an existing roster; an empty map ⇒ every species is still unseen.
func (r *fakeRepo) OwnerSpeciesSet(_ context.Context, _, _, _ string) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ownedSpeciesErr != nil {
		return nil, r.ownedSpeciesErr
	}
	out := make(map[string]bool, len(r.ownedSpecies))
	for sp := range r.ownedSpecies {
		out[sp] = true
	}
	return out, nil
}

// owns marks species as already in the learner's roster.
func (r *fakeRepo) owns(species ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ownedSpecies == nil {
		r.ownedSpecies = make(map[string]bool, len(species))
	}
	for _, sp := range species {
		r.ownedSpecies[sp] = true
	}
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		rows:            make(map[string]*growth.CompanionGrowthRow),
		dailyCounters:   make(map[string]int),
		idempotencyHits: make(map[string]bool),
	}
}

func (r *fakeRepo) GetGrowthRow(_ context.Context, tenantID, companionID string) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[companionID]
	if !ok {
		return nil, growth.ErrCompanionNotFound
	}
	if row.TenantID != tenantID {
		return nil, growth.ErrCompanionNotFound
	}
	clone := *row
	return &clone, nil
}

// AwardExpTx mirrors the pg adapter's transaction semantics (CHO-2039):
// writes are STAGED on clones, the PostAwardInTx hook runs, and only a nil
// hook error commits — a hook failure rolls the whole award back.
func (r *fakeRepo) AwardExpTx(ctx context.Context, in growth.AwardExpTxInput) (*growth.AwardExpTxOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.awardErr != nil {
		return nil, r.awardErr
	}
	row, ok := r.rows[in.CompanionID]
	if !ok {
		return nil, growth.ErrCompanionNotFound
	}
	idemKey := in.CompanionID + "|" + in.Source + "|" + in.IdempotencyKey
	if r.idempotencyHits[idemKey] {
		// Replay the existing row + signal duplicate. The in-tx hook
		// (repair lane) still runs; its error aborts the replay.
		clone := *row
		dupOut := &growth.AwardExpTxOutput{
			Row:           &clone,
			Duplicate:     true,
			NewStage:      clone.GrowthStage,
			PreviousStage: clone.GrowthStage,
		}
		if in.PostAwardInTx != nil {
			if err := in.PostAwardInTx(ctx, dupOut); err != nil {
				return nil, err
			}
		}
		return dupOut, nil
	}

	dayBucket := in.Now.UTC().Format("2006-01-02")
	counterKey := in.CompanionID + "|" + in.Source + "|" + dayBucket
	already := r.dailyCounters[counterKey]
	clamped, capHit := growth.ClampDeltaWithCap(in.DailyCap, already, in.RequestedDelta)

	staged := *row
	prevStage := staged.GrowthStage
	out := growth.StateAfterAward(prevStage, staged.GrowthExp, clamped)
	staged.GrowthExp = out.NewExp
	staged.GrowthStage = out.NewStage
	if out.StageUp {
		now := in.Now
		staged.LastStageUpAt = &now
	}
	llmTier := growth.LLMTierForStageAndMana(staged.GrowthStage, in.ManaTier)
	staged.EffectiveLLMTierCached = llmTier

	ev := &growth.GrowthEventRow{
		GrowthEventID:    "evt-" + idemKey,
		TenantID:         in.TenantID,
		CompanionID:      in.CompanionID,
		OwnerGCID:        in.OwnerGCID,
		Source:           in.Source,
		RequestedDelta:   in.RequestedDelta,
		AwardedDelta:     clamped,
		ExpTotalAfter:    staged.GrowthExp,
		DailyCapHit:      capHit,
		TriggeredStageUp: out.StageUp,
		IdempotencyKey:   in.IdempotencyKey,
		SourceEventID:    in.SourceEventID,
		SourceTopic:      in.SourceTopic,
		SourceSessionID:  in.SourceSessionID,
		SourceTurnSeq:    in.SourceTurnSeq,
		AwardedAt:        in.Now,
	}

	clone := staged
	txOut := &growth.AwardExpTxOutput{
		Row:                &clone,
		GrowthEvent:        ev,
		Duplicate:          false,
		ClampedDelta:       clamped,
		DailyCapHit:        capHit,
		TriggeredStageUp:   out.StageUp,
		PreviousStage:      prevStage,
		NewStage:           staged.GrowthStage,
		NewlyUnlockedTools: growth.NewlyUnlockedTools(prevStage, staged.GrowthStage),
		NewMemoryMode:      growth.MemoryModeForStage(staged.GrowthStage),
		EffectiveLLMTier:   llmTier,
	}
	if in.PostAwardInTx != nil {
		if err := in.PostAwardInTx(ctx, txOut); err != nil {
			return nil, err // nothing committed — the award rolls back
		}
	}

	// COMMIT the staged transaction.
	*row = staged
	r.dailyCounters[counterKey] = already + clamped
	r.events = append(r.events, ev)
	r.idempotencyHits[idemKey] = true
	return txOut, nil
}

// CommitReveal mirrors the pg adapter's semantics (CHO-2229): persist the
// roll once, guarded by revealed_at IS NULL — a second commit returns the
// original roll with Duplicate=true and never overwrites it.
func (r *fakeRepo) CommitReveal(_ context.Context, in growth.RevealTxInput) (*growth.RevealTxOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok {
		return nil, growth.ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, growth.ErrAlreadyHatched
	}
	if row.RevealedAt != nil {
		clone := *row
		return &growth.RevealTxOutput{Row: &clone, Duplicate: true}, nil
	}
	row.Species = in.Species
	row.ShinyVariant = in.ShinyVariant
	row.SpeciesRarity = in.SpeciesRarity
	row.RolledProb = in.RolledProbability
	now := in.Now
	row.RevealedAt = &now
	clone := *row
	return &growth.RevealTxOutput{Row: &clone}, nil
}

func (r *fakeRepo) CommitHatch(_ context.Context, in growth.HatchTxInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hatchErr != nil {
		return nil, r.hatchErr
	}
	row, ok := r.rows[in.CompanionID]
	if !ok {
		return nil, growth.ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, growth.ErrAlreadyHatched
	}
	// CHO-2229 commit-only: the roll is already on the row (CommitReveal);
	// an unrevealed pod cannot commit.
	if row.RevealedAt == nil {
		return nil, growth.ErrNotRevealed
	}
	row.GrowthStage = 1
	row.ResonantAtom = in.ResonantAtomID
	row.DisplayName = in.DisplayName
	row.Tone = in.Tone
	row.LearnerPersona = in.LearnerPersona
	now := in.Now
	row.HatchedAt = &now
	row.LastStageUpAt = &now
	clone := *row
	return &clone, nil
}

func (r *fakeRepo) MarkAhaMoment(_ context.Context, in growth.AhaMomentInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok {
		return nil, growth.ErrCompanionNotFound
	}
	if row.AhaMomentConsumed {
		return nil, growth.ErrAhaMomentConsumed
	}
	row.AhaMomentConsumed = true
	row.AhaMomentActiveUntil = &in.WindowExpiresAt
	row.AhaMomentPreviewLLMTier = in.PreviewLLMTier
	clone := *row
	return &clone, nil
}

func (r *fakeRepo) CountCompanionsOfSpecies(_ context.Context, _, _, _, _ string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.speciesRosterCount, nil
}

func (r *fakeRepo) ListGrowthEvents(_ context.Context, in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*growth.GrowthEventRow
	for _, ev := range r.events {
		if ev.CompanionID == in.CompanionID {
			out = append(out, ev)
		}
	}
	return &growth.ListGrowthEventsOutput{Events: out, NextPageToken: ""}, nil
}

func (r *fakeRepo) ProvisionEgg(_ context.Context, in growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.provisionedSamePID != nil &&
		r.provisionedSamePID.EggPurchaseID == in.EggPurchaseID {
		clone := *r.provisionedSamePID
		return &clone, nil
	}
	row := &growth.CompanionGrowthRow{
		CompanionID:    "fam-" + in.EggPurchaseID,
		TenantID:       in.TenantID,
		OwnerGCID:      in.OwnerGCID,
		GrowthStage:    0,
		EggSku:         in.EggSku,
		EggPurchaseID:  in.EggPurchaseID,
		EggSource:      in.EggSource,
		EggPurchasedAt: &in.Now,
		EggSoftExpiry:  &in.SoftExpiryAt,
		EggHardExpiry:  &in.HardExpiryAt,
	}
	r.rows[row.CompanionID] = row
	r.provisionedSamePID = row
	clone := *row
	return &clone, nil
}

type fakeOutbox struct {
	mu     sync.Mutex
	calls  []outboxCall
	failFn func(topic string) error
}

type outboxCall struct {
	Topic   string
	Payload map[string]any
	Env     growth.GrowthEnvelope
}

func (o *fakeOutbox) PublishGrowthEvent(_ context.Context, topic string, payload map[string]any, env growth.GrowthEnvelope) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failFn != nil {
		if err := o.failFn(topic); err != nil {
			return err
		}
	}
	o.calls = append(o.calls, outboxCall{Topic: topic, Payload: payload, Env: env})
	return nil
}

func (o *fakeOutbox) topics() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.calls))
	for _, c := range o.calls {
		out = append(out, c.Topic)
	}
	return out
}

type stubDist struct {
	dist []growth.BreedWeight
	err  error
}

func (s stubDist) Lookup(_, _ string) ([]growth.BreedWeight, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.dist, nil
}

// ----- service tests -----

func newService(t *testing.T, opts ...func(*growth.ServiceConfig)) (*growth.Service, *fakeRepo, *fakeOutbox) {
	t.Helper()
	repo := newFakeRepo()
	ox := &fakeOutbox{}
	cfg := growth.ServiceConfig{
		Repo:   repo,
		Outbox: ox,
		Dist:   stubDist{dist: defaultDistribution()},
		Clock:  func() time.Time { return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC) },
		NewID:  func() string { return "evt-test" },
	}
	for _, o := range opts {
		o(&cfg)
	}
	svc, err := growth.NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, repo, ox
}

func defaultDistribution() []growth.BreedWeight {
	return []growth.BreedWeight{
		{Species: "owl", Probability: 50, Rarity: "common"},
		{Species: "dragon", Probability: 50, Rarity: "legendary"},
	}
}

func TestAwardExp_HappyPath(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	}
	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-1",
		OwnerGCID:      "user-1",
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "evt-source-1",
		Traceparent:    "00-aaaa-bbbb-00",
	})
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if resp.ClampedDelta != 3 {
		t.Errorf("ClampedDelta = %d, want 3", resp.ClampedDelta)
	}
	if resp.Row.GrowthExp != 3 {
		t.Errorf("GrowthExp = %d, want 3", resp.Row.GrowthExp)
	}
	topics := ox.topics()
	if len(topics) != 1 || topics[0] != "chora.consumption.companion.exp_awarded.v1" {
		t.Errorf("topics = %v, want exp_awarded.v1", topics)
	}
}

func TestAwardExp_StageUp_PublishesBothEvents(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 48,
	}
	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-1",
		OwnerGCID:      "user-1",
		Source:         "atom_session",
		RequestedDelta: 3,
		IdempotencyKey: "evt-source-1",
		Traceparent:    "00-aaaa-bbbb-00",
	})
	if err != nil {
		t.Fatalf("AwardExp: %v", err)
	}
	if !resp.StageUpTriggered {
		t.Errorf("expected stage_up_triggered=true")
	}
	if resp.Row.GrowthStage != 2 {
		t.Errorf("GrowthStage = %d, want 2", resp.Row.GrowthStage)
	}
	topics := ox.topics()
	// CHO-2012: a stage transition additionally announces its slot change
	// (skill_slot_unlocked, ADR-218 D2).
	if len(topics) != 3 {
		t.Errorf("expected 3 topics, got %v", topics)
	}
	// exp_awarded → stage_up → skill_slot_unlocked — order matters for audit.
	if topics[0] != "chora.consumption.companion.exp_awarded.v1" {
		t.Errorf("first topic = %q", topics[0])
	}
	if topics[1] != "chora.consumption.companion.stage_up.v1" {
		t.Errorf("second topic = %q", topics[1])
	}
	if topics[2] != growth.TopicCompanionSkillSlotUnlocked {
		t.Errorf("third topic = %q", topics[2])
	}
}

func TestAwardExp_DailyCapClamped(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	}
	// daily_dose_open has cap=2 → second call clamps to 0.
	_, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-1",
		OwnerGCID:      "user-1",
		Source:         "daily_dose_open",
		RequestedDelta: 2,
		IdempotencyKey: "evt-1",
		Traceparent:    "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-1",
		OwnerGCID:      "user-1",
		Source:         "daily_dose_open",
		RequestedDelta: 2,
		IdempotencyKey: "evt-2",
		Traceparent:    "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if resp.ClampedDelta != 0 {
		t.Errorf("clamped = %d, want 0", resp.ClampedDelta)
	}
	if !resp.DailyCapHit {
		t.Errorf("expected DailyCapHit=true")
	}
}

func TestAwardExp_Duplicate(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	}
	_, _ = svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "evt-same",
		Traceparent: "00-aa-bb-00",
	})
	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "evt-same",
		Traceparent: "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	if !resp.Duplicate {
		t.Errorf("expected Duplicate=true")
	}
	if resp.ClampedDelta != 0 {
		t.Errorf("duplicate ClampedDelta = %d, want 0", resp.ClampedDelta)
	}
}

func TestAwardExp_RejectsInvalidSource(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "junk", RequestedDelta: 1, IdempotencyKey: "evt-1",
		Traceparent: "00-aa-bb-00",
	})
	if !errors.Is(err, growth.ErrInvalidSource) {
		t.Errorf("expected ErrInvalidSource, got %v", err)
	}
}

func TestAwardExp_RejectsNegativeDelta(t *testing.T) {
	// CHO-2012: delta 0 now means "use the resolved rule value" (ADR-218
	// D6) — only NEGATIVE deltas reject outright. Zero-with-zero-valued
	// source rejection is covered by TestAwardExp_ZeroEffectiveDeltaRejected.
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1", GrowthStage: 1}
	_, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: -5, IdempotencyKey: "evt-x",
		Traceparent: "00-aa-bb-00",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("delta=-5 should reject, got %v", err)
	}

	// delta=0 resolves the fallback rule (atom_session=3) and succeeds.
	resp, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 0, IdempotencyKey: "evt-y",
		Traceparent: "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("delta=0 (resolved default) should succeed, got %v", err)
	}
	if resp.ClampedDelta != 3 {
		t.Errorf("resolved-default delta = %d, want 3", resp.ClampedDelta)
	}
}

func TestCeremony_HappyPath_PublishesFourEvents(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: 30, EggSku: "egg.standard.v1", // F-I1.3: stirring (>= threshold)
	}
	// CHO-2229: the full ceremony is reveal → hatch. breed_revealed fires
	// at the reveal; the commit adds hatched + stage_up + the slot change
	// (CHO-2012, ADR-218 D2) — the same 4 topics in the same order the old
	// single-call hatch produced.
	if _, err := revealEgg(svc); err != nil {
		t.Fatalf("RevealBreed: %v", err)
	}
	resp, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-egg",
		OwnerGCID:      "user-1",
		DisplayName:    "Eira",
		Tone:           "socratic",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001",
		Traceparent:    "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("HatchEgg: %v", err)
	}
	if resp.Species == "" {
		t.Errorf("species not set")
	}
	if resp.Row.GrowthStage != 1 {
		t.Errorf("GrowthStage = %d, want 1", resp.Row.GrowthStage)
	}
	topics := ox.topics()
	if len(topics) != 4 {
		t.Fatalf("expected 4 topics, got %d: %v", len(topics), topics)
	}
	wantTopics := []string{
		"chora.consumption.companion.breed_revealed.v1",
		"chora.consumption.companion.hatched.v1",
		"chora.consumption.companion.stage_up.v1",
		growth.TopicCompanionSkillSlotUnlocked,
	}
	for i, w := range wantTopics {
		if topics[i] != w {
			t.Errorf("topic[%d] = %q, want %q", i, topics[i], w)
		}
	}
}

func TestRevealBreed_DetectsShinyOnDuplicate(t *testing.T) {
	// The shiny check rides the roll (CHO-2229: at the reveal); the hatch
	// commit then reads the persisted flag back.
	svc, repo, _ := newService(t)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: 30, EggSku: "egg.standard.v1", // F-I1.3: stirring (>= threshold)
	}
	repo.speciesRosterCount = 1 // caller already owns one of this species
	revealed, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("RevealBreed: %v", err)
	}
	if !revealed.ShinyVariant {
		t.Errorf("expected the reveal to detect ShinyVariant=true")
	}
	resp, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID:       "tenant-1",
		CompanionID:    "fam-egg",
		OwnerGCID:      "user-1",
		DisplayName:    "Eira",
		Tone:           "socratic",
		LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001",
		Traceparent:    "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("HatchEgg: %v", err)
	}
	if !resp.ShinyVariant {
		t.Errorf("expected the commit to carry the persisted ShinyVariant=true")
	}
}

func TestHatchEgg_RejectsAlreadyHatched(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1,
	}
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-aa-bb-00",
	})
	if !errors.Is(err, growth.ErrAlreadyHatched) {
		t.Errorf("expected ErrAlreadyHatched, got %v", err)
	}
}

func TestHatchEgg_RejectsBadTone(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0,
	}
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		DisplayName: "x", Tone: "evil-overlord", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-aa-bb-00",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestTriggerSourceRevelation_HappyPath(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 3,
	}
	resp, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		ManaTier: "premium", Traceparent: "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("TriggerSourceRevelation: %v", err)
	}
	if resp.PreviewLLMTier != "pro" {
		t.Errorf("preview tier = %q, want pro", resp.PreviewLLMTier)
	}
	// Anchor on the service's simulated clock (fixed at 2026-05-13 12:00:00 UTC in
	// newService) rather than time.Now(); the test must remain stable as wall-clock
	// advances past the fixed clock. WindowExpiresAt must be strictly after the
	// simulated "now" (default 24h window).
	clockNow := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	if resp.WindowExpiresAt.IsZero() || !resp.WindowExpiresAt.After(clockNow) {
		t.Errorf("WindowExpiresAt = %v, want non-zero and after %v", resp.WindowExpiresAt, clockNow)
	}
	if resp.WindowDurationSeconds != growth.DefaultAhaMomentWindowSeconds {
		t.Errorf("WindowDurationSeconds = %d, want %d", resp.WindowDurationSeconds, growth.DefaultAhaMomentWindowSeconds)
	}
	tx := ox.topics()
	if len(tx) != 1 || tx[0] != "chora.consumption.companion.source_revelation.v1" {
		t.Errorf("topics = %v", tx)
	}
}

func TestTriggerSourceRevelation_RejectsAlreadyConsumed(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 3, AhaMomentConsumed: true,
	}
	_, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		ManaTier: "premium", Traceparent: "00-aa-bb-00",
	})
	if !errors.Is(err, growth.ErrAhaMomentConsumed) {
		t.Errorf("expected ErrAhaMomentConsumed, got %v", err)
	}
}

func TestPreviewEggOdds(t *testing.T) {
	svc, _, _ := newService(t)
	resp, err := svc.PreviewEggOdds(context.Background(), growth.PreviewEggOddsInput{
		TenantID: "tenant-1", EggSku: "egg.standard.v1",
	})
	if err != nil {
		t.Fatalf("PreviewEggOdds: %v", err)
	}
	if len(resp.Odds) != 2 {
		t.Errorf("Odds len = %d, want 2", len(resp.Odds))
	}
	var sum float64
	for _, o := range resp.Odds {
		sum += o.Probability
	}
	if sum < 99 || sum > 101 {
		t.Errorf("sum = %f, want ~100", sum)
	}
	if resp.TotalWeight != sum {
		t.Errorf("TotalWeight = %f, want %f", resp.TotalWeight, sum)
	}
}

func TestProvisionEgg_PublishesEggPurchased(t *testing.T) {
	svc, _, ox := newService(t)
	_, err := svc.ProvisionEgg(context.Background(), growth.ProvisionEggInput{
		TenantID:        "tenant-1",
		OwnerGCID:       "user-1",
		EggSku:          "egg.standard.v1",
		EggPurchaseID:   "purchase-1",
		EggSource:       "purchase",
		Now:             time.Now(),
		SoftExpiryAt:    time.Now().AddDate(0, 0, 30),
		HardExpiryAt:    time.Now().AddDate(0, 0, 60),
		StripeSessionID: "cs_test_123",
		AmountCents:     1900,
		Currency:        "sgd",
		Traceparent:     "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:      "vendor=abc",
	})
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}
	if len(ox.topics()) != 1 || ox.topics()[0] != "chora.consumption.companion.egg_purchased.v1" {
		t.Fatalf("topics = %v", ox.topics())
	}
	call := ox.calls[0]
	// CHO-2028: the envelope must carry the REAL inbound payments traceparent,
	// not the retired "00-prov-egg-00" stub.
	if call.Env.Traceparent != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("envelope traceparent = %q, want the inbound payments traceparent", call.Env.Traceparent)
	}
	if call.Env.Tracestate != "vendor=abc" {
		t.Errorf("envelope tracestate = %q", call.Env.Tracestate)
	}
	// Schema fields the flat proto defines must be present in the payload.
	if got := call.Payload["stripe_session_id"]; got != "cs_test_123" {
		t.Errorf("payload stripe_session_id = %v", got)
	}
	if got := call.Payload["amount_cents"]; got != int64(1900) {
		t.Errorf("payload amount_cents = %v", got)
	}
	if got := call.Payload["currency"]; got != "sgd" {
		t.Errorf("payload currency = %v", got)
	}
}

func TestProvisionEgg_MissingTraceparent(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.ProvisionEgg(context.Background(), growth.ProvisionEggInput{
		TenantID:      "tenant-1",
		OwnerGCID:     "user-1",
		EggSku:        "egg.standard.v1",
		EggPurchaseID: "purchase-1",
		EggSource:     "purchase",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for missing traceparent, got %v", err)
	}
}

func TestGetCompanionGrowth(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 2, GrowthExp: 75, Species: "owl",
	}
	state, err := svc.GetCompanionGrowth(context.Background(), "tenant-1", "fam-1", "user-1")
	if err != nil {
		t.Fatalf("GetCompanionGrowth: %v", err)
	}
	if state.GrowthStage != 2 {
		t.Errorf("GrowthStage = %d", state.GrowthStage)
	}
	if state.Species != "owl" {
		t.Errorf("Species = %q", state.Species)
	}
}

func TestGetCompanionGrowth_CarriesDisplayName(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, Species: "owl", DisplayName: "Ember",
	}
	state, err := svc.GetCompanionGrowth(context.Background(), "tenant-1", "fam-1", "user-1")
	if err != nil {
		t.Fatalf("GetCompanionGrowth: %v", err)
	}
	// CHO-2028: the projected state carries the committed display name so
	// the profile header shows "Ember", not the untitled fallback.
	if state.DisplayName != "Ember" {
		t.Errorf("DisplayName = %q, want Ember", state.DisplayName)
	}
}

func TestListGrowthEvents_ReturnsAll(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.events = append(repo.events,
		&growth.GrowthEventRow{GrowthEventID: "e1", CompanionID: "fam-1", Source: "atom_session"},
		&growth.GrowthEventRow{GrowthEventID: "e2", CompanionID: "fam-1", Source: "hex_expand"},
	)
	resp, err := svc.ListGrowthEvents(context.Background(), growth.ListGrowthEventsInput{
		TenantID: "tenant-1", CompanionID: "fam-1", CallerGCID: "user-1", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
	if len(resp.Events) != 2 {
		t.Errorf("events = %d", len(resp.Events))
	}
}

func TestNewService_RejectsMissingDeps(t *testing.T) {
	_, err := growth.NewService(growth.ServiceConfig{})
	if err == nil || !strings.Contains(err.Error(), "repo") {
		t.Errorf("expected repo required error, got %v", err)
	}
}

func TestAwardExp_OutboxFailureSurfaces(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	}
	ox.failFn = func(topic string) error {
		return errors.New("simulated outbox failure")
	}
	_, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "evt-x",
		Traceparent: "00-aa-bb-00",
	})
	if err == nil {
		t.Errorf("expected outbox error")
	}
}
