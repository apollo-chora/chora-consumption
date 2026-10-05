// goal_campaign_test.go — WS-C1 (CHO-2080, ADR-227 D11 + D3) goal-side
// campaign state: the focus pointer and the seal timestamps.
//
// Key invariant pinned here: campaign seal history (CampaignSealedAt) is
// DISTINCT from the ADR-213 personal axis (PersonalCompletedAt). A bare
// personal-complete declaration never touches seal state; a seal sets the
// personal axis only when it is still open, and a re-seal moves ONLY the
// seal timestamp (the learner's original "done for me" moment is history,
// not a counter).
package goal

import (
	"errors"
	"testing"
	"time"
)

var campNow = time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)

func campaignGoalFixture(t *testing.T) *Goal {
	t.Helper()
	g, err := NewGoal(NewGoalInput{
		TenantID:    "01971a00-0000-7000-8000-0000000000aa",
		LearnerGCID: "01971a00-0000-7000-8000-0000000000bb",
		Kind:        KindCuriosity,
		Now:         campNow.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return g
}

func TestSetFocusConcept(t *testing.T) {
	g := campaignGoalFixture(t)
	focus := "01971a00-0000-7000-8000-0000000000cc"

	if err := g.SetFocusConcept(&focus, campNow); err != nil {
		t.Fatalf("set: %v", err)
	}
	if g.FocusConceptID == nil || *g.FocusConceptID != focus {
		t.Fatalf("FocusConceptID = %v, want %s", g.FocusConceptID, focus)
	}
	if !g.UpdatedAt.Equal(campNow.UTC()) {
		t.Errorf("UpdatedAt not bumped: %v", g.UpdatedAt)
	}

	// Move freely (D11).
	other := "01971a00-0000-7000-8000-0000000000dd"
	if err := g.SetFocusConcept(&other, campNow.Add(time.Hour)); err != nil {
		t.Fatalf("move: %v", err)
	}
	if *g.FocusConceptID != other {
		t.Errorf("FocusConceptID = %v, want %s", *g.FocusConceptID, other)
	}

	// Clear (unassigned: the Companion proposes, the learner assigns).
	if err := g.SetFocusConcept(nil, campNow.Add(2*time.Hour)); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if g.FocusConceptID != nil {
		t.Errorf("FocusConceptID = %v, want nil", g.FocusConceptID)
	}

	// Blank/whitespace id fails loud.
	blank := "   "
	if err := g.SetFocusConcept(&blank, campNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank err = %v, want ErrInvalid", err)
	}

	// Soft-deleted goal refuses.
	del := campNow
	g.DeletedAt = &del
	if err := g.SetFocusConcept(&focus, campNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("deleted err = %v, want ErrDeleted", err)
	}
}

func TestSealCampaign_FirstSealSetsBothAxes(t *testing.T) {
	g := campaignGoalFixture(t)
	if err := g.SealCampaign(campNow); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if g.CampaignSealedAt == nil || !g.CampaignSealedAt.Equal(campNow.UTC()) {
		t.Fatalf("CampaignSealedAt = %v, want %v", g.CampaignSealedAt, campNow)
	}
	if g.PersonalCompletedAt == nil || !g.PersonalCompletedAt.Equal(campNow.UTC()) {
		t.Fatalf("PersonalCompletedAt = %v, want seal to set the open personal axis", g.PersonalCompletedAt)
	}
}

func TestSealCampaign_ResealMovesOnlySealTimestamp(t *testing.T) {
	g := campaignGoalFixture(t)
	if err := g.SealCampaign(campNow); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	firstPersonal := *g.PersonalCompletedAt

	later := campNow.Add(10 * 24 * time.Hour)
	if err := g.SealCampaign(later); err != nil {
		t.Fatalf("re-seal: %v", err)
	}
	if !g.CampaignSealedAt.Equal(later.UTC()) {
		t.Errorf("CampaignSealedAt = %v, want %v", g.CampaignSealedAt, later)
	}
	if !g.PersonalCompletedAt.Equal(firstPersonal) {
		t.Errorf("PersonalCompletedAt moved on re-seal: %v, want %v", g.PersonalCompletedAt, firstPersonal)
	}
}

func TestSealCampaign_BarePersonalCompleteIsPreserved(t *testing.T) {
	g := campaignGoalFixture(t)
	declared := campNow.Add(-24 * time.Hour)
	if err := g.MarkPersonalComplete(declared); err != nil {
		t.Fatalf("bare personal complete: %v", err)
	}
	if g.CampaignSealedAt != nil {
		t.Fatal("bare personal-complete must not create seal history")
	}

	if err := g.SealCampaign(campNow); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !g.PersonalCompletedAt.Equal(declared.UTC()) {
		t.Errorf("PersonalCompletedAt = %v, want the original bare declaration %v", g.PersonalCompletedAt, declared)
	}
	if !g.CampaignSealedAt.Equal(campNow.UTC()) {
		t.Errorf("CampaignSealedAt = %v, want %v", g.CampaignSealedAt, campNow)
	}
}

func TestSealCampaign_DeletedRefuses(t *testing.T) {
	g := campaignGoalFixture(t)
	del := campNow
	g.DeletedAt = &del
	if err := g.SealCampaign(campNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("err = %v, want ErrDeleted", err)
	}
}
