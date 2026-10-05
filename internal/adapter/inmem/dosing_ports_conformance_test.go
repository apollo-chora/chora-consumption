// dosing_ports_conformance_test.go — CHO-2167 RED.
//
// The in-memory dosing projections are the DEV FALLBACK behind the same
// domain ports the pg adapters satisfy. Holding them as concrete types in the
// composition root is what made the pg adapter unreachable in production.
//
// Both adapters must be substitutable through the port, which means the
// in-memory side has to grow the ctx + error + learning_path_id surface the
// durable side has always had.
package inmem

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

func TestInmemDosingReposSatisfyDomainPorts(t *testing.T) {
	var (
		_ topic_accuracy.Repository     = (*TopicAccuracyRepo)(nil)
		_ active_path_topics.Repository = (*ActivePathTopicsRepo)(nil)
	)
}
