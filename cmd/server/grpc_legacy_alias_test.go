package main

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	grpcadapter "github.com/apollo-chora/chora-consumption/internal/adapter/grpc"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// grpc_legacy_alias_test.go - ADR-254 D9 W4 overlap: the CompanionGrowth
// implementation answers on BOTH service names. The live chat binary calls
// /chora.services.consumption.v1.FamiliarGrowth/ReadWeakness; the renamed
// path is /chora.services.consumption.v1.CompanionGrowth/ReadWeakness. Both
// must reach the same handler (an empty request is refused InvalidArgument by
// the handler's early validation, NEVER Unimplemented, which is what an
// unregistered service name would return).
func TestCompanionGrowth_ServedUnderLegacyFamiliarGrowthNameToo(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	registerCompanionGrowthWithLegacyAlias(srv, grpcadapter.NewCompanionGrowthServer(nil))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer conn.Close()

	// GetCompanionGrowth validates its arguments BEFORE touching any reader, so
	// an empty request is InvalidArgument on a reachable handler (a bare
	// ReadWeakness would answer Unimplemented "reader not wired" on this
	// zero-option server and could not tell the two apart).
	for _, method := range []string{
		"/chora.services.consumption.v1.CompanionGrowth/GetCompanionGrowth",
		"/chora.services.consumption.v1.FamiliarGrowth/GetCompanionGrowth",
	} {
		var out consumptionv1.GetCompanionGrowthResponse
		err := conn.Invoke(ctx, method, &consumptionv1.GetCompanionGrowthRequest{}, &out)
		if code := status.Code(err); code != codes.InvalidArgument {
			t.Fatalf("%s: code = %s (%v), want InvalidArgument from the shared handler", method, code, err)
		}
	}
	// A name nobody registered stays Unimplemented (the alias is exact, not a wildcard).
	var out consumptionv1.GetCompanionGrowthResponse
	err = conn.Invoke(ctx, "/chora.services.consumption.v1.Familiar/GetCompanionGrowth", &consumptionv1.GetCompanionGrowthRequest{}, &out)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("unregistered name: code = %s, want Unimplemented", status.Code(err))
	}
}
