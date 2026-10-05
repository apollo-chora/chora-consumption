// companion_growth_handlers.go — REST handlers for the ADR-149 Companion
// Growth axis. Mounted under /v1/me/companions/{id}/... per the existing
// learner-self surface convention.
//
// Auth model: X-Tenant-Id + gcid headers (same pattern as the existing
// companion_instance_handlers.go). The gateway BFF strips the Bearer at the
// edge + stamps tenant_id + gcid headers per S3.6 mesh trust convention.
//
// Endpoints:
//
//	GET    /v1/me/companions/{id}/growth             → GetCompanionGrowth
//	POST   /v1/me/companions/{id}/reveal             → RevealBreed (CHO-2229)
//	POST   /v1/me/companions/{id}/hatch              → HatchEgg
//	POST   /v1/me/companions/{id}/resonance          → PickResonantConcept (R3-1)
//	POST   /v1/me/companions/{id}/source-revelation  → TriggerSourceRevelation
//	GET    /v1/me/companions/{id}/growth-events      → ListGrowthEvents
//
// AwardExp + PreviewEggOdds are NOT exposed via REST — internal-only (the
// gRPC CompanionGrowth service is the canonical interface for them).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// GrowthServer is the dependency container the handlers depend on. The Go
// server.Routes() wires this onto Server when chora-consumption boots with
// a real Repository + OutboxPort + BreedDistributionProvider.
type GrowthServer interface {
	GetCompanionGrowth(ctx context.Context, tenantID, companionID, callerGCID string) (*growth.State, error)
	RevealBreed(ctx context.Context, in growth.RevealBreedInput) (*growth.RevealBreedResponse, error)
	HatchEgg(ctx context.Context, in growth.HatchEggInput) (*growth.HatchEggResponse, error)
	PickResonantConcept(ctx context.Context, in growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error)
	TriggerSourceRevelation(ctx context.Context, in growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error)
	ListGrowthEvents(ctx context.Context, in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error)
}

// growthServiceAdapter satisfies GrowthServer by delegating to a
// growth.Service. The handler interface decouples the HTTP layer from the
// concrete domain.Service so tests can supply a fake.
type growthServiceAdapter struct {
	svc *growth.Service
}

// NewGrowthServiceAdapter wraps a domain growth.Service as a GrowthServer.
func NewGrowthServiceAdapter(svc *growth.Service) GrowthServer {
	return &growthServiceAdapter{svc: svc}
}

func (a *growthServiceAdapter) GetCompanionGrowth(ctx context.Context, tenantID, companionID, callerGCID string) (*growth.State, error) {
	return a.svc.GetCompanionGrowth(ctx, tenantID, companionID, callerGCID)
}

func (a *growthServiceAdapter) HatchEgg(ctx context.Context, in growth.HatchEggInput) (*growth.HatchEggResponse, error) {
	return a.svc.HatchEgg(ctx, in)
}

func (a *growthServiceAdapter) RevealBreed(ctx context.Context, in growth.RevealBreedInput) (*growth.RevealBreedResponse, error) {
	return a.svc.RevealBreed(ctx, in)
}

func (a *growthServiceAdapter) PickResonantConcept(ctx context.Context, in growth.PickResonantConceptInput) (*growth.PickResonantConceptResponse, error) {
	return a.svc.PickResonantConcept(ctx, in)
}

func (a *growthServiceAdapter) TriggerSourceRevelation(ctx context.Context, in growth.TriggerSourceRevelationInput) (*growth.TriggerSourceRevelationResponse, error) {
	return a.svc.TriggerSourceRevelation(ctx, in)
}

func (a *growthServiceAdapter) ListGrowthEvents(ctx context.Context, in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	return a.svc.ListGrowthEvents(ctx, in)
}

// ----- request / response DTOs -----

type hatchEggReq struct {
	DisplayName    string `json:"display_name"`
	Tone           string `json:"tone"`
	LearnerPersona string `json:"learner_persona"`
	ResonantAtomID string `json:"resonant_atom_id"`
}

type hatchEggResp struct {
	State             *stateResp `json:"state"`
	Species           string     `json:"species"`
	ShinyVariant      bool       `json:"shiny_variant"`
	Rarity            string     `json:"rarity"`
	RolledProbability float64    `json:"rolled_probability"`
}

// revealResp — POST /reveal response (CHO-2229). Same roll shape as
// hatchEggResp plus the reveal moment and the idempotent-replay marker
// (a re-POST returns the persisted roll with already_revealed=true).
type revealResp struct {
	State             *stateResp `json:"state"`
	Species           string     `json:"species"`
	ShinyVariant      bool       `json:"shiny_variant"`
	Rarity            string     `json:"rarity"`
	RolledProbability float64    `json:"rolled_probability"`
	RevealedAt        time.Time  `json:"revealed_at"`
	AlreadyRevealed   bool       `json:"already_revealed"`
	// AwakeningClass (CHO-2235): hatch | wake | power_on — the reveal
	// metaphor for the rolled species (art brief §2), so the FE never
	// hardcodes the taxonomy.
	AwakeningClass string `json:"awakening_class"`
}

type stateResp struct {
	CompanionID              string     `json:"companion_id"`
	GrowthStage              int        `json:"growth_stage"`
	StageName                string     `json:"stage_name"`
	DisplayName              string     `json:"display_name,omitempty"`
	Species                  string     `json:"species,omitempty"`
	ShinyVariant             bool       `json:"shiny_variant"`
	Rarity                   string     `json:"rarity,omitempty"`
	ExpCurrent               int        `json:"exp_current"`
	ExpNextThreshold         int        `json:"exp_next_threshold"`
	ExpCumulative            int        `json:"exp_cumulative"`
	EffectiveLLMTier         string     `json:"effective_llm_tier"`
	EffectiveMaxOutputTokens int        `json:"effective_max_output_tokens"`
	UnlockedTools            []string   `json:"unlocked_tools"`
	ResonantAtomID           string     `json:"resonant_atom_id,omitempty"`
	ResonantConceptID        string     `json:"resonant_concept_id,omitempty"`
	// Resonant*Title (D3, CHO-2047): the learner-facing name behind each id,
	// resolved server-side on the direct growth read only. `omitempty` is the
	// contract, not a convenience: an ABSENT title means "this did not
	// resolve", which the profile renders as the concept being gone. An empty
	// string would be indistinguishable from a concept genuinely named "".
	ResonantAtomTitle    string `json:"resonant_atom_title,omitempty"`
	ResonantConceptTitle string `json:"resonant_concept_title,omitempty"`
	AhaMomentConsumed        bool       `json:"aha_moment_consumed"`
	AhaMomentActiveUntil     *time.Time `json:"aha_moment_active_until,omitempty"`
	// RevealedAt (CHO-2229) — set once the breed roll persisted; the FE
	// ceremony resumes past the crack step when present on a stage-0 pod.
	RevealedAt    *time.Time `json:"revealed_at,omitempty"`
	HatchedAt     *time.Time `json:"hatched_at,omitempty"`
	LastStageUpAt *time.Time `json:"last_stage_up_at,omitempty"`
	// NextUnlocks is the CHO-2030 (R3-9) next-stage Path preview — set
	// only on the direct growth read (handleGetGrowth enriches it when
	// the SpeciesPaths reader is wired); embedded stateResp uses
	// (hatch/resonance/revelation) omit it.
	NextUnlocks []nextUnlockResp `json:"next_unlocks,omitempty"`
}

// pickResonanceReq — CHO-2013 P1 (R3-1): the awakening resonant-concept pick.
type pickResonanceReq struct {
	ConceptID string `json:"concept_id"`
}

type pickResonanceResp struct {
	State *stateResp `json:"state"`
}

// nextUnlockResp is one CHO-2030 next-stage Path preview row. The
// catalogue_active flag lets the ceremony render owned-but-dark entries
// as "stirring, not yet awake" (R3-9: named, never fake-usable).
type nextUnlockResp struct {
	SkillKey        string `json:"skill_key"`
	SkillKind       string `json:"skill_kind"`
	UnlocksAtStage  int    `json:"unlocks_at_stage"`
	CatalogueActive bool   `json:"catalogue_active"`
}

type triggerRevelationReq struct {
	ManaTier               string `json:"mana_tier"`
	WindowDurationOverride int    `json:"window_duration_seconds_override,omitempty"`
}

type triggerRevelationResp struct {
	State                 *stateResp `json:"state"`
	PreviewLLMTier        string     `json:"preview_llm_tier"`
	WindowExpiresAt       time.Time  `json:"window_expires_at"`
	WindowDurationSeconds int        `json:"window_duration_seconds"`
}

type listEventsResp struct {
	Events        []eventRecordResp `json:"events"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}

type eventRecordResp struct {
	GrowthEventID    string `json:"growth_event_id"`
	Source           string `json:"source"`
	ExpDelta         int    `json:"exp_delta"`
	ExpTotalAfter    int    `json:"exp_total_after"`
	DailyCapHit      bool   `json:"daily_cap_hit"`
	TriggeredStageUp bool   `json:"triggered_stage_up"`
	// StageBefore/StageAfter are RECONSTRUCTED at read time (CHO-2144 BE gap):
	// the row persists only exp + the triggered_stage_up bool, but the FE renders
	// a numeric stage-up pill. Stage is a pure function of cumulative EXP (fixed
	// curve thresholds), so both derive from ExpTotalAfter (− ExpDelta) — no
	// schema change, and consistent with how the live state's stage is computed.
	StageBefore int       `json:"stage_before"`
	StageAfter  int       `json:"stage_after"`
	AwardedAt   time.Time `json:"awarded_at"`
}

// ----- response marshaling -----

func toStateResp(st *growth.State) *stateResp {
	if st == nil {
		return nil
	}
	out := &stateResp{
		CompanionID:              st.CompanionID,
		GrowthStage:              st.GrowthStage,
		StageName:                st.StageName,
		DisplayName:              st.DisplayName,
		Species:                  st.Species,
		ShinyVariant:             st.ShinyVariant,
		Rarity:                   st.Rarity,
		ExpCurrent:               st.ExpCurrent,
		ExpNextThreshold:         st.ExpNextThreshold,
		ExpCumulative:            st.ExpCumulative,
		EffectiveLLMTier:         st.EffectiveLLMTier,
		EffectiveMaxOutputTokens: st.EffectiveMaxOutputToks,
		UnlockedTools:            st.UnlockedTools,
		ResonantAtomID:           st.ResonantAtomID,
		ResonantConceptID:        st.ResonantConceptID,
		AhaMomentConsumed:        st.AhaMomentConsumed,
		AhaMomentActiveUntil:     st.AhaMomentActiveUntil,
		RevealedAt:               st.RevealedAt,
		HatchedAt:                st.HatchedAt,
		LastStageUpAt:            st.LastStageUpAt,
	}
	if out.UnlockedTools == nil {
		out.UnlockedTools = []string{}
	}
	return out
}

// ----- handlers -----

// rlsCtx attaches tenant_id + gcid to the request context so the repo
// layer's rls.ApplySession (which reads via tracing.TenantIDFromContext +
// tracing.GCIDFromContext) can SET LOCAL chora.tenant_id /
// chora.user_gcid inside the transaction.
//
// Per multi-tenant-rls SKILL + Debt #40 close (2026-05-16): handlers MUST
// populate ctx BEFORE invoking any RLS-scoped repo method. Without this
// wrap the pg repo's rls.ApplySession returns ErrNoTenantContext and the
// entire transaction aborts (HTTP 500 with "rls: tenant_id missing on
// context" — surfaces as a gateway 502 GATEWAY_UPSTREAM_5XX per CR v4
// rehearsal row 11). Mirrors companion_instance_handlers.go::repoCtx.
func rlsCtx(r *http.Request, tenantID, gcid string) context.Context {
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	ctx = tracing.WithGCID(ctx, gcid)
	return ctx
}

// handleCompanionGrowthRoute dispatches /v1/me/companions/{id}/growth-related
// paths to the right method. Called by router.go when the GrowthServer is
// wired.
func (s *Server) handleCompanionGrowthRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/companions/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "")
		return
	}
	id := parts[0]
	sub := parts[1]
	// CHO-2012 loadout subtree: "skills" + "skills/{key}/equip". The
	// handler does its own method dispatch + auth + RLS-ctx stamping and
	// derives the slot cap from the growth stage when the GrowthServer is
	// wired (stage-0 allowance otherwise).
	if sub == "skills" || strings.HasPrefix(sub, "skills/") {
		s.handleCompanionSkillsRoute(w, r, id, sub)
		return
	}
	// CHO-2016 Grimoire Rituals v1: the rituals subtree (list/create/get/
	// publish/run/runs). The handler does its own method dispatch + auth +
	// RLS-ctx stamping; a nil publisher/runner ⇒ 503 RITUALS_NOT_WIRED.
	if sub == "rituals" || strings.HasPrefix(sub, "rituals/") {
		s.handleRitualsRoute(w, r, id, sub)
		return
	}
	// CHO-2040 (CR §8 R7-3): the ceremony edge-scout PROPOSE runner — a
	// composed runner registered next to the Skill-invoke subtree above (no
	// grant/equip/stage gate; the binding ceremony is the acquisition moment).
	if sub == "ceremony/edge-scout" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.ceremonyEdgeScout(w, r, id)
		return
	}
	// CHO-2040 (owner ruling R8-5): the ceremony CONFIRM hook — the FE echoes
	// the CHO-2038 POST response here after minting the ticked edges on the
	// map; remediate ticks feed weakness.analyzed.v1 (→ per-Companion RAG),
	// explore ticks become one ceremony memory-note. No LLM turn, no mana.
	if sub == "ceremony/edge-scout/confirm" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.ceremonyEdgeScoutConfirm(w, r, id)
		return
	}
	// CHO-2040 (R8-6): the Virgin Proofing Test composed runner — same
	// composed-runner posture as the edge-scout above (goal-driven, no
	// catalogue row / grant / equip / stage gate).
	if sub == "proofing-test" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.createProofingTest(w, r, id)
		return
	}
	// CHO-2033: retire (soft-release) a roster companion to free a cap-3 slot.
	if sub == "retire" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.retireCompanion(w, r, id)
		return
	}
	switch sub {
	case "growth":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleGetGrowth(w, r, id)
	case "reveal":
		// CHO-2229 (ADR-228 Phase 2): roll + persist the breed on a stirring
		// pod so the reveal precedes naming. Idempotent POST, no body.
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleRevealBreed(w, r, id)
	case "hatch":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleHatchEgg(w, r, id)
	case "resonance":
		// CHO-2013 P1 (R3-1): the awakening resonant-concept pick.
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handlePickResonantConcept(w, r, id)
	case "source-revelation":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleTriggerSourceRevelation(w, r, id)
	case "growth-events":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleListGrowthEvents(w, r, id)
	case "chat":
		// ADR-154 — Companion conversational chat (SSE). The handler does
		// its own method check + body decode + mana gate.
		s.handleCompanionChat(w, r, id)
	case "persona":
		// CHO-2015 (ADR-219 D2): the learner-designed Persona sheet.
		// GET reads the current editable view; PUT applies a full edit
		// (the free-text note is Model-Armor-screened before persist).
		switch r.Method {
		case http.MethodGet:
			s.handleGetPersona(w, r, id)
		case http.MethodPut:
			s.handleUpdatePersona(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		}
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "")
	}
}

func (s *Server) handleGetGrowth(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Growth == nil {
		writeError(w, http.StatusServiceUnavailable, "GROWTH_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	// Debt #40 close (2026-05-16) — wrap ctx with tenant_id + gcid so the
	// pgx repo's rls.ApplySession can SET LOCAL chora.tenant_id /
	// chora.user_gcid inside the transaction. Mirrors the fix agent #37
	// applied at companion_instance_handlers.go (repoCtx helper).
	state, err := s.Growth.GetCompanionGrowth(rlsCtx(r, tenantID, gcid), tenantID, companionID, gcid)
	if err != nil {
		mapGrowthError(w, err)
		return
	}
	resp := toStateResp(state)
	if !s.enrichNextUnlocks(w, r, resp, state) {
		return
	}
	// Same RLS-scoped context as the read above, deliberately: see
	// ResonanceTitleReader on the Server struct.
	s.enrichResonantTitles(rlsCtx(r, tenantID, gcid), resp, state)
	writeJSON(w, http.StatusOK, resp)
}

// ResonanceTitleReader resolves a resonant id to the title a learner would
// recognise. An empty title with a nil error means the id no longer resolves
// for this learner, which is a real state and not an error: a concept can
// leave a map while the companion row still points at it.
type ResonanceTitleReader interface {
	ConceptTitle(ctx context.Context, conceptID string) (string, error)
	AtomTitle(ctx context.Context, atomID string) (string, error)
}

// AtomTitleReader is the ATOM arm alone, for readers that never ask about a
// concept.
//
// It exists because the two-arm reader above is bound only when BOTH
// projections are live, which is right for the profile (a half resolver looks
// healthy while answering absence to half of what it is asked) and wrong for
// the ritual source titles, which read atoms ONLY. Under the single rule a
// deployment with a live atom_index and no concept graph left every grounded
// step nameless for a reason that had nothing to do with atoms.
//
// Narrow on purpose, matching SeenMarker and PendingReviewLister: a consumer
// that cannot ask about concepts cannot be broken by the concept arm.
type AtomTitleReader interface {
	AtomTitle(ctx context.Context, atomID string) (string, error)
}

// enrichResonantTitles names the resonant concept and atom on a direct growth
// read (D3, CHO-2047). It never writes an error response and never fails the
// read, mirroring learnerCatalogue in the ritual handlers: a name is a nicety,
// the growth state is the payload.
//
// Three outcomes, two of which look identical on the wire on purpose:
//   - resolved      → the title is set
//   - did not exist → the title is ABSENT, and the profile says so honestly
//   - unreadable    → the title is ABSENT too, and the reason goes to the log
//
// A learner is owed the same copy either way; which of the two it was is an
// operator concern, so it is logged rather than encoded in the payload.
func (s *Server) enrichResonantTitles(ctx context.Context, resp *stateResp, state *growth.State) {
	if s.ResonanceTitles == nil || resp == nil || state == nil {
		return
	}
	if id := state.ResonantConceptID; id != "" {
		title, err := s.ResonanceTitles.ConceptTitle(ctx, id)
		switch {
		case err != nil:
			log.Printf("consumption: resonant concept title unreadable for companion %s (title omitted, growth read still served): %v",
				state.CompanionID, err)
		case title != "":
			resp.ResonantConceptTitle = title
		}
	}
	if id := state.ResonantAtomID; id != "" {
		title, err := s.ResonanceTitles.AtomTitle(ctx, id)
		switch {
		case err != nil:
			log.Printf("consumption: resonant atom title unreadable for companion %s (title omitted, growth read still served): %v",
				state.CompanionID, err)
		case title != "":
			resp.ResonantAtomTitle = title
		}
	}
}

// enrichNextUnlocks attaches the CHO-2030 (R3-9) next-stage Path preview
// to a direct growth read. Wiring-absent (nil reader/catalogue) or a
// blank species ⇒ skip, mirroring the loadGrowthStage convention. A
// species with NO active path degrades loudly-in-logs but keeps the
// growth read alive (legacy rows must not brick the profile); any other
// read/compute failure is a fail-loud 500. Returns false when it wrote
// the error response.
func (s *Server) enrichNextUnlocks(w http.ResponseWriter, r *http.Request, resp *stateResp, state *growth.State) bool {
	if s.SpeciesPaths == nil || s.SkillCatalog == nil || state == nil || state.Species == "" {
		return true
	}
	path, err := s.SpeciesPaths.ActivePathForSpecies(r.Context(), state.Species)
	if err != nil {
		if errors.Is(err, companion.ErrSpeciesPathNotFound) {
			log.Printf("consumption: next-unlocks omitted — no active path for species %q (companion %s)",
				state.Species, state.CompanionID)
			return true
		}
		writeError(w, http.StatusInternalServerError, "NEXT_UNLOCKS_READ_FAILED", err.Error())
		return false
	}
	catalogue, err := s.SkillCatalog.ListCatalogue(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NEXT_UNLOCKS_READ_FAILED", err.Error())
		return false
	}
	previews, err := companion.NextUnlocksForStage(path, catalogue, state.GrowthStage, companion.DefaultStageUnlockCounts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NEXT_UNLOCKS_READ_FAILED", err.Error())
		return false
	}
	rows := make([]nextUnlockResp, 0, len(previews))
	for _, p := range previews {
		rows = append(rows, nextUnlockResp{
			SkillKey:        p.SkillKey,
			SkillKind:       string(p.SkillKind),
			UnlocksAtStage:  p.UnlocksAtStage,
			CatalogueActive: p.CatalogueActive,
		})
	}
	resp.NextUnlocks = rows
	return true
}

// handleRevealBreed — POST /v1/me/companions/{id}/reveal (CHO-2229): the
// breed roll, split out of the hatch commit so the reveal precedes naming.
// Stirring-gated; idempotent (a re-POST returns the persisted roll with
// already_revealed=true and never re-rolls); takes no request body.
func (s *Server) handleRevealBreed(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Growth == nil {
		writeError(w, http.StatusServiceUnavailable, "GROWTH_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	traceparent, tracestate := traceFromHeaders(r)
	resp, err := s.Growth.RevealBreed(rlsCtx(r, tenantID, gcid), growth.RevealBreedInput{
		TenantID:    tenantID,
		CompanionID: companionID,
		OwnerGCID:   gcid,
		Traceparent: traceparent,
		Tracestate:  tracestate,
	})
	if err != nil {
		mapGrowthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revealResp{
		State:             toStateResp(&resp.State),
		Species:           resp.Species,
		ShinyVariant:      resp.ShinyVariant,
		Rarity:            resp.Rarity,
		RolledProbability: resp.RolledProbability,
		RevealedAt:        resp.RevealedAt,
		AlreadyRevealed:   resp.AlreadyRevealed,
		AwakeningClass:    resp.AwakeningClass,
	})
}

func (s *Server) handleHatchEgg(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Growth == nil {
		writeError(w, http.StatusServiceUnavailable, "GROWTH_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req hatchEggReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	traceparent, tracestate := traceFromHeaders(r)
	// Debt #40 close (2026-05-16) — wrap ctx with tenant_id + gcid.
	resp, err := s.Growth.HatchEgg(rlsCtx(r, tenantID, gcid), growth.HatchEggInput{
		TenantID:       tenantID,
		CompanionID:    companionID,
		OwnerGCID:      gcid,
		DisplayName:    req.DisplayName,
		Tone:           req.Tone,
		LearnerPersona: req.LearnerPersona,
		ResonantAtomID: req.ResonantAtomID,
		Traceparent:    traceparent,
		Tracestate:     tracestate,
	})
	if err != nil {
		mapGrowthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hatchEggResp{
		State:             toStateResp(&resp.State),
		Species:           resp.Species,
		ShinyVariant:      resp.ShinyVariant,
		Rarity:            resp.Rarity,
		RolledProbability: resp.RolledProbability,
	})
}

// handlePickResonantConcept — POST /v1/me/companions/{id}/resonance
// (CHO-2013 P1, R3-1): persists the awakening resonant-concept pick.
func (s *Server) handlePickResonantConcept(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Growth == nil {
		writeError(w, http.StatusServiceUnavailable, "GROWTH_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req pickResonanceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	traceparent, tracestate := traceFromHeaders(r)
	resp, err := s.Growth.PickResonantConcept(rlsCtx(r, tenantID, gcid), growth.PickResonantConceptInput{
		TenantID:    tenantID,
		CompanionID: companionID,
		OwnerGCID:   gcid,
		ConceptID:   req.ConceptID,
		Traceparent: traceparent,
		Tracestate:  tracestate,
	})
	if err != nil {
		mapGrowthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pickResonanceResp{State: toStateResp(&resp.State)})
}

func (s *Server) handleTriggerSourceRevelation(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Growth == nil {
		writeError(w, http.StatusServiceUnavailable, "GROWTH_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req triggerRevelationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	traceparent, tracestate := traceFromHeaders(r)
	// Debt #40 close (2026-05-16) — wrap ctx with tenant_id + gcid.
	resp, err := s.Growth.TriggerSourceRevelation(rlsCtx(r, tenantID, gcid), growth.TriggerSourceRevelationInput{
		TenantID:               tenantID,
		CompanionID:            companionID,
		OwnerGCID:              gcid,
		ManaTier:               req.ManaTier,
		WindowDurationOverride: req.WindowDurationOverride,
		Traceparent:            traceparent,
		Tracestate:             tracestate,
	})
	if err != nil {
		mapGrowthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, triggerRevelationResp{
		State:                 toStateResp(&resp.State),
		PreviewLLMTier:        resp.PreviewLLMTier,
		WindowExpiresAt:       resp.WindowExpiresAt,
		WindowDurationSeconds: resp.WindowDurationSeconds,
	})
}

func (s *Server) handleListGrowthEvents(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.Growth == nil {
		writeError(w, http.StatusServiceUnavailable, "GROWTH_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	pageSize := 50
	if ps := r.URL.Query().Get("page_size"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 {
			pageSize = v
		}
	}
	// Debt #40 close (2026-05-16) — wrap ctx with tenant_id + gcid.
	out, err := s.Growth.ListGrowthEvents(rlsCtx(r, tenantID, gcid), growth.ListGrowthEventsInput{
		TenantID:    tenantID,
		CompanionID: companionID,
		CallerGCID:  gcid,
		PageSize:    pageSize,
		PageToken:   r.URL.Query().Get("page_token"),
	})
	if err != nil {
		mapGrowthError(w, err)
		return
	}
	resp := listEventsResp{NextPageToken: out.NextPageToken}
	for _, ev := range out.Events {
		resp.Events = append(resp.Events, eventRecordResp{
			GrowthEventID:    ev.GrowthEventID,
			Source:           ev.Source,
			ExpDelta:         ev.AwardedDelta,
			ExpTotalAfter:    ev.ExpTotalAfter,
			DailyCapHit:      ev.DailyCapHit,
			TriggeredStageUp: ev.TriggeredStageUp,
			StageBefore:      growth.StageForExp(ev.ExpTotalAfter - ev.AwardedDelta),
			StageAfter:       growth.StageForExp(ev.ExpTotalAfter),
			AwardedAt:        ev.AwardedAt,
		})
	}
	if resp.Events == nil {
		resp.Events = []eventRecordResp{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// mapGrowthError converts a domain growth error to an HTTP status.
func mapGrowthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, growth.ErrCompanionNotFound):
		writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", err.Error())
	case errors.Is(err, growth.ErrAlreadyHatched):
		writeError(w, http.StatusConflict, "ALREADY_HATCHED", err.Error())
	case errors.Is(err, growth.ErrNotStirring):
		writeError(w, http.StatusConflict, "NOT_STIRRING", err.Error())
	case errors.Is(err, growth.ErrNotRevealed):
		// CHO-2229: hatch is commit-only — the FE must run the reveal step
		// first. 409 so the ceremony can route to it rather than retry.
		writeError(w, http.StatusConflict, "NOT_REVEALED", err.Error())
	case errors.Is(err, growth.ErrNotInEggStage):
		writeError(w, http.StatusConflict, "NOT_IN_EGG_STAGE", err.Error())
	case errors.Is(err, growth.ErrAhaMomentConsumed):
		writeError(w, http.StatusConflict, "AHA_MOMENT_CONSUMED", err.Error())
	case errors.Is(err, growth.ErrCompanionNotBound):
		writeError(w, http.StatusConflict, "COMPANION_NOT_BOUND", err.Error())
	case errors.Is(err, growth.ErrCompanionNotHatched):
		writeError(w, http.StatusConflict, "COMPANION_NOT_HATCHED", err.Error())
	case errors.Is(err, growth.ErrConceptNotFound):
		writeError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", err.Error())
	case errors.Is(err, growth.ErrResonanceNotWired):
		writeError(w, http.StatusServiceUnavailable, "RESONANCE_NOT_WIRED", err.Error())
	case errors.Is(err, growth.ErrInvalidArguments), errors.Is(err, growth.ErrInvalidSource):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
