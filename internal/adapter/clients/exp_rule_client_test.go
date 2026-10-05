// exp_rule_client_test.go — CHO-2012 (ADR-218 D6) ExpRuleService client:
// resolution mapping, TTL caching (hot award path), error propagation
// (never cached), empty-target fail-loud.
package clients

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"
)

type fakeExpRuleService struct {
	resp  *identityv1.ResolveExpRuleResponse
	err   error
	calls int
}

func (f *fakeExpRuleService) ResolveExpRule(_ context.Context, _ *identityv1.ResolveExpRuleRequest, _ ...grpc.CallOption) (*identityv1.ResolveExpRuleResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestExpRuleClient_ResolvesAndCaches(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	fake := &fakeExpRuleService{resp: &identityv1.ResolveExpRuleResponse{
		SourceCode: "atom_session", ExpValue: 3, DailyCap: 30, Enabled: true,
		ResolutionSource: "catalogue",
	}}
	c := newExpRuleClient(fake, time.Second, time.Minute, func() time.Time { return now })

	rule, err := c.ResolveExpRule(context.Background(), "tenant-1", "atom_session")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rule.Value != 3 || rule.DailyCap != 30 || !rule.Enabled {
		t.Errorf("rule = %+v, want {3 30 true}", rule)
	}
	// Second call within the TTL hits the cache — no extra RPC.
	if _, err := c.ResolveExpRule(context.Background(), "tenant-1", "atom_session"); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("RPC calls = %d, want 1 (second read cached)", fake.calls)
	}
	// A different (tenant, source) key misses the cache.
	if _, err := c.ResolveExpRule(context.Background(), "tenant-2", "atom_session"); err != nil {
		t.Fatalf("tenant-2 resolve: %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("RPC calls = %d, want 2 (per-tenant cache key)", fake.calls)
	}
}

func TestExpRuleClient_TTLExpiry(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	clockNow := now
	fake := &fakeExpRuleService{resp: &identityv1.ResolveExpRuleResponse{
		SourceCode: "social_share", ExpValue: 8, DailyCap: 16, Enabled: true,
	}}
	c := newExpRuleClient(fake, time.Second, time.Minute, func() time.Time { return clockNow })

	if _, err := c.ResolveExpRule(context.Background(), "t", "social_share"); err != nil {
		t.Fatal(err)
	}
	clockNow = now.Add(2 * time.Minute) // past the TTL
	if _, err := c.ResolveExpRule(context.Background(), "t", "social_share"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Errorf("RPC calls = %d, want 2 (TTL expired)", fake.calls)
	}
}

func TestExpRuleClient_ErrorPropagatesAndNotCached(t *testing.T) {
	fake := &fakeExpRuleService{err: errors.New("identity down")}
	c := newExpRuleClient(fake, time.Second, time.Minute, nil)

	if _, err := c.ResolveExpRule(context.Background(), "t", "atom_session"); err == nil {
		t.Fatal("expected transport error to propagate (growth falls back loudly)")
	}
	// Errors are not cached: the next call retries the RPC.
	fake.err = nil
	fake.resp = &identityv1.ResolveExpRuleResponse{SourceCode: "atom_session", ExpValue: 3, DailyCap: 30, Enabled: true}
	rule, err := c.ResolveExpRule(context.Background(), "t", "atom_session")
	if err != nil {
		t.Fatalf("post-recovery resolve: %v", err)
	}
	if rule.Value != 3 {
		t.Errorf("rule.Value = %d, want 3", rule.Value)
	}
	if fake.calls != 2 {
		t.Errorf("RPC calls = %d, want 2 (error not cached)", fake.calls)
	}
}

func TestExpRuleClient_EmptyTargetFailsLoud(t *testing.T) {
	if _, err := NewExpRuleClient("   ", time.Second); !errors.Is(err, ErrExpRuleEmptyTarget) {
		t.Fatalf("err = %v, want ErrExpRuleEmptyTarget", err)
	}
}

func TestExpRuleClient_DisabledSourcePassesThrough(t *testing.T) {
	fake := &fakeExpRuleService{resp: &identityv1.ResolveExpRuleResponse{
		SourceCode: "social_share", ExpValue: 8, DailyCap: 16, Enabled: false,
	}}
	c := newExpRuleClient(fake, time.Second, time.Minute, nil)
	rule, err := c.ResolveExpRule(context.Background(), "t", "social_share")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rule.Enabled {
		t.Error("Enabled = true, want false (editor-disabled source skips the award)")
	}
}
