package clusterprojection

import (
	"context"
	"errors"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

const (
	tenantID = "01970000-0000-7000-8000-000000000001"
	gcid     = "01970000-0000-7000-9000-000000000001"
	seedAtom = "01970000-0000-7000-a000-000000000001"
)

type fakeLoader struct {
	cluster *userknowledgegraph.MapCluster
	err     error
}

func (f *fakeLoader) Load(ctx context.Context, clusterID string) (*userknowledgegraph.MapCluster, error) {
	return f.cluster, f.err
}

type fakeApplier struct {
	root    *conceptgraph.ConceptNode
	goal    *goal.Goal
	cluster *userknowledgegraph.MapCluster
	called  bool
	err     error
}

func (f *fakeApplier) Apply(ctx context.Context, root *conceptgraph.ConceptNode, g *goal.Goal, cluster *userknowledgegraph.MapCluster) error {
	f.called = true
	f.root, f.goal, f.cluster = root, g, cluster
	return f.err
}

func activeCluster(t *testing.T) *userknowledgegraph.MapCluster {
	t.Helper()
	c, err := userknowledgegraph.NewMapCluster(tenantID, gcid, "agile", seedAtom, "Agile")
	if err != nil {
		t.Fatalf("NewMapCluster: %v", err)
	}
	return c
}

// TestProject_MintsGoalAndRoot — the happy path materialises a curiosity Goal
// anchored to a root ConceptNode titled from the cluster, with the seed atom
// attached, and flips the cluster to projected recording the Goal (ADR-223).
func TestProject_MintsGoalAndRoot(t *testing.T) {
	loader := &fakeLoader{cluster: activeCluster(t)}
	applier := &fakeApplier{}
	p := NewProjector(loader, applier)

	goalID, err := p.Project(context.Background(), tenantID, gcid, loader.cluster.ClusterID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if goalID == "" {
		t.Fatal("expected a goal id")
	}
	if !applier.called {
		t.Fatal("applier.Apply was not called")
	}
	// Root concept
	if applier.root.Title != "Agile" {
		t.Errorf("root title = %q, want Agile", applier.root.Title)
	}
	if len(applier.root.AtomRefs) != 1 || applier.root.AtomRefs[0] != seedAtom {
		t.Errorf("root atomRefs = %v, want [%s]", applier.root.AtomRefs, seedAtom)
	}
	if string(applier.root.Provenance) != "learner_authored" {
		t.Errorf("root provenance = %q, want learner_authored", applier.root.Provenance)
	}
	// Goal
	if applier.goal.Kind != goal.KindCuriosity {
		t.Errorf("goal kind = %q, want curiosity", applier.goal.Kind)
	}
	if applier.goal.RootConceptID == nil || *applier.goal.RootConceptID != applier.root.ConceptID {
		t.Errorf("goal root = %v, want %s", applier.goal.RootConceptID, applier.root.ConceptID)
	}
	if applier.goal.NorthStarNote != "Agile" {
		t.Errorf("goal note = %q, want Agile", applier.goal.NorthStarNote)
	}
	if goalID != applier.goal.GoalID {
		t.Errorf("returned goalID %q != applied goal %q", goalID, applier.goal.GoalID)
	}
	// Cluster flipped to projected recording the goal
	if applier.cluster.Status != userknowledgegraph.ClusterStatusProjected {
		t.Errorf("cluster status = %q, want projected", applier.cluster.Status)
	}
	if applier.cluster.ProjectedIntoGoalID != goalID {
		t.Errorf("cluster projectedIntoGoalID = %q, want %q", applier.cluster.ProjectedIntoGoalID, goalID)
	}
}

// TestProject_Idempotent — an already-projected cluster returns its recorded
// goal without re-materialising.
func TestProject_Idempotent(t *testing.T) {
	c := activeCluster(t)
	_ = c.Project("01970000-0000-7000-d000-000000000009")
	loader := &fakeLoader{cluster: c}
	applier := &fakeApplier{}
	p := NewProjector(loader, applier)

	goalID, err := p.Project(context.Background(), tenantID, gcid, c.ClusterID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if goalID != "01970000-0000-7000-d000-000000000009" {
		t.Errorf("goalID = %q, want the recorded goal", goalID)
	}
	if applier.called {
		t.Error("applier must NOT be called for an already-projected cluster")
	}
}

// TestProject_NotOwned — a cluster owned by another learner is rejected.
func TestProject_NotOwned(t *testing.T) {
	c := activeCluster(t)
	c.UserGCID = "01970000-0000-7000-9000-000000000099"
	p := NewProjector(&fakeLoader{cluster: c}, &fakeApplier{})
	if _, err := p.Project(context.Background(), tenantID, gcid, c.ClusterID); !errors.Is(err, ErrClusterNotOwned) {
		t.Errorf("err = %v, want ErrClusterNotOwned", err)
	}
}

// TestProject_LoadError — a load failure propagates (fail-loud).
func TestProject_LoadError(t *testing.T) {
	sentinel := errors.New("boom")
	p := NewProjector(&fakeLoader{err: sentinel}, &fakeApplier{})
	if _, err := p.Project(context.Background(), tenantID, gcid, "x"); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

// TestProject_ApplierError — an apply failure propagates (no silent success).
func TestProject_ApplierError(t *testing.T) {
	sentinel := errors.New("tx failed")
	loader := &fakeLoader{cluster: activeCluster(t)}
	p := NewProjector(loader, &fakeApplier{err: sentinel})
	if _, err := p.Project(context.Background(), tenantID, gcid, loader.cluster.ClusterID); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}
