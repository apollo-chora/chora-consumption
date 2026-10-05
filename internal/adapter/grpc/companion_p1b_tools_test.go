package grpc

// companion_p1b_tools_test.go — CHO-2013 P1.B (R4-2/R4-3):
//
//  1. ResolveCompanionConfig gains `allowed_tools` — the MERGED runtime tool
//     allowlist: innate(stage) ladder names mapped to canonical agent tool
//     names ∪ the equipped active Skills' tool_handler_refs (via the
//     catalogue), deduplicated + sorted. allowed_skills stays the equipped
//     skill KEYS. An equipped key missing from the catalogue is data
//     corruption → fail-loud Internal.
//  2. ReadLearnerProfile — read-only ADR-200 projection slice, scoped to the
//     companion's owner (foreign caller ⇒ NotFound; no cross-owner
//     disclosure); window filters facts + activity.
//  3. RecordCompanionMemoryNote — one learner-visible memory note through the
//     existing companion.Record port (embed + record, memory_type from the
//     closed note_type set; v1 = "recap"). Failures are LOUD (the write IS
//     the point — no chat-handler-style soft-fail).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// ----- fakes -----

type fakeLoadoutReader struct {
	grants []companion.SkillGrant
	err    error
}

func (f fakeLoadoutReader) ListGrants(_ context.Context, _, _ string) ([]companion.SkillGrant, error) {
	return f.grants, f.err
}

type fakeCatalogueReader struct {
	cat map[string]companion.CatalogEntry
	err error
}

func (f fakeCatalogueReader) ListCatalogue(_ context.Context) (map[string]companion.CatalogEntry, error) {
	return f.cat, f.err
}

type fakeProfileReader struct {
	facts    []*lp.Fact
	activity []*lp.ActivityEntry
	err      error
}

func (f fakeProfileReader) ListFacts(_ context.Context, _, _ string) ([]*lp.Fact, error) {
	return f.facts, f.err
}

func (f fakeProfileReader) RecentActivity(_ context.Context, _, _ string, _ int) ([]*lp.ActivityEntry, error) {
	return f.activity, f.err
}

type fakeMemoryRecorder struct {
	recorded []companion.RecordMemoryInput
	err      error
}

func (f *fakeMemoryRecorder) Record(_ context.Context, in companion.RecordMemoryInput) error {
	if f.err != nil {
		return f.err
	}
	f.recorded = append(f.recorded, in)
	return nil
}

type fakeEmbedder struct {
	vec []float32
	err error
}

func (f fakeEmbedder) Embed(_ context.Context, _ companion.EmbedInput) ([]float32, error) {
	return f.vec, f.err
}

func p1bInstance() *companion.Instance {
	return &companion.Instance{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		Name: "Cinder", Specialization: "Owls",
		SkillSlotsUnlocked: 3, MemoryContextCapacity: 4000,
		ConfiguredRules: map[string]string{},
	}
}

func p1bCatalogue() map[string]companion.CatalogEntry {
	return map[string]companion.CatalogEntry{
		"explain_anew":    {SkillKey: "explain_anew", SkillKind: companion.SkillKindActive, ToolHandlerRefs: []string{"atom.search", "atom.cite"}},
		"progress_mirror": {SkillKey: "progress_mirror", SkillKind: companion.SkillKindActive, ToolHandlerRefs: []string{"profile.read"}},
		"recap_scribe":    {SkillKey: "recap_scribe", SkillKind: companion.SkillKindActive, ToolHandlerRefs: []string{"memory.note"}},
	}
}

func equippedGrants(keys ...string) []companion.SkillGrant {
	out := make([]companion.SkillGrant, 0, len(keys))
	for _, k := range keys {
		out = append(out, companion.SkillGrant{SkillKey: k, SkillKind: companion.SkillKindActive, Equipped: true})
	}
	return out
}

func p1bServer(t *testing.T, opts ...CompanionGrowthOption) (*CompanionGrowthServer, *inmem.GrowthRepo) {
	t.Helper()
	repo := inmem.NewGrowthRepo()
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: &fakeGrowthOutbox{},
		Dist:   stubBreedDist{},
		Clock:  func() time.Time { return fixedClock },
		NewID:  func() string { return "evt-fixed" },
	})
	if err != nil {
		t.Fatalf("growth.NewService: %v", err)
	}
	s := NewCompanionGrowthServer(svc, opts...)
	return s, repo
}

func seedStage2(repo *inmem.GrowthRepo) {
	hatched := fixedClock.Add(-48 * time.Hour)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		GrowthStage: 2, Species: "penguin", HatchedAt: &hatched,
	})
}

// ----- 1. allowed_tools merge -----

func TestResolveCompanionConfig_AllowedToolsMergesInnateAndEquipped(t *testing.T) {
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithCompanionLoadoutReader(fakeLoadoutReader{grants: equippedGrants("explain_anew", "progress_mirror")}),
		WithSkillCatalogueReader(fakeCatalogueReader{cat: p1bCatalogue()}),
	)
	seedStage2(repo)

	resp, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if err != nil {
		t.Fatalf("ResolveCompanionConfig: %v", err)
	}
	cfg := resp.GetConfig()
	// allowed_skills — equipped KEYS only (cap check + prompt weave).
	if got := cfg.GetAllowedSkills(); len(got) != 2 || got[0] != "explain_anew" || got[1] != "progress_mirror" {
		t.Fatalf("allowed_skills = %v", got)
	}
	// allowed_tools: innate st2 contributes only atom.cite (atom_search is
	// declared unshipped per ADR-249 A1a) ∪ explain_anew{atom.search,
	// atom.cite} ∪ progress_mirror{profile.read}, deduplicated + sorted.
	// atom.search survives here ONLY via the equipped catalogue ref, which
	// keeps the R4-2 pass-through (the agent narrows unknown names safely).
	want := []string{"atom.cite", "atom.search", "profile.read"}
	got := cfg.GetAllowedTools()
	if len(got) != len(want) {
		t.Fatalf("allowed_tools = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowed_tools = %v want %v", got, want)
		}
	}
}

func TestResolveCompanionConfig_NoLoadoutStillCarriesInnateTools(t *testing.T) {
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithSkillCatalogueReader(fakeCatalogueReader{cat: p1bCatalogue()}),
	)
	seedStage2(repo)

	resp, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if err != nil {
		t.Fatalf("ResolveCompanionConfig: %v", err)
	}
	// ADR-249 A1a: innate st2 grants {cite_atom, atom_search} on the ladder,
	// but atom_search is declared unshipped, so the runtime allowlist
	// carries only the tool that exists.
	want := []string{"atom.cite"}
	got := resp.GetConfig().GetAllowedTools()
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("allowed_tools = %v want %v (innate st2, shipped only)", got, want)
	}
}

func TestResolveCompanionConfig_EquippedKeyMissingFromCatalogueFailsLoud(t *testing.T) {
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithCompanionLoadoutReader(fakeLoadoutReader{grants: equippedGrants("ghost_skill")}),
		WithSkillCatalogueReader(fakeCatalogueReader{cat: p1bCatalogue()}),
	)
	seedStage2(repo)

	_, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Internal {
		t.Fatalf("ghost equipped key: want Internal (data corruption), got %s", got)
	}
}

// ----- 2. ReadLearnerProfile -----

func profileFixture() ([]*lp.Fact, []*lp.ActivityEntry) {
	// Relative to the REAL clock — the window cutoff uses time.Now().
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-2 * 24 * time.Hour)
	return []*lp.Fact{
			{Type: lp.FactCourseCompleted, RefID: "course-1", Detail: lp.Detail{Label: "Owls 101"}, OccurredAt: old},
			{Type: lp.FactAssessmentGraded, RefID: "exam-9", Detail: lp.Detail{Label: "Night Flight Exam"}, OccurredAt: recent},
		}, []*lp.ActivityEntry{
			{Kind: "scored_assessment", Summary: "answered 10 owl atoms", OccurredAt: recent},
		}
}

func TestReadLearnerProfile_HappyPathWindowFilters(t *testing.T) {
	facts, activity := profileFixture()
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithLearnerProfileReader(fakeProfileReader{facts: facts, activity: activity}),
	)
	seedStage2(repo)

	resp, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Window: "week",
	})
	if err != nil {
		t.Fatalf("ReadLearnerProfile: %v", err)
	}
	if len(resp.GetFacts()) != 1 || resp.GetFacts()[0].GetFactType() != "assessment_graded" {
		t.Fatalf("week window facts = %+v", resp.GetFacts())
	}
	if len(resp.GetRecentActivity()) != 1 {
		t.Fatalf("recent_activity = %+v", resp.GetRecentActivity())
	}

	all, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Window: "all",
	})
	if err != nil {
		t.Fatalf("ReadLearnerProfile all: %v", err)
	}
	if len(all.GetFacts()) != 2 {
		t.Fatalf("all window facts = %d want 2", len(all.GetFacts()))
	}
}

func TestReadLearnerProfile_ForeignCallerHidden(t *testing.T) {
	facts, activity := profileFixture()
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithLearnerProfileReader(fakeProfileReader{facts: facts, activity: activity}),
	)
	seedStage2(repo)

	_, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "intruder", Window: "all",
	})
	if got := codeOf(t, err); got != codes.NotFound {
		t.Fatalf("foreign caller: want NotFound, got %s", got)
	}
}

func TestReadLearnerProfile_UnwiredUnimplemented(t *testing.T) {
	s, repo := p1bServer(t, WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}))
	seedStage2(repo)
	_, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Unimplemented {
		t.Fatalf("unwired: want Unimplemented, got %s", got)
	}
}

// ----- 3. RecordCompanionMemoryNote -----

func TestRecordCompanionMemoryNote_HappyPath(t *testing.T) {
	mem := &fakeMemoryRecorder{}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithCompanionMemoryNote(mem, fakeEmbedder{vec: []float32{0.1, 0.2}}, "embed-model-1"),
	)
	seedStage2(repo)

	resp, err := s.RecordCompanionMemoryNote(context.Background(), &consumptionv1.RecordCompanionMemoryNoteRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
		NoteType: "recap", Content: "Tonight we mastered owl wing anatomy.",
	})
	if err != nil {
		t.Fatalf("RecordCompanionMemoryNote: %v", err)
	}
	if !resp.GetRecorded() {
		t.Fatal("recorded = false")
	}
	if len(mem.recorded) != 1 {
		t.Fatalf("Record calls = %d", len(mem.recorded))
	}
	in := mem.recorded[0]
	if in.MemoryType != "recap" || in.CompanionID != "fam-1" || in.OwnerGCID != "g" || in.TenantID != "t" {
		t.Fatalf("RecordMemoryInput = %+v", in)
	}
	if in.ModelID != "embed-model-1" || len(in.Embedding) != 2 {
		t.Fatalf("embedding not threaded: %+v", in)
	}
}

func TestRecordCompanionMemoryNote_Validation(t *testing.T) {
	mem := &fakeMemoryRecorder{}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithCompanionMemoryNote(mem, fakeEmbedder{vec: []float32{0.1}}, "m"),
	)
	seedStage2(repo)

	cases := []struct {
		name string
		req  *consumptionv1.RecordCompanionMemoryNoteRequest
		want codes.Code
	}{
		{"unknown note_type", &consumptionv1.RecordCompanionMemoryNoteRequest{TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", NoteType: "diary", Content: "x"}, codes.InvalidArgument},
		{"empty content", &consumptionv1.RecordCompanionMemoryNoteRequest{TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", NoteType: "recap", Content: "   "}, codes.InvalidArgument},
		{"oversize content", &consumptionv1.RecordCompanionMemoryNoteRequest{TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", NoteType: "recap", Content: strings.Repeat("a", 2001)}, codes.InvalidArgument},
		{"foreign caller", &consumptionv1.RecordCompanionMemoryNoteRequest{TenantId: "t", CompanionId: "fam-1", CallerGcid: "intruder", NoteType: "recap", Content: "x"}, codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.RecordCompanionMemoryNote(context.Background(), tc.req)
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("want %s got %s", tc.want, got)
			}
		})
	}
	if len(mem.recorded) != 0 {
		t.Fatalf("invalid requests must not record: %d", len(mem.recorded))
	}
}

func TestRecordCompanionMemoryNote_EmbedFailureIsLoud(t *testing.T) {
	mem := &fakeMemoryRecorder{}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithCompanionMemoryNote(mem, fakeEmbedder{err: errors.New("embedder down")}, "m"),
	)
	seedStage2(repo)

	_, err := s.RecordCompanionMemoryNote(context.Background(), &consumptionv1.RecordCompanionMemoryNoteRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", NoteType: "recap", Content: "x",
	})
	if got := codeOf(t, err); got != codes.Internal {
		t.Fatalf("embed failure: want Internal (the write IS the point), got %s", got)
	}
	if len(mem.recorded) != 0 {
		t.Fatal("must not record without an embedding")
	}
}
