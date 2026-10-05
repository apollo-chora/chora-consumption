// companion_growth_server_cover_test.go — white-box unit tests for the
// CompanionGrowthServer gRPC adapter (companion_growth_server.go).
//
// These are NON-tagged (plain `go test`) bufconn-free tests: they drive the
// gRPC methods directly with a REAL growth.Service composed from the in-memory
// growth repo (internal/adapter/repo/inmem.GrowthRepo) + a fake outbox + a stub
// breed-distribution provider. No Postgres / network. The adapter under test
// is transport-only (validation + OTLP spans + proto marshaling), so these
// tests characterize: request validation, domain-error → gRPC-code mapping,
// and the domain-state → proto-message field copy.
package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// ----- test harness -----

// fakeGrowthOutbox records published topics. PublishGrowthEvent never fails so
// the happy-path RPCs reach their proto-marshaling tail.
type fakeGrowthOutbox struct {
	topics []string
}

func (o *fakeGrowthOutbox) PublishGrowthEvent(_ context.Context, topic string, _ map[string]any, _ growth.GrowthEnvelope) error {
	o.topics = append(o.topics, topic)
	return nil
}

// stubBreedDist returns a fixed two-species distribution that sums to 100.
type stubBreedDist struct{}

func (stubBreedDist) Lookup(_, _ string) ([]growth.BreedWeight, error) {
	return []growth.BreedWeight{
		{Species: "owl", Probability: 70, Rarity: "common"},
		{Species: "dragon", Probability: 30, Rarity: "legendary"},
	}, nil
}

// fixedClock makes hatch/award timestamps deterministic.
var fixedClock = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

// newGrowthServer builds a CompanionGrowthServer backed by a real growth.Service
// over the in-memory repo. Returns the server + repo so tests can seed rows.
func newGrowthServer(t *testing.T) (*CompanionGrowthServer, *inmem.GrowthRepo) {
	t.Helper()
	repo := inmem.NewGrowthRepo()
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: &fakeGrowthOutbox{},
		Dist:   stubBreedDist{},
		Clock:  func() time.Time { return fixedClock },
		NewID:  func() string { return "evt-fixed" },
	})
	require.NoError(t, err)
	return NewCompanionGrowthServer(svc), repo
}

const tp = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"

// ----- extractTrace -----

func TestExtractTrace(t *testing.T) {
	// No span in context → deterministic all-zero fallback + empty tracestate.
	gotTP, gotTS := extractTrace(context.Background())
	assert.Equal(t, "00-00000000000000000000000000000000-0000000000000000-00", gotTP)
	assert.Equal(t, "", gotTS)

	// With a valid (sampled) span context → real W3C traceparent shape.
	tid, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	sid, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	gotTP, _ = extractTrace(ctx)
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", gotTP)

	// Unsampled span → flags "00".
	scUnsampled := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid})
	gotTP, _ = extractTrace(trace.ContextWithSpanContext(context.Background(), scUnsampled))
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-00", gotTP)
}

// ----- GetCompanionGrowth -----

func TestGetCompanionGrowth_Validation(t *testing.T) {
	s, _ := newGrowthServer(t)
	cases := []*consumptionv1.GetCompanionGrowthRequest{
		{CompanionId: "f", CallerGcid: "g"}, // no tenant
		{TenantId: "t", CallerGcid: "g"},    // no companion
		{TenantId: "t", CompanionId: "f"},   // no caller
		{},                                  // all empty
	}
	for i, req := range cases {
		_, err := s.GetCompanionGrowth(context.Background(), req)
		assert.Equalf(t, codes.InvalidArgument, codeOf(t, err), "case %d", i)
	}
}

func TestGetCompanionGrowth_NotFound(t *testing.T) {
	s, repo := newGrowthServer(t)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "real-owner", GrowthStage: 2,
	})
	// Missing companion → NotFound.
	_, err := s.GetCompanionGrowth(context.Background(), &consumptionv1.GetCompanionGrowthRequest{
		TenantId: "t", CompanionId: "missing", CallerGcid: "g",
	})
	assert.Equal(t, codes.NotFound, codeOf(t, err))
	// Ownership mismatch (caller != owner) → NotFound (defence-in-depth).
	_, err = s.GetCompanionGrowth(context.Background(), &consumptionv1.GetCompanionGrowthRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "intruder",
	})
	assert.Equal(t, codes.NotFound, codeOf(t, err))
}

func TestGetCompanionGrowth_HappyPath(t *testing.T) {
	s, repo := newGrowthServer(t)
	hatchedAt := fixedClock.Add(-48 * time.Hour)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		GrowthStage: 2, Species: "owl", SpeciesRarity: "common",
		ResonantAtom: "atom-9",
		HatchedAt:    &hatchedAt,
	})
	resp, err := s.GetCompanionGrowth(context.Background(), &consumptionv1.GetCompanionGrowthRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	require.NoError(t, err)
	require.NotNil(t, resp.State)
	assert.Equal(t, "fam-1", resp.State.CompanionId)
	assert.Equal(t, int32(2), resp.State.GrowthStage)
	assert.Equal(t, "owl", resp.State.Species)
	assert.Equal(t, "atom-9", resp.State.ResonantAtomId)
	// Neighbors projected as KgNeighbor with graph_distance=1, via=user_pick.
}

// ----- AwardExp -----

func TestAwardExp_InvalidSource(t *testing.T) {
	s, repo := newGrowthServer(t)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g", GrowthStage: 1,
	})
	_, err := s.AwardExp(context.Background(), &consumptionv1.AwardExpRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		Source: "not-a-real-source", RequestedDelta: 3, IdempotencyKey: "k1",
	})
	// Domain returns ErrInvalidSource (no traceparent here, but source check
	// fires first) → mapped to InvalidArgument.
	assert.Equal(t, codes.InvalidArgument, codeOf(t, err))
}

func TestAwardExp_HappyPath(t *testing.T) {
	s, repo := newGrowthServer(t)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g", GrowthStage: 1, GrowthExp: 0,
	})
	// extractTrace fallback supplies the traceparent the domain requires.
	resp, err := s.AwardExp(context.Background(), &consumptionv1.AwardExpRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k1",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), resp.ClampedDelta)
	assert.False(t, resp.Duplicate)
	require.NotNil(t, resp.State)
	assert.Equal(t, "fam-1", resp.State.CompanionId)

	// Same idempotency key replays → Duplicate, ClampedDelta 0.
	dup, err := s.AwardExp(context.Background(), &consumptionv1.AwardExpRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k1",
	})
	require.NoError(t, err)
	assert.True(t, dup.Duplicate)
	assert.Equal(t, int32(0), dup.ClampedDelta)
}

// ----- HatchEgg -----

func TestHatchEgg_Validation(t *testing.T) {
	s, _ := newGrowthServer(t)
	// Missing display_name (tenant/companion/owner present) → InvalidArgument.
	_, err := s.HatchEgg(context.Background(), &consumptionv1.HatchEggRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
	})
	assert.Equal(t, codes.InvalidArgument, codeOf(t, err))
	// Bad tone → InvalidArgument.
	_, err = s.HatchEgg(context.Background(), &consumptionv1.HatchEggRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		DisplayName: "Spark", Tone: "bogus", LearnerPersona: "curious-explorer",
		ResonantAtomId: "01971a90-1111-7000-8000-000000000001",
	})
	assert.Equal(t, codes.InvalidArgument, codeOf(t, err))
}

func TestHatchEgg_NotFound(t *testing.T) {
	s, _ := newGrowthServer(t)
	// Fully-valid request but no Stage-0 row seeded → domain GetGrowthRow
	// returns ErrCompanionNotFound → NotFound.
	_, err := s.HatchEgg(context.Background(), &consumptionv1.HatchEggRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		DisplayName: "Spark", Tone: "encouraging", LearnerPersona: "curious-explorer",
		ResonantAtomId: "01971a90-1111-7000-8000-000000000001",
	})
	assert.Equal(t, codes.NotFound, codeOf(t, err))
}

func TestHatchEgg_HappyPath(t *testing.T) {
	s, repo := newGrowthServer(t)
	revealedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		GrowthStage: 0, GrowthExp: 30, EggSku: "egg-basic", // F-I1.3: stirring (>= threshold)
		// CHO-2229 commit-only hatch: the roll is already persisted (the
		// ceremony's reveal step); "owl" sits in the stub distribution.
		Species: "owl", SpeciesRarity: "common", RolledProb: 50, RevealedAt: &revealedAt,
	})
	resp, err := s.HatchEgg(context.Background(), &consumptionv1.HatchEggRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		DisplayName: "Spark", Tone: "encouraging", LearnerPersona: "curious-explorer",
		ResonantAtomId: "01971a90-1111-7000-8000-000000000001",
	})
	require.NoError(t, err)
	// Species is one of the stub distribution's species (mapping is verbatim).
	assert.Contains(t, []string{"owl", "dragon"}, resp.Species)
	require.NotNil(t, resp.State)
	assert.Equal(t, int32(1), resp.State.GrowthStage) // hatched → Stage 1 (baby)
	assert.Equal(t, resp.Species, resp.State.Species)

	// Re-hatch → ErrAlreadyHatched → FailedPrecondition.
	_, err = s.HatchEgg(context.Background(), &consumptionv1.HatchEggRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		DisplayName: "Spark", Tone: "encouraging", LearnerPersona: "curious-explorer",
		ResonantAtomId: "01971a90-1111-7000-8000-000000000001",
	})
	assert.Equal(t, codes.FailedPrecondition, codeOf(t, err))
}

// ----- PreviewEggOdds -----

func TestPreviewEggOdds_MissingSku(t *testing.T) {
	s, _ := newGrowthServer(t)
	_, err := s.PreviewEggOdds(context.Background(), &consumptionv1.PreviewEggOddsRequest{
		TenantId: "t",
	})
	assert.Equal(t, codes.InvalidArgument, codeOf(t, err))
}

func TestPreviewEggOdds_HappyPath(t *testing.T) {
	s, _ := newGrowthServer(t)
	resp, err := s.PreviewEggOdds(context.Background(), &consumptionv1.PreviewEggOddsRequest{
		TenantId: "t", EggSku: "egg-basic",
	})
	require.NoError(t, err)
	assert.Equal(t, "egg-basic", resp.EggSku)
	assert.InDelta(t, 100.0, resp.TotalWeight, 0.001)
	// Odds sorted descending by probability (owl 70 before dragon 30).
	require.Len(t, resp.Odds, 2)
	assert.Equal(t, "owl", resp.Odds[0].Species)
	assert.InDelta(t, 70.0, resp.Odds[0].Probability, 0.001)
	assert.Equal(t, "dragon", resp.Odds[1].Species)
	assert.NotNil(t, resp.DistributionUpdatedAt)
}

// ----- PickKgNeighbor (RETIRED R3-1, CHO-2013 P1) -----

func TestPickKgNeighbor_RetiredFailedPrecondition(t *testing.T) {
	s, _ := newGrowthServer(t)
	_, err := s.PickKgNeighbor(context.Background(), &consumptionv1.PickKgNeighborRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "gcid-1", AtomId: "atom-a",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "PICK_KG_NEIGHBOR_RETIRED")
}

// ----- TriggerSourceRevelation -----

func TestTriggerSourceRevelation_StageTooLow(t *testing.T) {
	s, repo := newGrowthServer(t)
	// Stage 2 < required Stage 3 → ErrInvalidArguments → InvalidArgument.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g", GrowthStage: 2,
	})
	_, err := s.TriggerSourceRevelation(context.Background(), &consumptionv1.TriggerSourceRevelationRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g", ManaTier: "premium",
	})
	assert.Equal(t, codes.InvalidArgument, codeOf(t, err))
}

func TestTriggerSourceRevelation_HappyPath(t *testing.T) {
	s, repo := newGrowthServer(t)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g", GrowthStage: 3,
	})
	resp, err := s.TriggerSourceRevelation(context.Background(), &consumptionv1.TriggerSourceRevelationRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g", ManaTier: "premium",
		WindowDurationSecondsOverride: 3600,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3600), resp.WindowDurationSeconds)
	assert.NotEmpty(t, resp.PreviewLlmTier)
	assert.NotNil(t, resp.WindowExpiresAt)
	require.NotNil(t, resp.State)

	// Second call → aha moment already consumed → FailedPrecondition.
	_, err = s.TriggerSourceRevelation(context.Background(), &consumptionv1.TriggerSourceRevelationRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g", ManaTier: "premium",
	})
	assert.Equal(t, codes.FailedPrecondition, codeOf(t, err))
}

// ----- ListGrowthEvents -----

func TestListGrowthEvents_Validation(t *testing.T) {
	s, _ := newGrowthServer(t)
	_, err := s.ListGrowthEvents(context.Background(), &consumptionv1.ListGrowthEventsRequest{
		CompanionId: "fam-1", // no tenant
	})
	assert.Equal(t, codes.InvalidArgument, codeOf(t, err))
}

func TestListGrowthEvents_HappyPath(t *testing.T) {
	s, repo := newGrowthServer(t)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g", GrowthStage: 1,
	})
	// Generate one ledger entry via a real award.
	_, err := s.AwardExp(context.Background(), &consumptionv1.AwardExpRequest{
		TenantId: "t", CompanionId: "fam-1", OwnerGcid: "g",
		Source: "atom_session", RequestedDelta: 5, IdempotencyKey: "k-1",
	})
	require.NoError(t, err)

	resp, err := s.ListGrowthEvents(context.Background(), &consumptionv1.ListGrowthEventsRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	ev := resp.Events[0]
	assert.Equal(t, "atom_session", ev.Source)
	assert.Equal(t, int32(5), ev.ExpDelta)
	assert.Equal(t, int32(5), ev.ExpTotalAfter)
	assert.NotNil(t, ev.AwardedAt)
	assert.NotEmpty(t, ev.GrowthEventId)
}
