// ADR-197 P3 (CHO-2368) - the consumption half of the companion's prompt
// override lane. The companion's ADK session is created HERE (not in the
// orchestrator), so consumption resolves the active override from the
// ai-kernel prompt registry's read route and injects the ADR-197 session-state
// trio at session build (see CompanionEngineClient.injectPromptOverrides).
//
// Availability posture: the registry is an ENHANCEMENT source, not a
// dependency - every failure path is fail-OPEN (the agent renders its
// embedded baseline, exactly today's behaviour). The engine client logs the
// WARN; this adapter just surfaces typed errors.
package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PromptOverridesResolution is the resolve verdict for one (tenant, agent).
//
// OverridesJSON is the raw JSON object (segment_id -> body) EXACTLY as the
// session-state contract wants it ("" when the resolution is embedded);
// Version/Source mirror the route's fields (embedded resolutions carry the
// agent's embedded version, e.g. "v1", so stamps agree with reality).
type PromptOverridesResolution struct {
	OverridesJSON string
	Version       string
	Source        string
}

// PromptOverridesResolver resolves the active prompt override for a tenant.
type PromptOverridesResolver interface {
	ResolvePromptOverrides(ctx context.Context, tenantID string) (PromptOverridesResolution, error)
}

// PromptRegistryHTTPResolver resolves via the orchestrator's read route
// GET {base}/v1/prompt-registry/agents/{agent}/resolve?tenant_id=... with a
// per-tenant TTL cache (session builds are frequent, the registry changes
// rarely; activation propagates within the TTL).
type PromptRegistryHTTPResolver struct {
	baseURL string
	agentID string
	ttl     time.Duration
	client  *http.Client

	mu    sync.Mutex
	cache map[string]cachedResolution
}

type cachedResolution struct {
	res     PromptOverridesResolution
	expires time.Time
}

// NewPromptRegistryHTTPResolver builds the adapter. httpClient may be nil
// (http.DefaultClient with a sane timeout is used).
func NewPromptRegistryHTTPResolver(
	baseURL, agentID string, ttl time.Duration, httpClient *http.Client,
) *PromptRegistryHTTPResolver {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &PromptRegistryHTTPResolver{
		baseURL: strings.TrimRight(baseURL, "/"),
		agentID: agentID,
		ttl:     ttl,
		client:  httpClient,
		cache:   make(map[string]cachedResolution),
	}
}

// resolveRouteResponse mirrors the orchestrator's PromptResolveResponse.
type resolveRouteResponse struct {
	AgentID  string            `json:"agent_id"`
	TenantID string            `json:"tenant_id"`
	Version  *string           `json:"version"`
	Source   string            `json:"source"`
	Segments map[string]string `json:"segments"`
}

// ResolvePromptOverrides satisfies PromptOverridesResolver.
func (r *PromptRegistryHTTPResolver) ResolvePromptOverrides(
	ctx context.Context, tenantID string,
) (PromptOverridesResolution, error) {
	now := time.Now()
	r.mu.Lock()
	if hit, ok := r.cache[tenantID]; ok && now.Before(hit.expires) {
		r.mu.Unlock()
		return hit.res, nil
	}
	r.mu.Unlock()

	u := fmt.Sprintf("%s/v1/prompt-registry/agents/%s/resolve", r.baseURL, url.PathEscape(r.agentID))
	if tenantID != "" {
		u += "?tenant_id=" + url.QueryEscape(tenantID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return PromptOverridesResolution{}, fmt.Errorf("prompt_overrides_resolver: build request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return PromptOverridesResolution{}, fmt.Errorf("prompt_overrides_resolver: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return PromptOverridesResolution{}, fmt.Errorf(
			"prompt_overrides_resolver: resolve %s: HTTP %d", r.agentID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PromptOverridesResolution{}, fmt.Errorf("prompt_overrides_resolver: read body: %w", err)
	}
	var parsed resolveRouteResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return PromptOverridesResolution{}, fmt.Errorf("prompt_overrides_resolver: parse body: %w", err)
	}

	res := PromptOverridesResolution{Source: parsed.Source}
	if parsed.Version != nil {
		res.Version = *parsed.Version
	}
	if len(parsed.Segments) > 0 {
		raw, err := json.Marshal(parsed.Segments)
		if err != nil {
			return PromptOverridesResolution{}, fmt.Errorf("prompt_overrides_resolver: marshal segments: %w", err)
		}
		res.OverridesJSON = string(raw)
	}

	r.mu.Lock()
	r.cache[tenantID] = cachedResolution{res: res, expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return res, nil
}

// Compile-time check.
var _ PromptOverridesResolver = (*PromptRegistryHTTPResolver)(nil)
