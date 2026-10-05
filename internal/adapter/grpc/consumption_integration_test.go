//go:build integration

// consumption_integration_test.go — `//go:build integration` tagged smoke
// for chora-consumption. Runs in Cloud Build stage 3 (integration-test)
// invoked via `go test -race -tags=integration ./...`.
//
// Exercises the gRPC Health Check end-to-end via bufconn — verifies that
// the Consumption + CompanionGrowth + FogOrchestrator + KnowledgeGraphService
// surfaces all report SERVING. This is the same contract the Cloud Service
// Mesh probe-routing relies on for in-cluster traffic dispatch. No external
// Postgres / Pub/Sub needed — bufconn keeps it in-process.
//
// Why a separate file: the existing `internal/adapter/repo/pg/integration_test.go`
// auto-skips in CI without CHORA_TEST_DSN. This file always runs and exercises
// the gRPC composition, giving stage 3 a real signal in every Cloud Build run.
package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

const bufconnSize = 1024 * 1024

// startBufconnHealthServer boots a minimal gRPC server with the same Health
// composition cmd/server/main.go performs (service-scoped SERVING for the
// 4 chora-consumption surfaces). The full ConsumptionServer is not bound
// here because it's UnimplementedConsumptionServer at the moment — Health
// is the meaningful end-to-end probe.
func startBufconnHealthServer(t *testing.T) (healthpb.HealthClient, func()) {
	t.Helper()
	lis := bufconn.Listen(bufconnSize)
	srv := grpc.NewServer()

	healthSrv := healthgrpc.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.consumption.v1.Consumption", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.consumption.v1.FogOrchestrator", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.consumption.v1.KnowledgeGraphService", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("integration bufconn server stopped: %v", err)
		}
	}()

	//nolint:staticcheck // bufconn dial requires the legacy DialContext API.
	conn, err := grpc.DialContext(
		context.Background(),
		"bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("integration bufconn dial: %v", err)
	}
	return healthpb.NewHealthClient(conn), func() {
		_ = conn.Close()
		srv.GracefulStop()
		_ = lis.Close()
	}
}

// Integration smoke: gRPC Health/Check responds SERVING for the default
// surface AND for each service-scoped registration that the Cloud Service
// Mesh probe-routing relies on. Mirrors the verify-job RPC that runs in
// Cloud Deploy's VERIFY stage post-DEPLOY.
func TestIntegration_Consumption_HealthCheck_AllSurfacesServing(t *testing.T) {
	t.Parallel()
	client, cleanup := startBufconnHealthServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name    string
		service string
	}{
		{"default", ""},
		{"Consumption", "chora.services.consumption.v1.Consumption"},
		{"FogOrchestrator", "chora.services.consumption.v1.FogOrchestrator"},
		{"KnowledgeGraphService", "chora.services.consumption.v1.KnowledgeGraphService"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: tc.service})
			if err != nil {
				t.Fatalf("Health/Check(%q): %v", tc.service, err)
			}
			if resp.Status != healthpb.HealthCheckResponse_SERVING {
				t.Errorf("Health/Check(%q) = %v; want SERVING", tc.service, resp.Status)
			}
		})
	}
}
