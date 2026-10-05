// companion_persona_handlers.go — CHO-2015 (ADR-219 D2): the learner-designed
// Persona sheet BFF handlers under /v1/me/companions/{companionId}/persona.
// Clean camelCase JSON (no envelope) — same wire convention as the Grimoire
// Rituals designer (companion_ritual_handlers.go), since the Persona tab lives
// in the same 3-tab Grimoire shell and is reached via the CompanionBridge
// classify pass-through.
//
//	GET /v1/me/companions/{fid}/persona  → current editable view
//	PUT /v1/me/companions/{fid}/persona  → apply a full Persona edit
//
// SAFETY: the single free-text field (guidanceNote) is the one sanctioned
// exception to ADR-205's zero-free-form rule, so PUT screens it via Cloud
// Model Armor (agent_id companion_persona → strict) BEFORE the domain applies
// it. A Block → 422 and NOTHING is persisted. The note enters a kid-facing
// system prompt, so a note is NEVER persisted unscreened: if the guardrail is
// unwired and the note is non-empty the handler fails loud (503). An empty
// note is trivially safe and skips the screen.
//
// The domain (companion.PersonaService) guarantees the bounded SHAPE +
// ownership; this adapter authenticates, decodes, screens, and delegates.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	consumptionmodelarmor "github.com/apollo-chora/chora-consumption/internal/adapter/modelarmor"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---- DTOs (camelCase) ------------------------------------------------------

// personaViewDTO is the read/write projection. Version is server-owned
// (monotonic, bumped on each save) — ignored on PUT input.
type personaViewDTO struct {
	Tone                 string   `json:"tone"`
	HintProgression      string   `json:"hintProgression"`
	MaxHintsBeforeReveal int      `json:"maxHintsBeforeReveal"`
	DifficultyCap        string   `json:"difficultyCap"`
	Language             string   `json:"language"`
	CitationStrictness   string   `json:"citationStrictness"`
	Archetype            string   `json:"archetype"`
	AddressStyle         string   `json:"addressStyle"`
	InterestChips        []string `json:"interestChips"`
	GuidanceNote         string   `json:"guidanceNote"`
	Version              int      `json:"version"`
}

// updatePersonaReq is the PUT body — the full editable Persona surface
// (a complete replace). Version is NOT accepted from the client.
type updatePersonaReq struct {
	Tone                 string   `json:"tone"`
	HintProgression      string   `json:"hintProgression"`
	MaxHintsBeforeReveal int      `json:"maxHintsBeforeReveal"`
	DifficultyCap        string   `json:"difficultyCap"`
	Language             string   `json:"language"`
	CitationStrictness   string   `json:"citationStrictness"`
	Archetype            string   `json:"archetype"`
	AddressStyle         string   `json:"addressStyle"`
	InterestChips        []string `json:"interestChips"`
	GuidanceNote         string   `json:"guidanceNote"`
}

func (req updatePersonaReq) toDomain() companion.PersonaEdit {
	return companion.PersonaEdit{
		Tone:                 req.Tone,
		HintProgression:      req.HintProgression,
		MaxHintsBeforeReveal: req.MaxHintsBeforeReveal,
		DifficultyCap:        req.DifficultyCap,
		Language:             req.Language,
		CitationStrictness:   req.CitationStrictness,
		Archetype:            req.Archetype,
		AddressStyle:         req.AddressStyle,
		InterestChips:        req.InterestChips,
		GuidanceNote:         req.GuidanceNote,
	}
}

func toPersonaViewDTO(v companion.PersonaView) personaViewDTO {
	chips := v.InterestChips
	if chips == nil {
		chips = []string{}
	}
	return personaViewDTO{
		Tone:                 v.Tone,
		HintProgression:      v.HintProgression,
		MaxHintsBeforeReveal: v.MaxHintsBeforeReveal,
		DifficultyCap:        v.DifficultyCap,
		Language:             v.Language,
		CitationStrictness:   v.CitationStrictness,
		Archetype:            v.Archetype,
		AddressStyle:         v.AddressStyle,
		InterestChips:        chips,
		GuidanceNote:         v.GuidanceNote,
		Version:              v.Version,
	}
}

// ---- handlers --------------------------------------------------------------

func (s *Server) handleGetPersona(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Persona == nil {
		writeError(w, http.StatusServiceUnavailable, "PERSONA_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	view, err := s.Persona.GetPersona(rlsCtx(r, tenantID, gcid), companionID, gcid)
	if err != nil {
		mapPersonaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPersonaViewDTO(view))
}

func (s *Server) handleUpdatePersona(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Persona == nil {
		writeError(w, http.StatusServiceUnavailable, "PERSONA_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req updatePersonaReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	edit := req.toDomain()
	// Fast shape reject (422) BEFORE spending a Model Armor call on an edit
	// the bounded grammar already refuses (also bounds the note length).
	if err := edit.Validate(); err != nil {
		mapPersonaError(w, err)
		return
	}
	// SAFETY gate — screen the free-text note before it is persisted or ever
	// composed into the Companion system prompt. An empty note is trivially
	// safe and skips the screen.
	if strings.TrimSpace(edit.GuidanceNote) != "" {
		if !s.screenPersonaNote(w, r, tenantID, gcid, edit.GuidanceNote) {
			return
		}
	}
	view, err := s.Persona.UpdatePersona(rlsCtx(r, tenantID, gcid), companionID, gcid, edit)
	if err != nil {
		mapPersonaError(w, err)
		return
	}
	// B6: announce the save (best-effort — see emitPersonaUpdated).
	s.emitPersonaUpdated(r, tenantID, gcid, companionID, view)
	writeJSON(w, http.StatusOK, toPersonaViewDTO(view))
}

// topicCompanionPersonaUpdated is the CHO-2015 (ADR-219 D2) persona-save event.
// Registered BINARY (protomarshal.encodeCompanionPersonaUpdated) — a JSON
// fallback would dead-letter. Deploy-time: register the flat schema + create
// the topic before the consumption image carrying this emit goes live.
const topicCompanionPersonaUpdated = "chora.consumption.companion.persona_updated.v1"

// emitPersonaUpdated publishes chora.consumption.companion.persona_updated.v1 via
// the loadout outbox. BEST-EFFORT: the event is consumer-less audit / prompt-
// cache-invalidation signal, so a missing outbox (dev/tests) or a failed
// enqueue is logged loud but NEVER fails the learner's persona save. PRIVACY:
// the free-text guidance note is NOT emitted — only has_guidance_note.
func (s *Server) emitPersonaUpdated(r *http.Request, tenantID, gcid, companionID string, view companion.PersonaView) {
	if s.LoadoutEvents == nil {
		log.Printf("consumption: persona_updated NOT emitted (no outbox wired) — companion=%s v%d", companionID, view.Version)
		return
	}
	now := time.Now().UTC()
	env := companion.LoadoutEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("persona:%s:%d", companionID, view.Version),
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    tracing.EnsureTraceparent(r.Header.Get("traceparent")),
		Tracestate:     r.Header.Get("tracestate"),
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"companion_id":       companionID,
		"owner_gcid":         gcid,
		"persona_version":    view.Version,
		"tone":               view.Tone,
		"difficulty_cap":     view.DifficultyCap,
		"address_style":      view.AddressStyle,
		"has_guidance_note":  strings.TrimSpace(view.GuidanceNote) != "",
		"updated_at":         now,
		"chora_companion_id": companionID,
	}
	if err := s.LoadoutEvents.PublishLoadoutEvent(rlsCtx(r, tenantID, gcid), topicCompanionPersonaUpdated, payload, env); err != nil {
		log.Printf("consumption: persona_updated emit failed (non-fatal) — companion=%s v%d: %v", companionID, view.Version, err)
	}
}

// screenPersonaNote runs the guidance note through Cloud Model Armor. It
// returns true when the caller may proceed to persist, and false (having
// already written the error response) when the note is blocked, screening
// failed, or the guardrail is unwired. A NON-EMPTY note with no guardrail is
// a fail-loud 503 — a note bound for a kid-facing system prompt is never
// persisted unscreened.
func (s *Server) screenPersonaNote(w http.ResponseWriter, r *http.Request, tenantID, gcid, note string) bool {
	if s.PersonaGuardrail == nil {
		writeError(w, http.StatusServiceUnavailable, "PERSONA_GUARDRAIL_NOT_WIRED",
			"note screening unavailable — refusing to persist an unscreened note")
		return false
	}
	verdict, err := s.PersonaGuardrail.Screen(rlsCtx(r, tenantID, gcid), consumptionmodelarmor.ScreenRequest{
		TenantID: tenantID,
		GCID:     gcid,
		AgentID:  consumptionmodelarmor.AgentIDCompanionPersona,
		Content:  note,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "PERSONA_SCREEN_FAILED", err.Error())
		return false
	}
	if verdict.Verdict == consumptionmodelarmor.VerdictBlocked {
		reason := verdict.Reason
		if reason == "" {
			reason = "guidance note blocked by content safety"
		}
		writeError(w, http.StatusUnprocessableEntity, "PERSONA_NOTE_BLOCKED", reason)
		return false
	}
	// VerdictFlagged (INSPECT_ONLY) → proceed; the audit event is B6.
	return true
}

// mapPersonaError converts a persona domain error to an HTTP status. The
// bounded-grammar sentinels → 422; a missing/non-owned companion → 404;
// anything else (repo failure) → 500 fail-loud.
func mapPersonaError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrInstanceNotFound):
		writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", err.Error())
	case isPersonaShapeError(err):
		writeError(w, http.StatusUnprocessableEntity, "PERSONA_INVALID", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "PERSONA_ERROR", err.Error())
	}
}

// isPersonaShapeError reports whether err is one of the bounded-grammar
// validation sentinels from the persona aggregate (all → 422).
func isPersonaShapeError(err error) bool {
	for _, sentinel := range []error{
		companion.ErrPersonaBadTone,
		companion.ErrPersonaBadDifficulty,
		companion.ErrPersonaBadCitation,
		companion.ErrPersonaBadLanguage,
		companion.ErrPersonaBadProgression,
		companion.ErrPersonaBadAddressStyle,
		companion.ErrPersonaBadHints,
		companion.ErrPersonaArchetype,
		companion.ErrPersonaNoteTooLong,
		companion.ErrPersonaTooManyChips,
		companion.ErrPersonaChipTooLong,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
