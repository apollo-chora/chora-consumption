package userknowledgegraph

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

var (
	ErrFromFocalRequired = errors.New("kg.trail: from_focal_atom_id required")
	ErrToFocalRequired   = errors.New("kg.trail: to_focal_atom_id required")
	ErrTrailSameAtoms    = errors.New("kg.trail: from and to focal atom_ids must differ (schema CHECK)")
	ErrStepIndexNegative = errors.New("kg.trail: step_index must be >= 0")
	ErrRelationRequired  = errors.New("kg.trail: relation required")
)

// TrailHop is one append-only entry in an exploration's path history.
// One row per click-to-graduate. Mirrors kg_exploration_trail; database
// trigger enforces append-only semantics (no UPDATE / DELETE).
type TrailHop struct {
	TrailID         string
	ExplorationID   string
	ClusterID       string
	TenantID        string
	UserGCID        string
	FromFocalAtomID string
	ToFocalAtomID   string
	Relation        NeighborRelation
	StepIndex       int
	TraversedAt     time.Time
}

// NewTrailHop validates and constructs a TrailHop. Caller (the application
// service handling MoveFocal) is responsible for atomically persisting
// this hop in the same transaction as the Exploration.MoveFocal update.
func NewTrailHop(
	explorationID, clusterID, tenantID, userGCID,
	fromFocalAtomID, toFocalAtomID string,
	relation NeighborRelation,
	stepIndex int,
) (*TrailHop, error) {
	explorationID = strings.TrimSpace(explorationID)
	clusterID = strings.TrimSpace(clusterID)
	tenantID = strings.TrimSpace(tenantID)
	userGCID = strings.TrimSpace(userGCID)
	fromFocalAtomID = strings.TrimSpace(fromFocalAtomID)
	toFocalAtomID = strings.TrimSpace(toFocalAtomID)

	if explorationID == "" {
		return nil, ErrExplorationIDRequired
	}
	if clusterID == "" {
		return nil, ErrClusterIDRequired
	}
	if tenantID == "" {
		return nil, ErrTenantIDRequired
	}
	if userGCID == "" {
		return nil, ErrUserGCIDRequired
	}
	if fromFocalAtomID == "" {
		return nil, ErrFromFocalRequired
	}
	if toFocalAtomID == "" {
		return nil, ErrToFocalRequired
	}
	if fromFocalAtomID == toFocalAtomID {
		return nil, ErrTrailSameAtoms
	}
	if stepIndex < 0 {
		return nil, ErrStepIndexNegative
	}
	if strings.TrimSpace(string(relation)) == "" {
		return nil, ErrRelationRequired
	}

	return &TrailHop{
		TrailID:         domain.NewUUIDv7(),
		ExplorationID:   explorationID,
		ClusterID:       clusterID,
		TenantID:        tenantID,
		UserGCID:        userGCID,
		FromFocalAtomID: fromFocalAtomID,
		ToFocalAtomID:   toFocalAtomID,
		Relation:        relation,
		StepIndex:       stepIndex,
		TraversedAt:     time.Now().UTC(),
	}, nil
}

// JunctionOpportunity is a transient surface — never persisted. Computed
// during fog generation when a candidate neighbor's atom_id is also a focal
// in another active cluster of the same user. Carried in the
// JunctionDetected event payload + the GraphQL Subscription push.
type JunctionOpportunity struct {
	ViaAtomID                 string
	CurrentClusterID          string
	OtherClusterID            string
	CurrentClusterDisplayName string
	OtherClusterDisplayName   string
}

// NewJunctionOpportunity is a thin constructor — no validation beyond the
// sanity checks already enforced by the orchestrator detect_junctions node.
func NewJunctionOpportunity(viaAtomID, currentClusterID, otherClusterID, currentDisplayName, otherDisplayName string) JunctionOpportunity {
	return JunctionOpportunity{
		ViaAtomID:                 viaAtomID,
		CurrentClusterID:          currentClusterID,
		OtherClusterID:            otherClusterID,
		CurrentClusterDisplayName: currentDisplayName,
		OtherClusterDisplayName:   otherDisplayName,
	}
}
