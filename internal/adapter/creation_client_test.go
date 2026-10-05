// creation_client_test — HTTP client for the chora-creation
// knowledge-graph snapshot. Tests use httptest to stub responses.
//
// No inline config: production reads CHORA_CREATION_BASE_URL env var
// (per feedback_no_inline_config). Tests inject a base URL.
package adapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCreationClient_Neighbors fetches adjacency for a topic via HTTP.
func TestCreationClient_Neighbors(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/knowledge-graph/neighbors" {
			http.NotFound(w, r)
			return
		}
		topic := r.URL.Query().Get("topic")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"topic":     topic,
			"neighbors": []string{"linear", "quadratic"},
		})
	}))
	defer stub.Close()

	c := NewCreationClient(stub.URL, 5*time.Second)
	neighbors, err := c.Neighbors("algebra")
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(neighbors) != 2 {
		t.Errorf("len(neighbors) = %d, want 2", len(neighbors))
	}
}

// TestCreationClient_Neighbors_HTTPError surfaces non-2xx as error.
func TestCreationClient_Neighbors_HTTPError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer stub.Close()

	c := NewCreationClient(stub.URL, 5*time.Second)
	if _, err := c.Neighbors("algebra"); err == nil {
		t.Error("expected error on 500")
	}
}

// TestCreationClient_RequiresBaseURL — empty base URL fails fast (no
// inline config; misconfiguration is loud).
func TestCreationClient_RequiresBaseURL(t *testing.T) {
	c := NewCreationClient("", 5*time.Second)
	if _, err := c.Neighbors("algebra"); err == nil {
		t.Error("expected error when base URL empty")
	}
}
