// weakness_review_pending_subscriber.go — the bounded HITL review-panel bridge
// (ADR-205 D4 / CHO-1973). Consumes chora.consumption.weakness.review_pending.v1
// (emitted by the graduated ai-kernel weakness-analyser crew when its LangGraph
// flow PAUSES at the bounded HITL interrupt) and PARKS the originating upload
// job in AWAITING_REVIEW with the full review panel stored verbatim, so the A+ FE
// can poll GET .../uploads/{id} and drive the bounded accept/reject/merge
// decision (which resumes the SAME orchestrator thread on {tenant, upload}).
//
// Per ddd-enforcement chora-consumption never reads chora_ai_kernel — this event
// IS the cross-domain seam (the panel arrives on the wire, never via a cross-DB
// read). The orchestrator owns proposed_edge_id; this service treats it OPAQUELY
// (stores + echoes it, never mints or interprets it).
//
// Idempotency: keyed on (tenant, gcid, upload_id) via idempotent.Store.Process —
// the dedup key COMMITS ONLY when the park succeeds, so a transient pg failure
// leaves the key unclaimed and Pub/Sub redelivery reprocesses (no lost panel). A
// redelivered review_pending for an already-parked upload is skipped; the repo's
// status-guarded UPDATE makes the rare concurrent reprocess a harmless no-op.
package subscribers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// WeaknessReviewPendingPayload mirrors the decoded
// chora.consumption.weakness.review_pending.v1 body (the push handler maps the
// binary proto onto this). Panel is the bounded HITL panel stored verbatim.
type WeaknessReviewPendingPayload struct {
	UploadID    string
	TenantID    string
	LearnerGCID string
	Panel       *wu.ReviewPanel
	PendingAt   time.Time
}

// ReviewParker parks an upload job at the bounded HITL interrupt (flips
// QUEUED/ANALYZING → AWAITING_REVIEW and stores the panel). Required — the
// subscriber has nothing to do without it. The pg weakness_upload.Repository
// satisfies it structurally (it is NOT on the Repository port; mirrors the
// analyzed subscriber's JobCompleter + the repo's MarkBlobShredded precedent).
type ReviewParker interface {
	MarkAwaitingReview(ctx context.Context, learnerGCID, uploadID string, panel *wu.ReviewPanel, now time.Time) error
}

// WeaknessReviewPendingSubscriber parks the upload job in AWAITING_REVIEW and
// stores the bounded HITL panel for the A+ poll.
type WeaknessReviewPendingSubscriber struct {
	parker ReviewParker
	inbox  idempotent.Store
	ttl    time.Duration
}

// NewWeaknessReviewPendingSubscriber constructs the subscriber with an in-memory
// inbox (production wires a PostgresStore via …WithStore for cross-pod dedup).
func NewWeaknessReviewPendingSubscriber(parker ReviewParker) *WeaknessReviewPendingSubscriber {
	return NewWeaknessReviewPendingSubscriberWithStore(parker, idempotent.NewMemoryStore(), inboxTTL)
}

// NewWeaknessReviewPendingSubscriberWithStore allows injecting a durable inbox + TTL.
func NewWeaknessReviewPendingSubscriberWithStore(parker ReviewParker, store idempotent.Store, ttl time.Duration) *WeaknessReviewPendingSubscriber {
	if store == nil {
		store = idempotent.NewMemoryStore()
	}
	if ttl <= 0 {
		ttl = inboxTTL
	}
	return &WeaknessReviewPendingSubscriber{parker: parker, inbox: store, ttl: ttl}
}

// Handle parks the upload job in AWAITING_REVIEW with the panel. The whole park
// is one idempotent unit — the dedup key commits only on success.
func (s *WeaknessReviewPendingSubscriber) Handle(ctx context.Context, env events.Envelope, p WeaknessReviewPendingPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(p.UploadID) == "" {
		return errors.New("subscribers: weakness.review_pending payload missing upload_id")
	}
	if s.parker == nil {
		// Fail loud — a wired subscriber with no parker is a boot misconfiguration,
		// never a silent ack/drop (that would strand the learner at the interrupt).
		return errors.New("subscribers: weakness.review_pending has no ReviewParker wired")
	}
	key := "weakness.review_pending:" + p.TenantID + "|" + p.LearnerGCID + "|" + p.UploadID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		// Tenant + GCID on ctx so the pg repo's rls.ApplySession scopes correctly.
		rctx := tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)
		return s.parker.MarkAwaitingReview(rctx, p.LearnerGCID, p.UploadID, p.Panel, env.OccurredAt)
	})
}
