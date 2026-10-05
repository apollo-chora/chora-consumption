package grpc

// companion_p1b_course_name_test.go — CHO-2059 follow-up: ReadLearnerProfile
// stitches real course NAMES from the course_directory projection onto the
// LearnerProfile facts/activity, and — critically — is byte-identical to the
// pre-projection behaviour when the reader is nil or errors (the projection is
// purely additive).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

const cnCourseID = "05000000-0000-7000-8000-0000000c5301"

type fakeCourseDirReader struct {
	titles map[string]string
	err    error
	called [][]string
}

func (f *fakeCourseDirReader) LookupTitles(_ context.Context, ids []string) (map[string]string, error) {
	f.called = append(f.called, ids)
	if f.err != nil {
		return nil, f.err
	}
	return f.titles, nil
}

// unnamedCourseFixture: an enrollment fact + enrolled activity for a course with
// NO human label — so WITHOUT resolution both read as the generic "a course".
func unnamedCourseFixture() ([]*lp.Fact, []*lp.ActivityEntry) {
	recent := time.Now().UTC().Add(-2 * 24 * time.Hour)
	return []*lp.Fact{
			{Type: lp.FactEnrollment, RefID: cnCourseID, OccurredAt: recent},
		}, []*lp.ActivityEntry{
			{Kind: "enrolled", RefID: cnCourseID, OccurredAt: recent},
		}
}

func TestReadLearnerProfile_ResolvesCourseName(t *testing.T) {
	facts, activity := unnamedCourseFixture()
	dir := &fakeCourseDirReader{titles: map[string]string{cnCourseID: "Algebra I"}}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithLearnerProfileReader(fakeProfileReader{facts: facts, activity: activity}),
		WithCourseDirectoryReader(dir),
	)
	seedStage2(repo)

	resp, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Window: "all",
	})
	if err != nil {
		t.Fatalf("ReadLearnerProfile: %v", err)
	}
	if got := resp.GetFacts()[0].GetTitle(); got != "Algebra I" {
		t.Errorf("fact title = %q, want resolved Algebra I", got)
	}
	if dj := resp.GetFacts()[0].GetDetailJson(); !strings.Contains(dj, "Algebra I") {
		t.Errorf("detail_json missing resolved name: %s", dj)
	}
	if got := resp.GetRecentActivity()[0].GetSummary(); got != "Algebra I" {
		t.Errorf("activity summary = %q, want resolved Algebra I", got)
	}
	// The directory was queried once with exactly the collected course id.
	if len(dir.called) != 1 || len(dir.called[0]) != 1 || dir.called[0][0] != cnCourseID {
		t.Errorf("LookupTitles called with %v, want one lookup for [%s]", dir.called, cnCourseID)
	}
}

func TestReadLearnerProfile_NilCourseDirectory_GenericNounUnchanged(t *testing.T) {
	// ABSOLUTE REQUIREMENT (CHO-2059): a nil/unwired reader is byte-identical to
	// today — the un-named course reads as the leak-free generic noun.
	facts, activity := unnamedCourseFixture()
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithLearnerProfileReader(fakeProfileReader{facts: facts, activity: activity}),
		// NO WithCourseDirectoryReader → s.courseDir stays nil
	)
	seedStage2(repo)

	resp, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Window: "all",
	})
	if err != nil {
		t.Fatalf("ReadLearnerProfile: %v", err)
	}
	if got := resp.GetFacts()[0].GetTitle(); got != "a course" {
		t.Errorf("nil reader must render the generic noun; got %q", got)
	}
	if got := resp.GetRecentActivity()[0].GetSummary(); got != "Enrolled in a course" {
		t.Errorf("nil reader activity must be unchanged; got %q", got)
	}
}

func TestReadLearnerProfile_CourseDirErrorIsNonFatal(t *testing.T) {
	// A directory read failure must NOT fail the profile read (additive
	// enrichment) — it falls back to the generic noun.
	facts, activity := unnamedCourseFixture()
	dir := &fakeCourseDirReader{err: errors.New("directory down")}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithLearnerProfileReader(fakeProfileReader{facts: facts, activity: activity}),
		WithCourseDirectoryReader(dir),
	)
	seedStage2(repo)

	resp, err := s.ReadLearnerProfile(context.Background(), &consumptionv1.ReadLearnerProfileRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Window: "all",
	})
	if err != nil {
		t.Fatalf("a directory error must be non-fatal; got %v", err)
	}
	if got := resp.GetFacts()[0].GetTitle(); got != "a course" {
		t.Errorf("directory error must fall back to generic noun; got %q", got)
	}
}
