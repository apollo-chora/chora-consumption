package companion

import (
	"strings"
	"testing"
)

// CHO-2225 — the event_id envelope contract, companion side.
//
// Both constructors below used to silently default NewID to
// fmt.Sprintf("evt-%d", UnixNano()) — the same anti-pattern that shipped a
// non-UUID event_id out of growth.NewService and 22P02'd chora-sharing's
// milestone lane on 100% of messages. Each constructor already fails loud on
// its other required ports; NewID was the one dependency that silently
// substituted a value violating the platform envelope (event_id = UUIDv7).
//
// There is no safe default: the composition root must inject a real UUIDv7
// mint (domain.NewUUIDv7). These are the guards that force it.

func TestNewPathUnlocker_RequiresNewID(t *testing.T) {
	_, err := NewPathUnlocker(PathUnlockerConfig{
		Catalog: &fakeCatalogReader{},
		Paths:   &fakePathReader{},
		Grants:  &fakeGrantWriter{granted: map[string]bool{}},
		Outbox:  &fakeLoadoutOutbox{},
	})
	if err == nil {
		t.Fatal("NewPathUnlocker accepted a nil NewID: an evt-<unixnano> fallback " +
			"violates the event envelope (event_id = UUIDv7) on " +
			"companion.skill_granted.v1 / companion.loadout_changed.v1")
	}
	if !strings.Contains(err.Error(), "id factory") {
		t.Errorf("expected an id-factory-required error, got %v", err)
	}
}

func TestNewRitualPublisher_RequiresNewID(t *testing.T) {
	_, err := NewRitualPublisher(RitualPublisherConfig{
		Repo:   &fakeRitualRepo{},
		Caps:   &fakeCapsResolver{caps: ritualTestCaps()},
		Outbox: &fakeRitualOutbox{},
		// Sinks supplied so this stays a test about the ID factory: without it the
		// dependency check trips first and the assertion below reads a different error.
		Sinks: allSinksWired(),
	})
	if err == nil {
		t.Fatal("NewRitualPublisher accepted a nil NewID: an evt-<unixnano> " +
			"fallback violates the event envelope (event_id = UUIDv7)")
	}
	if !strings.Contains(err.Error(), "id factory") {
		t.Errorf("expected an id-factory-required error, got %v", err)
	}
}
