package conceptgraph

import (
	"errors"
	"testing"
	"time"
)

// Re-rooting fixtures. Concept + edge IDs are readable markers (ReRoot matches
// them as opaque strings — no UUID-shape check on endpoints, mirroring NewEdge).
const (
	rrTenant  = "01970000-0000-7000-8000-0000000000aa"
	rrLearner = "01970000-0000-7000-9000-0000000000aa"
)

var rrNow = time.Date(2026, 7, 1, 15, 4, 5, 0, time.UTC)

func rrConcept(id string) ConceptNode {
	return ConceptNode{
		ConceptID:   id,
		TenantID:    rrTenant,
		LearnerGCID: rrLearner,
		Title:       id,
		Provenance:  ProvenanceLearnerAuthored,
		CreatedAt:   rrNow,
		UpdatedAt:   rrNow,
	}
}

// rrEdge builds a live edge parent(source)->child(target) of the given class +
// provenance (the WS-2 hierarchy convention: source = parent, target = child).
func rrEdge(source, target string, class EdgeClass, prov Provenance) Edge {
	return Edge{
		EdgeID:          "edge:" + source + "->" + target,
		TenantID:        rrTenant,
		LearnerGCID:     rrLearner,
		SourceConceptID: source,
		TargetConceptID: target,
		Class:           class,
		Provenance:      prov,
		CreatedAt:       rrNow,
		UpdatedAt:       rrNow,
	}
}

func rrHier(parent, child string) Edge {
	return rrEdge(parent, child, EdgeClassHierarchy, ProvenanceLearnerAuthored)
}

func findEdge(edges []Edge, source, target string) (Edge, bool) {
	for _, e := range edges {
		if e.SourceConceptID == source && e.TargetConceptID == target {
			return e, true
		}
	}
	return Edge{}, false
}

func TestReRoot_EmptyRootRejected(t *testing.T) {
	_, err := ReRoot([]ConceptNode{rrConcept("A")}, nil, "   ", rrNow)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v; want ErrInvalid", err)
	}
}

func TestReRoot_UnknownRootRejected(t *testing.T) {
	_, err := ReRoot([]ConceptNode{rrConcept("A")}, nil, "ZZZ", rrNow)
	if !errors.Is(err, ErrReRootUnknownRoot) {
		t.Fatalf("err = %v; want ErrReRootUnknownRoot", err)
	}
}

func TestReRoot_SoftDeletedRootRejected(t *testing.T) {
	c := rrConcept("A")
	del := rrNow
	c.DeletedAt = &del
	_, err := ReRoot([]ConceptNode{c}, nil, "A", rrNow)
	if !errors.Is(err, ErrConceptDeleted) {
		t.Fatalf("err = %v; want ErrConceptDeleted", err)
	}
}

func TestReRoot_AlreadyApexIsNoOp(t *testing.T) {
	// A is apex (A->B->C). Re-rooting to A changes nothing.
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B"), rrConcept("C")}
	edges := []Edge{rrHier("A", "B"), rrHier("B", "C")}
	plan, err := ReRoot(concepts, edges, "A", rrNow)
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	if !plan.IsNoOp() {
		t.Fatalf("apex re-root should be a no-op; got %+v", plan)
	}
	if plan.NewRootID != "A" {
		t.Errorf("NewRootID = %q; want A", plan.NewRootID)
	}
}

func TestReRoot_IsolatedConceptIsNoOp(t *testing.T) {
	concepts := []ConceptNode{rrConcept("solo")}
	plan, err := ReRoot(concepts, nil, "solo", rrNow)
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	if !plan.IsNoOp() {
		t.Fatalf("isolated concept re-root should be a no-op; got %+v", plan)
	}
}

func TestReRoot_LinearChainReversed(t *testing.T) {
	// A->B->C->D (A apex). Re-root at D: reverse every edge on the path.
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B"), rrConcept("C"), rrConcept("D")}
	edges := []Edge{rrHier("A", "B"), rrHier("B", "C"), rrHier("C", "D")}

	plan, err := ReRoot(concepts, edges, "D", rrNow)
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	if plan.NewRootID != "D" {
		t.Errorf("NewRootID = %q; want D", plan.NewRootID)
	}
	if len(plan.ToSoftDelete) != 3 || len(plan.ToCreate) != 3 {
		t.Fatalf("want 3 soft-deletes + 3 creates; got %d/%d", len(plan.ToSoftDelete), len(plan.ToCreate))
	}
	// Every original hierarchy edge is soft-deleted (append-only supersede).
	for _, se := range [][2]string{{"A", "B"}, {"B", "C"}, {"C", "D"}} {
		e, ok := findEdge(plan.ToSoftDelete, se[0], se[1])
		if !ok {
			t.Fatalf("soft-delete missing %s->%s", se[0], se[1])
		}
		if e.DeletedAt == nil || !e.DeletedAt.Equal(rrNow) {
			t.Errorf("%s->%s soft-delete stamp = %v; want %v", se[0], se[1], e.DeletedAt, rrNow)
		}
		if e.Class != EdgeClassHierarchy {
			t.Errorf("%s->%s class = %q; want hierarchy", se[0], se[1], e.Class)
		}
	}
	// The flipped edges make D the apex: D->C->B->A.
	softIDs := map[string]bool{}
	for _, e := range plan.ToSoftDelete {
		softIDs[e.EdgeID] = true
	}
	for _, ce := range [][2]string{{"D", "C"}, {"C", "B"}, {"B", "A"}} {
		e, ok := findEdge(plan.ToCreate, ce[0], ce[1])
		if !ok {
			t.Fatalf("create missing %s->%s", ce[0], ce[1])
		}
		if e.Class != EdgeClassHierarchy {
			t.Errorf("%s->%s class = %q; want hierarchy", ce[0], ce[1], e.Class)
		}
		if e.DeletedAt != nil {
			t.Errorf("%s->%s created edge should be live; got deleted_at %v", ce[0], ce[1], e.DeletedAt)
		}
		if !e.CreatedAt.Equal(rrNow) || !e.UpdatedAt.Equal(rrNow) {
			t.Errorf("%s->%s created stamps = %v/%v; want %v", ce[0], ce[1], e.CreatedAt, e.UpdatedAt, rrNow)
		}
		// Append-only: a flipped edge is a NEW row, not a reused id.
		if len(e.EdgeID) != 36 {
			t.Errorf("%s->%s edge id = %q; want a fresh UUIDv7", ce[0], ce[1], e.EdgeID)
		}
		if softIDs[e.EdgeID] {
			t.Errorf("%s->%s reuses a soft-deleted edge id %q", ce[0], ce[1], e.EdgeID)
		}
	}
}

func TestReRoot_BranchesAndLateralPreserved(t *testing.T) {
	// A apex; A->B, A->E, B->C, B->D; lateral C~E. Re-root at C.
	concepts := []ConceptNode{
		rrConcept("A"), rrConcept("B"), rrConcept("C"), rrConcept("D"), rrConcept("E"),
	}
	lateral := rrEdge("C", "E", EdgeClassLateral, ProvenanceLearnerAuthored)
	edges := []Edge{
		rrHier("A", "B"), rrHier("A", "E"), rrHier("B", "C"), rrHier("B", "D"), lateral,
	}

	plan, err := ReRoot(concepts, edges, "C", rrNow)
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	// Only the path C..A is reversed: B->C and A->B.
	if len(plan.ToSoftDelete) != 2 || len(plan.ToCreate) != 2 {
		t.Fatalf("want 2 soft-deletes + 2 creates; got %d/%d", len(plan.ToSoftDelete), len(plan.ToCreate))
	}
	if _, ok := findEdge(plan.ToSoftDelete, "B", "C"); !ok {
		t.Error("expected B->C soft-deleted")
	}
	if _, ok := findEdge(plan.ToSoftDelete, "A", "B"); !ok {
		t.Error("expected A->B soft-deleted")
	}
	// Sibling branches + the lateral edge are never touched.
	for _, untouched := range [][2]string{{"A", "E"}, {"B", "D"}, {"C", "E"}} {
		if _, ok := findEdge(plan.ToSoftDelete, untouched[0], untouched[1]); ok {
			t.Errorf("%s->%s must NOT be soft-deleted (sibling/lateral)", untouched[0], untouched[1])
		}
	}
	// Re-rooting never emits a lateral edge.
	for _, e := range append(append([]Edge{}, plan.ToSoftDelete...), plan.ToCreate...) {
		if e.Class == EdgeClassLateral {
			t.Errorf("plan must not contain a lateral edge: %+v", e)
		}
	}
}

func TestReRoot_SoftDeletedHierarchyEdgeIgnored(t *testing.T) {
	// A->B live; B->C tombstoned. Re-root C: its only parent edge is dead, so C
	// is the apex of the LIVE hierarchy -> no-op (guard soft-deleted edges).
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B"), rrConcept("C")}
	dead := rrHier("B", "C")
	del := rrNow
	dead.DeletedAt = &del
	edges := []Edge{rrHier("A", "B"), dead}

	plan, err := ReRoot(concepts, edges, "C", rrNow)
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	if !plan.IsNoOp() {
		t.Fatalf("soft-deleted parent edge must be ignored; got %+v", plan)
	}
}

func TestReRoot_CycleDetected(t *testing.T) {
	// A->B->C->A (corrupt hierarchy cycle). Walking up from B loops.
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B"), rrConcept("C")}
	edges := []Edge{rrHier("A", "B"), rrHier("B", "C"), rrHier("C", "A")}
	_, err := ReRoot(concepts, edges, "B", rrNow)
	if !errors.Is(err, ErrReRootCycle) {
		t.Fatalf("err = %v; want ErrReRootCycle", err)
	}
}

func TestReRoot_MultipleParentsRejected(t *testing.T) {
	// X has two live hierarchy parents -> re-root is ambiguous (fail loud).
	concepts := []ConceptNode{rrConcept("P1"), rrConcept("P2"), rrConcept("X")}
	edges := []Edge{rrHier("P1", "X"), rrHier("P2", "X")}
	_, err := ReRoot(concepts, edges, "X", rrNow)
	if !errors.Is(err, ErrReRootMultipleParents) {
		t.Fatalf("err = %v; want ErrReRootMultipleParents", err)
	}
}

func TestReRoot_ProvenancePreservedOnFlip(t *testing.T) {
	// A Companion-suggested hierarchy edge keeps its provenance when flipped —
	// re-rooting re-expresses an existing relation, it does not re-author it.
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B")}
	edges := []Edge{rrEdge("A", "B", EdgeClassHierarchy, ProvenanceCompanionSuggestedAccepted)}
	plan, err := ReRoot(concepts, edges, "B", rrNow)
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	e, ok := findEdge(plan.ToCreate, "B", "A")
	if !ok {
		t.Fatal("expected flipped B->A")
	}
	if e.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("flipped provenance = %q; want companion_suggested_accepted", e.Provenance)
	}
}

func TestReRoot_FlipInheritsInvalidProvenanceFailsLoud(t *testing.T) {
	// A corrupt provenance on the stored edge must surface, never be swallowed.
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B")}
	edges := []Edge{rrEdge("A", "B", EdgeClassHierarchy, Provenance("bogus"))}
	_, err := ReRoot(concepts, edges, "B", rrNow)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v; want ErrInvalid (flip rejected)", err)
	}
}

func TestReRootPlan_IsNoOp(t *testing.T) {
	if !(ReRootPlan{}).IsNoOp() {
		t.Error("empty plan should be a no-op")
	}
	if (ReRootPlan{ToCreate: []Edge{{}}}).IsNoOp() {
		t.Error("a plan with creates is not a no-op")
	}
	if (ReRootPlan{ToSoftDelete: []Edge{{}}}).IsNoOp() {
		t.Error("a plan with soft-deletes is not a no-op")
	}
}

func TestReRoot_ZeroNowFallsBackToWallClock(t *testing.T) {
	concepts := []ConceptNode{rrConcept("A"), rrConcept("B")}
	edges := []Edge{rrHier("A", "B")}
	before := time.Now().UTC()
	plan, err := ReRoot(concepts, edges, "B", time.Time{})
	if err != nil {
		t.Fatalf("ReRoot: %v", err)
	}
	e, ok := findEdge(plan.ToCreate, "B", "A")
	if !ok {
		t.Fatal("expected flipped B->A")
	}
	if e.CreatedAt.Before(before) {
		t.Errorf("zero now should fall back to wall-clock; got %v (before %v)", e.CreatedAt, before)
	}
}
