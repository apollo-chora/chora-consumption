// ceremony_edge_scout_runs_test.go — CHO-2040 (owner ruling R8-1): the in-memory
// first-run ledger behind edgescout.RunStore. HasRun flips true after exactly
// one RecordRun; the double-record is idempotent (mirrors the pg ON CONFLICT DO
// NOTHING); scoping is the full (tenant, goal, learner) triple.
package inmem

import (
	"context"
	"testing"
	"time"
)

const (
	cesTenant = "01970000-0000-7000-8000-0000000000t1"
	cesGoal   = "01970000-aaaa-7000-8000-00000000e5c0"
	cesGCID   = "01970000-0000-7000-8000-0000000000u1"
)

func TestCeremonyEdgeScoutRunRepo_FirstRunThenRecorded(t *testing.T) {
	repo := NewCeremonyEdgeScoutRunRepo()
	ctx := context.Background()

	has, err := repo.HasRun(ctx, cesTenant, cesGoal, cesGCID)
	if err != nil {
		t.Fatalf("HasRun: %v", err)
	}
	if has {
		t.Fatalf("HasRun = true on a virgin store, want false")
	}

	if err := repo.RecordRun(ctx, cesTenant, cesGoal, cesGCID, time.Now().UTC()); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	has, err = repo.HasRun(ctx, cesTenant, cesGoal, cesGCID)
	if err != nil {
		t.Fatalf("HasRun after record: %v", err)
	}
	if !has {
		t.Fatalf("HasRun = false after RecordRun, want true")
	}
}

func TestCeremonyEdgeScoutRunRepo_DoubleRecordIsIdempotent(t *testing.T) {
	repo := NewCeremonyEdgeScoutRunRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.RecordRun(ctx, cesTenant, cesGoal, cesGCID, now); err != nil {
		t.Fatalf("first RecordRun: %v", err)
	}
	// The concurrent double-tap lands twice; the second write must be a
	// no-op success (the pg impl absorbs it via ON CONFLICT DO NOTHING).
	if err := repo.RecordRun(ctx, cesTenant, cesGoal, cesGCID, now.Add(time.Minute)); err != nil {
		t.Fatalf("second RecordRun: %v (want idempotent no-op)", err)
	}
	has, err := repo.HasRun(ctx, cesTenant, cesGoal, cesGCID)
	if err != nil || !has {
		t.Fatalf("HasRun = (%v, %v), want (true, nil)", has, err)
	}
}

func TestCeremonyEdgeScoutRunRepo_ScopedPerTenantGoalLearner(t *testing.T) {
	repo := NewCeremonyEdgeScoutRunRepo()
	ctx := context.Background()
	if err := repo.RecordRun(ctx, cesTenant, cesGoal, cesGCID, time.Now().UTC()); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	for name, triple := range map[string][3]string{
		"other tenant":  {"01970000-0000-7000-8000-0000000000t2", cesGoal, cesGCID},
		"other goal":    {cesTenant, "01970000-aaaa-7000-8000-00000000e5c1", cesGCID},
		"other learner": {cesTenant, cesGoal, "01970000-0000-7000-8000-0000000000u2"},
	} {
		has, err := repo.HasRun(ctx, triple[0], triple[1], triple[2])
		if err != nil {
			t.Fatalf("%s: HasRun: %v", name, err)
		}
		if has {
			t.Errorf("%s: HasRun = true, want false (freebie must not leak across the triple)", name)
		}
	}
}
