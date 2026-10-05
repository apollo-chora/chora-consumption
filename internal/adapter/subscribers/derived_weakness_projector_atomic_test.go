// derived_weakness_projector_atomic_test.go — CHO-2129: the Growth-Edge fold is
// ATOMIC. The claim-first audit (CHO-2107 comment 14202) proved the projector's
// durable session-dedup (MarkSessionSeen, its own tx) fired BEFORE the fold
// (RecordAttempt → embed → Upsert/Recover → grown emit): a transient failure
// after the claim NACK'd, and the redelivery was ack-dropped as seen — the fold
// permanently lost, surviving pod restarts.
//
// The contract pinned here:
//   - the LLM embed runs BEFORE the fold transaction (an embed failure writes
//     NO dedup row — the redelivery retries the embed);
//   - session-dedup + attempt + weakness write + grown outbox land in ONE
//     transaction (a failure rolls ALL of it back, incl. the dedup row, and the
//     redelivery re-processes cleanly);
//   - a successful fold gates the redelivery (no double attempt count);
//   - the envelope-id tracker is seen()/mark() (never burned by a failure);
//   - grown rides the fold tx via the GrownOutbox port (no post-commit
//     best-effort emit).
package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// --- tx harness -------------------------------------------------------------
//
// dwFoldHarness models the pg fold transaction for unit fakes: fn's writes are
// STAGED on the tx-aware fakes and only become visible on commit; an error from
// fn rolls the staging back. inTx lets fakes serve tx-scoped reads (the
// authoritative in-tx accuracy read that includes the just-recorded attempt).

type dwFoldHarness struct {
	log        []string
	inTx       bool
	committers []interface{ commit() }
	rollbacks  []interface{ rollback() }
}

func (h *dwFoldHarness) attach(f interface {
	commit()
	rollback()
}) {
	h.committers = append(h.committers, f)
	h.rollbacks = append(h.rollbacks, f)
}

func (h *dwFoldHarness) run(ctx context.Context, fn func(ctx context.Context) error) error {
	h.log = append(h.log, "tx-begin")
	h.inTx = true
	err := fn(ctx)
	h.inTx = false
	if err != nil {
		h.log = append(h.log, "tx-rollback")
		for _, r := range h.rollbacks {
			r.rollback()
		}
		return err
	}
	h.log = append(h.log, "tx-commit")
	for _, c := range h.committers {
		c.commit()
	}
	return nil
}

// dwTxAccuracy is a tx-aware AccuracyRecorder: writes stage until commit; the
// accuracy read returns `base` outside the tx (the projector's pre-tx
// prediction read) and `inTx` inside it (the post-attempt authority).
type dwTxAccuracy struct {
	h    *dwFoldHarness
	base map[string]float64
	inTx map[string]float64

	attempts       []dwAttempt
	stagedAttempts []dwAttempt
	seen           map[string]bool
	stagedSeen     map[string]bool

	markErr   error
	recordErr error
	markCalls int
}

func (a *dwTxAccuracy) commit() {
	a.attempts = append(a.attempts, a.stagedAttempts...)
	for k := range a.stagedSeen {
		if a.seen == nil {
			a.seen = map[string]bool{}
		}
		a.seen[k] = true
	}
	a.stagedAttempts, a.stagedSeen = nil, nil
}
func (a *dwTxAccuracy) rollback() { a.stagedAttempts, a.stagedSeen = nil, nil }

func (a *dwTxAccuracy) RecordAttempt(_ context.Context, _, _ string, topicTag string, isCorrect bool) error {
	if a.recordErr != nil {
		return a.recordErr
	}
	a.h.log = append(a.h.log, "attempt:"+topicTag)
	a.stagedAttempts = append(a.stagedAttempts, dwAttempt{Topic: topicTag, Correct: isCorrect})
	return nil
}

func (a *dwTxAccuracy) GetByLearner(context.Context, string, string) (map[string]float64, error) {
	if a.h.inTx {
		a.h.log = append(a.h.log, "read-accuracy-in-tx")
		if a.inTx == nil {
			return map[string]float64{}, nil
		}
		return a.inTx, nil
	}
	a.h.log = append(a.h.log, "read-accuracy-pre-tx")
	if a.base == nil {
		return map[string]float64{}, nil
	}
	return a.base, nil
}

func (a *dwTxAccuracy) MarkSessionSeen(_ context.Context, tenantID, gcid, sessionID string) (bool, error) {
	a.markCalls++
	if a.markErr != nil {
		return false, a.markErr
	}
	a.h.log = append(a.h.log, "dedup")
	k := tenantID + "|" + gcid + "|" + sessionID
	if a.seen[k] || a.stagedSeen[k] {
		return false, nil
	}
	if a.stagedSeen == nil {
		a.stagedSeen = map[string]bool{}
	}
	a.stagedSeen[k] = true
	return true, nil
}

// dwTxRepo is a tx-aware lw.Repository (staged upserts/recovers).
type dwTxRepo struct {
	h *dwFoldHarness

	upserts       []lw.UpsertInput
	stagedUpserts []lw.UpsertInput

	recovers       []dwRecoverCall
	stagedRecovers []dwRecoverCall

	upsertErr       error
	recoverErr      error
	drillRecoverErr error
	grownOnRecover  []lw.GrownEdge
}

func (r *dwTxRepo) commit() {
	r.upserts = append(r.upserts, r.stagedUpserts...)
	r.recovers = append(r.recovers, r.stagedRecovers...)
	r.stagedUpserts, r.stagedRecovers = nil, nil
}
func (r *dwTxRepo) rollback() { r.stagedUpserts, r.stagedRecovers = nil, nil }

func (r *dwTxRepo) Upsert(_ context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	if r.upsertErr != nil {
		return lw.UpsertResult{}, r.upsertErr
	}
	r.h.log = append(r.h.log, "upsert:"+in.ConceptLabel)
	r.stagedUpserts = append(r.stagedUpserts, in)
	return lw.UpsertResult{ID: "ge-1"}, nil
}
func (r *dwTxRepo) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	return lw.ListResult{}, nil
}
func (r *dwTxRepo) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *dwTxRepo) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *dwTxRepo) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (r *dwTxRepo) RecoverByConceptKey(_ context.Context, gcid, conceptKey string, strength float64, _ time.Time) ([]lw.GrownEdge, error) {
	if r.recoverErr != nil {
		return nil, r.recoverErr
	}
	r.h.log = append(r.h.log, "recover:"+conceptKey)
	r.stagedRecovers = append(r.stagedRecovers, dwRecoverCall{GCID: gcid, ConceptKey: conceptKey, Strength: strength})
	return r.grownOnRecover, nil
}
func (r *dwTxRepo) RecoverByDrillAtomID(_ context.Context, _, _ string, _ float64, _ time.Time) ([]lw.GrownEdge, error) {
	if r.drillRecoverErr != nil {
		return nil, r.drillRecoverErr
	}
	r.h.log = append(r.h.log, "drill-recover")
	return nil, nil
}
func (r *dwTxRepo) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

// dwTxGrownOutbox is a tx-aware GrownOutbox (staged rows).
type dwTxGrownOutbox struct {
	h      *dwFoldHarness
	rows   []events.Event
	staged []events.Event
	err    error
}

func (o *dwTxGrownOutbox) commit()   { o.rows = append(o.rows, o.staged...); o.staged = nil }
func (o *dwTxGrownOutbox) rollback() { o.staged = nil }

func (o *dwTxGrownOutbox) PublishGrownInTx(_ context.Context, topic string, env events.Envelope, payload map[string]any) error {
	if o.err != nil {
		return o.err
	}
	o.h.log = append(o.h.log, "grown-emit")
	o.staged = append(o.staged, events.Event{Topic: topic, Envelope: env, Payload: payload})
	return nil
}

// dwTxEmbedder logs whether the embed ran inside or outside the tx.
type dwTxEmbedder struct {
	h     *dwFoldHarness
	calls int
	err   error
}

func (e *dwTxEmbedder) Embed(context.Context, string, string) ([]float32, error) {
	e.calls++
	if e.h.inTx {
		e.h.log = append(e.h.log, "embed-in-tx")
	} else {
		e.h.log = append(e.h.log, "embed-pre-tx")
	}
	if e.err != nil {
		return nil, e.err
	}
	return []float32{0.1, 0.2}, nil
}

// --- fixture ----------------------------------------------------------------

func dwSessionAtoms() *dwFakeAtoms {
	return &dwFakeAtoms{atoms: map[string]*atom_index.AtomIndex{
		"atom-1": {AtomID: "atom-1", TenantID: dwTenant, AtomType: "mcq", CorrectOptionID: "b", TopicTags: []string{"fractions"}},
	}}
}

func dwAtomSessionCompletedPayload() AtomSessionCompletedPayload {
	return AtomSessionCompletedPayload{
		SessionID:   "sess-1",
		AtomID:      "atom-1",
		IsCorrect:   false,
		TenantID:    dwTenant,
		LearnerGCID: dwGCID,
	}
}

type dwAtomicRig struct {
	h    *dwFoldHarness
	acc  *dwTxAccuracy
	repo *dwTxRepo
	out  *dwTxGrownOutbox
	emb  *dwTxEmbedder
	p    *DerivedWeaknessProjector
}

func newAtomicRig() *dwAtomicRig {
	h := &dwFoldHarness{}
	acc := &dwTxAccuracy{h: h, base: map[string]float64{}, inTx: map[string]float64{}}
	repo := &dwTxRepo{h: h}
	out := &dwTxGrownOutbox{h: h}
	emb := &dwTxEmbedder{h: h}
	h.attach(acc)
	h.attach(repo)
	h.attach(out)
	p := NewDerivedWeaknessProjector(repo, emb, acc).
		WithAtomIndex(dwSessionAtoms()).
		WithGrownOutbox(out).
		WithFoldTx(h.run)
	return &dwAtomicRig{h: h, acc: acc, repo: repo, out: out, emb: emb, p: p}
}

func logJoined(h *dwFoldHarness) string { return strings.Join(h.log, " → ") }

// --- the atomic-fold contract -----------------------------------------------

// AC "Atomic fold": a transient weakness-write failure rolls back EVERYTHING —
// including the session-dedup row — and the redelivery re-processes cleanly
// with no double attempt count.
func TestAtomSession_TransientUpsertFailure_RollsBackDedupAndRetriesCleanly(t *testing.T) {
	rig := newAtomicRig()
	rig.repo.upsertErr = errors.New("pg down")

	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err == nil {
		t.Fatalf("want error on transient upsert failure (NACK), got ack; log=%s", logJoined(rig.h))
	}
	if len(rig.acc.attempts) != 0 || len(rig.acc.seen) != 0 || len(rig.repo.upserts) != 0 {
		t.Fatalf("rollback must discard attempt+dedup+upsert: attempts=%d seen=%d upserts=%d",
			len(rig.acc.attempts), len(rig.acc.seen), len(rig.repo.upserts))
	}

	// Redelivery (same event id + same session) — heals.
	rig.repo.upsertErr = nil
	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("redelivery must process cleanly, got %v", err)
	}
	if len(rig.acc.attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (no loss, no double-count)", len(rig.acc.attempts))
	}
	if len(rig.repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want exactly 1", len(rig.repo.upserts))
	}
}

// AC "No double-count": a successfully processed session gates its redelivery
// via the same-tx dedup row.
func TestAtomSession_SuccessfulFold_GatesRedelivery(t *testing.T) {
	rig := newAtomicRig()

	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Redelivery with a FRESH event id (a republish) — the durable session dedup
	// must gate the increment.
	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-2"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(rig.acc.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 (dedup gates the increment)", len(rig.acc.attempts))
	}
}

// AC "Embed outside the claim": a transient embed failure NACKs BEFORE any
// dedup row exists — the redelivery retries the embed.
func TestAtomSession_EmbedFailure_WritesNoDedupRow(t *testing.T) {
	rig := newAtomicRig()
	rig.emb.err = errors.New("vertex 503")

	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err == nil {
		t.Fatal("want error on embed failure (NACK), got ack")
	}
	if rig.acc.markCalls != 0 {
		t.Fatalf("MarkSessionSeen called %d times before/despite embed failure — the claim must come after the embed", rig.acc.markCalls)
	}
	for _, entry := range rig.h.log {
		if entry == "tx-begin" {
			t.Fatalf("fold tx began despite embed failure; log=%s", logJoined(rig.h))
		}
	}

	rig.emb.err = nil
	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("redelivery must retry the embed and fold, got %v", err)
	}
	if len(rig.repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(rig.repo.upserts))
	}
}

// The structural order: embed strictly before the tx; dedup + attempt +
// weakness write strictly inside it.
func TestAtomSession_FoldOrder_EmbedThenTxThenDedupAttemptWrite(t *testing.T) {
	rig := newAtomicRig()

	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := logJoined(rig.h)
	idx := func(s string) int { return strings.Index(got, s) }
	for _, step := range []string{"embed-pre-tx", "tx-begin", "dedup", "attempt:fractions", "upsert:fractions", "tx-commit"} {
		if idx(step) < 0 {
			t.Fatalf("log missing %q; log=%s", step, got)
		}
	}
	if !(idx("embed-pre-tx") < idx("tx-begin") && idx("tx-begin") < idx("dedup") &&
		idx("dedup") < idx("attempt:fractions") && idx("attempt:fractions") < idx("upsert:fractions") &&
		idx("upsert:fractions") < idx("tx-commit")) {
		t.Fatalf("fold order wrong; log=%s", got)
	}
}

// AC "Grown emit durable": the recover branch emits weakness.grown through the
// fold tx — a later in-tx failure discards the staged emit and the redelivery
// re-emits exactly once.
func TestAtomSession_GrownRidesTheFoldTx(t *testing.T) {
	rig := newAtomicRig()
	// Post-attempt authority puts the topic AT the threshold → recover branch.
	rig.acc.base = map[string]float64{"fractions": 0.9}
	rig.acc.inTx = map[string]float64{"fractions": 0.9}
	rig.repo.grownOnRecover = []lw.GrownEdge{{ID: "edge-1", ConceptKey: "fractions", GrownAt: time.Now()}}
	rig.repo.drillRecoverErr = errors.New("pg down") // fails AFTER the grown emit

	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err == nil {
		t.Fatal("want error (NACK) when the drill recover fails after the grown emit")
	}
	if len(rig.out.rows) != 0 {
		t.Fatalf("grown rows visible after rollback: %d, want 0 (emit must ride the tx)", len(rig.out.rows))
	}

	rig.repo.drillRecoverErr = nil
	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(rig.out.rows) != 1 {
		t.Fatalf("grown rows = %d, want exactly 1", len(rig.out.rows))
	}
	got := logJoined(rig.h)
	if !(strings.Contains(got, "grown-emit") && strings.LastIndex(got, "grown-emit") < strings.LastIndex(got, "tx-commit")) {
		t.Fatalf("grown emit must land inside the fold tx; log=%s", got)
	}
}

// The recover branch never pre-embeds (prediction says recover); when the
// in-tx authoritative accuracy disagrees (rare same-learner race), the embed
// falls back INSIDE the tx rather than livelocking or upserting nil.
func TestAtomSession_PredictedRecover_SkipsPreEmbed_InTxFallbackOnFlip(t *testing.T) {
	rig := newAtomicRig()
	rig.acc.base = map[string]float64{"fractions": 0.95} // prediction: recover — skip pre-embed
	rig.acc.inTx = map[string]float64{"fractions": 0.10} // authority: below — needs the embedding

	// A CORRECT answer is the only shape whose accuracy provably rises — an
	// incorrect one can always cross DOWN through the threshold from a
	// ratio-only read, so it must pre-embed conservatively.
	payload := dwAtomSessionCompletedPayload()
	payload.IsCorrect = true
	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), payload); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := logJoined(rig.h)
	if strings.Contains(got, "embed-pre-tx") {
		t.Fatalf("predicted-recover must not pre-embed; log=%s", got)
	}
	if !strings.Contains(got, "embed-in-tx") {
		t.Fatalf("branch flip must fall back to an in-tx embed; log=%s", got)
	}
	if len(rig.repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(rig.repo.upserts))
	}
}

// The envelope-id layer is seen()/mark(): a transient failure must not burn the
// event id (the pre-fix markSeen claim did — same-pod redeliveries were
// silently acked).
func TestAtomSession_EnvelopeIDNotBurnedByFailure(t *testing.T) {
	rig := newAtomicRig()
	rig.acc.recordErr = errors.New("pg down")

	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err == nil {
		t.Fatal("want error on transient record failure")
	}
	rig.acc.recordErr = nil
	if err := rig.p.HandleAtomSessionCompleted(context.Background(), dwEnv("evt-1"), dwAtomSessionCompletedPayload()); err != nil {
		t.Fatalf("redelivery with the SAME event id must re-process, got %v", err)
	}
	if len(rig.acc.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(rig.acc.attempts))
	}
}

// The submission path folds atomically too: its recover/upsert + grown emit
// ride the fold tx, and neither the envelope id nor the submission key is
// burned by a transient failure.
func TestSubmissionGraded_FoldAtomic_TrackersNotBurned(t *testing.T) {
	rig := newAtomicRig()
	rig.repo.upsertErr = errors.New("pg down")

	evidence := SubmissionGradedEvidence{
		SubmissionID:    "sub-1",
		AssessmentID:    "assess-1",
		AssessmentTitle: "Fractions Checkpoint",
		LearnerGCID:     dwGCID,
		TenantID:        dwTenant,
		PointsEarned:    1,
		PointsPossible:  10,
	}
	if err := rig.p.HandleSubmissionGraded(context.Background(), dwEnv("evt-s1"), evidence); err == nil {
		t.Fatal("want error on transient upsert failure")
	}
	if len(rig.repo.upserts) != 0 {
		t.Fatalf("rollback must discard the upsert, got %d", len(rig.repo.upserts))
	}

	rig.repo.upsertErr = nil
	if err := rig.p.HandleSubmissionGraded(context.Background(), dwEnv("evt-s1"), evidence); err != nil {
		t.Fatalf("redelivery must process (neither env id nor submission key burned), got %v", err)
	}
	if len(rig.repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(rig.repo.upserts))
	}
}

// The live-quiz path: award-key + envelope-id survive a transient failure, and
// its attempts ride the fold tx (rolled back together).
func TestLiveQuiz_FoldAtomic_AwardKeyNotBurned(t *testing.T) {
	rig := newAtomicRig()
	rig.repo.upsertErr = errors.New("pg down")

	if err := rig.p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("evt-q1"), dwScorePayload(false)); err == nil {
		t.Fatal("want error on transient upsert failure")
	}
	if len(rig.acc.attempts) != 0 {
		t.Fatalf("rollback must discard the recorded attempts, got %d", len(rig.acc.attempts))
	}

	rig.repo.upsertErr = nil
	if err := rig.p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("evt-q1"), dwScorePayload(false)); err != nil {
		t.Fatalf("redelivery must process (award key not burned), got %v", err)
	}
	if got := len(rig.acc.attempts); got != 2 { // two topic tags
		t.Fatalf("attempts = %d, want 2 (one per tag, exactly once)", got)
	}
}
