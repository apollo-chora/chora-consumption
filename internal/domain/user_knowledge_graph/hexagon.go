package userknowledgegraph

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// HexagonNeighborCount is the canonical neighbor cardinality enforced at
// schema (CHECK constraint), proto contract, GraphQL cardinality, and
// here in the domain.
const HexagonNeighborCount = 6

// NeighborRelation mirrors the kg_neighbor_relation PostgreSQL enum.
// Superset of the authoring-time KnowledgeGraphEdgeType plus
// CuriosityJump — fog-only neighbors the LLM proposed without a pre-existing
// authoring edge.
type NeighborRelation string

const (
	NeighborRelationPrerequisiteOf NeighborRelation = "prerequisite_of"
	NeighborRelationExtends        NeighborRelation = "extends"
	NeighborRelationAnalogyOf      NeighborRelation = "analogy_of"
	NeighborRelationContrastsWith  NeighborRelation = "contrasts_with"
	NeighborRelationAppliedIn      NeighborRelation = "applied_in"
	NeighborRelationCuriosityJump  NeighborRelation = "curiosity_jump"
)

// FogInvalidationReason mirrors the kg_fog_invalidation_reason enum.
// Listed in priority order (HIGHEST first) — see Invalidate() semantics.
type FogInvalidationReason string

const (
	FogInvalidationReasonNeverGenerated      FogInvalidationReason = "never_generated"
	FogInvalidationReasonAtomPublished       FogInvalidationReason = "atom_published"
	FogInvalidationReasonAtomRevisionUpdated FogInvalidationReason = "atom_revision_updated"
	FogInvalidationReasonUserRetentionShift  FogInvalidationReason = "user_retention_shifted"
	FogInvalidationReasonManualAdmin         FogInvalidationReason = "manual_admin"
)

// fogReasonPriority assigns numeric priority to invalidation reasons.
// Higher = stronger signal. Used by Invalidate() to decide whether a
// later, weaker invalidation event should override an existing stronger one.
//
// Order rationale:
//   - atom_published / atom_revision_updated: content actually changed —
//     highest priority (the cached neighbors may now point to stale atoms)
//   - user_retention_shifted: learner's retention profile shifted; the
//     fog should re-rank to match — slightly lower than content change
//   - manual_admin: explicit admin reset — lower than data-driven signals
//     (admin reset is "I want a clean slate" — content events take precedence)
//   - never_generated: pseudo-state for a fresh row that's never been
//     invalidated; lowest priority (always overridden)
var fogReasonPriority = map[FogInvalidationReason]int{
	FogInvalidationReasonAtomPublished:       100,
	FogInvalidationReasonAtomRevisionUpdated: 90,
	FogInvalidationReasonUserRetentionShift:  70,
	FogInvalidationReasonManualAdmin:         50,
	FogInvalidationReasonNeverGenerated:      0,
}

var (
	ErrExplorationIDRequired   = errors.New("kg.hexagon: exploration_id required")
	ErrFocalAtomIDRequired     = errors.New("kg.hexagon: focal_atom_id required")
	ErrNeighborCountInvalid    = errors.New("kg.hexagon: neighbors must contain exactly 6 records")
	ErrNeighborAtomIDRequired  = errors.New("kg.hexagon: neighbor atom_id required")
	ErrNeighborConfidenceRange = errors.New("kg.hexagon: neighbor confidence must be in [0.0, 1.0]")
	ErrNeighborDuplicate       = errors.New("kg.hexagon: neighbor atom_ids must be distinct")
	ErrNeighborIsFocal         = errors.New("kg.hexagon: focal_atom_id must not appear in neighbors")
	ErrRunIDRequired           = errors.New("kg.hexagon: generated_by_run_id required")
	ErrModelIDRequired         = errors.New("kg.hexagon: generated_by_model_id required")
)

// HexagonNeighbor is one of 6 LLM-generated neighbors around a focal atom.
// Mirrors the JSONB record stored in kg_hexagon_nodes.neighbors_json — the
// json tags ARE that persisted shape (snake_case, stable across releases);
// the pg adapter marshals/unmarshals this struct verbatim.
type HexagonNeighbor struct {
	AtomID     string           `json:"atom_id"`
	Relation   NeighborRelation `json:"relation"`
	Confidence float32          `json:"confidence"`
	FogLabel   string           `json:"fog_label"`
	IsJunction bool             `json:"is_junction"`
}

// HexagonNode is the persisted fog cache row — focal atom + its 6 neighbors.
// Value-object-with-id: caller upserts on (exploration_id, focal_atom_id).
type HexagonNode struct {
	HexNodeID          string
	ExplorationID      string
	ClusterID          string
	TenantID           string
	UserGCID           string
	FocalAtomID        string
	Neighbors          []HexagonNeighbor
	GeneratedByRunID   string
	GeneratedByModelID string
	GeneratedAt        time.Time
	InvalidatedAt      *time.Time
	InvalidationReason FogInvalidationReason
	Version            int
	DeletedAt          *time.Time
}

// NewHexagonNode constructs a freshly-generated hexagon. Caller (the
// chora-consumption application service) is responsible for the upsert
// against (exploration_id, focal_atom_id) — domain only validates shape.
func NewHexagonNode(
	clusterID, explorationID, tenantID, userGCID, focalAtomID string,
	neighbors []HexagonNeighbor,
	generatedByRunID, generatedByModelID string,
) (*HexagonNode, error) {
	clusterID = strings.TrimSpace(clusterID)
	explorationID = strings.TrimSpace(explorationID)
	tenantID = strings.TrimSpace(tenantID)
	userGCID = strings.TrimSpace(userGCID)
	focalAtomID = strings.TrimSpace(focalAtomID)
	generatedByRunID = strings.TrimSpace(generatedByRunID)
	generatedByModelID = strings.TrimSpace(generatedByModelID)

	if clusterID == "" {
		return nil, ErrClusterIDRequired
	}
	if explorationID == "" {
		return nil, ErrExplorationIDRequired
	}
	if tenantID == "" {
		return nil, ErrTenantIDRequired
	}
	if userGCID == "" {
		return nil, ErrUserGCIDRequired
	}
	if focalAtomID == "" {
		return nil, ErrFocalAtomIDRequired
	}
	if generatedByRunID == "" {
		return nil, ErrRunIDRequired
	}
	if generatedByModelID == "" {
		return nil, ErrModelIDRequired
	}
	if len(neighbors) != HexagonNeighborCount {
		return nil, ErrNeighborCountInvalid
	}

	seen := make(map[string]bool, HexagonNeighborCount)
	for i := range neighbors {
		n := neighbors[i]
		atom := strings.TrimSpace(n.AtomID)
		if atom == "" {
			return nil, ErrNeighborAtomIDRequired
		}
		if n.Confidence < 0 || n.Confidence > 1 {
			return nil, ErrNeighborConfidenceRange
		}
		if seen[atom] {
			return nil, ErrNeighborDuplicate
		}
		seen[atom] = true
		if atom == focalAtomID {
			return nil, ErrNeighborIsFocal
		}
	}

	return &HexagonNode{
		HexNodeID:          domain.NewUUIDv7(),
		ExplorationID:      explorationID,
		ClusterID:          clusterID,
		TenantID:           tenantID,
		UserGCID:           userGCID,
		FocalAtomID:        focalAtomID,
		Neighbors:          append([]HexagonNeighbor(nil), neighbors...), // copy
		GeneratedByRunID:   generatedByRunID,
		GeneratedByModelID: generatedByModelID,
		GeneratedAt:        time.Now().UTC(),
		InvalidatedAt:      nil,
		InvalidationReason: FogInvalidationReasonNeverGenerated, // pseudo-state for "fresh"
		Version:            0,
	}, nil
}

// IsCacheFresh returns true when the hexagon has not been invalidated.
// Cache hits skip Model Broker calls; cache misses or stale rows trigger
// fog regen.
func (h *HexagonNode) IsCacheFresh() bool { return h.InvalidatedAt == nil }

// Invalidate marks the hexagon as stale with the given reason. Subsequent
// fetches will trigger fog regen via the FogOrchestrator.
//
// Priority semantics: a stronger signal (e.g., atom_published) overrides
// a weaker one (e.g., manual_admin) but not vice versa. This prevents an
// admin manual-reset from masking a content-change event in the audit trail.
func (h *HexagonNode) Invalidate(reason FogInvalidationReason) error {
	newPriority := fogReasonPriority[reason]
	currentPriority := 0
	if h.InvalidatedAt != nil {
		currentPriority = fogReasonPriority[h.InvalidationReason]
	}
	if h.InvalidatedAt != nil && newPriority <= currentPriority {
		// Existing invalidation is at least as strong; preserve it.
		return nil
	}
	now := time.Now().UTC()
	h.InvalidatedAt = &now
	h.InvalidationReason = reason
	h.Version++
	return nil
}

// JunctionAtomIDs returns the atom_ids of neighbors flagged as junction
// candidates. Used by application service to populate JunctionDetected
// event payload + Subscription.junctionDetected push.
func (h *HexagonNode) JunctionAtomIDs() []string {
	out := make([]string, 0, HexagonNeighborCount)
	for _, n := range h.Neighbors {
		if n.IsJunction {
			out = append(out, n.AtomID)
		}
	}
	return out
}
