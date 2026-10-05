// companion_growth_handlers_rls_test.go — RED-phase RLS-context propagation
// proof for the ADR-149 Companion Growth handlers.
//
// Debt #40 close (2026-05-16). The 5 growth handlers (get growth / hatch /
// kg-neighbors / source-revelation / growth-events) all call the GrowthServer
// with `r.Context()` directly. The pgx repo layer's `rls.ApplySession` reads
// tenant_id + gcid via `tracing.TenantIDFromContext` / `tracing.GCIDFromContext`
// and aborts the transaction with `rls: tenant_id missing on context` when
// they are empty — surfaces as a pod-direct HTTP 500 and a gateway-normalised
// 502 GATEWAY_UPSTREAM_5XX (per CR v4 rehearsal row 11).
//
// Mirrors the pattern agent #37 applied at companion_instance_handlers.go:
// every handler MUST wrap r.Context() with tracing.WithTenantID +
// tracing.WithGCID before invoking the repo-backed service.
//
// Per multi-tenant-rls SKILL: handlers populate ctx BEFORE invoking any
// RLS-scoped repo method. The tests below stub the GrowthServer so we can
// observe the ctx it receives — no DB required.
package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ctxCapturingGrowth is a GrowthServer fake that records the ctx it receives
// on every method call so the test can assert tracing.TenantIDFromContext +
// tracing.GCIDFromContext are populated.
type ctxCapturingGrowth struct {
	lastCtx context.Context
}

func (c *ctxCapturingGrowth) GetCompanionGrowth(ctx context.Context, _, companionID, _ string) (*growth.State, error) {
	c.lastCtx = ctx
	return &growth.State{CompanionID: companionID, GrowthStage: 1, StageName: "egg"}, nil
}
func (c *ctxCapturingGrowth) HatchEgg(ctx context.Context, in growth.HatchEggInput) (*growth.HatchEggResponse, error) {
	c.lastCtx = ctx
	return &growth.HatchEggResponse{
		State:   growth.State{CompanionID: in.CompanionID, GrowthStage: 1, StageName: "baby", Species: "dragon"},
		Species: "dragon",
		Rarity:  "common",
	}, nil
}
func (c *ctxCapturingGrowth) RevealBreed(ctx context.Context, in growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
	c.lastCtx = ctx
	return &growth.RevealBreedResponse{
		State:   growth.State{CompanionID: in.CompanionID, GrowthStage: 0, StageName: "egg", Species: "dragon"},
		Species: "dragon",
		Rarity:  "common",
	}, nil
}
func (c *ctxCapturingGrowth) PickResonantConcept(ctx context.Context, in growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error) {
	c.lastCtx = ctx
	return &growth.PickResonantConceptResponse{
		State: growth.State{CompanionID: in.CompanionID, GrowthStage: 2, ResonantConceptID: in.ConceptID},
	}, nil
}
func (c *ctxCapturingGrowth) TriggerSourceRevelation(ctx context.Context, in growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error) {
	c.lastCtx = ctx
	return &growth.TriggerSourceRevelationResponse{
		State:                 growth.State{CompanionID: in.CompanionID, GrowthStage: 3},
		PreviewLLMTier:        "pro",
		WindowDurationSeconds: 86400,
	}, nil
}
func (c *ctxCapturingGrowth) ListGrowthEvents(ctx context.Context, _ growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	c.lastCtx = ctx
	return &growth.ListGrowthEventsOutput{}, nil
}

const (
	growthRLSTenant = "tenant-rls-1"
	growthRLSGCID   = "gcid-rls-1"
)

// TestGetGrowth_PropagatesRLSContextToService — RED before the handler is
// fixed to wrap r.Context() with tracing.WithTenantID + WithGCID.
func TestGetGrowth_PropagatesRLSContextToService(t *testing.T) {
	g := &ctxCapturingGrowth{}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, growthRLSTenant, growthRLSGCID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	assertCtxHasTenantGCID(t, g.lastCtx)
}

// TestHatchEgg_PropagatesRLSContextToService — write-path mirror.
func TestHatchEgg_PropagatesRLSContextToService(t *testing.T) {
	g := &ctxCapturingGrowth{}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/hatch",
		hatchEggReq{DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer", ResonantAtomID: "atom-1"},
		growthRLSTenant, growthRLSGCID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	assertCtxHasTenantGCID(t, g.lastCtx)
}

// TestPickResonantConcept_PropagatesRLSContextToService — the R3-1 pick
// endpoint (kg-neighbors is retired) must stamp tenant+gcid for the repo.
func TestPickResonantConcept_PropagatesRLSContextToService(t *testing.T) {
	g := &ctxCapturingGrowth{}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/resonance",
		pickResonanceReq{ConceptID: "01970000-0000-7000-a000-0000000000c1"}, growthRLSTenant, growthRLSGCID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	assertCtxHasTenantGCID(t, g.lastCtx)
}

// TestTriggerSourceRevelation_PropagatesRLSContextToService.
func TestTriggerSourceRevelation_PropagatesRLSContextToService(t *testing.T) {
	g := &ctxCapturingGrowth{}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/source-revelation",
		triggerRevelationReq{ManaTier: "premium"}, growthRLSTenant, growthRLSGCID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	assertCtxHasTenantGCID(t, g.lastCtx)
}

// TestListGrowthEvents_PropagatesRLSContextToService.
func TestListGrowthEvents_PropagatesRLSContextToService(t *testing.T) {
	g := &ctxCapturingGrowth{}
	srv := newServerWithGrowth(g)
	res := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth-events", nil,
		growthRLSTenant, growthRLSGCID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	assertCtxHasTenantGCID(t, g.lastCtx)
}

// assertCtxHasTenantGCID is the canonical assertion for the RLS-ctx
// propagation contract. Mirrors the pg repo's rls.ApplySession read path.
func assertCtxHasTenantGCID(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx == nil {
		t.Fatal("captured ctx is nil — handler did not invoke the service")
	}
	if v := tracing.TenantIDFromContext(ctx); v != growthRLSTenant {
		t.Errorf("tracing.TenantIDFromContext(ctx) = %q; want %q (RLS context missing — handler needs tracing.WithTenantID wrap)",
			v, growthRLSTenant)
	}
	if v := tracing.GCIDFromContext(ctx); v != growthRLSGCID {
		t.Errorf("tracing.GCIDFromContext(ctx) = %q; want %q (RLS context missing — handler needs tracing.WithGCID wrap)",
			v, growthRLSGCID)
	}
}
