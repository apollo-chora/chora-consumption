// dose_growth_edges_handler_test.go — W6 (Epic-1b): the daily-dose handler
// feeds the learner's Growth Edges into the composer's weakness slot (cached
// drill atoms preferred). Reuses the L4 Fix-1 real-atom-universe harness.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// doseLWStub is a settable lw.Repository double (mirrors kgLWStub but local
// recording of the query keeps the two suites independent).
type doseLWStub struct {
	items   []lw.LearnerWeakness
	listErr error
	// getResult / getErr back the focused-practice Get(ctx, gcid, id) path
	// (Phase 2A). nil/nil keeps the historical (nil, nil) "not found" default.
	getResult *lw.LearnerWeakness
	getErr    error
}

func (s *doseLWStub) Upsert(context.Context, lw.UpsertInput) (lw.UpsertResult, error) {
	return lw.UpsertResult{}, nil
}
func (s *doseLWStub) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	if s.listErr != nil {
		return lw.ListResult{}, s.listErr
	}
	return lw.ListResult{Items: s.items}, nil
}
func (s *doseLWStub) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return s.items, s.listErr
}
func (s *doseLWStub) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return s.getResult, s.getErr
}
func (s *doseLWStub) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (s *doseLWStub) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *doseLWStub) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *doseLWStub) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

func doseEdge(t *testing.T, label string, strength float64, cached ...string) lw.LearnerWeakness {
	t.Helper()
	w, err := lw.New(lw.UpsertInput{
		TenantID:     testTenant,
		LearnerGCID:  testGCID,
		ConceptLabel: label,
		Embedding:    []float32{0.1},
		Strength:     strength,
		Source:       lw.SourceExplicit,
		Now:          time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	w.CachedDrillAtomIDs = cached
	return *w
}

func doseWeaknessAtomIDs(t *testing.T, srv *Server) []string {
	t.Helper()
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			AtomID     string `json:"atom_id"`
			DoseReason string `json:"dose_reason"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := []string{}
	for _, e := range resp.Entries {
		if e.DoseReason == "weakness" {
			out = append(out, e.AtomID)
		}
	}
	return out
}

func TestDailyDose_GrowthEdgeDrillsCachedAtom(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.LearnerWeakness = &doseLWStub{items: []lw.LearnerWeakness{
		doseEdge(t, "Product Ownership", 0.9, realDoseAtomIDs[3]),
	}}

	weak := doseWeaknessAtomIDs(t, srv)
	if len(weak) == 0 || weak[0] != realDoseAtomIDs[3] {
		t.Fatalf("weakness picks = %v, want cached drill %s first", weak, realDoseAtomIDs[3])
	}
}

func TestDailyDose_GrowthEdgeReadErrorFailsSoft(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.LearnerWeakness = &doseLWStub{listErr: errors.New("boom")}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Growth-Edge read failure must not break the dose: %d", w.Code)
	}
}

// doseEntries returns (atom_id, dose_reason) pairs for a daily-dose request,
// optionally focused via ?growth_edge_id={id}.
func doseEntries(t *testing.T, srv *Server, target string) []struct{ AtomID, Reason string } {
	t.Helper()
	w := authedReq(t, srv, http.MethodGet, target, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			AtomID     string `json:"atom_id"`
			DoseReason string `json:"dose_reason"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := make([]struct{ AtomID, Reason string }, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, struct{ AtomID, Reason string }{e.AtomID, e.DoseReason})
	}
	return out
}

// TestDailyDose_FocusedGrowthEdge_DrillsSingleEdge — the ?growth_edge_id={id}
// param routes the dose to the focused-practice path: the weakness slot drills
// the one edge loaded via Get from its (gradable) cached drill atoms ONLY (no
// slug fallback — CHO-1895), so the 3 cached atoms are the 3 weakness picks and
// the remaining budget tops up with fresh. The normal cascade caps weakness at
// DoseWeaknessCount (2), so 3 cached-atom weakness picks unambiguously prove the
// focused path AND the alignment invariant (every weakness pick is a cached
// drill atom that can recover the edge via RecoverByDrillAtomID).
func TestDailyDose_FocusedGrowthEdge_DrillsSingleEdge(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs) // 6 atoms, all topic "product-ownership", gradable
	cached := []string{realDoseAtomIDs[0], realDoseAtomIDs[1], realDoseAtomIDs[2]}
	edge := doseEdge(t, "Product Ownership", 0.9, cached...)
	srv.LearnerWeakness = &doseLWStub{getResult: &edge}

	entries := doseEntries(t, srv, "/companion/daily-dose?growth_edge_id="+edge.ID)
	weak := []string{}
	for _, e := range entries {
		if e.Reason == "weakness" {
			weak = append(weak, e.AtomID)
		}
	}
	if len(weak) != 3 {
		t.Fatalf("focused weakness picks = %d, want 3 (only the edge's cached drill atoms); entries=%+v", len(weak), entries)
	}
	cachedSet := map[string]bool{cached[0]: true, cached[1]: true, cached[2]: true}
	for _, id := range weak {
		if !cachedSet[id] {
			t.Errorf("focused weakness pick %s is NOT a cached drill atom — it can never recover the edge", id)
		}
	}
	for _, id := range cached {
		found := false
		for _, w := range weak {
			if w == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("cached drill atom %s missing from focused weakness picks %v", id, weak)
		}
	}
}

// TestDailyDose_NoFocusParam_DoesNotFocusDrill — without the param the dose
// takes the normal path (List, not Get), so it is NOT a 5-weakness focused
// drill even when Get would return an edge. Proves the param is the trigger.
func TestDailyDose_NoFocusParam_DoesNotFocusDrill(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	edge := doseEdge(t, "Product Ownership", 0.9, realDoseAtomIDs[0], realDoseAtomIDs[1], realDoseAtomIDs[2])
	srv.LearnerWeakness = &doseLWStub{getResult: &edge} // Get set, List empty

	entries := doseEntries(t, srv, "/companion/daily-dose")
	weak := 0
	for _, e := range entries {
		if e.Reason == "weakness" {
			weak++
		}
	}
	if weak == 5 {
		t.Fatalf("no-param dose must not focus-drill the whole budget; weakness=%d", weak)
	}
}

// TestDailyDose_FocusedGrowthEdge_ReadErrorFailsSoft — a Get error on the
// focused path must NOT break the dose; it degrades to a normal fresh dose (200).
func TestDailyDose_FocusedGrowthEdge_ReadErrorFailsSoft(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.LearnerWeakness = &doseLWStub{getErr: errors.New("boom")}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose?growth_edge_id=edge-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("focused Get failure must not break the dose: %d body=%s", w.Code, w.Body.String())
	}
}
