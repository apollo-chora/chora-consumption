package course_directory

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	cid1 = "01970000-0000-7000-b000-000000000001"
	cid2 = "01970000-0000-7000-b000-000000000002"
)

func TestInMem_Upsert_RejectsEmptyCourseID(t *testing.T) {
	t.Parallel()
	d := NewInMemCourseDirectory()
	if err := d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: "", Title: "x", UpdatedAt: time.Now()}); !errors.Is(err, ErrCourseIDRequired) {
		t.Fatalf("expected ErrCourseIDRequired; got %v", err)
	}
	// whitespace-only is also empty
	if err := d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: "   ", Title: "x", UpdatedAt: time.Now()}); !errors.Is(err, ErrCourseIDRequired) {
		t.Fatalf("expected ErrCourseIDRequired for whitespace; got %v", err)
	}
}

func TestInMem_UpsertThenLookup(t *testing.T) {
	t.Parallel()
	d := NewInMemCourseDirectory()
	ts := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	if err := d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "Algebra I", UpdatedAt: ts}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := d.LookupTitles(context.Background(), []string{cid1, cid2})
	if err != nil {
		t.Fatalf("LookupTitles: %v", err)
	}
	if got[cid1] != "Algebra I" {
		t.Errorf("cid1 title = %q, want Algebra I", got[cid1])
	}
	if _, ok := got[cid2]; ok {
		t.Errorf("cid2 absent should not be in the map; got %q", got[cid2])
	}
}

func TestInMem_Upsert_LastWriterWins_NewerWins(t *testing.T) {
	t.Parallel()
	d := NewInMemCourseDirectory()
	older := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	_ = d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "Old Title", UpdatedAt: older})
	_ = d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "New Title", UpdatedAt: newer})
	got, _ := d.LookupTitles(context.Background(), []string{cid1})
	if got[cid1] != "New Title" {
		t.Errorf("newer write should win: got %q, want New Title", got[cid1])
	}
}

func TestInMem_Upsert_LastWriterWins_StaleIgnored(t *testing.T) {
	t.Parallel()
	d := NewInMemCourseDirectory()
	older := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	// Apply newer FIRST, then a stale (out-of-order) redelivery must NOT clobber.
	_ = d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "New Title", UpdatedAt: newer})
	_ = d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "Stale Title", UpdatedAt: older})
	got, _ := d.LookupTitles(context.Background(), []string{cid1})
	if got[cid1] != "New Title" {
		t.Errorf("stale out-of-order write must be ignored: got %q, want New Title", got[cid1])
	}
}

func TestInMem_Upsert_EqualTimestampReplay_NoOp(t *testing.T) {
	t.Parallel()
	d := NewInMemCourseDirectory()
	ts := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	_ = d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "First", UpdatedAt: ts})
	// Equal-timestamp replay with a DIFFERENT title must no-op (strict < LWW).
	_ = d.Upsert(context.Background(), CourseDirectoryEntry{CourseID: cid1, Title: "Second", UpdatedAt: ts})
	got, _ := d.LookupTitles(context.Background(), []string{cid1})
	if got[cid1] != "First" {
		t.Errorf("equal-timestamp replay must no-op: got %q, want First", got[cid1])
	}
}

func TestInMem_LookupTitles_EmptyInput_ShortCircuits(t *testing.T) {
	t.Parallel()
	d := NewInMemCourseDirectory()
	got, err := d.LookupTitles(context.Background(), nil)
	if err != nil {
		t.Fatalf("LookupTitles(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
	got, err = d.LookupTitles(context.Background(), []string{})
	if err != nil {
		t.Fatalf("LookupTitles([]): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}

// TestInMem_SatisfiesPort guards the InMem adapter satisfies the port at test
// time too (the production file carries the compile-time assertion).
func TestInMem_SatisfiesPort(t *testing.T) {
	t.Parallel()
	var _ CourseDirectoryPort = NewInMemCourseDirectory()
}
