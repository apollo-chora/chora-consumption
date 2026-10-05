package knowledge_graph_read

import "testing"

// mapGraph is a static adjacency fake for the Graph port.
type mapGraph map[string][]string

func (m mapGraph) Neighbors(node string) ([]string, error) { return m[node], nil }

func TestRingsForStage(t *testing.T) {
	// 1 ring at or below Awakened (stage 3); 2 rings once Structural (stage 4+).
	cases := []struct {
		stage, want int
	}{
		{0, 1}, {1, 1}, {2, 1}, {3, 1}, {4, 2}, {5, 2}, {6, 2},
	}
	for _, c := range cases {
		if got := RingsForStage(c.stage); got != c.want {
			t.Fatalf("RingsForStage(%d) = %d want %d", c.stage, got, c.want)
		}
	}
}

func TestRingScan_TracksRingDistanceAndBounds(t *testing.T) {
	// centre → a,b (ring 1); a → c (ring 2); c → d (ring 3, beyond maxRing=2).
	// The back-edges (a→centre, c→a) exercise the cycle guard.
	g := mapGraph{
		"centre": {"a", "b"},
		"a":      {"c", "centre"},
		"b":      {},
		"c":      {"d", "a"},
		"d":      {},
	}
	got, err := RingScan(g, "centre", 2)
	if err != nil {
		t.Fatalf("RingScan: %v", err)
	}
	want := map[string]int{"centre": 0, "a": 1, "b": 1, "c": 2} // d excluded
	if len(got) != len(want) {
		t.Fatalf("RingScan returned %d nodes, want %d: %+v", len(got), len(want), got)
	}
	for _, rn := range got {
		w, ok := want[rn.ID]
		if !ok {
			t.Fatalf("unexpected node %q (ring %d)", rn.ID, rn.Ring)
		}
		if w != rn.Ring {
			t.Fatalf("node %q ring = %d want %d", rn.ID, rn.Ring, w)
		}
	}
}

func TestRingScan_OneRingStopsAtAdjacent(t *testing.T) {
	g := mapGraph{"centre": {"a"}, "a": {"c"}, "c": {}}
	got, err := RingScan(g, "centre", 1)
	if err != nil {
		t.Fatalf("RingScan: %v", err)
	}
	want := map[string]int{"centre": 0, "a": 1} // c (ring 2) excluded at 1 ring
	if len(got) != len(want) {
		t.Fatalf("1-ring scan = %+v want %v", got, want)
	}
}

func TestRingScan_SeedOnlyAtRingZero(t *testing.T) {
	got, err := RingScan(mapGraph{"centre": {"a"}}, "centre", 0)
	if err != nil {
		t.Fatalf("RingScan: %v", err)
	}
	if len(got) != 1 || got[0].ID != "centre" || got[0].Ring != 0 {
		t.Fatalf("maxRing 0 must return only the seed at ring 0, got %+v", got)
	}
}

func TestRingScan_EmptySeedErrors(t *testing.T) {
	if _, err := RingScan(mapGraph{}, "   ", 2); err == nil {
		t.Fatal("empty seed must error")
	}
}
