package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

// fakeProjRepo is an in-test ProjectionRepo capturing replace calls (armable
// to fail transiently via failErr).
type fakeProjRepo struct {
	byCourse map[string][]*course_content.Item
	replaces int
	lastCtx  context.Context // captured so the RLS-ctx test can assert the tenant
	failErr  error
}

func newFakeProjRepo() *fakeProjRepo {
	return &fakeProjRepo{byCourse: map[string][]*course_content.Item{}}
}

func (r *fakeProjRepo) ReplaceByCourse(ctx context.Context, tenantID, courseID string, items []*course_content.Item) error {
	if r.failErr != nil {
		return r.failErr
	}
	r.replaces++
	r.lastCtx = ctx
	r.byCourse[tenantID+"/"+courseID] = items
	return nil
}

func (r *fakeProjRepo) ListByCourse(_ context.Context, tenantID, courseID string) ([]*course_content.Item, error) {
	return r.byCourse[tenantID+"/"+courseID], nil
}

func ccEnvelope(eventID string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: eventID,
		TenantID:       "11111111-1111-7111-8111-111111111111",
		OccurredAt:     time.Now().UTC(),
		PublishedAt:    time.Now().UTC(),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-delivery",
		SchemaVersion:  1,
	}
}

func TestCourseContentComposed_ProjectsItems(t *testing.T) {
	repo := newFakeProjRepo()
	sub := NewCourseContentComposedSubscriber(repo, nil)
	env := ccEnvelope("evt-cc-1")
	p := CourseContentComposedPayload{
		CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d",
		Items: []CourseContentItemPayload{
			{ItemID: "i1", Kind: "atom", Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "Intro", Position: 0},
			{ItemID: "i2", Kind: "video", Ref: "https://cdn/x.mp4", Title: "Lecture", Position: 1},
		},
	}
	if err := sub.Handle(env, p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, _ := repo.ListByCourse(context.Background(), env.TenantID, p.CourseID)
	if len(got) != 2 {
		t.Fatalf("expected 2 projected items, got %d", len(got))
	}
	if got[0].Kind != course_content.KindAtom || got[1].Kind != course_content.KindVideo {
		t.Fatalf("kinds not projected correctly")
	}
}

func TestCourseContentComposed_ReplaceSemantics(t *testing.T) {
	repo := newFakeProjRepo()
	sub := NewCourseContentComposedSubscriber(repo, nil)
	courseID := "019e30db-692f-7d10-8ce0-59669fe9298d"
	_ = sub.Handle(ccEnvelope("evt-cc-a"), CourseContentComposedPayload{
		CourseID: courseID,
		Items:    []CourseContentItemPayload{{ItemID: "i1", Kind: "atom", Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "a", Position: 0}},
	})
	// second event with a different (smaller) set replaces, not appends
	_ = sub.Handle(ccEnvelope("evt-cc-b"), CourseContentComposedPayload{
		CourseID: courseID,
		Items:    []CourseContentItemPayload{},
	})
	got, _ := repo.ListByCourse(context.Background(), "11111111-1111-7111-8111-111111111111", courseID)
	if len(got) != 0 {
		t.Fatalf("replace semantics: expected 0 after empty event, got %d", len(got))
	}
}

func TestCourseContentComposed_Idempotent(t *testing.T) {
	repo := newFakeProjRepo()
	sub := NewCourseContentComposedSubscriber(repo, nil)
	p := CourseContentComposedPayload{
		CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d",
		Items:    []CourseContentItemPayload{{ItemID: "i1", Kind: "atom", Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "a", Position: 0}},
	}
	env := ccEnvelope("evt-cc-dup")
	_ = sub.Handle(env, p)
	_ = sub.Handle(env, p) // same event_id → deduped
	if repo.replaces != 1 {
		t.Fatalf("expected 1 replace (idempotent), got %d", repo.replaces)
	}
}

// Regression: the pg ProjectionRepo runs rls.ApplySession, which reads the
// tenant from the CONTEXT. The subscriber must stamp the envelope tenant onto
// the ctx it passes to ReplaceByCourse — a bare context.Background() yields
// rls.ErrNoTenantContext in prod and the projection errors on every event.
// The in-mem fake ignores ctx, so without this assertion the bug was masked.
func TestCourseContentComposed_StampsTenantOnRepoContext(t *testing.T) {
	repo := newFakeProjRepo()
	sub := NewCourseContentComposedSubscriber(repo, nil)
	env := ccEnvelope("evt-cc-rls")
	if err := sub.Handle(env, CourseContentComposedPayload{
		CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d",
		Items:    []CourseContentItemPayload{{ItemID: "i1", Kind: "atom", Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "a", Position: 0}},
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := tracing.TenantIDFromContext(repo.lastCtx); got != env.TenantID {
		t.Fatalf("repo ctx tenant = %q, want %q (RLS would drop the projection)", got, env.TenantID)
	}
}

func TestCourseContentComposed_SkipsInvalidItems(t *testing.T) {
	repo := newFakeProjRepo()
	sub := NewCourseContentComposedSubscriber(repo, nil)
	p := CourseContentComposedPayload{
		CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d",
		Items: []CourseContentItemPayload{
			{ItemID: "i1", Kind: "atom", Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "ok", Position: 0},
			{ItemID: "i2", Kind: "bogus", Ref: "x", Title: "bad", Position: 1}, // invalid kind → skipped
		},
	}
	if err := sub.Handle(ccEnvelope("evt-cc-mix"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, _ := repo.ListByCourse(context.Background(), "11111111-1111-7111-8111-111111111111", p.CourseID)
	if len(got) != 1 {
		t.Fatalf("expected 1 valid item (invalid skipped), got %d", len(got))
	}
}
