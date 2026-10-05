// campaign_test.go — WS-C1 (CHO-2080, ADR-227 D6-D9) domain-core contract.
//
// Pure, clock-injected, table-driven. Pins:
//   - the D6 rung ladder (original-Bloom vocabulary, ladder order:
//     evaluation=5, synthesis=6 — the summit; NOT creation CognitiveLevel
//     numbering),
//   - the rung-clear rule (threshold corrects at the current rung),
//   - the D7 pacing gate (at most one rung advance per UTC calendar day —
//     corrects still count + retention still reviews, the CLEAR waits),
//   - the D8 refresh discipline (refresher at highest cleared rung, cleared
//     rungs never lost),
//   - the D9 win ratchet (6/6 is permanent; no post-win campaign grading),
//   - frontier/seal (seal only on empty frontier; re-seal gated on >= N new
//     wins + the weekly interval).
package campaign

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
)

const (
	cTenant  = "01971a00-0000-7000-8000-00000000000a"
	cGCID    = "01971a00-0000-7000-8000-00000000000b"
	cConcept = "01971a00-0000-7000-8000-00000000000c"
)

var cNow = time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)

func freshProgress(t *testing.T) *NodeProgress {
	t.Helper()
	p, err := NewNodeProgress(cTenant, cGCID, cConcept, cNow)
	if err != nil {
		t.Fatalf("NewNodeProgress: %v", err)
	}
	return p
}

// clearRung drives one full rung clear (threshold corrects) on the given day.
func clearRung(t *testing.T, p *NodeProgress, rung Rung, day time.Time) {
	t.Helper()
	for i := 0; i < DefaultRungClearCorrect; i++ {
		out, err := p.ApplyGraded(rung, true, DefaultRungClearCorrect, day.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("ApplyGraded(rung %d, correct %d): %v", rung, i+1, err)
		}
		if i == DefaultRungClearCorrect-1 && out.ClearedRung != rung {
			t.Fatalf("expected rung %d to clear on correct #%d, got %+v", rung, i+1, out)
		}
	}
}

// --- Ladder ------------------------------------------------------------------

func TestRungLadder_OrderAndLabels(t *testing.T) {
	wantOrder := []struct {
		r     Rung
		label string
	}{
		{RungKnowledge, "knowledge"},
		{RungComprehension, "comprehension"},
		{RungApplication, "application"},
		{RungAnalysis, "analysis"},
		{RungEvaluation, "evaluation"},
		{RungSynthesis, "synthesis"},
	}
	for i, w := range wantOrder {
		if int(w.r) != i+1 {
			t.Errorf("%s: ladder position = %d, want %d", w.label, int(w.r), i+1)
		}
		if w.r.Label() != w.label {
			t.Errorf("rung %d label = %q, want %q", int(w.r), w.r.Label(), w.label)
		}
		if !w.r.Valid() {
			t.Errorf("rung %d should be valid", int(w.r))
		}
	}
	// The two-vocabularies trap: the ladder tops out at synthesis (6), with
	// evaluation below it (5) — the reverse of the original-Bloom enum order
	// used by chora-creation.
	if RungSynthesis != 6 || RungEvaluation != 5 {
		t.Fatalf("ladder order broken: evaluation=%d synthesis=%d", RungEvaluation, RungSynthesis)
	}
	if TotalRungs != 6 {
		t.Fatalf("TotalRungs = %d", TotalRungs)
	}
	for _, bad := range []Rung{0, 7, -1} {
		if bad.Valid() {
			t.Errorf("rung %d should be invalid", int(bad))
		}
	}
}

// --- Constructor ---------------------------------------------------------------

func TestNewNodeProgress(t *testing.T) {
	p := freshProgress(t)
	if p.ID == "" {
		t.Error("ID not minted")
	}
	if p.RungsCleared != 0 || len(p.RungClearedAt) != 0 || p.CurrentRungCorrect != 0 {
		t.Errorf("fresh progress not zeroed: %+v", p)
	}
	if p.Won() {
		t.Error("fresh progress must not be won")
	}
	if next, ok := p.NextRung(); !ok || next != RungKnowledge {
		t.Errorf("NextRung = %v/%v, want knowledge/true", next, ok)
	}

	for _, tc := range []struct{ tenant, gcid, concept string }{
		{"", cGCID, cConcept},
		{cTenant, "", cConcept},
		{cTenant, cGCID, ""},
	} {
		if _, err := NewNodeProgress(tc.tenant, tc.gcid, tc.concept, cNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("NewNodeProgress(%q,%q,%q) err = %v, want ErrInvalid", tc.tenant, tc.gcid, tc.concept, err)
		}
	}
}

// --- ApplyGraded ---------------------------------------------------------------

func TestApplyGraded_CountsThenClearsAtThreshold(t *testing.T) {
	p := freshProgress(t)

	out, err := p.ApplyGraded(RungKnowledge, true, 2, cNow)
	if err != nil {
		t.Fatalf("correct #1: %v", err)
	}
	if !out.Counted || out.ClearedRung != 0 || out.Won || out.PacedToday {
		t.Errorf("correct #1 outcome = %+v, want counted-only", out)
	}
	if p.CurrentRungCorrect != 1 {
		t.Errorf("counter = %d, want 1", p.CurrentRungCorrect)
	}

	out, err = p.ApplyGraded(RungKnowledge, true, 2, cNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("correct #2: %v", err)
	}
	if out.ClearedRung != RungKnowledge {
		t.Errorf("correct #2 should clear knowledge, got %+v", out)
	}
	if p.RungsCleared != 1 || len(p.RungClearedAt) != 1 {
		t.Errorf("after clear: rungs=%d audit=%d", p.RungsCleared, len(p.RungClearedAt))
	}
	if p.CurrentRungCorrect != 0 {
		t.Errorf("counter must reset after clear, got %d", p.CurrentRungCorrect)
	}
	if p.LastAdvanceDate == nil || p.LastAdvanceDate.UTC().Format("2006-01-02") != cNow.UTC().Format("2006-01-02") {
		t.Errorf("LastAdvanceDate = %v, want today", p.LastAdvanceDate)
	}
}

func TestApplyGraded_WrongAnswerNeverCounts(t *testing.T) {
	p := freshProgress(t)
	out, err := p.ApplyGraded(RungKnowledge, false, 2, cNow)
	if err != nil {
		t.Fatalf("wrong: %v", err)
	}
	if out.Counted || out.ClearedRung != 0 || out.IsRefresher {
		t.Errorf("wrong-answer outcome = %+v, want no-op", out)
	}
	if p.CurrentRungCorrect != 0 {
		t.Errorf("counter = %d, want 0", p.CurrentRungCorrect)
	}
}

func TestApplyGraded_PacingGateFollowsAcceleratedBuckets(t *testing.T) {
	if err := doseclock.Init(600); err != nil {
		t.Fatalf("doseclock.Init: %v", err)
	}
	t.Cleanup(doseclock.Reset)
	p := freshProgress(t)
	clearRung(t, p, RungKnowledge, cNow) // advanced this 10-min dose day

	// Same 10-minute dose day: the D7 gate holds.
	out, err := p.ApplyGraded(RungComprehension, true, 1, cNow.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("same-bucket correct: %v", err)
	}
	if out.ClearedRung != 0 || !out.PacedToday {
		t.Fatalf("same-bucket outcome = %+v, want paced with no clear", out)
	}

	// The NEXT 10-minute dose day releases the clear — the accelerated
	// clock exists precisely so the walk arc proves out in one sitting.
	out, err = p.ApplyGraded(RungComprehension, true, 1, cNow.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("next-bucket correct: %v", err)
	}
	if out.ClearedRung != RungComprehension || out.PacedToday {
		t.Fatalf("next-bucket outcome = %+v, want the comprehension clear", out)
	}
}

func TestApplyGraded_PacingGateHoldsSecondAdvanceSameDay(t *testing.T) {
	p := freshProgress(t)
	clearRung(t, p, RungKnowledge, cNow) // advanced today

	// Two more corrects at rung 2, same day: counted, threshold met, but the
	// D7 gate holds the clear (AC: "the next rung unlocks only tomorrow").
	later := cNow.Add(2 * time.Hour)
	if out, err := p.ApplyGraded(RungComprehension, true, 2, later); err != nil || out.ClearedRung != 0 {
		t.Fatalf("paced correct #1: out=%+v err=%v", out, err)
	}
	out, err := p.ApplyGraded(RungComprehension, true, 2, later.Add(time.Minute))
	if err != nil {
		t.Fatalf("paced correct #2: %v", err)
	}
	if out.ClearedRung != 0 || !out.PacedToday {
		t.Errorf("outcome = %+v, want PacedToday with no clear", out)
	}
	if p.RungsCleared != 1 {
		t.Errorf("rungs cleared = %d, want still 1", p.RungsCleared)
	}

	// Tomorrow: the pending threshold releases on the next correct.
	tomorrow := cNow.Add(24 * time.Hour)
	out, err = p.ApplyGraded(RungComprehension, true, 2, tomorrow)
	if err != nil {
		t.Fatalf("next-day correct: %v", err)
	}
	if out.ClearedRung != RungComprehension {
		t.Errorf("next-day outcome = %+v, want comprehension cleared", out)
	}
	if p.RungsCleared != 2 {
		t.Errorf("rungs cleared = %d, want 2", p.RungsCleared)
	}
}

func TestApplyGraded_RefresherAtClearedRung(t *testing.T) {
	p := freshProgress(t)
	clearRung(t, p, RungKnowledge, cNow)

	// Grading at an already-cleared rung is a D8 refresher: no counter, no
	// clear, cleared rungs never lost.
	out, err := p.ApplyGraded(RungKnowledge, true, 2, cNow.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("refresher: %v", err)
	}
	if !out.IsRefresher || out.Counted || out.ClearedRung != 0 {
		t.Errorf("refresher outcome = %+v", out)
	}
	if p.RungsCleared != 1 || p.CurrentRungCorrect != 0 {
		t.Errorf("refresher mutated ladder: rungs=%d counter=%d", p.RungsCleared, p.CurrentRungCorrect)
	}
}

func TestApplyGraded_BeyondUnlockedRungFailsLoud(t *testing.T) {
	p := freshProgress(t)
	if _, err := p.ApplyGraded(RungApplication, true, 2, cNow); !errors.Is(err, ErrRungNotUnlocked) {
		t.Errorf("err = %v, want ErrRungNotUnlocked", err)
	}
}

func TestApplyGraded_WinAtSixthIsPermanentRatchet(t *testing.T) {
	p := freshProgress(t)
	day := cNow
	for _, r := range []Rung{RungKnowledge, RungComprehension, RungApplication, RungAnalysis, RungEvaluation} {
		clearRung(t, p, r, day)
		day = day.Add(24 * time.Hour)
	}
	if p.RungsCleared != 5 || p.Won() {
		t.Fatalf("setup: rungs=%d won=%v", p.RungsCleared, p.Won())
	}

	// First correct at synthesis: counted, not cleared (threshold 2).
	if out, err := p.ApplyGraded(RungSynthesis, true, 2, day); err != nil || out.Won {
		t.Fatalf("synthesis #1: out=%+v err=%v", out, err)
	}
	out, err := p.ApplyGraded(RungSynthesis, true, 2, day.Add(time.Minute))
	if err != nil {
		t.Fatalf("synthesis #2: %v", err)
	}
	if out.ClearedRung != RungSynthesis || !out.Won {
		t.Errorf("outcome = %+v, want synthesis cleared + won", out)
	}
	if !p.Won() || p.WonAt == nil || p.RungsCleared != 6 {
		t.Errorf("won state broken: %+v", p)
	}
	if next, ok := p.NextRung(); ok {
		t.Errorf("NextRung after win = %v, want none", next)
	}

	// D9: the node exits the game — campaign grading refuses post-win.
	if _, err := p.ApplyGraded(RungSynthesis, true, 2, day.Add(48*time.Hour)); !errors.Is(err, ErrAlreadyWon) {
		t.Errorf("post-win grade err = %v, want ErrAlreadyWon", err)
	}
}

func TestApplyGraded_InvalidInputs(t *testing.T) {
	p := freshProgress(t)
	if _, err := p.ApplyGraded(Rung(0), true, 2, cNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("rung 0 err = %v, want ErrInvalid", err)
	}
	if _, err := p.ApplyGraded(Rung(7), true, 2, cNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("rung 7 err = %v, want ErrInvalid", err)
	}
	if _, err := p.ApplyGraded(RungKnowledge, true, 0, cNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("threshold 0 err = %v, want ErrInvalid", err)
	}
	del := cNow
	p.DeletedAt = &del
	if _, err := p.ApplyGraded(RungKnowledge, true, 2, cNow); !errors.Is(err, ErrDeleted) {
		t.Errorf("deleted err = %v, want ErrDeleted", err)
	}
}

// --- DecideServe ---------------------------------------------------------------

func fptr(v float64) *float64 { return &v }

func TestDecideServe(t *testing.T) {
	warm := fptr(0.9)
	cold := fptr(0.3)

	cleared2 := freshProgress(t)
	clearRung(t, cleared2, RungKnowledge, cNow)
	clearRung(t, cleared2, RungComprehension, cNow.Add(24*time.Hour))

	cases := []struct {
		name        string
		p           func(t *testing.T) *NodeProgress
		retention   *float64
		wantRung    Rung
		wantRefresh bool
	}{
		{"nil progress = unstarted node -> rung 1", func(*testing.T) *NodeProgress { return nil }, nil, RungKnowledge, false},
		{"fresh node ignores retention -> rung 1", freshProgress, cold, RungKnowledge, false},
		{"warm -> next rung", func(*testing.T) *NodeProgress { return cleared2 }, warm, RungApplication, false},
		{"cold -> refresher at highest cleared (D8)", func(*testing.T) *NodeProgress { return cleared2 }, cold, RungComprehension, true},
		{"missing retention row + cleared rungs -> conservative refresher", func(*testing.T) *NodeProgress { return cleared2 }, nil, RungComprehension, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := DecideServe(tc.p(t), tc.retention, cNow.Add(72*time.Hour))
			if err != nil {
				t.Fatalf("DecideServe: %v", err)
			}
			if dec.Rung != tc.wantRung || dec.IsRefresher != tc.wantRefresh {
				t.Errorf("decision = %+v, want rung=%d refresher=%v", dec, tc.wantRung, tc.wantRefresh)
			}
		})
	}
}

func TestDecideServe_WonNodeRefuses(t *testing.T) {
	p := freshProgress(t)
	day := cNow
	for _, r := range []Rung{RungKnowledge, RungComprehension, RungApplication, RungAnalysis, RungEvaluation, RungSynthesis} {
		clearRung(t, p, r, day)
		day = day.Add(24 * time.Hour)
	}
	if !p.Won() {
		t.Fatal("setup: node should be won")
	}
	if _, err := DecideServe(p, fptr(0.2), day); !errors.Is(err, ErrAlreadyWon) {
		t.Errorf("err = %v, want ErrAlreadyWon (won nodes exit the game, D9)", err)
	}
}

// --- Frontier + seal -------------------------------------------------------------

func TestComputeFrontier(t *testing.T) {
	subtree := map[string]bool{"root": true, "a": true, "b": true, "c": true}
	won := map[string]bool{"a": true, "c": true}
	f := ComputeFrontier(subtree, won)
	if f.Total != 4 || f.Won != 2 || len(f.Remaining) != 2 {
		t.Fatalf("frontier = %+v", f)
	}
	rem := map[string]bool{}
	for _, id := range f.Remaining {
		rem[id] = true
	}
	if !rem["root"] || !rem["b"] {
		t.Errorf("remaining = %v, want root+b", f.Remaining)
	}
}

func TestEvaluateSeal(t *testing.T) {
	emptyFrontier := FrontierState{Total: 3, Won: 3, Remaining: nil}
	openFrontier := FrontierState{Total: 3, Won: 1, Remaining: []string{"x", "y"}}
	weekAgo := cNow.Add(-8 * 24 * time.Hour)
	yesterday := cNow.Add(-24 * time.Hour)

	t.Run("first seal on empty frontier", func(t *testing.T) {
		dec, err := EvaluateSeal(emptyFrontier, nil, 0, DefaultResealMinNewWins, ResealMinInterval, cNow)
		if err != nil {
			t.Fatalf("EvaluateSeal: %v", err)
		}
		if dec.IsReseal || dec.NodesWon != 3 {
			t.Errorf("decision = %+v, want first seal of 3", dec)
		}
	})
	t.Run("blocked while frontier non-empty", func(t *testing.T) {
		if _, err := EvaluateSeal(openFrontier, nil, 0, DefaultResealMinNewWins, ResealMinInterval, cNow); !errors.Is(err, ErrFrontierNotEmpty) {
			t.Errorf("err = %v, want ErrFrontierNotEmpty", err)
		}
	})
	t.Run("empty campaign cannot seal", func(t *testing.T) {
		if _, err := EvaluateSeal(FrontierState{}, nil, 0, DefaultResealMinNewWins, ResealMinInterval, cNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
	t.Run("re-seal too soon (weekly cap)", func(t *testing.T) {
		if _, err := EvaluateSeal(emptyFrontier, &yesterday, 5, DefaultResealMinNewWins, ResealMinInterval, cNow); !errors.Is(err, ErrSealTooSoon) {
			t.Errorf("err = %v, want ErrSealTooSoon", err)
		}
	})
	t.Run("re-seal without enough new wins", func(t *testing.T) {
		if _, err := EvaluateSeal(emptyFrontier, &weekAgo, 2, 3, ResealMinInterval, cNow); !errors.Is(err, ErrSealNoNewWins) {
			t.Errorf("err = %v, want ErrSealNoNewWins", err)
		}
	})
	t.Run("re-seal after genuine expansion", func(t *testing.T) {
		dec, err := EvaluateSeal(emptyFrontier, &weekAgo, 3, 3, ResealMinInterval, cNow)
		if err != nil {
			t.Fatalf("EvaluateSeal: %v", err)
		}
		if !dec.IsReseal {
			t.Errorf("decision = %+v, want re-seal", dec)
		}
	})
}
