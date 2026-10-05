// ritual_reserved_fields_test.go — RED-first for ADR-257 N1: the reserved
// inter-step contract fields on RitualStep and the reserved port columns on
// CatalogEntry.
//
// ADR-257 reserves a shape; it does NOT switch chaining on (that ships with the
// per-step ToolFilter enforcement gate, ADR-257 §5, and the connectors stay dark
// per owner ruling R22). So the acceptance here is deliberately two-sided:
//
//   - an OLD-shape revision, persisted before these fields existed, must still
//     unmarshal, validate and publish unchanged; and
//   - a reserved field that is SET must be REFUSED fail-loud, never accepted and
//     silently ignored, because a stored `from_step_id` the runner does not read
//     is a lie about what the ritual does.
//
// These reference symbols that do NOT exist yet (RitualStepKind, StepKindSkill,
// ErrRitualStepKindNotV1, EffectivePort, …) so the package fails to COMPILE,
// which is the RED signal.
package companion

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Wire-shape guard. This is the load-bearing one.
// ---------------------------------------------------------------------------

// TestRitualStep_PersistedWireShapeIsPinned pins the EXACT keys the pg repo
// writes into companion_ritual_revisions.steps (json.Marshal of []RitualStep,
// companion_ritual_repo.go:162). Two facts this locks down, both measured:
//
//  1. The live shape is Go field names, "SkillKey" / "Params" — NOT the
//     "[{skill_key, params}]" the 0071 migration comment claims. The comment is
//     wrong; the applied migration is frozen and is not edited to match.
//  2. Adding reserved fields must NOT widen the shape for a step that sets
//     none of them, or every existing revision's stored bytes stop round-tripping.
func TestRitualStep_PersistedWireShapeIsPinned(t *testing.T) {
	b, err := json.Marshal([]RitualStep{{SkillKey: "quiz_me", Params: map[string]any{"count": "3"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	const want = `[{"SkillKey":"quiz_me","Params":{"count":"3"}}]`
	if got != want {
		t.Errorf("persisted step shape drifted.\n got: %s\nwant: %s\n"+
			"Renaming these keys silently breaks every stored revision (see the sibling test).", got, want)
	}
}

// TestRitualStep_SnakeCaseKeysDoNotRoundTrip is the reason the guard above
// exists, stated as an executable fact rather than a comment: encoding/json
// matches keys case-insensitively, so "params" reaches Params, but "skill_key"
// does NOT reach SkillKey (the underscore defeats the fold). A step read back
// with an empty SkillKey fails ValidateSteps as not-equipped, so a "tidy the
// tags to snake_case" refactor would make every published ritual unrunnable
// while every unit test still passed.
func TestRitualStep_SnakeCaseKeysDoNotRoundTrip(t *testing.T) {
	var steps []RitualStep
	if err := json.Unmarshal([]byte(`[{"skill_key":"quiz_me","params":{"count":"3"}}]`), &steps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if steps[0].SkillKey != "" {
		t.Errorf("snake_case skill_key unexpectedly bound to SkillKey (%q); "+
			"if this now works the wire shape changed and the pinning test above must be re-derived", steps[0].SkillKey)
	}
}

// TestRitualStep_LegacyJSONUnmarshalsWithReservedFieldsZero — a revision stored
// before ADR-257 reads back with the reserved fields at their zero values.
func TestRitualStep_LegacyJSONUnmarshalsWithReservedFieldsZero(t *testing.T) {
	var steps []RitualStep
	legacy := `[{"SkillKey":"explain_anew","Params":{"style":"analogy"}}]`
	if err := json.Unmarshal([]byte(legacy), &steps); err != nil {
		t.Fatalf("unmarshal legacy revision: %v", err)
	}
	s := steps[0]
	if s.SkillKey != "explain_anew" {
		t.Fatalf("SkillKey = %q, want explain_anew", s.SkillKey)
	}
	if s.StepID != "" || s.StepKind != "" || s.FromStepID != "" || s.Actor != "" || s.CapabilityScope != "" {
		t.Errorf("legacy step should carry zero reserved fields, got %+v", s)
	}
}

// ---------------------------------------------------------------------------
// Both-shape tolerance: the acceptance clause for this row.
// ---------------------------------------------------------------------------

// TestPublish_LegacyShapeStillPublishes — the B3 acceptance clause. Steps that
// set only the two pre-ADR-257 fields publish exactly as before.
func TestPublish_LegacyShapeStillPublishes(t *testing.T) {
	r, err := NewRitual("t-1", "comp-1", "Morning Review", TriggerManual, SinkChat)
	if err != nil {
		t.Fatalf("NewRitual: %v", err)
	}
	legacy := []RitualStep{
		{SkillKey: "explain_anew", Params: map[string]any{"target": "019f26d5-e707-745c-891f-44a1c7694b75"}},
		{SkillKey: "map_sight"},
	}
	rev, err := r.Publish(legacy, "allow", ritualTestCaps(), fixedClock())
	if err != nil {
		t.Fatalf("legacy-shape publish must still succeed, got: %v", err)
	}
	if rev.RevisionNo != 1 || len(rev.Steps) != 2 {
		t.Fatalf("revision = %+v, want 1 revision with 2 steps", rev)
	}
	if rev.Steps[0].StepKind != "" {
		t.Errorf("publish must not backfill StepKind onto a legacy step, got %q", rev.Steps[0].StepKind)
	}
}

// TestPublish_ReservedFieldsRoundTripThroughRevision — a step_id and an explicit
// skill kind survive into the append-only revision snapshot.
func TestPublish_ReservedFieldsRoundTripThroughRevision(t *testing.T) {
	r, _ := NewRitual("t-1", "comp-1", "Morning Review", TriggerManual, SinkChat)
	steps := []RitualStep{{
		StepID:   "019f26d5-e707-745c-891f-44a1c7694b75",
		StepKind: StepKindSkill,
		SkillKey: "quiz_me",
	}}
	rev, err := r.Publish(steps, "allow", ritualTestCaps(), fixedClock())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if rev.Steps[0].StepID != "019f26d5-e707-745c-891f-44a1c7694b75" {
		t.Errorf("StepID lost through Publish: %+v", rev.Steps[0])
	}
	if rev.Steps[0].StepKind != StepKindSkill {
		t.Errorf("StepKind lost through Publish: %+v", rev.Steps[0])
	}
}

// ---------------------------------------------------------------------------
// Step kinds. Mirrors the trigger idiom (isKnownTrigger / isV1RunnableTrigger).
// ---------------------------------------------------------------------------

func TestStepKind_AbsentAndSkillAreV1Runnable(t *testing.T) {
	for _, k := range []RitualStepKind{"", StepKindSkill} {
		if !isKnownStepKind(k) {
			t.Errorf("step kind %q must be known", k)
		}
		if !isV1RunnableStepKind(k) {
			t.Errorf("step kind %q must be v1-runnable", k)
		}
	}
}

// TestValidateSteps_RefusesReservedStepKinds — roster_peer needs the peer
// executor and the ADR-254 D1 exception ruling (ADR-257 §8); sink is reserved by
// owner ruling R14 until an ADR rules on partial writes. Both are KNOWN values
// that refuse, exactly as schedule/on_event triggers do.
func TestValidateSteps_RefusesReservedStepKinds(t *testing.T) {
	r, _ := NewRitual("t-1", "comp-1", "R", TriggerManual, SinkChat)
	for _, k := range []RitualStepKind{StepKindRosterPeer, StepKindSink} {
		if !isKnownStepKind(k) {
			t.Errorf("reserved step kind %q must be KNOWN (it is a real enum value), not unknown", k)
		}
		if isV1RunnableStepKind(k) {
			t.Errorf("reserved step kind %q must not be v1-runnable", k)
		}
		err := r.ValidateSteps([]RitualStep{{SkillKey: "quiz_me", StepKind: k}}, ritualTestCaps())
		if !errors.Is(err, ErrRitualStepKindNotV1) {
			t.Errorf("step kind %q: err = %v, want ErrRitualStepKindNotV1", k, err)
		}
	}
}

func TestValidateSteps_RefusesUnknownStepKind(t *testing.T) {
	r, _ := NewRitual("t-1", "comp-1", "R", TriggerManual, SinkChat)
	err := r.ValidateSteps([]RitualStep{{SkillKey: "quiz_me", StepKind: "wizard"}}, ritualTestCaps())
	if !errors.Is(err, ErrRitualStepKindUnknown) {
		t.Errorf("err = %v, want ErrRitualStepKindUnknown", err)
	}
}

// ---------------------------------------------------------------------------
// Reserved fields refuse rather than accept-and-ignore.
// ---------------------------------------------------------------------------

// TestValidateSteps_RefusesReservedFieldsUntilTheyAreHonoured — the fail-loud
// half of "reserved". Accepting a from_step_id the runner never reads would
// store a chain claim that does not happen.
func TestValidateSteps_RefusesReservedFieldsUntilTheyAreHonoured(t *testing.T) {
	r, _ := NewRitual("t-1", "comp-1", "R", TriggerManual, SinkChat)
	cases := []struct {
		name string
		step RitualStep
	}{
		{"from_step_id", RitualStep{SkillKey: "quiz_me", FromStepID: "019f26d5-e707-745c-891f-44a1c7694b75"}},
		{"actor", RitualStep{SkillKey: "quiz_me", Actor: "019f26d5-e707-745c-891f-44a1c7694b76"}},
		{"capability_scope", RitualStep{SkillKey: "quiz_me", CapabilityScope: "companion.read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.ValidateSteps([]RitualStep{tc.step}, ritualTestCaps())
			if !errors.Is(err, ErrRitualStepFieldReserved) {
				t.Errorf("err = %v, want ErrRitualStepFieldReserved", err)
			}
			if err != nil && !strings.Contains(err.Error(), tc.name) {
				t.Errorf("error must name the offending field %q, got: %v", tc.name, err)
			}
		})
	}
}

// TestPublish_RefusesReservedFields — the guard runs on the publish path too,
// not only on the bare validator (Publish delegates, and the run-time re-check
// shares it).
func TestPublish_RefusesReservedFields(t *testing.T) {
	r, _ := NewRitual("t-1", "comp-1", "R", TriggerManual, SinkChat)
	_, err := r.Publish([]RitualStep{{SkillKey: "quiz_me", FromStepID: "x"}}, "allow", ritualTestCaps(), fixedClock())
	if !errors.Is(err, ErrRitualStepFieldReserved) {
		t.Errorf("Publish err = %v, want ErrRitualStepFieldReserved", err)
	}
}

// ---------------------------------------------------------------------------
// Catalogue ports (ADR-257 D2). Reserved: carried, closed-set, unread.
// ---------------------------------------------------------------------------

// TestEffectivePort_AbsentMeansNone — the pg column defaults to 'none' but a Go
// zero value is "", so the two must agree at the seam.
func TestEffectivePort_AbsentMeansNone(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if got := EffectivePort(in); got != PortNone {
			t.Errorf("EffectivePort(%q) = %q, want %q", in, got, PortNone)
		}
	}
	if got := EffectivePort(PortConceptRef); got != PortConceptRef {
		t.Errorf("EffectivePort(%q) = %q, want unchanged", PortConceptRef, got)
	}
}

// TestIsKnownPort_ClosedSet — the vocabulary is a superset of the five param
// kinds (skill_params.go), so the composer has ONE type system. An unknown port
// is refused rather than guessed, mirroring the FE's own refuse-to-render rule.
func TestIsKnownPort_ClosedSet(t *testing.T) {
	known := []string{PortNone, PortText, PortConceptRef, PortGrowthEdgeRef, PortAtomRef, PortQuestionRef, PortSourceRef}
	for _, p := range known {
		if !IsKnownPort(p) {
			t.Errorf("port %q must be known", p)
		}
	}
	for _, p := range []string{"", "concept", "vector_ref", "enum"} {
		if IsKnownPort(p) {
			t.Errorf("port %q must NOT be known", p)
		}
	}
}

// TestCatalogEntry_CarriesPortsUnread — the fields exist on the projection so the
// column can be selected, and default to none.
func TestCatalogEntry_CarriesPortsUnread(t *testing.T) {
	e := CatalogEntry{SkillKey: "map_sight", Consumes: PortNone, Produces: PortConceptRef}
	if EffectivePort(e.Consumes) != PortNone || EffectivePort(e.Produces) != PortConceptRef {
		t.Errorf("ports did not survive the projection: %+v", e)
	}
	var zero CatalogEntry
	if EffectivePort(zero.Consumes) != PortNone || EffectivePort(zero.Produces) != PortNone {
		t.Errorf("zero-value CatalogEntry must read as none/none, got %q/%q", zero.Consumes, zero.Produces)
	}
}
