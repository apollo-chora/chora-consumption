// claim_first_split_test.go — RED-phase tests for CHO-2130: the remaining
// claim-first subscribers (audit CHO-2107 comment 14202, findings #2-#5) must
// follow the seen→process→mark discipline. markSeen BEFORE the side effect
// burned the dedupe key, so a transient post-claim failure NACKed the event and
// its Pub/Sub redelivery was ack-dropped as a duplicate — swallow-on-failure.
// Once-only events (atom.created / atom.published) were lost permanently.
//
// Each test arms a transient repo failure, asserts the failure surfaces
// (NACK), heals the repo, redelivers the SAME envelope, and asserts the write
// lands. Also covered (same audit): TopicAccuracy + DerivedWeaknessProjector
// ack-dropped a countable attempt on a TRANSIENT atoms.Get error — only
// atom_index.ErrNotFound (out-of-order hydration) is a legit ack.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

var errTransient = errors.New("test: transient infra failure")

// flakyAtomIndexRepo wraps a real atom_index.Repo; each armed error fires on
// every call until disarmed (set nil), simulating a transient infra failure
// that heals before the Pub/Sub redelivery.
type flakyAtomIndexRepo struct {
	atom_index.Repo
	saveErr    error
	markPubErr error
	getErr     error
}

func (r *flakyAtomIndexRepo) Save(ctx context.Context, a *atom_index.AtomIndex) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	return r.Repo.Save(ctx, a)
}

func (r *flakyAtomIndexRepo) MarkPublished(ctx context.Context, atomID string, st atom_index.Status, atomType, correctOptionID string, answerCount int, cognitiveLevel string) error {
	if r.markPubErr != nil {
		return r.markPubErr
	}
	return r.Repo.MarkPublished(ctx, atomID, st, atomType, correctOptionID, answerCount, cognitiveLevel)
}

func (r *flakyAtomIndexRepo) Get(ctx context.Context, atomID string) (*atom_index.AtomIndex, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.Repo.Get(ctx, atomID)
}

// ---- AtomCreatedSubscriber (audit #2, med-high: a lost atom_index row makes
// the atom invisible to grading + daily dose; atom.created is once-only) ----

func TestAtomCreatedSubscriber_RedeliveryAfterTransientSaveFailureLands(t *testing.T) {
	inner := repoinmem.NewAtomIndexRepo()
	flaky := &flakyAtomIndexRepo{Repo: inner, saveErr: errTransient}
	sub := NewAtomCreatedSubscriber(flaky, nil)
	env := newTestEnvelope("evt-cfs-created", "t1", "g1")
	payload := AtomCreatedPayload{
		AtomID: "atom-cfs-1", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	}

	if err := sub.Handle(context.Background(), env, payload); err == nil {
		t.Fatal("transient Save failure must surface (Pub/Sub NACK)")
	}
	flaky.saveErr = nil // infra healed before the redelivery

	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if _, err := inner.Get(context.Background(), "atom-cfs-1"); err != nil {
		t.Fatalf("redelivery must land the atom_index row (claim-first swallowed it): %v", err)
	}
}

// ---- AtomPublishedSubscriber (audit #3, med: stuck DRAFT never serves) ----

func TestAtomPublishedSubscriber_RedeliveryAfterTransientFlipFailureLands(t *testing.T) {
	inner := repoinmem.NewAtomIndexRepo()
	seedDraftViaCreated(t, inner, AtomCreatedPayload{
		AtomID: "atom-cfs-2", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	})
	flaky := &flakyAtomIndexRepo{Repo: inner, markPubErr: errTransient}
	sub := NewAtomPublishedSubscriber(flaky)
	env := newTestEnvelope("evt-cfs-pub", "t1", "author")
	p := AtomPublishedIndexPayload{
		AtomID: "atom-cfs-2", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt_1", AnswerCount: 2,
	}

	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("transient MarkPublished failure must surface (Pub/Sub NACK)")
	}
	flaky.markPubErr = nil

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	got, err := inner.Get(context.Background(), "atom-cfs-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Playable() {
		t.Error("redelivery must flip the atom to published (claim-first left it stuck DRAFT)")
	}
}

// ---- CourseContentComposedSubscriber (audit #4, med: curriculum snapshot;
// self-repairs only on the NEXT compose — the current one must not be lost) ----

func TestCourseContentComposed_RedeliveryAfterTransientReplaceFailureLands(t *testing.T) {
	repo := newFakeProjRepo()
	repo.failErr = errTransient
	sub := NewCourseContentComposedSubscriber(repo, nil)
	env := ccEnvelope("evt-cfs-cc")
	p := CourseContentComposedPayload{
		CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d",
		Items: []CourseContentItemPayload{
			{ItemID: "i1", Kind: "atom", Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "a", Position: 0},
		},
	}

	if err := sub.Handle(env, p); err == nil {
		t.Fatal("transient ReplaceByCourse failure must surface (Pub/Sub NACK)")
	}
	repo.failErr = nil

	if err := sub.Handle(env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if repo.replaces != 1 {
		t.Errorf("replaces = %d; want 1 (redelivery must land the curriculum)", repo.replaces)
	}
}

// ---- CourseMetadataSubscriber (audit #5, low: LWW title) ----

func TestCourseMetadata_RedeliveryAfterTransientUpsertFailureLands(t *testing.T) {
	dir := &fakeCourseDir{failErr: errTransient}
	sub := NewCourseMetadataSubscriber(dir)
	env := cmEnvelope("evt-cfs-cm", time.Now().UTC())
	p := CourseMetadataPayload{CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d", Title: "Algebra I"}

	if err := sub.Handle(env, p); err == nil {
		t.Fatal("transient Upsert failure must surface (Pub/Sub NACK)")
	}
	dir.failErr = nil

	if err := sub.Handle(env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(dir.upserts) != 1 {
		t.Errorf("upserts = %d; want 1 (redelivery must land the title)", len(dir.upserts))
	}
}

// ---- TopicAccuracySubscriber honest NACK (audit ticket-worthy: a transient
// atoms.Get error ack-dropped a countable attempt; only ErrNotFound is a legit
// out-of-order-hydration ack) ----

func TestTopicAccuracySubscriber_TransientAtomLookupNACKs(t *testing.T) {
	inner := repoinmem.NewAtomIndexRepo()
	a, err := atomIndexHelperWithTopics("atom-cfs-3", "t1", "c1", []string{"agile"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = inner.Save(context.Background(), a)
	flaky := &flakyAtomIndexRepo{Repo: inner, getErr: errTransient}
	sub := NewTopicAccuracySubscriber(inmem.NewTopicAccuracyRepo(), flaky)

	env := newTestEnvelope("evt-cfs-acc-nack", "t1", "phyllis")
	p := AtomSessionCompletedPayload{
		SessionID: "sess-cfs-1", AtomID: "atom-cfs-3", LearnerGCID: "phyllis",
		TenantID: "t1", IsCorrect: true, OccurredAt: time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("transient atoms.Get failure must surface (Pub/Sub NACK) — ack-dropping consumes a countable attempt")
	}
}

func TestTopicAccuracySubscriber_RedeliveryAfterTransientLookupCounts(t *testing.T) {
	inner := repoinmem.NewAtomIndexRepo()
	a, err := atomIndexHelperWithTopics("atom-cfs-4", "t1", "c1", []string{"agile"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = inner.Save(context.Background(), a)
	flaky := &flakyAtomIndexRepo{Repo: inner, getErr: errTransient}
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, flaky)

	env := newTestEnvelope("evt-cfs-acc-redeliver", "t1", "phyllis")
	p := AtomSessionCompletedPayload{
		SessionID: "sess-cfs-2", AtomID: "atom-cfs-4", LearnerGCID: "phyllis",
		TenantID: "t1", IsCorrect: true, OccurredAt: time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("transient atoms.Get failure must surface (Pub/Sub NACK)")
	}
	flaky.getErr = nil // infra healed before the redelivery

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	got := accByLearner(t, accRepo, "t1", "phyllis")
	if got["agile"] != 1.0 {
		t.Errorf("accuracy[agile] = %v; want 1.0 (redelivery must count the attempt — env+session keys were burned pre-lookup)", got["agile"])
	}
}

// ---- DerivedWeaknessProjector eligibility reads (same transient-vs-notfound
// class — its atom-session tail deliberately mirrored TopicAccuracySubscriber,
// and the live-quiz tag fallback silently dropped evidence on a transient Get) ----

func TestDerivedWeakness_SessionCompleted_TransientAtomLookupNACKs(t *testing.T) {
	p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{}).
		WithAtomIndex(&dwFakeAtoms{getErr: errTransient})
	err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-cfs-dw1"), AtomSessionCompletedPayload{
		SessionID: "s-cfs-1", AtomID: "atom-x", LearnerGCID: dwGCID, TenantID: dwTenant, IsCorrect: false,
	})
	if err == nil {
		t.Fatal("transient atoms.Get failure must surface (Pub/Sub NACK), not ack-drop the fold")
	}
}

func TestDerivedWeakness_SessionCompleted_MissingAtomStillAcks(t *testing.T) {
	p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{}).
		WithAtomIndex(&dwFakeAtoms{}) // empty map → atom_index.ErrNotFound
	err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-cfs-dw2"), AtomSessionCompletedPayload{
		SessionID: "s-cfs-2", AtomID: "atom-unhydrated", LearnerGCID: dwGCID, TenantID: dwTenant, IsCorrect: false,
	})
	if err != nil {
		t.Fatalf("out-of-order hydration (ErrNotFound) must still ack: %v", err)
	}
}

func TestDerivedWeakness_LiveQuiz_TransientTagFallbackLookupNACKs(t *testing.T) {
	p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{}).
		WithAtomIndex(&dwFakeAtoms{getErr: errTransient})
	pay := dwScorePayload(false)
	pay.TopicTags = nil // force the atom-index tag fallback
	err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-cfs-dw3"), pay)
	if err == nil {
		t.Fatal("transient fallback atoms.Get failure must surface (Pub/Sub NACK), not silently drop the evidence")
	}
}

func TestDerivedWeakness_LiveQuiz_MissingAtomStillAcksUnmappable(t *testing.T) {
	p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{}).
		WithAtomIndex(&dwFakeAtoms{}) // empty map → atom_index.ErrNotFound
	pay := dwScorePayload(false)
	pay.TopicTags = nil
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-cfs-dw4"), pay); err != nil {
		t.Fatalf("not-found fallback stays an unmappable ack-drop: %v", err)
	}
}
