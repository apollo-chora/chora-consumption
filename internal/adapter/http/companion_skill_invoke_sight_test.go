package http

// companion_skill_invoke_sight_test.go — CHO-2014: direct unit tests for the
// SIGHT-skill invoke-runner builders (buildWeaknessSightTurn first). The builder
// is exercised directly with a fake reader on a bare *Server, so the
// activation/equip/stage/mana gates in invokeCompanionSkill are bypassed (per the
// runner contract they run BEFORE the builder). Focus: the composed turn is
// learner-safe (concept labels + sanitized evidence, no UUIDs) and honest
// (empty ⇒ no fabricated weakness).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/kgmapread"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// fakeKGMapReader records the (centre, rings) it was called with + returns a
// canned ring map (satisfies the http Server's KGMapReader).
type fakeKGMapReader struct {
	nodes      []kgmapread.MapNode
	err        error
	lastCentre string
	lastRings  int
	calls      int
}

func (f *fakeKGMapReader) ReadRingMap(_ context.Context, _, _, centre string, rings int) ([]kgmapread.MapNode, error) {
	f.calls++
	f.lastCentre = centre
	f.lastRings = rings
	return f.nodes, f.err
}

func mapSightServer(state *growth.State, kg *fakeKGMapReader) *Server {
	return &Server{
		Growth: &fakeGrowth{getFn: func(_, _, _ string) (*growth.State, error) { return state, nil }},
		KGMap:  kg,
	}
}

func TestBuildMapSightTurn_Structural2RingsNarratesMap(t *testing.T) {
	kg := &fakeKGMapReader{nodes: []kgmapread.MapNode{
		{Title: "Long division", Ring: 0, IsCentre: true},
		{Title: "Remainders", Ring: 1},
		{Title: "Fractions", Ring: 2},
	}}
	s := mapSightServer(&growth.State{GrowthStage: 4, ResonantConceptID: "concept-centre"}, kg)

	turn, ierr := buildMapSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	if kg.lastRings != 2 || kg.lastCentre != "concept-centre" {
		t.Fatalf("kg read centre=%q rings=%d (want concept-centre / 2)", kg.lastCentre, kg.lastRings)
	}
	for _, want := range []string{"Long division", "Remainders", "Fractions", "kg.read_map"} {
		if !strings.Contains(turn.message, want) {
			t.Fatalf("prompt missing %q:\n%s", want, turn.message)
		}
	}
}

func TestBuildMapSightTurn_FocusOverridesCentre(t *testing.T) {
	kg := &fakeKGMapReader{nodes: []kgmapread.MapNode{{Title: "X", Ring: 0, IsCentre: true}}}
	s := mapSightServer(&growth.State{GrowthStage: 3, ResonantConceptID: "resonant"}, kg)

	_, ierr := buildMapSightTurn(s, context.Background(), "t", "g", "fam-1",
		invokeParams{"focus": "01957c8c-eeee-7000-eeee-eeeeeeeeeeee"})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	if kg.lastCentre != "01957c8c-eeee-7000-eeee-eeeeeeeeeeee" {
		t.Fatalf("focus did not override centre: %q", kg.lastCentre)
	}
	if kg.lastRings != 1 { // Awakened st3 → 1 ring
		t.Fatalf("rings = %d want 1", kg.lastRings)
	}
}

func TestBuildMapSightTurn_NoCentreIsHonestNoRead(t *testing.T) {
	kg := &fakeKGMapReader{}
	s := mapSightServer(&growth.State{GrowthStage: 4, ResonantConceptID: ""}, kg)

	turn, ierr := buildMapSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	if kg.calls != 0 {
		t.Fatal("no centre ⇒ must not read the map")
	}
	if !strings.Contains(strings.ToLower(turn.message), "centre") {
		t.Fatalf("no-centre turn should say so honestly:\n%s", turn.message)
	}
}

func TestBuildMapSightTurn_NotWired(t *testing.T) {
	_, ierr := buildMapSightTurn(&Server{}, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr == nil || ierr.status != http.StatusServiceUnavailable {
		t.Fatalf("unwired: want 503, got %+v", ierr)
	}
}

func TestBuildMapSightTurn_GrowthReadErrorIs500(t *testing.T) {
	s := &Server{
		Growth: &fakeGrowth{getFn: func(_, _, _ string) (*growth.State, error) { return nil, errors.New("boom") }},
		KGMap:  &fakeKGMapReader{},
	}
	_, ierr := buildMapSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr == nil || ierr.status != http.StatusInternalServerError {
		t.Fatalf("growth read error: want 500, got %+v", ierr)
	}
}

func TestBuildMapSightTurn_KGReadErrorIs500(t *testing.T) {
	kg := &fakeKGMapReader{err: errors.New("concept graph down")}
	s := mapSightServer(&growth.State{GrowthStage: 4, ResonantConceptID: "c"}, kg)
	_, ierr := buildMapSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr == nil || ierr.status != http.StatusInternalServerError {
		t.Fatalf("kg read error: want 500, got %+v", ierr)
	}
}

// fakeWeaknessRepo satisfies learner_weakness.Repository; only List + Get are
// exercised by the weakness_sight builder.
type fakeWeaknessRepo struct {
	list      lw.ListResult
	get       *lw.LearnerWeakness
	listErr   error
	getErr    error
	lastQuery lw.ListQuery
}

func (f *fakeWeaknessRepo) Upsert(context.Context, lw.UpsertInput) (lw.UpsertResult, error) {
	return lw.UpsertResult{}, nil
}
func (f *fakeWeaknessRepo) List(_ context.Context, q lw.ListQuery) (lw.ListResult, error) {
	f.lastQuery = q
	return f.list, f.listErr
}
func (f *fakeWeaknessRepo) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil
}
func (f *fakeWeaknessRepo) Get(_ context.Context, _, _ string) (*lw.LearnerWeakness, error) {
	return f.get, f.getErr
}
func (f *fakeWeaknessRepo) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (f *fakeWeaknessRepo) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (f *fakeWeaknessRepo) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (f *fakeWeaknessRepo) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

func TestBuildWeaknessSightTurn_AutoTopNarratesEvidence(t *testing.T) {
	repo := &fakeWeaknessRepo{list: lw.ListResult{Items: []lw.LearnerWeakness{
		{
			ID: "01957c8c-aaaa-7000-aaaa-aaaaaaaaaaaa", ConceptKey: "long-division",
			ConceptLabel: "Long division", Strength: 0.8,
			Descriptor: lw.Descriptor{
				Summary:         "drops the remainder step",
				Misconceptions:  []string{"forgets to bring down"},
				SuggestedAngles: []string{"try it with money"},
			},
		},
		{ConceptLabel: "Fractions", Strength: 0.5},
	}}}
	s := &Server{LearnerWeakness: repo}

	turn, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	// auto-top ⇒ shakiest-first, active-only page requested from the reader.
	if repo.lastQuery.Sort != lw.SortStrengthDesc || repo.lastQuery.IncludeGrown {
		t.Fatalf("query = %+v (want strength_desc, active-only)", repo.lastQuery)
	}
	if repo.lastQuery.LearnerGCID != "g" || repo.lastQuery.TenantID != "t" {
		t.Fatalf("query scope = %+v", repo.lastQuery)
	}
	msg := turn.message
	for _, want := range []string{
		"Long division", "0.80", "drops the remainder step",
		"forgets to bring down", "try it with money", "Fractions", "weakness.read",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("prompt missing %q:\n%s", want, msg)
		}
	}
	// learner-safe: the raw edge UUID must NEVER reach the prompt.
	if strings.Contains(msg, "01957c8c-aaaa-7000-aaaa-aaaaaaaaaaaa") {
		t.Fatalf("edge id leaked into prompt:\n%s", msg)
	}
}

func TestBuildWeaknessSightTurn_SpecificEdgeRef(t *testing.T) {
	repo := &fakeWeaknessRepo{get: &lw.LearnerWeakness{
		ConceptLabel: "Long division", Strength: 0.7,
		Descriptor: lw.Descriptor{Summary: "keeps misaligning columns"},
	}}
	s := &Server{LearnerWeakness: repo}

	turn, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1",
		invokeParams{"edge": "01957c8c-bbbb-7000-bbbb-bbbbbbbbbbbb"})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	if !strings.Contains(turn.message, "Long division") || !strings.Contains(turn.message, "keeps misaligning columns") {
		t.Fatalf("specific edge not narrated:\n%s", turn.message)
	}
}

func TestBuildWeaknessSightTurn_EmptyIsHonestNoFabrication(t *testing.T) {
	s := &Server{LearnerWeakness: &fakeWeaknessRepo{list: lw.ListResult{}}}
	turn, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	if !strings.Contains(strings.ToLower(turn.message), "nothing") {
		t.Fatalf("empty edges must narrate honestly (no fabrication):\n%s", turn.message)
	}
}

func TestBuildWeaknessSightTurn_NotWired(t *testing.T) {
	_, ierr := buildWeaknessSightTurn(&Server{}, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr == nil || ierr.status != http.StatusServiceUnavailable {
		t.Fatalf("unwired reader: want 503, got %+v", ierr)
	}
}

func TestBuildWeaknessSightTurn_EdgeNotFound(t *testing.T) {
	s := &Server{LearnerWeakness: &fakeWeaknessRepo{get: nil}} // live-miss
	_, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1",
		invokeParams{"edge": "01957c8c-cccc-7000-cccc-cccccccccccc"})
	if ierr == nil || ierr.status != http.StatusNotFound {
		t.Fatalf("missing edge: want 404, got %+v", ierr)
	}
}

func TestBuildWeaknessSightTurn_ReadErrorIs500(t *testing.T) {
	s := &Server{LearnerWeakness: &fakeWeaknessRepo{listErr: errors.New("db down")}}
	_, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr == nil || ierr.status != http.StatusInternalServerError {
		t.Fatalf("read error: want 500, got %+v", ierr)
	}
}

func TestBuildWeaknessSightTurn_SanitizesUUIDInEvidence(t *testing.T) {
	leak := "01957c8c-dddd-7000-dddd-dddddddddddd"
	repo := &fakeWeaknessRepo{list: lw.ListResult{Items: []lw.LearnerWeakness{{
		ConceptLabel: "Topic", Strength: 0.9,
		Descriptor: lw.Descriptor{
			Summary:        "see atom " + leak + " again",
			Misconceptions: []string{"confuses with " + leak},
		},
	}}}}
	s := &Server{LearnerWeakness: repo}
	turn, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	if strings.Contains(turn.message, leak) {
		t.Fatalf("UUID leaked through descriptor into prompt:\n%s", turn.message)
	}
}

// TestBuildWeaknessSightTurn_InstructionIsTightAndGrounded pins the CHO-2014
// re-gate prompt-tighten: the strict groundedness autorater penalised the old
// open-ended "explain WHY … then give next steps" instruction for elaborative
// coaching prose (groundedness 0.33). The tightened instruction must bound the
// elaboration (concise), ground every next step in the edge's SHOWN angles,
// keep the no-invention + no-shaming floor — mirroring map_sight's tight
// "strictly from … never invent" discipline that scored 1.0. Asserted on the
// [INSTRUCTION] segment only (the data section legitimately carries "angles").
func TestBuildWeaknessSightTurn_InstructionIsTightAndGrounded(t *testing.T) {
	repo := &fakeWeaknessRepo{list: lw.ListResult{Items: []lw.LearnerWeakness{{
		ConceptLabel: "Long division", Strength: 0.8,
		Descriptor: lw.Descriptor{
			Summary:         "drops the remainder step",
			Misconceptions:  []string{"forgets to bring down"},
			SuggestedAngles: []string{"try it with money"},
		},
	}}}}
	s := &Server{LearnerWeakness: repo}

	turn, ierr := buildWeaknessSightTurn(s, context.Background(), "t", "g", "fam-1", invokeParams{})
	if ierr != nil {
		t.Fatalf("builder err: %+v", ierr)
	}
	_, instr, ok := strings.Cut(turn.message, "[INSTRUCTION]")
	if !ok {
		t.Fatalf("no [INSTRUCTION] block:\n%s", turn.message)
	}
	li := strings.ToLower(instr)
	// concise = bounded elaboration; angle = next steps grounded in the shown
	// suggested_angles; shaming = curiosity-first persona floor; "not listed" =
	// the explicit anti-fabrication clause.
	for _, want := range []string{"concise", "angle", "shaming", "not listed"} {
		if !strings.Contains(li, want) {
			t.Fatalf("tightened instruction missing %q:\n%s", want, instr)
		}
	}
}
