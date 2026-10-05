// persona.go — the learner-designed Persona sheet (CHO-2015, ADR-219 D2).
//
// The Persona is the "who your Companion IS" surface: bounded typed knobs
// (tone / hint policy / difficulty cap / citation strictness / language /
// address style) + archetype + interest chips + ONE fenced free-text
// guidance note (≤280 chars — the single sanctioned exception to ADR-205's
// zero-free-form rule). The typed knobs are persisted in the SAME
// companion_instances.configured_rules JSONB the agent already composes from
// (keys mirror companion_config_server.go mapConfiguredRules — no drift); the
// guidance note rides its dedicated GuidanceNote column (Armor-screened at
// save + runtime, injected into a locked-frame agent segment).
//
// This file is PURE DOMAIN: it validates the bounded grammar + applies the
// edit + bumps the version. Model Armor screening of the note (a SAFETY gate,
// not a shape gate) happens at the service/handler layer BEFORE ApplyPersona —
// the domain guarantees the SHAPE is legal; Armor guarantees the note is SAFE.
package companion

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// configured_rules keys the Persona sheet writes. These MIRROR the reader in
// services/chora-consumption/internal/adapter/grpc/companion_config_server.go
// (mapConfiguredRules) — the single source of truth for the stored shape.
const (
	RuleTone                 = "tone"
	RuleHintProgression      = "hint_progression"
	RuleMaxHintsBeforeReveal = "max_hints_before_reveal"
	RuleDifficultyCap        = "difficulty_cap"
	RuleLanguage             = "language"
	RuleCitationStrictness   = "citation_strictness"
	RuleLearnerPersona       = "learner_persona" // archetype
	RuleAddressStyle         = "address_style"   // NEW (ADR-219 D2)
	RuleInterestChips        = "interest_chips"  // NEW (comma-joined)
)

// Bounded grammar limits (ADR-219 D2).
const (
	PersonaNoteMaxLen         = 280 // the fenced free-text note ceiling
	PersonaMaxHintsCeiling    = 5
	PersonaMaxInterestChips   = 8
	PersonaInterestChipMaxLen = 32
	PersonaArchetypeMaxLen    = 64
)

// Allowed enum sets — bounded controls (the note is the ONLY free-text field).
var (
	personaTones            = map[string]bool{"socratic": true, "direct": true, "encouraging": true}
	personaDifficulties     = map[string]bool{"foundation": true, "intermediate": true, "advanced": true}
	personaCitations        = map[string]bool{"strict": true, "lenient": true}
	personaLanguages        = map[string]bool{"en": true, "zh": true, "ms": true} // platform i18n locales
	personaHintProgressions = map[string]bool{"ladder": true, "uniform": true}
	personaAddressStyles    = map[string]bool{"first_name": true, "nickname": true, "formal": true}
)

// Sentinel errors (mapped to 422 at the HTTP edge).
var (
	ErrPersonaBadTone         = errors.New("companion.persona: tone not in {socratic,direct,encouraging}")
	ErrPersonaBadDifficulty   = errors.New("companion.persona: difficulty_cap not in {foundation,intermediate,advanced}")
	ErrPersonaBadCitation     = errors.New("companion.persona: citation_strictness not in {strict,lenient}")
	ErrPersonaBadLanguage     = errors.New("companion.persona: language not a supported locale {en,zh,ms}")
	ErrPersonaBadProgression  = errors.New("companion.persona: hint_progression not in {ladder,uniform}")
	ErrPersonaBadAddressStyle = errors.New("companion.persona: address_style not in {first_name,nickname,formal}")
	ErrPersonaBadHints        = errors.New("companion.persona: max_hints_before_reveal out of range [0,5]")
	ErrPersonaArchetype       = errors.New("companion.persona: archetype required (max 64 chars)")
	ErrPersonaNoteTooLong     = errors.New("companion.persona: guidance note exceeds 280 chars")
	ErrPersonaTooManyChips    = errors.New("companion.persona: too many interest chips (max 8)")
	ErrPersonaChipTooLong     = errors.New("companion.persona: interest chip exceeds 32 chars")
)

// PersonaEdit is a learner's full Persona-sheet submission (a full replace of
// the editable persona surface — all typed knobs + the optional note/chips).
type PersonaEdit struct {
	Tone                 string
	HintProgression      string
	MaxHintsBeforeReveal int
	DifficultyCap        string
	Language             string
	CitationStrictness   string
	Archetype            string // learner_persona
	AddressStyle         string
	InterestChips        []string
	GuidanceNote         string // fenced free-text; may be empty; Armor-screened at the service layer
}

// Validate checks the whole edit against the bounded grammar. Fail-loud with a
// named error; the note's LENGTH is checked here, its SAFETY at the service
// layer (Model Armor) before ApplyPersona runs.
func (e PersonaEdit) Validate() error {
	if !personaTones[e.Tone] {
		return ErrPersonaBadTone
	}
	if !personaHintProgressions[e.HintProgression] {
		return ErrPersonaBadProgression
	}
	if e.MaxHintsBeforeReveal < 0 || e.MaxHintsBeforeReveal > PersonaMaxHintsCeiling {
		return ErrPersonaBadHints
	}
	if !personaDifficulties[e.DifficultyCap] {
		return ErrPersonaBadDifficulty
	}
	if !personaLanguages[e.Language] {
		return ErrPersonaBadLanguage
	}
	if !personaCitations[e.CitationStrictness] {
		return ErrPersonaBadCitation
	}
	if !personaAddressStyles[e.AddressStyle] {
		return ErrPersonaBadAddressStyle
	}
	arch := strings.TrimSpace(e.Archetype)
	if arch == "" || len(arch) > PersonaArchetypeMaxLen {
		return ErrPersonaArchetype
	}
	if len(e.InterestChips) > PersonaMaxInterestChips {
		return ErrPersonaTooManyChips
	}
	for _, c := range e.InterestChips {
		if len(strings.TrimSpace(c)) > PersonaInterestChipMaxLen {
			return ErrPersonaChipTooLong
		}
	}
	if len([]rune(e.GuidanceNote)) > PersonaNoteMaxLen {
		return ErrPersonaNoteTooLong
	}
	return nil
}

// cleanChips trims + drops empty interest chips, preserving order.
func cleanChips(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// ApplyPersona validates the edit, writes the typed knobs into ConfiguredRules,
// sets the fenced GuidanceNote, and bumps PersonaVersion. It is ATOMIC on
// failure: an invalid edit mutates NOTHING (validation runs before any write).
// The caller MUST have Armor-screened the note first (SAFETY); this enforces
// the bounded SHAPE.
func (inst *Instance) ApplyPersona(e PersonaEdit) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if inst.ConfiguredRules == nil {
		inst.ConfiguredRules = map[string]string{}
	}
	inst.ConfiguredRules[RuleTone] = e.Tone
	inst.ConfiguredRules[RuleHintProgression] = e.HintProgression
	inst.ConfiguredRules[RuleMaxHintsBeforeReveal] = strconv.Itoa(e.MaxHintsBeforeReveal)
	inst.ConfiguredRules[RuleDifficultyCap] = e.DifficultyCap
	inst.ConfiguredRules[RuleLanguage] = e.Language
	inst.ConfiguredRules[RuleCitationStrictness] = e.CitationStrictness
	inst.ConfiguredRules[RuleLearnerPersona] = strings.TrimSpace(e.Archetype)
	inst.ConfiguredRules[RuleAddressStyle] = e.AddressStyle
	inst.ConfiguredRules[RuleInterestChips] = strings.Join(cleanChips(e.InterestChips), ",")
	inst.GuidanceNote = strings.TrimSpace(e.GuidanceNote)
	inst.PersonaVersion++
	inst.UpdatedAt = time.Now().UTC()
	return nil
}

// PersonaView is the read projection returned by GET .../persona — the current
// editable persona surface, decoded back out of ConfiguredRules + GuidanceNote.
type PersonaView struct {
	Tone                 string
	HintProgression      string
	MaxHintsBeforeReveal int
	DifficultyCap        string
	Language             string
	CitationStrictness   string
	Archetype            string
	AddressStyle         string
	InterestChips        []string
	GuidanceNote         string
	Version              int
}

// Persona projects the instance's stored config into a PersonaView, applying
// the SAME defaults the agent's mapConfiguredRules uses so a never-edited
// Companion returns a coherent (default) sheet rather than blanks.
func (inst *Instance) Persona() PersonaView {
	get := func(key, def string) string {
		if v, ok := inst.ConfiguredRules[key]; ok && strings.TrimSpace(v) != "" {
			return v
		}
		return def
	}
	maxHints := 3
	if n, err := strconv.Atoi(get(RuleMaxHintsBeforeReveal, "")); err == nil && n >= 0 {
		maxHints = n
	}
	var chips []string
	if raw := get(RuleInterestChips, ""); raw != "" {
		chips = cleanChips(strings.Split(raw, ","))
	}
	return PersonaView{
		Tone:                 get(RuleTone, "encouraging"),
		HintProgression:      get(RuleHintProgression, "ladder"),
		MaxHintsBeforeReveal: maxHints,
		DifficultyCap:        get(RuleDifficultyCap, "intermediate"),
		Language:             get(RuleLanguage, "en"),
		CitationStrictness:   get(RuleCitationStrictness, "strict"),
		Archetype:            get(RuleLearnerPersona, ""),
		AddressStyle:         get(RuleAddressStyle, "first_name"),
		InterestChips:        chips,
		GuidanceNote:         inst.GuidanceNote,
		Version:              inst.PersonaVersion,
	}
}
