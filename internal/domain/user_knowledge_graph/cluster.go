// Package userknowledgegraph implements the per-user Knowledge Graph
// aggregate family for the Content Consumption domain (per ADR-143).
//
// Aggregates:
//
//   - MapCluster   — disconnected per-user knowledge map (the entity that
//     the user-consented Join action merges)
//   - Exploration  — seed-rooted journey within a cluster
//   - HexagonNode  — cached focal + 6 LLM-generated neighbors
//   - TrailHop     — append-only path history
//
// JunctionOpportunity is transient (computed via index probe; never persisted).
//
// The relocated atom_semantic_edges table (renamed from chora_creation's
// knowledge_graph_edges) is read-only authoring-time semantic data; queries
// over it live in the existing knowledge_graph_read package, not here.
//
// HARD RULES:
//
//   - Pure domain code: NO database imports, NO HTTP/gRPC imports, NO Pub/Sub
//     adapter imports. All side effects are mediated by ports (see ports.go).
//   - Cross-DB queries forbidden (per ddd-enforcement #11). Cross-aggregate
//     reads use ports + Pub/Sub events.
//   - UUIDv7 for all new aggregate IDs (per ddd-enforcement #7).
//   - Soft delete via DeletedAt (per ddd-enforcement #5).
//
// Per ADR-143 §1, this package implements the chora-consumption domain
// home for KG; chora-creation no longer holds KG schema after migration
// 0002 is applied.
package userknowledgegraph

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// ClusterStatus is the lifecycle status enum mirroring the
// kg_cluster_status PostgreSQL enum.
type ClusterStatus string

const (
	ClusterStatusActive     ClusterStatus = "active"
	ClusterStatusArchived   ClusterStatus = "archived"
	ClusterStatusMergedInto ClusterStatus = "merged_into"
	// ClusterStatusProjected is the terminal state a cluster reaches once it
	// has been re-projected into a sovereign Goal (ADR-223). The fog cluster
	// is retired but never deleted; ProjectedIntoGoalID records the map it
	// became.
	ClusterStatusProjected ClusterStatus = "projected"
)

// Field-length constraints — must mirror migrations/0002.
const (
	maxSeedTopicLen   = 128
	maxDisplayNameLen = 128
)

// Domain errors. Adapter layer maps these to gRPC / HTTP status codes.
var (
	ErrTenantIDRequired         = errors.New("kg.cluster: tenant_id required")
	ErrUserGCIDRequired         = errors.New("kg.cluster: user_gcid required")
	ErrSeedTopicRequired        = errors.New("kg.cluster: seed_topic required")
	ErrSeedAtomIDRequired       = errors.New("kg.cluster: seed_atom_id required")
	ErrSeedTopicTooLong         = errors.New("kg.cluster: seed_topic exceeds max length")
	ErrDisplayNameTooLong       = errors.New("kg.cluster: display_name exceeds max length")
	ErrDisplayNameRequired      = errors.New("kg.cluster: display_name required")
	ErrSurvivingClusterRequired = errors.New("kg.cluster: surviving_cluster_id required")
	ErrViaAtomIDRequired        = errors.New("kg.cluster: via_atom_id required")
	ErrAlreadyArchived          = errors.New("kg.cluster: already archived")
	ErrAlreadyMerged            = errors.New("kg.cluster: already merged_into")
	ErrAlreadyProjected         = errors.New("kg.cluster: already projected")
	ErrProjectionGoalRequired   = errors.New("kg.cluster: projected_into_goal_id required")
	ErrSelfMerge                = errors.New("kg.cluster: cannot merge cluster into itself")
	ErrCannotRenameInactive     = errors.New("kg.cluster: cannot rename non-active cluster")
)

// MapCluster is the disconnected per-user knowledge map aggregate root.
// Mirrors the kg_user_map_clusters table.
type MapCluster struct {
	ClusterID           string
	TenantID            string
	UserGCID            string
	DisplayName         string
	SeedTopic           string
	SeedAtomID          string
	Status              ClusterStatus
	MergedIntoClusterID string
	MergedViaAtomID     string
	ProjectedIntoGoalID string
	NodeCount           int
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

// NewMapCluster constructs a fresh active cluster for a user-driven seed.
// displayName is optional; when empty, defaults to seedTopic.
func NewMapCluster(tenantID, userGCID, seedTopic, seedAtomID, displayName string) (*MapCluster, error) {
	tenantID = strings.TrimSpace(tenantID)
	userGCID = strings.TrimSpace(userGCID)
	seedTopic = strings.TrimSpace(seedTopic)
	seedAtomID = strings.TrimSpace(seedAtomID)
	displayName = strings.TrimSpace(displayName)

	if tenantID == "" {
		return nil, ErrTenantIDRequired
	}
	if userGCID == "" {
		return nil, ErrUserGCIDRequired
	}
	if seedTopic == "" {
		return nil, ErrSeedTopicRequired
	}
	if seedAtomID == "" {
		return nil, ErrSeedAtomIDRequired
	}
	if len(seedTopic) > maxSeedTopicLen {
		return nil, ErrSeedTopicTooLong
	}
	if displayName == "" {
		displayName = seedTopic
	}
	if len(displayName) > maxDisplayNameLen {
		return nil, ErrDisplayNameTooLong
	}

	now := time.Now().UTC()
	return &MapCluster{
		ClusterID:   domain.NewUUIDv7(),
		TenantID:    tenantID,
		UserGCID:    userGCID,
		DisplayName: displayName,
		SeedTopic:   seedTopic,
		SeedAtomID:  seedAtomID,
		Status:      ClusterStatusActive,
		NodeCount:   1,
		Version:     0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// IsActive returns true when the cluster is in the active lifecycle stage.
func (c *MapCluster) IsActive() bool { return c.Status == ClusterStatusActive }

// Archive transitions the cluster from ACTIVE to ARCHIVED. Idempotency at
// the adapter layer; the domain rejects double-archive to expose stale
// callers. ARCHIVED clusters become read-only; the sweeper soft-deletes
// their hexagon nodes after grace × 12.
func (c *MapCluster) Archive() error {
	switch c.Status {
	case ClusterStatusArchived:
		return ErrAlreadyArchived
	case ClusterStatusMergedInto:
		return ErrAlreadyMerged
	}
	c.Status = ClusterStatusArchived
	c.touch()
	return nil
}

// Project transitions the cluster to PROJECTED, recording the sovereign
// Goal (ADR-214) it was re-projected into (ADR-223). Terminal state: the
// fog cluster is retired but never deleted (history preserved). Active or
// archived clusters may be projected; merged / already-projected reject —
// the adapter maps ErrAlreadyProjected to an idempotent response carrying
// the recorded ProjectedIntoGoalID.
func (c *MapCluster) Project(goalID string) error {
	goalID = strings.TrimSpace(goalID)
	if goalID == "" {
		return ErrProjectionGoalRequired
	}
	switch c.Status {
	case ClusterStatusMergedInto:
		return ErrAlreadyMerged
	case ClusterStatusProjected:
		return ErrAlreadyProjected
	}
	c.Status = ClusterStatusProjected
	c.ProjectedIntoGoalID = goalID
	c.touch()
	return nil
}

// MergeInto transitions the cluster from ACTIVE to MERGED_INTO with the
// surviving cluster + via_atom recorded. Trail histories on the merged
// cluster's explorations are preserved (they continue to live in
// kg_exploration_trail with cluster_id pointing at the merged cluster).
//
// Caller (chora-consumption merge command handler) must atomically:
//
//  1. Reassign explorations' cluster_id to the surviving cluster
//  2. Persist this status change
//  3. Recompute surviving cluster's node_count
//  4. Emit chora.consumption.kg_map_cluster.merged.v1
func (c *MapCluster) MergeInto(survivingClusterID, viaAtomID string) error {
	survivingClusterID = strings.TrimSpace(survivingClusterID)
	viaAtomID = strings.TrimSpace(viaAtomID)

	if survivingClusterID == "" {
		return ErrSurvivingClusterRequired
	}
	if viaAtomID == "" {
		return ErrViaAtomIDRequired
	}
	if survivingClusterID == c.ClusterID {
		return ErrSelfMerge
	}
	switch c.Status {
	case ClusterStatusArchived:
		return ErrAlreadyArchived
	case ClusterStatusMergedInto:
		return ErrAlreadyMerged
	}

	c.Status = ClusterStatusMergedInto
	c.MergedIntoClusterID = survivingClusterID
	c.MergedViaAtomID = viaAtomID
	c.touch()
	return nil
}

// Rename updates the display_name on an ACTIVE cluster. Archived /
// merged_into clusters are read-only.
func (c *MapCluster) Rename(newDisplayName string) error {
	newDisplayName = strings.TrimSpace(newDisplayName)
	if newDisplayName == "" {
		return ErrDisplayNameRequired
	}
	if len(newDisplayName) > maxDisplayNameLen {
		return ErrDisplayNameTooLong
	}
	if !c.IsActive() {
		return ErrCannotRenameInactive
	}
	c.DisplayName = newDisplayName
	c.touch()
	return nil
}

// IncrementNodeCount bumps the materialised distinct-focal count when an
// Exploration command extends the cluster with a new focal atom.
// Caller (Exploration aggregate) is responsible for ensuring the increment
// only happens on the FIRST encounter of a given atom_id within the cluster.
func (c *MapCluster) IncrementNodeCount() {
	c.NodeCount++
	c.touch()
}

// AbsorbMerged updates the surviving cluster's display_name (defaulting to
// "{seed_a} + {seed_b}") and node_count after a merge. Caller must invoke
// MergeInto on the merged cluster separately.
//
// Per ADR-143 Open Item 2: post-merge display_name defaults to
// "{seed_topic_a} + {seed_topic_b}"; user can rename later via Rename().
func (c *MapCluster) AbsorbMerged(merged *MapCluster, viaAtomID string) error {
	if !c.IsActive() {
		return ErrAlreadyArchived
	}
	// via_atom is counted in both pre-merge clusters; subtract once on merge.
	c.NodeCount += merged.NodeCount - 1
	c.DisplayName = c.SeedTopic + " + " + merged.SeedTopic
	c.touch()
	return nil
}

func (c *MapCluster) touch() {
	c.UpdatedAt = time.Now().UTC()
	c.Version++
}
