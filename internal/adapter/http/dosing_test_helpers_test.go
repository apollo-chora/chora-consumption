// dosing_test_helpers_test.go — CHO-2167.
//
// The dose projections are reached through domain ports now (pg in production,
// in-memory in dev), so reads take a ctx and can fail. These helpers keep the
// existing wiring assertions readable and fail loud: a storage error must never
// be silently read as "the learner has no topics".
package http

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

// dosingTestPathID stands in for the learning_path_id that is part of the
// durable projection's primary key.
const dosingTestPathID = "0197a000-0000-7000-8000-00000000beef"

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

func mustRecordPathTopics(t *testing.T, r active_path_topics.Repository, tenantID, gcid string, topics []string) {
	t.Helper()
	if err := r.RecordPathTopics(context.Background(), tenantID, gcid, dosingTestPathID, topics); err != nil {
		t.Fatalf("RecordPathTopics: %v", err)
	}
}
