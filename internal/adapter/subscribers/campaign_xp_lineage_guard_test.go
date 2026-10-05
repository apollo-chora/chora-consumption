// campaign_xp_lineage_guard_test.go — WS-C6 (CHO-2085, ADR-227 D14) lineage
// re-award refusal on the campaign XP consumer. RED-first.
//
// Binding semantics pinned here (mig 0077: "did XP already flow to an
// ancestor?" — the refusal lives HERE, consumer-side, same-DB read):
//   - a merge lowers the survivor's ladder to the AND of rungs; the re-climb
//     emits fresh rung_cleared/node_won events with fresh event ids, so DB
//     idempotency alone would re-award. The guard reads the node's tombstoned
//     ladder history (its own pre-merge rows + every lineage ancestor's) and
//     REFUSES: a first-clear at a rung any ancestor already cleared awards
//     nothing (ack + loud log); a node_won on a lineage that was ever won
//     awards nothing.
//   - refresher events (is_refresher=true) are already reduced-priced re-warms
//     of the LIVE ladder — the lineage read is skipped entirely.
//   - goal_sealed has no lineage dimension (goal-level; spacing guard owns it).
//   - a lineage-history read error NACKs (fail-loud) — never award-anyway,
//     never skip-anyway.
package subscribers

import (
	"context"
	"errors"
	"testing"
)

// cxpLineage fakes the CampaignLineageHistory port.
type cxpLineage struct {
	maxRungs   int
	everWon    bool
	err        error
	calls      int
	gotTenant  string
	gotGCID    string
	gotConcept string
}

func (f *cxpLineage) PriorLadderState(_ context.Context, tenantID, learnerGCID, conceptID string) (int, bool, error) {
	f.calls++
	f.gotTenant, f.gotGCID, f.gotConcept = tenantID, learnerGCID, conceptID
	if f.err != nil {
		return 0, false, f.err
	}
	return f.maxRungs, f.everWon, nil
}

func newCXPWithLineage(a *cxpAwarder, r *cxpResolver, h *cxpHistory, l *cxpLineage) *CampaignXPSubscriber {
	return NewCampaignXPSubscriber(a, r, h, l, DefaultSealXPMinSpacing, cxpFixedNow)
}

func TestRungCleared_LineageRefusesAncestorClearedRung(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{maxRungs: 4} // survivor's pre-merge tombstone reached 4/6
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	p := cxpRungPayload() // rung 3, is_refresher=false
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("HandleRungCleared: %v (refusal must ACK, not NACK)", err)
	}
	if got := a.awards(); len(got) != 0 {
		t.Fatalf("awards = %d, want 0 (rung 3 <= lineage max 4 — no re-award, D14)", len(got))
	}
	if l.calls != 1 {
		t.Fatalf("lineage reads = %d, want 1", l.calls)
	}
	if l.gotConcept != p.ConceptID || l.gotTenant != p.TenantID || l.gotGCID != p.LearnerGCID {
		t.Fatalf("lineage read scoped wrong: got (%s,%s,%s)", l.gotTenant, l.gotGCID, l.gotConcept)
	}
}

func TestRungCleared_LineageAllowsRungAboveAncestors(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{maxRungs: 4}
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	p := cxpRungPayload()
	p.Rung = 5 // above every ancestor — genuinely new ground
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("HandleRungCleared: %v", err)
	}
	got := a.awards()
	if len(got) != 1 {
		t.Fatalf("awards = %d, want 1", len(got))
	}
	if got[0].Source != "campaign_rung_cleared" {
		t.Fatalf("source = %q, want campaign_rung_cleared", got[0].Source)
	}
}

func TestRungCleared_RefresherSkipsLineageRead(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{maxRungs: 6}
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	p := cxpRungPayload()
	p.IsRefresher = true
	if err := s.HandleRungCleared(context.Background(), p); err != nil {
		t.Fatalf("HandleRungCleared: %v", err)
	}
	got := a.awards()
	if len(got) != 1 || got[0].Source != "campaign_rung_refreshed" {
		t.Fatalf("awards = %+v, want one campaign_rung_refreshed (refreshers untouched by lineage)", got)
	}
	if l.calls != 0 {
		t.Fatalf("lineage reads = %d, want 0 (refresher path)", l.calls)
	}
}

func TestRungCleared_LineageReadErrorNACKs(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{err: errors.New("db unavailable")}
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	if err := s.HandleRungCleared(context.Background(), cxpRungPayload()); err == nil {
		t.Fatalf("expected NACK on lineage read error, got ack")
	}
	if got := a.awards(); len(got) != 0 {
		t.Fatalf("awards = %d, want 0 (never award on a failed guard read)", len(got))
	}
}

func TestNodeWon_LineageRefusesEverWon(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{everWon: true}
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	if err := s.HandleNodeWon(context.Background(), cxpWonPayload()); err != nil {
		t.Fatalf("HandleNodeWon: %v (refusal must ACK)", err)
	}
	if got := a.awards(); len(got) != 0 {
		t.Fatalf("awards = %d, want 0 (lineage ever-won — no node_won re-award, D14)", len(got))
	}
	if l.calls != 1 {
		t.Fatalf("lineage reads = %d, want 1", l.calls)
	}
}

func TestNodeWon_AwardsWhenLineageNeverWon(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{maxRungs: 4, everWon: false}
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	if err := s.HandleNodeWon(context.Background(), cxpWonPayload()); err != nil {
		t.Fatalf("HandleNodeWon: %v", err)
	}
	got := a.awards()
	if len(got) != 1 || got[0].Source != "campaign_node_won" {
		t.Fatalf("awards = %+v, want one campaign_node_won", got)
	}
}

func TestNodeWon_LineageReadErrorNACKs(t *testing.T) {
	a := &cxpAwarder{}
	r := &cxpResolver{byGoal: map[string]string{cxpGoal: cxpFam}}
	l := &cxpLineage{err: errors.New("db unavailable")}
	s := newCXPWithLineage(a, r, &cxpHistory{}, l)

	if err := s.HandleNodeWon(context.Background(), cxpWonPayload()); err == nil {
		t.Fatalf("expected NACK on lineage read error, got ack")
	}
}

func TestNewCampaignXPSubscriber_NilLineagePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on nil lineage history (no-stubs wiring gate)")
		}
	}()
	NewCampaignXPSubscriber(&cxpAwarder{}, &cxpResolver{}, &cxpHistory{}, nil, 0, nil)
}
