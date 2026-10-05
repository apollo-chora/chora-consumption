// derived_weakness_projector_drill_test.go — W4 drill-completion recovery wiring.
//
// When a learner answers a suggested-drill atom at/above the mastery threshold,
// the projector must ALSO recover the EXPLICIT edge(s) that cached that atom
// (via RecoverByDrillAtomID) — the only path that moves an uploaded edge, whose
// minted concept_key never equals the atom's broad primary topic. Below the
// threshold there is no drill recovery (the existing topic-upsert path stands).
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// dwDrillCall captures one RecoverByDrillAtomID invocation on dwFakeRepo.
type dwDrillCall struct {
	GCID      string
	AtomID    string
	Strength  float64
	CtxTenant string
}

func (r *dwFakeRepo) RecoverByDrillAtomID(ctx context.Context, gcid, atomID string, strength float64, _ time.Time) ([]lw.GrownEdge, error) {
	if r.drillRecoverErr != nil {
		return nil, r.drillRecoverErr
	}
	r.drillRecovers = append(r.drillRecovers, dwDrillCall{
		GCID: gcid, AtomID: atomID, Strength: strength,
		CtxTenant: tracing.TenantIDFromContext(ctx),
	})
	return r.grownOnDrill, nil
}

func TestAtomSession_DrillAtomCompletedAboveThreshold_RecoversByDrillAtom(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.85}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-drill-1"), dwSessionPayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.drillRecovers) != 1 {
		t.Fatalf("drillRecovers = %d, want 1 (the completed atom recovers its drilling edge)", len(repo.drillRecovers))
	}
	got := repo.drillRecovers[0]
	if got.AtomID != "atom-1" {
		t.Fatalf("drill recover atom = %q, want atom-1 (the completed atom id)", got.AtomID)
	}
	if got.GCID != dwGCID {
		t.Fatalf("drill recover gcid = %q, want %q", got.GCID, dwGCID)
	}
	if got.CtxTenant != dwTenant {
		t.Fatalf("drill recover ctx tenant = %q, want %q (tenant-propagated)", got.CtxTenant, dwTenant)
	}
}

func TestAtomSession_BelowThreshold_NoDrillRecovery(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.4}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-drill-2"), dwSessionPayload(false)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.drillRecovers) != 0 {
		t.Fatalf("below the mastery threshold must NOT drill-recover; got %d", len(repo.drillRecovers))
	}
}

func TestAtomSession_DrillRecoverErrorNacks(t *testing.T) {
	repo := &dwFakeRepo{drillRecoverErr: errors.New("boom")}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.9}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-drill-3"), dwSessionPayload(true)); err == nil {
		t.Fatal("a drill-recovery failure must surface (Pub/Sub NACK)")
	}
}
