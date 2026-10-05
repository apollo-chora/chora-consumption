// companion_kg_map_read.go — CHO-2014 (map_sight / kg.read_map): the ReadKGMap
// gRPC READ-tool that backs the map_sight Skill (and goal_scribe).
//
// ReadKGMap resolves the Companion's resonant-concept centre + growth stage from
// the growth State, derives the ring reach from the stage (RingsForStage —
// 1 ring ≤ Awakened st3, 2 rings st4+), and delegates the ring-scoped concept
// read to the injected KGMapReader (kgmapread.Reader over the learner's per-user
// concept_graph). Owner scoping rides GetCompanionGrowth (foreign caller ⇒
// NotFound). Unimplemented when the reader is unwired; an unpicked resonant
// centre yields an empty map (no read). LEARNER-SAFE: the wire projection is
// concept TITLES + ring distance only — never the concept UUID or atom refs.
package grpc

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/kgmapread"
	kgr "github.com/apollo-chora/chora-consumption/internal/domain/knowledge_graph_read"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// KGMapReader assembles the ring-scoped concept map around a centre concept.
// Satisfied by kgmapread.Reader (over the pg concept_graph repos). Learner-SAFE
// by construction — MapNode carries titles + ring distance, no identifiers.
type KGMapReader interface {
	ReadRingMap(ctx context.Context, tenantID, learnerGCID, centreConceptID string, rings int) ([]kgmapread.MapNode, error)
}

// WithKGMapReader injects the ring-map reader (nil ⇒ ReadKGMap returns
// Unimplemented — fail-loud, never a stubbed empty map).
func WithKGMapReader(r KGMapReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.kgMap = r }
}

// ReadKGMap implements consumptionv1.CompanionGrowthServer (CHO-2014, R3-3).
func (s *CompanionGrowthServer) ReadKGMap(
	ctx context.Context,
	req *consumptionv1.ReadKGMapRequest,
) (*consumptionv1.ReadKGMapResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.ReadKGMap")
	defer span.End()
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
	)
	if s.kgMap == nil {
		return nil, status.Error(codes.Unimplemented,
			"kg map reader not wired (WithKGMapReader)")
	}
	tenantID, companionID, callerGCID := strings.TrimSpace(req.GetTenantId()), strings.TrimSpace(req.GetCompanionId()), strings.TrimSpace(req.GetCallerGcid())
	if tenantID == "" || companionID == "" || callerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, companion_id, caller_gcid required")
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), callerGCID)

	// Growth State carries BOTH the ownership gate (foreign caller ⇒
	// ErrCompanionNotFound) AND the ring centre + stage the map read needs.
	state, err := s.svc.GetCompanionGrowth(ctx, tenantID, companionID, callerGCID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	rings := kgr.RingsForStage(state.GrowthStage)
	resp := &consumptionv1.ReadKGMapResponse{Rings: int32(rings)}

	centre := strings.TrimSpace(state.ResonantConceptID)
	if centre == "" {
		// No resonant concept picked yet — the map has no centre to scope from.
		// Return the reach (rings) with an empty concept set; the Skill narrates
		// "your map has no centre yet" rather than fabricating one.
		return resp, nil
	}

	nodes, err := s.kgMap.ReadRingMap(ctx, tenantID, callerGCID, centre, rings)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read ring map: %v", err)
	}
	for _, n := range nodes {
		if n.IsCentre {
			resp.CentreTitle = n.Title
		}
		resp.Concepts = append(resp.Concepts, &consumptionv1.MapConcept{
			Title:    n.Title,
			Ring:     int32(n.Ring),
			IsCentre: n.IsCentre,
		})
	}
	return resp, nil
}
