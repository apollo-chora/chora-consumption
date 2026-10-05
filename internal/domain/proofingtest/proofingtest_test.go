// proofingtest_test.go — CHO-2040 (R8-6/R8-7): the Virgin Proofing Test
// aggregate + pure engines, RED-first.
//
// Pins:
//   - NewRequested invariants (identity fields, edge validation, UUIDv7 ids,
//     requested status, distinct assist_id).
//   - R8-7: ValidateTargetEdges fails ErrEdgesLackKeys when ANY ticked edge
//     carries no concept key (the composer passes KEYS, never titles).
//   - Status machine: requested|composing → ready|failed; terminal states
//     reject further transitions; soft-deleted rows reject everything.
//   - BuildTypePlan clamps + defaults (mcq=4/oe=2), total >= 1, <= 20.
//   - BuildPrompt is deterministic and carries keys + counts + goal anchor.
package proofingtest

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	tTenant    = "11111111-1111-4111-8111-111111111111"
	tLearner   = "22222222-2222-4222-8222-222222222222"
	tGoal      = "33333333-3333-4333-8333-333333333333"
	tCompanion = "44444444-4444-4444-8444-444444444444"
	tConcept   = "55555555-5555-4555-8555-555555555555"
	tConcept2  = "66666666-6666-4666-8666-666666666666"
)

func keyedEdges() []TargetEdge {
	return []TargetEdge{
		{ConceptID: tConcept, Key: "algebra.quadratic_roots", Title: "Quadratic roots", Intent: "remediate"},
		{ConceptID: tConcept2, Key: "algebra.completing_square", Title: "Completing the square", Intent: "explore"},
	}
}

func newValidInput() NewRequestedInput {
	return NewRequestedInput{
		TenantID:      tTenant,
		LearnerGCID:   tLearner,
		GoalID:        tGoal,
		CompanionID:   tCompanion,
		TargetEdges:   keyedEdges(),
		ManaReserved:  25,
		ReservationID: "proofing_test_gen:t:g:id",
		Now:           time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// ValidateTargetEdges — the R8-7 gate engine.
// ---------------------------------------------------------------------------

func TestValidateTargetEdges_EmptyIsNoTickedEdges(t *testing.T) {
	if err := ValidateTargetEdges(nil); !errors.Is(err, ErrNoTickedEdges) {
		t.Fatalf("nil edges: want ErrNoTickedEdges, got %v", err)
	}
	if err := ValidateTargetEdges([]TargetEdge{}); !errors.Is(err, ErrNoTickedEdges) {
		t.Fatalf("empty edges: want ErrNoTickedEdges, got %v", err)
	}
}

func TestValidateTargetEdges_MissingKeyFailsLoud_R87(t *testing.T) {
	edges := keyedEdges()
	edges[1].Key = "  " // whitespace-only == missing
	err := ValidateTargetEdges(edges)
	if !errors.Is(err, ErrEdgesLackKeys) {
		t.Fatalf("want ErrEdgesLackKeys, got %v", err)
	}
	// The error names the offending edge title so the FE/learner can see
	// WHICH edge is un-keyed (honest fail-loud, never a silent skip).
	if !strings.Contains(err.Error(), "Completing the square") {
		t.Fatalf("error should carry the offending title, got %q", err.Error())
	}
}

func TestValidateTargetEdges_AllKeysMissingFails(t *testing.T) {
	edges := keyedEdges()
	edges[0].Key = ""
	edges[1].Key = ""
	if err := ValidateTargetEdges(edges); !errors.Is(err, ErrEdgesLackKeys) {
		t.Fatalf("want ErrEdgesLackKeys, got %v", err)
	}
}

func TestValidateTargetEdges_MalformedConceptIDFails(t *testing.T) {
	edges := keyedEdges()
	edges[0].ConceptID = "not-a-uuid"
	if err := ValidateTargetEdges(edges); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for malformed concept id, got %v", err)
	}
}

func TestValidateTargetEdges_UntitledEdgeFails(t *testing.T) {
	edges := keyedEdges()
	edges[0].Title = " "
	if err := ValidateTargetEdges(edges); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for untitled edge, got %v", err)
	}
}

func TestValidateTargetEdges_KeyedEdgesPass(t *testing.T) {
	if err := ValidateTargetEdges(keyedEdges()); err != nil {
		t.Fatalf("keyed edges must validate, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewRequested — constructor invariants.
// ---------------------------------------------------------------------------

func TestNewRequested_MintsRequestedAggregate(t *testing.T) {
	in := newValidInput()
	p, err := NewRequested(in)
	if err != nil {
		t.Fatalf("NewRequested: %v", err)
	}
	if p.Status != StatusRequested {
		t.Fatalf("status = %q, want requested", p.Status)
	}
	if p.ID == "" || p.AssistID == "" {
		t.Fatalf("ID + AssistID must be minted, got %q / %q", p.ID, p.AssistID)
	}
	if p.ID == p.AssistID {
		t.Fatalf("ID and AssistID must be DISTINCT (aggregate id vs ai_assist batch ref)")
	}
	if p.TenantID != tTenant || p.LearnerGCID != tLearner || p.GoalID != tGoal || p.CompanionID != tCompanion {
		t.Fatalf("identity fields mis-set: %+v", p)
	}
	if len(p.TargetEdges) != 2 {
		t.Fatalf("target edges = %d, want 2", len(p.TargetEdges))
	}
	if p.ManaReserved != 25 || p.ReservationID != in.ReservationID {
		t.Fatalf("mana fields mis-set: reserved=%d resID=%q", p.ManaReserved, p.ReservationID)
	}
	if !p.CreatedAt.Equal(in.Now) || !p.UpdatedAt.Equal(in.Now) {
		t.Fatalf("clock mis-set: created=%v updated=%v", p.CreatedAt, p.UpdatedAt)
	}
	if p.DeletedAt != nil || p.TestSetRef != nil || len(p.TestSetPayload) != 0 {
		t.Fatalf("fresh aggregate must carry no testset/tombstone")
	}
}

func TestNewRequested_RequiredFieldsFailLoud(t *testing.T) {
	for name, mutate := range map[string]func(*NewRequestedInput){
		"tenant":    func(in *NewRequestedInput) { in.TenantID = "" },
		"learner":   func(in *NewRequestedInput) { in.LearnerGCID = " " },
		"goal":      func(in *NewRequestedInput) { in.GoalID = "" },
		"companion": func(in *NewRequestedInput) { in.CompanionID = "nope" },
	} {
		in := newValidInput()
		mutate(&in)
		if _, err := NewRequested(in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestNewRequested_EdgeValidationPropagates(t *testing.T) {
	in := newValidInput()
	in.TargetEdges = nil
	if _, err := NewRequested(in); !errors.Is(err, ErrNoTickedEdges) {
		t.Fatalf("want ErrNoTickedEdges, got %v", err)
	}
	in = newValidInput()
	in.TargetEdges[0].Key = ""
	if _, err := NewRequested(in); !errors.Is(err, ErrEdgesLackKeys) {
		t.Fatalf("want ErrEdgesLackKeys, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Status machine.
// ---------------------------------------------------------------------------

func TestMarkReady_FromRequested(t *testing.T) {
	p, _ := NewRequested(newValidInput())
	later := p.CreatedAt.Add(time.Minute)
	if err := p.MarkReady([]byte(`{"candidates":[],"proposed_test_set":{}}`), later); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if p.Status != StatusReady {
		t.Fatalf("status = %q, want ready", p.Status)
	}
	if len(p.TestSetPayload) == 0 {
		t.Fatalf("testset payload must be stored")
	}
	if !p.UpdatedAt.Equal(later) {
		t.Fatalf("UpdatedAt not touched")
	}
}

func TestMarkReady_RequiresPayload(t *testing.T) {
	p, _ := NewRequested(newValidInput())
	if err := p.MarkReady(nil, time.Now().UTC()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty payload must ErrInvalid, got %v", err)
	}
}

func TestMarkFailed_FromRequested_AndTerminalGuards(t *testing.T) {
	p, _ := NewRequested(newValidInput())
	if err := p.MarkFailed("", time.Now().UTC()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty reason must ErrInvalid, got %v", err)
	}
	if err := p.MarkFailed("qgen refused", time.Now().UTC()); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if p.Status != StatusFailed || p.FailureReason != "qgen refused" {
		t.Fatalf("failed state mis-set: %+v", p)
	}
	// Terminal: no further transitions.
	if err := p.MarkReady([]byte(`{}`), time.Now().UTC()); !errors.Is(err, ErrTerminal) {
		t.Fatalf("ready-after-failed must ErrTerminal, got %v", err)
	}
	if err := p.MarkFailed("again", time.Now().UTC()); !errors.Is(err, ErrTerminal) {
		t.Fatalf("failed-after-failed must ErrTerminal, got %v", err)
	}
}

func TestMarkComposing_ThenReady(t *testing.T) {
	p, _ := NewRequested(newValidInput())
	if err := p.MarkComposing(time.Now().UTC()); err != nil {
		t.Fatalf("MarkComposing: %v", err)
	}
	if p.Status != StatusComposing {
		t.Fatalf("status = %q, want composing", p.Status)
	}
	if err := p.MarkReady([]byte(`{"x":1}`), time.Now().UTC()); err != nil {
		t.Fatalf("MarkReady from composing: %v", err)
	}
}

func TestSoftDeleted_RejectsTransitions(t *testing.T) {
	p, _ := NewRequested(newValidInput())
	now := time.Now().UTC()
	p.SoftDelete(now)
	if p.DeletedAt == nil {
		t.Fatalf("SoftDelete must tombstone")
	}
	if err := p.MarkReady([]byte(`{}`), now); !errors.Is(err, ErrDeleted) {
		t.Fatalf("deleted must reject MarkReady, got %v", err)
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{StatusRequested, StatusComposing, StatusReady, StatusFailed} {
		if !s.Valid() {
			t.Fatalf("%q must be valid", s)
		}
	}
	if Status("bogus").Valid() {
		t.Fatalf("bogus must be invalid")
	}
}

// ---------------------------------------------------------------------------
// BuildTypePlan + RequestedCount.
// ---------------------------------------------------------------------------

func TestBuildTypePlan_DefaultsWhenUnspecified(t *testing.T) {
	plan, err := BuildTypePlan(0, 0)
	if err != nil {
		t.Fatalf("BuildTypePlan: %v", err)
	}
	if len(plan) != 2 || plan[0].QuestionType != "mcq" || plan[0].Count != DefaultMCQCount ||
		plan[1].QuestionType != "oe" || plan[1].Count != DefaultOECount {
		t.Fatalf("default plan wrong: %+v", plan)
	}
	if RequestedCount(plan) != DefaultMCQCount+DefaultOECount {
		t.Fatalf("RequestedCount = %d", RequestedCount(plan))
	}
}

func TestBuildTypePlan_ExplicitCountsAndSingleType(t *testing.T) {
	plan, err := BuildTypePlan(3, 0)
	if err != nil {
		t.Fatalf("BuildTypePlan: %v", err)
	}
	if len(plan) != 1 || plan[0].QuestionType != "mcq" || plan[0].Count != 3 {
		t.Fatalf("mcq-only plan wrong: %+v", plan)
	}
	plan, err = BuildTypePlan(0, 2)
	if err != nil {
		t.Fatalf("BuildTypePlan: %v", err)
	}
	if len(plan) != 1 || plan[0].QuestionType != "oe" || plan[0].Count != 2 {
		t.Fatalf("oe-only plan wrong: %+v", plan)
	}
}

func TestBuildTypePlan_Bounds(t *testing.T) {
	if _, err := BuildTypePlan(-1, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative mcq must ErrInvalid, got %v", err)
	}
	if _, err := BuildTypePlan(0, -3); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative oe must ErrInvalid, got %v", err)
	}
	if _, err := BuildTypePlan(MaxPerTypeCount+1, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-cap mcq must ErrInvalid, got %v", err)
	}
	if _, err := BuildTypePlan(1, MaxPerTypeCount+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-cap oe must ErrInvalid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BuildPrompt.
// ---------------------------------------------------------------------------

func TestBuildPrompt_CarriesKeysCountsAndGoalAnchor(t *testing.T) {
	plan, _ := BuildTypePlan(4, 2)
	prompt := BuildPrompt("Quadratic Equations", []string{"algebra", "roots"}, keyedEdges(), plan)
	for _, want := range []string{
		"Quadratic Equations",
		"algebra.quadratic_roots",
		"algebra.completing_square",
		"4", "2",
		"remediate",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// Deterministic: same input → same prompt.
	if again := BuildPrompt("Quadratic Equations", []string{"algebra", "roots"}, keyedEdges(), plan); again != prompt {
		t.Fatalf("prompt must be deterministic")
	}
}

func TestBuildPrompt_NoGoalTitleStillWellFormed(t *testing.T) {
	plan, _ := BuildTypePlan(1, 1)
	prompt := BuildPrompt("", nil, keyedEdges(), plan)
	if strings.TrimSpace(prompt) == "" {
		t.Fatalf("prompt must never be empty (orchestrator validate rejects empty prompt)")
	}
}

// ---------------------------------------------------------------------------
// NormalizeTitle + DedupeTargetEdges — within-request title-dedupe (CHO-2040
// loop tail). Cross-ceremony ConceptNode duplicates share a display title;
// the composed request must probe each concept ONCE (mirrors the ceremony
// mint's own "dedup within-batch by title", INSTRUCT 2026-07-04).
// ---------------------------------------------------------------------------

func TestNormalizeTitle_FoldsCaseAndWhitespace(t *testing.T) {
	cases := map[string]string{
		"Quadratic roots":       "quadratic roots",
		"  Quadratic   ROOTS  ": "quadratic roots",
		"quadratic\troots":      "quadratic roots",
		"":                      "",
		"   ":                   "",
	}
	for in, want := range cases {
		if got := NormalizeTitle(in); got != want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupeTargetEdges_CollapsesDuplicateNormalizedTitles(t *testing.T) {
	edges := []TargetEdge{
		{ConceptID: tConcept, Key: "algebra.quadratic_roots", Title: "Quadratic roots", Intent: "remediate"},
		{ConceptID: tConcept2, Key: "algebra.completing_square", Title: "Completing the square", Intent: "explore"},
		// A cross-ceremony duplicate of the first by normalized title —
		// different concept id + key, same learner-facing concept.
		{ConceptID: "77777777-7777-4777-8777-777777777777", Key: "algebra.roots_again", Title: "  quadratic   ROOTS ", Intent: "explore"},
	}
	got := DedupeTargetEdges(edges)
	if len(got) != 2 {
		t.Fatalf("deduped len = %d, want 2 (%+v)", len(got), got)
	}
	// First occurrence wins — keeps its intent + key + original-cased title.
	if got[0].Title != "Quadratic roots" || got[0].Key != "algebra.quadratic_roots" || got[0].Intent != "remediate" {
		t.Fatalf("first occurrence not preserved: %+v", got[0])
	}
	if got[1].Title != "Completing the square" {
		t.Fatalf("order not preserved: %+v", got[1])
	}
}

func TestDedupeTargetEdges_DistinctAndEmpty(t *testing.T) {
	if got := DedupeTargetEdges(nil); len(got) != 0 {
		t.Fatalf("nil → len %d, want 0", len(got))
	}
	distinct := keyedEdges()
	if got := DedupeTargetEdges(distinct); len(got) != len(distinct) {
		t.Fatalf("distinct titles must survive: got %d, want %d", len(got), len(distinct))
	}
}

func TestDedupeTargetEdges_DoesNotMutateInput(t *testing.T) {
	edges := []TargetEdge{
		{ConceptID: tConcept, Key: "k1", Title: "Roots", Intent: "remediate"},
		{ConceptID: tConcept2, Key: "k2", Title: "roots", Intent: "explore"},
	}
	if got := DedupeTargetEdges(edges); len(got) != 1 {
		t.Fatalf("deduped len = %d, want 1", len(got))
	}
	if len(edges) != 2 || edges[1].Title != "roots" {
		t.Fatalf("input slice was mutated: %+v", edges)
	}
}

func TestNewRequested_DedupesDuplicateTitleEdges(t *testing.T) {
	in := newValidInput()
	in.TargetEdges = []TargetEdge{
		{ConceptID: tConcept, Key: "algebra.quadratic_roots", Title: "Quadratic roots", Intent: "remediate"},
		{ConceptID: tConcept2, Key: "algebra.dup", Title: "QUADRATIC ROOTS", Intent: "explore"},
	}
	p, err := NewRequested(in)
	if err != nil {
		t.Fatalf("NewRequested: %v", err)
	}
	if len(p.TargetEdges) != 1 {
		t.Fatalf("stored edges = %d, want 1 (deduped by title)", len(p.TargetEdges))
	}
	if p.TargetEdges[0].Key != "algebra.quadratic_roots" {
		t.Fatalf("first occurrence must win, got %+v", p.TargetEdges[0])
	}
}

// ---------------------------------------------------------------------------
// EdgeSignature — the cross-request idempotency key (goal + sorted normalized
// deduped titles). A re-submitted identical request maps to the same key.
// ---------------------------------------------------------------------------

func TestEdgeSignature_StableAcrossOrderAndDupes(t *testing.T) {
	a := []TargetEdge{
		{ConceptID: tConcept, Key: "k1", Title: "Quadratic roots", Intent: "remediate"},
		{ConceptID: tConcept2, Key: "k2", Title: "Completing the square", Intent: "explore"},
	}
	// Same concepts, reversed order + a case/whitespace duplicate of the first.
	b := []TargetEdge{
		{ConceptID: tConcept2, Key: "k2", Title: "Completing the square", Intent: "explore"},
		{ConceptID: "77777777-7777-4777-8777-777777777777", Key: "kx", Title: "  QUADRATIC   roots ", Intent: "explore"},
		{ConceptID: tConcept, Key: "k1", Title: "Quadratic roots", Intent: "remediate"},
	}
	if EdgeSignature(tGoal, a) != EdgeSignature(tGoal, b) {
		t.Fatalf("signature must be order- and dup-independent:\n a=%q\n b=%q",
			EdgeSignature(tGoal, a), EdgeSignature(tGoal, b))
	}
	// Different goal ⇒ different signature (same edges, different owner goal).
	if EdgeSignature(tGoal, a) == EdgeSignature(tConcept, a) {
		t.Fatalf("different goal must change the signature")
	}
	// Different edge set ⇒ different signature.
	c := []TargetEdge{{ConceptID: tConcept, Key: "k1", Title: "Factoring", Intent: "remediate"}}
	if EdgeSignature(tGoal, a) == EdgeSignature(tGoal, c) {
		t.Fatalf("different edges must change the signature")
	}
}
