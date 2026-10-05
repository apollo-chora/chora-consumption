// dosing_ports_conformance_test.go — CHO-2167 RED.
//
// The two daily-dose projections (topic_accuracy → WEAKNESS slot,
// active_path_topics → CURIOSITY slot) shipped with a pg adapter that was
// never reachable from the composition root: ExtServer/Server held the
// CONCRETE *inmem.* types, so cmd/server could not bind the durable adapter
// even though it existed. The projection therefore lived only in process
// memory (active_path_topics: 0 rows in chora_consumption, verified live).
//
// The fix is a domain-owned port both adapters satisfy. This test is the
// compile-time contract for the pg side.
package pg

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

func TestPgDosingReposSatisfyDomainPorts(t *testing.T) {
	var (
		_ topic_accuracy.Repository     = (*TopicAccuracyRepo)(nil)
		_ active_path_topics.Repository = (*ActivePathTopicsRepo)(nil)
	)
}
