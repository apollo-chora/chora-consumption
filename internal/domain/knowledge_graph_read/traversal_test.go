// Package knowledge_graph_read — discovery-mode read traversal over a
// snapshot of the chora-creation knowledge graph.
//
// Discovery mode is the curiosity-driven traversal anchor in the canonical
// architecture (Tier 1 D1 — bimodal delivery: Straight-Up + Graph-Based
// Discovery). The fog-of-war progressive reveal (Comic Ch7 P15) is FUTURE.
// MVP behaviour: BFS adjacency from a seed topic up to a max depth.
//
// The traversal works on a Graph adapter port — production wires the
// chora-creation HTTP client; tests inject an in-memory map.
package knowledge_graph_read

import (
	"reflect"
	"sort"
	"testing"
)

// fakeGraph is a deterministic in-memory adjacency snapshot.
type fakeGraph struct {
	adj map[string][]string
}

func (g *fakeGraph) Neighbors(topic string) ([]string, error) {
	return g.adj[topic], nil
}

func TestDiscover_DepthZero_ReturnsSeedOnly(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{"algebra": {"linear", "quadratic"}}}
	got, err := Discover(g, "algebra", 0)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !reflect.DeepEqual(got.Topics, []string{"algebra"}) {
		t.Errorf("Topics = %v, want [algebra]", got.Topics)
	}
	if got.Depth != 0 {
		t.Errorf("Depth = %d, want 0", got.Depth)
	}
}

func TestDiscover_Depth1_IncludesNeighbors(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{
		"algebra":   {"linear", "quadratic"},
		"linear":    {"algebra", "matrix"},
		"quadratic": {"algebra"},
	}}
	got, err := Discover(g, "algebra", 1)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	sort.Strings(got.Topics)
	want := []string{"algebra", "linear", "quadratic"}
	if !reflect.DeepEqual(got.Topics, want) {
		t.Errorf("Topics = %v, want %v", got.Topics, want)
	}
}

// TestDiscover_Depth2_BFSExpands traverses two levels.
func TestDiscover_Depth2_BFSExpands(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{
		"algebra":    {"linear"},
		"linear":     {"matrix"},
		"matrix":     {"eigenvalue"},
		"eigenvalue": {},
	}}
	got, err := Discover(g, "algebra", 2)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	sort.Strings(got.Topics)
	want := []string{"algebra", "linear", "matrix"}
	if !reflect.DeepEqual(got.Topics, want) {
		t.Errorf("Topics = %v, want %v", got.Topics, want)
	}
}

// TestDiscover_Cycle_NoInfiniteLoop guards BFS against cycles.
func TestDiscover_Cycle_NoInfiniteLoop(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{
		"a": {"b"},
		"b": {"a", "c"},
		"c": {"b"},
	}}
	got, err := Discover(g, "a", 5)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got.Topics) != 3 {
		t.Errorf("expected 3 unique topics, got %d (%v)", len(got.Topics), got.Topics)
	}
}

// TestDiscover_RejectsNegativeDepth.
func TestDiscover_RejectsNegativeDepth(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{}}
	if _, err := Discover(g, "a", -1); err == nil {
		t.Error("expected error for negative depth")
	}
}

// TestDiscover_RejectsEmptySeed.
func TestDiscover_RejectsEmptySeed(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{}}
	if _, err := Discover(g, "", 1); err == nil {
		t.Error("expected error for empty seed")
	}
}

// TestDiscover_CapDepth enforces an upper depth bound (5) per
// consumption-admin.yaml KG inspection contract.
func TestDiscover_CapDepth(t *testing.T) {
	g := &fakeGraph{adj: map[string][]string{}}
	if _, err := Discover(g, "a", 999); err == nil {
		t.Error("expected error for depth > MaxDepth")
	}
}

// TestHeatmap mocks per-topic retention scores.
func TestHeatmap(t *testing.T) {
	scores := map[string]float32{
		"algebra":  0.8,
		"calculus": 0.4,
	}
	hm := BuildHeatmap(testGCID, scores)
	if hm.GCID != testGCID {
		t.Errorf("GCID = %q", hm.GCID)
	}
	if len(hm.Cells) != 2 {
		t.Errorf("Cells len = %d, want 2", len(hm.Cells))
	}
	// Verify color-coding helper assigns RAG correctly.
	for _, c := range hm.Cells {
		switch c.Topic {
		case "algebra":
			if c.Tier != TierGreen {
				t.Errorf("algebra tier = %s, want %s", c.Tier, TierGreen)
			}
		case "calculus":
			if c.Tier != TierAmber {
				t.Errorf("calculus tier = %s, want %s", c.Tier, TierAmber)
			}
		}
	}
}

func TestHeatmap_RedTier(t *testing.T) {
	hm := BuildHeatmap(testGCID, map[string]float32{"hard-topic": 0.2})
	if len(hm.Cells) != 1 || hm.Cells[0].Tier != TierRed {
		t.Errorf("expected red tier, got %v", hm.Cells)
	}
}

const testGCID = "01970000-0000-7000-9000-000000000001"
