// atom_attempt_test - repo round-trips for the explicit state-machine
// AtomAttempt. Soft-delete exclusion enforced.
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

// TestAtomSessionRepo_SaveAndGet round-trips a session.
func TestAtomSessionRepo_SaveAndGet(t *testing.T) {
	r := NewAtomSessionRepo()
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := atom_attempt.Start(tenantID, gcid, atom1, clk)
	r.Save(context.Background(), s)

	got, err := r.Get(context.Background(), s.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AtomID != atom1 {
		t.Errorf("AtomID = %q", got.AtomID)
	}
	if got.Status != atom_attempt.StatusStarted {
		t.Errorf("Status = %s", got.Status)
	}
}

// TestAtomSessionRepo_Get_NotFound returns error for missing.
func TestAtomSessionRepo_Get_NotFound(t *testing.T) {
	r := NewAtomSessionRepo()
	if _, err := r.Get(context.Background(), "nonexistent"); err == nil {
		t.Error("expected error")
	}
}
