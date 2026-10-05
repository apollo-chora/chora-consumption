package subscribers

import "testing"

func TestParseContentItems(t *testing.T) {
	raw := []any{
		map[string]any{"item_id": "i1", "kind": "atom", "ref": "r1", "title": "t1", "position": float64(0)},
		"not-a-map",
		map[string]any{"item_id": "i2", "kind": "video", "ref": "https://x/v", "title": "t2", "position": float64(1)},
	}
	got := parseContentItems(raw)
	if len(got) != 2 || got[0].ItemID != "i1" || got[1].Position != 1 {
		t.Fatalf("parse mismatch: %+v", got)
	}
	if parseContentItems("nope") != nil {
		t.Fatalf("non-array should return nil")
	}
}
