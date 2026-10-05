// atom_attempt_test.go - RLS contract verification for the
// AtomAttempt pgx adapter.
package pg

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

func TestPGAtomSessionRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewAtomSessionRepo(tx)

	s, err := atom_attempt.Start(pgTenantID, pgUserGCID, "01970000-0000-7000-a000-000000000001", atom_attempt.SystemClock)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := repo.Save(withCtx(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO atomic_sessions") {
		t.Errorf("expected upsert SQL; got %q", last)
	}
}

func TestPGAtomSessionRepo_SaveRejectsNil(t *testing.T) {
	repo := NewAtomSessionRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidAtomSession) {
		t.Errorf("err = %v; want ErrInvalidAtomSession", err)
	}
}

func TestPGAtomSessionRepo_GetAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewAtomSessionRepo(tx)
	_, _ = repo.Get(withCtx(), "01970000-0000-7000-d000-000000000001")
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}
