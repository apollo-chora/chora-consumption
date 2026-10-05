// goal_test.go — RED tests for the learner-owned Goal aggregate (ADR-204 §2,
// amends ADR-203). Pure-domain: constructor validation + the 1:1 Companion
// attachment invariant + status/soft-delete guards. No infra, no clock leak
// (every mutation takes an explicit `now`).
package goal

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	gTenant    = "01970000-0000-7000-8000-0000000000a1"
	gLearner   = "01970000-0000-7000-9000-0000000000a1"
	gCompanion = "01970000-0000-7000-b000-0000000000a1"
	gTarget    = "theme:algebra-2026"
	gConcept   = "01970000-0000-7000-c000-0000000000a1"
)

var gNow = time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

func strptr(s string) *string { return &s }

// --- constructor: kind validation + chora_target_ref rules ---

func TestNewGoal_CuriosityTargetNilOK(t *testing.T) {
	g, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindCuriosity, Now: gNow,
	})
	if err != nil {
		t.Fatalf("curiosity with nil target must be valid: %v", err)
	}
	if g.Status != StatusActive {
		t.Errorf("status = %q; want active (default)", g.Status)
	}
	if g.ChoraTargetRef != nil {
		t.Errorf("curiosity target = %v; want nil", *g.ChoraTargetRef)
	}
	if g.GoalID == "" {
		t.Error("GoalID must be minted (UUIDv7)")
	}
	if !g.CreatedAt.Equal(gNow) || !g.UpdatedAt.Equal(gNow) {
		t.Errorf("timestamps not set to now: created=%v updated=%v", g.CreatedAt, g.UpdatedAt)
	}
	if g.DeletedAt != nil {
		t.Error("fresh goal must not be soft-deleted")
	}
}

// ADR-214 §3: the self-completing credential kinds (cert/course/path) are
// REMOVED — creating one fails-loud (closes the credential-forgery hole).
func TestNewGoal_CredentialKindsRemoved(t *testing.T) {
	for _, k := range []Kind{Kind("cert"), Kind("course"), Kind("path")} {
		if k.Valid() {
			t.Errorf("kind %q must no longer be valid (ADR-214 §3)", k)
		}
		if _, err := NewGoal(NewGoalInput{
			TenantID: gTenant, LearnerGCID: gLearner, Kind: k,
			ChoraTargetRef: strptr("PMP"), Now: gNow,
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("removed credential kind %q must be rejected; err = %v", k, err)
		}
	}
}

// theme_mastery + edge also reference a verifiable Chora object (ADR-203 §3:
// "cert/course/path/theme/edge whose completion is provable"); only curiosity
// is target-optional.
func TestNewGoal_ThemeAndEdgeRequireTarget(t *testing.T) {
	for _, k := range []Kind{KindThemeMastery, KindEdge} {
		if _, err := NewGoal(NewGoalInput{
			TenantID: gTenant, LearnerGCID: gLearner, Kind: k, Now: gNow,
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("kind %q with nil target: err = %v; want ErrInvalid", k, err)
		}
	}
}

func TestNewGoal_ThemeMasteryWithTargetOK(t *testing.T) {
	g, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindThemeMastery,
		ChoraTargetRef: strptr(gTarget), Now: gNow,
	})
	if err != nil {
		t.Fatalf("theme_mastery with target must be valid: %v", err)
	}
	if g.ChoraTargetRef == nil || *g.ChoraTargetRef != gTarget {
		t.Errorf("target = %v; want %q", g.ChoraTargetRef, gTarget)
	}
}

// An empty / whitespace target pointer is normalised to nil, so a credential
// kind still fails-loud (no silent "" target persisted).
func TestNewGoal_BlankTargetPointerNormalisedToNil(t *testing.T) {
	if _, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindThemeMastery,
		ChoraTargetRef: strptr("   "), Now: gNow,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank target on target-requiring kind: err = %v; want ErrInvalid", err)
	}
	// curiosity with a blank target → normalised to nil, valid.
	g, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindCuriosity,
		ChoraTargetRef: strptr("  "), Now: gNow,
	})
	if err != nil {
		t.Fatalf("curiosity with blank target should normalise to nil: %v", err)
	}
	if g.ChoraTargetRef != nil {
		t.Errorf("blank target must normalise to nil; got %v", *g.ChoraTargetRef)
	}
}

func TestNewGoal_InvalidKind(t *testing.T) {
	if _, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: Kind("nonsense"), Now: gNow,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown kind: err = %v; want ErrInvalid", err)
	}
}

func TestNewGoal_RequiresTenantAndLearner(t *testing.T) {
	if _, err := NewGoal(NewGoalInput{
		TenantID: "  ", LearnerGCID: gLearner, Kind: KindCuriosity, Now: gNow,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank tenant: err = %v; want ErrInvalid", err)
	}
	if _, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: "", Kind: KindCuriosity, Now: gNow,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank learner: err = %v; want ErrInvalid", err)
	}
}

// ConceptSet is normalised: trimmed, empties dropped, de-duplicated, order kept.
func TestNewGoal_ConceptSetNormalised(t *testing.T) {
	g, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindCuriosity,
		ConceptSet: []string{" agile ", "agile", "", "  ", "scrum"}, Now: gNow,
	})
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	want := []string{"agile", "scrum"}
	if len(g.ConceptSet) != len(want) {
		t.Fatalf("concept_set = %v; want %v", g.ConceptSet, want)
	}
	for i := range want {
		if g.ConceptSet[i] != want[i] {
			t.Errorf("concept_set[%d] = %q; want %q", i, g.ConceptSet[i], want[i])
		}
	}
}

func TestNewGoal_TrimsNorthStarNoteAndScope(t *testing.T) {
	g, err := NewGoal(NewGoalInput{
		TenantID: "  " + gTenant + " ", LearnerGCID: " " + gLearner,
		Kind: KindCuriosity, NorthStarNote: "  pass the real PMP  ", Now: gNow,
	})
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	if g.TenantID != gTenant || g.LearnerGCID != gLearner {
		t.Errorf("scope not trimmed: %q / %q", g.TenantID, g.LearnerGCID)
	}
	if g.NorthStarNote != "pass the real PMP" {
		t.Errorf("north_star_note = %q; want trimmed", g.NorthStarNote)
	}
}

func TestNewGoal_ZeroNowFallsBackToWallClock(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	g, err := NewGoal(NewGoalInput{TenantID: gTenant, LearnerGCID: gLearner, Kind: KindCuriosity})
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	if g.CreatedAt.Before(before) {
		t.Errorf("created_at = %v; want a fresh wall-clock time", g.CreatedAt)
	}
}

// --- 1:1 Companion attachment invariant ---

func mustGoal(t *testing.T) *Goal {
	t.Helper()
	g, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindThemeMastery,
		ChoraTargetRef: strptr(gTarget), Now: gNow,
	})
	if err != nil {
		t.Fatalf("mustGoal: %v", err)
	}
	return g
}

func TestAttachCompanion_OK(t *testing.T) {
	g := mustGoal(t)
	later := gNow.Add(time.Hour)
	if err := g.AttachCompanion(gCompanion, later); err != nil {
		t.Fatalf("AttachCompanion: %v", err)
	}
	if g.AttachedCompanionID == nil || *g.AttachedCompanionID != gCompanion {
		t.Errorf("attached = %v; want %q", g.AttachedCompanionID, gCompanion)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
}

func TestAttachCompanion_EmptyID(t *testing.T) {
	g := mustGoal(t)
	if err := g.AttachCompanion("   ", gNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty companion id: err = %v; want ErrInvalid", err)
	}
}

func TestAttachCompanion_SameIDIdempotent(t *testing.T) {
	g := mustGoal(t)
	if err := g.AttachCompanion(gCompanion, gNow); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if err := g.AttachCompanion(gCompanion, gNow.Add(time.Hour)); err != nil {
		t.Errorf("re-attach same companion must be idempotent: %v", err)
	}
}

func TestAttachCompanion_DifferentWhenBoundRejected(t *testing.T) {
	g := mustGoal(t)
	if err := g.AttachCompanion(gCompanion, gNow); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	other := "01970000-0000-7000-b000-0000000000ff"
	if err := g.AttachCompanion(other, gNow); !errors.Is(err, ErrAlreadyAttached) {
		t.Errorf("attach different companion while bound: err = %v; want ErrAlreadyAttached", err)
	}
	if *g.AttachedCompanionID != gCompanion {
		t.Errorf("bond must be unchanged after rejected attach; got %q", *g.AttachedCompanionID)
	}
}

func TestAttachCompanion_OnDeletedRejected(t *testing.T) {
	g := mustGoal(t)
	g.SoftDelete(gNow)
	if err := g.AttachCompanion(gCompanion, gNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("attach on soft-deleted goal: err = %v; want ErrDeleted", err)
	}
}

func TestDetachCompanion_OK(t *testing.T) {
	g := mustGoal(t)
	_ = g.AttachCompanion(gCompanion, gNow)
	later := gNow.Add(2 * time.Hour)
	if err := g.DetachCompanion(later); err != nil {
		t.Fatalf("DetachCompanion: %v", err)
	}
	if g.AttachedCompanionID != nil {
		t.Errorf("bond not cleared: %v", *g.AttachedCompanionID)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
}

func TestDetachCompanion_NoneAttached(t *testing.T) {
	g := mustGoal(t)
	if err := g.DetachCompanion(gNow); !errors.Is(err, ErrNotAttached) {
		t.Errorf("detach with no bond: err = %v; want ErrNotAttached", err)
	}
}

// --- status + soft-delete ---

func TestSetStatus_Valid(t *testing.T) {
	g := mustGoal(t)
	later := gNow.Add(time.Hour)
	if err := g.SetStatus(StatusAchieved, later); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if g.Status != StatusAchieved {
		t.Errorf("status = %q; want achieved", g.Status)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
}

func TestSetStatus_Invalid(t *testing.T) {
	g := mustGoal(t)
	if err := g.SetStatus(Status("paused"), gNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid status: err = %v; want ErrInvalid", err)
	}
}

func TestSetStatus_OnDeletedRejected(t *testing.T) {
	g := mustGoal(t)
	g.SoftDelete(gNow)
	if err := g.SetStatus(StatusRetired, gNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("set status on soft-deleted goal: err = %v; want ErrDeleted", err)
	}
}

// --- graduation state machine (ADR-204 §9 — the active→achieved slice) ---

func TestGraduate_ActiveToAchieved(t *testing.T) {
	g := mustGoal(t) // NewGoal ⇒ StatusActive
	later := gNow.Add(time.Hour)
	if err := g.Graduate(later); err != nil {
		t.Fatalf("Graduate: %v", err)
	}
	if g.Status != StatusAchieved {
		t.Errorf("status = %q; want achieved", g.Status)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
}

func TestGraduate_IdempotentWhenAchieved(t *testing.T) {
	g := mustGoal(t)
	first := gNow.Add(time.Hour)
	if err := g.Graduate(first); err != nil {
		t.Fatalf("Graduate: %v", err)
	}
	// Re-graduating an already-achieved goal is a no-op (idempotent), NOT an
	// error — the subscriber may redeliver. No state change ⇒ no UpdatedAt bump.
	if err := g.Graduate(gNow.Add(2 * time.Hour)); err != nil {
		t.Fatalf("idempotent Graduate: %v", err)
	}
	if g.Status != StatusAchieved {
		t.Errorf("status = %q; want achieved", g.Status)
	}
	if !g.UpdatedAt.Equal(first) {
		t.Errorf("updated_at moved on idempotent graduate: %v; want %v", g.UpdatedAt, first)
	}
}

func TestGraduate_FromMaintenanceRejected(t *testing.T) {
	g := mustGoal(t)
	if err := g.SetStatus(StatusMaintenance, gNow); err != nil {
		t.Fatalf("setup SetStatus: %v", err)
	}
	if err := g.Graduate(gNow.Add(time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Errorf("graduate from maintenance: err = %v; want ErrInvalid", err)
	}
}

func TestGraduate_FromRetiredRejected(t *testing.T) {
	g := mustGoal(t)
	if err := g.SetStatus(StatusRetired, gNow); err != nil {
		t.Fatalf("setup SetStatus: %v", err)
	}
	if err := g.Graduate(gNow.Add(time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Errorf("graduate from retired: err = %v; want ErrInvalid", err)
	}
}

func TestGraduate_OnDeletedRejected(t *testing.T) {
	g := mustGoal(t)
	g.SoftDelete(gNow)
	if err := g.Graduate(gNow.Add(time.Hour)); !errors.Is(err, ErrDeleted) {
		t.Errorf("graduate on soft-deleted goal: err = %v; want ErrDeleted", err)
	}
}

func TestRecordMastery_SetsCountAndBumpsUpdatedAt(t *testing.T) {
	g := mustGoal(t)
	later := gNow.Add(time.Hour)
	if err := g.RecordMastery(3, later); err != nil {
		t.Fatalf("RecordMastery: %v", err)
	}
	if g.MasteredConceptCount != 3 {
		t.Errorf("mastered_concept_count = %d; want 3", g.MasteredConceptCount)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
}

func TestRecordMastery_ClampsNegativeToZero(t *testing.T) {
	g := mustGoal(t)
	if err := g.RecordMastery(-5, gNow.Add(time.Hour)); err != nil {
		t.Fatalf("RecordMastery: %v", err)
	}
	if g.MasteredConceptCount != 0 {
		t.Errorf("mastered_concept_count = %d; want 0 (clamped)", g.MasteredConceptCount)
	}
}

func TestRecordMastery_OnDeletedRejected(t *testing.T) {
	g := mustGoal(t)
	g.SoftDelete(gNow)
	if err := g.RecordMastery(2, gNow.Add(time.Hour)); !errors.Is(err, ErrDeleted) {
		t.Errorf("RecordMastery on soft-deleted goal: err = %v; want ErrDeleted", err)
	}
}

func TestSoftDelete(t *testing.T) {
	g := mustGoal(t)
	later := gNow.Add(time.Hour)
	g.SoftDelete(later)
	if g.DeletedAt == nil || !g.DeletedAt.Equal(later) {
		t.Errorf("deleted_at = %v; want %v", g.DeletedAt, later)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
}

// --- enum helpers ---

func TestKindAndStatusValidity(t *testing.T) {
	for _, k := range []Kind{KindCuriosity, KindThemeMastery, KindEdge} {
		if !k.Valid() {
			t.Errorf("kind %q should be valid", k)
		}
	}
	if Kind("x").Valid() {
		t.Error("unknown kind reported valid")
	}
	// ADR-214 §3: the removed credential kinds are no longer valid.
	for _, k := range []Kind{Kind("cert"), Kind("course"), Kind("path")} {
		if k.Valid() {
			t.Errorf("removed credential kind %q must be invalid", k)
		}
	}
	for _, s := range []Status{StatusActive, StatusAchieved, StatusMaintenance, StatusRetired} {
		if !s.Valid() {
			t.Errorf("status %q should be valid", s)
		}
	}
	if Status("x").Valid() {
		t.Error("unknown status reported valid")
	}
}

// --- ADR-214/213: root-concept anchor + personal-axis completion ---

func TestNewGoal_RootConceptAnchor(t *testing.T) {
	g, err := NewGoal(NewGoalInput{
		TenantID: gTenant, LearnerGCID: gLearner, Kind: KindCuriosity,
		RootConceptID: strptr("  " + gConcept + " "), Now: gNow,
	})
	if err != nil {
		t.Fatalf("NewGoal with root concept: %v", err)
	}
	if g.RootConceptID == nil || *g.RootConceptID != gConcept {
		t.Errorf("root_concept_id = %v; want %q (trimmed)", g.RootConceptID, gConcept)
	}
	// A blank anchor normalises to nil (a plain goal may have none yet).
	g2, _ := NewGoal(NewGoalInput{TenantID: gTenant, LearnerGCID: gLearner, Kind: KindCuriosity, RootConceptID: strptr("  "), Now: gNow})
	if g2.RootConceptID != nil {
		t.Errorf("blank anchor must normalise to nil; got %v", *g2.RootConceptID)
	}
}

func TestAnchorToConcept(t *testing.T) {
	g := mustGoal(t)
	later := gNow.Add(time.Hour)
	if err := g.AnchorToConcept(" "+gConcept, later); err != nil {
		t.Fatalf("AnchorToConcept: %v", err)
	}
	if g.RootConceptID == nil || *g.RootConceptID != gConcept {
		t.Errorf("root_concept_id = %v; want %q", g.RootConceptID, gConcept)
	}
	if !g.UpdatedAt.Equal(later) {
		t.Errorf("updated_at not bumped: %v", g.UpdatedAt)
	}
	if err := g.AnchorToConcept("   ", gNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank anchor must be ErrInvalid; got %v", err)
	}
	g.SoftDelete(gNow)
	if err := g.AnchorToConcept(gConcept, gNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("anchor on deleted must be ErrDeleted; got %v", err)
	}
}

// The impermeable wall (ADR-213 §2/§3): personal completion is learner-defined
// and NEVER touches the verified Status/Graduate axis — no forge.
func TestMarkPersonalComplete_NoForge(t *testing.T) {
	g := mustGoal(t) // StatusActive
	later := gNow.Add(time.Hour)
	if err := g.MarkPersonalComplete(later); err != nil {
		t.Fatalf("MarkPersonalComplete: %v", err)
	}
	if g.PersonalCompletedAt == nil || !g.PersonalCompletedAt.Equal(later) {
		t.Fatalf("personal_completed_at = %v; want %v", g.PersonalCompletedAt, later)
	}
	// NO FORGE: the verified/credentialed axis is untouched.
	if g.Status != StatusActive {
		t.Errorf("personal completion forged the verified status: %q; want still active", g.Status)
	}
	// Idempotent: re-marking keeps the first completion time (no updated_at move).
	if err := g.MarkPersonalComplete(gNow.Add(2 * time.Hour)); err != nil {
		t.Fatalf("idempotent MarkPersonalComplete: %v", err)
	}
	if !g.PersonalCompletedAt.Equal(later) {
		t.Errorf("personal_completed_at moved on idempotent mark: %v; want %v", g.PersonalCompletedAt, later)
	}
	// Reopen clears it, still orthogonal to the verified status.
	if err := g.ReopenPersonal(gNow.Add(3 * time.Hour)); err != nil {
		t.Fatalf("ReopenPersonal: %v", err)
	}
	if g.PersonalCompletedAt != nil || g.Status != StatusActive {
		t.Errorf("reopen: personal=%v status=%q; want nil / active", g.PersonalCompletedAt, g.Status)
	}
}

func TestMarkPersonalComplete_OnDeletedRejected(t *testing.T) {
	g := mustGoal(t)
	g.SoftDelete(gNow)
	if err := g.MarkPersonalComplete(gNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("personal completion on deleted: err = %v; want ErrDeleted", err)
	}
	if err := g.ReopenPersonal(gNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("reopen on deleted: err = %v; want ErrDeleted", err)
	}
}

func TestErrorsAreDistinct(t *testing.T) {
	all := []error{ErrInvalid, ErrAlreadyAttached, ErrNotAttached, ErrDeleted}
	for i := range all {
		for j := range all {
			if i != j && errors.Is(all[i], all[j]) {
				t.Errorf("sentinel %v collides with %v", all[i], all[j])
			}
		}
		if strings.TrimSpace(all[i].Error()) == "" {
			t.Errorf("sentinel %d has empty message", i)
		}
	}
}
