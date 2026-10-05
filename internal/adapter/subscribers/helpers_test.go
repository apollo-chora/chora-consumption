// helpers_test.go — shared seed/test helpers for the subscribers package.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

func atomIndexHelper(atomID, tenantID, courseID string, correctOptionID string) (*atom_index.AtomIndex, error) {
	return atom_index.New(atom_index.NewParams{
		AtomID:          atomID,
		TenantID:        tenantID,
		CourseID:        courseID,
		AtomType:        "mcq",
		CorrectOptionID: correctOptionID,
		AnswerCount:     4,
		PublishedAt:     time.Now().UTC(),
	})
}

func atomIndexHelperWithTopics(atomID, tenantID, courseID string, topics []string) (*atom_index.AtomIndex, error) {
	return atom_index.New(atom_index.NewParams{
		AtomID:          atomID,
		TenantID:        tenantID,
		CourseID:        courseID,
		AtomType:        "mcq",
		TopicTags:       topics,
		CorrectOptionID: "opt-a",
		AnswerCount:     4,
		PublishedAt:     time.Now().UTC(),
	})
}

// atomIndexFromFreshParams builds an arbitrary atom_index entry with the
// given atom_type. Pass `correctOptionID=""` for non-MCQ atoms (which won't
// be eligible for MCQ-accuracy projection).
func atomIndexFromFreshParams(atomID, tenantID, courseID, atomType string, topics []string, correctOptionID string) (*atom_index.AtomIndex, error) {
	return atom_index.New(atom_index.NewParams{
		AtomID:          atomID,
		TenantID:        tenantID,
		CourseID:        courseID,
		AtomType:        atomType,
		TopicTags:       topics,
		CorrectOptionID: correctOptionID,
		AnswerCount:     0,
		PublishedAt:     time.Now().UTC(),
	})
}

// --- CHO-2167 dosing-projection port helpers --------------------------------
//
// The projections are now reached through domain ports (ctx + error), so the
// reads are fallible. These keep the assertions readable AND fail loud: a
// storage error in a test must never read as "empty projection".

func aptTopics(t *testing.T, r active_path_topics.Repository, tenantID, gcid string) map[string]bool {
	t.Helper()
	got, err := r.GetTopics(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("GetTopics(%s,%s): %v", tenantID, gcid, err)
	}
	return got
}

func accByLearner(t *testing.T, r topic_accuracy.Repository, tenantID, gcid string) map[string]float64 {
	t.Helper()
	got, err := r.GetByLearner(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("GetByLearner(%s,%s): %v", tenantID, gcid, err)
	}
	return got
}
