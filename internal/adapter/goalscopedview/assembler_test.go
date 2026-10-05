// assembler_test.go — CHO-2118 sub-phase B. The composed tier-1 assembler.
//
// The load-bearing test here is the CONCEPT KEY-SPACE one. Goal.ConceptSet is
// only trimmed/deduped (goal.normaliseConcepts), NEVER slugified; a weakness's
// concept_key IS a slug. companionmind.AssembleGoalScopedView intersects them on
// exact trimmed strings, so an assembler that forwards Goal.ConceptSet raw
// intersects to NOTHING: no shaky concepts, no progress, HasSignal false — the
// learner is told "no memory yet" forever and no synthesis is ever requested.
// It fails silently and looks like an empty learner. Both sides are normalised
// through lw.NormalizeConceptKey, exactly as the graduation subscriber and the
// A+ %-ring already do.
package goalscopedview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const (
	tnt  = "11111111-1111-1111-1111-111111111111"
	gcid = "22222222-2222-2222-2222-222222222222"
	fam  = "33333333-3333-3333-3333-333333333333"
	gid  = "44444444-4444-4444-4444-444444444444"
)

// --- fakes ---

type fakeGoals struct {
	g   *goal.Goal
	err error
}

func (f fakeGoals) GetByID(_ context.Context, _, _, _ string) (*goal.Goal, error) {
	return f.g, f.err
}

type fakeWeaknesses struct {
	edges []lw.LearnerWeakness
	err   error
	calls int
	last  lw.ListQuery
}

func (f *fakeWeaknesses) ListAll(_ context.Context, q lw.ListQuery) ([]lw.LearnerWeakness, error) {
	f.calls++
	f.last = q
	return f.edges, f.err
}

type fakeMemories struct {
	mems  []companionmind.EpisodicMemory
	err   error
	calls int
}

func (f *fakeMemories) RecentMemories(_ context.Context, _ companionmind.Query) ([]companionmind.EpisodicMemory, error) {
	f.calls++
	return f.mems, f.err
}

type fakeCompanions struct {
	inst  *companion.Instance
	err   error
	calls int
}

func (f *fakeCompanions) Get(_ context.Context, _ string) (*companion.Instance, error) {
	f.calls++
	return f.inst, f.err
}

type fakeConcepts struct {
	node *conceptgraph.ConceptNode
	err  error
}

func (f fakeConcepts) GetByID(_ context.Context, _, _, _ string) (*conceptgraph.ConceptNode, error) {
	return f.node, f.err
}

func testGoal() *goal.Goal {
	return &goal.Goal{
		GoalID:      gid,
		TenantID:    tnt,
		LearnerGCID: gcid,
		// Raw, human-entered concepts — trimmed + deduped by the Goal aggregate,
		// but NOT slugified. This is the shape the DB actually holds.
		ConceptSet:    []string{"Place Value", "Comparing Fractions", "Decimals"},
		NorthStarNote: "get good at fractions",
	}
}

func ownedInstance() *companion.Instance {
	return &companion.Instance{CompanionID: fam, TenantID: tnt, OwnerGCID: gcid, Name: "Ember"}
}

// THE trap: raw goal concepts vs slugified weakness keys. If the assembler does
// not normalise both sides, every count here is 0 and the view reports no signal.
func TestAssembleForGoal_NormalisesBothSidesOfTheConceptKeySpace(t *testing.T) {
	weak := &fakeWeaknesses{edges: []lw.LearnerWeakness{
		{ConceptKey: "place-value", ConceptLabel: "place value", Strength: 0.8, Status: lw.StatusActive},
		{ConceptKey: "decimals", ConceptLabel: "decimals", Strength: 0.05, Status: lw.StatusGrown},
		{ConceptKey: "trigonometry", ConceptLabel: "trig", Strength: 0.9, Status: lw.StatusActive}, // outside the goal
	}}
	a := &Assembler{
		Goals:      fakeGoals{g: testGoal()},
		Weaknesses: weak,
		Memories:   &fakeMemories{},
		Companions: &fakeCompanions{inst: ownedInstance()},
	}

	v, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal())
	if err != nil {
		t.Fatalf("AssembleForGoal: %v", err)
	}
	if v.ConceptsTotal != 3 {
		t.Errorf("ConceptsTotal = %d; want 3 (the goal's 3 concepts, slugified)", v.ConceptsTotal)
	}
	// "Decimals" (goal, raw) ∩ "decimals" (grown edge, slug) — only matches once
	// both sides are normalised.
	if v.ConceptsMastered != 1 {
		t.Errorf("ConceptsMastered = %d; want 1 — the grown 'decimals' edge must match the goal's raw 'Decimals'", v.ConceptsMastered)
	}
	// "Place Value" (goal, raw) ∩ "place-value" (active edge, slug).
	if len(v.ShakyConcepts) != 1 {
		t.Fatalf("ShakyConcepts = %d; want 1 (place-value; trigonometry is outside the goal, decimals is grown)", len(v.ShakyConcepts))
	}
	if got := v.ShakyConcepts[0].ConceptKey; got != "place-value" {
		t.Errorf("shaky[0].ConceptKey = %q; want place-value", got)
	}
	if !v.HasSignal() {
		t.Error("HasSignal() = false; a shaky concept inside the goal IS signal")
	}
}

// ONE ListAll serves BOTH inputs: grown ⇒ mastered, active ⇒ shaky. A second
// source of mastery would be a second way to answer a question that already has
// exactly one answer in this codebase.
func TestAssembleForGoal_OneWeaknessListYieldsBothMasteredAndShaky(t *testing.T) {
	weak := &fakeWeaknesses{edges: []lw.LearnerWeakness{
		{ConceptKey: "place-value", Strength: 0.8, Status: lw.StatusActive},
		{ConceptKey: "decimals", Strength: 0.05, Status: lw.StatusGrown},
	}}
	a := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: weak, Memories: &fakeMemories{}, Companions: &fakeCompanions{inst: ownedInstance()}}

	if _, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal()); err != nil {
		t.Fatalf("AssembleForGoal: %v", err)
	}
	if weak.calls != 1 {
		t.Errorf("weakness ListAll calls = %d; want exactly 1 (one list gives both inputs)", weak.calls)
	}
	if !weak.last.IncludeGrown {
		t.Error("ListQuery.IncludeGrown = false; mastered edges are soft-archived to grown and MUST be opted in, or progress reads 0 forever")
	}
}

// No Companion bound ⇒ there are no per-Companion memories by definition. The
// memory repo must not be asked with a blank companion_id (its query is scoped by
// that id; what a blank one returns is not a question worth having an answer to).
func TestAssembleForGoal_NoCompanionBound_SkipsMemoryAndInstanceReads(t *testing.T) {
	mem := &fakeMemories{mems: []companionmind.EpisodicMemory{{ID: "m1"}}}
	inst := &fakeCompanions{inst: ownedInstance()}
	a := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{}, Memories: mem, Companions: inst}

	v, err := a.AssembleForGoal(context.Background(), tnt, gcid, "", testGoal())
	if err != nil {
		t.Fatalf("AssembleForGoal: %v", err)
	}
	if mem.calls != 0 {
		t.Errorf("RecentMemories calls = %d; want 0 with no Companion bound", mem.calls)
	}
	if inst.calls != 0 {
		t.Errorf("companion Get calls = %d; want 0 with no Companion bound", inst.calls)
	}
	if v.HasMemory || len(v.Memories) != 0 {
		t.Errorf("HasMemory=%v Memories=%d; want an honestly empty memory set", v.HasMemory, len(v.Memories))
	}
	if v.CompanionName != "" {
		t.Errorf("CompanionName = %q; want blank (no Companion is bound)", v.CompanionName)
	}
}

// Load-bearing reads FAIL LOUD. They are the ContentHash inputs: if a weakness
// read fails soft to empty on the READ path but succeeds on the completion
// drift-check path (or vice versa), the two hashes disagree and EVERY synthesis
// is refused forever — a bug that looks exactly like "the model never answers".
func TestAssembleForGoal_LoadBearingReadErrorsFailLoud(t *testing.T) {
	boom := errors.New("db down")

	t.Run("weaknesses", func(t *testing.T) {
		a := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{err: boom}, Memories: &fakeMemories{}, Companions: &fakeCompanions{inst: ownedInstance()}}
		if _, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal()); err == nil {
			t.Fatal("want an error when the weakness read fails; a fail-soft empty set would silently poison the content hash")
		}
	})
	t.Run("memories", func(t *testing.T) {
		a := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{}, Memories: &fakeMemories{err: boom}, Companions: &fakeCompanions{inst: ownedInstance()}}
		if _, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal()); err == nil {
			t.Fatal("want an error when the memory read fails")
		}
	})
}

// The Companion name + goal title are ENRICHMENT: excluded from ContentHash
// (verify: it hashes goal/companion/progress/shaky/memory-ids only), so degrading
// them cannot destabilise the hash. They fail soft rather than blanking the tab.
func TestAssembleForGoal_EnrichmentFailsSoftAndIsHashStable(t *testing.T) {
	base := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{edges: []lw.LearnerWeakness{
		{ConceptKey: "place-value", Strength: 0.8, Status: lw.StatusActive},
	}}, Memories: &fakeMemories{}, Companions: &fakeCompanions{inst: ownedInstance()}}
	good, err := base.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal())
	if err != nil {
		t.Fatalf("AssembleForGoal: %v", err)
	}

	degraded := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{edges: []lw.LearnerWeakness{
		{ConceptKey: "place-value", Strength: 0.8, Status: lw.StatusActive},
	}}, Memories: &fakeMemories{}, Companions: &fakeCompanions{err: errors.New("instance read failed")}}
	deg, err := degraded.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal())
	if err != nil {
		t.Fatalf("a failed name lookup must not fail the view: %v", err)
	}
	if deg.CompanionName != "" {
		t.Errorf("CompanionName = %q; want blank on a failed lookup — never fabricated", deg.CompanionName)
	}
	if good.ContentHash() != deg.ContentHash() {
		t.Error("ContentHash moved when only the (hash-excluded) Companion name degraded — enrichment must not destabilise the hash")
	}
}

// A Companion that is not the learner's own must never have its name rendered
// into the learner's reflection (cross-owner leak), and must not be fabricated.
func TestAssembleForGoal_ForeignCompanionNameIsNotLeaked(t *testing.T) {
	foreign := &companion.Instance{CompanionID: fam, TenantID: tnt, OwnerGCID: "99999999-9999-9999-9999-999999999999", Name: "SomeoneElsesCompanion"}
	a := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{}, Memories: &fakeMemories{}, Companions: &fakeCompanions{inst: foreign}}

	v, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal())
	if err != nil {
		t.Fatalf("AssembleForGoal: %v", err)
	}
	if v.CompanionName != "" {
		t.Errorf("CompanionName = %q; a Companion owned by another learner must never be named here", v.CompanionName)
	}
}

// Goal title: the root concept's title, else the north-star note, else BLANK.
// Never a fabricated placeholder (ADR-207 — unknown is a state).
func TestAssembleForGoal_GoalTitleResolution(t *testing.T) {
	root := "55555555-5555-5555-5555-555555555555"
	g := testGoal()
	g.RootConceptID = &root

	t.Run("root concept title wins", func(t *testing.T) {
		a := &Assembler{Goals: fakeGoals{g: g}, Weaknesses: &fakeWeaknesses{}, Memories: &fakeMemories{},
			Companions: &fakeCompanions{inst: ownedInstance()},
			Concepts:   fakeConcepts{node: &conceptgraph.ConceptNode{ConceptID: root, Title: "Fractions"}}}
		v, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, g)
		if err != nil {
			t.Fatalf("AssembleForGoal: %v", err)
		}
		if v.GoalTitle != "Fractions" {
			t.Errorf("GoalTitle = %q; want the root concept's title", v.GoalTitle)
		}
	})

	t.Run("falls back to the north-star note", func(t *testing.T) {
		a := &Assembler{Goals: fakeGoals{g: g}, Weaknesses: &fakeWeaknesses{}, Memories: &fakeMemories{},
			Companions: &fakeCompanions{inst: ownedInstance()},
			Concepts:   fakeConcepts{err: errors.New("concept read failed")}}
		v, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, g)
		if err != nil {
			t.Fatalf("a failed concept read must not fail the view: %v", err)
		}
		if v.GoalTitle != "get good at fractions" {
			t.Errorf("GoalTitle = %q; want the north-star note fallback", v.GoalTitle)
		}
	})

	t.Run("blank when nothing names it", func(t *testing.T) {
		bare := &goal.Goal{GoalID: gid, TenantID: tnt, LearnerGCID: gcid, ConceptSet: []string{"x"}}
		a := &Assembler{Goals: fakeGoals{g: bare}, Weaknesses: &fakeWeaknesses{}, Memories: &fakeMemories{}, Companions: &fakeCompanions{inst: ownedInstance()}}
		v, err := a.AssembleForGoal(context.Background(), tnt, gcid, fam, bare)
		if err != nil {
			t.Fatalf("AssembleForGoal: %v", err)
		}
		if v.GoalTitle != "" {
			t.Errorf("GoalTitle = %q; want blank — never a fabricated title", v.GoalTitle)
		}
	})
}

// The port the completion subscriber holds (subscribers.GoalScopedViewAssembler):
// it loads the goal itself. Both entry points MUST produce the identical view —
// that is the whole reason the assembly is composed once.
func TestAssembleGoalScopedView_MatchesTheGoalPreloadedPath(t *testing.T) {
	edges := []lw.LearnerWeakness{{ConceptKey: "place-value", Strength: 0.8, Status: lw.StatusActive}}
	mems := []companionmind.EpisodicMemory{{ID: "m1", Content: "we did place value", CreatedAt: time.Now().UTC()}}

	viaPort := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{edges: edges}, Memories: &fakeMemories{mems: mems}, Companions: &fakeCompanions{inst: ownedInstance()}}
	viaGoal := &Assembler{Goals: fakeGoals{g: testGoal()}, Weaknesses: &fakeWeaknesses{edges: edges}, Memories: &fakeMemories{mems: mems}, Companions: &fakeCompanions{inst: ownedInstance()}}

	a, err := viaPort.AssembleGoalScopedView(context.Background(), tnt, gcid, fam, gid)
	if err != nil {
		t.Fatalf("AssembleGoalScopedView: %v", err)
	}
	b, err := viaGoal.AssembleForGoal(context.Background(), tnt, gcid, fam, testGoal())
	if err != nil {
		t.Fatalf("AssembleForGoal: %v", err)
	}
	if a.ContentHash() != b.ContentHash() {
		t.Errorf("the two entry points disagree on ContentHash (%s vs %s) — every synthesis would be refused as drifted", a.ContentHash(), b.ContentHash())
	}
}

// A goal that does not exist cannot be assembled — the subscriber's port must
// not receive an empty view that hashes to "a real but empty learner".
func TestAssembleGoalScopedView_MissingGoalIsAnError(t *testing.T) {
	a := &Assembler{Goals: fakeGoals{g: nil}, Weaknesses: &fakeWeaknesses{}, Memories: &fakeMemories{}, Companions: &fakeCompanions{}}
	if _, err := a.AssembleGoalScopedView(context.Background(), tnt, gcid, fam, gid); err == nil {
		t.Fatal("want an error for an absent goal")
	}
}
