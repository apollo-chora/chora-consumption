// branch_cover_test.go — statement coverage for the in-memory adapter
// branches not driven by the feature tests:
//
//   - atom_index: MarkPublished (0%), Save's empty-Status default, the
//     SearchForLearner limit clamps, and atomMatchesTopic's empty-tag skip.
//   - growth: every failure branch of AwardExpTx / CommitReveal /
//     CommitHatch / MarkAhaMoment, plus the wholly-untested CommitBornHatched,
//     SetResonantConcept and ProvisionEgg zero-Now branch.
//   - learning_path: GetBySourceCollection and GetByStudyListEventID (0%).
//   - user_kg: the multi-row sort closures (ListByCluster / LoadTrail /
//     ListPendingForUser) and the negative-filter `continue` branches
//     (soft-deleted / foreign-row) in MarkInvalidatedBy*, FindInvalidatedOlderThan
//     and AtomSemanticEdgesRepo.ListByTarget.
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ---------- atom_index ----------

// TestAtomIndexRepo_MarkPublished drives the whole MarkPublished path: the
// not-projected no-op, the non-downgrading status flip, the authoritative MCQ
// key, and the cognitive-level normalize/blank-clobber rules.
func TestAtomIndexRepo_MarkPublished(t *testing.T) {
	ctx := context.Background()
	r := NewAtomIndexRepo()

	// Unknown atom → no-op (no error).
	if err := r.MarkPublished(ctx, "missing", atom_index.StatusPublished, "mcq", "opt_9", 3, "application"); err != nil {
		t.Fatalf("MarkPublished(missing): %v", err)
	}

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	draft, _ := atom_index.New(atom_index.NewParams{
		AtomID: "a1", TenantID: "t1", CourseID: "c1", Title: "T",
		AtomType: "mcq", TopicTags: []string{"agile"}, PublishedAt: now,
		// CognitiveLevel left blank: MarkPublished must set it, and a later blank
		// never clobbers a known level.
	})
	if err := r.Save(ctx, draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// Flip to published with an explicit canonical-ish level (proto name form).
	if err := r.MarkPublished(ctx, "a1", atom_index.StatusPublished, "mcq", "opt_1", 2, "COGNITIVE_LEVEL_APPLICATION"); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	got, err := r.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != atom_index.StatusPublished {
		t.Errorf("Status = %q, want published", got.Status)
	}
	if got.AtomType != "mcq" || got.CorrectOptionID != "opt_1" || got.AnswerCount != 2 {
		t.Errorf("key = (%q,%q,%d), want (mcq,opt_1,2)", got.AtomType, got.CorrectOptionID, got.AnswerCount)
	}
	if got.CognitiveLevel != "application" {
		t.Errorf("CognitiveLevel = %q, want application (normalized from proto name)", got.CognitiveLevel)
	}

	// Second MarkPublished with a BLANK level must not clobber the known level.
	if err := r.MarkPublished(ctx, "a1", atom_index.StatusDraft, "mcq", "opt_omitted", 0, ""); err != nil {
		t.Fatalf("MarkPublished blank level: %v", err)
	}
	draft2, _ := r.Get(ctx, "a1")
	if draft2.CognitiveLevel != "application" {
		t.Errorf("cognitive level = %q, want application (blank must not clobber)", draft2.CognitiveLevel)
	}
}

// TestAtomIndexRepo_Save_EmptyStatusDefaultsToDraft covers the `a.Status == ""`
// default in Save. atom_index.New() already defaults the field, so this branch
// only fires for a hand-built projection with an empty Status.
func TestAtomIndexRepo_Save_EmptyStatusDefaultsToDraft(t *testing.T) {
	ctx := context.Background()
	r := NewAtomIndexRepo()
	if err := r.Save(ctx, &atom_index.AtomIndex{AtomID: "raw", TenantID: "t1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "raw")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != atom_index.StatusDraft {
		t.Errorf("Status = %q, want draft (empty default)", got.Status)
	}
}

// TestAtomIndexRepo_SearchForLearner_LimitClampsAndEmptyTag covers the
// searchDefaultLimit / searchMaxLimit clamps on line 134-139 and the
// empty-topic-tag skip inside atomMatchesTopic.
func TestAtomIndexRepo_SearchForLearner_LimitClampsAndEmptyTag(t *testing.T) {
	ctx := context.Background()
	r := NewAtomIndexRepo()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// One atom with an effective EMPTY tag (whitespace) — the hint partition
	// must skip it inside atomMatchesTopic rather than substring-matching.
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID: "blank-tag", TenantID: "t1", CourseID: "c1", AtomType: "mcq",
		TopicTags:   []string{"   "},
		PublishedAt: base.Add(time.Hour),
	})
	_ = r.Save(ctx, a)
	// A second atom tagged "scrum" so the hint actually matches something.
	b, _ := atom_index.New(atom_index.NewParams{
		AtomID: "scrummy", TenantID: "t1", CourseID: "c1", AtomType: "mcq",
		TopicTags:   []string{"scrum"},
		PublishedAt: base,
	})
	_ = r.Save(ctx, b)

	// limit <= 0 → default (5).
	got, err := r.SearchForLearner(ctx, "t1", "scrum", 0)
	if err != nil {
		t.Fatalf("SearchForLearner limit0: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit<=0 len = %d, want 2 (scrum match first + blank-tag top-up)", len(got))
	}
	if got[0].AtomID != "scrummy" {
		t.Errorf("got[0] = %q, want scrummy (hint match bands on-theme first)", got[0].AtomID)
	}

	// limit > max → capped at max (10).
	got2, err := r.SearchForLearner(ctx, "t1", "scrum", 1000)
	if err != nil {
		t.Fatalf("SearchForLearner limit1000: %v", err)
	}
	if len(got2) != 2 {
		t.Errorf("capped limit len = %d, want 2", len(got2))
	}
}

// ---------- growth ----------

// TestGrowthRepo_AwardExpTx_NotFoundAndTenantIsolation covers the missing-row
// and cross-tenant failure branches of AwardExpTx.
func TestGrowthRepo_AwardExpTx_NotFoundAndTenantIsolation(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	in := growth.AwardExpTxInput{
		TenantID: "tenant-1", CompanionID: "absent", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k", Now: time.Now(),
	}
	if _, err := repo.AwardExpTx(ctx, in); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	})
	in.CompanionID = "fam-1"
	in.TenantID = "tenant-2" // cross-tenant
	if _, err := repo.AwardExpTx(ctx, in); err != growth.ErrCompanionNotFound {
		t.Errorf("cross-tenant err = %v, want ErrCompanionNotFound", err)
	}
}

// TestGrowthRepo_CommitReveal_FailureBranches covers the missing-row /
// cross-tenant and already-hatched guards.
func TestGrowthRepo_CommitReveal_FailureBranches(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	now := time.Now()

	// Absent companion.
	if _, err := repo.CommitReveal(ctx, growth.RevealTxInput{
		TenantID: "t1", CompanionID: "missing", OwnerGCID: "u1", Species: "dragon", Now: now,
	}); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	// Cross-tenant.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 0,
	})
	if _, err := repo.CommitReveal(ctx, growth.RevealTxInput{
		TenantID: "t2", CompanionID: "fam-1", OwnerGCID: "u1", Species: "dragon", Now: now,
	}); err != growth.ErrCompanionNotFound {
		t.Errorf("cross-tenant err = %v, want ErrCompanionNotFound", err)
	}

	// Already hatched / non-zero stage → ErrAlreadyHatched.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "hatched", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 1,
	})
	if _, err := repo.CommitReveal(ctx, growth.RevealTxInput{
		TenantID: "t1", CompanionID: "hatched", OwnerGCID: "u1", Species: "dragon", Now: now,
	}); err != growth.ErrAlreadyHatched {
		t.Errorf("hatched err = %v, want ErrAlreadyHatched", err)
	}
}

// TestGrowthRepo_CommitHatch_NotFound covers the missing/cross-tenant branch
// (ErrNotRevealed / ErrAlreadyHatched are already covered by growth_test).
func TestGrowthRepo_CommitHatch_NotFound(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	now := time.Now()
	if _, err := repo.CommitHatch(ctx, growth.HatchTxInput{
		TenantID: "t1", CompanionID: "missing", OwnerGCID: "u1", Now: now,
	}); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 0,
	})
	if _, err := repo.CommitHatch(ctx, growth.HatchTxInput{
		TenantID: "t2", CompanionID: "fam-1", OwnerGCID: "u1", Now: now,
	}); err != growth.ErrCompanionNotFound {
		t.Errorf("cross-tenant err = %v, want ErrCompanionNotFound", err)
	}
}

// TestGrowthRepo_CommitBornHatched covers the born-hatched transition
// (Happy path with a species pick, the empty-species branch, and both guards).
func TestGrowthRepo_CommitBornHatched(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	now := time.Now()

	if _, err := repo.CommitBornHatched(ctx, growth.BornHatchedTxInput{
		TenantID: "t1", CompanionID: "missing", Species: "dragon", Now: now,
	}); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	now2 := now.Add(-time.Hour)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "egg", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 0,
	})
	row, err := repo.CommitBornHatched(ctx, growth.BornHatchedTxInput{
		TenantID: "t1", CompanionID: "egg", OwnerGCID: "u1", Species: "phoenix", Now: now2,
	})
	if err != nil {
		t.Fatalf("CommitBornHatched: %v", err)
	}
	if row.GrowthStage != 1 || row.HatchedAt == nil || !row.HatchedAt.Equal(now2) {
		t.Errorf("stage/hatched = %d/%v, want 1 & %v", row.GrowthStage, row.HatchedAt, now2)
	}
	if row.Species != "phoenix" {
		t.Errorf("Species = %q, want phoenix (explicit pick persists)", row.Species)
	}

	// Empty species pick → stage still advances, species untouched.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "egg2", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 0,
	})
	row2, err := repo.CommitBornHatched(ctx, growth.BornHatchedTxInput{
		TenantID: "t1", CompanionID: "egg2", OwnerGCID: "u1", Species: "", Now: now,
	})
	if err != nil {
		t.Fatalf("CommitBornHatched(empty species): %v", err)
	}
	if row2.GrowthStage != 1 || row2.Species != "" {
		t.Errorf("empty-species stage/species = %d/%q, want 1/\"\"", row2.GrowthStage, row2.Species)
	}

	// Already hatched (or non-zero stage) → ErrAlreadyHatched.
	if _, err := repo.CommitBornHatched(ctx, growth.BornHatchedTxInput{
		TenantID: "t1", CompanionID: "egg", OwnerGCID: "u1", Species: "owl", Now: now,
	}); err != growth.ErrAlreadyHatched {
		t.Errorf("already-hatched err = %v, want ErrAlreadyHatched", err)
	}
}

// TestGrowthRepo_SetResonantConcept covers set, clear (nil), and not-found.
func TestGrowthRepo_SetResonantConcept(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	if _, err := repo.SetResonantConcept(ctx, "t1", "missing", nil); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 2,
	})
	cid := "concept-1"
	row, err := repo.SetResonantConcept(ctx, "t1", "fam-1", &cid)
	if err != nil {
		t.Fatalf("SetResonantConcept: %v", err)
	}
	if row.ResonantConceptID != "concept-1" {
		t.Errorf("ResonantConceptID = %q, want concept-1", row.ResonantConceptID)
	}
	// Clear with nil.
	row2, err := repo.SetResonantConcept(ctx, "t1", "fam-1", nil)
	if err != nil {
		t.Fatalf("SetResonantConcept(nil): %v", err)
	}
	if row2.ResonantConceptID != "" {
		t.Errorf("ResonantConceptID = %q, want empty after clear", row2.ResonantConceptID)
	}
	// Cross-tenant → not found.
	if _, err := repo.SetResonantConcept(ctx, "t2", "fam-1", &cid); err != growth.ErrCompanionNotFound {
		t.Errorf("cross-tenant err = %v, want ErrCompanionNotFound", err)
	}
}

// TestGrowthRepo_MarkAhaMoment_NotFound covers the missing/cross-tenant guard
// (the consumed-guard is already covered by growth_test).
func TestGrowthRepo_MarkAhaMoment_NotFound(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	in := growth.AhaMomentInput{
		TenantID: "t1", CompanionID: "missing", OwnerGCID: "u1",
		PreviewLLMTier: "pro", WindowExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := repo.MarkAhaMoment(ctx, in); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t1", OwnerGCID: "u1", GrowthStage: 3,
	})
	in.CompanionID = "fam-1"
	in.OwnerGCID = "u2" // wrong owner
	if _, err := repo.MarkAhaMoment(ctx, in); err != growth.ErrCompanionNotFound {
		t.Errorf("wrong-owner err = %v, want ErrCompanionNotFound", err)
	}
}

// TestGrowthRepo_ProvisionEgg_ZeroNow covers the zero-Now default in
// ProvisionEgg (the existing test passes a non-zero Now).
func TestGrowthRepo_ProvisionEgg_ZeroNow(t *testing.T) {
	repo := NewGrowthRepo()
	ctx := context.Background()
	row, err := repo.ProvisionEgg(ctx, growth.ProvisionEggInput{
		TenantID: "t1", OwnerGCID: "u1", EggSku: "egg.standard.v1",
		EggPurchaseID: "p-zero", EggSource: "purchase",
	})
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}
	if row.EggPurchasedAt == nil || row.EggPurchasedAt.IsZero() {
		t.Errorf("EggPurchasedAt = %v, want a non-zero default", row.EggPurchasedAt)
	}
}

// ---------- learning_path ----------

// TestLearningPathRepo_GetBySourceCollection drives the get-or-create probe
// (ADR-233): source_type=collection match, wrong-source skip, tenant/owner
// mismatch, and soft-deleted exclusion.
func TestLearningPathRepo_GetBySourceCollection(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	collection, _ := learning_path.NewFromCollection(learning_path.StudyListParams{
		TenantID: tenantID, OwnerGCID: gcid, CollectionID: "col-1", Title: "Fractions",
		AtomIDs: []string{atom1}, StudyListEventID: "evt-1", Now: time.Now(),
	})
	if err := r.Save(ctx, collection); err != nil {
		t.Fatalf("save collection path: %v", err)
	}

	got, err := r.GetBySourceCollection(ctx, tenantID, gcid, "col-1")
	if err != nil {
		t.Fatalf("GetBySourceCollection: %v", err)
	}
	if got.PathID != collection.PathID {
		t.Errorf("PathID = %q, want %q", got.PathID, collection.PathID)
	}

	// Unknown collection → ErrNotFound.
	if _, err := r.GetBySourceCollection(ctx, tenantID, gcid, "col-9"); err != ErrNotFound {
		t.Errorf("missing collection err = %v, want ErrNotFound", err)
	}
	// Wrong tenant.
	if _, err := r.GetBySourceCollection(ctx, lpTenant2, gcid, "col-1"); err != ErrNotFound {
		t.Errorf("wrong tenant err = %v, want ErrNotFound", err)
	}
	// Wrong owner.
	if _, err := r.GetBySourceCollection(ctx, tenantID, lpGCID2, "col-1"); err != ErrNotFound {
		t.Errorf("wrong owner err = %v, want ErrNotFound", err)
	}
	// A non-collection source (course path) must be skipped.
	coursePath := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom1})
	_ = r.Save(ctx, coursePath)
	if _, err := r.GetBySourceCollection(ctx, tenantID, gcid, lpCourse1); err != ErrNotFound {
		t.Errorf("course-source err = %v, want ErrNotFound (source_type filter)", err)
	}

	// Soft-deleted collection path → ErrNotFound.
	collection.SoftDelete()
	_ = r.Save(ctx, collection)
	if _, err := r.GetBySourceCollection(ctx, tenantID, gcid, "col-1"); err != ErrNotFound {
		t.Errorf("soft-deleted err = %v, want ErrNotFound", err)
	}
}

// TestLearningPathRepo_GetByStudyListEventID drives the durable delivery-dedupe
// anchor. Note the source: soft-deleted paths are INTENTIONALLY still matched.
func TestLearningPathRepo_GetByStudyListEventID(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	collection, _ := learning_path.NewFromCollection(learning_path.StudyListParams{
		TenantID: tenantID, OwnerGCID: gcid, CollectionID: "col-1", Title: "Fractions",
		AtomIDs: []string{atom1}, StudyListEventID: "evt-abc", Now: time.Now(),
	})
	if err := r.Save(ctx, collection); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := r.GetByStudyListEventID(ctx, "evt-abc")
	if err != nil {
		t.Fatalf("GetByStudyListEventID: %v", err)
	}
	if got.PathID != collection.PathID {
		t.Errorf("PathID = %q, want %q", got.PathID, collection.PathID)
	}

	// Empty anchor → ErrNotFound (no panic).
	if _, err := r.GetByStudyListEventID(ctx, ""); err != ErrNotFound {
		t.Errorf("empty anchor err = %v, want ErrNotFound", err)
	}
	// No match → ErrNotFound.
	if _, err := r.GetByStudyListEventID(ctx, "evt-nope"); err != ErrNotFound {
		t.Errorf("no-match err = %v, want ErrNotFound", err)
	}
	// Soft-deleted path is STILL matched (durable anchor survives a delete).
	collection.SoftDelete()
	_ = r.Save(ctx, collection)
	got2, err := r.GetByStudyListEventID(ctx, "evt-abc")
	if err != nil || got2.PathID != collection.PathID {
		t.Errorf("soft-deleted anchor = (%v,%v), want still matched", got2, err)
	}
}

// ---------- user_kg ----------

// TestExplorationRepo_ListByCluster_MultipleSorts covers the CreatedAt sort
// closure (≥2 rows) alongside the existing single-row test, plus the
// soft-deleted skip.
func TestExplorationRepo_ListByCluster_MultipleSorts(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	cluster, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	base := time.Now().UTC()
	e1, _ := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomA)
	e1.CreatedAt = base.Add(2 * time.Hour)
	e2, _ := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomB)
	e2.CreatedAt = base.Add(time.Hour)
	eDeleted, _ := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomC)
	softDelete(&eDeleted.DeletedAt)
	for _, e := range []*userknowledgegraph.Exploration{e1, e2, eDeleted} {
		if err := repo.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.ListByCluster(ctx, cluster.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (e2 before e1, deleted skipped)", len(got))
	}
	if got[0].ExplorationID != e2.ExplorationID || got[1].ExplorationID != e1.ExplorationID {
		t.Errorf("order = [%q,%q], want [e2,e1] by CreatedAt", got[0].ExplorationID, got[1].ExplorationID)
	}
}

// TestExplorationRepo_LoadTrail_SortByStepIndex covers the StepIndex sort
// closure with ≥2 hops (existing test appends a single hop).
func TestExplorationRepo_LoadTrail_SortByStepIndex(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	cluster, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	exp, _ := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomA)
	_ = repo.Save(ctx, exp)

	mkHop := func(from, to string, step int) *userknowledgegraph.TrailHop {
		h, err := userknowledgegraph.NewTrailHop(exp.ExplorationID, cluster.ClusterID, kgTenantA, kgUserA, from, to, userknowledgegraph.NeighborRelationExtends, step)
		if err != nil {
			t.Fatalf("NewTrailHop: %v", err)
		}
		return h
	}
	if err := repo.AppendTrailHop(ctx, exp, mkHop(kgAtomA, kgAtomB, 1)); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendTrailHop(ctx, exp, mkHop(kgAtomB, kgAtomC, 0)); err != nil {
		t.Fatal(err)
	}

	trail, err := repo.LoadTrail(ctx, exp.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trail) != 2 {
		t.Fatalf("len = %d, want 2", len(trail))
	}
	if trail[0].StepIndex != 0 || trail[1].StepIndex != 1 {
		t.Errorf("order = [%d,%d], want [0,1] by StepIndex", trail[0].StepIndex, trail[1].StepIndex)
	}
	if trail[0].ToFocalAtomID != kgAtomC || trail[1].ToFocalAtomID != kgAtomB {
		t.Errorf("hops out of order: to=[%q,%q]", trail[0].ToFocalAtomID, trail[1].ToFocalAtomID)
	}
}

// TestHexagonRepo_MarkInvalidatedByFocal_NoMatchContinue covers the
// `!matches → continue` branch (a hexagon with neither the focal nor any
// neighbor equal to the target atom is left untouched).
func TestHexagonRepo_MarkInvalidatedByFocal_NoMatchContinue(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	// Its focal is kgAtomA and its neighbors (mkSixNeighbors) never include the
	// target below, so it must NOT be invalidated.
	unrelated, _ := userknowledgegraph.NewHexagonNode(
		"c1", "e1", kgTenantA, kgUserA, kgAtomB, mkSixNeighbors(kgAtomB), "r", "m")
	_ = repo.Upsert(ctx, unrelated)

	// Target a focal atom that no hexagon holds (marking by it matches nothing).
	n, err := repo.MarkInvalidatedByFocal(ctx, "01970000-0000-7000-a000-0000000000ff", userknowledgegraph.FogInvalidationReasonAtomPublished)
	if err != nil {
		t.Fatalf("MarkInvalidatedByFocal: %v", err)
	}
	if n != 0 {
		t.Errorf("invalidated = %d, want 0 (unrelated hexagon untouched)", n)
	}
}

// TestHexagonRepo_MarkInvalidatedByUser_SoftDeletedSkip covers the
// DeletedAt skip in MarkInvalidatedByUser (foreign-user skip is covered by the
// existing feature test).
func TestHexagonRepo_MarkInvalidatedByUser_SoftDeletedSkip(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	live, _ := userknowledgegraph.NewHexagonNode(
		"c1", "e1", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	deleted, _ := userknowledgegraph.NewHexagonNode(
		"c2", "e2", kgTenantA, kgUserA, kgAtomB, mkSixNeighbors(kgAtomB), "r", "m")
	softDelete(&deleted.DeletedAt)
	_ = repo.Upsert(ctx, live)
	_ = repo.Upsert(ctx, deleted)

	n, err := repo.MarkInvalidatedByUser(ctx, kgTenantA, kgUserA, userknowledgegraph.FogInvalidationReasonUserRetentionShift)
	if err != nil {
		t.Fatalf("MarkInvalidatedByUser: %v", err)
	}
	if n != 1 {
		t.Errorf("invalidated = %d, want 1 (soft-deleted skipped)", n)
	}
}

// TestHexagonRepo_FindInvalidatedOlderThan_SkipsLiveAndDeleted covers the
// `DeletedAt != nil || InvalidatedAt == nil → continue` branch (the existing
// test only supplies invalidated live rows). It also re-exercises the plain
// RFC3339 cutoff form.
func TestHexagonRepo_FindInvalidatedOlderThan_SkipsLiveAndDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	now := time.Now().UTC()

	live, _ := userknowledgegraph.NewHexagonNode("c1", "e1", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	deleted, _ := userknowledgegraph.NewHexagonNode("c2", "e2", kgTenantA, kgUserA, kgAtomB, mkSixNeighbors(kgAtomB), "r", "m")
	softDelete(&deleted.DeletedAt)
	oldInvalidated, _ := userknowledgegraph.NewHexagonNode("c3", "e3", kgTenantA, kgUserA, kgAtomC, mkSixNeighbors(kgAtomC), "r", "m")
	_ = oldInvalidated.Invalidate(userknowledgegraph.FogInvalidationReasonAtomPublished)
	oldInvalidated.InvalidatedAt = &now // ensure it's older than the cutoff below? see Note
	_ = repo.Upsert(ctx, live)
	_ = repo.Upsert(ctx, deleted)
	_ = repo.Upsert(ctx, oldInvalidated)

	// A cutoff far in the PAST: the live row (InvalidatedAt==nil) and the
	// deleted row are skipped by the guard, and the invalidated row (stamped at
	// `now`) is NOT older than the past cutoff — so nothing is returned, and the
	// `DeletedAt != nil || InvalidatedAt == nil` continue is exercised. Plain
	// RFC3339 (no fractional second) also re-parses on this path.
	pastCutoff := now.Add(-24 * time.Hour).Format(time.RFC3339)
	got, err := repo.FindInvalidatedOlderThan(ctx, pastCutoff)
	if err != nil {
		t.Fatalf("FindInvalidatedOlderThan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0 (live + deleted skipped; invalidated is not older than a past cutoff)", len(got))
	}

	// A cutoff in the FUTURE returns only the already-invalidated row (stamped
	// `now`, which is older than now+24h).
	futureCutoff := now.Add(24 * time.Hour).Format(time.RFC3339)
	got2, err := repo.FindInvalidatedOlderThan(ctx, futureCutoff)
	if err != nil {
		t.Fatalf("FindInvalidatedOlderThan(future): %v", err)
	}
	if len(got2) != 1 || got2[0].HexNodeID != oldInvalidated.HexNodeID {
		t.Fatalf("got2 = %v, want only the invalidated row", got2)
	}
}

// TestAtomSemanticEdgesRepo_ListByTarget_SoftDeletedAndWrongTenant covers the
// DeletedAt and tenant-mismatch continues in ListByTarget.
func TestAtomSemanticEdgesRepo_ListByTarget_SoftDeletedAndWrongTenant(t *testing.T) {
	ctx := context.Background()
	repo := NewAtomSemanticEdgesRepo()
	now := time.Now().UTC()
	live, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantA, kgAtomA, kgAtomB, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	deleted, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantA, kgAtomC, kgAtomB, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	deleted.SoftDelete(now)
	otherTenant, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantB, kgAtomD, kgAtomB, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	for _, e := range []*userknowledgegraph.AtomSemanticEdge{live, deleted, otherTenant} {
		_ = repo.Save(ctx, e)
	}

	got, err := repo.ListByTarget(ctx, kgTenantA, kgAtomB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceAtomID != kgAtomA {
		t.Errorf("ListByTarget(A,B) = %d items, want only the A→B live edge", len(got))
	}
}

// TestJunctionRepo_ListPendingForUser_SortsMultiple covers the DetectedAt sort
// closure with ≥2 pending junctions for the same user.
func TestJunctionRepo_ListPendingForUser_SortsMultiple(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	base := time.Now().UTC()
	jLater := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000021",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ca", ClusterBID: "cb",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: base.Add(time.Hour), CreatedAt: base, UpdatedAt: base,
	}
	jEarlier := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000022",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "cc", ClusterBID: "cd",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: base.Add(-time.Hour), CreatedAt: base, UpdatedAt: base,
	}
	deleted := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000023",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ce", ClusterBID: "cf",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: base, CreatedAt: base, UpdatedAt: base,
	}
	softDelete(&deleted.DeletedAt)
	for _, j := range []*userknowledgegraph.Junction{jLater, jEarlier, deleted} {
		_ = repo.Save(ctx, j)
	}

	got, err := repo.ListPendingForUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (deleted skipped)", len(got))
	}
	if got[0].JunctionID != jEarlier.JunctionID || got[1].JunctionID != jLater.JunctionID {
		t.Errorf("order = [%q,%q], want [earlier,later] by DetectedAt", got[0].JunctionID, got[1].JunctionID)
	}
}
