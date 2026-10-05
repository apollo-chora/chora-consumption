// Package grpc — Consumption service gRPC adapter (Wave-1 CN-FULL,
// 2026-05-16, docs/m13/grpc-mass-remediation-2026-05-16.md).
//
// Registers the typed chora.services.consumption.v1.Consumption gRPC
// surface so chora-gateway BFF (and peer Content {verb} services) can
// dial chora-consumption:9090 instead of the HTTP :8080 shim path.
//
// Closes the systemic ADR-140 violation for this service. Per the
// always-loaded `feedback_no_stubs_real_wiring`: NO conditional
// registration; on Unimplemented surfaces, callers receive the
// canonical gRPC `codes.Unimplemented` — the proper fail-loud signal
// that the RPC's domain wiring has not yet landed (NOT a silent stub
// returning fixture data).
//
// RPC-by-RPC wiring follows in subsequent slices (Wave-2 GW-SWITCH
// flips the BFF over RPC-by-RPC; each cutover requires the matching
// server surface to be live first). Today we register the typed server
// so the surface is bindable + Authz/probe topology can be staged.
package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// AtomIndexSearcher is the narrow read port the RecommendAtomsForLearner RPC
// needs: the SearchForLearner slice of atom_index.Repo. Declared here (consumer
// side) so the gRPC adapter depends on a minimal seam, not the full repo.
type AtomIndexSearcher interface {
	SearchForLearner(ctx context.Context, tenantID, topicHint string, limit int) ([]*atom_index.AtomIndex, error)
}

// ConsumptionServer is the gRPC adapter for the Consumption service.
//
// It embeds UnimplementedConsumptionServer per gRPC convention so the
// surface is forward-compatible: adding new RPCs to consumption.proto
// will NOT break the build. Each RPC will be implemented + wired to
// its domain port (atom_attempt.StateMachine, companion.Instance,
// learning_path.Enrollment, dailydose.Composer) as the Wave-2 BFF
// switch lights them up.
//
// RecommendAtomsForLearner is the FIRST wired RPC: it reads the local
// atom_index projection (via atoms) so the AI Kernel Content Recommender crew
// gets REAL published atoms instead of atom-stub-*. A nil atoms searcher keeps
// the RPC fail-loud (codes.Unimplemented) per feedback_no_stubs_real_wiring.
type ConsumptionServer struct {
	consumptionv1.UnimplementedConsumptionServer

	atoms AtomIndexSearcher
}

// NewConsumptionServer constructs the server. Pass the atom_index searcher to
// light up RecommendAtomsForLearner; pass nil to keep that RPC Unimplemented
// (the contract-registration posture for the other not-yet-wired RPCs).
func NewConsumptionServer(atoms AtomIndexSearcher) *ConsumptionServer {
	return &ConsumptionServer{atoms: atoms}
}

// RecommendAtomsForLearner returns ranked candidate atoms from the local
// atom_index projection. Tenant-scoped: it stamps tenant_id (+ learner gcid)
// onto ctx so the pg repo's rls.ApplySession reads them (else the read filters
// to 0 rows). Read-only, side-effect free. NEVER fabricates atom_ids — an empty
// projection yields an empty list (the recommender surfaces an empty-list
// reason rather than a stub).
func (s *ConsumptionServer) RecommendAtomsForLearner(
	ctx context.Context,
	in *consumptionv1.RecommendAtomsForLearnerRequest,
) (*consumptionv1.RecommendAtomsForLearnerResponse, error) {
	if s.atoms == nil {
		return nil, status.Error(codes.Unimplemented,
			"consumption: RecommendAtomsForLearner not wired (no atom_index searcher)")
	}
	if in.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "consumption: tenant_id required")
	}
	if in.GetLearnerGcid() == "" {
		return nil, status.Error(codes.InvalidArgument, "consumption: learner_gcid required")
	}

	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, in.GetTenantId()), in.GetLearnerGcid())

	atoms, err := s.atoms.SearchForLearner(ctx, in.GetTenantId(), in.GetTopicHint(), int(in.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "consumption: search atoms for learner: %v", err)
	}

	out := make([]*consumptionv1.AtomRecommendation, 0, len(atoms))
	for _, a := range atoms {
		if a == nil {
			continue
		}
		out = append(out, &consumptionv1.AtomRecommendation{
			AtomId:      a.AtomID,
			Title:       a.Title,
			Topic:       a.PrimaryTopic(),
			Difficulty:  int32(a.Difficulty),
			PublishedAt: timestamppb.New(a.PublishedAt),
		})
	}
	return &consumptionv1.RecommendAtomsForLearnerResponse{Recommendations: out}, nil
}
