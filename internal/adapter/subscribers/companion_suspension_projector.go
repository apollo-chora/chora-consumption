// companion_suspension_projector.go: the ADR-254 D11 pull consumer that
// projects chora.governance.audit.companion_suspension_changed.v1 (ADR-252 D4,
// published by chora-observability through its outbox) into consumption's
// LOCAL ADVISORY read-copy (migration 0112, companion.SuspensionProjection).
//
// ⚠ ADVISORY. chora-model-gateway is the containment control (ADR-252 D1 /
// D6, uncached, fail-closed, deny-before-debit). This projection only lets
// consumption refuse a chat turn honestly, skip the ADR-235 reflection claim
// while paused (ADR-252 Q5) and render a paused status. A dropped or late
// event here is a UX wobble, never a containment breach; that is why a stale
// event is ACKed and only a MALFORMED one dead-letters.
//
// # Wiring (cmd/server/main.go, next to the closure subscriber; NOT done here)
//
//	// pg when the pool is up (durable, RLS-bound), in-memory otherwise:
//	var suspProj companion.SuspensionProjection
//	if pool != nil {
//	    suspProj = consumptionpg.NewCompanionSuspensionProjectionRepo(consumptionpg.NewPgxTxRunner(pool))
//	} else {
//	    suspProj = inmem.NewCompanionSuspensionProjection()   // NOT durable across restart
//	}
//	srv.CompanionSuspension = suspProj                        // the SuspensionAdvisor the handlers read
//	if pubsubClient != nil {
//	    projector := subscribers.NewCompanionSuspensionProjector(suspProj)
//	    subName := subscribers.CompanionSuspensionSubscriptionName() // env CHORA_COMPANION_SUSPENSION_SUBSCRIPTION, default below
//	    go func() {
//	        log.Printf("consumption: companion suspension projector binding %s -> %s", subName, subscribers.TopicCompanionSuspensionChanged)
//	        if err := bus.Subscribe(ctx, consumerConfig(subName, subscribers.TopicCompanionSuspensionChanged), projector.PullHandler()); err != nil && !errors.Is(err, context.Canceled) {
//	            log.Printf("consumption: companion suspension projector exited: %v", err)
//	        }
//	    }()
//	}
//
// The subscription chora-consumption.governance-audit-companion-suspension-
// changed is provisioned (ack 60, pull) in chora-infra/terraform/environments/
// dev/companion_topic_estate.tf under the content SA; the CloudSubscriber
// ACKs on a nil return and NACKs on an error (retry, then the topic DLQ).
//
// # Chat handler (internal/adapter/http/companion_chat_handler.go)
//
// After requireContext + the action code is known and BEFORE the mana balance
// pre-check, the session lookup and the SSE headers:
//
//	if s.CompanionSuspension != nil {
//	    st, err := s.CompanionSuspension.Status(repoCtx(r, tenantID, gcid), tenantID, actionCode)
//	    switch {
//	    case err != nil:
//	        // UNKNOWN: log loud and let the gateway decide (the projection is
//	        // advisory; it must not become a second, fail-closed control).
//	        log.Printf("consumption: companion suspension advisory read FAILED tenant=%s gcid=%s action=%s: %v (gateway remains the control)", ...)
//	    case st.Paused:
//	        writeJSONError(w, http.StatusForbidden, companion.ErrCodeCompanionSuspended,
//	            "your companion is paused ("+string(st.Scope)+"): "+st.Reason)   // before ANY claim / publish / debit
//	        return
//	    }
//	}
//
// The same Status() check, keyed on the reflection action code, goes in
// goal_knowledge_read_handler.go BEFORE requestGoalKnowledgeSynthesis's claim
// (ADR-252 Q5: skip the claim AND the publish, serve the tier-1 read with a
// `paused` status alongside fresh / reflecting / none).
//
// # Companion profile (GET /v1/me/companions/{id}, getCompanionInstance)
//
//	st, err := s.CompanionSuspension.Status(ctx, tenantID, companion.ChatTurnActionCodeForTier(tier))
//	resp.CompanionStatus = &companionStatusResp{Paused: st.Paused, Reason: st.Reason, Scope: string(st.Scope)}
//	// JSON: "companion_status": {"paused": true, "reason": "...", "scope": "platform"|"tenant"}
//	// (reason / scope omitted when not paused; on err render {"paused": false}
//	// and log loud, never 500 the profile over an advisory read).
//
// Pass "" as the action code to ask the whole-companion question (only
// all-skills suspensions count); pass the chat-turn code to ask about chat.
//
// # Delivery semantics
//
//   - AT-LEAST-ONCE: the inbox dedupes on event_id (fast path, per pod) and
//     the repository's monotonic source_version guard dedupes durably.
//   - NOT ORDER-PRESERVING: the guard applies an event only when strictly
//     newer for that suspension row; a late "engaged" can never overwrite a
//     newer "released" (or the reverse). A discarded-as-stale event is ACKed:
//     a normal outcome, not a fault.
//   - MALFORMED (bad JSON, unknown scope, missing reason / actor / version /
//     changed_at, wrong topic attribute): fail loud, error -> NACK -> retry ->
//     DLQ. Never silently dropped.
//
// # Wire mirror
//
// The body is protojson of governance.v1.CompanionSuspensionChanged exactly as
// observability's enqueueChanged marshals it (EmitUnpopulated: camelCase keys,
// int64 `version` as a JSON string, explicit `"engaged": false` on a release,
// a nested `envelope`). It is decoded with the generated type, which also
// accepts the proto field names and tolerates unknown fields.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	governancev1 "github.com/apollo-chora/chora-contracts/gen/go/chora/governance/v1"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const (
	// TopicCompanionSuspensionChanged is the source topic (ADR-252 D4,
	// JSON-wire, schema: null in chora-infra/topics/topics.yaml).
	TopicCompanionSuspensionChanged = "chora.governance.audit.companion_suspension_changed.v1"

	// DefaultCompanionSuspensionSubscription is the provisioned pull
	// subscription (companion_topic_estate.tf, ack 60).
	DefaultCompanionSuspensionSubscription = "chora-consumption.governance-audit-companion-suspension-changed"

	// CompanionSuspensionSubscriptionEnv overrides the subscription id.
	CompanionSuspensionSubscriptionEnv = "CHORA_COMPANION_SUSPENSION_SUBSCRIPTION"

	// companionSuspensionInboxKeyPrefix namespaces the inbox key.
	companionSuspensionInboxKeyPrefix = "companion_suspension_changed:"
)

// CompanionSuspensionSubscriptionName returns the subscription id to bind:
// the env override when set, else the provisioned default.
func CompanionSuspensionSubscriptionName() string {
	if v := strings.TrimSpace(os.Getenv(CompanionSuspensionSubscriptionEnv)); v != "" {
		return v
	}
	return DefaultCompanionSuspensionSubscription
}

// CompanionSuspensionProjector projects suspension changes.
type CompanionSuspensionProjector struct {
	applier companion.SuspensionApplier
	inbox   idempotent.Store
	ttl     time.Duration
}

// NewCompanionSuspensionProjector constructs the projector with an in-memory
// inbox (the durable dedupe is the repository's version guard). Panics on a
// nil applier so a wiring bug fails loud at boot rather than silently
// dropping every containment change at runtime.
func NewCompanionSuspensionProjector(applier companion.SuspensionApplier) *CompanionSuspensionProjector {
	return NewCompanionSuspensionProjectorWithStore(applier, idempotent.NewMemoryStore(), inboxTTL)
}

// NewCompanionSuspensionProjectorWithStore allows injecting the inbox + TTL.
func NewCompanionSuspensionProjectorWithStore(applier companion.SuspensionApplier, store idempotent.Store, ttl time.Duration) *CompanionSuspensionProjector {
	if applier == nil {
		panic("subscribers.NewCompanionSuspensionProjector: nil SuspensionApplier")
	}
	if store == nil {
		store = idempotent.NewMemoryStore()
	}
	if ttl <= 0 {
		ttl = inboxTTL
	}
	return &CompanionSuspensionProjector{applier: applier, inbox: store, ttl: ttl}
}

// Handle projects one decoded change. nil = ACK (applied, stale, or already
// seen); error = NACK (invalid event or a failed apply, which the inbox does
// not claim so the broker retry re-runs it).
func (p *CompanionSuspensionProjector) Handle(ctx context.Context, ev companion.SuspensionChanged) error {
	if p == nil || p.applier == nil {
		return errors.New("subscribers: companion suspension projector not initialised")
	}
	if err := ev.Validate(); err != nil {
		// FAIL LOUD -> NACK -> retry -> DLQ. A malformed containment event is a
		// producer bug; swallowing it would diverge this read from the
		// operator's decision.
		return fmt.Errorf("subscribers: companion_suspension_changed invalid event %s: %w", ev.EventID, err)
	}
	return p.inbox.Process(ctx, companionSuspensionInboxKeyPrefix+ev.EventID, p.ttl, func() error {
		applied, err := p.applier.Apply(ctx, ev)
		if err != nil {
			return fmt.Errorf("subscribers: companion_suspension_changed apply suspension=%s version=%d event=%s: %w",
				ev.SuspensionID, ev.Version, ev.EventID, err)
		}
		if !applied {
			// Out-of-order or duplicate redelivery: a state at least as new is
			// already projected. Discarding is the CORRECT outcome; ACK it.
			log.Printf("consumption: companion suspension projection SKIPPED stale event (suspension=%s scope=%s tenant=%s skill=%q version=%d event=%s): a newer state is already projected",
				ev.SuspensionID, ev.Scope, ev.TenantID, ev.SkillKey, ev.Version, ev.EventID)
			return nil
		}
		state := "ENGAGED"
		if !ev.Engaged {
			state = "RELEASED"
		}
		log.Printf("consumption: companion suspension projection APPLIED %s (suspension=%s scope=%s tenant=%s skill=%q version=%d actor=%s reason=%q event=%s)",
			state, ev.SuspensionID, ev.Scope, ev.TenantID, ev.SkillKey, ev.Version, ev.ActorGCID, ev.Reason, ev.EventID)
		return nil
	})
}

// PullHandler adapts the projector to an eventbus.Handler for
// eventbus.Subscribe: decode (fail loud) then Handle.
func (p *CompanionSuspensionProjector) PullHandler() eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if p == nil {
			return errors.New("subscribers: companion suspension pull handler not initialised")
		}
		ev, err := DecodeCompanionSuspensionChanged(msg)
		if err != nil {
			return err
		}
		return p.Handle(ctx, ev)
	}
}

// DecodeCompanionSuspensionChanged decodes one delivered message into the
// domain event. Mandatory fields are checked by companion.SuspensionChanged.
// Validate (called by Handle); this function only fails on a structurally bad
// message (another topic's subject, unparseable body).
//
// Precedence for the fields the envelope also carries: the body is the
// source, the bus envelope is the fallback (event_id, actor gcid,
// changed_at <- occurred_at). The envelope's tenant_id is NEVER used: for
// platform scope it is the nil-UUID sentinel, and the projection keys on the
// body's empty tenant_id.
func DecodeCompanionSuspensionChanged(msg eventbus.Message) (companion.SuspensionChanged, error) {
	if msg.Subject != "" && msg.Subject != TopicCompanionSuspensionChanged {
		return companion.SuspensionChanged{}, fmt.Errorf("subscribers: companion_suspension_changed: message is stamped topic %q, want %s (mis-bound subscription)", msg.Subject, TopicCompanionSuspensionChanged)
	}
	var body governancev1.CompanionSuspensionChanged
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(msg.Payload, &body); err != nil {
		return companion.SuspensionChanged{}, fmt.Errorf("subscribers: companion_suspension_changed: decode body: %w", err)
	}

	eventID := msg.Envelope.EventID
	if eventID == "" {
		eventID = body.GetEnvelope().GetEventId()
	}
	actor := body.GetActorGcid()
	if actor == "" {
		actor = msg.Envelope.GCID
	}
	var changedAt time.Time
	if ts := body.GetChangedAt(); ts != nil && ts.IsValid() {
		changedAt = ts.AsTime()
	} else if !msg.Envelope.OccurredAt.IsZero() {
		changedAt = msg.Envelope.OccurredAt
	}
	occurredAt := msg.Envelope.OccurredAt
	if occurredAt.IsZero() {
		if ts := body.GetEnvelope().GetOccurredAt(); ts != nil && ts.IsValid() {
			occurredAt = ts.AsTime()
		}
	}

	return companion.SuspensionChanged{
		EventID:      eventID,
		SuspensionID: body.GetSuspensionId(),
		Scope:        companion.SuspensionScope(body.GetScope()),
		TenantID:     body.GetTenantId(),
		SkillKey:     body.GetSkillKey(),
		Engaged:      body.GetEngaged(),
		Version:      body.GetVersion(),
		Reason:       body.GetReason(),
		ActorGCID:    actor,
		ChangedAt:    changedAt,
		OccurredAt:   occurredAt,
	}, nil
}
