package growth_test

// born_hatched_test.go — CHO-2013 P1 (R3-7 + the dev_hatched honest fix):
// sovereign acquisition mints a companion that is BORN HATCHED (stage 1, 2
// slots, hatched_at set) instead of a silent stage-0 egg nothing hatches.
// The learner may pick a hero species they have not met yet; omitted ⇒ the
// domain resolves one from the UNSEEN set and writes it explicitly (owner
// ruling 2026-08-07 - the blind 0046 column DEFAULT is never left standing).
// The egg funnel keeps its gacha roll+reveal (HatchEgg) - no breed_revealed here.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// CommitBornHatched — the born-hatched transition on the shared fakeRepo.
func (r *fakeRepo) CommitBornHatched(_ context.Context, in growth.BornHatchedTxInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok || row.TenantID != in.TenantID {
		return nil, growth.ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, growth.ErrAlreadyHatched
	}
	row.GrowthStage = 1
	if in.Species != "" {
		row.Species = in.Species
	}
	hatched := in.Now
	row.HatchedAt = &hatched
	cp := *row
	return &cp, nil
}

const (
	bornTenant = "019eb1b4-0000-7000-8000-00000000c0de"
	bornOwner  = "019eb1b4-0000-7000-8000-00000000face"
	bornFam    = "019f2620-0000-7000-8000-00000000f002"
)

func seedEggRow(repo *fakeRepo, defaultSpecies string) {
	repo.rows[bornFam] = &growth.CompanionGrowthRow{
		CompanionID: bornFam,
		TenantID:    bornTenant,
		OwnerGCID:   bornOwner,
		GrowthStage: 0,
		Species:     defaultSpecies, // 0046 random-hero DEFAULT rolls at INSERT
	}
}

func bornService(t *testing.T, repo *fakeRepo, out *fakeOutbox) *growth.Service {
	t.Helper()
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: out,
		Dist:   stubDist{dist: defaultDistribution()},
		Clock:  func() time.Time { return time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC) },
		NewID:  func() string { return "evt-born-1" },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func bornInput(species string) growth.InitBornHatchedInput {
	return growth.InitBornHatchedInput{
		TenantID:    bornTenant,
		CompanionID: bornFam,
		OwnerGCID:   bornOwner,
		Species:     species,
		DisplayName: "Cinder",
		Traceparent: "00-abc-def-01",
	}
}

func TestInitBornHatched_SpeciesPickOverridesDefault(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	out := &fakeOutbox{}
	svc := bornService(t, repo, out)

	resp, err := svc.InitBornHatched(context.Background(), bornInput("owl"))
	if err != nil {
		t.Fatalf("InitBornHatched: %v", err)
	}
	if resp.State.GrowthStage != 1 {
		t.Fatalf("stage = %d want 1", resp.State.GrowthStage)
	}
	if resp.Species != "owl" {
		t.Fatalf("effective species = %q want owl", resp.Species)
	}
	if repo.rows[bornFam].Species != "owl" || repo.rows[bornFam].HatchedAt == nil {
		t.Fatalf("row not born-hatched: %+v", repo.rows[bornFam])
	}
	topics := out.topics()
	wantTopics := map[string]bool{
		"chora.consumption.companion.hatched.v1":             false,
		"chora.consumption.companion.stage_up.v1":            false,
		"chora.consumption.companion.skill_slot_unlocked.v1": false,
	}
	for _, tp := range topics {
		if _, ok := wantTopics[tp]; ok {
			wantTopics[tp] = true
		}
		if tp == "chora.consumption.companion.breed_revealed.v1" {
			t.Fatalf("breed_revealed must NOT emit on a sovereign pick (no gacha reveal)")
		}
	}
	for tp, seen := range wantTopics {
		if !seen {
			t.Errorf("expected topic %s not emitted (got %v)", tp, topics)
		}
	}
}

// THE DB-DEFAULT HOLE (owner ruling 2026-08-07). companion_instances.species
// carries a 0046 column DEFAULT that picks randomly from the hero roster at
// INSERT, and insertCompanionInstanceSQL (the acquire path) omits the column, so
// the row arrives already carrying a species nobody chose. That default knows
// nothing about what the learner owns. An omitted pick must therefore be
// RESOLVED from the unseen set and written EXPLICITLY, never left standing.
func TestInitBornHatched_OmittedPickIsResolvedFromTheUnseenSet(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "penguin") // the blind 0046 DEFAULT already rolled penguin
	repo.owns("penguin", "fox", "owl", "dragon")
	svc := bornService(t, repo, &fakeOutbox{})

	resp, err := svc.InitBornHatched(context.Background(), bornInput(""))
	if err != nil {
		t.Fatalf("InitBornHatched: %v", err)
	}
	if resp.Species != "phoenix" {
		t.Fatalf("effective species = %q, want phoenix (the only unseen species); "+
			"the blind penguin DEFAULT must not survive", resp.Species)
	}
	if repo.rows[bornFam].Species != "phoenix" {
		t.Fatalf("row species = %q, want phoenix written EXPLICITLY over the DEFAULT",
			repo.rows[bornFam].Species)
	}
}

// An omitted pick with nothing owned still resolves explicitly: the species is
// chosen by the domain, so the persisted value is never the blind DEFAULT.
func TestInitBornHatched_OmittedPickIsExplicitEvenWithAnEmptyRoster(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "penguin")
	svc := bornService(t, repo, &fakeOutbox{})

	resp, err := svc.InitBornHatched(context.Background(), bornInput(""))
	if err != nil {
		t.Fatalf("InitBornHatched: %v", err)
	}
	if !growth.IsValidSpecies(resp.Species) {
		t.Fatalf("effective species = %q, want a canonical species", resp.Species)
	}
	if repo.rows[bornFam].Species != resp.Species {
		t.Fatalf("row species %q must equal the resolved species %q",
			repo.rows[bornFam].Species, resp.Species)
	}
}

// INVERTED (owner ruling 2026-08-07): an explicit pick of a species the learner
// ALREADY owns is refused while an unseen one remains. The retired directive
// refused the opposite - the pick that DIFFERED from the committed species.
func TestInitBornHatched_RejectsAPickTheLearnerAlreadyOwns(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	repo.owns("owl")
	svc := bornService(t, repo, &fakeOutbox{})

	_, err := svc.InitBornHatched(context.Background(), bornInput("owl"))
	if !errors.Is(err, growth.ErrSpeciesAlreadyOwned) {
		t.Fatalf("pick owl while owl is owned = %v, want ErrSpeciesAlreadyOwned", err)
	}
	// The row must NOT have hatched (fail-loud, no partial mint).
	if repo.rows[bornFam].HatchedAt != nil {
		t.Fatalf("row hatched despite the already-owned rejection: %+v", repo.rows[bornFam])
	}
}

// The mirror of the above: a pick the learner has NOT met is exactly what the
// ruling wants, and it must be honoured even though other species are owned.
func TestInitBornHatched_AllowsAnUnseenPick(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	repo.owns("owl", "penguin")
	svc := bornService(t, repo, &fakeOutbox{})

	resp, err := svc.InitBornHatched(context.Background(), bornInput("dragon"))
	if err != nil {
		t.Fatalf("unseen pick dragon = %v, want nil", err)
	}
	if resp.Species != "dragon" {
		t.Fatalf("effective species = %q want dragon", resp.Species)
	}
}

// WRAP: once every species is owned the repeat is allowed again, so a learner
// with the full roster can still acquire (never blocked).
func TestInitBornHatched_WrapAllowsARepeatOnceEverySpeciesIsOwned(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	repo.owns(growth.CanonicalSpecies...)
	svc := bornService(t, repo, &fakeOutbox{})

	resp, err := svc.InitBornHatched(context.Background(), bornInput("owl"))
	if err != nil {
		t.Fatalf("a wrapped roster must still acquire, got %v", err)
	}
	if resp.Species != "owl" {
		t.Fatalf("effective species = %q want owl (wrap honours the pick)", resp.Species)
	}
}

func TestInitBornHatched_InvalidSpeciesRejected(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	svc := bornService(t, repo, &fakeOutbox{})

	_, err := svc.InitBornHatched(context.Background(), bornInput("basilisk"))
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Fatalf("want ErrInvalidArguments, got %v", err)
	}
}

func TestInitBornHatched_AlreadyHatchedRejected(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	hatched := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	repo.rows[bornFam].GrowthStage = 1
	repo.rows[bornFam].HatchedAt = &hatched
	svc := bornService(t, repo, &fakeOutbox{})

	_, err := svc.InitBornHatched(context.Background(), bornInput("owl"))
	if !errors.Is(err, growth.ErrAlreadyHatched) {
		t.Fatalf("want ErrAlreadyHatched, got %v", err)
	}
}

func TestInitBornHatched_ForeignOwnerHidden(t *testing.T) {
	repo := newFakeRepo()
	seedEggRow(repo, "fox")
	svc := bornService(t, repo, &fakeOutbox{})

	in := bornInput("owl")
	in.OwnerGCID = "019eb1b4-0000-7000-8000-000000000bad"
	_, err := svc.InitBornHatched(context.Background(), in)
	if !errors.Is(err, growth.ErrCompanionNotFound) {
		t.Fatalf("want ErrCompanionNotFound, got %v", err)
	}
}
