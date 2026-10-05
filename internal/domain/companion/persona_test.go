package companion

import (
	"strings"
	"testing"
)

func validPersonaEdit() PersonaEdit {
	return PersonaEdit{
		Tone:                 "socratic",
		HintProgression:      "ladder",
		MaxHintsBeforeReveal: 2,
		DifficultyCap:        "intermediate",
		Language:             "en",
		CitationStrictness:   "strict",
		Archetype:            "cert-focused",
		AddressStyle:         "first_name",
		InterestChips:        []string{"space", "dinosaurs"},
		GuidanceNote:         "Keep examples short and space-themed when you can.",
	}
}

func newPersonaInstance(t *testing.T) *Instance {
	t.Helper()
	inst, err := NewInstance("11111111-1111-7111-8111-111111111111",
		"22222222-2222-7222-8222-222222222222", "Ember", "math")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	return inst
}

func TestPersonaEdit_Validate_HappyPath(t *testing.T) {
	if err := validPersonaEdit().Validate(); err != nil {
		t.Fatalf("valid edit rejected: %v", err)
	}
}

func TestPersonaEdit_Validate_NoteTooLong(t *testing.T) {
	e := validPersonaEdit()
	e.GuidanceNote = strings.Repeat("x", PersonaNoteMaxLen+1)
	if err := e.Validate(); err == nil {
		t.Fatal("expected ErrPersonaNoteTooLong")
	}
}

func TestPersonaEdit_Validate_EmptyNoteAllowed(t *testing.T) {
	e := validPersonaEdit()
	e.GuidanceNote = ""
	if err := e.Validate(); err != nil {
		t.Fatalf("empty note should be allowed: %v", err)
	}
}

func TestPersonaEdit_Validate_RejectsBadEnums(t *testing.T) {
	cases := map[string]func(*PersonaEdit){
		"tone":         func(e *PersonaEdit) { e.Tone = "sarcastic" },
		"difficulty":   func(e *PersonaEdit) { e.DifficultyCap = "phd" },
		"citation":     func(e *PersonaEdit) { e.CitationStrictness = "whatever" },
		"language":     func(e *PersonaEdit) { e.Language = "kl" },
		"progression":  func(e *PersonaEdit) { e.HintProgression = "spiral" },
		"addressStyle": func(e *PersonaEdit) { e.AddressStyle = "yelling" },
	}
	for name, mut := range cases {
		e := validPersonaEdit()
		mut(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestPersonaEdit_Validate_HintsCeiling(t *testing.T) {
	e := validPersonaEdit()
	e.MaxHintsBeforeReveal = PersonaMaxHintsCeiling + 1
	if err := e.Validate(); err == nil {
		t.Fatal("expected an error for max hints over the ceiling")
	}
	e.MaxHintsBeforeReveal = -1
	if err := e.Validate(); err == nil {
		t.Fatal("expected an error for negative max hints")
	}
}

func TestPersonaEdit_Validate_InterestChips(t *testing.T) {
	e := validPersonaEdit()
	e.InterestChips = make([]string, PersonaMaxInterestChips+1)
	for i := range e.InterestChips {
		e.InterestChips[i] = "x"
	}
	if err := e.Validate(); err == nil {
		t.Fatal("expected an error for too many interest chips")
	}
	e = validPersonaEdit()
	e.InterestChips = []string{strings.Repeat("y", PersonaInterestChipMaxLen+1)}
	if err := e.Validate(); err == nil {
		t.Fatal("expected an error for an over-long interest chip")
	}
}

func TestPersonaEdit_Validate_Archetype(t *testing.T) {
	e := validPersonaEdit()
	e.Archetype = ""
	if err := e.Validate(); err == nil {
		t.Fatal("expected an error for an empty archetype")
	}
	e.Archetype = strings.Repeat("a", PersonaArchetypeMaxLen+1)
	if err := e.Validate(); err == nil {
		t.Fatal("expected an error for an over-long archetype")
	}
}

func TestInstance_ApplyPersona_WritesRulesAndBumpsVersion(t *testing.T) {
	inst := newPersonaInstance(t)
	if inst.PersonaVersion != 0 {
		t.Fatalf("fresh instance should be persona version 0, got %d", inst.PersonaVersion)
	}
	if err := inst.ApplyPersona(validPersonaEdit()); err != nil {
		t.Fatalf("ApplyPersona: %v", err)
	}
	if inst.PersonaVersion != 1 {
		t.Errorf("expected version bumped to 1, got %d", inst.PersonaVersion)
	}
	if inst.ConfiguredRules[RuleTone] != "socratic" {
		t.Errorf("tone not written: %q", inst.ConfiguredRules[RuleTone])
	}
	if inst.ConfiguredRules[RuleMaxHintsBeforeReveal] != "2" {
		t.Errorf("max hints not written: %q", inst.ConfiguredRules[RuleMaxHintsBeforeReveal])
	}
	if inst.ConfiguredRules[RuleInterestChips] != "space,dinosaurs" {
		t.Errorf("interest chips not written: %q", inst.ConfiguredRules[RuleInterestChips])
	}
	if inst.GuidanceNote == "" {
		t.Error("guidance note not written")
	}
}

func TestInstance_ApplyPersona_InvalidLeavesInstanceUntouched(t *testing.T) {
	inst := newPersonaInstance(t)
	bad := validPersonaEdit()
	bad.Tone = "sarcastic"
	if err := inst.ApplyPersona(bad); err == nil {
		t.Fatal("expected ApplyPersona to reject an invalid edit")
	}
	if inst.PersonaVersion != 0 {
		t.Errorf("version must not bump on a rejected edit, got %d", inst.PersonaVersion)
	}
	if len(inst.ConfiguredRules) != 0 {
		t.Errorf("no rules should be written on a rejected edit, got %v", inst.ConfiguredRules)
	}
}

func TestInstance_Persona_Projection(t *testing.T) {
	inst := newPersonaInstance(t)
	_ = inst.ApplyPersona(validPersonaEdit())
	v := inst.Persona()
	if v.Tone != "socratic" || v.DifficultyCap != "intermediate" || v.Language != "en" {
		t.Errorf("projection knobs wrong: %+v", v)
	}
	if v.Archetype != "cert-focused" {
		t.Errorf("projection archetype wrong: %q", v.Archetype)
	}
	if len(v.InterestChips) != 2 || v.InterestChips[0] != "space" {
		t.Errorf("projection interest chips wrong: %v", v.InterestChips)
	}
	if v.Version != 1 {
		t.Errorf("projection version wrong: %d", v.Version)
	}
}
