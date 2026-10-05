package clusterprojection

import (
	"context"
	"testing"
	"time"
)

type recordingSink struct {
	calls int
	refs  []string
	src   string
}

func (r *recordingSink) ConceptAtomsBound(_ context.Context, conceptID, _, _, _ string, refs []string, _ time.Time) error {
	r.calls++
	r.refs = refs
	r.src = conceptID
	return nil
}

// ADR-244 D4: projection mints a root carrying the cluster's seed atom, which
// IS a binding and must not stay silent while the other write paths emit.
func TestProject_EmitsBindingForSeededRoot(t *testing.T) {
	loader := &fakeLoader{cluster: activeCluster(t)}
	sink := &recordingSink{}
	p := NewProjector(loader, &fakeApplier{}).WithBindingSink(sink)

	if _, err := p.Project(context.Background(), tenantID, gcid, loader.cluster.ClusterID); err != nil {
		t.Fatalf("Project: %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("emitted %d binding event(s), want 1", sink.calls)
	}
	if len(sink.refs) != 1 || sink.refs[0] != seedAtom {
		t.Errorf("refs = %v, want the seed atom %s", sink.refs, seedAtom)
	}
}

// A root minted with NO atoms must not emit: an empty concept is a first-class
// state (ADR-212 D1) and a phantom event would be churn every consumer then has
// to defend against.
//
// NB NewMapCluster REFUSES a blank seed_atom_id, so this state is unreachable
// through the aggregate's constructor. The cluster is therefore hand-built to
// exercise the guard directly. The guard is defensive, not a live branch: what
// it actually protects against is `Project`'s TrimSpace turning a
// whitespace-only seed into no atoms at all.
func TestProject_RootWithNoAtomsEmitsNothing(t *testing.T) {
	c := activeCluster(t)
	c.SeedAtomID = "   " // whitespace-only: TrimSpace yields no atom refs
	sink := &recordingSink{}
	p := NewProjector(&fakeLoader{cluster: c}, &fakeApplier{}).WithBindingSink(sink)

	if _, err := p.Project(context.Background(), tenantID, gcid, c.ClusterID); err != nil {
		t.Fatalf("Project: %v", err)
	}
	if sink.calls != 0 {
		t.Fatalf("an atomless projection emitted %d event(s), want 0", sink.calls)
	}
}

// An unwired sink must never fail the learner's projection.
func TestProject_NilSinkStillProjects(t *testing.T) {
	loader := &fakeLoader{cluster: activeCluster(t)}
	if _, err := NewProjector(loader, &fakeApplier{}).Project(
		context.Background(), tenantID, gcid, loader.cluster.ClusterID); err != nil {
		t.Fatalf("nil sink must not fail the projection: %v", err)
	}
}
