// concept_paint_lineage_test.go — WS-C6 (CHO-2085, ADR-227 addendum #7):
// the overlay/mastery paint keys on the STORED concept_key (post-0074), not a
// slug re-derived from the live title — a rename must not silently un-paint.
// And the paint resolves through concept_node_lineage ancestor keys so a
// weakness painted pre-merge/pre-split survives the re-keying (CHO-2085 AC
// "join survival"). RED-first.
package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// cplStubLineage fakes the ConceptLineageReader port.
type cplStubLineage struct {
	keys map[string][]string
	err  error
}

func (s *cplStubLineage) AncestorKeysByConcept(_ context.Context, _, _ string) (map[string][]string, error) {
	return s.keys, s.err
}

// cplRenamedConcept mints a concept titled mintTitle (fixing its ConceptKey)
// then renames it — the stored key must survive the rename (key-at-mint).
func cplRenamedConcept(t *testing.T, mintTitle, newTitle string) *conceptgraph.ConceptNode {
	t.Helper()
	c, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID:    fmTenantID,
		LearnerGCID: fmGCID,
		Title:       mintTitle,
		Now:         time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewConceptNode: %v", err)
	}
	if err := c.Rename(newTitle, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	return c
}

// TestConceptGraph_PaintSurvivesRename pins the addendum-#7 latent-bug fix:
// a weakness keyed "photosynthesis" still paints the node after the learner
// renames it to "Light Reactions" (stored concept_key, not live title).
func TestConceptGraph_PaintSurvivesRename(t *testing.T) {
	renamed := cplRenamedConcept(t, "Photosynthesis", "Light Reactions")
	lwStub := &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Photosynthesis", 0.8),
		kgOverlayEdge(t, "Cellular Respiration", 0.05), // grown → mastered
	}}
	masteredRenamed := cplRenamedConcept(t, "Cellular Respiration", "Aerobic Energy Release")
	s := &ExtServer{
		Concepts:        &fmStubConcepts{out: []*conceptgraph.ConceptNode{renamed, masteredRenamed}},
		ConceptEdges:    &reStubEdges{},
		LearnerWeakness: lwStub,
	}
	w := cgServe(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	byTitle := conceptDTOsByTitle(t, w.Body.Bytes())

	lr := byTitle["Light Reactions"]
	if lr == nil {
		t.Fatalf("renamed node missing from response: %v", byTitle)
	}
	if ge, _ := lr["growthEdge"].(map[string]any); ge == nil {
		t.Fatalf("rename un-painted the node (growthEdge nil) — paint must key on stored concept_key: %+v", lr)
	}
	aer := byTitle["Aerobic Energy Release"]
	if aer == nil {
		t.Fatalf("renamed mastered node missing: %v", byTitle)
	}
	if m, _ := aer["mastered"].(bool); !m {
		t.Fatalf("rename dropped the mastered flag — mastery must key on stored concept_key: %+v", aer)
	}
}

// TestConceptGraph_PaintResolvesLineageAncestors pins CHO-2085 AC "join
// survival": a weakness keyed by a PRE-SPLIT parent's concept_key paints the
// post-split child via the concept_node_lineage ancestor walk.
func TestConceptGraph_PaintResolvesLineageAncestors(t *testing.T) {
	child, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID:    fmTenantID,
		LearnerGCID: fmGCID,
		Title:       "Rational Numbers",
		Now:         time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewConceptNode: %v", err)
	}
	lwStub := &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Fractions", 0.7), // the split parent's key
	}}
	s := &ExtServer{
		Concepts:        &fmStubConcepts{out: []*conceptgraph.ConceptNode{child}},
		ConceptEdges:    &reStubEdges{},
		LearnerWeakness: lwStub,
		ConceptLineage:  &cplStubLineage{keys: map[string][]string{child.ConceptID: {"fractions"}}},
	}
	w := cgServe(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	byTitle := conceptDTOsByTitle(t, w.Body.Bytes())
	rn := byTitle["Rational Numbers"]
	if rn == nil {
		t.Fatalf("child node missing: %v", byTitle)
	}
	if ge, _ := rn["growthEdge"].(map[string]any); ge == nil {
		t.Fatalf("pre-split weakness did not paint the child via lineage — no orphaned joins allowed: %+v", rn)
	}
}

// TestBuildMapCard_ShakyKeysOnStoredKey pins the maps_handler side of the
// same fix: the Atlas card's shaky/mastered counts survive a rename.
func TestBuildMapCard_ShakyKeysOnStoredKey(t *testing.T) {
	renamed := cplRenamedConcept(t, "Photosynthesis", "Light Reactions")
	mastered := cplRenamedConcept(t, "Cellular Respiration", "Aerobic Energy Release")

	g, err := goal.NewGoal(goal.NewGoalInput{
		TenantID:    fmTenantID,
		LearnerGCID: fmGCID,
		Kind:        goal.KindCuriosity,
		Now:         time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	if err := g.AnchorToConcept(renamed.ConceptID, time.Date(2026, 7, 10, 8, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("AnchorToConcept: %v", err)
	}

	edge, err := conceptgraph.NewEdge(conceptgraph.NewEdgeInput{
		TenantID:        fmTenantID,
		LearnerGCID:     fmGCID,
		SourceConceptID: renamed.ConceptID,
		TargetConceptID: mastered.ConceptID,
		Class:           conceptgraph.EdgeClassHierarchy,
		Now:             time.Date(2026, 7, 10, 8, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}

	concepts := []conceptgraph.ConceptNode{*renamed, *mastered}
	edges := []conceptgraph.Edge{*edge}
	byID := map[string]*conceptgraph.ConceptNode{renamed.ConceptID: renamed, mastered.ConceptID: mastered}
	paint := &growthPaint{
		edges: lw.BuildOverlayIndex([]lw.LearnerWeakness{kgOverlayEdge(t, "Photosynthesis", 0.8)}),
		now:   time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
	}
	masteredKeys := map[string]bool{"cellular-respiration": true}

	card := buildMapCard(*g, concepts, edges, byID, paint, masteredKeys, nil, nil)
	if card.ShakyCount != 1 {
		t.Fatalf("ShakyCount = %d, want 1 (renamed node must stay shaky via stored key)", card.ShakyCount)
	}
	if card.MasteredCount != 1 {
		t.Fatalf("MasteredCount = %d, want 1 (renamed node must stay mastered via stored key)", card.MasteredCount)
	}
}

// TestBuildMapCard_LineageAncestorCounts pins the Atlas card resolving a
// pre-merge key through lineage: the survivor counts shaky when the ABSORBED
// node's key carries the weakness.
func TestBuildMapCard_LineageAncestorCounts(t *testing.T) {
	survivor, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID:    fmTenantID,
		LearnerGCID: fmGCID,
		Title:       "Rational Numbers",
		Now:         time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewConceptNode: %v", err)
	}
	g, err := goal.NewGoal(goal.NewGoalInput{
		TenantID:    fmTenantID,
		LearnerGCID: fmGCID,
		Kind:        goal.KindCuriosity,
		Now:         time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	if err := g.AnchorToConcept(survivor.ConceptID, time.Date(2026, 7, 10, 8, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("AnchorToConcept: %v", err)
	}
	paint := &growthPaint{
		edges: lw.BuildOverlayIndex([]lw.LearnerWeakness{kgOverlayEdge(t, "Fractions", 0.7)}),
		now:   time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
	}
	ancestors := map[string][]string{survivor.ConceptID: {"fractions"}}

	card := buildMapCard(*g, []conceptgraph.ConceptNode{*survivor}, nil,
		map[string]*conceptgraph.ConceptNode{survivor.ConceptID: survivor}, paint, nil, ancestors, nil)
	if card.ShakyCount != 1 {
		t.Fatalf("ShakyCount = %d, want 1 (absorbed node's weakness must paint the survivor via lineage)", card.ShakyCount)
	}
}
