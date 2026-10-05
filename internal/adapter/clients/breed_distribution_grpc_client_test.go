// breed_distribution_grpc_client_test.go — TDD coverage for the gRPC client
// adapter that swaps the REST BreedDistributionClient with a typed RPC to
// chora-tenancy's CompanionEgg.PreviewEggOdds.
//
// Coverage classes:
//   - Happy path: round-trips a distribution + caches the result
//   - NotFound (NOT_FOUND status code) → growth.ErrSkuNotFound
//   - PermissionDenied surfaces as a generic error (not ErrSkuNotFound)
//   - Cache hit avoids the wire call
//   - Cache miss after TTL expiry triggers another fetch
//   - Invalidate clears entries
//   - Empty sku → growth.ErrInvalidArguments
//   - Upstream invalid distribution → error
//
// Uses bufconn for an in-process gRPC server stub so tests stay fast +
// hermetic. The stub implements tenancyv1.CompanionEggServer.
package clients_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	tenancyv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/tenancy/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// stubCompanionEgg implements tenancyv1.CompanionEggServer for testing.
type stubCompanionEgg struct {
	tenancyv1.UnimplementedCompanionEggServer
	resp       *tenancyv1.PreviewEggOddsResponse
	err        error
	calls      int64
	lastSKU    string
	lastTenant string
}

func (s *stubCompanionEgg) PreviewEggOdds(_ context.Context, req *tenancyv1.PreviewEggOddsRequest) (*tenancyv1.PreviewEggOddsResponse, error) {
	atomic.AddInt64(&s.calls, 1)
	s.lastSKU = req.GetEggSku()
	s.lastTenant = req.GetTenantId()
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

// newBufconnDialer spins up an in-process gRPC server and returns a
// grpc.DialOption that routes connections through the bufconn listener
// + cleanup func. Returned alongside the stub so tests can mutate its
// response per case.
func newBufconnDialer(t *testing.T, stub *stubCompanionEgg) (grpc.DialOption, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	tenancyv1.RegisterCompanionEggServer(srv, stub)
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn server stopped: %v", err)
		}
	}()
	dialer := grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	})
	cleanup := func() {
		srv.GracefulStop()
		_ = lis.Close()
	}
	return dialer, cleanup
}

func mkHappyResponse() *tenancyv1.PreviewEggOddsResponse {
	return &tenancyv1.PreviewEggOddsResponse{
		EggSku: "egg.standard.v1",
		Odds: []*tenancyv1.BreedOdds{
			{Species: "owl", Probability: 25, Rarity: "common"},
			{Species: "fox", Probability: 25, Rarity: "common"},
			{Species: "penguin", Probability: 20, Rarity: "common"},
			{Species: "dragon", Probability: 22.5, Rarity: "uncommon"},
			{Species: "phoenix", Probability: 7.5, Rarity: "legendary"},
		},
		TotalWeight:           100,
		DistributionUpdatedAt: timestamppb.New(time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)),
	}
}

func TestBreedDistributionGRPCClient_Lookup_HappyPath(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{resp: mkHappyResponse()}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, err := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewBreedDistributionGRPCClient: %v", err)
	}
	defer c.Close()

	dist, err := c.Lookup("tenant-A", "egg.standard.v1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got := len(dist); got != 5 {
		t.Fatalf("len = %d; want 5", got)
	}
	if dist[0].Species != "owl" || dist[0].Probability != 25 {
		t.Errorf("dist[0] = %+v; want owl 25", dist[0])
	}
	if stub.lastSKU != "egg.standard.v1" {
		t.Errorf("stub.lastSKU = %q; want egg.standard.v1", stub.lastSKU)
	}
	if stub.lastTenant != "tenant-A" {
		t.Errorf("stub.lastTenant = %q; want tenant-A", stub.lastTenant)
	}
}

func TestBreedDistributionGRPCClient_Lookup_CacheHit(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{resp: mkHappyResponse()}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	for i := 0; i < 3; i++ {
		if _, err := c.Lookup("tenant-A", "egg.standard.v1"); err != nil {
			t.Fatalf("Lookup #%d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&stub.calls); got != 1 {
		t.Errorf("stub.calls = %d; want 1 (cached)", got)
	}
}

func TestBreedDistributionGRPCClient_Lookup_CacheTTLExpiry(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{resp: mkHappyResponse()}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    20 * time.Millisecond,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	if _, err := c.Lookup("tenant-A", "egg.standard.v1"); err != nil {
		t.Fatalf("Lookup #1: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := c.Lookup("tenant-A", "egg.standard.v1"); err != nil {
		t.Fatalf("Lookup #2: %v", err)
	}
	if got := atomic.LoadInt64(&stub.calls); got != 2 {
		t.Errorf("stub.calls = %d; want 2 (TTL expired)", got)
	}
}

func TestBreedDistributionGRPCClient_Lookup_NotFound(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{err: status.Error(codes.NotFound, "missing")}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	_, err := c.Lookup("tenant-A", "egg.missing.v1")
	if !errors.Is(err, growth.ErrSkuNotFound) {
		t.Errorf("err = %v; want growth.ErrSkuNotFound", err)
	}
}

func TestBreedDistributionGRPCClient_Lookup_PermissionDenied(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{err: status.Error(codes.PermissionDenied, "out of scope")}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	_, err := c.Lookup("tenant-A", "egg.test.v1")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, growth.ErrSkuNotFound) {
		t.Errorf("err is ErrSkuNotFound; want generic error")
	}
}

func TestBreedDistributionGRPCClient_Lookup_EmptySku(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{resp: mkHappyResponse()}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	_, err := c.Lookup("tenant-A", "")
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want growth.ErrInvalidArguments", err)
	}
	if got := atomic.LoadInt64(&stub.calls); got != 0 {
		t.Errorf("stub.calls = %d; want 0 (no upstream call on validation fail)", got)
	}
}

func TestBreedDistributionGRPCClient_Lookup_InvalidDistribution(t *testing.T) {
	t.Parallel()
	// Server returns 50 total weight — fails ValidateBreedDistribution.
	resp := &tenancyv1.PreviewEggOddsResponse{
		EggSku: "egg.bad.v1",
		Odds: []*tenancyv1.BreedOdds{
			{Species: "owl", Probability: 50, Rarity: "common"},
		},
		TotalWeight: 50,
	}
	stub := &stubCompanionEgg{resp: resp}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	_, err := c.Lookup("tenant-A", "egg.bad.v1")
	if err == nil {
		t.Fatal("expected error on invalid distribution")
	}
}

func TestBreedDistributionGRPCClient_Invalidate(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{resp: mkHappyResponse()}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    5 * time.Minute,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	// Prime cache.
	_, _ = c.Lookup("tenant-A", "egg.standard.v1")
	c.Invalidate("tenant-A", "egg.standard.v1")
	_, _ = c.Lookup("tenant-A", "egg.standard.v1")
	if got := atomic.LoadInt64(&stub.calls); got != 2 {
		t.Errorf("stub.calls = %d; want 2 (cache invalidated)", got)
	}

	// Full clear.
	c.Invalidate("", "")
	_, _ = c.Lookup("tenant-A", "egg.standard.v1")
	if got := atomic.LoadInt64(&stub.calls); got != 3 {
		t.Errorf("stub.calls = %d; want 3 (full invalidate)", got)
	}
}

func TestBreedDistributionGRPCClient_ImplementsProvider(t *testing.T) {
	t.Parallel()
	c := &clients.BreedDistributionGRPCClient{}
	var _ growth.BreedDistributionProvider = c
}

func TestBreedDistributionGRPCClient_NoCacheWhenTTLZero(t *testing.T) {
	t.Parallel()
	stub := &stubCompanionEgg{resp: mkHappyResponse()}
	dialer, cleanup := newBufconnDialer(t, stub)
	defer cleanup()

	c, _ := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "bufconn",
		CacheTTL:    0, // disable
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
		CallTimeout: 5 * time.Second,
	})
	defer c.Close()

	for i := 0; i < 3; i++ {
		_, _ = c.Lookup("tenant-A", "egg.standard.v1")
	}
	if got := atomic.LoadInt64(&stub.calls); got != 3 {
		t.Errorf("stub.calls = %d; want 3 (no cache)", got)
	}
}

func TestNewBreedDistributionGRPCClient_EmptyTargetRejected(t *testing.T) {
	t.Parallel()
	_, err := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target: "",
	})
	if err == nil {
		t.Fatal("expected error on empty target")
	}
}
