package conceptgraph

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// CHO-2038 — ceremony learning-edges: goal-scoped selection → mint ConceptNodes
// on the map. RED-first domain tests for the unified selection operation
// (ADR-212/214). A ticked learning-edge mints a ConceptNode AND a hierarchy edge
// root→concept (so it lands in the goal's root subtree and paints on the map),
// carrying a remediate|explore intent (Fork 2, "Both, labelled").

const (
	testRoot    = "00000000-0000-0000-0000-0000000000aa"
	testTenant  = "t-1"
	testLearner = "g-1"
)

func fixedNow() time.Time { return time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC) }

// --- Intent value object ---

func TestIntent_Valid(t *testing.T) {
	for _, i := range []Intent{IntentUnspecified, IntentRemediate, IntentExplore} {
		if !i.Valid() {
			t.Errorf("Intent(%q).Valid() = false, want true", i)
		}
	}
	if Intent("bogus").Valid() {
		t.Error("Intent(\"bogus\").Valid() = true, want false")
	}
}

func TestIntent_Labelled(t *testing.T) {
	// A ceremony learning-edge MUST be labelled remediate|explore; unspecified is
	// a valid default for a plain concept but is NOT a labelled learning-edge.
	if !IntentRemediate.Labelled() || !IntentExplore.Labelled() {
		t.Error("remediate/explore should be Labelled()")
	}
	if IntentUnspecified.Labelled() {
		t.Error("unspecified must NOT be Labelled()")
	}
	if Intent("bogus").Labelled() {
		t.Error("bogus must NOT be Labelled()")
	}
}

// --- ConceptNode carries Intent (additive field) ---

func TestNewConceptNode_CarriesIntent(t *testing.T) {
	n, err := NewConceptNode(NewConceptNodeInput{
		TenantID: testTenant, LearnerGCID: testLearner, Title: "Fractions",
		Intent: IntentRemediate, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("NewConceptNode: unexpected err %v", err)
	}
	if n.Intent != IntentRemediate {
		t.Errorf("node.Intent = %q, want %q", n.Intent, IntentRemediate)
	}
}

func TestNewConceptNode_SetsConceptKeyFromTitle(t *testing.T) {
	// Key-at-mint (CHO-2038): the stable normalised slug of the title —
	// lower-case, non-alphanumeric runs collapse to one '-', trimmed. This is
	// the qgen target_growth_edges key the proofing R8-7 gate requires.
	cases := map[string]string{
		"Roots":                 "roots",
		"Completing the square": "completing-the-square",
		"  Photosynthesis!! ":   "photosynthesis",
		"Off-by-one errors":     "off-by-one-errors",
	}
	for title, want := range cases {
		n, err := NewConceptNode(NewConceptNodeInput{
			TenantID: testTenant, LearnerGCID: testLearner, Title: title, Now: fixedNow(),
		})
		if err != nil {
			t.Fatalf("NewConceptNode(%q): %v", title, err)
		}
		if n.ConceptKey != want {
			t.Errorf("ConceptKey(%q) = %q, want %q", title, n.ConceptKey, want)
		}
	}
}

func TestNewConceptNode_DefaultsIntentUnspecified(t *testing.T) {
	n, err := NewConceptNode(NewConceptNodeInput{
		TenantID: testTenant, LearnerGCID: testLearner, Title: "Fractions", Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("NewConceptNode: unexpected err %v", err)
	}
	if n.Intent != IntentUnspecified {
		t.Errorf("default node.Intent = %q, want unspecified", n.Intent)
	}
}

func TestNewConceptNode_RejectsInvalidIntent(t *testing.T) {
	_, err := NewConceptNode(NewConceptNodeInput{
		TenantID: testTenant, LearnerGCID: testLearner, Title: "Fractions",
		Intent: Intent("bogus"), Now: fixedNow(),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewConceptNode with bogus intent: err = %v, want ErrInvalid", err)
	}
}

// --- SelectLearningEdges: the batch operation ---

func baseSelect(sels ...LearningEdgeSelection) SelectLearningEdgesInput {
	return SelectLearningEdgesInput{
		TenantID: testTenant, LearnerGCID: testLearner, RootConceptID: testRoot,
		Selections: sels, Now: fixedNow(),
	}
}

func TestSelectLearningEdges_MintsConceptAndHierarchyEdgeUnderRoot(t *testing.T) {
	out, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "Adding fractions", Intent: IntentRemediate},
	))
	if err != nil {
		t.Fatalf("SelectLearningEdges: unexpected err %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	m := out[0]
	if m.Node == nil || m.Edge == nil {
		t.Fatalf("minted edge must have both Node and Edge; got node=%v edge=%v", m.Node, m.Edge)
	}
	// Node: minted, provenance companion_suggested_accepted, intent carried, title kept.
	if m.Node.Title != "Adding fractions" {
		t.Errorf("node.Title = %q, want %q", m.Node.Title, "Adding fractions")
	}
	if m.Node.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("node.Provenance = %q, want companion_suggested_accepted", m.Node.Provenance)
	}
	if m.Node.Intent != IntentRemediate || m.Intent != IntentRemediate {
		t.Errorf("intent not carried: node=%q result=%q", m.Node.Intent, m.Intent)
	}
	if m.Node.TenantID != testTenant || m.Node.LearnerGCID != testLearner {
		t.Errorf("node scope wrong: tenant=%q learner=%q", m.Node.TenantID, m.Node.LearnerGCID)
	}
	// Edge: hierarchy, source=root (parent), target=node (child), so it joins the subtree.
	if m.Edge.Class != EdgeClassHierarchy {
		t.Errorf("edge.Class = %q, want hierarchy", m.Edge.Class)
	}
	if m.Edge.SourceConceptID != testRoot {
		t.Errorf("edge.Source = %q, want root %q", m.Edge.SourceConceptID, testRoot)
	}
	if m.Edge.TargetConceptID != m.Node.ConceptID {
		t.Errorf("edge.Target = %q, want node %q", m.Edge.TargetConceptID, m.Node.ConceptID)
	}
	if m.Edge.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("edge.Provenance = %q, want companion_suggested_accepted", m.Edge.Provenance)
	}

	// Prove it actually lands in the goal's subtree via the live traversal engine.
	nodes := []ConceptNode{{ConceptID: testRoot, TenantID: testTenant, LearnerGCID: testLearner, Title: "Maths"}, *m.Node}
	edges := []Edge{*m.Edge}
	set := SubtreeConceptIDs(nodes, edges, testRoot)
	if !set[m.Node.ConceptID] {
		t.Error("minted concept is NOT in the goal's root subtree — it will not paint on the map")
	}
}

func TestSelectLearningEdges_BothLabelled(t *testing.T) {
	out, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "Adding fractions", Intent: IntentRemediate},
		LearningEdgeSelection{Title: "Continued fractions", Intent: IntentExplore},
	))
	if err != nil {
		t.Fatalf("SelectLearningEdges: unexpected err %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	gotIntents := map[Intent]bool{}
	for _, m := range out {
		gotIntents[m.Node.Intent] = true
		if m.Edge.SourceConceptID != testRoot {
			t.Errorf("all edges must root at %q; got %q", testRoot, m.Edge.SourceConceptID)
		}
	}
	if !gotIntents[IntentRemediate] || !gotIntents[IntentExplore] {
		t.Errorf("both intents must survive; got %v", gotIntents)
	}
}

func TestSelectLearningEdges_DistinctConceptIDs(t *testing.T) {
	out, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "Alpha", Intent: IntentExplore},
		LearningEdgeSelection{Title: "Beta", Intent: IntentExplore},
	))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if out[0].Node.ConceptID == out[1].Node.ConceptID {
		t.Error("minted concepts must have distinct ids")
	}
}

func TestSelectLearningEdges_DedupByTitleWithinBatch(t *testing.T) {
	out, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "Fractions", Intent: IntentRemediate},
		LearningEdgeSelection{Title: "  fractions ", Intent: IntentExplore}, // dup (case/space)
	))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dedup within batch failed: len(out) = %d, want 1", len(out))
	}
	// First-seen wins (its title + intent).
	if strings.TrimSpace(out[0].Node.Title) != "Fractions" {
		t.Errorf("first-seen title should win; got %q", out[0].Node.Title)
	}
}

func TestSelectLearningEdges_BlankRootFailsLoud(t *testing.T) {
	in := baseSelect(LearningEdgeSelection{Title: "X", Intent: IntentRemediate})
	in.RootConceptID = "   "
	_, err := SelectLearningEdges(in)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank root: err = %v, want ErrInvalid (can't place on a map with no root)", err)
	}
}

func TestSelectLearningEdges_UnlabelledIntentFailsLoud(t *testing.T) {
	// unspecified is a valid ConceptNode default but NOT a valid ceremony edge.
	_, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "X", Intent: IntentUnspecified},
	))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unlabelled intent: err = %v, want ErrInvalid", err)
	}
}

func TestSelectLearningEdges_InvalidIntentFailsLoud(t *testing.T) {
	_, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "X", Intent: Intent("bogus")},
	))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("bogus intent: err = %v, want ErrInvalid", err)
	}
}

func TestSelectLearningEdges_BlankTitleFailsLoud(t *testing.T) {
	_, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "   ", Intent: IntentRemediate},
	))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title: err = %v, want ErrInvalid", err)
	}
}

func TestSelectLearningEdges_EmptySelectionsFailsLoud(t *testing.T) {
	_, err := SelectLearningEdges(baseSelect())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty selections: err = %v, want ErrInvalid", err)
	}
}

func TestSelectLearningEdges_RequiresTenantAndLearner(t *testing.T) {
	for _, tc := range []struct {
		name            string
		tenant, learner string
	}{
		{"blank tenant", "", testLearner},
		{"blank learner", testTenant, ""},
	} {
		in := baseSelect(LearningEdgeSelection{Title: "X", Intent: IntentRemediate})
		in.TenantID, in.LearnerGCID = tc.tenant, tc.learner
		if _, err := SelectLearningEdges(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}
}

func TestSelectLearningEdges_AtomRefsFlowThrough(t *testing.T) {
	atom := "11111111-1111-1111-1111-111111111111"
	out, err := SelectLearningEdges(baseSelect(
		LearningEdgeSelection{Title: "Grounded", Intent: IntentExplore, AtomRefs: []string{atom}},
	))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if len(out[0].Node.AtomRefs) != 1 || out[0].Node.AtomRefs[0] != atom {
		t.Errorf("atom refs did not flow onto the minted node: %v", out[0].Node.AtomRefs)
	}
}
