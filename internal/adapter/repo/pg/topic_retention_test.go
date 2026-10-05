// topic_retention_test.go — RLS contract verification.
package pg

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

func TestPGTopicRetentionRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTopicRetentionRepo(tx)

	s, err := topic_retention.New(pgTenantID, pgUserGCID, "agile", time.Now().UTC(), 1.0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.Save(withCtx(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO topic_retention") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGTopicRetentionRepo_SaveRejectsNil(t *testing.T) {
	repo := NewTopicRetentionRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidTopicScore) {
		t.Errorf("err = %v; want ErrInvalidTopicScore", err)
	}
}

func TestPGTopicRetentionRepo_GetAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewTopicRetentionRepo(tx)
	_, _ = repo.Get(withCtx(), pgTenantID, pgUserGCID, "agile")
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGTopicRetentionRepo_ListByLearnerAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewTopicRetentionRepo(tx)
	_, _ = repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID, 0)
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}
