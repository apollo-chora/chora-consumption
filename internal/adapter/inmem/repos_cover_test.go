// repos_cover_test.go — branch-coverage characterization for the in-memory
// adapters in repos.go + companion_instance_repo.go + streak_repo.go.
//
// Each test targets a specific uncovered branch the existing suites skip:
//   - PathRepo.List   → the limit-truncation early break
//   - hex1            → the out-of-range [0,15] guard returning "0"
//   - CompanionInstanceRepo.Update     → insert-on-unknown-ID (new owner index)
//   - CompanionInstanceRepo.SoftDelete → unknown-ID silent-nil branch
//   - XPRepo.Award           → non-positive-delta no-op guard
//   - TopicAccuracyRepo.GetByLearner → zero-total topic skip
//   - ActivePathTopicsRepo.RecordPathTopics → empty-string topic skip
//
// All assertions characterize CURRENT behavior and would catch a regression.
package inmem

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ---------- PathRepo.List — limit truncation ----------

// TestPathRepo_List_HonoursLimit verifies the `limit > 0 && len(out) >= limit`
// early-break: with 3 saved paths and limit=2 the result is capped at 2.
func TestPathRepo_List_HonoursLimit(t *testing.T) {
	r := NewPathRepo()
	tenant := "01970000-0000-7000-8000-000000000001"
	for i, atom := range []string{
		"01970000-0000-7000-a000-000000000001",
		"01970000-0000-7000-a000-000000000002",
		"01970000-0000-7000-a000-000000000003",
	} {
		p, err := learning_path.New(tenant, "01970000-0000-7000-9000-00000000000"+string(rune('1'+i)), "P", []string{atom})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		r.Save(p)
	}
	got := r.List(tenant, 2)
	if len(got) != 2 {
		t.Fatalf("List(limit=2) = %d paths, want exactly 2 (truncated)", len(got))
	}
}

// TestPathRepo_List_ZeroLimitReturnsAll verifies limit<=0 disables truncation
// (the `limit > 0` guard is false → no break).
func TestPathRepo_List_ZeroLimitReturnsAll(t *testing.T) {
	r := NewPathRepo()
	tenant := "01970000-0000-7000-8000-000000000001"
	for i, atom := range []string{
		"01970000-0000-7000-a000-000000000001",
		"01970000-0000-7000-a000-000000000002",
		"01970000-0000-7000-a000-000000000003",
	} {
		p, err := learning_path.New(tenant, "01970000-0000-7000-9000-00000000000"+string(rune('1'+i)), "P", []string{atom})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		r.Save(p)
	}
	if got := r.List(tenant, 0); len(got) != 3 {
		t.Fatalf("List(limit=0) = %d paths, want all 3", len(got))
	}
}

// TestPathRepo_List_FiltersOtherTenant verifies the cross-tenant filter
// (`tenantID != "" && p.TenantID != tenantID` → continue): a path owned by a
// different tenant is excluded from the result even with a generous limit.
func TestPathRepo_List_FiltersOtherTenant(t *testing.T) {
	r := NewPathRepo()
	tenantA := "01970000-0000-7000-8000-00000000000a"
	tenantB := "01970000-0000-7000-8000-00000000000b"
	pA, err := learning_path.New(tenantA, "01970000-0000-7000-9000-000000000001", "A", []string{"01970000-0000-7000-a000-000000000001"})
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	pB, err := learning_path.New(tenantB, "01970000-0000-7000-9000-000000000002", "B", []string{"01970000-0000-7000-a000-000000000002"})
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	r.Save(pA)
	r.Save(pB)

	got := r.List(tenantA, 50)
	if len(got) != 1 {
		t.Fatalf("List(tenantA) = %d, want 1 (tenantB filtered out)", len(got))
	}
	if got[0].TenantID != tenantA {
		t.Fatalf("List(tenantA) leaked tenant %s", got[0].TenantID)
	}
}

// TestPathRepo_List_EmptyTenantReturnsAll verifies that an empty tenantID
// disables the tenant filter (the `tenantID != ""` guard is false), returning
// paths across all tenants.
func TestPathRepo_List_EmptyTenantReturnsAll(t *testing.T) {
	r := NewPathRepo()
	pA, err := learning_path.New("01970000-0000-7000-8000-00000000000a", "01970000-0000-7000-9000-000000000001", "A", []string{"01970000-0000-7000-a000-000000000001"})
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	pB, err := learning_path.New("01970000-0000-7000-8000-00000000000b", "01970000-0000-7000-9000-000000000002", "B", []string{"01970000-0000-7000-a000-000000000002"})
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	r.Save(pA)
	r.Save(pB)
	if got := r.List("", 50); len(got) != 2 {
		t.Fatalf("List(\"\") = %d, want 2 (no tenant filter)", len(got))
	}
}

// TestPathRepo_List_ExcludesSoftDeleted verifies the `p.DeletedAt != nil`
// continue branch in List: a soft-deleted path is omitted from the listing
// (ddd-enforcement #6 default-read filter) while a live path is returned.
func TestPathRepo_List_ExcludesSoftDeleted(t *testing.T) {
	r := NewPathRepo()
	tenant := "01970000-0000-7000-8000-00000000000a"
	live, err := learning_path.New(tenant, "01970000-0000-7000-9000-000000000001", "live", []string{"01970000-0000-7000-a000-000000000001"})
	if err != nil {
		t.Fatalf("New live: %v", err)
	}
	gone, err := learning_path.New(tenant, "01970000-0000-7000-9000-000000000002", "gone", []string{"01970000-0000-7000-a000-000000000002"})
	if err != nil {
		t.Fatalf("New gone: %v", err)
	}
	r.Save(live)
	gone.SoftDelete()
	r.Save(gone)

	got := r.List(tenant, 50)
	if len(got) != 1 {
		t.Fatalf("List = %d, want 1 (soft-deleted excluded)", len(got))
	}
	if got[0].PathID != live.PathID {
		t.Fatalf("List returned the soft-deleted path")
	}
}

// ---------- hex1 — out-of-range guard ----------

// TestHex1 characterizes the single-nibble hex encoder, including the
// out-of-[0,15] guard that returns "0" (used to keep AtomID UUIDs valid).
func TestHex1(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{9, "9"},
		{10, "a"},
		{15, "f"},
		{-1, "0"},  // below range → guard
		{16, "0"},  // above range → guard
		{255, "0"}, // far above range → guard
	}
	for _, c := range cases {
		if got := hex1(c.in); got != c.want {
			t.Errorf("hex1(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------- CompanionInstanceRepo.Update — insert on unknown ID ----------

// TestCompanionInstanceRepo_Update_InsertsUnknownID verifies the forgiving MVP
// upsert: Update on a never-Created Instance inserts it AND wires the
// byOwner index so it is then visible via ListByOwner.
func TestCompanionInstanceRepo_Update_InsertsUnknownID(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	inst := mustNewInstance(t, "g1", "Newton", "math")

	// No prior Create — Update must take the `if !ok` insert branch.
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("Update (insert): %v", err)
	}

	got, err := r.Get(ctx, inst.CompanionID)
	if err != nil {
		t.Fatalf("Get after Update-insert: %v", err)
	}
	if got.CompanionID != inst.CompanionID {
		t.Fatalf("Get returned wrong instance")
	}

	// The owner index must have been created so ListByOwner finds it.
	list, err := r.ListByOwner(ctx, "t1", "g1")
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(list) != 1 || list[0].CompanionID != inst.CompanionID {
		t.Fatalf("Update-insert did not wire byOwner index; list=%v", list)
	}
}

// TestCompanionInstanceRepo_Update_ExistingDoesNotDuplicateOwnerIndex verifies
// that a second Update on a known ID takes the `ok` path (no re-index) and the
// owner still lists exactly one entry.
func TestCompanionInstanceRepo_Update_ExistingDoesNotDuplicateOwnerIndex(t *testing.T) {
	r := NewCompanionInstanceRepo()
	ctx := context.Background()
	inst := mustNewInstance(t, "g1", "Newton", "math")
	if err := r.Create(ctx, inst, 5); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("Update existing: %v", err)
	}
	list, _ := r.ListByOwner(ctx, "t1", "g1")
	if len(list) != 1 {
		t.Fatalf("re-Update produced %d list entries, want 1", len(list))
	}
}

// ---------- CompanionInstanceRepo.SoftDelete — unknown ID ----------

// TestCompanionInstanceRepo_SoftDelete_UnknownID verifies the silent-nil
// convention: deleting an ID that was never created is success (not an error).
func TestCompanionInstanceRepo_SoftDelete_UnknownID(t *testing.T) {
	r := NewCompanionInstanceRepo()
	if err := r.SoftDelete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("SoftDelete(unknown) = %v, want nil (delete-unknown-is-success)", err)
	}
}

// ---------- XPRepo.Award — non-positive delta no-op ----------

// TestXPRepo_Award_NonPositiveDeltaNoOp verifies that zero and negative awards
// do not mutate the learner's total (defensive guard at the top of Award).
func TestXPRepo_Award_NonPositiveDeltaNoOp(t *testing.T) {
	r := NewXPRepo()
	r.Award(context.Background(), "tenant-a", "learner-1", 50)
	r.Award(context.Background(), "tenant-a", "learner-1", 0)   // no-op
	r.Award(context.Background(), "tenant-a", "learner-1", -25) // no-op (does NOT subtract)
	if got := mustXP(t, r, "tenant-a", "learner-1"); got != 50 {
		t.Fatalf("Get = %d, want 50 (zero/negative awards must be ignored)", got)
	}
}

// TestXPRepo_Award_NonPositiveOnFreshLearner verifies a no-op award on a
// never-seen learner leaves them at 0 and does not create a phantom entry.
func TestXPRepo_Award_NonPositiveOnFreshLearner(t *testing.T) {
	r := NewXPRepo()
	r.Award(context.Background(), "tenant-a", "fresh", -1)
	if got := mustXP(t, r, "tenant-a", "fresh"); got != 0 {
		t.Fatalf("Get = %d, want 0 after negative no-op award", got)
	}
}

// ---------- TopicAccuracyRepo.GetByLearner — zero-total skip ----------

// TestTopicAccuracyRepo_GetByLearner_SkipsZeroTotalTopics constructs a learner
// whose stored topic map contains a zero-total counter (no attempts recorded)
// and asserts GetByLearner omits it (the `att.total == 0` continue branch),
// avoiding a 0/0 NaN division. A topic with attempts is retained.
func TestTopicAccuracyRepo_GetByLearner_SkipsZeroTotalTopics(t *testing.T) {
	r := NewTopicAccuracyRepo()
	key := learnerKey("tenant-a", "learner-1")

	// Directly seed a zero-total entry alongside a real one. (White-box: same
	// package, so we can reach the unexported store + counter type.)
	r.store[key] = map[string]*topicAttempt{
		"empty-topic": {correct: 0, total: 0}, // pathological zero-total row
		"scrum":       {correct: 3, total: 4}, // real attempts
	}

	got := mustGetByLearner(t, r, "tenant-a", "learner-1")
	if _, present := got["empty-topic"]; present {
		t.Fatal("zero-total topic must be skipped (would be 0/0 NaN)")
	}
	if acc, ok := got["scrum"]; !ok || acc < 0.749 || acc > 0.751 {
		t.Fatalf("scrum accuracy = %v (present=%v), want 0.75", acc, ok)
	}
	if len(got) != 1 {
		t.Fatalf("result size = %d, want 1 (zero-total skipped)", len(got))
	}
}

// ---------- ActivePathTopicsRepo.RecordPathTopics — empty-string skip ----------

// TestActivePathTopicsRepo_RecordPathTopics_SkipsEmptyTopic verifies that an
// empty-string topic in the slice is ignored (the `t == ""` continue), while
// non-empty topics in the same call are recorded.
func TestActivePathTopicsRepo_RecordPathTopics_SkipsEmptyTopic(t *testing.T) {
	r := NewActivePathTopicsRepo()
	mustRecordPathTopics(t, r, "tenant-a", "learner-1", []string{"agile", "", "scrum", ""})
	got := mustGetTopics(t, r, "tenant-a", "learner-1")
	if got[""] {
		t.Fatal("empty-string topic must not be recorded")
	}
	if !got["agile"] || !got["scrum"] {
		t.Fatalf("non-empty topics missing; got %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("topic count = %d, want 2 (agile, scrum; empties skipped)", len(got))
	}
}

// TestActivePathTopicsRepo_RecordPathTopics_AllEmpty verifies a slice of only
// empty strings records nothing (every iteration hits the continue) yet still
// initializes the learner's bucket without leaking a "" key.
func TestActivePathTopicsRepo_RecordPathTopics_AllEmpty(t *testing.T) {
	r := NewActivePathTopicsRepo()
	mustRecordPathTopics(t, r, "tenant-a", "learner-1", []string{"", ""})
	if got := mustGetTopics(t, r, "tenant-a", "learner-1"); len(got) != 0 {
		t.Fatalf("topic count = %d, want 0 (all empties skipped)", len(got))
	}
}
