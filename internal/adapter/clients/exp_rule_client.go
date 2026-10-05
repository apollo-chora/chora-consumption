// exp_rule_client.go — chora-identity ExpRuleService gRPC client adapter
// (CHO-2012, ADR-218 D6: one EXP economy on the ExpRuleResolver).
//
// Hexagonal port: implements growth.ExpRuler so the growth award path
// resolves (tenant, source) → {value, daily cap, enabled} without knowing
// the transport. The resolver runs identity-side (precedence ladder: tenant
// override → platform plan → exp_source_def catalogue, parity-seeded by
// identity migration 0030); on ANY adapter error the growth service falls
// back to the in-code parity map and fires OnExpRuleFallback (loud, never
// silent — the documented ADR-218 D6 fallback).
//
// Dial shape mirrors mana_client.go: insecure transport credentials (Cloud
// Service Mesh injects mTLS), mesh-DNS target from env
// CHORA_IDENTITY_EXP_RULES_GRPC_URL (typically the SAME identity :9090
// endpoint as the mana client — a separate variable keeps the seam
// independently switchable).
//
// Cache: EXP rules change only via rare H+ editor actions but resolution
// sits on the hot award path (every atom answer) — a short in-process TTL
// cache (default 60s) bounds identity load without meaningfully delaying
// editor changes. Errors are NEVER cached.
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ErrExpRuleEmptyTarget fails construction loudly when the target is empty
// (feedback_no_inline_config — never a silent no-op resolver).
var ErrExpRuleEmptyTarget = errors.New("exp_rule_client: target empty (set CHORA_IDENTITY_EXP_RULES_GRPC_URL)")

const (
	expRuleDefaultTimeout  = 3 * time.Second
	expRuleDefaultCacheTTL = 60 * time.Second
)

// expRuleServiceClient is the minimal slice of the generated client; tests
// inject a fake.
type expRuleServiceClient interface {
	ResolveExpRule(ctx context.Context, in *identityv1.ResolveExpRuleRequest, opts ...grpc.CallOption) (*identityv1.ResolveExpRuleResponse, error)
}

type expRuleCacheEntry struct {
	rule      growth.ExpRule
	expiresAt time.Time
}

// ExpRuleClient calls chora-identity ExpRuleService over gRPC with a TTL
// cache. Implements growth.ExpRuler.
type ExpRuleClient struct {
	client  expRuleServiceClient
	timeout time.Duration
	ttl     time.Duration
	clock   func() time.Time

	mu    sync.RWMutex
	cache map[string]expRuleCacheEntry
}

// NewExpRuleClient dials the identity gRPC target (mesh mTLS via sidecar).
func NewExpRuleClient(target string, timeout time.Duration) (*ExpRuleClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrExpRuleEmptyTarget
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("exp_rule_client: dial %q: %w", target, err)
	}
	return newExpRuleClient(identityv1.NewExpRuleServiceClient(conn), timeout, expRuleDefaultCacheTTL, time.Now), nil
}

// newExpRuleClient is the testable constructor.
func newExpRuleClient(c expRuleServiceClient, timeout, ttl time.Duration, clock func() time.Time) *ExpRuleClient {
	if timeout <= 0 {
		timeout = expRuleDefaultTimeout
	}
	if ttl <= 0 {
		ttl = expRuleDefaultCacheTTL
	}
	if clock == nil {
		clock = time.Now
	}
	return &ExpRuleClient{
		client:  c,
		timeout: timeout,
		ttl:     ttl,
		clock:   clock,
		cache:   map[string]expRuleCacheEntry{},
	}
}

// ResolveExpRule implements growth.ExpRuler.
func (c *ExpRuleClient) ResolveExpRule(ctx context.Context, tenantID, source string) (growth.ExpRule, error) {
	if c == nil || c.client == nil {
		return growth.ExpRule{}, errors.New("exp_rule_client: not configured")
	}
	key := tenantID + "|" + source
	now := c.clock()

	c.mu.RLock()
	if entry, ok := c.cache[key]; ok && now.Before(entry.expiresAt) {
		c.mu.RUnlock()
		return entry.rule, nil
	}
	c.mu.RUnlock()

	rpcCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.client.ResolveExpRule(rpcCtx, &identityv1.ResolveExpRuleRequest{
		SourceCode: source,
		TenantId:   tenantID,
	})
	if err != nil {
		return growth.ExpRule{}, fmt.Errorf("exp_rule_client: resolve %q: %w", source, err)
	}
	rule := growth.ExpRule{
		Value:    int(resp.GetExpValue()),
		DailyCap: int(resp.GetDailyCap()),
		Enabled:  resp.GetEnabled(),
	}
	c.mu.Lock()
	c.cache[key] = expRuleCacheEntry{rule: rule, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return rule, nil
}

// Compile-time port guarantee.
var _ growth.ExpRuler = (*ExpRuleClient)(nil)
