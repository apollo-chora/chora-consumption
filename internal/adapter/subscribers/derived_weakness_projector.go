// derived_weakness_projector.go — W3-derived of the Epic-1b Growth-Edge track.
//
// Projects PERFORMANCE evidence into the LearnerWeakness aggregate, completing
// the source triad next to the explicit upload path (weakness_analyzed_subscriber):
//
//   - chora.delivery.live_quiz_session.score_awarded.v1  → source=classroom
//     (CR2-C3 atoms-as-questions; the event denormalises the linked atom's
//     topic_tags so no cross-DB read into chora_creation is ever needed)
//   - chora.consumption.atom_session.completed.v1        → source=derived
//     (same eligibility as TopicAccuracySubscriber: MCQ atoms with a topic)
//   - chora.delivery.submission.graded.v1                → source=derived
//     (WS-6: whole-submission accuracy keyed on the assessment title)
//
// ATOMIC FOLD (CHO-2129): the pre-fix shape claimed durable session-dedup
// (MarkSessionSeen, its own tx) BEFORE the fold — a transient failure after the
// claim NACK'd and the redelivery was ack-dropped as seen, permanently losing
// the fold (survives pod restarts; fired on every graded attempt). The projector
// now runs
//
//	eligibility reads → LLM embed (pre-claim; skipped when the prediction
//	says the recover branch) → ONE fold transaction { session-dedup →
//	RecordAttempt → authoritative accuracy read → Upsert / Recover →
//	weakness.grown.v1 via the SAME-TX outbox }
//
// via the FoldTx seam (production wires pg.PgxTxRunner.RunAmbient — every repo
// in adapter/repo/pg joins the ambient transaction, tx_ambient.go). A failure
// ANYWHERE inside the fold rolls back everything including the dedup row, so
// the redelivery re-processes cleanly; a success commits the dedup row that
// gates double-counting. The envelope-id + domain-key trackers are
// seen()/mark() (CHO-2107): read before, mark only after success — never
// burned by a failure.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

// derivedSessionDedupID mints the derived projector's session-dedup key as a
// deterministic UUIDv5 of "derived:"+sessionID. The namespace keeps this dedup
// row from colliding with TopicAccuracySubscriber's plain-session-id row for
// the SAME completion in the SAME topic_accuracy_session_dedup table — but
// that table's atom_session_id column is UUID-typed, so the namespace must be
// folded into a uuid rather than string-prefixed (a bare "derived:"+id 22P02s
// at the pg layer and the push NACK-loops with no DLQ).
func derivedSessionDedupID(sessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("derived:"+sessionID)).String()
}

// LiveQuizScoreAwardedPayload mirrors the consumed fields of
// chora.delivery.live_quiz_session.score_awarded.v1 (decoded at the push
// handler; binary per the delivery protomarshal flat schema).
type LiveQuizScoreAwardedPayload struct {
	SessionID   string
	LiveQuizID  string
	QuestionID  string
	AtomID      string // empty for ad-hoc (non-atom-linked) questions
	TopicTags   []string
	Correct     bool
	TenantID    string
	LearnerGCID string
}

// AccuracyRecorder is the DURABLE topic_accuracy role this projector needs —
// the domain port plus the DB-level (gcid, session_id) dedup that only a real
// database can provide. Inside the fold transaction every call joins the
// ambient tx (pg tx_ambient.go).
//
// CHO-2167 (interface segregation): the shared read/write surface now comes
// from topic_accuracy.Repository — the same port the dose composer and
// TopicAccuracySubscriber depend on — so there is ONE definition of what the
// projection is, and the pg adapter provably satisfies both roles. Only
// MarkSessionSeen is extra, and only pg satisfies it: durable dedup against a
// heap map would be a lie.
type AccuracyRecorder interface {
	topic_accuracy.Repository

	MarkSessionSeen(ctx context.Context, tenantID, gcid, sessionID string) (bool, error)
}

// RetentionReader surfaces the Ebbinghaus retention score for a topic when one
// exists (false otherwise). Optional — the wiring adapts the in-memory
// TopicRetention repo; absent, DerivedStrength runs accuracy-only.
type RetentionReader interface {
	RetentionAt(ctx context.Context, tenantID, gcid, topic string, now time.Time) (float64, bool)
}

// GrownOutbox is the SAME-TX outbox port for the ADR-196 weakness.grown.v1
// reward emit (CHO-2129 AC4). Production wires pg.TxOutbox — the row INSERT
// joins the ambient fold transaction, so the reward commits or rolls back WITH
// the recovery UPDATE that earned it (no post-commit best-effort emit loss).
type GrownOutbox interface {
	PublishGrownInTx(ctx context.Context, topic string, env events.Envelope, payload map[string]any) error
}

// FoldTx is the atomic-fold transaction seam: fn's writes (session-dedup +
// attempt + weakness rows + grown outbox) commit or roll back together.
// Production wires pg.PgxTxRunner.RunAmbient. When unset the fold runs
// non-atomically (unit-test / in-memory wiring shape) — wireDerivedWeakness
// ALWAYS installs the pg runner (it requires a pool to wire at all).
type FoldTx func(ctx context.Context, fn func(ctx context.Context) error) error

// DerivedWeaknessProjector folds performance evidence into Growth Edges.
type DerivedWeaknessProjector struct {
	weaknesses  lw.Repository
	embedder    lw.Embedder
	accuracy    AccuracyRecorder
	atoms       atom_index.Repo // optional: tag fallback (live-quiz) + eligibility (atom-session)
	retention   RetentionReader // optional: sharpens DerivedStrength
	grownOutbox GrownOutbox     // optional: ADR-196 weakness.grown.v1 same-tx reward emit (nil ⇒ no reward event)
	foldTx      FoldTx          // atomic-fold seam (nil ⇒ direct run; production always injects)
	threshold   float64
	envIDs      *idempotencyTracker
	awards      *idempotencyTracker
	submissions *idempotencyTracker // WS-6 per-submission domain dedup
}

// NewDerivedWeaknessProjector constructs the projector with in-memory dedup
// trackers (per-pod, seen()/mark() — the atom-session path additionally dedups
// durably via AccuracyRecorder.MarkSessionSeen INSIDE the fold transaction).
func NewDerivedWeaknessProjector(repo lw.Repository, embedder lw.Embedder, accuracy AccuracyRecorder) *DerivedWeaknessProjector {
	return &DerivedWeaknessProjector{
		weaknesses:  repo,
		embedder:    embedder,
		accuracy:    accuracy,
		threshold:   lw.DerivedAccuracyThreshold,
		envIDs:      newIdempotencyTracker(),
		awards:      newIdempotencyTracker(),
		submissions: newIdempotencyTracker(),
	}
}

// WithAtomIndex attaches the atom projection (builder-style, nil-safe).
func (p *DerivedWeaknessProjector) WithAtomIndex(atoms atom_index.Repo) *DerivedWeaknessProjector {
	p.atoms = atoms
	return p
}

// WithRetention attaches the Ebbinghaus retention reader (builder-style, nil-safe).
func (p *DerivedWeaknessProjector) WithRetention(r RetentionReader) *DerivedWeaknessProjector {
	p.retention = r
	return p
}

// WithGrownOutbox attaches the same-tx outbox for the ADR-196
// weakness.grown.v1 reward event (builder-style, nil-safe). Without it the
// recover paths still run — the reward event is simply not emitted (the basic
// grow still persists via the recovery UPDATE).
func (p *DerivedWeaknessProjector) WithGrownOutbox(o GrownOutbox) *DerivedWeaknessProjector {
	p.grownOutbox = o
	return p
}

// WithFoldTx installs the atomic-fold transaction seam (builder-style).
func (p *DerivedWeaknessProjector) WithFoldTx(tx FoldTx) *DerivedWeaknessProjector {
	p.foldTx = tx
	return p
}

// runFold executes fn under the fold transaction seam (direct when unset).
func (p *DerivedWeaknessProjector) runFold(ctx context.Context, fn func(ctx context.Context) error) error {
	if p.foldTx == nil {
		return fn(ctx)
	}
	return p.foldTx(ctx, fn)
}

// prepareEmbedding runs the PRE-CLAIM embed (CHO-2129 AC3): a transient LLM
// failure NACKs before any dedup row exists, so the redelivery retries it.
// The embed is SKIPPED when the pre-tx prediction says the recover branch
// (correct answer + rolling accuracy already at/above the threshold, or no
// prior row — a lone correct attempt reads 1.0): a rising accuracy cannot
// cross DOWN through the threshold, so the fold won't need an embedding. Every
// other case pre-embeds; the rare prediction miss (a concurrent same-topic
// attempt between the read and the fold) falls back to an in-tx embed rather
// than livelocking.
func (p *DerivedWeaknessProjector) prepareEmbedding(ctx context.Context, tenantID, gcid, topic string, isCorrect bool) ([]float32, error) {
	if isCorrect {
		accMap, err := p.accuracy.GetByLearner(ctx, tenantID, gcid)
		if err != nil {
			return nil, fmt.Errorf("subscribers: read topic accuracy: %w", err)
		}
		if base, ok := accMap[topic]; !ok || base >= p.threshold {
			return nil, nil // predicted recover — the fold needs no embedding
		}
	}
	embedding, err := p.embedder.Embed(ctx, topic, tenantID)
	if err != nil {
		return nil, fmt.Errorf("subscribers: embed derived concept %q: %w", topic, err)
	}
	return embedding, nil
}

// HandleLiveQuizScoreAwarded projects one graded classroom answer. No-ops
// (acks) on: ad-hoc questions (no atom_id), unmappable evidence (no topic tags
// and no atom-index hit), and duplicates.
func (p *DerivedWeaknessProjector) HandleLiveQuizScoreAwarded(ctx context.Context, env events.Envelope, in LiveQuizScoreAwardedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if p.envIDs.seen(env.EventID) {
		return nil // at-least-once redelivery
	}
	tenantID, gcid := fallbackIdentity(in.TenantID, in.LearnerGCID, env)
	if strings.TrimSpace(in.AtomID) == "" {
		p.envIDs.mark(env.EventID)
		return nil // ad-hoc question — no atom-mastery signal
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)

	tags := cleanTopicTags(in.TopicTags)
	if len(tags) == 0 && p.atoms != nil {
		// Transient-vs-notfound (CHO-2130): ErrNotFound = genuinely tagless /
		// unhydrated atom — the unmappable ack-drop below stands. Any OTHER
		// error is transient infra — NACK unmarked (silently proceeding
		// dropped countable classroom evidence as "unmappable").
		atom, err := p.atoms.Get(ctx, in.AtomID)
		if err != nil && !errors.Is(err, atom_index.ErrNotFound) {
			return fmt.Errorf("subscribers: live-quiz tag fallback atom lookup %q: %w", in.AtomID, err)
		}
		if atom != nil {
			tags = cleanTopicTags(atom.TopicTags)
		}
	}
	if len(tags) == 0 {
		p.envIDs.mark(env.EventID)
		return nil // unmappable — nothing to score against
	}

	// Domain dedup: one award = one (session, question, atom) tuple; a
	// republished award with a fresh event_id must not double-count.
	awardKey := "lqaward:" + tenantID + "|" + gcid + "|" + in.SessionID + "|" + in.QuestionID + "|" + in.AtomID
	if p.awards.seen(awardKey) {
		p.envIDs.mark(env.EventID)
		return nil
	}

	embedding, err := p.prepareEmbedding(ctx, tenantID, gcid, tags[0], in.Correct)
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("Live-quiz answers on “%s” are landing below the mastery bar.", tags[0])
	if err := p.runFold(ctx, func(txCtx context.Context) error {
		for _, tag := range tags {
			if err := p.accuracy.RecordAttempt(txCtx, tenantID, gcid, tag, in.Correct); err != nil {
				return fmt.Errorf("subscribers: record live-quiz attempt %q: %w", tag, err)
			}
		}
		return p.foldTopicEvidence(txCtx, env, tenantID, gcid, foldEvidence{
			topic: tags[0], tags: tags, atomID: in.AtomID,
			source: lw.SourceClassroom, summary: summary, embedding: embedding,
		})
	}); err != nil {
		return err
	}
	p.envIDs.mark(env.EventID)
	p.awards.mark(awardKey)
	return nil
}

// HandleAtomSessionCompleted projects one completed self-paced MCQ session
// (same payload + eligibility as TopicAccuracySubscriber).
func (p *DerivedWeaknessProjector) HandleAtomSessionCompleted(ctx context.Context, env events.Envelope, in AtomSessionCompletedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if p.envIDs.seen(env.EventID) {
		return nil
	}
	tenantID, gcid := fallbackIdentity(in.TenantID, in.LearnerGCID, env)
	if p.atoms == nil {
		return nil // eligibility needs the atom projection — fail-soft ack
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)

	// Transient-vs-notfound (CHO-2130, mirrors TopicAccuracySubscriber):
	// ErrNotFound = out-of-order projection hydration — ack (unmarked, so a
	// republish after hydration may still count). Any OTHER error is
	// transient infra — NACK so the redelivery re-folds instead of the
	// evidence being ack-dropped.
	atom, err := p.atoms.Get(ctx, in.AtomID)
	if err != nil && !errors.Is(err, atom_index.ErrNotFound) {
		return fmt.Errorf("subscribers: derived eligibility atom lookup %q: %w", in.AtomID, err)
	}
	if atom == nil {
		return nil // out-of-order projection hydration — ack
	}
	if !atom.IsMCQ() {
		p.envIDs.mark(env.EventID)
		return nil // text/video atoms are not gradable
	}
	topic := strings.TrimSpace(atom.PrimaryTopic())
	if topic == "" {
		p.envIDs.mark(env.EventID)
		return nil // unclassified atom
	}

	// PRE-CLAIM embed (AC3) — a transient LLM failure leaves no dedup row.
	embedding, err := p.prepareEmbedding(ctx, tenantID, gcid, topic, in.IsCorrect)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("Recent practice on “%s” is below the mastery bar.", topic)
	if err := p.runFold(ctx, func(txCtx context.Context) error {
		// DB-level domain dedup (tenant, gcid, session) — durable across replicas
		// AND now atomic with the fold: a rollback releases the claim (AC1); a
		// commit makes it gate the redelivery (AC2).
		if strings.TrimSpace(in.SessionID) != "" {
			firstSeen, err := p.accuracy.MarkSessionSeen(txCtx, tenantID, gcid, derivedSessionDedupID(in.SessionID))
			if err != nil {
				return fmt.Errorf("subscribers: derived session dedup: %w", err)
			}
			if !firstSeen {
				return nil // duplicate — commit the no-op
			}
		}
		if err := p.accuracy.RecordAttempt(txCtx, tenantID, gcid, topic, in.IsCorrect); err != nil {
			return fmt.Errorf("subscribers: record session attempt %q: %w", topic, err)
		}
		return p.foldTopicEvidence(txCtx, env, tenantID, gcid, foldEvidence{
			topic: topic, tags: cleanTopicTags(atom.TopicTags), atomID: in.AtomID,
			source: lw.SourceDerived, summary: summary, embedding: embedding,
		})
	}); err != nil {
		return err
	}
	p.envIDs.mark(env.EventID)
	return nil
}

// SubmissionGradedEvidence mirrors the consumed fields of
// chora.delivery.submission.graded.v1 (decoded at the push handler). Named to
// avoid colliding with subscribers.SubmissionGradedPayload (the LearnerProfile
// read-model DTO for the SAME topic). PointsEarned/PointsPossible give the
// whole-submission accuracy; AssessmentTitle is the only concept-bearing signal.
type SubmissionGradedEvidence struct {
	SubmissionID    string
	AssessmentID    string
	AssessmentTitle string
	LearnerGCID     string
	TenantID        string
	PointsEarned    float64
	PointsPossible  int
}

// HandleSubmissionGraded projects one graded assessment submission into the
// Growth-Edge aggregate (WS-6, ADR-205 — the SECOND performance-evidence source
// after the classroom/atom-session paths). A whole-submission grade carries no
// per-atom topic, so it keys a DERIVED edge on the assessment TITLE with the
// accuracy taken DIRECTLY from the event (not the topic_accuracy read-back the
// classroom/atom-session tail uses — a submission is not a single MCQ attempt).
//
// No-ops (acks) on: a blank assessment title (no concept to key on — unmappable
// evidence, mirroring the ad-hoc/topicless acks above), a zero-point submission
// (no denominator), and duplicates (envelope event_id + per-submission domain
// key, both seen()/mark()). Below the mastery bar → embed (pre-fold) + Upsert;
// at/above → RecoverByConceptKey (Ebbinghaus auto-recovery; only ever lowers,
// may flip active→grown) with the grown reward riding the fold tx.
func (p *DerivedWeaknessProjector) HandleSubmissionGraded(ctx context.Context, env events.Envelope, in SubmissionGradedEvidence) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if p.envIDs.seen(env.EventID) {
		return nil // at-least-once redelivery
	}
	tenantID, gcid := fallbackIdentity(in.TenantID, in.LearnerGCID, env)

	label := strings.TrimSpace(in.AssessmentTitle)
	if label == "" {
		p.envIDs.mark(env.EventID)
		return nil // no concept-bearing signal — unmappable evidence
	}
	if in.PointsPossible <= 0 {
		p.envIDs.mark(env.EventID)
		return nil // no denominator — cannot score
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)

	// Domain dedup: one submission grades once (a retry is a NEW submission id);
	// a republish with a fresh event_id must not double-fold. In-memory per pod
	// (seen before, marked only after success), mirroring the live-quiz award
	// tracker; the Upsert embedding-dedup fold is the durable backstop.
	subKey := ""
	if id := strings.TrimSpace(in.SubmissionID); id != "" {
		subKey = "submgraded:" + tenantID + "|" + gcid + "|" + id
		if p.submissions.seen(subKey) {
			p.envIDs.mark(env.EventID)
			return nil
		}
	}

	accuracy := in.PointsEarned / float64(in.PointsPossible)
	// A whole-assessment grade has no per-topic Ebbinghaus retention score —
	// the miss-rate stands alone.
	strength := lw.DerivedStrength(accuracy, 0, false)
	conceptKey := lw.NormalizeConceptKey(label)

	if accuracy >= p.threshold {
		if err := p.runFold(ctx, func(txCtx context.Context) error {
			grownCK, err := p.weaknesses.RecoverByConceptKey(txCtx, gcid, conceptKey, strength, env.OccurredAt)
			if err != nil {
				return fmt.Errorf("subscribers: recover assessment growth edge %q: %w", label, err)
			}
			return p.publishGrown(txCtx, env, tenantID, gcid, grownCK, lw.RecoverySourceConceptKey)
		}); err != nil {
			return err
		}
		p.envIDs.mark(env.EventID)
		if subKey != "" {
			p.submissions.mark(subKey)
		}
		return nil
	}

	// Below the bar — the branch is exact (the accuracy comes from the event),
	// so the embed always runs, and always PRE-fold (AC3).
	embedding, err := p.embedder.Embed(ctx, label, tenantID)
	if err != nil {
		return fmt.Errorf("subscribers: embed assessment concept %q: %w", label, err)
	}
	summary := fmt.Sprintf("Assessment “%s” scored below the mastery bar.", label)
	if err := p.runFold(ctx, func(txCtx context.Context) error {
		if _, err := p.weaknesses.Upsert(txCtx, lw.UpsertInput{
			TenantID:     tenantID,
			LearnerGCID:  gcid,
			ConceptKey:   conceptKey,
			ConceptLabel: label,
			Embedding:    embedding,
			Strength:     strength,
			Source:       lw.SourceDerived,
			Descriptor:   lw.Descriptor{Summary: summary},
			Now:          env.OccurredAt,
		}); err != nil {
			return fmt.Errorf("subscribers: upsert assessment growth edge %q: %w", label, err)
		}
		return nil
	}); err != nil {
		return err
	}
	p.envIDs.mark(env.EventID)
	if subKey != "" {
		p.submissions.mark(subKey)
	}
	return nil
}

// foldEvidence carries the shared-tail inputs into the fold transaction.
type foldEvidence struct {
	topic     string
	tags      []string
	atomID    string
	source    lw.Source
	summary   string
	embedding []float32 // nil ⇒ the pre-tx prediction said recover (in-tx fallback embeds on a branch flip)
}

// foldTopicEvidence is the shared IN-TX tail: read back the authoritative
// rolling accuracy for the primary topic (the same-tx read includes the
// just-recorded attempt), blend in retention, then upsert (below threshold) or
// recover (at/above threshold) with the grown reward riding the same tx.
func (p *DerivedWeaknessProjector) foldTopicEvidence(ctx context.Context, env events.Envelope, tenantID, gcid string, ev foldEvidence) error {
	accMap, err := p.accuracy.GetByLearner(ctx, tenantID, gcid)
	if err != nil {
		return fmt.Errorf("subscribers: read topic accuracy: %w", err)
	}
	accuracy, ok := accMap[ev.topic]
	if !ok {
		// The projection has no row yet (e.g. stub repo in tests) — treat the
		// single observed attempt as the whole signal: a miss scores 0.
		accuracy = 0
	}

	retention, hasRetention := 0.0, false
	if p.retention != nil {
		retention, hasRetention = p.retention.RetentionAt(ctx, tenantID, gcid, ev.topic, env.OccurredAt)
	}
	strength := lw.DerivedStrength(accuracy, retention, hasRetention)

	if accuracy >= p.threshold {
		// Ebbinghaus recovery — only ever lowers; absent edge is a no-op. Any
		// edge that crosses active→grown is rewarded with weakness.grown.v1 —
		// through the SAME-TX outbox, so the reward commits with the recovery.
		grownCK, err := p.weaknesses.RecoverByConceptKey(ctx, gcid, lw.NormalizeConceptKey(ev.topic), strength, env.OccurredAt)
		if err != nil {
			return fmt.Errorf("subscribers: recover growth edge %q: %w", ev.topic, err)
		}
		if err := p.publishGrown(ctx, env, tenantID, gcid, grownCK, lw.RecoverySourceConceptKey); err != nil {
			return err
		}
		// W4 drill-completion recovery: a well-answered suggested-drill atom also
		// recovers the EXPLICIT edge(s) that cached it. An explicit edge's
		// analyser-minted concept_key never equals the atom's broad primary topic,
		// so this is the ONLY path that closes the upload→practice→grow loop for
		// uploaded edges. No-op when atomID is empty or no edge cached it.
		if strings.TrimSpace(ev.atomID) != "" {
			grownDA, err := p.weaknesses.RecoverByDrillAtomID(ctx, gcid, ev.atomID, strength, env.OccurredAt)
			if err != nil {
				return fmt.Errorf("subscribers: recover drill edge for atom %q: %w", ev.atomID, err)
			}
			if err := p.publishGrown(ctx, env, tenantID, gcid, grownDA, lw.RecoverySourceDrillAtom); err != nil {
				return err
			}
		}
		return nil
	}

	embedding := ev.embedding
	if embedding == nil {
		// The pre-tx prediction said recover but the authoritative in-tx read
		// disagrees (a concurrent same-topic attempt raced the fold). Embed
		// inside the tx — rare and bounded, and strictly better than a
		// deterministic NACK loop (the redelivery would predict identically).
		if embedding, err = p.embedder.Embed(ctx, ev.topic, tenantID); err != nil {
			return fmt.Errorf("subscribers: embed derived concept %q: %w", ev.topic, err)
		}
	}
	if _, err := p.weaknesses.Upsert(ctx, lw.UpsertInput{
		TenantID:     tenantID,
		LearnerGCID:  gcid,
		ConceptLabel: ev.topic,
		Embedding:    embedding,
		Tags:         ev.tags,
		Strength:     strength,
		Source:       ev.source,
		Descriptor:   lw.Descriptor{Summary: ev.summary},
		Now:          env.OccurredAt,
	}); err != nil {
		return fmt.Errorf("subscribers: upsert derived growth edge %q: %w", ev.topic, err)
	}
	return nil
}

// publishGrown emits one chora.consumption.weakness.grown.v1 per edge that just
// crossed active→grown (ADR-196 — the Curiosity-Reward trigger) through the
// SAME-TX outbox port (CHO-2129 AC4: the reward row commits or rolls back with
// the fold). Nil-safe: no outbox OR no transitions ⇒ no-op (the basic grow
// still persists via the recovery UPDATE). The trigger's W3C trace is
// propagated (one span in the same trace); occurred_at carries the trigger
// time; published_at is now; the idempotency_key (edge:grown_at) makes a replay
// a no-op at the outbox — one reward per real grow. A publish failure surfaces
// so the fold rolls back and the inbound event NACKs.
func (p *DerivedWeaknessProjector) publishGrown(ctx context.Context, env events.Envelope, tenantID, gcid string, grown []lw.GrownEdge, source string) error {
	if p.grownOutbox == nil || len(grown) == 0 {
		return nil
	}
	traceparent := tracing.EnsureTraceparent(env.Traceparent)
	now := time.Now().UTC()
	for _, g := range grown {
		grownAt := g.GrownAt.UTC()
		outEnv := events.Envelope{
			EventID:        domain.NewUUIDv7(),
			IdempotencyKey: "chora.consumption.weakness.grown:" + g.ID + ":" + grownAt.Format(time.RFC3339Nano),
			TenantID:       tenantID,
			GCID:           gcid,
			OccurredAt:     env.OccurredAt,
			PublishedAt:    now,
			Traceparent:    traceparent,
			Tracestate:     env.Tracestate,
			SourceProject:  events.SourceProject,
			SourceService:  events.SourceService,
			SchemaVersion:  1,
		}
		payload := map[string]any{
			"growth_edge_id":  g.ID,
			"tenant_id":       tenantID,
			"learner_gcid":    gcid,
			"concept_label":   g.ConceptLabel,
			"concept_key":     g.ConceptKey,
			"final_strength":  g.FinalStrength,
			"recovery_source": source,
			"tags":            g.Tags,
			"grown_at":        grownAt,
		}
		if err := p.grownOutbox.PublishGrownInTx(ctx, events.TopicWeaknessGrown, outEnv, payload); err != nil {
			return fmt.Errorf("subscribers: publish weakness.grown for edge %s: %w", g.ID, err)
		}
	}
	return nil
}

// fallbackIdentity prefers payload identity, falling back to the envelope.
func fallbackIdentity(tenantID, gcid string, env events.Envelope) (string, string) {
	if strings.TrimSpace(tenantID) == "" {
		tenantID = env.TenantID
	}
	if strings.TrimSpace(gcid) == "" {
		gcid = env.GCID
	}
	return tenantID, gcid
}

// cleanTopicTags trims + drops empty tags (preserves order, no dedup — the
// aggregate's cleanTags normalises further on Upsert).
func cleanTopicTags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
