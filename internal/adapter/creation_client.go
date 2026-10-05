// Package adapter — HTTP client to chora-creation knowledge-graph snapshot.
//
// This is the production adapter for the
// `internal/domain/knowledge_graph_read.Graph` port. The base URL is
// supplied by env var `CHORA_CREATION_BASE_URL` (per
// `feedback_no_inline_config` — no inline URLs / configs / stubs in source).
//
// Hexagonal note: domain code never imports this package directly; the HTTP
// handler injects the Graph port at wiring time.
//
// Cross-DB rule: chora-consumption never queries chora_creation directly;
// this client speaks REST to chora-creation's admin/read API. See
// .claude/rules/ddd-enforcement.md ("Cross-database queries forbidden").
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CreationClient calls the chora-creation knowledge-graph endpoints.
type CreationClient struct {
	baseURL string
	hc      *http.Client
}

// NewCreationClient constructs a client against the supplied base URL.
// Production wires base URL from env var CHORA_CREATION_BASE_URL.
func NewCreationClient(baseURL string, timeout time.Duration) *CreationClient {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &CreationClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: timeout},
	}
}

// neighborsResp matches the chora-creation response payload.
type neighborsResp struct {
	Topic     string   `json:"topic"`
	Neighbors []string `json:"neighbors"`
}

// Neighbors fetches adjacency for a topic. Returns ErrEmptyBaseURL when
// the client was constructed with an empty base URL (misconfiguration —
// fail loud per feedback_no_inline_config).
func (c *CreationClient) Neighbors(topic string) ([]string, error) {
	if c.baseURL == "" {
		return nil, errors.New("creation_client: base URL empty (set CHORA_CREATION_BASE_URL)")
	}

	q := url.Values{}
	q.Set("topic", topic)
	endpoint := c.baseURL + "/api/v1/knowledge-graph/neighbors?" + q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), c.hc.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creation_client: build request: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("creation_client: HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("creation_client: status=%d body=%s", resp.StatusCode, string(body))
	}

	var nr neighborsResp
	if err := json.NewDecoder(resp.Body).Decode(&nr); err != nil {
		return nil, fmt.Errorf("creation_client: decode: %w", err)
	}
	return nr.Neighbors, nil
}
