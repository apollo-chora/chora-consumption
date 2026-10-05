package userknowledgegraph

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// ExplorationStatus mirrors the kg_exploration_status PostgreSQL enum.
type ExplorationStatus string

const (
	ExplorationStatusActive   ExplorationStatus = "active"
	ExplorationStatusArchived ExplorationStatus = "archived"
)

var (
	ErrClusterIDRequired         = errors.New("kg.exploration: cluster_id required")
	ErrStartedAtAtomIDRequired   = errors.New("kg.exploration: started_at_atom_id required")
	ErrTargetAtomIDRequired      = errors.New("kg.exploration: target_atom_id required")
	ErrExplorationAlreadyArchive = errors.New("kg.exploration: already archived")
	ErrExplorationNotActive      = errors.New("kg.exploration: not in active status")
)

// Exploration is a seed-rooted journey within a MapCluster. Multiple per
// cluster after a merge (each pre-merge cluster contributes its own
// exploration). Mirrors the kg_user_explorations table.
type Exploration struct {
	ExplorationID      string
	ClusterID          string
	TenantID           string
	UserGCID           string
	StartedAtAtomID    string
	CurrentFocalAtomID string
	Status             ExplorationStatus
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// NewExploration constructs a fresh active exploration. CurrentFocalAtomID
// is initialised to startedAtAtomID; the user's first click-to-graduate
// will update it via MoveFocal.
func NewExploration(clusterID, tenantID, userGCID, startedAtAtomID string) (*Exploration, error) {
	clusterID = strings.TrimSpace(clusterID)
	tenantID = strings.TrimSpace(tenantID)
	userGCID = strings.TrimSpace(userGCID)
	startedAtAtomID = strings.TrimSpace(startedAtAtomID)

	if clusterID == "" {
		return nil, ErrClusterIDRequired
	}
	if tenantID == "" {
		return nil, ErrTenantIDRequired
	}
	if userGCID == "" {
		return nil, ErrUserGCIDRequired
	}
	if startedAtAtomID == "" {
		return nil, ErrStartedAtAtomIDRequired
	}

	now := time.Now().UTC()
	return &Exploration{
		ExplorationID:      domain.NewUUIDv7(),
		ClusterID:          clusterID,
		TenantID:           tenantID,
		UserGCID:           userGCID,
		StartedAtAtomID:    startedAtAtomID,
		CurrentFocalAtomID: startedAtAtomID,
		Status:             ExplorationStatusActive,
		Version:            0,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

// IsActive returns true when the exploration is in ACTIVE status.
func (e *Exploration) IsActive() bool { return e.Status == ExplorationStatusActive }

// Archive transitions ACTIVE -> ARCHIVED.
func (e *Exploration) Archive() error {
	if e.Status == ExplorationStatusArchived {
		return ErrExplorationAlreadyArchive
	}
	e.Status = ExplorationStatusArchived
	e.touch()
	return nil
}

// MoveFocal advances the exploration's current focal atom (click-to-graduate).
// Caller (the application service) is responsible for verifying that
// targetAtomID is one of the cached 6 neighbors of the current hexagon
// AND for atomically appending a TrailHop in the same transaction.
//
// MoveFocal to the current focal is a no-op (idempotent — defends against
// double-tap from the UI).
func (e *Exploration) MoveFocal(targetAtomID string) error {
	targetAtomID = strings.TrimSpace(targetAtomID)
	if targetAtomID == "" {
		return ErrTargetAtomIDRequired
	}
	if !e.IsActive() {
		return ErrExplorationNotActive
	}
	if targetAtomID == e.CurrentFocalAtomID {
		// No-op — no version bump.
		return nil
	}
	e.CurrentFocalAtomID = targetAtomID
	e.touch()
	return nil
}

func (e *Exploration) touch() {
	e.UpdatedAt = time.Now().UTC()
	e.Version++
}
