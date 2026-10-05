package knowledge_graph_read

// ring.go — CHO-2014 (map_sight / kg.read_map): stage-gated ring scoping over
// the learner's per-user concept map.
//
// The Companion's KG map reach WIDENS as it grows: an Awakened (stage ≤3)
// Companion sees one ring out from its resonant concept; a Structural (stage 4+)
// Companion sees two. RingScan is the distance-preserving BFS the kg.read_map
// tool runs over the per-user concept-graph adjacency (Graph port), seeded at
// the resonant concept. It mirrors Discover's cycle-guarded BFS but keeps each
// node's ring distance (Discover flattens it away, and the 1-ring vs 2-ring
// reach IS the point here).

import (
	"errors"
	"strings"
)

// structuralStage is the ADR-149 stage (4, "Structural") at which the map reach
// widens from one ring to two.
const structuralStage = 4

// RingsForStage maps a Companion's growth stage to its kg.read_map reach in
// rings: 1 ring at or below Awakened (stage 3), 2 rings once Structural
// (stage 4) or beyond.
func RingsForStage(stage int) int {
	if stage >= structuralStage {
		return 2
	}
	return 1
}

// RingNode is one node reached by RingScan with its ring distance from the seed.
type RingNode struct {
	ID   string
	Ring int // 0 = seed, 1 = adjacent, 2 = two-hop, …
}

// RingScan performs a bounded breadth-first traversal from seed out to maxRing
// rings (inclusive), returning each reached node with its ring distance. The
// seed is always included at ring 0; maxRing ≤ 0 returns just the seed. Cycles
// (and already-seen nodes on a nearer ring) are guarded by a visited set, so a
// node is reported exactly once at its SHORTEST distance.
func RingScan(g Graph, seed string, maxRing int) ([]RingNode, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, errors.New("knowledge_graph_read: seed required")
	}

	visited := map[string]struct{}{seed: {}}
	out := []RingNode{{ID: seed, Ring: 0}}
	frontier := []string{seed}

	for ring := 1; ring <= maxRing && len(frontier) > 0; ring++ {
		next := make([]string, 0)
		for _, node := range frontier {
			ns, err := g.Neighbors(node)
			if err != nil {
				return nil, err
			}
			for _, n := range ns {
				if _, seen := visited[n]; seen {
					continue
				}
				visited[n] = struct{}{}
				next = append(next, n)
				out = append(out, RingNode{ID: n, Ring: ring})
			}
		}
		frontier = next
	}

	return out, nil
}
