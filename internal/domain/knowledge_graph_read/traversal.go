// Package knowledge_graph_read — discovery-mode read traversal over a
// snapshot of the chora-creation knowledge graph.
//
// MVP traversal is BFS adjacency from a seed topic up to a max depth.
// Comic Ch7 P15 fog-of-war progressive reveal is FUTURE. The graph snapshot
// is sourced from chora-creation via an HTTP adapter (see
// internal/adapter/creation_client.go) — domain code only depends on the
// Graph port (interface) below.
//
// Heatmap mocks per-topic Ebbinghaus retention scores into RAG (red /
// amber / green) tiers for the learner's per-gcid retention-by-topic
// view. Real spaced-repetition retention reads land in M12 (the in-flight
// Phyllis-MVP DailyDose agent owns SM-2 — this package only EXPOSES
// retention scores as a heatmap, never computes them).
package knowledge_graph_read

import (
	"errors"
	"strings"
)

// MaxDepth caps BFS traversal — matches consumption-admin.yaml KG inspection.
const MaxDepth = 5

// Tier is a RAG retention bucket.
type Tier string

const (
	TierGreen Tier = "GREEN" // retention >= 0.70
	TierAmber Tier = "AMBER" // 0.40 <= retention < 0.70
	TierRed   Tier = "RED"   // retention < 0.40
)

// Graph is the traversal port. Adapters: in-memory (tests) + HTTP client
// to chora-creation (production).
type Graph interface {
	Neighbors(topic string) ([]string, error)
}

// DiscoverResult is the BFS result.
type DiscoverResult struct {
	Seed   string
	Depth  int
	Topics []string
}

// Discover performs BFS adjacency traversal from seed up to depth.
// Cycles are guarded by a visited set. Depth=0 returns just the seed.
func Discover(g Graph, seed string, depth int) (DiscoverResult, error) {
	if strings.TrimSpace(seed) == "" {
		return DiscoverResult{}, errors.New("knowledge_graph_read: seed required")
	}
	if depth < 0 {
		return DiscoverResult{}, errors.New("knowledge_graph_read: depth must be >= 0")
	}
	if depth > MaxDepth {
		return DiscoverResult{}, errors.New("knowledge_graph_read: depth exceeds MaxDepth (5)")
	}

	visited := map[string]struct{}{seed: {}}
	frontier := []string{seed}
	out := []string{seed}

	for level := 0; level < depth && len(frontier) > 0; level++ {
		next := make([]string, 0)
		for _, node := range frontier {
			ns, err := g.Neighbors(node)
			if err != nil {
				return DiscoverResult{}, err
			}
			for _, n := range ns {
				if _, seen := visited[n]; seen {
					continue
				}
				visited[n] = struct{}{}
				next = append(next, n)
				out = append(out, n)
			}
		}
		frontier = next
	}

	return DiscoverResult{Seed: seed, Depth: depth, Topics: out}, nil
}

// HeatmapCell is a single (topic, retention, tier) triple.
type HeatmapCell struct {
	Topic     string
	Retention float32
	Tier      Tier
}

// Heatmap is the per-gcid retention summary.
type Heatmap struct {
	GCID  string
	Cells []HeatmapCell
}

// BuildHeatmap converts a topic→retention map to a Heatmap with RAG tiers.
// Iteration order is non-deterministic (map) but tests sort by topic.
func BuildHeatmap(gcid string, scores map[string]float32) Heatmap {
	cells := make([]HeatmapCell, 0, len(scores))
	for topic, r := range scores {
		cells = append(cells, HeatmapCell{Topic: topic, Retention: r, Tier: tierFor(r)})
	}
	return Heatmap{GCID: gcid, Cells: cells}
}

func tierFor(r float32) Tier {
	switch {
	case r >= 0.70:
		return TierGreen
	case r >= 0.40:
		return TierAmber
	default:
		return TierRed
	}
}
