// chat_session_test.go — RED tests for the ADR-154 ChatSession value object.
//
// Run with: go test ./internal/domain/companion/... -run ChatSession
package companion

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// NewChatSession — constructor invariants
// -----------------------------------------------------------------------------

func TestNewChatSession_HappyPath(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	sess, err := NewChatSession(NewChatSessionInput{
		ID:              "01957d00-0000-7000-aaaa-000000000001",
		TenantID:        "01957d00-0000-7000-bbbb-000000000001",
		OwnerGCID:       "00000000-0000-7000-8000-000000001999",
		CompanionID:     "01957c8c-1111-7000-aaaa-1111aaaa1111",
		EngineSessionID: "vertex-session-abc",
		Now:             now,
	})
	if err != nil {
		t.Fatalf("NewChatSession: unexpected error %v", err)
	}
	if sess.ID != "01957d00-0000-7000-aaaa-000000000001" {
		t.Errorf("ID = %q; want UUIDv7", sess.ID)
	}
	if sess.EngineSessionID != "vertex-session-abc" {
		t.Errorf("EngineSessionID = %q", sess.EngineSessionID)
	}
	if sess.TurnCount != 0 {
		t.Errorf("TurnCount = %d; want 0 for fresh session", sess.TurnCount)
	}
	if sess.ManaChargedTotal != 0 {
		t.Errorf("ManaChargedTotal = %d; want 0 for fresh session", sess.ManaChargedTotal)
	}
	if !sess.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v; want now=%v", sess.CreatedAt, now)
	}
	if !sess.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt = %v; want now=%v", sess.LastUsedAt, now)
	}
	if sess.ClosedAt != nil {
		t.Errorf("ClosedAt = %v; want nil for open session", sess.ClosedAt)
	}
}

func TestNewChatSession_RejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	base := NewChatSessionInput{
		ID:              "01957d00-0000-7000-aaaa-000000000001",
		TenantID:        "01957d00-0000-7000-bbbb-000000000001",
		OwnerGCID:       "00000000-0000-7000-8000-000000001999",
		CompanionID:     "01957c8c-1111-7000-aaaa-1111aaaa1111",
		EngineSessionID: "vertex-session-abc",
		Now:             now,
	}

	cases := []struct {
		name   string
		mutate func(*NewChatSessionInput)
	}{
		{"missing ID", func(in *NewChatSessionInput) { in.ID = "" }},
		{"missing TenantID", func(in *NewChatSessionInput) { in.TenantID = "" }},
		{"missing OwnerGCID", func(in *NewChatSessionInput) { in.OwnerGCID = "" }},
		{"missing CompanionID", func(in *NewChatSessionInput) { in.CompanionID = "" }},
		{"missing EngineSessionID", func(in *NewChatSessionInput) { in.EngineSessionID = "" }},
		{"whitespace-only EngineSessionID", func(in *NewChatSessionInput) { in.EngineSessionID = "   " }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mutate(&in)
			_, err := NewChatSession(in)
			if !errors.Is(err, ErrChatSessionInvalid) {
				t.Errorf("err=%v; want ErrChatSessionInvalid", err)
			}
		})
	}
}

func TestNewChatSession_DefaultsNowWhenZero(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	sess, err := NewChatSession(NewChatSessionInput{
		ID:              "01957d00-0000-7000-aaaa-000000000001",
		TenantID:        "01957d00-0000-7000-bbbb-000000000001",
		OwnerGCID:       "00000000-0000-7000-8000-000000001999",
		CompanionID:     "01957c8c-1111-7000-aaaa-1111aaaa1111",
		EngineSessionID: "vertex-session-abc",
	})
	if err != nil {
		t.Fatalf("NewChatSession: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)
	if sess.CreatedAt.Before(before) || sess.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v; expected between %v and %v",
			sess.CreatedAt, before, after)
	}
}

// -----------------------------------------------------------------------------
// RecordTurn — immutable update (returns new value)
// -----------------------------------------------------------------------------

func TestChatSession_RecordTurn_IncrementsCountersAndStampsLastUsed(t *testing.T) {
	t0 := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	sess, _ := NewChatSession(NewChatSessionInput{
		ID:              "id1",
		TenantID:        "t",
		OwnerGCID:       "g",
		CompanionID:     "f",
		EngineSessionID: "es",
		Now:             t0,
	})

	t1 := t0.Add(15 * time.Second)
	next := sess.RecordTurn(15, t1)

	if next.TurnCount != 1 {
		t.Errorf("after one RecordTurn, TurnCount = %d; want 1", next.TurnCount)
	}
	if next.ManaChargedTotal != 15 {
		t.Errorf("ManaChargedTotal = %d; want 15", next.ManaChargedTotal)
	}
	if !next.LastUsedAt.Equal(t1) {
		t.Errorf("LastUsedAt = %v; want %v", next.LastUsedAt, t1)
	}
	// Source value MUST NOT be mutated — RecordTurn returns a new value.
	if sess.TurnCount != 0 {
		t.Errorf("source.TurnCount = %d; want immutable 0", sess.TurnCount)
	}
	if sess.ManaChargedTotal != 0 {
		t.Errorf("source.ManaChargedTotal = %d; want immutable 0", sess.ManaChargedTotal)
	}
	if !sess.LastUsedAt.Equal(t0) {
		t.Errorf("source.LastUsedAt = %v; want immutable %v", sess.LastUsedAt, t0)
	}

	// CreatedAt MUST NOT change after RecordTurn.
	if !next.CreatedAt.Equal(t0) {
		t.Errorf("next.CreatedAt = %v; want unchanged %v", next.CreatedAt, t0)
	}
	// ID + TenantID + OwnerGCID + CompanionID + EngineSessionID MUST NOT
	// change after RecordTurn.
	if next.ID != sess.ID || next.TenantID != sess.TenantID ||
		next.OwnerGCID != sess.OwnerGCID || next.CompanionID != sess.CompanionID ||
		next.EngineSessionID != sess.EngineSessionID {
		t.Errorf("identity fields mutated by RecordTurn — next=%+v sess=%+v", next, sess)
	}
}

func TestChatSession_RecordTurn_AccumulatesAcrossTurns(t *testing.T) {
	t0 := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	sess, _ := NewChatSession(NewChatSessionInput{
		ID: "i", TenantID: "t", OwnerGCID: "g", CompanionID: "f",
		EngineSessionID: "es", Now: t0,
	})
	turn1 := sess.RecordTurn(5, t0.Add(10*time.Second))
	turn2 := turn1.RecordTurn(15, t0.Add(20*time.Second))
	turn3 := turn2.RecordTurn(30, t0.Add(30*time.Second))

	if turn3.TurnCount != 3 {
		t.Errorf("turn3.TurnCount = %d; want 3", turn3.TurnCount)
	}
	if turn3.ManaChargedTotal != 50 {
		t.Errorf("turn3.ManaChargedTotal = %d; want 50", turn3.ManaChargedTotal)
	}
	if !turn3.LastUsedAt.Equal(t0.Add(30 * time.Second)) {
		t.Errorf("turn3.LastUsedAt = %v; want t0+30s", turn3.LastUsedAt)
	}
}

func TestChatSession_RecordTurn_NegativeManaIsClampedToZero(t *testing.T) {
	t0 := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	sess, _ := NewChatSession(NewChatSessionInput{
		ID: "i", TenantID: "t", OwnerGCID: "g", CompanionID: "f",
		EngineSessionID: "es", Now: t0,
	})
	// Defensive: negative mana would corrupt the running total. RecordTurn
	// clamps to zero.
	next := sess.RecordTurn(-5, t0.Add(time.Second))
	if next.ManaChargedTotal != 0 {
		t.Errorf("ManaChargedTotal = %d; want 0 after clamp", next.ManaChargedTotal)
	}
	if next.TurnCount != 1 {
		t.Errorf("TurnCount = %d; want 1 even on zero-mana turn", next.TurnCount)
	}
}

// -----------------------------------------------------------------------------
// IsOpen — invariant query
// -----------------------------------------------------------------------------

func TestChatSession_IsOpen(t *testing.T) {
	sess, _ := NewChatSession(NewChatSessionInput{
		ID: "i", TenantID: "t", OwnerGCID: "g", CompanionID: "f",
		EngineSessionID: "es",
	})
	if !sess.IsOpen() {
		t.Error("fresh session should be open")
	}
	t1 := time.Now().UTC()
	closed := sess.Close(t1)
	if closed.IsOpen() {
		t.Error("closed session should not be open")
	}
	if closed.ClosedAt == nil || !closed.ClosedAt.Equal(t1) {
		t.Errorf("ClosedAt = %v; want %v", closed.ClosedAt, t1)
	}
	// Source MUST NOT mutate.
	if !sess.IsOpen() {
		t.Error("source session mutated by Close()")
	}
}

// -----------------------------------------------------------------------------
// ErrChatSessionInvalid surfacing
// -----------------------------------------------------------------------------

func TestErrChatSessionInvalid_MessageCarriesField(t *testing.T) {
	_, err := NewChatSession(NewChatSessionInput{
		// All blank — the first detected blank is reported.
	})
	if err == nil {
		t.Fatal("expected error on all-blank input")
	}
	if !errors.Is(err, ErrChatSessionInvalid) {
		t.Fatalf("err=%v; want ErrChatSessionInvalid", err)
	}
	// The message should name the failing field so ops triage doesn't grep.
	if !strings.Contains(err.Error(), "id") &&
		!strings.Contains(err.Error(), "tenant") &&
		!strings.Contains(err.Error(), "gcid") {
		t.Errorf("err=%q; expected named field in message", err.Error())
	}
}
