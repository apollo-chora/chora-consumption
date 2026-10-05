// dosing_test_helpers_test.go — CHO-2167.
//
// The in-memory dosing projections now sit behind the same domain ports the pg
// adapters satisfy, so their methods take a ctx and return an error. These
// helpers keep the existing assertions readable while still failing loud — an
// error must never be mistaken for an empty projection, which is the exact
// confusion that let this bug live in production.
package inmem

import (
	"context"
	"testing"
)

// testPathID stands in for the learning_path_id the durable projection needs as
// part of its primary key.
const testPathID = "0197a000-0000-7000-8000-00000000cafe"

func mustRecordAttempt(t *testing.T, r *TopicAccuracyRepo, tenantID, gcid, topic string, correct bool) {
	t.Helper()
	if err := r.RecordAttempt(context.Background(), tenantID, gcid, topic, correct); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
}

func mustGetByLearner(t *testing.T, r *TopicAccuracyRepo, tenantID, gcid string) map[string]float64 {
	t.Helper()
	got, err := r.GetByLearner(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("GetByLearner: %v", err)
	}
	return got
}

func mustRecordPathTopics(t *testing.T, r *ActivePathTopicsRepo, tenantID, gcid string, topics []string) {
	t.Helper()
	if err := r.RecordPathTopics(context.Background(), tenantID, gcid, testPathID, topics); err != nil {
		t.Fatalf("RecordPathTopics: %v", err)
	}
}

func mustGetTopics(t *testing.T, r *ActivePathTopicsRepo, tenantID, gcid string) map[string]bool {
	t.Helper()
	got, err := r.GetTopics(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("GetTopics: %v", err)
	}
	return got
}
