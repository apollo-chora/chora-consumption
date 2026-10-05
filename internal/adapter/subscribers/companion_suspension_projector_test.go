// companion_suspension_projector_test.go: the ADR-254 D11 pull consumer of
// chora.governance.audit.companion_suspension_changed.v1.
//
// Fixtures mirror the observability emitter EXACTLY (services/chora-
// observability/internal/adapter/pg/companion_suspension_repository.go
// enqueueChanged): payload = protojson of governance.v1.CompanionSuspensionChanged
// with EmitUnpopulated (camelCase keys, int64 version as a JSON string,
// explicit "engaged": false on a release), envelope = the flat attribute map
// the CloudSubscriber turns back into msg.Envelope.
package subscribers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	governancev1 "github.com/apollo-chora/chora-contracts/gen/go/chora/governance/v1"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const (
	suspTenantA   = "11111111-1111-7111-8111-000000000001"
	suspActor     = "01970000-0000-7000-9000-000000000001"
	suspEventID   = "01990000-0000-7000-8000-000000000001"
	suspRowID     = "01990000-0000-7000-8000-000000000010"
	suspChatKey   = "companion_chat_turn_basic"
	suspNilTenant = "00000000-0000-0000-0000-000000000000"
)

var suspT0 = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

// emitterFixture builds the eventbus message the observability outbox relay
// publishes for one change, byte-for-byte the way enqueueChanged does.
func emitterFixture(t *testing.T, eventID, suspensionID, scope, tenantID, skillKey string, engaged bool, version int64, reason string, at time.Time) eventbus.Message {
	t.Helper()
	envTenant := tenantID
	if envTenant == "" {
		envTenant = suspNilTenant
	}
	idem := suspensionID + ":" + itoa(version)
	msg := &governancev1.CompanionSuspensionChanged{
		Envelope: &commonv1.EventEnvelope{
			EventId:            eventID,
			IdempotencyKey:     idem,
			TenantId:           envTenant,
			Gcid:               suspActor,
			OccurredAt:         timestamppb.New(at),
			PublishedAt:        timestamppb.New(at),
			Traceparent:        "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			SourceProject:      "chora-489812",
			SourceService:      "chora-observability",
			SchemaVersion:      1,
			ChoraImdaDimension: "fairness_and_human_oversight",
			ImdaLifecycleStage: "runtime",
		},
		SuspensionId: suspensionID,
		Scope:        scope,
		TenantId:     tenantID,
		SkillKey:     skillKey,
		Engaged:      engaged,
		Version:      version,
		Reason:       reason,
		ActorGcid:    suspActor,
		ChangedAt:    timestamppb.New(at),
	}
	payload, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return eventbus.Message{
		Subject: TopicCompanionSuspensionChanged,
		Envelope: envelope.Envelope{
			EventID:            eventID,
			IdempotencyKey:     idem,
			TenantID:           envTenant,
			GCID:               suspActor,
			OccurredAt:         at,
			PublishedAt:        at,
			Traceparent:        "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			SourceProject:      "chora-489812",
			SourceService:      "chora-observability",
			SchemaVersion:      1,
			ChoraImdaDimension: "fairness_and_human_oversight",
			ImdaLifecycleStage: "runtime",
		},
		Payload:         payload,
		DeliveryAttempt: 1,
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// ---------------------------------------------------------------------------
// Decode
// ---------------------------------------------------------------------------

func TestDecodeCompanionSuspensionChanged_MirrorsEmitterTenantEngage(t *testing.T) {
	msg := emitterFixture(t, suspEventID, suspRowID, "tenant", suspTenantA, suspChatKey, true, 1, "incident 42", suspT0)
	if !strings.Contains(string(msg.Payload), `"suspensionId"`) || !strings.Contains(string(msg.Payload), `"version":"1"`) {
		t.Fatalf("fixture is not the emitter's protojson shape: %s", msg.Payload)
	}

	ev, err := DecodeCompanionSuspensionChanged(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := companion.SuspensionChanged{
		EventID:      suspEventID,
		SuspensionID: suspRowID,
		Scope:        companion.SuspensionScopeTenant,
		TenantID:     suspTenantA,
		SkillKey:     suspChatKey,
		Engaged:      true,
		Version:      1,
		Reason:       "incident 42",
		ActorGCID:    suspActor,
		ChangedAt:    suspT0,
		OccurredAt:   suspT0,
	}
	if ev != want {
		t.Fatalf("decoded = %+v\nwant    = %+v", ev, want)
	}
}

func TestDecodeCompanionSuspensionChanged_MirrorsEmitterPlatformRelease(t *testing.T) {
	msg := emitterFixture(t, suspEventID, suspRowID, "platform", "", "", false, 2, "resolved", suspT0)
	if !strings.Contains(string(msg.Payload), `"engaged":false`) {
		t.Fatalf("a release must say engaged:false explicitly on the wire: %s", msg.Payload)
	}
	ev, err := DecodeCompanionSuspensionChanged(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Scope != companion.SuspensionScopePlatform || ev.TenantID != "" || ev.SkillKey != "" || ev.Engaged || ev.Version != 2 || ev.Reason != "resolved" {
		t.Fatalf("decoded platform release = %+v", ev)
	}
	// The envelope tenant is the nil-UUID sentinel for platform scope; the
	// body's EMPTY tenant_id is what the projection keys on, never the sentinel.
	if ev.TenantID == suspNilTenant {
		t.Fatal("platform scope must not inherit the envelope's nil-UUID tenant")
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("a mirrored platform release must validate: %v", err)
	}
}

// TestDecodeCompanionSuspensionChanged_PinsWireShape hand-writes the JSON so a
// change to how the emitter is READ here is caught even if the fixture builder
// above drifted with it: camelCase keys, version as a string, nested envelope.
func TestDecodeCompanionSuspensionChanged_PinsWireShape(t *testing.T) {
	raw := `{
	  "envelope": {"eventId":"` + suspEventID + `","idempotencyKey":"` + suspRowID + `:2","tenantId":"` + suspNilTenant + `","gcid":"` + suspActor + `",
	               "occurredAt":"2026-08-22T10:00:00Z","publishedAt":"2026-08-22T10:00:00Z","traceparent":"","tracestate":"",
	               "sourceProject":"chora-489812","sourceService":"chora-observability","schemaVersion":1,
	               "correlationId":"","causationId":"","choraImdaDimension":"fairness_and_human_oversight","imdaLifecycleStage":"runtime"},
	  "suspensionId":"` + suspRowID + `","scope":"platform","tenantId":"","skillKey":"companion_skill_cite_atom",
	  "engaged":false,"version":"2","reason":"resolved","actorGcid":"` + suspActor + `","changedAt":"2026-08-22T10:05:00Z"
	}`
	msg := eventbus.Message{
		Subject:  TopicCompanionSuspensionChanged,
		Envelope: envelope.Envelope{EventID: suspEventID, TenantID: suspNilTenant, OccurredAt: suspT0, PublishedAt: suspT0},
		Payload:  []byte(raw),
	}
	ev, err := DecodeCompanionSuspensionChanged(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Version != 2 || ev.Engaged || ev.SkillKey != "companion_skill_cite_atom" || !ev.ChangedAt.Equal(suspT0.Add(5*time.Minute)) || ev.EventID != suspEventID {
		t.Fatalf("decoded = %+v", ev)
	}
}

func TestDecodeCompanionSuspensionChanged_AcceptsProtoFieldNames(t *testing.T) {
	// protojson also accepts the original proto names, so an emitter switched
	// to UseProtoNames keeps flowing instead of dead-lettering.
	raw := `{"suspension_id":"` + suspRowID + `","scope":"tenant","tenant_id":"` + suspTenantA + `","skill_key":"",
	         "engaged":true,"version":1,"reason":"incident","actor_gcid":"` + suspActor + `","changed_at":"2026-08-22T10:00:00Z"}`
	msg := eventbus.Message{Envelope: envelope.Envelope{EventID: suspEventID, OccurredAt: suspT0}, Payload: []byte(raw)}
	ev, err := DecodeCompanionSuspensionChanged(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.TenantID != suspTenantA || ev.Version != 1 || !ev.Engaged {
		t.Fatalf("decoded = %+v", ev)
	}
}

func TestDecodeCompanionSuspensionChanged_ToleratesUnknownFields(t *testing.T) {
	raw := `{"suspensionId":"` + suspRowID + `","scope":"platform","engaged":true,"version":"1","reason":"r","actorGcid":"` + suspActor + `",
	         "changedAt":"2026-08-22T10:00:00Z","futureField":"additive schema evolution must not poison the lane"}`
	msg := eventbus.Message{Envelope: envelope.Envelope{EventID: suspEventID, OccurredAt: suspT0}, Payload: []byte(raw)}
	if _, err := DecodeCompanionSuspensionChanged(msg); err != nil {
		t.Fatalf("unknown field must be discarded, got %v", err)
	}
}

func TestDecodeCompanionSuspensionChanged_MalformedBodyIsAnError(t *testing.T) {
	msg := eventbus.Message{Envelope: envelope.Envelope{EventID: suspEventID}, Payload: []byte(`{"suspensionId": `)}
	if _, err := DecodeCompanionSuspensionChanged(msg); err == nil {
		t.Fatal("malformed JSON must error (NACK -> DLQ), never ACK silently")
	}
	if _, err := DecodeCompanionSuspensionChanged(eventbus.Message{}); err == nil {
		t.Fatal("nil message must error")
	}
}

func TestDecodeCompanionSuspensionChanged_WrongTopicIsAnError(t *testing.T) {
	msg := emitterFixture(t, suspEventID, suspRowID, "platform", "", "", true, 1, "r", suspT0)
	msg.Subject = "chora.tenancy.external_egress_policy.updated.v1"
	if _, err := DecodeCompanionSuspensionChanged(msg); err == nil {
		t.Fatal("a message stamped with another topic must fail loud (mis-bound subscription)")
	}
	msg.Subject = "" // older publishers do not stamp the attribute: tolerated
	if _, err := DecodeCompanionSuspensionChanged(msg); err != nil {
		t.Fatalf("empty topic attribute must be tolerated: %v", err)
	}
}

func TestDecodeCompanionSuspensionChanged_Fallbacks(t *testing.T) {
	// event_id: Pub/Sub attribute first, body envelope second.
	msg := emitterFixture(t, suspEventID, suspRowID, "platform", "", "", true, 1, "r", suspT0)
	msg.Envelope.EventID = ""
	ev, err := DecodeCompanionSuspensionChanged(msg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.EventID != suspEventID {
		t.Fatalf("event id must fall back to the body envelope, got %q", ev.EventID)
	}

	// changed_at: body first, envelope occurred_at second.
	raw := `{"suspensionId":"` + suspRowID + `","scope":"platform","engaged":true,"version":"1","reason":"r","actorGcid":"` + suspActor + `"}`
	msg2 := eventbus.Message{Envelope: envelope.Envelope{EventID: suspEventID, OccurredAt: suspT0}, Payload: []byte(raw)}
	ev2, err := DecodeCompanionSuspensionChanged(msg2)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ev2.ChangedAt.Equal(suspT0) {
		t.Fatalf("changed_at must fall back to envelope occurred_at, got %v", ev2.ChangedAt)
	}

	// actor: body first, envelope gcid second.
	raw3 := `{"suspensionId":"` + suspRowID + `","scope":"platform","engaged":true,"version":"1","reason":"r","changedAt":"2026-08-22T10:00:00Z"}`
	msg3 := eventbus.Message{Envelope: envelope.Envelope{EventID: suspEventID, GCID: suspActor, OccurredAt: suspT0}, Payload: []byte(raw3)}
	ev3, err := DecodeCompanionSuspensionChanged(msg3)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev3.ActorGCID != suspActor {
		t.Fatalf("actor must fall back to envelope gcid, got %q", ev3.ActorGCID)
	}
}

// ---------------------------------------------------------------------------
// Handle (inbox + applier)
// ---------------------------------------------------------------------------

type recordingApplier struct {
	calls   []companion.SuspensionChanged
	applied bool
	err     error
}

func (a *recordingApplier) Apply(_ context.Context, ev companion.SuspensionChanged) (bool, error) {
	a.calls = append(a.calls, ev)
	return a.applied, a.err
}

func validEvent() companion.SuspensionChanged {
	return companion.SuspensionChanged{
		EventID: suspEventID, SuspensionID: suspRowID, Scope: companion.SuspensionScopeTenant, TenantID: suspTenantA,
		SkillKey: suspChatKey, Engaged: true, Version: 1, Reason: "incident 42", ActorGCID: suspActor, ChangedAt: suspT0,
	}
}

func TestNewCompanionSuspensionProjector_NilApplierPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil applier must panic at boot (fail loud, mirrors the egress projection)")
		}
	}()
	NewCompanionSuspensionProjector(nil)
}

func TestNewCompanionSuspensionProjectorWithStore_DefaultsNilStoreAndTTL(t *testing.T) {
	ap := &recordingApplier{applied: true}
	p := NewCompanionSuspensionProjectorWithStore(ap, nil, 0)
	if p.inbox == nil || p.ttl != inboxTTL {
		t.Fatalf("nil store / zero ttl must default (inbox=%v ttl=%v)", p.inbox, p.ttl)
	}
	if err := p.Handle(context.Background(), validEvent()); err != nil {
		t.Fatalf("Handle on defaulted projector: %v", err)
	}
	var nilP *CompanionSuspensionProjector
	if err := nilP.Handle(context.Background(), validEvent()); err == nil {
		t.Fatal("nil projector Handle must error")
	}
}

func TestCompanionSuspensionProjector_Handle_AppliesOnceThenDedupes(t *testing.T) {
	ap := &recordingApplier{applied: true}
	p := NewCompanionSuspensionProjector(ap)
	ctx := context.Background()

	if err := p.Handle(ctx, validEvent()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := p.Handle(ctx, validEvent()); err != nil {
		t.Fatalf("Handle (redelivery): %v", err)
	}
	if len(ap.calls) != 1 {
		t.Fatalf("Apply called %d times, want 1 (inbox dedupe on event_id)", len(ap.calls))
	}
}

func TestCompanionSuspensionProjector_Handle_StaleIsAcked(t *testing.T) {
	ap := &recordingApplier{applied: false}
	p := NewCompanionSuspensionProjector(ap)
	if err := p.Handle(context.Background(), validEvent()); err != nil {
		t.Fatalf("a stale event is a normal outcome and must be ACKed, got %v", err)
	}
}

func TestCompanionSuspensionProjector_Handle_ApplierErrorNacksAndDoesNotClaim(t *testing.T) {
	boom := errors.New("db down")
	ap := &recordingApplier{err: boom}
	store := idempotent.NewMemoryStore()
	p := NewCompanionSuspensionProjectorWithStore(ap, store, time.Hour)
	ctx := context.Background()

	if err := p.Handle(ctx, validEvent()); !errors.Is(err, boom) {
		t.Fatalf("Handle = %v, want boom (NACK)", err)
	}
	seen, _ := store.Seen(ctx, "companion_suspension_changed:"+suspEventID)
	if seen {
		t.Fatal("a failed apply must NOT claim the inbox key (the broker retry must re-run it)")
	}
	ap.err = nil
	ap.applied = true
	if err := p.Handle(ctx, validEvent()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(ap.calls) != 2 {
		t.Fatalf("Apply called %d times, want 2 (one failed, one retry)", len(ap.calls))
	}
}

func TestCompanionSuspensionProjector_Handle_InvalidEventNacksWithoutApply(t *testing.T) {
	ap := &recordingApplier{applied: true}
	p := NewCompanionSuspensionProjector(ap)
	bad := validEvent()
	bad.Reason = ""
	if err := p.Handle(context.Background(), bad); !errors.Is(err, companion.ErrSuspensionReasonRequired) {
		t.Fatalf("Handle(bad) = %v, want ErrSuspensionReasonRequired", err)
	}
	if len(ap.calls) != 0 {
		t.Fatal("an invalid event must never reach the applier")
	}
}

// ---------------------------------------------------------------------------
// PullHandler: decode -> handle -> apply -> status, end to end on the
// in-memory projection
// ---------------------------------------------------------------------------

func TestCompanionSuspensionProjector_PullHandler_EndToEnd(t *testing.T) {
	proj := inmem.NewCompanionSuspensionProjection()
	h := NewCompanionSuspensionProjector(proj).PullHandler()
	ctx := context.Background()

	// engage tenant A chat turns
	if err := h(ctx, emitterFixture(t, suspEventID, suspRowID, "tenant", suspTenantA, suspChatKey, true, 1, "incident 42", suspT0)); err != nil {
		t.Fatalf("engage: %v", err)
	}
	st, err := proj.Status(ctx, suspTenantA, suspChatKey)
	if err != nil || !st.Paused || st.Scope != companion.SuspensionScopeTenant || st.Reason != "incident 42" {
		t.Fatalf("after engage Status = %+v, %v", st, err)
	}

	// stale redelivery of the same engage is ACKed and changes nothing
	if err := h(ctx, emitterFixture(t, suspEventID, suspRowID, "tenant", suspTenantA, suspChatKey, true, 1, "incident 42", suspT0)); err != nil {
		t.Fatalf("redelivery: %v", err)
	}

	// release (v2) lifts it
	if err := h(ctx, emitterFixture(t, "01990000-0000-7000-8000-000000000002", suspRowID, "tenant", suspTenantA, suspChatKey, false, 2, "resolved", suspT0.Add(time.Minute))); err != nil {
		t.Fatalf("release: %v", err)
	}
	st, err = proj.Status(ctx, suspTenantA, suspChatKey)
	if err != nil || st.Paused {
		t.Fatalf("after release Status = %+v, %v", st, err)
	}

	// a platform engage pauses every tenant
	if err := h(ctx, emitterFixture(t, "01990000-0000-7000-8000-000000000003", "01990000-0000-7000-8000-000000000030", "platform", "", "", true, 1, "platform incident", suspT0.Add(2*time.Minute))); err != nil {
		t.Fatalf("platform engage: %v", err)
	}
	st, err = proj.Status(ctx, "22222222-2222-7222-8222-000000000002", "companion_skill_cite_atom")
	if err != nil || !st.Paused || st.Scope != companion.SuspensionScopePlatform {
		t.Fatalf("platform Status = %+v, %v", st, err)
	}
}

func TestCompanionSuspensionProjector_PullHandler_Guards(t *testing.T) {
	h := NewCompanionSuspensionProjector(&recordingApplier{applied: true}).PullHandler()
	if err := h(context.Background(), eventbus.Message{}); err == nil {
		t.Fatal("nil message must error")
	}
	var nilP *CompanionSuspensionProjector
	if err := nilP.PullHandler()(context.Background(), eventbus.Message{Payload: []byte("{}")}); err == nil {
		t.Fatal("nil projector must error")
	}
	malformed := eventbus.Message{Envelope: envelope.Envelope{EventID: suspEventID}, Payload: []byte("not json")}
	if err := h(context.Background(), malformed); err == nil {
		t.Fatal("malformed body must NACK")
	}
}

// ---------------------------------------------------------------------------
// Subscription name (env override)
// ---------------------------------------------------------------------------

func TestCompanionSuspensionSubscriptionName(t *testing.T) {
	t.Setenv(CompanionSuspensionSubscriptionEnv, "")
	if got := CompanionSuspensionSubscriptionName(); got != DefaultCompanionSuspensionSubscription {
		t.Fatalf("default = %q", got)
	}
	if DefaultCompanionSuspensionSubscription != "chora-consumption.governance-audit-companion-suspension-changed" {
		t.Fatalf("default subscription id = %q (must match the provisioned pull subscription)", DefaultCompanionSuspensionSubscription)
	}
	t.Setenv(CompanionSuspensionSubscriptionEnv, "chora-consumption.custom-sub")
	if got := CompanionSuspensionSubscriptionName(); got != "chora-consumption.custom-sub" {
		t.Fatalf("override = %q", got)
	}
	if TopicCompanionSuspensionChanged != "chora.governance.audit.companion_suspension_changed.v1" {
		t.Fatalf("topic = %q", TopicCompanionSuspensionChanged)
	}
}
