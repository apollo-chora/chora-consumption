// wave_one_xp_subscriber_test.go — F-I3 (CHO-2090, ADR-228 D4 Wave 1) unit
// suite for the Wave-1 XP consumer. RED-first.
//
// Binding semantics pinned here:
//   - All three Wave-1 events are course-/learner-bound and carry NO goal_id,
//     so attribution is the ACTIVE COMPANION (GQ-26 fallback via
//     CompanionResolver.ResolveActiveCompanion) — goal-tagging arrives with the
//     ADR-216 aspiration link. A pre-hatch egg is therefore never warmed by a
//     Wave-1 event (F-I1.4 bind-to-warm: eggs earn only via exact goal match).
//   - Idempotency is DB-ONLY (AwardExpTx UNIQUE(companion_id, source,
//     idempotency_key)) keyed on the upstream envelope event_id: a redelivery
//     re-runs the award with the SAME key and lands on the conflict
//     (Duplicate=true → ack). Deliberately NO mark-before-process tracker —
//     the WS-C5 pattern (a mark-first tracker's loss window drops XP forever;
//     re-running the award is safe because the ledger dedupes).
//   - Transient award failures NACK (error propagates) and the retry MUST
//     still award. ErrNoCompanion drops silently (ack): XP is a Companion-layer
//     moment. Missing identity/anchor fields fail loud (NACK → DLQ).
//   - RequestedDelta is 0 — the resolver-priced flat value (ADR-218 D6) with
//     the identity-0037/curve.go parity pinned in curve_wave1_test.go.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// w1xpResolver is a CompanionResolver double (active-companion attribution).
type w1xpResolver struct {
	id    string
	err   error
	calls int
}

func (f *w1xpResolver) ResolveActiveCompanion(_ context.Context, _, _ string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if f.id == "" {
		return "", ErrNoCompanion
	}
	return f.id, nil
}

const (
	w1xpTenant = "11111111-1111-7111-8111-111111111111"
	w1xpGCID   = "00000000-0000-7000-8000-000000001999"
	w1xpFam    = "01980000-0000-7000-8000-00000000f101"
)

func w1xpEnvelope(eventID string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: eventID,
		TenantID:       w1xpTenant,
		GCID:           w1xpGCID,
		OccurredAt:     time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC),
		PublishedAt:    time.Date(2026, 7, 10, 9, 0, 0, 100, time.UTC),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

func newW1XPForTest(a *cxpAwarder, r *w1xpResolver) *WaveOneXPSubscriber {
	return NewWaveOneXPSubscriber(a, r)
}

// --- weakness_grown ---------------------------------------------------------

func TestWaveOneXP_WeaknessGrown_AwardsActiveCompanion(t *testing.T) {
	award := &cxpAwarder{}
	resolver := &w1xpResolver{id: w1xpFam}
	sub := newW1XPForTest(award, resolver)

	env := w1xpEnvelope("evt-wg-1")
	err := sub.HandleWeaknessGrown(context.Background(), env, WeaknessGrownPayload{
		GrowthEdgeID: "edge-1",
		TenantID:     w1xpTenant,
		LearnerGCID:  w1xpGCID,
		ConceptKey:   "photosynthesis",
	})
	if err != nil {
		t.Fatalf("HandleWeaknessGrown: %v", err)
	}
	calls := award.awards()
	if len(calls) != 1 {
		t.Fatalf("awards = %d, want 1", len(calls))
	}
	in := calls[0]
	if in.Source != "weakness_grown" {
		t.Errorf("source = %q, want weakness_grown", in.Source)
	}
	if in.CompanionID != w1xpFam || in.TenantID != w1xpTenant || in.OwnerGCID != w1xpGCID {
		t.Errorf("routing = %+v, want active companion %s @ %s/%s", in, w1xpFam, w1xpTenant, w1xpGCID)
	}
	if in.IdempotencyKey != "evt-wg-1" || in.SourceEventID != "evt-wg-1" {
		t.Errorf("idempotency anchor = (%q, %q), want the envelope event_id", in.IdempotencyKey, in.SourceEventID)
	}
	if in.SourceTopic != "chora.consumption.weakness.grown.v1" {
		t.Errorf("source_topic = %q, want the weakness.grown topic", in.SourceTopic)
	}
	if in.RequestedDelta != 0 {
		t.Errorf("requested_delta = %d, want 0 (resolver-priced flat value)", in.RequestedDelta)
	}
	if in.Traceparent == "" {
		t.Error("traceparent empty — AwardExp requires a W3C trace")
	}
}

func TestWaveOneXP_WeaknessGrown_NoCompanionDrops(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{}) // resolves ErrNoCompanion

	err := sub.HandleWeaknessGrown(context.Background(), w1xpEnvelope("evt-wg-2"), WeaknessGrownPayload{
		TenantID: w1xpTenant, LearnerGCID: w1xpGCID,
	})
	if err != nil {
		t.Fatalf("no-companion must ack (drop), got %v", err)
	}
	if len(award.awards()) != 0 {
		t.Fatal("no companion — nothing may award")
	}
}

func TestWaveOneXP_WeaknessGrown_ResolverErrorNACKs(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{err: errors.New("pg down")})

	if err := sub.HandleWeaknessGrown(context.Background(), w1xpEnvelope("evt-wg-3"), WeaknessGrownPayload{
		TenantID: w1xpTenant, LearnerGCID: w1xpGCID,
	}); err == nil {
		t.Fatal("resolver read error must NACK (redelivery), got ack")
	}
	if len(award.awards()) != 0 {
		t.Fatal("resolver failed — nothing may award")
	}
}

func TestWaveOneXP_WeaknessGrown_MissingAnchorFailsLoud(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	// No event_id ⇒ no idempotency anchor ⇒ NACK.
	env := w1xpEnvelope("")
	if err := sub.HandleWeaknessGrown(context.Background(), env, WeaknessGrownPayload{
		TenantID: w1xpTenant, LearnerGCID: w1xpGCID,
	}); err == nil {
		t.Fatal("missing event_id must fail loud")
	}
	// No learner identity anywhere ⇒ NACK.
	env = w1xpEnvelope("evt-wg-4")
	env.TenantID, env.GCID = "", ""
	if err := sub.HandleWeaknessGrown(context.Background(), env, WeaknessGrownPayload{}); err == nil {
		t.Fatal("missing tenant/gcid must fail loud")
	}
	if len(award.awards()) != 0 {
		t.Fatal("malformed events must not award")
	}
}

// TestWaveOneXP_WeaknessGrown_ReplaySameKey — the idempotent-replay story: a
// Pub/Sub redelivery re-runs the award with the SAME idempotency key; the
// ledger conflict surfaces as Duplicate=true and the subscriber acks. No
// second-award suppression happens in the subscriber (deliberately no
// tracker) — the DB is the sole anchor.
func TestWaveOneXP_WeaknessGrown_ReplaySameKey(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	env := w1xpEnvelope("evt-wg-5")
	p := WeaknessGrownPayload{TenantID: w1xpTenant, LearnerGCID: w1xpGCID}
	if err := sub.HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Redelivery of the SAME message (same event_id).
	if err := sub.HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery must ack (ledger dedupes), got %v", err)
	}
	calls := award.awards()
	if len(calls) != 2 {
		t.Fatalf("award attempts = %d, want 2 (re-run on every delivery; DB dedupes)", len(calls))
	}
	if calls[0].IdempotencyKey != calls[1].IdempotencyKey {
		t.Fatalf("replay keys differ (%q vs %q) — the ledger UNIQUE can no longer absorb the replay",
			calls[0].IdempotencyKey, calls[1].IdempotencyKey)
	}
}

// TestWaveOneXP_WeaknessGrown_TransientAwardFailureRetries — a transient
// award failure NACKs; the redelivery re-attempts and the XP still lands
// (the anti-loss property that killed the mark-before-process pattern).
func TestWaveOneXP_WeaknessGrown_TransientAwardFailureRetries(t *testing.T) {
	award := &cxpAwarder{failN: 1}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	env := w1xpEnvelope("evt-wg-6")
	p := WeaknessGrownPayload{TenantID: w1xpTenant, LearnerGCID: w1xpGCID}
	if err := sub.HandleWeaknessGrown(context.Background(), env, p); err == nil {
		t.Fatal("transient award failure must NACK")
	}
	if err := sub.HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("retry must succeed: %v", err)
	}
	if got := len(award.awards()); got != 1 {
		t.Fatalf("successful awards = %d, want exactly 1 after retry", got)
	}
}

// --- submission_graded --------------------------------------------------------

func TestWaveOneXP_SubmissionGraded_AwardsActiveCompanion(t *testing.T) {
	award := &cxpAwarder{}
	resolver := &w1xpResolver{id: w1xpFam}
	sub := newW1XPForTest(award, resolver)

	env := w1xpEnvelope("evt-sg-xp-1")
	err := sub.HandleSubmissionGraded(context.Background(), env, SubmissionGradedEvidence{
		SubmissionID:    "sub-1",
		AssessmentID:    "ass-1",
		AssessmentTitle: "Algebra Midterm",
		LearnerGCID:     w1xpGCID,
		TenantID:        w1xpTenant,
		PointsEarned:    7,
		PointsPossible:  10,
	})
	if err != nil {
		t.Fatalf("HandleSubmissionGraded: %v", err)
	}
	calls := award.awards()
	if len(calls) != 1 {
		t.Fatalf("awards = %d, want 1", len(calls))
	}
	in := calls[0]
	if in.Source != "submission_graded" {
		t.Errorf("source = %q, want submission_graded", in.Source)
	}
	if in.CompanionID != w1xpFam {
		t.Errorf("companion = %q, want the active companion (course-bound event, GQ-26 fallback)", in.CompanionID)
	}
	if in.IdempotencyKey != "evt-sg-xp-1" {
		t.Errorf("idempotency key = %q, want the envelope event_id", in.IdempotencyKey)
	}
	if in.SourceTopic != "chora.delivery.submission.graded.v1" {
		t.Errorf("source_topic = %q, want the delivery graded topic", in.SourceTopic)
	}
	if in.RequestedDelta != 0 {
		t.Errorf("requested_delta = %d, want 0 (flat resolver-priced — NO accuracy scaling, L16)", in.RequestedDelta)
	}
}

// TestWaveOneXP_SubmissionGraded_FlatRegardlessOfScore — a failed assessment
// still awards the SAME flat value request (verified effort; the award never
// reads points — content-blind per the ADR-227 anti-farming doctrine).
func TestWaveOneXP_SubmissionGraded_FlatRegardlessOfScore(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	if err := sub.HandleSubmissionGraded(context.Background(), w1xpEnvelope("evt-sg-xp-2"), SubmissionGradedEvidence{
		TenantID: w1xpTenant, LearnerGCID: w1xpGCID, PointsEarned: 0, PointsPossible: 10,
	}); err != nil {
		t.Fatalf("zero-score graded submission must still award flat: %v", err)
	}
	calls := award.awards()
	if len(calls) != 1 || calls[0].RequestedDelta != 0 {
		t.Fatalf("want exactly 1 flat award (delta 0 = resolver value), got %+v", calls)
	}
}

func TestWaveOneXP_SubmissionGraded_ReplaySameKey(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	env := w1xpEnvelope("evt-sg-xp-3")
	p := SubmissionGradedEvidence{TenantID: w1xpTenant, LearnerGCID: w1xpGCID, PointsPossible: 10}
	if err := sub.HandleSubmissionGraded(context.Background(), env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandleSubmissionGraded(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery must ack: %v", err)
	}
	calls := award.awards()
	if len(calls) != 2 || calls[0].IdempotencyKey != calls[1].IdempotencyKey {
		t.Fatalf("replay must re-run the award with the SAME key, got %+v", calls)
	}
}

// --- module_completed ---------------------------------------------------------

func TestWaveOneXP_ModuleCompleted_AwardsActiveCompanion(t *testing.T) {
	award := &cxpAwarder{}
	resolver := &w1xpResolver{id: w1xpFam}
	sub := newW1XPForTest(award, resolver)

	env := w1xpEnvelope("evt-mc-1")
	err := sub.HandleModuleCompleted(context.Background(), env, ModuleCompletedPayload{
		ModuleID:    "01980000-0000-7000-8000-00000000m001",
		CourseID:    "01980000-0000-7000-8000-00000000c001",
		TenantID:    w1xpTenant,
		LearnerGCID: w1xpGCID,
	})
	if err != nil {
		t.Fatalf("HandleModuleCompleted: %v", err)
	}
	calls := award.awards()
	if len(calls) != 1 {
		t.Fatalf("awards = %d, want 1", len(calls))
	}
	in := calls[0]
	if in.Source != "module_completed" {
		t.Errorf("source = %q, want module_completed", in.Source)
	}
	if in.CompanionID != w1xpFam {
		t.Errorf("companion = %q, want the active companion (course-bound event)", in.CompanionID)
	}
	if in.IdempotencyKey != "evt-mc-1" || in.SourceEventID != "evt-mc-1" {
		t.Errorf("idempotency anchor = (%q, %q), want the envelope event_id", in.IdempotencyKey, in.SourceEventID)
	}
	if in.SourceTopic != "chora.delivery.module_progress.completed.v1" {
		t.Errorf("source_topic = %q, want the module_progress.completed topic", in.SourceTopic)
	}
}

// TestWaveOneXP_ModuleCompleted_MissingModuleIDFailsLoud — a module event
// without its module_id is malformed producer output: NACK → DLQ keeps the
// contract honest rather than silently awarding off a hollow event.
func TestWaveOneXP_ModuleCompleted_MissingModuleIDFailsLoud(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	if err := sub.HandleModuleCompleted(context.Background(), w1xpEnvelope("evt-mc-2"), ModuleCompletedPayload{
		TenantID: w1xpTenant, LearnerGCID: w1xpGCID,
	}); err == nil {
		t.Fatal("missing module_id must fail loud")
	}
	if len(award.awards()) != 0 {
		t.Fatal("malformed module event must not award")
	}
}

func TestWaveOneXP_ModuleCompleted_ReplaySameKey(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	env := w1xpEnvelope("evt-mc-3")
	p := ModuleCompletedPayload{ModuleID: "m-1", TenantID: w1xpTenant, LearnerGCID: w1xpGCID}
	if err := sub.HandleModuleCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandleModuleCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery must ack: %v", err)
	}
	calls := award.awards()
	if len(calls) != 2 || calls[0].IdempotencyKey != calls[1].IdempotencyKey {
		t.Fatalf("replay must re-run the award with the SAME key, got %+v", calls)
	}
}

// TestWaveOneXP_IdentityFallsBackToEnvelope — payload identity missing ⇒ the
// envelope's tenant/gcid attribute leg routes the award (fallbackIdentity).
func TestWaveOneXP_IdentityFallsBackToEnvelope(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	if err := sub.HandleWeaknessGrown(context.Background(), w1xpEnvelope("evt-wg-7"), WeaknessGrownPayload{}); err != nil {
		t.Fatalf("envelope-identity event must award: %v", err)
	}
	calls := award.awards()
	if len(calls) != 1 || calls[0].TenantID != w1xpTenant || calls[0].OwnerGCID != w1xpGCID {
		t.Fatalf("award identity = %+v, want the envelope tenant/gcid", calls)
	}
}

// TestWaveOneXP_EnsuresTraceparent — an envelope missing its traceparent
// still awards with a minted W3C trace (AwardExp hard-requires one; a
// missing upstream trace must not drop verified XP).
func TestWaveOneXP_EnsuresTraceparent(t *testing.T) {
	award := &cxpAwarder{}
	sub := newW1XPForTest(award, &w1xpResolver{id: w1xpFam})

	env := w1xpEnvelope("evt-wg-8")
	env.Traceparent = ""
	if err := sub.HandleWeaknessGrown(context.Background(), env, WeaknessGrownPayload{
		TenantID: w1xpTenant, LearnerGCID: w1xpGCID,
	}); err != nil {
		t.Fatalf("missing traceparent must not drop the award: %v", err)
	}
	calls := award.awards()
	if len(calls) != 1 || calls[0].Traceparent == "" {
		t.Fatalf("award must carry an ensured traceparent, got %+v", calls)
	}
}

// TestWaveOneXP_ConstructorPanicsOnNilDeps — a nil award port or resolver is
// a wiring bug: panic at construction, never fail-open at consume time.
func TestWaveOneXP_ConstructorPanicsOnNilDeps(t *testing.T) {
	assertPanics := func(name string, fn func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s: want panic on nil dep", name)
			}
		}()
		fn()
	}
	assertPanics("nil award", func() { NewWaveOneXPSubscriber(nil, &w1xpResolver{}) })
	assertPanics("nil resolver", func() { NewWaveOneXPSubscriber(&cxpAwarder{}, nil) })
}
