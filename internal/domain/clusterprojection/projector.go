// Package clusterprojection re-projects a retired fog MapCluster (ADR-143)
// into the sovereign Knowledge Graph (ADR-212/214): a learner Goal anchored to
// a root ConceptNode seeded from the cluster's topic, materialised atomically
// (ADR-223). Learner-triggered, idempotent, and it never deletes the cluster -
// the cluster is flipped to the terminal `projected` state recording the Goal
// it became.
package clusterprojection

import (
	"context"
	"errors"
	"strings"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ErrClusterNotOwned is returned when the caller's GCID does not own the
// cluster (defence-in-depth beside RLS, which already scopes Load).
var ErrClusterNotOwned = errors.New("clusterprojection: cluster not owned by caller")

// ClusterLoader loads a MapCluster by id within the caller's RLS session.
// Satisfied by the existing userknowledgegraph.MapClusterRepo.
type ClusterLoader interface {
	Load(ctx context.Context, clusterID string) (*userknowledgegraph.MapCluster, error)
}

// Applier persists the root ConceptNode + the anchored Goal + the projected
// cluster in ONE transaction (ADR-223 D2), mirroring the existing
// SuggestionAccepter.ApplyAccept precedent so the three writes stay consistent.
type Applier interface {
	Apply(ctx context.Context, root *conceptgraph.ConceptNode, g *goal.Goal, cluster *userknowledgegraph.MapCluster) error
}

// BindingSink reports that a concept's atom_refs changed (ADR-244 D4). The
// projection mints a root concept carrying the cluster's seed atom, which IS a
// binding, so it must not stay silent while the other five write paths emit.
//
// OPTIONAL by design: a nil sink degrades to no event, never to a failed
// projection. Emission is additive observability on an already-durable write,
// and refusing a learner their Goal because a downstream lane is unwired would
// be the worse failure. Its absence is the caller's to log at wire time.
type BindingSink interface {
	ConceptAtomsBound(ctx context.Context, conceptID, tenantID, learnerGCID, provenance string, atomRefs []string, at time.Time) error
}

// Projector orchestrates the fog-cluster → sovereign-Goal projection.
type Projector struct {
	clusters ClusterLoader
	applier  Applier
	bindings BindingSink
	now      func() time.Time
}

// WithBindingSink attaches the ADR-244 D4 sink. Separate from the constructor so
// existing wiring and tests are untouched.
func (p *Projector) WithBindingSink(s BindingSink) *Projector {
	p.bindings = s
	return p
}

// NewProjector wires a Projector. now defaults to time.Now().UTC() when nil.
func NewProjector(clusters ClusterLoader, applier Applier) *Projector {
	return &Projector{
		clusters: clusters,
		applier:  applier,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Project re-projects the cluster into a sovereign Goal and returns the goal id.
// It is idempotent: an already-projected cluster returns its recorded goal
// without re-materialising. The cluster is never hard-deleted (ADR-223 D1).
func (p *Projector) Project(ctx context.Context, tenantID, gcid, clusterID string) (string, error) {
	cluster, err := p.clusters.Load(ctx, clusterID)
	if err != nil {
		return "", err
	}
	if cluster.UserGCID != gcid {
		return "", ErrClusterNotOwned
	}
	// Idempotent: the cluster already became a Goal - return it.
	if cluster.Status == userknowledgegraph.ClusterStatusProjected {
		return cluster.ProjectedIntoGoalID, nil
	}

	now := p.now()

	// Root ConceptNode: the map's apex (ADR-214 D1), titled from the cluster's
	// display name, with the cluster's seed atom attached so the projected map
	// is non-empty. Provenance defaults to learner_authored - the learner
	// seeded the fog topic.
	var atomRefs []string
	if seed := strings.TrimSpace(cluster.SeedAtomID); seed != "" {
		atomRefs = []string{seed}
	}
	root, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Title:       cluster.DisplayName,
		AtomRefs:    atomRefs,
		Now:         now,
	})
	if err != nil {
		return "", err
	}

	// Goal anchored to the root concept (ADR-214: goal ≡ evolving root concept).
	rootID := root.ConceptID
	g, err := goal.NewGoal(goal.NewGoalInput{
		TenantID:      tenantID,
		LearnerGCID:   gcid,
		Kind:          goal.KindCuriosity,
		NorthStarNote: cluster.DisplayName,
		RootConceptID: &rootID,
		Now:           now,
	})
	if err != nil {
		return "", err
	}

	// Retire the fog cluster (never delete) recording the Goal it became.
	if err := cluster.Project(g.GoalID); err != nil {
		return "", err
	}

	// Atomic: root concept + goal + projected cluster in one transaction.
	if err := p.applier.Apply(ctx, root, g, cluster); err != nil {
		return "", err
	}
	// ADR-244 D4, AFTER the durable write (persist-then-emit). A seedless
	// cluster mints an EMPTY root, which is not a binding and must not emit:
	// an empty concept is a first-class state (ADR-212 D1), and a phantom
	// event would be churn every consumer then has to defend against.
	if p.bindings != nil && len(root.AtomRefs) > 0 {
		_ = p.bindings.ConceptAtomsBound(ctx, root.ConceptID, tenantID, gcid,
			string(root.Provenance), root.AtomRefs, now)
	}
	return g.GoalID, nil
}
