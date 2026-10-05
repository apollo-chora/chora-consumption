package conceptgraph

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// merge_split_test.go — WS-C6 (CHO-2085, ADR-227 D14). Fixtures reuse the
// rerooting_test.go helpers (rrConcept / rrEdge / rrHier / findEdge / rrTenant /
// rrLearner / rrNow — same package). Concept + edge IDs are readable markers
// (the planners match them as opaque strings, mirroring NewEdge).

// msConcept is rrConcept plus an explicit ConceptKey (the slug axis lineage
// records carry). msAtoms are UUID-shaped so NewConceptNode accepts them.
func msConcept(id, key string) ConceptNode {
	c := rrConcept(id)
	c.ConceptKey = key
	return c
}

func msLateral(source, target string) Edge {
	return rrEdge(source, target, EdgeClassLateral, ProvenanceLearnerAuthored)
}

// hasEdge reports whether a directed src->tgt edge is present (any class).
func hasEdge(edges []Edge, source, target string) bool {
	_, ok := findEdge(edges, source, target)
	return ok
}

// createdShape projects created edges to a stable, mint-id-independent signature
// (source->target:class:provenance). Merge mints no concepts, so its created
// endpoints are pre-existing ids — directly comparable across runs.
func createdShape(edges []Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.SourceConceptID+"->"+e.TargetConceptID+":"+string(e.Class)+":"+string(e.Provenance))
	}
	return out
}

// childLabel maps a freshly-minted child id to c#<index>, else returns the
// literal id — so split plans can be compared across runs despite fresh UUIDs.
func childLabel(children []ConceptNode, id string) string {
	for i := range children {
		if children[i].ConceptID == id {
			return "c#" + strconv.Itoa(i)
		}
	}
	return id
}

func splitCreatedShape(p SplitPlan) []string {
	out := make([]string, 0, len(p.EdgesToCreate))
	for _, e := range p.EdgesToCreate {
		out = append(out, childLabel(p.Children, e.SourceConceptID)+"->"+childLabel(p.Children, e.TargetConceptID)+":"+string(e.Class)+":"+string(e.Provenance))
	}
	return out
}

// ---- PlanMerge ---------------------------------------------------------------

func TestPlanMerge_HappyPath_ChildrenReparent(t *testing.T) {
	// R apex; R->absorbed, R->survivor, absorbed->c1, absorbed->c2. survivor is
	// NOT under absorbed => no pathChild: children re-parent to survivor and
	// absorbed's parent edge is simply dropped.
	concepts := []ConceptNode{
		msConcept("R", "r"), msConcept("absorbed", "absorbed-key"),
		msConcept("survivor", "survivor-key"), rrConcept("c1"), rrConcept("c2"),
	}
	edges := []Edge{
		rrHier("R", "absorbed"), rrHier("R", "survivor"),
		rrHier("absorbed", "c1"), rrHier("absorbed", "c2"),
	}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", map[string]bool{"R": true}, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if plan.SurvivorID != "survivor" {
		t.Errorf("SurvivorID = %q; want survivor", plan.SurvivorID)
	}
	// Soft-deletes: R->absorbed, absorbed->c1, absorbed->c2 (R->survivor untouched).
	if len(plan.EdgesToSoftDelete) != 3 {
		t.Fatalf("want 3 soft-deletes; got %d (%+v)", len(plan.EdgesToSoftDelete), plan.EdgesToSoftDelete)
	}
	for _, sd := range [][2]string{{"R", "absorbed"}, {"absorbed", "c1"}, {"absorbed", "c2"}} {
		e, ok := findEdge(plan.EdgesToSoftDelete, sd[0], sd[1])
		if !ok {
			t.Fatalf("missing soft-delete %s->%s", sd[0], sd[1])
		}
		if e.DeletedAt == nil || !e.DeletedAt.Equal(rrNow) {
			t.Errorf("%s->%s soft-delete stamp = %v; want %v", sd[0], sd[1], e.DeletedAt, rrNow)
		}
	}
	if hasEdge(plan.EdgesToSoftDelete, "R", "survivor") {
		t.Error("R->survivor must NOT be touched")
	}
	// Creates: survivor->c1, survivor->c2 (no parent create — no pathChild).
	if len(plan.EdgesToCreate) != 2 {
		t.Fatalf("want 2 creates; got %d (%+v)", len(plan.EdgesToCreate), plan.EdgesToCreate)
	}
	for _, ce := range [][2]string{{"survivor", "c1"}, {"survivor", "c2"}} {
		e, ok := findEdge(plan.EdgesToCreate, ce[0], ce[1])
		if !ok {
			t.Fatalf("missing create %s->%s", ce[0], ce[1])
		}
		if e.Class != EdgeClassHierarchy {
			t.Errorf("%s->%s class = %q; want hierarchy", ce[0], ce[1], e.Class)
		}
		if e.DeletedAt != nil {
			t.Errorf("%s->%s created edge should be live", ce[0], ce[1])
		}
		if len(e.EdgeID) != 36 {
			t.Errorf("%s->%s edge id = %q; want fresh UUIDv7", ce[0], ce[1], e.EdgeID)
		}
	}
	// Absorbed tombstoned; lineage on both axes.
	if plan.Absorbed.DeletedAt == nil || !plan.Absorbed.DeletedAt.Equal(rrNow) {
		t.Errorf("absorbed tombstone stamp = %v; want %v", plan.Absorbed.DeletedAt, rrNow)
	}
	want := LineageRecord{
		Operation: LineageOperationMerge, FromConceptID: "absorbed", ToConceptID: "survivor",
		FromConceptKey: "absorbed-key", ToConceptKey: "survivor-key",
	}
	if plan.Lineage != want {
		t.Errorf("lineage = %+v; want %+v", plan.Lineage, want)
	}
	// Purity: inputs unmutated.
	if concepts[1].DeletedAt != nil {
		t.Error("input absorbed concept was mutated")
	}
	for i := range edges {
		if edges[i].DeletedAt != nil {
			t.Errorf("input edge %d was mutated", i)
		}
	}
}

func TestPlanMerge_IntoOwnParent(t *testing.T) {
	// G->absorbed->survivor->leaf. Merge absorbed into survivor: pathChild ==
	// survivor, so survivor takes absorbed's slot under G. NO self-loop.
	concepts := []ConceptNode{rrConcept("G"), rrConcept("absorbed"), rrConcept("survivor"), rrConcept("leaf")}
	edges := []Edge{rrHier("G", "absorbed"), rrHier("absorbed", "survivor"), rrHier("survivor", "leaf")}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !hasEdge(plan.EdgesToSoftDelete, "G", "absorbed") || !hasEdge(plan.EdgesToSoftDelete, "absorbed", "survivor") {
		t.Fatalf("want G->absorbed + absorbed->survivor soft-deleted; got %+v", plan.EdgesToSoftDelete)
	}
	if !hasEdge(plan.EdgesToCreate, "G", "survivor") {
		t.Errorf("want G->survivor created; got %+v", plan.EdgesToCreate)
	}
	for _, e := range plan.EdgesToCreate {
		if e.SourceConceptID == e.TargetConceptID {
			t.Errorf("no self-loop may be created; got %s->%s", e.SourceConceptID, e.TargetConceptID)
		}
	}
	if hasEdge(plan.EdgesToSoftDelete, "survivor", "leaf") {
		t.Error("survivor->leaf must be untouched")
	}
}

func TestPlanMerge_GrandparentIntoGrandchild(t *testing.T) {
	// P->absorbed->M->survivor. Merge grandparent(absorbed) into grandchild
	// (survivor): pathChild == M, so M splices into absorbed's slot under P; the
	// chain stays acyclic (no survivor->M created).
	concepts := []ConceptNode{rrConcept("P"), rrConcept("absorbed"), rrConcept("M"), rrConcept("survivor")}
	edges := []Edge{rrHier("P", "absorbed"), rrHier("absorbed", "M"), rrHier("M", "survivor")}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !hasEdge(plan.EdgesToCreate, "P", "M") {
		t.Errorf("intermediate M must re-parent to absorbed's parent P; got %+v", plan.EdgesToCreate)
	}
	if hasEdge(plan.EdgesToCreate, "survivor", "M") {
		t.Error("must NOT create survivor->M (would cycle with M->survivor)")
	}
	if hasEdge(plan.EdgesToSoftDelete, "M", "survivor") {
		t.Error("M->survivor must be untouched")
	}
	for _, e := range plan.EdgesToCreate {
		if e.SourceConceptID == e.TargetConceptID {
			t.Errorf("no self-loop; got %s->%s", e.SourceConceptID, e.TargetConceptID)
		}
	}
}

func TestPlanMerge_AbsorbedApexNoPathChild(t *testing.T) {
	// absorbed apex (no parent); survivor separate. Children re-parent, no parent
	// edge to drop.
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("c1"), rrConcept("c2"), rrConcept("survivor")}
	edges := []Edge{rrHier("absorbed", "c1"), rrHier("absorbed", "c2")}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if len(plan.EdgesToSoftDelete) != 2 || len(plan.EdgesToCreate) != 2 {
		t.Fatalf("want 2 soft-deletes + 2 creates; got %d/%d", len(plan.EdgesToSoftDelete), len(plan.EdgesToCreate))
	}
	if !hasEdge(plan.EdgesToCreate, "survivor", "c1") || !hasEdge(plan.EdgesToCreate, "survivor", "c2") {
		t.Errorf("children must re-parent to survivor; got %+v", plan.EdgesToCreate)
	}
}

func TestPlanMerge_AbsorbedApexWithPathChild(t *testing.T) {
	// absorbed apex; absorbed->survivor->leaf, absorbed->other. pathChild ==
	// survivor and absorbed has no parent => survivor becomes apex (no P->child
	// create), other re-parents to survivor.
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor"), rrConcept("leaf"), rrConcept("other")}
	edges := []Edge{rrHier("absorbed", "survivor"), rrHier("survivor", "leaf"), rrHier("absorbed", "other")}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !hasEdge(plan.EdgesToSoftDelete, "absorbed", "survivor") || !hasEdge(plan.EdgesToSoftDelete, "absorbed", "other") {
		t.Fatalf("want absorbed->survivor + absorbed->other soft-deleted; got %+v", plan.EdgesToSoftDelete)
	}
	// survivor gets no new parent (it becomes apex); only other re-parents to it.
	if len(plan.EdgesToCreate) != 1 || !hasEdge(plan.EdgesToCreate, "survivor", "other") {
		t.Fatalf("want exactly survivor->other created; got %+v", plan.EdgesToCreate)
	}
}

func TestPlanMerge_LateralRepointSelfLoopDedupe(t *testing.T) {
	// absorbed~survivor (self-loop -> drop); absorbed->X (forward -> survivor->X);
	// W->absorbed (reversed: absorbed is TARGET -> W->survivor, direction kept);
	// survivor~Y pre-exists + absorbed~Y (-> dedupe drop); survivor~Z (control);
	// a soft-deleted absorbed lateral must be ignored (only LIVE edges participate).
	concepts := []ConceptNode{
		rrConcept("absorbed"), rrConcept("survivor"), rrConcept("X"),
		rrConcept("W"), rrConcept("Y"), rrConcept("Z"), rrConcept("Q"),
	}
	deadLateral := msLateral("absorbed", "Q")
	del := rrNow
	deadLateral.DeletedAt = &del
	edges := []Edge{
		msLateral("absorbed", "survivor"),
		msLateral("absorbed", "X"),
		msLateral("W", "absorbed"),
		msLateral("survivor", "Y"),
		msLateral("absorbed", "Y"),
		msLateral("survivor", "Z"),
		deadLateral,
	}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	// Soft-deletes: the four LIVE absorbed-touching laterals (dead one ignored).
	if len(plan.EdgesToSoftDelete) != 4 {
		t.Fatalf("want 4 soft-deletes; got %d (%+v)", len(plan.EdgesToSoftDelete), plan.EdgesToSoftDelete)
	}
	for _, sd := range [][2]string{{"absorbed", "survivor"}, {"absorbed", "X"}, {"W", "absorbed"}, {"absorbed", "Y"}} {
		if !hasEdge(plan.EdgesToSoftDelete, sd[0], sd[1]) {
			t.Errorf("missing soft-delete %s~%s", sd[0], sd[1])
		}
	}
	// Creates: survivor->X (forward kept) + W->survivor (reversed kept); self-loop
	// + dedupe + dead edge dropped.
	if len(plan.EdgesToCreate) != 2 {
		t.Fatalf("want 2 creates; got %d (%+v)", len(plan.EdgesToCreate), plan.EdgesToCreate)
	}
	if !hasEdge(plan.EdgesToCreate, "survivor", "X") {
		t.Error("want survivor->X (forward direction preserved)")
	}
	if !hasEdge(plan.EdgesToCreate, "W", "survivor") {
		t.Error("want W->survivor (reversed direction preserved)")
	}
	for _, e := range plan.EdgesToCreate {
		if e.Class != EdgeClassLateral {
			t.Errorf("re-pointed edge class = %q; want lateral", e.Class)
		}
	}
	if hasEdge(plan.EdgesToSoftDelete, "survivor", "Y") || hasEdge(plan.EdgesToSoftDelete, "survivor", "Z") {
		t.Error("control laterals not touching absorbed must be untouched")
	}
}

func TestPlanMerge_DuplicateHierChildSkipped(t *testing.T) {
	// survivor->Y already live; absorbed->Y. Re-parenting must NOT create a
	// second survivor->Y.
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor"), rrConcept("Y")}
	edges := []Edge{rrHier("survivor", "Y"), rrHier("absorbed", "Y")}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !hasEdge(plan.EdgesToSoftDelete, "absorbed", "Y") {
		t.Error("absorbed->Y should be soft-deleted")
	}
	if len(plan.EdgesToCreate) != 0 {
		t.Errorf("duplicate survivor->Y must be skipped; got creates %+v", plan.EdgesToCreate)
	}
}

func TestPlanMerge_ProvenancePreservedOnReparent(t *testing.T) {
	// A companion-suggested absorbed->c edge keeps its provenance when re-expressed
	// as survivor->c (re-expressing, not re-authoring).
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor"), rrConcept("c")}
	edges := []Edge{rrEdge("absorbed", "c", EdgeClassHierarchy, ProvenanceCompanionSuggestedAccepted)}

	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	e, ok := findEdge(plan.EdgesToCreate, "survivor", "c")
	if !ok {
		t.Fatal("expected survivor->c")
	}
	if e.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("provenance = %q; want companion_suggested_accepted", e.Provenance)
	}
}

func TestPlanMerge_RootImmutableAbsorbedRejected(t *testing.T) {
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor")}
	_, err := PlanMerge(concepts, nil, "survivor", "absorbed", map[string]bool{"absorbed": true}, rrNow)
	if !errors.Is(err, ErrRootImmutable) {
		t.Fatalf("err = %v; want ErrRootImmutable", err)
	}
}

func TestPlanMerge_SurvivorGoalRootAllowed(t *testing.T) {
	// Merging INTO a goal root keeps the root — allowed.
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor"), rrConcept("c")}
	edges := []Edge{rrHier("absorbed", "c")}
	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", map[string]bool{"survivor": true}, rrNow)
	if err != nil {
		t.Fatalf("merging into a goal-root survivor must be allowed: %v", err)
	}
	if !hasEdge(plan.EdgesToCreate, "survivor", "c") {
		t.Errorf("want survivor->c; got %+v", plan.EdgesToCreate)
	}
}

func TestPlanMerge_SelfAndEmptyRejected(t *testing.T) {
	concepts := []ConceptNode{rrConcept("A")}
	if _, err := PlanMerge(concepts, nil, "A", "A", nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("self-merge err = %v; want ErrInvalid", err)
	}
	if _, err := PlanMerge(concepts, nil, "A", "  ", nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty absorbed err = %v; want ErrInvalid", err)
	}
	if _, err := PlanMerge(concepts, nil, "", "A", nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty survivor err = %v; want ErrInvalid", err)
	}
}

func TestPlanMerge_UnknownConceptRejected(t *testing.T) {
	concepts := []ConceptNode{rrConcept("survivor")}
	if _, err := PlanMerge(concepts, nil, "survivor", "absorbed", nil, rrNow); !errors.Is(err, ErrMergeSplitUnknownConcept) {
		t.Errorf("unknown absorbed err = %v; want ErrMergeSplitUnknownConcept", err)
	}
	concepts2 := []ConceptNode{rrConcept("absorbed")}
	if _, err := PlanMerge(concepts2, nil, "survivor", "absorbed", nil, rrNow); !errors.Is(err, ErrMergeSplitUnknownConcept) {
		t.Errorf("unknown survivor err = %v; want ErrMergeSplitUnknownConcept", err)
	}
}

func TestPlanMerge_DeletedConceptRejected(t *testing.T) {
	absorbed := rrConcept("absorbed")
	del := rrNow
	absorbed.DeletedAt = &del
	concepts := []ConceptNode{rrConcept("survivor"), absorbed}
	if _, err := PlanMerge(concepts, nil, "survivor", "absorbed", nil, rrNow); !errors.Is(err, ErrConceptDeleted) {
		t.Errorf("err = %v; want ErrConceptDeleted", err)
	}
}

func TestPlanMerge_MultipleParentsRejected(t *testing.T) {
	// absorbed has two live hierarchy parents -> ambiguous.
	concepts := []ConceptNode{rrConcept("P1"), rrConcept("P2"), rrConcept("absorbed"), rrConcept("survivor")}
	edges := []Edge{rrHier("P1", "absorbed"), rrHier("P2", "absorbed")}
	if _, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow); !errors.Is(err, ErrMergeSplitMultipleParents) {
		t.Errorf("err = %v; want ErrMergeSplitMultipleParents", err)
	}
}

func TestPlanMerge_SurvivorMultipleParentsRejectedWhenSpliced(t *testing.T) {
	// absorbed->survivor is the splice path, but survivor ALSO has parent Q ->
	// re-pointing would leave survivor double-parented (walk-through check).
	concepts := []ConceptNode{rrConcept("G"), rrConcept("Q"), rrConcept("absorbed"), rrConcept("survivor")}
	edges := []Edge{rrHier("G", "absorbed"), rrHier("absorbed", "survivor"), rrHier("Q", "survivor")}
	if _, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow); !errors.Is(err, ErrMergeSplitMultipleParents) {
		t.Errorf("err = %v; want ErrMergeSplitMultipleParents", err)
	}
}

func TestPlanMerge_ReparentInheritsInvalidProvenanceFailsLoud(t *testing.T) {
	// A corrupt provenance on a stored edge must surface, never be swallowed.
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor"), rrConcept("c")}
	edges := []Edge{rrEdge("absorbed", "c", EdgeClassHierarchy, Provenance("bogus"))}
	if _, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v; want ErrInvalid (re-express rejected)", err)
	}
}

func TestPlanMerge_Deterministic(t *testing.T) {
	concepts := []ConceptNode{
		msConcept("R", "r"), msConcept("absorbed", "absorbed-key"),
		msConcept("survivor", "survivor-key"), rrConcept("c1"), rrConcept("c2"),
	}
	edges := []Edge{
		rrHier("R", "absorbed"), rrHier("R", "survivor"),
		rrHier("absorbed", "c1"), rrHier("absorbed", "c2"),
	}
	p1, err1 := PlanMerge(concepts, edges, "survivor", "absorbed", map[string]bool{"R": true}, rrNow)
	p2, err2 := PlanMerge(concepts, edges, "survivor", "absorbed", map[string]bool{"R": true}, rrNow)
	if err1 != nil || err2 != nil {
		t.Fatalf("PlanMerge: %v / %v", err1, err2)
	}
	if !reflect.DeepEqual(createdShape(p1.EdgesToCreate), createdShape(p2.EdgesToCreate)) {
		t.Errorf("create shapes differ: %v vs %v", createdShape(p1.EdgesToCreate), createdShape(p2.EdgesToCreate))
	}
	sd1, sd2 := createdShape(p1.EdgesToSoftDelete), createdShape(p2.EdgesToSoftDelete)
	if !reflect.DeepEqual(sd1, sd2) {
		t.Errorf("soft-delete shapes differ: %v vs %v", sd1, sd2)
	}
	if p1.Lineage != p2.Lineage {
		t.Errorf("lineage differs: %+v vs %+v", p1.Lineage, p2.Lineage)
	}
}

func TestPlanMerge_ZeroNowFallsBackToWallClock(t *testing.T) {
	concepts := []ConceptNode{rrConcept("absorbed"), rrConcept("survivor"), rrConcept("c")}
	edges := []Edge{rrHier("absorbed", "c")}
	before := time.Now().UTC()
	plan, err := PlanMerge(concepts, edges, "survivor", "absorbed", nil, time.Time{})
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if plan.Absorbed.DeletedAt == nil || plan.Absorbed.DeletedAt.Before(before) {
		t.Errorf("zero now should fall back to wall-clock; got %v (before %v)", plan.Absorbed.DeletedAt, before)
	}
}

// ---- PlanSplit ---------------------------------------------------------------

// msSplitParent builds a parent concept carrying atoms + intent (inherited by
// the minted children).
func msSplitParent() ConceptNode {
	p := msConcept("parent", "parent-key")
	p.AtomRefs = []string{"01970000-0000-7000-a000-000000000001", "01970000-0000-7000-a000-000000000002"}
	p.Intent = IntentRemediate
	return p
}

func TestPlanSplit_HappyPath_TwoChildren(t *testing.T) {
	// P->parent; parent->X1, parent->X2. Split into [Alpha, Beta]; X1 routes to
	// Beta (index 1), X2 defaults to Alpha (index 0). P fans out to both children.
	parent := msSplitParent()
	concepts := []ConceptNode{rrConcept("P"), parent, rrConcept("X1"), rrConcept("X2")}
	edges := []Edge{
		rrEdge("P", "parent", EdgeClassHierarchy, ProvenanceCompanionSuggestedAccepted),
		rrEdge("parent", "X1", EdgeClassHierarchy, ProvenanceSystemDerived),
		rrHier("parent", "X2"),
	}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	assign := map[string]int{"X1": 1}

	plan, err := PlanSplit(concepts, edges, "parent", specs, assign, map[string]bool{"P": false}, rrNow)
	if err != nil {
		t.Fatalf("PlanSplit: %v", err)
	}
	if len(plan.Children) != 2 {
		t.Fatalf("want 2 children; got %d", len(plan.Children))
	}
	alpha, beta := plan.Children[0], plan.Children[1]
	if alpha.Title != "Alpha" || beta.Title != "Beta" {
		t.Errorf("child titles = %q,%q; want Alpha,Beta", alpha.Title, beta.Title)
	}
	if alpha.ConceptKey != "alpha" || beta.ConceptKey != "beta" {
		t.Errorf("child keys = %q,%q; want alpha,beta", alpha.ConceptKey, beta.ConceptKey)
	}
	for _, c := range plan.Children {
		if !reflect.DeepEqual(c.AtomRefs, parent.AtomRefs) {
			t.Errorf("child %q atomRefs = %v; want parent's %v", c.Title, c.AtomRefs, parent.AtomRefs)
		}
		if c.Intent != IntentRemediate {
			t.Errorf("child %q intent = %q; want remediate", c.Title, c.Intent)
		}
		if c.Provenance != ProvenanceLearnerAuthored {
			t.Errorf("child %q provenance = %q; want learner_authored", c.Title, c.Provenance)
		}
		if len(c.ConceptID) != 36 {
			t.Errorf("child %q id = %q; want fresh UUIDv7", c.Title, c.ConceptID)
		}
		if c.DeletedAt != nil {
			t.Errorf("child %q should be live", c.Title)
		}
	}
	// Soft-deletes: P->parent, parent->X1, parent->X2.
	if len(plan.EdgesToSoftDelete) != 3 {
		t.Fatalf("want 3 soft-deletes; got %d (%+v)", len(plan.EdgesToSoftDelete), plan.EdgesToSoftDelete)
	}
	// Creates: P->alpha, P->beta (fan-out, companion prov preserved); beta->X1
	// (system prov preserved); alpha->X2 (learner prov).
	pAlpha, ok := findEdge(plan.EdgesToCreate, "P", alpha.ConceptID)
	if !ok || pAlpha.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("want P->alpha with companion provenance; got %+v (ok=%v)", pAlpha, ok)
	}
	if !hasEdge(plan.EdgesToCreate, "P", beta.ConceptID) {
		t.Error("want P->beta fan-out")
	}
	betaX1, ok := findEdge(plan.EdgesToCreate, beta.ConceptID, "X1")
	if !ok || betaX1.Provenance != ProvenanceSystemDerived {
		t.Errorf("want beta->X1 with system provenance; got %+v (ok=%v)", betaX1, ok)
	}
	if !hasEdge(plan.EdgesToCreate, alpha.ConceptID, "X2") {
		t.Error("want alpha->X2 (default assignment index 0)")
	}
	if len(plan.EdgesToCreate) != 4 {
		t.Errorf("want 4 creates; got %d (%+v)", len(plan.EdgesToCreate), plan.EdgesToCreate)
	}
	// Parent tombstoned + lineage per child in order.
	if plan.Parent.DeletedAt == nil || !plan.Parent.DeletedAt.Equal(rrNow) {
		t.Errorf("parent tombstone = %v; want %v", plan.Parent.DeletedAt, rrNow)
	}
	if len(plan.Lineage) != 2 {
		t.Fatalf("want 2 lineage records; got %d", len(plan.Lineage))
	}
	wantL0 := LineageRecord{Operation: LineageOperationSplit, FromConceptID: "parent", ToConceptID: alpha.ConceptID, FromConceptKey: "parent-key", ToConceptKey: "alpha"}
	if plan.Lineage[0] != wantL0 {
		t.Errorf("lineage[0] = %+v; want %+v", plan.Lineage[0], wantL0)
	}
	if plan.Lineage[1].ToConceptID != beta.ConceptID || plan.Lineage[1].ToConceptKey != "beta" {
		t.Errorf("lineage[1] = %+v; want to beta", plan.Lineage[1])
	}
	// Purity.
	if concepts[1].DeletedAt != nil {
		t.Error("input parent concept was mutated")
	}
	for i := range edges {
		if edges[i].DeletedAt != nil {
			t.Errorf("input edge %d mutated", i)
		}
	}
}

func TestPlanSplit_Apex(t *testing.T) {
	// parent apex; parent->X1. Children become apexes (no P->child creates).
	parent := msSplitParent()
	concepts := []ConceptNode{parent, rrConcept("X1")}
	edges := []Edge{rrHier("parent", "X1")}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}

	plan, err := PlanSplit(concepts, edges, "parent", specs, map[string]int{"X1": 0}, nil, rrNow)
	if err != nil {
		t.Fatalf("PlanSplit: %v", err)
	}
	if len(plan.EdgesToCreate) != 1 || !hasEdge(plan.EdgesToCreate, plan.Children[0].ConceptID, "X1") {
		t.Fatalf("apex split: want only child0->X1; got %+v", plan.EdgesToCreate)
	}
	for _, e := range plan.EdgesToCreate {
		if e.TargetConceptID == plan.Children[0].ConceptID || e.TargetConceptID == plan.Children[1].ConceptID {
			t.Errorf("apex children must have no parent edge; got %+v", e)
		}
	}
}

func TestPlanSplit_LateralRepointAndDedupe(t *testing.T) {
	// parent->A twice (dedupe) + B->parent (reversed: parent is TARGET -> B->head,
	// direction kept) + control C~D + a soft-deleted parent lateral (ignored).
	// All re-point to children[0].
	parent := msSplitParent()
	concepts := []ConceptNode{parent, rrConcept("A"), rrConcept("B"), rrConcept("C"), rrConcept("D"), rrConcept("E")}
	deadLateral := msLateral("parent", "E")
	del := rrNow
	deadLateral.DeletedAt = &del
	edges := []Edge{
		msLateral("parent", "A"),
		msLateral("parent", "A"),
		msLateral("B", "parent"),
		msLateral("C", "D"),
		deadLateral,
	}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}

	plan, err := PlanSplit(concepts, edges, "parent", specs, nil, nil, rrNow)
	if err != nil {
		t.Fatalf("PlanSplit: %v", err)
	}
	head := plan.Children[0].ConceptID
	if !hasEdge(plan.EdgesToCreate, head, "A") {
		t.Errorf("want child0->A (forward kept); got %+v", plan.EdgesToCreate)
	}
	if !hasEdge(plan.EdgesToCreate, "B", head) {
		t.Errorf("want B->child0 (reversed kept); got %+v", plan.EdgesToCreate)
	}
	if len(plan.EdgesToCreate) != 2 {
		t.Errorf("duplicate parent->A must dedupe to one create; got %d (%+v)", len(plan.EdgesToCreate), plan.EdgesToCreate)
	}
	if hasEdge(plan.EdgesToSoftDelete, "C", "D") {
		t.Error("control lateral C~D must be untouched")
	}
	if len(plan.EdgesToSoftDelete) != 3 {
		t.Errorf("want 3 soft-deletes (dead lateral ignored); got %d", len(plan.EdgesToSoftDelete))
	}
}

func TestPlanSplit_TooFewRejected(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{parent}
	if _, err := PlanSplit(concepts, nil, "parent", []SplitChildSpec{{Title: "Only"}}, nil, nil, rrNow); !errors.Is(err, ErrSplitTooFew) {
		t.Errorf("err = %v; want ErrSplitTooFew", err)
	}
}

func TestPlanSplit_DuplicateChildKeyRejected(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{parent}
	specs := []SplitChildSpec{{Title: "Foo Bar"}, {Title: "foo-bar"}} // both normalise to foo-bar
	if _, err := PlanSplit(concepts, nil, "parent", specs, nil, nil, rrNow); !errors.Is(err, ErrSplitDuplicateChildKey) {
		t.Errorf("err = %v; want ErrSplitDuplicateChildKey", err)
	}
}

func TestPlanSplit_EmptyTitleRejected(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{parent}
	if _, err := PlanSplit(concepts, nil, "parent", []SplitChildSpec{{Title: "Alpha"}, {Title: "   "}}, nil, nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v; want ErrInvalid", err)
	}
}

func TestPlanSplit_BadAssignmentRejected(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{parent, rrConcept("X1")}
	edges := []Edge{rrHier("parent", "X1")}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	// unknown child key.
	if _, err := PlanSplit(concepts, edges, "parent", specs, map[string]int{"NOPE": 0}, nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown-child assign err = %v; want ErrInvalid", err)
	}
	// value out of range.
	if _, err := PlanSplit(concepts, edges, "parent", specs, map[string]int{"X1": 5}, nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("out-of-range assign err = %v; want ErrInvalid", err)
	}
}

func TestPlanSplit_RootImmutableRejected(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{parent}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	if _, err := PlanSplit(concepts, nil, "parent", specs, nil, map[string]bool{"parent": true}, rrNow); !errors.Is(err, ErrRootImmutable) {
		t.Errorf("err = %v; want ErrRootImmutable", err)
	}
}

func TestPlanSplit_UnknownAndDeletedParentRejected(t *testing.T) {
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	if _, err := PlanSplit(nil, nil, "parent", specs, nil, nil, rrNow); !errors.Is(err, ErrMergeSplitUnknownConcept) {
		t.Errorf("unknown parent err = %v; want ErrMergeSplitUnknownConcept", err)
	}
	del := rrNow
	parent := msSplitParent()
	parent.DeletedAt = &del
	if _, err := PlanSplit([]ConceptNode{parent}, nil, "parent", specs, nil, nil, rrNow); !errors.Is(err, ErrConceptDeleted) {
		t.Errorf("deleted parent err = %v; want ErrConceptDeleted", err)
	}
	if _, err := PlanSplit([]ConceptNode{msSplitParent()}, nil, "  ", specs, nil, nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty parent id err = %v; want ErrInvalid", err)
	}
}

func TestPlanSplit_MultipleParentsRejected(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{rrConcept("P1"), rrConcept("P2"), parent}
	edges := []Edge{rrHier("P1", "parent"), rrHier("P2", "parent")}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	if _, err := PlanSplit(concepts, edges, "parent", specs, nil, nil, rrNow); !errors.Is(err, ErrMergeSplitMultipleParents) {
		t.Errorf("err = %v; want ErrMergeSplitMultipleParents", err)
	}
}

func TestPlanSplit_MintErrorFailsLoud(t *testing.T) {
	// A parent with a blank tenant surfaces the mint failure (fail-loud), never a
	// half-built child.
	parent := msSplitParent()
	parent.TenantID = ""
	concepts := []ConceptNode{parent}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	if _, err := PlanSplit(concepts, nil, "parent", specs, nil, nil, rrNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v; want ErrInvalid (mint rejected)", err)
	}
}

func TestPlanSplit_Deterministic(t *testing.T) {
	parent := msSplitParent()
	concepts := []ConceptNode{rrConcept("P"), parent, rrConcept("X1"), rrConcept("X2")}
	edges := []Edge{rrHier("P", "parent"), rrHier("parent", "X1"), rrHier("parent", "X2")}
	specs := []SplitChildSpec{{Title: "Alpha"}, {Title: "Beta"}}
	assign := map[string]int{"X1": 1}
	p1, err1 := PlanSplit(concepts, edges, "parent", specs, assign, nil, rrNow)
	p2, err2 := PlanSplit(concepts, edges, "parent", specs, assign, nil, rrNow)
	if err1 != nil || err2 != nil {
		t.Fatalf("PlanSplit: %v / %v", err1, err2)
	}
	if !reflect.DeepEqual(splitCreatedShape(p1), splitCreatedShape(p2)) {
		t.Errorf("split create shapes differ:\n%v\n%v", splitCreatedShape(p1), splitCreatedShape(p2))
	}
	if !reflect.DeepEqual(createdShape(p1.EdgesToSoftDelete), createdShape(p2.EdgesToSoftDelete)) {
		t.Errorf("split soft-delete shapes differ")
	}
}

// ---- ADR-244 D6 (CHO-2303): merge must not drop the absorbed node's atoms ----
//
// PlanMerge preserved edges and lineage but tombstoned the absorbed node with
// its atom_refs still on it, so every binding the learner had curated there was
// lost from the live graph. SPLIT already handled the same field
// (each child inherits the parent's atoms), and merge is its dual, so the
// asymmetry was an omission rather than a decision. Measured zero rows lost at
// the time only because almost nothing carried atoms yet.

func TestPlanMerge_UnionsAtomRefsIntoSurvivor(t *testing.T) {
	const (
		atomA = "01970000-0000-7000-a000-00000000000a"
		atomB = "01970000-0000-7000-a000-00000000000b"
		atomC = "01970000-0000-7000-a000-00000000000c"
	)
	survivor := msConcept("s", "survivor")
	survivor.AtomRefs = []string{atomA}
	absorbed := msConcept("x", "absorbed")
	absorbed.AtomRefs = []string{atomB, atomC}

	plan, err := PlanMerge([]ConceptNode{survivor, absorbed}, nil, "s", "x", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	want := []string{atomA, atomB, atomC}
	if !reflect.DeepEqual(plan.Survivor.AtomRefs, want) {
		t.Fatalf("survivor atom_refs = %v, want %v", plan.Survivor.AtomRefs, want)
	}
	if plan.Survivor.ConceptID != "s" {
		t.Fatalf("survivor id = %q, want %q", plan.Survivor.ConceptID, "s")
	}
}

func TestPlanMerge_UnionDeduplicatesSharedAtoms(t *testing.T) {
	const (
		atomA = "01970000-0000-7000-a000-00000000000a"
		atomB = "01970000-0000-7000-a000-00000000000b"
	)
	survivor := msConcept("s", "survivor")
	survivor.AtomRefs = []string{atomA, atomB}
	absorbed := msConcept("x", "absorbed")
	absorbed.AtomRefs = []string{atomB, atomA} // fully overlapping, reordered

	plan, err := PlanMerge([]ConceptNode{survivor, absorbed}, nil, "s", "x", nil, rrNow)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	want := []string{atomA, atomB} // survivor order preserved, no duplicates
	if !reflect.DeepEqual(plan.Survivor.AtomRefs, want) {
		t.Fatalf("survivor atom_refs = %v, want %v", plan.Survivor.AtomRefs, want)
	}
}

// The file's purity contract: `concepts` is read-only. AtomRefs is a slice, so
// a value copy shares its backing array and a careless append writes through.
func TestPlanMerge_UnionDoesNotMutateInputConcepts(t *testing.T) {
	const (
		atomA = "01970000-0000-7000-a000-00000000000a"
		atomB = "01970000-0000-7000-a000-00000000000b"
	)
	survivor := msConcept("s", "survivor")
	survivor.AtomRefs = make([]string, 1, 8) // spare capacity: append would write through
	survivor.AtomRefs[0] = atomA
	absorbed := msConcept("x", "absorbed")
	absorbed.AtomRefs = []string{atomB}

	in := []ConceptNode{survivor, absorbed}
	if _, err := PlanMerge(in, nil, "s", "x", nil, rrNow); err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !reflect.DeepEqual(in[0].AtomRefs, []string{atomA}) {
		t.Fatalf("input survivor mutated: %v", in[0].AtomRefs)
	}
	if !reflect.DeepEqual(in[1].AtomRefs, []string{atomB}) {
		t.Fatalf("input absorbed mutated: %v", in[1].AtomRefs)
	}
}
