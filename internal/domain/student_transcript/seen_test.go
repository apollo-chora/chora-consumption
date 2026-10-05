package student_transcript

import (
	"testing"
	"time"
)

func TestTranscriptEntry_UnseenWhenSeenAtIsNil(t *testing.T) {
	e := &TranscriptEntry{EntryID: "e-1"}
	if !e.Unseen() {
		t.Fatal("an entry the learner has never opened must report Unseen")
	}
}

func TestTranscriptEntry_SeenOnceStamped(t *testing.T) {
	at := time.Now().UTC()
	e := &TranscriptEntry{EntryID: "e-1", SeenAt: &at}
	if e.Unseen() {
		t.Fatal("a stamped entry must not report Unseen")
	}
}

func TestTranscriptEntry_UnseenOnNilReceiverIsFalseNotAPanic(t *testing.T) {
	// A nil entry is not an unseen result. Reporting it as one would make a
	// missing row inflate the learner's unseen count.
	var e *TranscriptEntry
	if e.Unseen() {
		t.Fatal("a nil entry must not count as unseen")
	}
}

func TestMarkSeen_StampsOnlyTheFirstTime(t *testing.T) {
	// Opening a result twice must not move the timestamp. The stamp is when
	// the learner FIRST saw it, and a card that re-sorts because someone
	// re-read an old result would be wrong.
	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	later := first.Add(48 * time.Hour)

	e := &TranscriptEntry{EntryID: "e-1"}
	if !e.MarkSeen(first) {
		t.Fatal("the first MarkSeen must report that it changed the entry")
	}
	if e.SeenAt == nil || !e.SeenAt.Equal(first) {
		t.Fatalf("SeenAt = %v, want %v", e.SeenAt, first)
	}

	if e.MarkSeen(later) {
		t.Error("a second MarkSeen must report no change")
	}
	if !e.SeenAt.Equal(first) {
		t.Errorf("SeenAt moved to %v; the first-seen stamp must not be overwritten", e.SeenAt)
	}
}

func TestMarkSeen_NormalisesToUTC(t *testing.T) {
	// Every other timestamp on this aggregate is UTC. A local-zone stamp here
	// would compare wrongly against occurred_at in the same query.
	loc := time.FixedZone("UTC+8", 8*3600)
	local := time.Date(2026, 9, 1, 18, 0, 0, 0, loc)

	e := &TranscriptEntry{EntryID: "e-1"}
	e.MarkSeen(local)
	if e.SeenAt.Location() != time.UTC {
		t.Errorf("SeenAt location = %v, want UTC", e.SeenAt.Location())
	}
	if !e.SeenAt.Equal(local) {
		t.Errorf("normalising to UTC must preserve the instant: %v vs %v", e.SeenAt, local)
	}
}

func TestMarkSeen_ZeroTimeIsRefused(t *testing.T) {
	// A zero time is a caller bug, not a valid first-seen moment. Accepting it
	// would mark the result seen in year 1 and make it sort before everything.
	e := &TranscriptEntry{EntryID: "e-1"}
	if e.MarkSeen(time.Time{}) {
		t.Error("a zero timestamp must not stamp the entry")
	}
	if e.SeenAt != nil {
		t.Errorf("SeenAt = %v, want nil after a refused stamp", e.SeenAt)
	}
}

func TestMarkSeen_NilReceiverIsSafe(t *testing.T) {
	var e *TranscriptEntry
	if e.MarkSeen(time.Now()) {
		t.Error("MarkSeen on a nil entry must report no change rather than panic")
	}
}

func TestCountUnseen_CountsOnlyTheUnopenedOnes(t *testing.T) {
	at := time.Now().UTC()
	entries := []*TranscriptEntry{
		{EntryID: "a"},
		{EntryID: "b", SeenAt: &at},
		{EntryID: "c"},
		nil,
	}
	if got := CountUnseen(entries); got != 2 {
		t.Errorf("CountUnseen = %d, want 2 (nil entries must not be counted)", got)
	}
}

func TestCountUnseen_EmptyIsZero(t *testing.T) {
	if got := CountUnseen(nil); got != 0 {
		t.Errorf("CountUnseen(nil) = %d, want 0", got)
	}
}
