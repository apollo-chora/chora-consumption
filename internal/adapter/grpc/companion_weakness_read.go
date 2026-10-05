// companion_weakness_read.go — CHO-2014 (workstream B, R3-3): the weakness.read
// gRPC READ-tool that backs the weakness_sight Skill (and path_weaver).
//
// ReadWeakness returns the OWNER's top-N Growth Edges (learner_weakness
// aggregate) ordered strength-desc (shakiest first). It mirrors
// ReadLearnerProfile (companion_p1b_tools.go): owner-scoped ownership check
// (foreign caller ⇒ NotFound; no cross-owner disclosure), Unimplemented when
// unwired, and LEARNER-SAFE — the projection carries the concept slug + natural
// label + a SANITIZED summary only, NEVER the edge id, learner gcid, topic_id,
// or embedding (no-UUID-leak discipline, CHO-2059/2060).
package grpc

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-common/tracing"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// WeaknessReader is the learner_weakness (Growth-Edge) read slice backing the
// weakness.read agent tool. Satisfied structurally by the pg LearnerWeaknessRepo
// (its List method), which applies RLS via the request ctx.
type WeaknessReader interface {
	List(ctx context.Context, q lw.ListQuery) (lw.ListResult, error)
}

// WithWeaknessReader injects the Growth-Edge reader (nil ⇒ ReadWeakness returns
// Unimplemented — fail-loud, never a stubbed empty list).
func WithWeaknessReader(r WeaknessReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.weakness = r }
}

const (
	// weaknessDefaultLimit is the top-N page size when the caller omits limit.
	weaknessDefaultLimit = 10
	// weaknessMaxLimit bounds the page size (a Companion narrates a handful of
	// shaky concepts, not the whole map).
	weaknessMaxLimit = 50
)

// ReadWeakness implements consumptionv1.CompanionGrowthServer (CHO-2014, R3-3).
func (s *CompanionGrowthServer) ReadWeakness(
	ctx context.Context,
	req *consumptionv1.ReadWeaknessRequest,
) (*consumptionv1.ReadWeaknessResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.ReadWeakness")
	defer span.End()
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
	)
	if s.instances == nil || s.weakness == nil {
		return nil, status.Error(codes.Unimplemented,
			"weakness reader not wired (WithWeaknessReader)")
	}
	tenantID, companionID, callerGCID := strings.TrimSpace(req.GetTenantId()), strings.TrimSpace(req.GetCompanionId()), strings.TrimSpace(req.GetCallerGcid())
	if tenantID == "" || companionID == "" || callerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, companion_id, caller_gcid required")
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), callerGCID)

	// Ownership gate — the Growth Edges belong to the companion's OWNER; a
	// foreign caller is indistinguishable from a missing companion.
	if _, err := s.loadOwnedCompanion(ctx, tenantID, companionID, callerGCID); err != nil {
		return nil, err
	}

	limit := clampWeaknessLimit(int(req.GetLimit()))
	page, err := s.weakness.List(ctx, lw.ListQuery{
		TenantID:     tenantID,
		LearnerGCID:  callerGCID,
		Sort:         lw.SortStrengthDesc, // shakiest first (auto-top)
		IncludeGrown: false,               // mastered edges are not weaknesses
		PageSize:     limit,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list growth edges: %v", err)
	}

	resp := &consumptionv1.ReadWeaknessResponse{}
	for i := range page.Items {
		resp.Edges = append(resp.Edges, weaknessEdgeToProto(&page.Items[i]))
	}
	return resp, nil
}

// clampWeaknessLimit bounds the requested top-N into [1, weaknessMaxLimit],
// defaulting an absent/non-positive value to weaknessDefaultLimit.
func clampWeaknessLimit(n int) int {
	if n <= 0 {
		return weaknessDefaultLimit
	}
	if n > weaknessMaxLimit {
		return weaknessMaxLimit
	}
	return n
}

// weaknessEdgeToProto maps one Growth Edge to its learner-SAFE wire projection.
// It deliberately drops the edge id, learner gcid, topic_id, embedding, and raw
// evidence arrays: those are platform internals / UUID leaks the Companion must
// never parrot into a reflection (domain-vocabulary + CHO-2059/2060 discipline).
// The free-text descriptor summary is sanitized through the shared
// learner_profile presenter (strips any embedded UUID) before it crosses the
// boundary.
func weaknessEdgeToProto(w *lw.LearnerWeakness) *consumptionv1.WeaknessEdge {
	return &consumptionv1.WeaknessEdge{
		ConceptKey:      w.ConceptKey,
		ConceptLabel:    w.ConceptLabel,
		Strength:        w.Strength,
		Category:        w.Category,
		Tags:            append([]string(nil), w.Tags...),
		Status:          string(w.Status),
		Summary:         lp.SanitizeLearnerText(w.Descriptor.Summary),
		LastEvidencedAt: timestamppb.New(w.LastEvidencedAt),
	}
}
