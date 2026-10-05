// subscribers_cover_test.go — white-box coverage tests for the shared
// subscriber plumbing in subscribers.go:
//
//   - validateInboundEnvelope (per-field rejection table)
//   - newIdempotencyTrackerWithStore (nil store + non-positive ttl fallbacks)
//   - markSeen store-error path (conservative no-op = already-seen)
//   - AtomCreatedSubscriber.Handle error branches (bad payload, repo Save
//     failure, ListByCourse failure, retroactive-append Save failure,
//     PublishedAt fallback)
//   - EnrollmentCreatedSubscriber.Handle error branches (bad envelope,
//     atom-list failure, bootstrap-validation failure, path Save failure)
//
// These complement the behavioural tests in subscribers_test.go which only
// exercise the happy paths via the inmem repos (which never error).
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ----------------- error-injecting fakes -----------------

var errInjected = errors.New("test: injected repo failure")

// fakeAtomRepo is an atom_index.Repo whose Save / ListByCourse can be made to
// error on demand. Get always succeeds returning nil (the subscriber paths
// under test do not consult Get).
type fakeAtomRepo struct {
	saveErr error
	listErr error
	list    []*atom_index.AtomIndex
}

func (r *fakeAtomRepo) Save(_ context.Context, _ *atom_index.AtomIndex) error { return r.saveErr }
func (r *fakeAtomRepo) Get(_ context.Context, _ string) (*atom_index.AtomIndex, error) {
	return nil, errInjected // unused by the paths under test
}
func (r *fakeAtomRepo) ListByCourse(_ context.Context, _, _ string) ([]*atom_index.AtomIndex, error) {
	return r.list, r.listErr
}

// fakeContentRepo is the COURSE-CONTENT projection double the enrolment bootstrap
// now reads (CHO-2169). It replaced fakeAtomRepo there: the bootstrap no longer
// asks atom_index "which atoms carry this course's tag" — a question chora-creation
// cannot answer for an R+-composed course — it asks the curriculum "which atoms are
// in this course", which is chora-delivery's to answer and is already projected here.
type fakeContentRepo struct {
	items   []*course_content.Item
	listErr error
}

func (r *fakeContentRepo) ReplaceByCourse(_ context.Context, _, _ string, _ []*course_content.Item) error {
	return nil
}
func (r *fakeContentRepo) ListByCourse(_ context.Context, _, _ string) ([]*course_content.Item, error) {
	return r.items, r.listErr
}
func (r *fakeAtomRepo) SearchForLearner(_ context.Context, _, _ string, _ int) ([]*atom_index.AtomIndex, error) {
	return r.list, r.listErr
}
func (r *fakeAtomRepo) MarkPublished(_ context.Context, _ string, _ atom_index.Status, _, _ string, _ int, _ string) error {
	return nil
}

// fakePathRepo is a learning_path.Repo with per-method error toggles.
type fakePathRepo struct {
	saveErr       error
	listByCourse  []*learning_path.LearningPath
	listCourseErr error
	getByTrio     *learning_path.LearningPath
	getByTrioErr  error
	saved         []*learning_path.LearningPath
}

func (r *fakePathRepo) Save(_ context.Context, p *learning_path.LearningPath) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, p)
	return nil
}
func (r *fakePathRepo) Get(_ context.Context, _ string) (*learning_path.LearningPath, error) {
	return nil, inmem.ErrNotFound
}
func (r *fakePathRepo) GetByCourseAndGCID(_ context.Context, _, _, _ string) (*learning_path.LearningPath, error) {
	return r.getByTrio, r.getByTrioErr
}

// ADR-233 provenance lookups (migration 0093). This fake serves the enrollment
// lane, which never converts a collection — both report not-found.
func (r *fakePathRepo) GetBySourceCollection(_ context.Context, _, _, _ string) (*learning_path.LearningPath, error) {
	return nil, inmem.ErrNotFound
}

func (r *fakePathRepo) GetByStudyListEventID(_ context.Context, _ string) (*learning_path.LearningPath, error) {
	return nil, inmem.ErrNotFound
}
func (r *fakePathRepo) ListByLearner(_ context.Context, _, _ string, _ int) ([]*learning_path.LearningPath, error) {
	return nil, nil
}
func (r *fakePathRepo) ListByLearnerWithAtom(_ context.Context, _, _, _ string) ([]*learning_path.LearningPath, error) {
	return nil, nil
}
func (r *fakePathRepo) ListByCourse(_ context.Context, _, _ string) ([]*learning_path.LearningPath, error) {
	return r.listByCourse, r.listCourseErr
}

// erroringStore is an idempotent.Store whose Process always errors, to drive
// the conservative markSeen no-op path (treat-as-seen on infra failure).
type erroringStore struct{}

func (erroringStore) Process(_ context.Context, _ string, _ time.Duration, _ func() error) error {
	return errInjected
}
func (erroringStore) Seen(_ context.Context, _ string) (bool, error)          { return false, nil }
func (erroringStore) Mark(_ context.Context, _ string, _ time.Duration) error { return nil }
func (erroringStore) CleanupExpired(_ context.Context) (int, error)           { return 0, nil }

// ----------------- validateInboundEnvelope -----------------

func TestValidateInboundEnvelope_PerFieldRejection(t *testing.T) {
	base := newTestEnvelope("evt-x", "t1", "g1")
	base.SourceService = "chora-delivery"
	cases := []struct {
		name   string
		mutate func(*events.Envelope)
	}{
		{"missing_event_id", func(e *events.Envelope) { e.EventID = "" }},
		{"missing_idempotency_key", func(e *events.Envelope) { e.IdempotencyKey = "" }},
		{"missing_tenant_id", func(e *events.Envelope) { e.TenantID = "" }},
		{"zero_occurred_at", func(e *events.Envelope) { e.OccurredAt = time.Time{} }},
		{"missing_traceparent", func(e *events.Envelope) { e.Traceparent = "" }},
		{"missing_source_project", func(e *events.Envelope) { e.SourceProject = "" }},
		{"missing_source_service", func(e *events.Envelope) { e.SourceService = "" }},
		{"schema_version_zero", func(e *events.Envelope) { e.SchemaVersion = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base
			tc.mutate(&env)
			if err := validateInboundEnvelope(env); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
	// A fully-populated envelope passes.
	if err := validateInboundEnvelope(base); err != nil {
		t.Errorf("valid envelope rejected: %v", err)
	}
}

// ----------------- newIdempotencyTrackerWithStore -----------------

func TestNewIdempotencyTrackerWithStore_NilStoreFallback(t *testing.T) {
	tr := newIdempotencyTrackerWithStore(nil, time.Hour)
	if tr.store == nil {
		t.Fatal("nil store should fall back to a MemoryStore")
	}
	// First mark is fresh, second is a duplicate — proves the fallback store
	// is functional.
	if !tr.markSeen("evt-1") {
		t.Error("first markSeen should be true (fresh)")
	}
	if tr.markSeen("evt-1") {
		t.Error("second markSeen should be false (duplicate)")
	}
}

func TestNewIdempotencyTrackerWithStore_NonPositiveTTLFallsBack(t *testing.T) {
	tr := newIdempotencyTrackerWithStore(idempotent.NewMemoryStore(), 0)
	if tr.ttl != inboxTTL {
		t.Errorf("ttl = %v; want fallback inboxTTL %v", tr.ttl, inboxTTL)
	}
	neg := newIdempotencyTrackerWithStore(idempotent.NewMemoryStore(), -5*time.Minute)
	if neg.ttl != inboxTTL {
		t.Errorf("negative ttl = %v; want fallback inboxTTL %v", neg.ttl, inboxTTL)
	}
}

// ----------------- markSeen store-error path -----------------

func TestMarkSeen_StoreErrorTreatedAsSeen(t *testing.T) {
	tr := newIdempotencyTrackerWithStore(erroringStore{}, time.Hour)
	// Store.Process errors → conservative no-op: treat as already-seen
	// (return false) so we do not double-process on transient infra failure.
	if tr.markSeen("evt-store-err") {
		t.Error("store error should yield markSeen=false (treat as already-seen)")
	}
}

func TestMarkSeen_EmptyEventIDShortCircuits(t *testing.T) {
	tr := newIdempotencyTracker()
	if !tr.markSeen("") {
		t.Error("empty eventID should short-circuit to true (no dedup possible)")
	}
}

// ----------------- AtomCreatedSubscriber error branches -----------------

func TestAtomCreatedSubscriber_PublishedAtFallsBackToOccurredAt(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	sub := NewAtomCreatedSubscriber(atomIdx, nil)
	env := newTestEnvelope("evt-pa", "t1", "g1")
	occurred := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	env.OccurredAt = occurred
	// PublishedAt left zero → handler stamps env.OccurredAt onto it.
	if err := sub.Handle(context.Background(), env, AtomCreatedPayload{
		AtomID: "atom-pa", TenantID: "t1", AtomType: "mcq",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, err := atomIdx.Get(context.Background(), "atom-pa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.PublishedAt.Equal(occurred) {
		t.Errorf("PublishedAt = %v; want fallback to OccurredAt %v", got.PublishedAt, occurred)
	}
}

func TestAtomCreatedSubscriber_RejectsInvalidPayload(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	sub := NewAtomCreatedSubscriber(atomIdx, nil)
	env := newTestEnvelope("evt-bad-payload", "t1", "g1")
	// Empty AtomID → atom_index.New fails (ErrInvalidAtom).
	err := sub.Handle(context.Background(), env, AtomCreatedPayload{
		AtomID: "", TenantID: "t1", PublishedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected error for empty atom_id payload")
	}
}

func TestAtomCreatedSubscriber_SaveErrorPropagates(t *testing.T) {
	repo := &fakeAtomRepo{saveErr: errInjected}
	sub := NewAtomCreatedSubscriber(repo, nil)
	env := newTestEnvelope("evt-save-err", "t1", "g1")
	err := sub.Handle(context.Background(), env, AtomCreatedPayload{
		AtomID: "atom-1", TenantID: "t1", AtomType: "mcq", PublishedAt: time.Now().UTC(),
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want wrapped injected save error", err)
	}
}

func TestAtomCreatedSubscriber_ListByCourseErrorPropagates(t *testing.T) {
	atomRepo := &fakeAtomRepo{} // Save succeeds
	pathRepo := &fakePathRepo{listCourseErr: errInjected}
	sub := NewAtomCreatedSubscriber(atomRepo, pathRepo)
	env := newTestEnvelope("evt-list-err", "t1", "g1")
	err := sub.Handle(context.Background(), env, AtomCreatedPayload{
		AtomID: "atom-1", TenantID: "t1", CourseID: "c1", AtomType: "mcq",
		PublishedAt: time.Now().UTC(),
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want wrapped injected list error", err)
	}
}

func TestAtomCreatedSubscriber_RetroactiveAppendSaveErrorPropagates(t *testing.T) {
	// A real path bound to course c1 (so AppendAtom returns true and the save
	// is attempted) but the path repo errors on Save.
	now := time.Now().UTC()
	path, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: "t1", LearnerGCID: "phyllis", CourseID: "c1", Now: now,
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	atomRepo := &fakeAtomRepo{}
	pathRepo := &fakePathRepo{
		listByCourse: []*learning_path.LearningPath{path},
		saveErr:      errInjected,
	}
	sub := NewAtomCreatedSubscriber(atomRepo, pathRepo)
	env := newTestEnvelope("evt-append-save-err", "t1", "g1")
	herr := sub.Handle(context.Background(), env, AtomCreatedPayload{
		AtomID: "atom-new", TenantID: "t1", CourseID: "c1", AtomType: "mcq",
		PublishedAt: now,
	})
	if !errors.Is(herr, errInjected) {
		t.Errorf("err = %v; want wrapped injected path-save error", herr)
	}
}

// ----------------- EnrollmentCreatedSubscriber error branches -----------------

func TestEnrollmentCreatedSubscriber_RejectsBlankEnvelope(t *testing.T) {
	sub := NewEnrollmentCreatedSubscriber(&fakePathRepo{}, &fakeContentRepo{}, events.NewInMemoryPublisher())
	if err := sub.Handle(context.Background(), events.Envelope{}, EnrollmentCreatedPayload{
		CourseID: "c1", LearnerGCID: "g1", TenantID: "t1",
	}); err == nil {
		t.Error("expected error on blank envelope")
	}
}

func TestEnrollmentCreatedSubscriber_CurriculumListErrorPropagates(t *testing.T) {
	pathRepo := &fakePathRepo{getByTrioErr: inmem.ErrNotFound} // no existing path
	contentRepo := &fakeContentRepo{listErr: errInjected}
	sub := NewEnrollmentCreatedSubscriber(pathRepo, contentRepo, events.NewInMemoryPublisher())
	env := newTestEnvelope("evt-enr-list-err", "t1", "g1")
	env.SourceService = "chora-delivery"
	err := sub.Handle(context.Background(), env, EnrollmentCreatedPayload{
		EnrollmentID: "e1", CourseID: "c1", LearnerGCID: "g1", TenantID: "t1",
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want wrapped injected curriculum-list error", err)
	}
}

func TestEnrollmentCreatedSubscriber_BootstrapValidationErrorPropagates(t *testing.T) {
	// Empty CourseID in the PAYLOAD makes BootstrapFromEnrollment fail
	// (course_id required) — even though the envelope is valid.
	pathRepo := &fakePathRepo{getByTrioErr: inmem.ErrNotFound}
	contentRepo := &fakeContentRepo{}
	sub := NewEnrollmentCreatedSubscriber(pathRepo, contentRepo, events.NewInMemoryPublisher())
	env := newTestEnvelope("evt-enr-bootstrap-err", "t1", "g1")
	env.SourceService = "chora-delivery"
	err := sub.Handle(context.Background(), env, EnrollmentCreatedPayload{
		EnrollmentID: "e1", CourseID: "", LearnerGCID: "g1", TenantID: "t1",
	})
	if err == nil {
		t.Fatal("expected bootstrap validation error for empty course_id")
	}
}

func TestEnrollmentCreatedSubscriber_PathSaveErrorPropagates(t *testing.T) {
	pathRepo := &fakePathRepo{getByTrioErr: inmem.ErrNotFound, saveErr: errInjected}
	contentRepo := &fakeContentRepo{}
	sub := NewEnrollmentCreatedSubscriber(pathRepo, contentRepo, events.NewInMemoryPublisher())
	env := newTestEnvelope("evt-enr-save-err", "t1", "g1")
	env.SourceService = "chora-delivery"
	err := sub.Handle(context.Background(), env, EnrollmentCreatedPayload{
		EnrollmentID: "e1", CourseID: "c1", LearnerGCID: "g1", TenantID: "t1",
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want wrapped injected path-save error", err)
	}
}
