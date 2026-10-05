// ports.go — hexagonal ports the campaign domain needs. Adapters implement;
// the domain never imports infrastructure.
package campaign

import "context"

// ProgressRepository persists per-(tenant, learner, concept) ladder state
// (campaign_node_progress, migrations 0077/0078).
type ProgressRepository interface {
	// GetByConcept returns the live ladder row for one node, or (nil, nil)
	// when the node has no ladder yet (lazily created on first grading).
	GetByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string) (*NodeProgress, error)
	// ListByLearner returns all live ladder rows for the learner (frontier
	// computation filters them to a goal subtree in memory — learner graphs
	// are small by construction).
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*NodeProgress, error)
	// Save upserts on the live (tenant, learner, concept) identity.
	Save(ctx context.Context, p *NodeProgress) error
}
