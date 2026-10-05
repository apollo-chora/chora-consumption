// service_cover_test.go — error-path + edge-branch coverage for the growth
// domain Service. Black-box (package growth_test); reuses newService/fakeRepo/
// fakeOutbox/stubDist from service_test.go and adds an error-injecting
// Repository to reach the repo-failure arms the happy-path fakes never trip.
package growth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// errRepo is a Repository whose every method can be made to fail on demand.
// It exists to drive the "repo returned err → propagate" branches in the
// Service methods that the in-memory fakeRepo cannot inject into.
type errRepo struct {
	getErr         error
	getRow         *growth.CompanionGrowthRow
	setResonantErr error
	setResonantRow *growth.CompanionGrowthRow
	bornErr        error
	bornRow        *growth.CompanionGrowthRow
	markAhaErr     error
	markAhaRow     *growth.CompanionGrowthRow
	countErr       error
	countN         int
	ownedErr       error
	commitErr      error
	commitRow      *growth.CompanionGrowthRow
	provisionErr   error
	provisionRow   *growth.CompanionGrowthRow
	revealErr      error
	revealOut      *growth.RevealTxOutput
}

func (r *errRepo) CommitReveal(_ context.Context, _ growth.RevealTxInput) (*growth.RevealTxOutput, error) {
	if r.revealErr != nil {
		return nil, r.revealErr
	}
	if r.revealOut != nil {
		return r.revealOut, nil
	}
	return nil, errors.New("errRepo: CommitReveal not configured")
}

func (r *errRepo) GetGrowthRow(_ context.Context, _, _ string) (*growth.CompanionGrowthRow, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.getRow, nil
}

func (r *errRepo) AwardExpTx(_ context.Context, _ growth.AwardExpTxInput) (*growth.AwardExpTxOutput, error) {
	return nil, errors.New("errRepo: AwardExpTx not configured")
}

func (r *errRepo) CommitHatch(_ context.Context, _ growth.HatchTxInput) (*growth.CompanionGrowthRow, error) {
	if r.commitErr != nil {
		return nil, r.commitErr
	}
	return r.commitRow, nil
}

func (r *errRepo) CommitBornHatched(_ context.Context, _ growth.BornHatchedTxInput) (*growth.CompanionGrowthRow, error) {
	if r.bornErr != nil {
		return nil, r.bornErr
	}
	return r.bornRow, nil
}

func (r *errRepo) SetResonantConcept(_ context.Context, _, _ string, _ *string) (*growth.CompanionGrowthRow, error) {
	if r.setResonantErr != nil {
		return nil, r.setResonantErr
	}
	return r.setResonantRow, nil
}

func (r *errRepo) MarkAhaMoment(_ context.Context, _ growth.AhaMomentInput) (*growth.CompanionGrowthRow, error) {
	if r.markAhaErr != nil {
		return nil, r.markAhaErr
	}
	return r.markAhaRow, nil
}

func (r *errRepo) CountCompanionsOfSpecies(_ context.Context, _, _, _, _ string) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.countN, nil
}

// OwnerSpeciesSet: an empty owned set by default, so the no-repeat exclusion is
// a pass-through in these error-path covers (they assert other failures).
func (r *errRepo) OwnerSpeciesSet(_ context.Context, _, _, _ string) (map[string]bool, error) {
	if r.ownedErr != nil {
		return nil, r.ownedErr
	}
	return map[string]bool{}, nil
}

func (r *errRepo) ListGrowthEvents(_ context.Context, _ growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	return &growth.ListGrowthEventsOutput{}, nil
}

func (r *errRepo) ProvisionEgg(_ context.Context, _ growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error) {
	if r.provisionErr != nil {
		return nil, r.provisionErr
	}
	return r.provisionRow, nil
}

// newServiceWithRepo wires the Service with a caller-supplied Repository, a
// fresh fakeOutbox, and a fixed clock/id.
func newServiceWithRepo(t *testing.T, repo growth.Repository, opts ...func(*growth.ServiceConfig)) (*growth.Service, *fakeOutbox) {
	t.Helper()
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
	return svc, ox
}

// ----- GetCompanionGrowth: repo error -----

func TestGetCompanionGrowth_RepoError(t *testing.T) {
	sentinel := errors.New("db down")
	svc, _ := newServiceWithRepo(t, &errRepo{getErr: sentinel})
	_, err := svc.GetCompanionGrowth(context.Background(), "t", "fam-1", "u")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected repo error propagated, got %v", err)
	}
}

// ----- AwardExp: AwardExpTx repo error -----

func TestAwardExp_RepoTxError(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1", GrowthStage: 1,
	}
	repo.awardErr = errors.New("tx aborted")
	_, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "evt-x",
		Traceparent: "00-aa-bb-00",
	})
	if err == nil {
		t.Errorf("expected AwardExpTx error to propagate")
	}
}

// ----- AwardExp: stage_up publish failure (second publish in the method) -----

func TestAwardExp_StageUpPublishFailureSurfaces(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 48, // +3 crosses 50 → stage 2 → stage_up emitted
	}
	// Fail ONLY the stage_up publish; exp_awarded must succeed first.
	ox.failFn = func(topic string) error {
		if topic == "chora.consumption.companion.stage_up.v1" {
			return errors.New("stage_up publish failed")
		}
		return nil
	}
	_, err := svc.AwardExp(context.Background(), growth.AwardExpInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "evt-x",
		Traceparent: "00-aa-bb-00",
	})
	if err == nil {
		t.Fatalf("expected stage_up publish error to surface")
	}
	// exp_awarded must have been published before the stage_up failure.
	topics := ox.topics()
	if len(topics) != 1 || topics[0] != "chora.consumption.companion.exp_awarded.v1" {
		t.Errorf("expected exp_awarded published before stage_up failure, got %v", topics)
	}
}

// ----- HatchEgg: input validation arms not covered by service_test/coverage_test -----

func TestHatchEgg_MissingTenant(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "", CompanionID: "fam-1", OwnerGCID: "u",
		DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-a-b-00",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for missing tenant, got %v", err)
	}
}

func TestHatchEgg_MissingDisplayName(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "t", CompanionID: "fam-1", OwnerGCID: "u",
		DisplayName: "  ", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-a-b-00",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for blank display_name, got %v", err)
	}
}

func TestHatchEgg_BadPersona(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "t", CompanionID: "fam-1", OwnerGCID: "u",
		DisplayName: "x", Tone: "socratic", LearnerPersona: "evil-genius",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-a-b-00",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for bad persona, got %v", err)
	}
}

func TestHatchEgg_MissingTraceparent(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "t", CompanionID: "fam-1", OwnerGCID: "u",
		DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for missing traceparent, got %v", err)
	}
}

// ----- HatchEgg: GetGrowthRow miss → repo error propagated (no row set) -----

func TestHatchEgg_GetGrowthRowError(t *testing.T) {
	svc, _, _ := newService(t) // no rows seeded → GetGrowthRow returns ErrCompanionNotFound
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "t", CompanionID: "missing", OwnerGCID: "u",
		DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-a-b-00",
	})
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("expected ErrCompanionNotFound from repo, got %v", err)
	}
}

// ----- RevealBreed: CountCompanionsOfSpecies error (the shiny lookup moved
// to the reveal with the roll — CHO-2229) -----

func TestRevealBreed_ShinyLookupError(t *testing.T) {
	row := &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "t", OwnerGCID: "u", GrowthStage: 0,
		GrowthExp: 30, EggSku: "egg.standard.v1", // stirring so the gate passes to the lookup
	}
	svc, _ := newServiceWithRepo(t, &errRepo{
		getRow:   row,
		countErr: errors.New("count query failed"),
	})
	_, err := svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "t", CompanionID: "fam-egg", OwnerGCID: "u", Traceparent: "00-a-b-00",
	})
	if err == nil {
		t.Errorf("expected shiny-lookup error to propagate")
	}
}

// ----- RevealBreed: RollBreed error (Lookup succeeds but distribution
// invalid — the roll lives in the reveal since CHO-2229) -----

func TestRevealBreed_RollBreedError(t *testing.T) {
	// stubDist returns a distribution that Lookup accepts but RollBreed's
	// ValidateBreedDistribution rejects (sum far from 100), driving the
	// "roll breed" error arm in RevealBreed distinct from the Lookup-error arm.
	badDist := []growth.BreedWeight{
		{Species: "owl", Probability: 10, Rarity: "common"},
		{Species: "fox", Probability: 10, Rarity: "common"}, // sum=20, not ~100
	}
	svc, repo, _ := newService(t, func(c *growth.ServiceConfig) {
		c.Dist = stubDist{dist: badDist}
	})
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "t", OwnerGCID: "u", GrowthStage: 0,
		GrowthExp: 30, EggSku: "egg.standard.v1", // F-I1.3: stirring so the gate passes to the roll
	}
	_, err := svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "t", CompanionID: "fam-egg", OwnerGCID: "u", Traceparent: "00-a-b-00",
	})
	if err == nil {
		t.Fatalf("expected roll-breed error from invalid distribution")
	}
	if !errors.Is(err, growth.ErrInvalidDistribution) {
		t.Errorf("expected ErrInvalidDistribution wrapped, got %v", err)
	}
}

// ----- HatchEgg: CommitHatch error -----

func TestHatchEgg_CommitError(t *testing.T) {
	svc, repo, _ := newService(t)
	revealedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "t", OwnerGCID: "u", GrowthStage: 0,
		GrowthExp: 30, EggSku: "egg.standard.v1",
		Species: "owl", SpeciesRarity: "common", RolledProb: 50, RevealedAt: &revealedAt,
	}
	repo.hatchErr = errors.New("commit failed")
	_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
		TenantID: "t", CompanionID: "fam-egg", OwnerGCID: "u",
		DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-a-b-00",
	})
	if err == nil || !strings.Contains(err.Error(), "commit failed") {
		t.Errorf("expected CommitHatch error to propagate, got %v", err)
	}
}

// ----- RevealBreed: the breed_revealed publish can fail (CHO-2229 — it
// fires at reveal time now) -----

func TestRevealBreed_PublishFailure(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "t", OwnerGCID: "u", GrowthStage: 0,
		GrowthExp: 30, EggSku: "egg.standard.v1",
	}
	ox.failFn = func(topic string) error {
		if topic == "chora.consumption.companion.breed_revealed.v1" {
			return errors.New("publish failed: " + topic)
		}
		return nil
	}
	_, err := svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "t", CompanionID: "fam-egg", OwnerGCID: "u", Traceparent: "00-a-b-00",
	})
	if err == nil {
		t.Errorf("expected error when breed_revealed publish fails")
	}
}

// ----- HatchEgg: each of the commit-lane publishes can fail independently -----

func TestHatchEgg_PublishFailures(t *testing.T) {
	failTopics := []string{
		"chora.consumption.companion.hatched.v1",
		"chora.consumption.companion.stage_up.v1",
	}
	for _, failTopic := range failTopics {
		t.Run(failTopic, func(t *testing.T) {
			svc, repo, ox := newService(t)
			revealedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
			repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
				CompanionID: "fam-egg", TenantID: "t", OwnerGCID: "u", GrowthStage: 0,
				GrowthExp: 30, EggSku: "egg.standard.v1",
				Species: "owl", SpeciesRarity: "common", RolledProb: 50, RevealedAt: &revealedAt,
			}
			ox.failFn = func(topic string) error {
				if topic == failTopic {
					return errors.New("publish failed: " + topic)
				}
				return nil
			}
			_, err := svc.HatchEgg(context.Background(), growth.HatchEggInput{
				TenantID: "t", CompanionID: "fam-egg", OwnerGCID: "u",
				DisplayName: "x", Tone: "socratic", LearnerPersona: "curious-explorer",
				ResonantAtomID: "01971a90-1111-7000-8000-000000000001", Traceparent: "00-a-b-00",
			})
			if err == nil {
				t.Errorf("expected error when %s publish fails", failTopic)
			}
		})
	}
}

// ----- TriggerSourceRevelation: missing traceparent (first guard) -----

func TestTriggerSourceRevelation_MissingTraceparent(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "t", CompanionID: "fam-1", OwnerGCID: "u",
		ManaTier: "premium", Traceparent: "",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for missing traceparent, got %v", err)
	}
}

// ----- TriggerSourceRevelation: GetGrowthRow miss -----

func TestTriggerSourceRevelation_GetGrowthRowError(t *testing.T) {
	svc, _, _ := newService(t) // no rows
	_, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "t", CompanionID: "missing", OwnerGCID: "u",
		ManaTier: "basic", Traceparent: "00-a-b-00",
	})
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Errorf("expected ErrCompanionNotFound, got %v", err)
	}
}

// ----- TriggerSourceRevelation: MarkAhaMoment repo error -----

func TestTriggerSourceRevelation_MarkAhaError(t *testing.T) {
	row := &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "u", GrowthStage: 3,
	}
	svc, _ := newServiceWithRepo(t, &errRepo{
		getRow:     row,
		markAhaErr: errors.New("mark failed"),
	})
	_, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "t", CompanionID: "fam-1", OwnerGCID: "u",
		ManaTier: "premium", Traceparent: "00-a-b-00",
	})
	if err == nil {
		t.Errorf("expected MarkAhaMoment error to propagate")
	}
}

// ----- TriggerSourceRevelation: publish failure -----

func TestTriggerSourceRevelation_PublishFailure(t *testing.T) {
	svc, repo, ox := newService(t)
	repo.rows["fam-1"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "u", GrowthStage: 3,
	}
	ox.failFn = func(_ string) error { return errors.New("publish down") }
	_, err := svc.TriggerSourceRevelation(context.Background(), growth.TriggerSourceRevelationInput{
		TenantID: "t", CompanionID: "fam-1", OwnerGCID: "u",
		ManaTier: "premium", Traceparent: "00-a-b-00",
	})
	if err == nil {
		t.Errorf("expected source_revelation publish error to surface")
	}
}

// ----- ProvisionEgg: repo error -----

func TestProvisionEgg_RepoError(t *testing.T) {
	svc, _ := newServiceWithRepo(t, &errRepo{
		provisionErr: errors.New("insert failed"),
	})
	_, err := svc.ProvisionEgg(context.Background(), growth.ProvisionEggInput{
		TenantID: "t", OwnerGCID: "u", EggSku: "egg.s.v1", EggPurchaseID: "p1",
		EggSource: "purchase", Traceparent: "00-aa-bb-00",
	})
	if err == nil {
		t.Errorf("expected ProvisionEgg repo error to propagate")
	}
}

// ----- ProvisionEgg: publish failure (row still returned per current behavior) -----

func TestProvisionEgg_PublishFailureReturnsRow(t *testing.T) {
	row := &growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "u", GrowthStage: 0,
		EggPurchaseID: "p1",
	}
	svc, ox := newServiceWithRepo(t, &errRepo{provisionRow: row})
	ox.failFn = func(_ string) error { return errors.New("publish down") }
	got, err := svc.ProvisionEgg(context.Background(), growth.ProvisionEggInput{
		TenantID: "t", OwnerGCID: "u", EggSku: "egg.s.v1", EggPurchaseID: "p1",
		EggSource: "purchase", Traceparent: "00-aa-bb-00",
	})
	if err == nil {
		t.Fatalf("expected publish error to surface")
	}
	// Current behavior: the persisted row is still returned alongside the error.
	if got == nil {
		t.Errorf("expected provisioned row returned even on publish failure")
	}
}

// ----- NewService default Clock + NewID actually drive a method call -----

// TestNewService_DefaultClockExercisedViaProvision constructs a Service with a
// nil Clock (default wired in NewService), then runs ProvisionEgg so the
// default time.Now Clock is actually invoked and the expiries derive from it.
// CHO-2225: NewID is injected — it no longer defaults, so only Clock is under
// test here.
func TestNewService_DefaultClockExercisedViaProvision(t *testing.T) {
	repo := newFakeRepo()
	ox := &fakeOutbox{}
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: ox,
		Dist:   stubDist{dist: defaultDistribution()},
		NewID:  func() string { return "019f6a1c-89f8-7089-8142-e3af46113b6b" },
		// Clock nil → NewService installs time.Now().UTC.
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	row, err := svc.ProvisionEgg(context.Background(), growth.ProvisionEggInput{
		TenantID: "t", OwnerGCID: "u", EggSku: "egg.s.v1", EggPurchaseID: "p-defaults",
		EggSource: "purchase", Traceparent: "00-aa-bb-00",
		// Now zero → default Clock fills it; expiries auto-derived.
	})
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}
	if row.EggSoftExpiry == nil || row.EggHardExpiry == nil {
		t.Errorf("expected expiries auto-derived from default clock")
	}
	if len(ox.topics()) != 1 {
		t.Errorf("expected egg_purchased published, got %v", ox.topics())
	}
}
