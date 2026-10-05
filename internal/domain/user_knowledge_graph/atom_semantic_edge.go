package userknowledgegraph

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// AtomEdgeType is the authoring-time semantic-edge enum, mirroring the
// kg_atom_edge_type PostgreSQL enum from migration 0002. NOTE: this is
// the AUTHORING-time superset (5 values). The LLM-fog NeighborRelation
// adds `curiosity_jump` on top — that value is fog-only and must NOT be
// stored in atom_semantic_edges.
type AtomEdgeType string

const (
	AtomEdgeTypePrerequisiteOf AtomEdgeType = "prerequisite_of"
	AtomEdgeTypeExtends        AtomEdgeType = "extends"
	AtomEdgeTypeAnalogyOf      AtomEdgeType = "analogy_of"
	AtomEdgeTypeContrastsWith  AtomEdgeType = "contrasts_with"
	AtomEdgeTypeAppliedIn      AtomEdgeType = "applied_in"
)

// IsAuthoringEdgeType reports whether the type is allowed in the
// authoring-time `atom_semantic_edges` table. `curiosity_jump` is
// rejected here on purpose — it lives only in the fog layer
// (kg_hexagon_nodes.neighbors_json), not in authoring-time edges.
func IsAuthoringEdgeType(t AtomEdgeType) bool {
	switch t {
	case AtomEdgeTypePrerequisiteOf,
		AtomEdgeTypeExtends,
		AtomEdgeTypeAnalogyOf,
		AtomEdgeTypeContrastsWith,
		AtomEdgeTypeAppliedIn:
		return true
	default:
		return false
	}
}

// Domain errors for AtomSemanticEdge.
var (
	ErrAtomEdgeTenantRequired = errors.New("kg.atom_edge: tenant_id required")
	ErrAtomEdgeSourceRequired = errors.New("kg.atom_edge: source_atom_id required")
	ErrAtomEdgeTargetRequired = errors.New("kg.atom_edge: target_atom_id required")
	ErrAtomEdgeSelfLoop       = errors.New("kg.atom_edge: source_atom_id and target_atom_id must differ")
	ErrAtomEdgeWeightRange    = errors.New("kg.atom_edge: weight must be in [0, 1]")
	ErrAtomEdgeTypeInvalid    = errors.New("kg.atom_edge: edge_type invalid (curiosity_jump is fog-only)")
)

// AtomSemanticEdge is the relocated, renamed authoring-time atom-to-atom
// semantic relationship. Mirrors the atom_semantic_edges table at
// migrations/0002. Tenant-scoped (NOT user-scoped — these are
// authoring-time facts shared across the tenant's catalogue).
type AtomSemanticEdge struct {
	EdgeID       string
	TenantID     string
	SourceAtomID string
	TargetAtomID string
	EdgeType     AtomEdgeType
	Weight       float32
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// NewAtomSemanticEdge constructs a new authoring-time edge after
// validation. UUIDv7 is minted by the domain (per ddd-enforcement #7).
func NewAtomSemanticEdge(tenantID, sourceAtomID, targetAtomID string, edgeType AtomEdgeType, weight float32, now time.Time) (*AtomSemanticEdge, error) {
	tenantID = strings.TrimSpace(tenantID)
	sourceAtomID = strings.TrimSpace(sourceAtomID)
	targetAtomID = strings.TrimSpace(targetAtomID)

	if tenantID == "" {
		return nil, ErrAtomEdgeTenantRequired
	}
	if sourceAtomID == "" {
		return nil, ErrAtomEdgeSourceRequired
	}
	if targetAtomID == "" {
		return nil, ErrAtomEdgeTargetRequired
	}
	if sourceAtomID == targetAtomID {
		return nil, ErrAtomEdgeSelfLoop
	}
	if weight < 0 || weight > 1 {
		return nil, ErrAtomEdgeWeightRange
	}
	if !IsAuthoringEdgeType(edgeType) {
		return nil, ErrAtomEdgeTypeInvalid
	}
	return &AtomSemanticEdge{
		EdgeID:       domain.NewUUIDv7(),
		TenantID:     tenantID,
		SourceAtomID: sourceAtomID,
		TargetAtomID: targetAtomID,
		EdgeType:     edgeType,
		Weight:       weight,
		Version:      0,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// SoftDelete marks the edge as deleted (per ddd-enforcement #5/#6).
// Default queries must filter `WHERE deleted_at IS NULL`.
func (e *AtomSemanticEdge) SoftDelete(now time.Time) {
	d := now
	e.DeletedAt = &d
	e.UpdatedAt = now
	e.Version++
}
