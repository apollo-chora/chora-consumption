// memory_test.go — domain unit tests for the F4 per-Companion RAG memory port.
//
// The CompanionMemory + Embedder ports are pure interfaces (no logic to test);
// the only domain helper is MemoryScopeKey, which mints the canonical
// per-Companion scope_key ('companion:{companion_id}') mirroring the agent's
// app_name prefix. These tests pin that contract — the pg adapter + the chat
// handler both depend on it producing exactly this shape.
package companion

import "testing"

func TestMemoryScopeKey(t *testing.T) {
	cases := []struct {
		name        string
		companionID string
		want        string
	}{
		{
			name:        "uuid companion id",
			companionID: "01970000-0000-7000-a000-000000000001",
			want:        "companion:01970000-0000-7000-a000-000000000001",
		},
		{
			name:        "short id",
			companionID: "fam-42",
			want:        "companion:fam-42",
		},
		{
			name:        "empty id still prefixed",
			companionID: "",
			want:        "companion:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MemoryScopeKey(tc.companionID); got != tc.want {
				t.Errorf("MemoryScopeKey(%q) = %q; want %q", tc.companionID, got, tc.want)
			}
		})
	}
}
