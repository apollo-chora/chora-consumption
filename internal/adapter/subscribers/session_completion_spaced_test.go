// session_completion_spaced_test.go — RED-phase regression guard for ADR-233 D3.
//
// Advance() now REFUSES on a spaced-traversal path (the cursor is inert; SM-2
// schedules it). The atom_session.completed handler fans out over EVERY path of
// the learner that contains the atom — and a study-list atom is very often ALSO
// in a course path, or the learner may simply answer a study-list atom.
//
// If the handler treated ErrSpacedPathNoCursor as fatal it would return an error
// → NACK → Pub/Sub retry → DLQ on EVERY completion of any atom that sits in a
// spaced study list. That would turn a correct domain refusal into a live
// outage of the whole retention/XP/streak pipeline downstream of it.
//
// A spaced path is a correct NO-OP for the cursor lane: skip it and carry on.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

func TestSessionCompletion_SpacedPathIsSkippedNotFatal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)

	const (
		tenant   = "01970000-0000-7000-8000-000000000001"
		learner  = "01970000-0000-7000-9000-000000000001"
		collID   = "01970000-0000-7000-b000-000000000001"
		evID     = "01970000-0000-7000-c000-000000000001"
		atomID   = "01970000-0000-7000-a000-000000000001"
		atomID2  = "01970000-0000-7000-a000-000000000002"
		courseID = "01970000-0000-7000-e000-000000000001"
	)

	paths := inmem.NewLearningPathRepo()

	// A spaced study list containing the atom.
	spaced, err := learning_path.NewFromCollection(learning_path.StudyListParams{
		TenantID: tenant, OwnerGCID: learner, CollectionID: collID,
		AtomIDs: []string{atomID, atomID2}, StudyListEventID: evID, Now: now,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}
	if err := paths.Save(ctx, spaced); err != nil {
		t.Fatalf("save spaced: %v", err)
	}

	// A LINEAR course path containing the SAME atom — it must still advance.
	linear, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: tenant, LearnerGCID: learner, CourseID: courseID,
		AtomIDs: []string{atomID, atomID2}, Now: now,
	})
	if err != nil {
		t.Fatalf("BootstrapFromEnrollment: %v", err)
	}
	if err := paths.Save(ctx, linear); err != nil {
		t.Fatalf("save linear: %v", err)
	}

	h := NewSessionCompletionHandler(paths, inmem.NewAtomIndexRepo(),
		inmem.NewTopicRetentionRepo(), events.NewInMemoryPublisher())

	res, err := h.Handle(ctx, SessionCompletedInput{
		TenantID:    tenant,
		LearnerGCID: learner,
		AtomID:      atomID,
		IsCorrect:   true,
		OccurredAt:  now,
		// The linear path DOES advance and publishes learning_path.advanced.v1,
		// whose envelope mandates a W3C traceparent.
		Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if err != nil {
		t.Fatalf("Handle returned error %v — a spaced path must be SKIPPED, not fatal. "+
			"ErrSpacedPathNoCursor reaching the Pub/Sub layer NACKs every completion of "+
			"any atom that also sits in a study list → DLQ (ADR-233 D3).", err)
	}

	// The linear path advanced; the spaced path's cursor stayed inert.
	if res.PathsAdvanced != 1 {
		t.Errorf("PathsAdvanced = %d; want 1 (the linear course path only)", res.PathsAdvanced)
	}
	reloadedSpaced, err := paths.GetBySourceCollection(ctx, tenant, learner, collID)
	if err != nil {
		t.Fatalf("reload spaced: %v", err)
	}
	if reloadedSpaced.CurrentIndex != 0 {
		t.Errorf("spaced CurrentIndex = %d; want 0 (cursor is INERT for spaced)", reloadedSpaced.CurrentIndex)
	}
	if reloadedSpaced.CompletedAt != nil {
		t.Error("spaced path got a completed_at stamp; completion is not cursor-derived for spaced paths")
	}
}
