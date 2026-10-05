package http

// course_directory_reader_test.go — white-box unit tests for the Server's
// CHO-2059 course-title resolution helper (progress_mirror). Proves: nil reader
// → empty (byte-identical fallback), wired reader → resolved titles, no course
// refs → no lookup, and a reader error is non-fatal (additive enrichment).

import (
	"context"
	"errors"
	"testing"

	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

const srvCourseID = "05000000-0000-7000-8000-0000000c5301"

type srvStubCourseDir struct {
	titles map[string]string
	err    error
	gotIDs []string
	calls  int
}

func (s *srvStubCourseDir) LookupTitles(_ context.Context, ids []string) (map[string]string, error) {
	s.calls++
	s.gotIDs = ids
	if s.err != nil {
		return nil, s.err
	}
	return s.titles, nil
}

func TestServerResolveCourseTitles_NilReader_Empty(t *testing.T) {
	s := &Server{} // CourseDirectory nil
	got := s.resolveCourseTitles(context.Background(),
		[]*lp.Fact{{Type: lp.FactEnrollment, RefID: srvCourseID}}, nil)
	if len(got) != 0 {
		t.Errorf("nil reader must return empty map (byte-identical fallback); got %v", got)
	}
}

func TestServerResolveCourseTitles_Wired_Resolves(t *testing.T) {
	dir := &srvStubCourseDir{titles: map[string]string{srvCourseID: "Algebra I"}}
	s := &Server{CourseDirectory: dir}
	facts := []*lp.Fact{{Type: lp.FactEnrollment, RefID: srvCourseID}}
	activity := []*lp.ActivityEntry{{Kind: "enrolled", RefID: srvCourseID}}
	got := s.resolveCourseTitles(context.Background(), facts, activity)
	if got[srvCourseID] != "Algebra I" {
		t.Errorf("wired reader must resolve the title; got %v", got)
	}
	// One deduped lookup for the single course referenced by both fact + activity.
	if dir.calls != 1 || len(dir.gotIDs) != 1 || dir.gotIDs[0] != srvCourseID {
		t.Errorf("expected one lookup for [%s]; calls=%d ids=%v", srvCourseID, dir.calls, dir.gotIDs)
	}
}

func TestServerResolveCourseTitles_NoCourseRefs_NoLookup(t *testing.T) {
	dir := &srvStubCourseDir{}
	s := &Server{CourseDirectory: dir}
	// A preference fact references no course → short-circuit before any lookup.
	got := s.resolveCourseTitles(context.Background(),
		[]*lp.Fact{{Type: lp.FactPreference, RefID: "dose_excluded_topics"}}, nil)
	if len(got) != 0 {
		t.Errorf("no course refs → empty map; got %v", got)
	}
	if dir.calls != 0 {
		t.Errorf("no course refs → no lookup; calls=%d", dir.calls)
	}
}

func TestServerResolveCourseTitles_ErrorNonFatal(t *testing.T) {
	dir := &srvStubCourseDir{err: errors.New("directory down")}
	s := &Server{CourseDirectory: dir}
	facts := []*lp.Fact{{Type: lp.FactEnrollment, RefID: srvCourseID}}
	got := s.resolveCourseTitles(context.Background(), facts, nil)
	if len(got) != 0 {
		t.Errorf("reader error must fall back to empty map (non-fatal); got %v", got)
	}
}

// -----------------------------------------------------------------------------
// CHO-2247 Guard 2 — an unresolved course title must not be SILENT
// -----------------------------------------------------------------------------

// TestLookupCourseTitles_MissFiresObserver is the guard the CHO-2247 gap needed
// and did not have: course_directory sat at 5 rows against 19 courses for weeks
// while learners were served placeholder titles, and NOTHING anywhere counted a
// miss. lookupCourseTitles logged loud on a lookup ERROR only — a clean lookup
// that simply had no row was, and this test ensures no longer is, invisible.
func TestLookupCourseTitles_MissFiresObserver(t *testing.T) {
	const resolved = "019f6966-1a0f-76a6-a824-02f7393581da"
	const missing = "019eb059-c77f-7de5-9aae-9c72a1c37636"
	srv := newMeServer()
	srv.CourseDirectory = &srvStubCourseDir{titles: map[string]string{resolved: "Scrum Framework Essentials"}}

	var got []string
	var calls int
	srv.OnUnresolvedCourseTitles = func(_ context.Context, ids []string) {
		calls++
		got = append([]string(nil), ids...)
	}

	titles := srv.LookupCourseTitlesForTest(context.Background(), []string{resolved, missing})
	if titles[resolved] != "Scrum Framework Essentials" {
		t.Fatalf("resolved title = %q; want the real title", titles[resolved])
	}
	if calls != 1 {
		t.Fatalf("observer calls = %d; want exactly 1 (a placeholder render MUST be observable)", calls)
	}
	if len(got) != 1 || got[0] != missing {
		t.Fatalf("observer ids = %v; want exactly [%s]", got, missing)
	}
}

// TestLookupCourseTitles_AllResolved_NoObserver — the observer must fire ONLY on
// a real miss. A guard that fires on the happy path is noise and gets muted,
// which is how the next gap hides.
func TestLookupCourseTitles_AllResolved_NoObserver(t *testing.T) {
	const a = "019f6966-1a0f-76a6-a824-02f7393581da"
	srv := newMeServer()
	srv.CourseDirectory = &srvStubCourseDir{titles: map[string]string{a: "Scrum Framework Essentials"}}
	calls := 0
	srv.OnUnresolvedCourseTitles = func(_ context.Context, _ []string) { calls++ }
	srv.LookupCourseTitlesForTest(context.Background(), []string{a})
	if calls != 0 {
		t.Fatalf("observer fired %d time(s) on a fully-resolved lookup; want 0", calls)
	}
}

// TestLookupCourseTitles_NilDirectory_NoObserver — a projection that is not
// wired at all (no pgx pool) is a DIFFERENT condition from a missing row, and is
// already logged at boot by wireCourseDirectoryRepo. Firing the miss observer
// here would blame the data for a wiring choice.
func TestLookupCourseTitles_NilDirectory_NoObserver(t *testing.T) {
	srv := newMeServer()
	srv.CourseDirectory = nil
	calls := 0
	srv.OnUnresolvedCourseTitles = func(_ context.Context, _ []string) { calls++ }
	srv.LookupCourseTitlesForTest(context.Background(), []string{"019f6966-1a0f-76a6-a824-02f7393581da"})
	if calls != 0 {
		t.Fatalf("observer fired %d time(s) with no directory wired; want 0", calls)
	}
}
