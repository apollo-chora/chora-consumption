// weakness_review_pending_subscriber_test.go — RED tests for the bounded HITL
// review-panel bridge (ADR-205 D4 / CHO-1973). The subscriber consumes
// chora.consumption.weakness.review_pending.v1 (emitted by the graduated
// ai-kernel analyser crew when it PAUSES at the HITL interrupt) and parks the
// originating upload job in AWAITING_REVIEW with the panel stored verbatim.
// Verified with a fake ReviewParker (no live pg).
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- fake ReviewParker ---

type parkCall struct {
	gcid, upload string
	panel        *wu.ReviewPanel
	now          time.Time
}

type fakeReviewParker struct {
	calls     []parkCall
	ctxTenant []string
	err       error
}

func (f *fakeReviewParker) MarkAwaitingReview(ctx context.Context, gcid, upload string, panel *wu.ReviewPanel, now time.Time) error {
	f.ctxTenant = append(f.ctxTenant, tracing.TenantIDFromContext(ctx))
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, parkCall{gcid, upload, panel, now})
	return nil
}

func reviewPendingPayload() WeaknessReviewPendingPayload {
	return WeaknessReviewPendingPayload{
		UploadID:    "upl-1",
		TenantID:    "01970000-0000-7000-8000-000000000001",
		LearnerGCID: "01970000-0000-7000-9000-000000000001",
		Panel: &wu.ReviewPanel{
			Companion: &wu.ReviewCompanion{CompanionID: "fam-1", Name: "Ember", Species: "dragon"},
			ProposedEdges: []wu.ProposedEdge{{
				ProposedEdgeID: "pe-opaque-1", // orchestrator-owned; stored verbatim
				ConceptLabel:   "causes of riverine flooding",
				Summary:        "shaky on causation",
				Strength:       0.7,
			}},
			CandidateStruggles: []wu.CandidateStruggle{{ConceptKey: "tectonics", ConceptLabel: "plate boundaries"}},
			AvailableOutputs:   []wu.AvailableOutput{{Kind: "practice_test", ManaPrice: 50, DefaultSelected: true}},
		},
		PendingAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
	}
}

func TestWeaknessReviewPending_ParksAndStoresPanel(t *testing.T) {
	parker := &fakeReviewParker{}
	sub := NewWeaknessReviewPendingSubscriber(parker)
	if err := sub.Handle(context.Background(), lwEnv("evt-rp-1"), reviewPendingPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(parker.calls) != 1 {
		t.Fatalf("MarkAwaitingReview calls = %d want 1", len(parker.calls))
	}
	c := parker.calls[0]
	if c.gcid != "01970000-0000-7000-9000-000000000001" || c.upload != "upl-1" {
		t.Errorf("park ids = %q / %q", c.gcid, c.upload)
	}
	if c.panel == nil || len(c.panel.ProposedEdges) != 1 || c.panel.ProposedEdges[0].ProposedEdgeID != "pe-opaque-1" {
		t.Errorf("panel not stored opaquely: %+v", c.panel)
	}
	if !c.now.Equal(time.Date(2026, 6, 9, 12, 0, 5, 0, time.UTC)) {
		// parked at the envelope's occurred_at (lwEnv)
		t.Errorf("park now = %v want occurred_at", c.now)
	}
	// RLS: the parker ctx must carry the envelope tenant (pg repo scopes on it).
	if parker.ctxTenant[0] != "01970000-0000-7000-8000-000000000001" {
		t.Errorf("park ctx tenant = %q", parker.ctxTenant[0])
	}
}

func TestWeaknessReviewPending_Idempotent_Redelivery(t *testing.T) {
	parker := &fakeReviewParker{}
	sub := NewWeaknessReviewPendingSubscriber(parker)
	env := lwEnv("evt-rp-2")
	p := reviewPendingPayload()
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("redelivered Handle: %v", err)
	}
	if len(parker.calls) != 1 {
		t.Fatalf("MarkAwaitingReview calls = %d want 1 (redelivery must dedup)", len(parker.calls))
	}
}

func TestWeaknessReviewPending_MissingUploadID_Errors(t *testing.T) {
	parker := &fakeReviewParker{}
	sub := NewWeaknessReviewPendingSubscriber(parker)
	p := reviewPendingPayload()
	p.UploadID = ""
	if err := sub.Handle(context.Background(), lwEnv("evt-rp-3"), p); err == nil {
		t.Fatal("want error on missing upload_id")
	}
	if len(parker.calls) != 0 {
		t.Fatalf("must not park without an upload_id; calls=%d", len(parker.calls))
	}
}

func TestWeaknessReviewPending_InvalidEnvelope_Errors(t *testing.T) {
	parker := &fakeReviewParker{}
	sub := NewWeaknessReviewPendingSubscriber(parker)
	env := lwEnv("evt-rp-4")
	env.Traceparent = "" // mandatory field missing → reject
	if err := sub.Handle(context.Background(), env, reviewPendingPayload()); err == nil {
		t.Fatal("want error on invalid envelope")
	}
}

func TestWeaknessReviewPending_ParkerError_NacksAndRetries(t *testing.T) {
	parker := &fakeReviewParker{err: errors.New("pg down")}
	sub := NewWeaknessReviewPendingSubscriber(parker)
	env := lwEnv("evt-rp-5")
	p := reviewPendingPayload()
	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("want error to propagate (NACK) when the parker fails")
	}
	// Inbox must NOT commit on failure: once the parker recovers, a redelivery
	// reprocesses (the dedup key was never claimed).
	parker.err = nil
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("recovery Handle: %v", err)
	}
	if len(parker.calls) != 1 {
		t.Fatalf("recovery must park exactly once; calls=%d", len(parker.calls))
	}
}
