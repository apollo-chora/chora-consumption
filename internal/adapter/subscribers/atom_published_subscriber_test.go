// atom_published_subscriber_test.go — RED→GREEN tests for the atom_index
// PLAYABILITY subscriber (CHO-1968). This is the 3rd consumer of
// chora.creation.atom.published.v1 (the others — CompanionGrowthSubscriber EXP +
// AtomPublishedKGSubscriber fog-invalidation — are unrelated and untouched).
//
// The subscriber flips the local atom_index projection from DRAFT (seeded by
// atom.created) to PUBLISHED and stamps the answer key from the PUBLISHED event
// (the created row's key is empty/unreliable). Only a published + answerable
// atom is serveable in the daily dose.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// seedDraftViaCreated projects a DRAFT atom_index row exactly as the real
// atom.created path does (via AtomCreatedSubscriber → atom_index.New default).
func seedDraftViaCreated(t *testing.T, repo atom_index.Repo, p AtomCreatedPayload) {
	t.Helper()
	sub := NewAtomCreatedSubscriber(repo, nil)
	env := newTestEnvelope("evt-created-"+p.AtomID, p.TenantID, "author")
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("seedDraftViaCreated: %v", err)
	}
}

func TestAtomPublishedSubscriber_FlipsToPlayable(t *testing.T) {
	repo := inmem.NewAtomIndexRepo()
	seedDraftViaCreated(t, repo, AtomCreatedPayload{
		AtomID: "atom-1", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	})
	// Precondition: the created row is a DRAFT — NOT playable.
	if pre, _ := repo.Get(context.Background(), "atom-1"); pre.Playable() {
		t.Fatalf("precondition failed: created row should be draft (not playable)")
	}

	sub := NewAtomPublishedSubscriber(repo)
	env := newTestEnvelope("evt-pub-1", "t1", "author")
	if err := sub.Handle(context.Background(), env, AtomPublishedIndexPayload{
		AtomID: "atom-1", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt_1", AnswerCount: 2,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, err := repo.Get(context.Background(), "atom-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Playable() {
		t.Errorf("Playable() = false after atom.published flip; want true (status=%q)", got.Status)
	}
}

func TestAtomPublishedSubscriber_SetsAnswerKeyFromEvent(t *testing.T) {
	repo := inmem.NewAtomIndexRepo()
	// The created row carries NO answer key (the whole point — keys are unreliable
	// at create time).
	seedDraftViaCreated(t, repo, AtomCreatedPayload{
		AtomID: "atom-2", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	})
	if pre, _ := repo.Get(context.Background(), "atom-2"); pre.CorrectOptionID != "" || pre.AnswerCount != 0 {
		t.Fatalf("precondition: created row should have NO key; got %q/%d", pre.CorrectOptionID, pre.AnswerCount)
	}

	sub := NewAtomPublishedSubscriber(repo)
	env := newTestEnvelope("evt-pub-2", "t1", "author")
	if err := sub.Handle(context.Background(), env, AtomPublishedIndexPayload{
		AtomID: "atom-2", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt_1", AnswerCount: 2,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, _ := repo.Get(context.Background(), "atom-2")
	if got.CorrectOptionID != "opt_1" {
		t.Errorf("CorrectOptionID = %q; want opt_1 (sourced from the PUBLISHED event)", got.CorrectOptionID)
	}
	if got.AnswerCount != 2 {
		t.Errorf("AnswerCount = %d; want 2 (sourced from the PUBLISHED event)", got.AnswerCount)
	}
	if !got.IsMCQ() {
		t.Errorf("IsMCQ() = false; the published flip must make the MCQ gradable")
	}
}

func TestAtomPublishedSubscriber_PreservesTitleTags(t *testing.T) {
	repo := inmem.NewAtomIndexRepo()
	seedDraftViaCreated(t, repo, AtomCreatedPayload{
		AtomID: "atom-3", TenantID: "t1", Title: "Scrum Roles", AtomType: "mcq",
		TopicTags: []string{"scrum", "agile"}, PublishedAt: time.Now().UTC(),
	})

	sub := NewAtomPublishedSubscriber(repo)
	env := newTestEnvelope("evt-pub-3", "t1", "author")
	if err := sub.Handle(context.Background(), env, AtomPublishedIndexPayload{
		AtomID: "atom-3", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt_1", AnswerCount: 4,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, _ := repo.Get(context.Background(), "atom-3")
	if got.Title != "Scrum Roles" {
		t.Errorf("Title = %q; want Scrum Roles (MarkPublished is targeted, must preserve title)", got.Title)
	}
	if len(got.TopicTags) != 2 || got.TopicTags[0] != "scrum" {
		t.Errorf("TopicTags = %v; want [scrum agile] (preserved across the flip)", got.TopicTags)
	}
}

func TestAtomPublishedSubscriber_LateCreatedDoesNotDowngrade(t *testing.T) {
	repo := inmem.NewAtomIndexRepo()
	seedDraftViaCreated(t, repo, AtomCreatedPayload{
		AtomID: "atom-4", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	})
	pubSub := NewAtomPublishedSubscriber(repo)
	if err := pubSub.Handle(context.Background(), newTestEnvelope("evt-pub-4", "t1", "author"),
		AtomPublishedIndexPayload{AtomID: "atom-4", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt_1", AnswerCount: 2}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got, _ := repo.Get(context.Background(), "atom-4"); !got.Playable() {
		t.Fatalf("precondition: atom should be published+playable before the late created replay")
	}

	// A late atom.created REPLAY (fresh event_id, i.e. an outbox redrive) arrives
	// AFTER the publish. The non-downgrading upsert must keep the row published.
	createSub := NewAtomCreatedSubscriber(repo, nil)
	if err := createSub.Handle(context.Background(), newTestEnvelope("evt-created-late-4", "t1", "author"),
		AtomCreatedPayload{AtomID: "atom-4", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("late created: %v", err)
	}

	got, _ := repo.Get(context.Background(), "atom-4")
	if got.Status != atom_index.StatusPublished {
		t.Errorf("Status = %q after late atom.created; want published (non-downgrading)", got.Status)
	}
	if !got.Playable() {
		t.Errorf("Playable() = false after late created; a draft replay must not revert a published row")
	}
}

func TestAtomPublishedSubscriber_Idempotent(t *testing.T) {
	inner := inmem.NewAtomIndexRepo()
	seedDraftViaCreated(t, inner, AtomCreatedPayload{
		AtomID: "atom-5", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	})
	spy := &markPublishedCounter{Repo: inner}
	sub := NewAtomPublishedSubscriber(spy)

	env := newTestEnvelope("evt-pub-idem", "t1", "author")
	p := AtomPublishedIndexPayload{AtomID: "atom-5", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt_1", AnswerCount: 2}
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// Second call with the SAME event_id must be a no-op (idempotency tracker).
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if spy.calls != 1 {
		t.Errorf("MarkPublished calls = %d; want 1 (duplicate event_id deduped before the repo)", spy.calls)
	}
}

func TestAtomPublishedSubscriber_InvalidEnvelope_Rejected(t *testing.T) {
	sub := NewAtomPublishedSubscriber(inmem.NewAtomIndexRepo())
	badEnv := events.Envelope{} // missing all mandatory fields
	if err := sub.Handle(context.Background(), badEnv, AtomPublishedIndexPayload{AtomID: "x", TenantID: "t1"}); err == nil {
		t.Error("expected error for invalid envelope, got nil")
	}
}

// markPublishedCounter wraps an atom_index.Repo and counts MarkPublished calls
// so the idempotency test can prove the tracker gates BEFORE the repo mutation.
type markPublishedCounter struct {
	atom_index.Repo
	calls int
}

func (c *markPublishedCounter) MarkPublished(ctx context.Context, atomID string, st atom_index.Status, atomType, correctOptionID string, answerCount int, cognitiveLevel string) error {
	c.calls++
	return c.Repo.MarkPublished(ctx, atomID, st, atomType, correctOptionID, answerCount, cognitiveLevel)
}
