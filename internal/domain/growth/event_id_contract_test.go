package growth_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// CHO-2225 — the event_id envelope contract.
//
// NewService used to substitute a defaultIDFactory emitting "evt-<unixnano>"
// whenever a composition root omitted NewID, under a doc comment promising
// "production injects a UUIDv7 mint". cmd/server/growth_wiring.go never did,
// so every growth event carried a non-UUID event_id and chora-sharing's
// milestone subscriber — which keys idempotency on it against a UUID column —
// failed SQLSTATE 22P02 on 100% of messages for hours, with no DLQ to catch
// them.
//
// event_id is a MANDATORY UUIDv7 envelope field, so NewID is exactly as
// load-bearing as Repo/Outbox/Dist. NewService must REFUSE a nil one rather
// than silently mint ids no consumer can store.
func TestNewService_RequiresNewID(t *testing.T) {
	_, err := growth.NewService(growth.ServiceConfig{
		Repo:   newFakeRepo(),
		Outbox: &fakeOutbox{},
		Dist:   stubDist{dist: defaultDistribution()},
	})
	if err == nil {
		t.Fatal("NewService accepted a nil NewID: a silent evt-<unixnano> fallback " +
			"violates the event envelope (event_id = UUIDv7) and 22P02s every " +
			"UUID-keyed consumer")
	}
	if !strings.Contains(err.Error(), "id factory") {
		t.Errorf("expected an id-factory-required error, got %v", err)
	}
}
