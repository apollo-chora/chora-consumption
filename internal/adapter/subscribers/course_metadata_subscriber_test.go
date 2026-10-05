package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_directory"
)

// fakeCourseDir captures Upsert calls (and can be armed to fail loudly).
type fakeCourseDir struct {
	upserts []course_directory.CourseDirectoryEntry
	failErr error
}

func (f *fakeCourseDir) Upsert(_ context.Context, e course_directory.CourseDirectoryEntry) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.upserts = append(f.upserts, e)
	return nil
}

func (f *fakeCourseDir) LookupTitles(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range f.upserts {
		for _, id := range ids {
			if e.CourseID == id {
				out[id] = e.Title
			}
		}
	}
	return out, nil
}

func cmEnvelope(eventID string, occurredAt time.Time) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: eventID,
		TenantID:       "11111111-1111-7111-8111-111111111111",
		OccurredAt:     occurredAt,
		PublishedAt:    occurredAt,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-delivery",
		SchemaVersion:  1,
	}
}

func TestCourseMetadata_ProjectsTitle(t *testing.T) {
	dir := &fakeCourseDir{}
	sub := NewCourseMetadataSubscriber(dir)
	occurred := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	env := cmEnvelope("evt-cm-1", occurred)
	p := CourseMetadataPayload{CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d", Title: "Algebra I"}
	if err := sub.Handle(env, p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(dir.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(dir.upserts))
	}
	got := dir.upserts[0]
	if got.CourseID != p.CourseID || got.Title != p.Title {
		t.Errorf("upsert = %+v, want course_id/title %s/%s", got, p.CourseID, p.Title)
	}
	if !got.UpdatedAt.Equal(occurred) {
		t.Errorf("UpdatedAt = %v, want env.OccurredAt %v", got.UpdatedAt, occurred)
	}
}

func TestCourseMetadata_Idempotent(t *testing.T) {
	dir := &fakeCourseDir{}
	sub := NewCourseMetadataSubscriber(dir)
	env := cmEnvelope("evt-cm-dup", time.Now().UTC())
	p := CourseMetadataPayload{CourseID: "019e30db-692f-7d10-8ce0-59669fe9298d", Title: "Algebra I"}
	_ = sub.Handle(env, p)
	_ = sub.Handle(env, p) // same event_id → deduped
	if len(dir.upserts) != 1 {
		t.Fatalf("expected 1 upsert (idempotent), got %d", len(dir.upserts))
	}
}

func TestCourseMetadata_RejectsInvalidEnvelope(t *testing.T) {
	dir := &fakeCourseDir{}
	sub := NewCourseMetadataSubscriber(dir)
	env := cmEnvelope("", time.Now().UTC()) // missing event_id
	if err := sub.Handle(env, CourseMetadataPayload{CourseID: "c", Title: "t"}); err == nil {
		t.Fatal("expected an error for an invalid envelope")
	}
	if len(dir.upserts) != 0 {
		t.Errorf("no upsert should run on an invalid envelope; got %d", len(dir.upserts))
	}
}

func TestCourseMetadata_UpsertErrorIsLoud(t *testing.T) {
	sentinel := errors.New("boom")
	dir := &fakeCourseDir{failErr: sentinel}
	sub := NewCourseMetadataSubscriber(dir)
	env := cmEnvelope("evt-cm-fail", time.Now().UTC())
	if err := sub.Handle(env, CourseMetadataPayload{CourseID: "c", Title: "t"}); !errors.Is(err, sentinel) {
		t.Fatalf("expected the upsert error to surface (fail-loud); got %v", err)
	}
}

func TestCourseMetadata_UpdatedAtFallbackToPublishedAt(t *testing.T) {
	// Defensive fallback: when OccurredAt is zero, PublishedAt is used. (A valid
	// envelope always carries OccurredAt, so this is exercised via the helper.)
	published := time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC)
	env := events.Envelope{PublishedAt: published} // OccurredAt zero
	if got := courseMetaUpdatedAt(env); !got.Equal(published) {
		t.Errorf("courseMetaUpdatedAt zero-OccurredAt = %v, want PublishedAt %v", got, published)
	}
	occurred := published.Add(-time.Hour)
	env2 := events.Envelope{OccurredAt: occurred, PublishedAt: published}
	if got := courseMetaUpdatedAt(env2); !got.Equal(occurred) {
		t.Errorf("courseMetaUpdatedAt = %v, want OccurredAt %v", got, occurred)
	}
}
