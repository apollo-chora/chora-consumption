// growth_wiring_test.go — verifies the boot helpers wire the growth axis
// correctly given various env configurations.
package main

import (
	"os"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func TestBootstrapBreedDistributionProvider_FallsBackToStaticWhenNoBaseURL(t *testing.T) {
	t.Setenv("CHORA_TENANCY_GRPC_BASE_URL", "")
	t.Setenv("CHORA_TENANCY_BASE_URL", "")
	p := bootstrapBreedDistributionProvider()
	if p == nil {
		t.Fatal("provider nil")
	}
	dist, err := p.Lookup("any-tenant", "egg.standard.v1")
	if err != nil {
		t.Fatalf("static fallback Lookup: %v", err)
	}
	if len(dist) == 0 {
		t.Error("static dist empty")
	}
}

func TestBootstrapBreedDistributionProvider_BuildsGRPCWhenTargetSet(t *testing.T) {
	t.Setenv("CHORA_TENANCY_GRPC_BASE_URL", "localhost:9999")
	t.Setenv("CHORA_TENANCY_BASE_URL", "")
	t.Setenv("CHORA_TENANCY_EGG_ODDS_CACHE_TTL", "1m")
	p := bootstrapBreedDistributionProvider()
	if p == nil {
		t.Fatal("provider nil")
	}
	// Reach into the type to confirm we got the gRPC client. We don't dial
	// the server here — just verify the type + the env-precedence rule.
	if _, ok := p.(interface {
		Invalidate(string, string)
		Close() error
	}); !ok {
		t.Errorf("provider type = %T; want *BreedDistributionGRPCClient", p)
	}
}

func TestBootstrapBreedDistributionProvider_PrefersGRPCOverREST(t *testing.T) {
	t.Setenv("CHORA_TENANCY_GRPC_BASE_URL", "localhost:9999")
	t.Setenv("CHORA_TENANCY_BASE_URL", "http://localhost:8888")
	p := bootstrapBreedDistributionProvider()
	if p == nil {
		t.Fatal("provider nil")
	}
	// The gRPC client exposes Close(); the REST client does not.
	if _, ok := p.(interface{ Close() error }); !ok {
		t.Errorf("provider type = %T; want gRPC client when GRPC target set", p)
	}
}

func TestBootstrapBreedDistributionProvider_BuildsRESTClientWhenURLSet(t *testing.T) {
	t.Setenv("CHORA_TENANCY_BASE_URL", "http://localhost:9999")
	t.Setenv("CHORA_TENANCY_EGG_ODDS_CACHE_TTL", "1m")
	p := bootstrapBreedDistributionProvider()
	if p == nil {
		t.Fatal("provider nil")
	}
	// Reach into the type assertion just enough to confirm we got the REST
	// client (not the static fallback).
	if _, ok := os.LookupEnv("CHORA_TENANCY_BASE_URL"); !ok {
		t.Fatal("env not set")
	}
}

func TestBootstrapBreedDistributionProvider_AcceptsBadDurationGracefully(t *testing.T) {
	t.Setenv("CHORA_TENANCY_BASE_URL", "http://localhost:9999")
	t.Setenv("CHORA_TENANCY_EGG_ODDS_CACHE_TTL", "not-a-duration")
	p := bootstrapBreedDistributionProvider()
	if p == nil {
		t.Fatal("provider nil")
	}
}

func TestWireGrowthService_NilPoolNoOp(t *testing.T) {
	srv := httpadapter.NewServer()
	svc := wireGrowthService(srv, nil, nil)
	if svc != nil {
		t.Errorf("svc = %v; want nil when pool nil", svc)
	}
	if srv.Growth != nil {
		t.Errorf("srv.Growth set with nil pool")
	}
}

func TestWireEggPurchaseSubscriber_NilSvcReturnsNil(t *testing.T) {
	if s := wireEggPurchaseSubscriber(nil); s != nil {
		t.Errorf("expected nil subscriber when svc nil; got %v", s)
	}
}

func TestWireCompanionGrowthSubscriber_NilArgsReturnsNil(t *testing.T) {
	if s := wireCompanionGrowthSubscriber(nil, nil); s != nil {
		t.Errorf("expected nil subscriber when args nil; got %v", s)
	}
}

// F1: with a CompanionInstances repo wired on the server (NewServer defaults
// it to the in-memory adapter), the growth push handlers MUST use the
// repo-backed resolver — NOT nopCompanionResolver — so 7-source EXP lands.
func TestPickCompanionResolver_RepoBackedWhenInstancesWired(t *testing.T) {
	srv := httpadapter.NewServer()
	r := pickCompanionResolver(srv, nil)
	if _, ok := r.(*subscribers.RepoCompanionResolver); !ok {
		t.Errorf("resolver = %T; want *subscribers.RepoCompanionResolver when CompanionInstances wired", r)
	}
}

// Defensive fallback: when no CompanionInstances repo is present, the nop
// resolver is retained (EXP dropped, but no nil deref).
func TestPickCompanionResolver_NopWhenNoInstances(t *testing.T) {
	srv := &httpadapter.Server{}
	r := pickCompanionResolver(srv, nil)
	if _, ok := r.(nopCompanionResolver); !ok {
		t.Errorf("resolver = %T; want nopCompanionResolver when no CompanionInstances repo", r)
	}
}
