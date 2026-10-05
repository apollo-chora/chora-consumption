// junction_detection.go — application service that detects user-driven
// cluster overlaps and persists Junction records (per ADR-143 §6 +
// S5.2 mission deliverable 4).
//
// Detection flow:
//
//  1. Caller supplies the candidate atom set for a "current" cluster
//     (typically the cluster's seed atom + any atoms already focal in
//     its explorations).
//  2. We scan all OTHER active clusters of the same (tenant, user)
//     and compare against the focal atoms cached for those clusters
//     (HexagonRepo.FindAllFocalsByUser, excluding currentClusterID).
//  3. For each other cluster, the overlap = intersection of
//     candidateAtoms ∩ that cluster's focals. If the overlap size
//     meets `threshold`, we persist a Junction (idempotent — same
//     cluster pair won't duplicate) and emit
//     chora.consumption.kg_junction.detected.v1.
//
// Hexagonal: this is a SUBSCRIBER / application service. HTTP handlers
// invoke it via the InvokeOnCreate / InvokeOnPromote helpers.
package subscribers

import (
	"context"
	"errors"
	"strings"
	"time"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// DefaultJunctionOverlapThreshold mirrors the ADR-143 mission default
// (3 atoms must overlap before a junction is surfaced). Configurable per
// tenant in production via tenant_entitlements.config_overrides.
const DefaultJunctionOverlapThreshold = 3

// JunctionDetector is the per-user, per-cluster overlap detector. Repos
// are the domain ports — production wires the pg adapters (RLS-bound),
// tests + dev wire the in-memory ones.
type JunctionDetector struct {
	clusters  userknowledgegraph.MapClusterRepo
	hexagons  userknowledgegraph.HexagonRepo
	junctions userknowledgegraph.JunctionRepo
	publisher userknowledgegraph.EventPublisher
	threshold int
}

// NewJunctionDetector constructs the detector.
func NewJunctionDetector(
	clusters userknowledgegraph.MapClusterRepo,
	hexagons userknowledgegraph.HexagonRepo,
	junctions userknowledgegraph.JunctionRepo,
	publisher userknowledgegraph.EventPublisher,
	threshold int,
) *JunctionDetector {
	if threshold < 1 {
		threshold = DefaultJunctionOverlapThreshold
	}
	return &JunctionDetector{
		clusters:  clusters,
		hexagons:  hexagons,
		junctions: junctions,
		publisher: publisher,
		threshold: threshold,
	}
}

// JunctionDetectionResult is the outcome of a single detect call.
type JunctionDetectionResult struct {
	JunctionsDetected int
	JunctionIDs       []string
}

// DetectForCluster runs detection for the supplied cluster against all
// other active clusters of the (tenant, user). Returns the count + IDs
// of newly-persisted Junctions (idempotent — already-pending pairs are
// skipped).
//
// candidateAtoms is the set of atoms-of-interest for the current
// cluster (typically: seed_atom_id + any focal atoms already explored).
// HTTP handler decides what to pass.
func (d *JunctionDetector) DetectForCluster(
	ctx context.Context,
	tenantID, userGCID, currentClusterID string,
	candidateAtoms []string,
) (JunctionDetectionResult, error) {
	out := JunctionDetectionResult{}
	if strings.TrimSpace(tenantID) == "" {
		return out, errors.New("junction_detection: tenant_id required")
	}
	if strings.TrimSpace(userGCID) == "" {
		return out, errors.New("junction_detection: user_gcid required")
	}
	if strings.TrimSpace(currentClusterID) == "" {
		return out, errors.New("junction_detection: current_cluster_id required")
	}
	if len(candidateAtoms) == 0 {
		return out, nil
	}

	candidateSet := make(map[string]bool, len(candidateAtoms))
	for _, a := range candidateAtoms {
		candidateSet[a] = true
	}

	// Other active clusters of the same (tenant, user).
	others, err := d.clusters.ListActiveByUser(ctx, tenantID, userGCID)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()

	for _, other := range others {
		if other.ClusterID == currentClusterID {
			continue
		}
		// Find the OTHER cluster's focal atoms (excluding current).
		// FindAllFocalsByUser returns ACROSS all clusters except
		// excludeClusterID, so we narrow to the specific other.ClusterID
		// by re-checking each focal's cluster_id via the cache.
		// (Production pgx swaps this for a tenant-scoped JOIN.)
		focals, err := d.hexagons.FindAllFocalsByUser(ctx, tenantID, userGCID, currentClusterID)
		if err != nil {
			return out, err
		}
		// Build per-cluster overlap. Filter focals down to those
		// belonging to other.ClusterID by inspecting the cache.
		otherFocals := d.focalsForCluster(ctx, tenantID, userGCID, other.ClusterID, focals)
		overlap := intersection(candidateSet, otherFocals)
		if len(overlap) < d.threshold {
			continue
		}

		// Idempotent: skip if a pending junction already exists for the
		// pair regardless of order.
		existing, err := d.junctions.FindPendingByPair(ctx, tenantID, userGCID, currentClusterID, other.ClusterID)
		if err != nil {
			return out, err
		}
		if existing != nil {
			continue
		}

		j, err := userknowledgegraph.NewJunction(tenantID, userGCID, currentClusterID, other.ClusterID, overlap, now)
		if err != nil {
			return out, err
		}
		if err := d.junctions.Save(ctx, j); err != nil {
			return out, err
		}
		// Load both clusters for the event payload.
		current, err := d.clusters.Load(ctx, currentClusterID)
		if err != nil {
			return out, err
		}
		// Pick the first overlap atom as the via-atom for the event;
		// the user picks the actual via-atom on accept.
		via := overlap[0]
		if err := d.publisher.PublishJunctionDetected(ctx, current, other, via, ""); err != nil {
			return out, err
		}
		out.JunctionsDetected++
		out.JunctionIDs = append(out.JunctionIDs, j.JunctionID)
	}
	return out, nil
}

// focalsForCluster narrows a focal list down to those belonging to one
// specific cluster. In production, FindAllFocalsByUser already groups
// by cluster — for the in-memory adapter we re-scan via Find on the
// cache (sub-millisecond N for typical N).
func (d *JunctionDetector) focalsForCluster(
	ctx context.Context,
	tenantID, userGCID, clusterID string,
	allFocals []string,
) []string {
	out := make([]string, 0, len(allFocals))
	for _, focal := range allFocals {
		// Find the hexagon row(s) for this focal across the user's
		// explorations. If any belongs to clusterID, include it.
		// We cheat with FindFresh + Find — they're keyed by
		// (exploration, focal). The in-memory adapter's iteration
		// path walks all rows, which is O(n). Acceptable for tests.
		_ = tenantID
		_ = userGCID
		matches := d.matchClusterByFocal(ctx, clusterID, focal)
		if matches {
			out = append(out, focal)
		}
	}
	return out
}

// matchClusterByFocal returns true if any hexagon row exists for
// (clusterID, focal). HasFocalInCluster is a port method since the pg
// swap — the in-memory adapter walks its store, production pg probes
// idx_hex_user_focals + cluster_id. Lookup errors are treated as
// no-match: detection is best-effort enrichment on the explore path and
// must not fail the caller's mutation.
func (d *JunctionDetector) matchClusterByFocal(ctx context.Context, clusterID, focal string) bool {
	ok, err := d.hexagons.HasFocalInCluster(ctx, clusterID, focal)
	if err != nil {
		return false
	}
	return ok
}

// intersection returns the sorted-set intersection of `set` and `list`,
// as a slice (preserves only items from `list` that are in `set`).
func intersection(set map[string]bool, list []string) []string {
	out := make([]string, 0)
	seen := make(map[string]bool)
	for _, x := range list {
		if set[x] && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
