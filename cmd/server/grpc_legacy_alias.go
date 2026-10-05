// grpc_legacy_alias.go - ADR-254 D9 W4 overlap: the renamed CompanionGrowth
// gRPC service is ALSO registered under its pre-rename name so the live chat
// binary (built against FamiliarGrowth/ReadWeakness) keeps working until the
// rebuilt image is set. One implementation, two ServiceDescs (same handlers;
// field-number compatible wire). The Istio allowlist carries both path sets.
//
// RETIRE after WP-A's "chat live": delete this file, its call in main.go, and
// the FamiliarGrowth block of authz-allow-consumption-grpc.yaml.
package main

import (
	"google.golang.org/grpc"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

const legacyFamiliarGrowthServiceName = "chora.services.consumption.v1.FamiliarGrowth"

// registerCompanionGrowthWithLegacyAlias registers srv under both service names.
func registerCompanionGrowthWithLegacyAlias(grpcSrv grpc.ServiceRegistrar, srv consumptionv1.CompanionGrowthServer) {
	consumptionv1.RegisterCompanionGrowthServer(grpcSrv, srv)
	legacyDesc := consumptionv1.CompanionGrowth_ServiceDesc
	legacyDesc.ServiceName = legacyFamiliarGrowthServiceName
	grpcSrv.RegisterService(&legacyDesc, srv)
}
