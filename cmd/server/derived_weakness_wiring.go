// derived_weakness_wiring.go — composition root for the Epic-1b Growth-Edge
// DERIVED paths (W3-derived).
//
// Builds the DerivedWeaknessProjector that folds performance evidence into the
// LearnerWeakness aggregate from two live deliveries:
//
//   - chora.delivery.live_quiz_session.score_awarded.v1 → its own push inbox
//     /api/internal/pubsub/live-quiz-score-awarded (source=classroom);
//   - chora.consumption.atom_session.completed.v1 → fanned out of the EXISTING
//     companion-growth-source-events push (source=derived) — main passes the
//     projector into wireGrowthPushHandlers, so growth EXP + derived weakness
//     ride the same delivery.
//
// Dependencies (fail-soft at boot, log + skip — exactly like weakness_wiring):
//   - pgx pool       → pg LearnerWeakness repo + pg topic_accuracy projection.
//   - srv.Embedder   → concept-label embeddings for the Upsert dedup fold.
//   - ext.AtomIndex  → eligibility + tag fallback. MUST run AFTER
//     wireAtomIndexRepo so the pg-backed projection is bound.
//   - ext.Retention  → optional Ebbinghaus sharpening of DerivedStrength
//     (the topic_retention.Repository port — pg-backed + durable once
//     wireTopicRetentionRepo runs before this; in-memory fallback when no pool.
//     not-found / error ⇒ accuracy-only is the safe floor).
package main

import (
	"errors"

	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// wireDerivedWeakness builds the projector + the live-quiz push inbox.
// Returns the projector (nil when prerequisites are missing) so main can also
// hand it to wireGrowthPushHandlers for the atom_session.completed fan-out.
// waveOneXP (F-I3, CHO-2090) is the Wave-1 companion-EXP subscriber teed onto
// the submission-graded-derived dispatch (award AFTER derivation); nil keeps
// the legacy derived-only contract with a loud log.
func wireDerivedWeakness(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool, waveOneXP *subscribers.WaveOneXPSubscriber, bus eventbus.Bus) *subscribers.DerivedWeaknessProjector {
	if pool == nil {
		log.Printf("consumption: derived-weakness projector NOT wired (no pgx pool)")
		return nil
	}
	if srv.Embedder == nil {
		log.Printf("consumption: derived-weakness projector NOT wired (no embedder, set CHORA_MODEL_GATEWAY_GRPC_URL)")
		return nil
	}

	// W4: the write repo carries the drill-atom cache decorator when the
	// creation ContentRetrieval gRPC is configured.
	repo := learnerWeaknessWriteRepo(pool)
	embedder := clients.NewLearnerWeaknessEmbedder(srv.Embedder)
	txRunner := pg.NewPgxTxRunner(pool)
	accuracy := pg.NewTopicAccuracyRepo(txRunner)
	// CHO-2129 atomic fold: session-dedup + attempt + weakness write + the
	// ADR-196 weakness.grown.v1 reward all commit or roll back together —
	// RunAmbient opens ONE transaction every pg repo call inside the fold
	// joins (tx_ambient.go), and the grown reward rides it via the same-tx
	// outbox enqueuer (byte-identical rows to ext.Publisher's post-commit
	// path; the dispatcher drains both).
	projector := subscribers.NewDerivedWeaknessProjector(repo, embedder, accuracy).
		WithAtomIndex(ext.AtomIndex).
		WithRetention(retentionReaderAdapter{repo: ext.Retention}).
		WithGrownOutbox(pg.NewTxOutbox("learner_weakness")).
		WithFoldTx(txRunner.RunAmbient)

	if bus == nil {
		log.Printf("consumption: derived-weakness projector NOT wired (no event bus, NATS_URL unset)")
		return projector
	}
	go func() {
		log.Printf("consumption: live-quiz-score subscriber binding chora.delivery.live_quiz_session.score_awarded.v1 (W3-derived classroom; atom_index=%T)", ext.AtomIndex)
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.live-quiz-score", subscribers.TopicLiveQuizScoreAwarded), subscribers.LiveQuizScoreHandler(projector)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: live-quiz-score subscriber exited: %v", err)
		}
	}()

	// WS-6 (ADR-205, CHO-1958) — second evidence source: chora.delivery.
	// submission.graded.v1 → derived Growth Edge keyed on the assessment title.
	// Reuses the SAME projector (one aggregate, one outbox) via a dedicated push
	// subscription/endpoint (the topic's LearnerProfile endpoint is separate).
	// F-I3 (CHO-2090): the same delivery ALSO awards the flat submission_graded
	// companion EXP when the Wave-1 subscriber is wired (nil ⇒ derived-only).
	if waveOneXP == nil {
		log.Printf("consumption: submission-graded-derived runs WITHOUT the wave-1 XP leg (growth service absent) — submission_graded awards nothing")
	}
	go func() {
		log.Printf("consumption: submission-graded-derived subscriber binding chora.delivery.submission.graded.v1 (WS-6 derived submission evidence; wave-1 XP leg wired=%t)", waveOneXP != nil)
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.submission-graded-derived", subscribers.TopicSubmissionGraded), subscribers.SubmissionGradedDerivedHandler(projector, waveOneXP)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: submission-graded-derived subscriber exited: %v", err)
		}
	}()
	return projector
}

// retentionReaderAdapter adapts the EXT TopicRetention repo (the
// topic_retention.Repository port — in-memory or pg) onto the projector's
// RetentionReader port (nil-safe; not-found / error ⇒ accuracy-only).
type retentionReaderAdapter struct {
	repo topic_retention.Repository
}

func (r retentionReaderAdapter) RetentionAt(ctx context.Context, tenantID, gcid, topic string, now time.Time) (float64, bool) {
	if r.repo == nil {
		return 0, false
	}
	score, err := r.repo.Get(ctx, tenantID, gcid, topic)
	if err != nil || score == nil {
		return 0, false
	}
	return score.RetentionAt(now), true
}
