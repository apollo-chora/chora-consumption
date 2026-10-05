package userknowledgegraph

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// JunctionStatus is the lifecycle status of a persistent Junction
// detection record. Detection emits `pending`; user-consented merge
// transitions it to `accepted` (which then triggers the cluster merge
// command); user-decline transitions it to `rejected` (so the same pair
// stops being re-surfaced for this user).
type JunctionStatus string

const (
	JunctionStatusPending  JunctionStatus = "pending"
	JunctionStatusAccepted JunctionStatus = "accepted"
	JunctionStatusRejected JunctionStatus = "rejected"
)

// Domain errors for Junction.
var (
	ErrJunctionTenantRequired   = errors.New("kg.junction: tenant_id required")
	ErrJunctionUserRequired     = errors.New("kg.junction: user_gcid required")
	ErrJunctionClusterARequired = errors.New("kg.junction: cluster_a_id required")
	ErrJunctionClusterBRequired = errors.New("kg.junction: cluster_b_id required")
	ErrJunctionSelfMerge        = errors.New("kg.junction: cluster_a_id and cluster_b_id must differ")
	ErrJunctionEmptyOverlap     = errors.New("kg.junction: overlap_atom_ids must contain at least one atom")
	ErrJunctionAlreadyResolved  = errors.New("kg.junction: already accepted or rejected")
	ErrJunctionAtomNotInOverlap = errors.New("kg.junction: resolved_via_atom_id must be one of overlap_atom_ids")
)

// Junction is the persistent record of a detected cluster overlap for
// one user, surfaced to the UI as a join opportunity. Distinct from
// JunctionOpportunity (which is the transient per-fog-call surface).
//
// Detection writes a Junction with status=pending; user accept/reject
// transitions it. Re-detection of the SAME (cluster_a, cluster_b) pair
// while a `pending` record exists is a no-op (idempotent — adapter
// upsert path uses FindPendingByPair).
type Junction struct {
	JunctionID        string
	TenantID          string
	UserGCID          string
	ClusterAID        string
	ClusterBID        string
	OverlapAtomIDs    []string
	Status            JunctionStatus
	ResolvedViaAtomID string
	DetectedAt        time.Time
	ResolvedAt        *time.Time
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
}

// NewJunction validates and constructs a new pending Junction.
func NewJunction(tenantID, userGCID, clusterAID, clusterBID string, overlapAtomIDs []string, now time.Time) (*Junction, error) {
	tenantID = strings.TrimSpace(tenantID)
	userGCID = strings.TrimSpace(userGCID)
	clusterAID = strings.TrimSpace(clusterAID)
	clusterBID = strings.TrimSpace(clusterBID)

	if tenantID == "" {
		return nil, ErrJunctionTenantRequired
	}
	if userGCID == "" {
		return nil, ErrJunctionUserRequired
	}
	if clusterAID == "" {
		return nil, ErrJunctionClusterARequired
	}
	if clusterBID == "" {
		return nil, ErrJunctionClusterBRequired
	}
	if clusterAID == clusterBID {
		return nil, ErrJunctionSelfMerge
	}
	if len(overlapAtomIDs) == 0 {
		return nil, ErrJunctionEmptyOverlap
	}
	// Defensive copy
	overlapCopy := make([]string, len(overlapAtomIDs))
	copy(overlapCopy, overlapAtomIDs)

	return &Junction{
		JunctionID:     domain.NewUUIDv7(),
		TenantID:       tenantID,
		UserGCID:       userGCID,
		ClusterAID:     clusterAID,
		ClusterBID:     clusterBID,
		OverlapAtomIDs: overlapCopy,
		Status:         JunctionStatusPending,
		DetectedAt:     now,
		Version:        0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// IsPending returns true when the junction is still awaiting user action.
func (j *Junction) IsPending() bool {
	return j.Status == JunctionStatusPending
}

// Accept transitions PENDING → ACCEPTED. The chosen via-atom must be one
// of the OverlapAtomIDs (the user picks WHICH overlap atom is the
// junction). Caller (application service) is responsible for invoking
// the cluster merge command after this returns.
func (j *Junction) Accept(viaAtomID string, now time.Time) error {
	if j.Status != JunctionStatusPending {
		return ErrJunctionAlreadyResolved
	}
	viaAtomID = strings.TrimSpace(viaAtomID)
	if viaAtomID == "" {
		return ErrJunctionAtomNotInOverlap
	}
	found := false
	for _, a := range j.OverlapAtomIDs {
		if a == viaAtomID {
			found = true
			break
		}
	}
	if !found {
		return ErrJunctionAtomNotInOverlap
	}
	j.Status = JunctionStatusAccepted
	j.ResolvedViaAtomID = viaAtomID
	t := now
	j.ResolvedAt = &t
	j.UpdatedAt = now
	j.Version++
	return nil
}

// Reject transitions PENDING → REJECTED. Idempotent semantics at the
// application layer; the domain rejects double-reject to expose stale
// callers.
func (j *Junction) Reject(now time.Time) error {
	if j.Status != JunctionStatusPending {
		return ErrJunctionAlreadyResolved
	}
	j.Status = JunctionStatusRejected
	t := now
	j.ResolvedAt = &t
	j.UpdatedAt = now
	j.Version++
	return nil
}
