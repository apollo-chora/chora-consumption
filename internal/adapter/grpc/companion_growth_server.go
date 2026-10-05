// Package grpc — gRPC adapters for chora-consumption. This file owns the
// CompanionGrowth server (ADR-149) which translates gRPC requests to the
// domain growth.Service.
//
// Per hexagonal SKILL: this adapter depends on the domain.growth package
// and on the chora-contracts generated stubs; it MUST NOT contain business
// logic. Validation + OTLP spans + proto marshaling only.
package grpc

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// tracerName for OTLP spans emitted by this server.
const tracerName = "chora-consumption.companion_growth"

// CompanionGrowthServer is the gRPC server adapter.
type CompanionGrowthServer struct {
	consumptionv1.UnimplementedCompanionGrowthServer
	svc *growth.Service
	// instances is the per-Companion base-config reader (companion_instances)
	// consumed by ResolveCompanionConfig. Optional: when nil,
	// ResolveCompanionConfig returns codes.Unimplemented (the growth-only
	// surface still works). Wired in production via WithCompanionInstanceReader.
	instances CompanionInstanceReader
	// loadouts is the CHO-2012 equipped-Skills reader (ADR-218 D3): the
	// runtime AllowedSkills = equipped active keys. Optional: nil ⇒ empty
	// allowlist (dark until grants exist). Wired via WithCompanionLoadoutReader.
	loadouts CompanionLoadoutReader
	// catalogue expands equipped skill keys → tool_handler_refs for the
	// allowed_tools merge (CHO-2013 P1.B, R4-2). nil ⇒ innate-only tools.
	catalogue SkillCatalogueReader
	// profiles backs ReadLearnerProfile (profile.read tool, ADR-200).
	profiles LearnerProfileReader
	// courseDir is the OPTIONAL course_id → title resolver (course_directory
	// projection, CHO-2059). nil ⇒ ReadLearnerProfile renders the leak-free
	// generic noun EXACTLY as before (the projection is purely additive).
	courseDir CourseDirectoryReader
	// weakness backs ReadWeakness (weakness.read tool, CHO-2014). nil ⇒
	// ReadWeakness returns Unimplemented (fail-loud, never a stubbed list).
	weakness WeaknessReader
	// kgMap backs ReadKGMap (kg.read_map tool, CHO-2014): ring-scoped per-user
	// concept map. nil ⇒ ReadKGMap returns Unimplemented (fail-loud).
	kgMap KGMapReader
	// memoryNotes + embedder back RecordCompanionMemoryNote (memory.note tool).
	memoryNotes      CompanionMemoryRecorder
	embedder         companion.Embedder
	embeddingModelID string
}

// CompanionGrowthOption configures optional CompanionGrowthServer collaborators.
type CompanionGrowthOption func(*CompanionGrowthServer)

// WithCompanionInstanceReader injects the companion_instances base-config reader
// that ResolveCompanionConfig needs (name / specialization / persona /
// configured_rules / skill grants). Without it, ResolveCompanionConfig returns
// codes.Unimplemented per feedback_no_stubs_real_wiring (fail-loud, never a
// stubbed config).
func WithCompanionInstanceReader(r CompanionInstanceReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.instances = r }
}

// WithCompanionLoadoutReader injects the equipped-Skills loadout reader
// (CHO-2012, ADR-218 D3) so ResolveCompanionConfig returns the equipped
// active keys as AllowedSkills.
func WithCompanionLoadoutReader(r CompanionLoadoutReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.loadouts = r }
}

// NewCompanionGrowthServer constructs the server. Optional collaborators (e.g.
// the companion_instances reader for ResolveCompanionConfig) are supplied via
// CompanionGrowthOption — keeps the growth-only call sites + tests compiling
// unchanged.
func NewCompanionGrowthServer(svc *growth.Service, opts ...CompanionGrowthOption) *CompanionGrowthServer {
	s := &CompanionGrowthServer{svc: svc}
	for _, o := range opts {
		o(s)
	}
	return s
}

// tracer lazily retrieves the package-level tracer (avoids init-time wiring).
func tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(tracerName)
}

// extractTrace returns (traceparent, tracestate) from the gRPC metadata
// envelope if present; otherwise emits a placeholder. Real OTLP propagation
// flows through grpc.UnaryServerInterceptor at the server-config layer; here
// we just need a value to plug into the event envelope.
func extractTrace(ctx context.Context) (string, string) {
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() && sc.HasSpanID() {
		// W3C traceparent shape: version-traceID-spanID-flags
		flags := "00"
		if sc.IsSampled() {
			flags = "01"
		}
		return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + flags, sc.TraceState().String()
	}
	// Fallback when called outside an OTel-instrumented context (e.g. tests).
	return "00-00000000000000000000000000000000-0000000000000000-00", ""
}

// GetCompanionGrowth implements consumptionv1.CompanionGrowthServer.
func (s *CompanionGrowthServer) GetCompanionGrowth(ctx context.Context, req *consumptionv1.GetCompanionGrowthRequest) (*consumptionv1.GetCompanionGrowthResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.GetCompanionGrowth")
	defer span.End()
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
	)
	if strings.TrimSpace(req.GetTenantId()) == "" || strings.TrimSpace(req.GetCompanionId()) == "" || strings.TrimSpace(req.GetCallerGcid()) == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id + companion_id + caller_gcid required")
	}
	state, err := s.svc.GetCompanionGrowth(ctx, req.GetTenantId(), req.GetCompanionId(), req.GetCallerGcid())
	if err != nil {
		return nil, mapDomainError(err)
	}
	return &consumptionv1.GetCompanionGrowthResponse{State: stateToProto(state)}, nil
}

// AwardExp implements consumptionv1.CompanionGrowthServer.
func (s *CompanionGrowthServer) AwardExp(ctx context.Context, req *consumptionv1.AwardExpRequest) (*consumptionv1.AwardExpResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.AwardExp")
	defer span.End()
	traceparent, tracestate := extractTrace(ctx)
	in := growth.AwardExpInput{
		TenantID:        req.GetTenantId(),
		CompanionID:     req.GetCompanionId(),
		OwnerGCID:       req.GetOwnerGcid(),
		Source:          req.GetSource(),
		RequestedDelta:  int(req.GetRequestedDelta()),
		IdempotencyKey:  req.GetIdempotencyKey(),
		SourceEventID:   req.GetSourceEventId(),
		SourceTopic:     req.GetSourceTopic(),
		SourceSessionID: req.GetSourceSessionId(),
		SourceTurnSeq:   int(req.GetSourceTurnSeq()),
		Traceparent:     traceparent,
		Tracestate:      tracestate,
	}
	resp, err := s.svc.AwardExp(ctx, in)
	if err != nil {
		return nil, mapDomainError(err)
	}
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
		attribute.String("chora.growth.source", req.GetSource()),
		attribute.Int("chora.growth.stage_before", resp.State.GrowthStage),
		attribute.Int("chora.growth.stage_after", resp.State.GrowthStage),
		attribute.Int("chora.exp.delta", resp.ClampedDelta),
		attribute.Bool("chora.daily_cap_hit", resp.DailyCapHit),
		attribute.Bool("chora.duplicate", resp.Duplicate),
	)
	return &consumptionv1.AwardExpResponse{
		ClampedDelta:              int32(resp.ClampedDelta),
		DailyCapHit:               resp.DailyCapHit,
		Duplicate:                 resp.Duplicate,
		State:                     stateToProto(&resp.State),
		StageUpTriggered:          resp.StageUpTriggered,
		SourceRevelationTriggered: resp.SourceRevelationTriggered,
	}, nil
}

// HatchEgg implements consumptionv1.CompanionGrowthServer.
func (s *CompanionGrowthServer) HatchEgg(ctx context.Context, req *consumptionv1.HatchEggRequest) (*consumptionv1.HatchEggResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.HatchEgg")
	defer span.End()
	traceparent, tracestate := extractTrace(ctx)
	resp, err := s.svc.HatchEgg(ctx, growth.HatchEggInput{
		TenantID:       req.GetTenantId(),
		CompanionID:    req.GetCompanionId(),
		OwnerGCID:      req.GetOwnerGcid(),
		DisplayName:    req.GetDisplayName(),
		Tone:           req.GetTone(),
		LearnerPersona: req.GetLearnerPersona(),
		ResonantAtomID: req.GetResonantAtomId(),
		Traceparent:    traceparent,
		Tracestate:     tracestate,
	})
	if err != nil {
		return nil, mapDomainError(err)
	}
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
		attribute.String("chora.growth.species", resp.Species),
		attribute.Bool("chora.growth.shiny", resp.ShinyVariant),
	)
	return &consumptionv1.HatchEggResponse{
		State:             stateToProto(&resp.State),
		Species:           resp.Species,
		ShinyVariant:      resp.ShinyVariant,
		Rarity:            resp.Rarity,
		RolledProbability: resp.RolledProbability,
	}, nil
}

// PreviewEggOdds implements consumptionv1.CompanionGrowthServer.
func (s *CompanionGrowthServer) PreviewEggOdds(ctx context.Context, req *consumptionv1.PreviewEggOddsRequest) (*consumptionv1.PreviewEggOddsResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.PreviewEggOdds")
	defer span.End()
	resp, err := s.svc.PreviewEggOdds(ctx, growth.PreviewEggOddsInput{
		TenantID: req.GetTenantId(),
		EggSku:   req.GetEggSku(),
	})
	if err != nil {
		return nil, mapDomainError(err)
	}
	out := &consumptionv1.PreviewEggOddsResponse{
		EggSku:                resp.EggSku,
		TotalWeight:           resp.TotalWeight,
		DistributionUpdatedAt: timestamppb.New(resp.DistributionUpdatedAt),
	}
	for _, w := range resp.Odds {
		out.Odds = append(out.Odds, &consumptionv1.BreedOdds{
			Species:     w.Species,
			Probability: w.Probability,
			Rarity:      w.Rarity,
		})
	}
	return out, nil
}

// PickKgNeighbor implements consumptionv1.CompanionGrowthServer — RETIRED
// (CHO-2013 P1, R3-1): the per-stage neighbor pick is superseded by binding +
// ring radius (resonant-concept centre). The RPC stays in the contract until
// the next contracts cycle (same discipline as xp_unlock_threshold); calls
// refuse loudly rather than silently no-op.
func (s *CompanionGrowthServer) PickKgNeighbor(ctx context.Context, _ *consumptionv1.PickKgNeighborRequest) (*consumptionv1.PickKgNeighborResponse, error) {
	_, span := tracer().Start(ctx, "CompanionGrowth.PickKgNeighbor")
	defer span.End()
	return nil, status.Error(codes.FailedPrecondition, "PICK_KG_NEIGHBOR_RETIRED: superseded by goal binding + resonant-concept ring radius (R3-1, CHO-2013)")
}

// TriggerSourceRevelation implements consumptionv1.CompanionGrowthServer.
func (s *CompanionGrowthServer) TriggerSourceRevelation(ctx context.Context, req *consumptionv1.TriggerSourceRevelationRequest) (*consumptionv1.TriggerSourceRevelationResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.TriggerSourceRevelation")
	defer span.End()
	traceparent, tracestate := extractTrace(ctx)
	resp, err := s.svc.TriggerSourceRevelation(ctx, growth.TriggerSourceRevelationInput{
		TenantID:               req.GetTenantId(),
		CompanionID:            req.GetCompanionId(),
		OwnerGCID:              req.GetOwnerGcid(),
		ManaTier:               req.GetManaTier(),
		WindowDurationOverride: int(req.GetWindowDurationSecondsOverride()),
		Traceparent:            traceparent,
		Tracestate:             tracestate,
	})
	if err != nil {
		return nil, mapDomainError(err)
	}
	return &consumptionv1.TriggerSourceRevelationResponse{
		State:                 stateToProto(&resp.State),
		PreviewLlmTier:        resp.PreviewLLMTier,
		WindowExpiresAt:       timestamppb.New(resp.WindowExpiresAt),
		WindowDurationSeconds: int32(resp.WindowDurationSeconds),
	}, nil
}

// ListGrowthEvents implements consumptionv1.CompanionGrowthServer.
func (s *CompanionGrowthServer) ListGrowthEvents(ctx context.Context, req *consumptionv1.ListGrowthEventsRequest) (*consumptionv1.ListGrowthEventsResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.ListGrowthEvents")
	defer span.End()
	out, err := s.svc.ListGrowthEvents(ctx, growth.ListGrowthEventsInput{
		TenantID:    req.GetTenantId(),
		CompanionID: req.GetCompanionId(),
		CallerGCID:  req.GetCallerGcid(),
		PageSize:    int(req.GetPageSize()),
		PageToken:   req.GetPageToken(),
	})
	if err != nil {
		return nil, mapDomainError(err)
	}
	resp := &consumptionv1.ListGrowthEventsResponse{
		NextPageToken: out.NextPageToken,
	}
	for _, ev := range out.Events {
		resp.Events = append(resp.Events, &consumptionv1.GrowthEventRecord{
			GrowthEventId:    ev.GrowthEventID,
			Source:           ev.Source,
			ExpDelta:         int32(ev.AwardedDelta),
			ExpTotalAfter:    int32(ev.ExpTotalAfter),
			DailyCapHit:      ev.DailyCapHit,
			TriggeredStageUp: ev.TriggeredStageUp,
			AwardedAt:        timestamppb.New(ev.AwardedAt),
		})
	}
	return resp, nil
}

// stateToProto marshals a domain growth.State into the proto message.
func stateToProto(st *growth.State) *consumptionv1.CompanionGrowthState {
	if st == nil {
		return nil
	}
	out := &consumptionv1.CompanionGrowthState{
		CompanionId:              st.CompanionID,
		GrowthStage:              int32(st.GrowthStage),
		StageName:                st.StageName,
		Species:                  st.Species,
		ShinyVariant:             st.ShinyVariant,
		Rarity:                   st.Rarity,
		ExpCurrent:               int32(st.ExpCurrent),
		ExpNextThreshold:         int32(st.ExpNextThreshold),
		ExpCumulative:            int32(st.ExpCumulative),
		EffectiveLlmTier:         st.EffectiveLLMTier,
		EffectiveMaxOutputTokens: int32(st.EffectiveMaxOutputToks),
		UnlockedTools:            st.UnlockedTools,
		ResonantAtomId:           st.ResonantAtomID,
		AhaMomentConsumed:        st.AhaMomentConsumed,
	}
	if st.AhaMomentActiveUntil != nil {
		out.AhaMomentActiveUntil = timestamppb.New(*st.AhaMomentActiveUntil)
	}
	if st.HatchedAt != nil {
		out.HatchedAt = timestamppb.New(*st.HatchedAt)
	}
	if st.LastStageUpAt != nil {
		out.LastStageUpAt = timestamppb.New(*st.LastStageUpAt)
	}
	// visible_kg_neighbors: RETIRED (R3-1) — the proto field stays empty.
	return out
}

// mapDomainError translates domain errors to gRPC status codes.
func mapDomainError(err error) error {
	switch {
	case errors.Is(err, growth.ErrCompanionNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, growth.ErrAlreadyHatched),
		errors.Is(err, growth.ErrNotInEggStage),
		errors.Is(err, growth.ErrNotStirring),
		errors.Is(err, growth.ErrAhaMomentConsumed),
		errors.Is(err, growth.ErrCompanionNotBound),
		errors.Is(err, growth.ErrCompanionNotHatched):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, growth.ErrInvalidArguments),
		errors.Is(err, growth.ErrInvalidSource):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
