package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestPathRepo_SaveGet(t *testing.T) {
	r := NewPathRepo()
	p, _ := learning_path.New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Test", []string{"01970000-0000-7000-a000-000000000001"})
	r.Save(p)
	got, err := r.Get(p.PathID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.PathID != p.PathID {
		t.Errorf("got %v, want %v", got.PathID, p.PathID)
	}
}

func TestPathRepo_GetNotFound(t *testing.T) {
	r := NewPathRepo()
	if _, err := r.Get("nope"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPathRepo_SoftDeletedHidden(t *testing.T) {
	r := NewPathRepo()
	p, _ := learning_path.New("01970000-0000-7000-8000-000000000001", "01970000-0000-7000-9000-000000000001", "Test", []string{"01970000-0000-7000-a000-000000000001"})
	r.Save(p)
	p.SoftDelete()
	r.Save(p)
	if _, err := r.Get(p.PathID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for soft-deleted, got %v", err)
	}
}

func TestPathRepo_ListByTenant(t *testing.T) {
	r := NewPathRepo()
	tenantA := "01970000-0000-7000-8000-000000000001"
	tenantB := "01970000-0000-7000-8000-000000000002"
	pA, _ := learning_path.New(tenantA, "01970000-0000-7000-9000-000000000001", "A", []string{"01970000-0000-7000-a000-000000000001"})
	pB, _ := learning_path.New(tenantB, "01970000-0000-7000-9000-000000000002", "B", []string{"01970000-0000-7000-a000-000000000002"})
	r.Save(pA)
	r.Save(pB)
	listA := r.List(tenantA, 50)
	if len(listA) != 1 || listA[0].TenantID != tenantA {
		t.Errorf("expected 1 path for tenantA, got %d", len(listA))
	}
}

func TestSessionRepo_SaveGet(t *testing.T) {
	r := NewSessionRepo()
	s, _ := atom_attempt.Start(
		"01970000-0000-7000-8000-000000000001",
		"01970000-0000-7000-9000-000000000001",
		"01970000-0000-7000-a000-000000000001",
		fixedClock{t: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)},
	)
	if err := r.Save(context.Background(), s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := r.Get(context.Background(), s.SessionID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.SessionID != s.SessionID {
		t.Errorf("mismatch")
	}
}

func TestSessionRepo_NotFound(t *testing.T) {
	r := NewSessionRepo()
	if _, err := r.Get(context.Background(), "nope"); err != atom_attempt.ErrNotFound {
		t.Errorf("expected atom_attempt.ErrNotFound, got %v", err)
	}
}

func TestCompanionRepo_SaveGet(t *testing.T) {
	r := NewCompanionRepo()
	tenantID := "01970000-0000-7000-8000-000000000001"
	gcid := "01970000-0000-7000-9000-000000000001"
	f, _ := companion.New(tenantID, gcid, "Sparky")
	r.Save(f)
	got, err := r.GetByGCID(tenantID, gcid)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.CompanionID != f.CompanionID {
		t.Errorf("mismatch")
	}
}

func TestCompanionRepo_NotFound(t *testing.T) {
	r := NewCompanionRepo()
	if _, err := r.GetByGCID("nope", "nope"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ExamPrepGoalRepo coverage — supports BE-EP1 dose enrichment.
//
// MVP semantics: per (tenant_id, gcid) at most one active goal. M12 swaps
// this for a Pub/Sub-projected goal cache populated from chora_delivery's
// ExamRegistration aggregate (cross-DB-forbidden rule applies).

func TestExamPrepGoalRepo_SaveAndGet(t *testing.T) {
	r := NewExamPrepGoalRepo()
	tenantID := "01970000-0000-7000-8000-0000000000aa"
	gcid := "018f-marcus"
	goal := &companion.ExamPrepGoal{
		LearnerGCID: gcid,
		ExamID:      "scrum-master-cert",
	}
	r.Save(tenantID, gcid, goal)
	got, ok := r.Get(tenantID, gcid)
	if !ok {
		t.Fatal("expected goal to be present")
	}
	if got.ExamID != "scrum-master-cert" {
		t.Errorf("ExamID = %q, want scrum-master-cert", got.ExamID)
	}
}

func TestExamPrepGoalRepo_NotFoundReturnsFalse(t *testing.T) {
	r := NewExamPrepGoalRepo()
	if _, ok := r.Get("nope", "nope"); ok {
		t.Error("expected ok=false for missing goal")
	}
}

func TestExamPrepGoalRepo_NilSaveIgnored(t *testing.T) {
	r := NewExamPrepGoalRepo()
	r.Save("t", "g", nil)
	if _, ok := r.Get("t", "g"); ok {
		t.Error("nil save MUST NOT populate the repo")
	}
}

func TestExamPrepGoalRepo_PerTenantIsolation(t *testing.T) {
	r := NewExamPrepGoalRepo()
	r.Save("tA", "shared-gcid", &companion.ExamPrepGoal{
		LearnerGCID: "shared-gcid", ExamID: "exam-a",
	})
	r.Save("tB", "shared-gcid", &companion.ExamPrepGoal{
		LearnerGCID: "shared-gcid", ExamID: "exam-b",
	})
	gotA, _ := r.Get("tA", "shared-gcid")
	gotB, _ := r.Get("tB", "shared-gcid")
	if gotA.ExamID != "exam-a" || gotB.ExamID != "exam-b" {
		t.Errorf("tenant isolation broken: A=%q B=%q", gotA.ExamID, gotB.ExamID)
	}
}

func TestAtomCatalogue_Seeds20Atoms5Topics(t *testing.T) {
	c := NewAtomCatalogue()
	seeds := c.Seeds()
	if len(seeds) != 20 {
		t.Errorf("len seeds = %d; want 20", len(seeds))
	}
	if _, ok := c.Lookup(seeds[0].AtomID); !ok {
		t.Errorf("Lookup of seeded atom_id failed")
	}
	if _, ok := c.Lookup("nonexistent"); ok {
		t.Errorf("Lookup of unknown returned ok")
	}
}

func TestSM2Repo_RoundTrip(t *testing.T) {
	r := NewSM2Repo()
	state := companion.SM2State{AtomID: "atom-1"}
	if err := r.SaveState(context.Background(), "t1", "g1", state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, ok, err := r.GetState(context.Background(), "t1", "g1", "atom-1")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !ok {
		t.Errorf("GetState ok=false")
	}
	if got.AtomID != "atom-1" {
		t.Errorf("AtomID = %q", got.AtomID)
	}
	all, err := r.AllStatesForLearner(context.Background(), "t1", "g1")
	if err != nil {
		t.Fatalf("AllStatesForLearner: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("AllStates len = %d", len(all))
	}
	if err := r.MarkTopicSeen(context.Background(), "t1", "g1", "agile"); err != nil {
		t.Fatalf("MarkTopicSeen: %v", err)
	}
	seen, err := r.SeenTopics(context.Background(), "t1", "g1")
	if err != nil {
		t.Fatalf("SeenTopics: %v", err)
	}
	if !seen["agile"] {
		t.Errorf("agile not in seen topics")
	}
}
