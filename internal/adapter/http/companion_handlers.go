// Phyllis MVP handlers — Companion + Daily Dose endpoints (Comic Ch6 P14,
// Ch7 P16). Mounted at root path per docs/m13/phyllis-mvp-2026-05-08.md §5.4.
//
// Endpoints implemented in this file:
//
//	GET  /companion/me                — current user's Companion (404 if not summoned)
//	POST /companion/summon            — summon new Companion (Comic Ch4 P3 ceremony)
//	GET  /companion/daily-dose        — 5-atom Daily Dose (deterministic SM-2 + Ebbinghaus)
//	POST /atoms/{id}/feedback        — SM-2 grade ("i_remember"|"not_sure"|"forgot")
//
// Auth model (MVP): X-Tenant-Id + gcid headers (same as the existing /api
// routes). Real WebAuthn JWT verification lands in M13.A BFF layer.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
)

// ----- Request / response payloads -----

type summonReq struct {
	Name      string                `json:"name"`
	Archetype string                `json:"archetype"`
	Stats     statAllocationPayload `json:"stats"`
}

type statAllocationPayload struct {
	Curiosity    int `json:"curiosity"`
	Focus        int `json:"focus"`
	Recall       int `json:"recall"`
	Social       int `json:"social"`
	Perseverance int `json:"perseverance"`
}

type companionMVPResp struct {
	CompanionID    string          `json:"companion_id"`
	TenantID       string          `json:"tenant_id"`
	OwnerGCID      string          `json:"owner_gcid"`
	Name           string          `json:"name"`
	Archetype      string          `json:"archetype"`
	Level          int             `json:"level"`
	XP             int             `json:"xp"`
	Stats          companion.Stats `json:"stats"`
	UnlockedTraits []string        `json:"unlocked_traits"`
	Traits         []string        `json:"traits"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func toCompanionMVPResp(f *companion.Companion) companionMVPResp {
	traits := f.Traits
	if traits == nil {
		traits = []string{}
	}
	unlocked := f.UnlockedTraits
	if unlocked == nil {
		unlocked = []string{}
	}
	return companionMVPResp{
		CompanionID:    f.CompanionID,
		TenantID:       f.TenantID,
		OwnerGCID:      f.OwnerGCID,
		Name:           f.Name,
		Archetype:      string(f.Archetype),
		Level:          f.Level,
		XP:             f.XP,
		Stats:          f.Stats,
		UnlockedTraits: unlocked,
		Traits:         traits,
		CreatedAt:      f.CreatedAt,
		UpdatedAt:      f.UpdatedAt,
	}
}

type feedbackReq struct {
	Grade string `json:"grade"`
}

type feedbackResp struct {
	AtomID         string          `json:"atom_id"`
	Grade          string          `json:"grade"`
	NextReviewAt   time.Time       `json:"next_review_at"`
	IntervalDays   int             `json:"interval_days"`
	Repetitions    int             `json:"repetitions"`
	XPEarned       int             `json:"xp_earned"`
	LeveledUp      bool            `json:"leveled_up"`
	UnlockedTrait  string          `json:"unlocked_trait,omitempty"`
	NudgeMessage   string          `json:"nudge_message,omitempty"`
	CompanionLevel int             `json:"companion_level"`
	CompanionStats companion.Stats `json:"companion_stats"`
}

// ----- /companion/me -----

func (s *Server) handleCompanionMe(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	f, err := s.Companions.GetByGCID(tenantID, gcid)
	if err != nil || f.Archetype == "" {
		// Per spec: 404 if not summoned yet (distinct from /api/companions
		// auto-bond legacy behaviour, which lacks an archetype).
		writeError(w, http.StatusNotFound, "COMPANION_NOT_SUMMONED",
			"summon a Companion via POST /companion/summon")
		return
	}
	writeJSON(w, http.StatusOK, toCompanionMVPResp(f))
}

// ----- /companion/summon -----

func (s *Server) handleCompanionSummon(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}

	// Idempotency: if a Companion with archetype already exists for this
	// (tenant, gcid), reject with 409 — summon is a one-time ceremony per
	// docs/design/ux_companion_chat.md §Summoning ("choice is permanent").
	if existing, err := s.Companions.GetByGCID(tenantID, gcid); err == nil &&
		existing.Archetype != "" {
		writeError(w, http.StatusConflict, "COMPANION_ALREADY_SUMMONED",
			"one Companion per learner per tenant")
		return
	}

	var req summonReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}

	alloc := companion.StatAllocation{
		Curiosity:    req.Stats.Curiosity,
		Focus:        req.Stats.Focus,
		Recall:       req.Stats.Recall,
		Social:       req.Stats.Social,
		Perseverance: req.Stats.Perseverance,
	}
	f, err := companion.Summon(tenantID, gcid, req.Name, companion.Archetype(req.Archetype), alloc)
	if err != nil {
		// Map domain errors → HTTP status. Invalid archetype + invalid
		// allocation are both 400 Bad Request per Phyllis MVP spec.
		writeError(w, http.StatusBadRequest, "INVALID_SUMMON", err.Error())
		return
	}
	s.Companions.Save(f)
	writeJSON(w, http.StatusCreated, toCompanionMVPResp(f))
}

// ----- /companion/daily-dose -----

// dailyDoseResp is the envelope returned to clients of GET /companion/daily-dose.
//
// F4 (Phyllis demo blocker, 2026-05-13): per
// docs/m13/HANDOFF_BE_F2_F4_PHYLLIS_BLOCKERS_2026-05-13.md §3.5 the response
// now carries BOTH the original snake_case "entries / greeting / ai_picks / ..."
// fields (kept for back-compat with internal tooling + the dress-rehearsal
// memo) AND the chora-web DailyDose model shape (camelCase atoms, composition,
// companionName / companionLevel / companionQuote, doseId, servedOn, nextDoseAt).
//
// The handoff also authorises graceful-degradation to 200 with a templated
// greeting + narrative + deterministic SM-2 + Ebbinghaus picks when engines
// are unavailable — degraded > absent for the demo.
type dailyDoseResp struct {
	// Legacy / internal fields (kept for back-compat) ------------------------
	GeneratedAt time.Time                  `json:"generated_at"`
	Message     string                     `json:"message"`
	Entries     []companion.DailyDoseEntry `json:"entries"`
	Greeting    string                     `json:"greeting,omitempty"`

	// AIPicks: 0-N atom_ids from the Recommender's tool call. Empty when
	// the recommender returned a narrative-only response or is unavailable.
	AIPicks []string `json:"ai_picks,omitempty"`

	// RecommenderNarrative: present when the recommender returned text
	// instead of a tool call (or in degraded mode, a templated narrative).
	RecommenderNarrative string `json:"recommender_narrative,omitempty"`

	// EngineMetadata: per-engine fingerprint (model + token counts) so
	// the UI / cost ledger can attribute the call. Empty in degraded mode.
	EngineMetadata *dailyDoseEngineMetadata `json:"engine_metadata,omitempty"`

	// Degraded reports whether this response was assembled from
	// deterministic SM-2 + Ebbinghaus picks because one or both engines
	// were unavailable. UI may surface a subtle "without Eira's help"
	// banner per AC-F4.3 graceful-degradation contract.
	Degraded bool `json:"degraded,omitempty"`

	// chora-web FE shape ----------------------------------------------------
	// Mirrors chora-web/src/app/features/surfaces/aplus/daily-dose/daily-dose.model.ts.
	DoseID           string               `json:"doseId"`
	ServedOn         string               `json:"servedOn"`
	Atoms            []dailyDoseFEAtom    `json:"atoms"`
	Composition      dailyDoseComposition `json:"composition"`
	TotalXPAvailable int                  `json:"totalXpAvailable"`
	NextDoseAt       string               `json:"nextDoseAt"`
	CompanionName    string               `json:"companionName"`
	CompanionLevel   int                  `json:"companionLevel"`
	CompanionQuote   string               `json:"companionQuote"`

	// N-Companion dispatch fields (CHO-1577) ---------------------------------
	// GreetingFrom identifies which Companion in the learner's roster is
	// greeting them for this dose. Absent when the roster is empty or
	// every Companion is pre-hatch (Mystery Egg only).
	GreetingFrom *companion.GreetingFrom `json:"greeting_from,omitempty"`

	// AtomBreakdown is the dose composition by reason bucket.
	AtomBreakdown companion.DoseAtomBreakdown `json:"atom_breakdown"`

	// Scope is the ADR-242 D2 disclosure: the served-card split for a
	// goal-scoped dose (owner-ruled three-way, 2026-08-17). Absent when no
	// goal was requested (D5: the unscoped payload is byte-identical).
	// NB the doseId stays (tenant, gcid, date)-derived, so two different
	// goals on the same day share a doseId with DIFFERENT scope blocks; a
	// cache keyed on doseId alone would serve a stale split.
	Scope *companion.DoseScope `json:"scope,omitempty"`
}

// dailyDoseFEAtom mirrors the chora-web DailyDoseAtom interface.
type dailyDoseFEAtom struct {
	AtomID       string `json:"atomId"`
	CourseCode   string `json:"courseCode"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	Category     string `json:"category"` // "review" | "new" | "stretch"
	Topic        string `json:"topic"`
	XPOnComplete int    `json:"xpOnComplete"`
}

// dailyDoseComposition mirrors the chora-web DailyDoseComposition interface.
type dailyDoseComposition struct {
	ReviewPercent  int `json:"reviewPercent"`
	NewPercent     int `json:"newPercent"`
	StretchPercent int `json:"stretchPercent"`
}

// dailyDoseEngineMetadata captures per-engine model + token counts for the
// runtime accountability slice (IMDA D1).
type dailyDoseEngineMetadata struct {
	CompanionModel          string `json:"companion_model,omitempty"`
	CompanionOutputTokens   int    `json:"companion_output_tokens,omitempty"`
	RecommenderModel        string `json:"recommender_model,omitempty"`
	RecommenderOutputTokens int    `json:"recommender_output_tokens,omitempty"`
}

// doseReasonToCategory maps the domain-side dose reason to the FE-facing
// category enum. The FE only knows three buckets — review / new / stretch —
// while the domain has four reasons (ebbinghaus_review, fresh, curiosity,
// weakness). Curiosity counts as "new" exploration; weakness as "stretch".
func doseReasonToCategory(r companion.DoseReason) string {
	switch r {
	case companion.DoseReasonEbbinghausReview:
		return "review"
	case companion.DoseReasonWeakness:
		return "stretch"
	case companion.DoseReasonFresh, companion.DoseReasonCuriosity:
		return "new"
	default:
		return "new"
	}
}

// xpForCategory returns the deterministic XP reward per dose-card category.
// Stretch picks reward most (they're the weakness-drill); review picks
// least (the learner has seen this content before). Cosmetic only — the
// real grade-based XP awarded on completion lives in handleAtomFeedback.
func xpForCategory(c string) int {
	switch c {
	case "review":
		return 20
	case "stretch":
		return 50
	default:
		return 30
	}
}

// composeDoseComposition computes the 40/30/30 breakdown from the actual
// dose entries. Returns rounded percentages whose sum is at most 100.
func composeDoseComposition(entries []companion.DailyDoseEntry) dailyDoseComposition {
	if len(entries) == 0 {
		return dailyDoseComposition{}
	}
	var review, newCnt, stretch int
	for _, e := range entries {
		switch doseReasonToCategory(e.DoseReason) {
		case "review":
			review++
		case "stretch":
			stretch++
		default:
			newCnt++
		}
	}
	total := review + newCnt + stretch
	return dailyDoseComposition{
		ReviewPercent:  int(100 * review / total),
		NewPercent:     int(100 * newCnt / total),
		StretchPercent: int(100 * stretch / total),
	}
}

// composeFEAtoms maps domain entries to the FE-facing atom shape, looking
// up additional context (CourseCode, summary) from the atom catalogue
// when available.
func composeFEAtoms(s *Server, entries []companion.DailyDoseEntry) ([]dailyDoseFEAtom, int) {
	out := make([]dailyDoseFEAtom, 0, len(entries))
	totalXP := 0
	for _, e := range entries {
		cat := doseReasonToCategory(e.DoseReason)
		xp := xpForCategory(cat)
		summary, courseCode := "", ""
		if s != nil && s.Atoms != nil {
			if seed, ok := s.Atoms.Lookup(e.AtomID); ok {
				// AtomSeed has Title + Topic only in the POC seed catalogue;
				// summary + course code fall back to deterministic stand-ins
				// the FE renders without confusion.
				_ = seed
			}
		}
		if summary == "" {
			summary = e.Title // FE binds summary onto card preview; title is meaningful preview
		}
		if courseCode == "" {
			courseCode = courseCodeFromTopic(e.Topic)
		}
		out = append(out, dailyDoseFEAtom{
			AtomID:       e.AtomID,
			CourseCode:   courseCode,
			Title:        e.Title,
			Summary:      summary,
			Category:     cat,
			Topic:        e.Topic,
			XPOnComplete: xp,
		})
		totalXP += xp
	}
	return out, totalXP
}

// courseCodeFromTopic derives a stand-in course code from the atom's
// topic when the chora-creation projection hasn't seeded it. Stable
// shape "T-<first 8 chars uppercased>" so the FE chip rendering is
// deterministic across page reloads.
func courseCodeFromTopic(topic string) string {
	t := strings.ToUpper(strings.TrimSpace(topic))
	if t == "" {
		return "T-LEARN"
	}
	t = strings.ReplaceAll(t, " ", "-")
	if len(t) > 16 {
		t = t[:16]
	}
	return "T-" + t
}

// composeDeterministicGreeting builds a templated greeting used when the
// CompanionEngine is unavailable. Preserves the FE contract (companionQuote
// non-empty) so the UI renders the speech-bubble normally.
func composeDeterministicGreeting(companionName string, level int) string {
	if companionName == "" {
		companionName = "Eira"
	}
	if level <= 1 {
		return companionName + " here. Let's tackle today's dose."
	}
	return companionName + " here, Level " + itoa(level) + " and ready for today's dose."
}

// proseNarrative returns s only when it is genuine learner-facing prose.
//
// The Recommender agent occasionally emits its structured bail-out envelope
// (e.g. {"reason":"Unable to generate...","recommendations":[],...}) instead of
// a sentence — sometimes wrapped in a ```json code fence. Rendering that raw
// JSON in the learner's Companion panel is a fail-loud violation (garbage as
// prose), so we detect and drop it here: on non-prose we return "" and the
// caller falls back to the deterministic narrative. Pure function (unit-tested
// in companion_handlers_narrative_test.go).
func proseNarrative(s string) string {
	out := strings.TrimSpace(s)

	// Unwrap a Markdown code fence (```json … ``` or ``` … ```). The opening
	// fence may carry a bare language tag (e.g. "json") on its first line.
	if strings.HasPrefix(out, "```") {
		out = strings.TrimPrefix(out, "```")
		if nl := strings.IndexByte(out, '\n'); nl >= 0 {
			// A first line with no interior whitespace is a language tag, not
			// prose — strip it. Otherwise keep it (it is content).
			if firstLine := strings.TrimSpace(out[:nl]); firstLine == "" || !strings.ContainsAny(firstLine, " \t") {
				out = out[nl+1:]
			}
		}
		out = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out), "```"))
	}

	// Empty, or a JSON value (object / array / scalar) → not prose.
	if out == "" || strings.HasPrefix(out, "{") || strings.HasPrefix(out, "[") || json.Valid([]byte(out)) {
		return ""
	}
	return out
}

// composeDeterministicNarrative builds a templated recommender narrative
// from the deterministic dose entries when the Recommender engine is
// unavailable. Per AC-F4.3 this MUST NOT be empty.
func composeDeterministicNarrative(entries []companion.DailyDoseEntry) string {
	if len(entries) == 0 {
		return "No atoms queued today. Explore the discovery graph for inspiration."
	}
	review, newCnt, stretch := 0, 0, 0
	for _, e := range entries {
		switch doseReasonToCategory(e.DoseReason) {
		case "review":
			review++
		case "stretch":
			stretch++
		default:
			newCnt++
		}
	}
	parts := make([]string, 0, 3)
	if review > 0 {
		parts = append(parts, "revisit "+itoa(review)+" from earlier")
	}
	if newCnt > 0 {
		parts = append(parts, "explore "+itoa(newCnt)+" new")
	}
	if stretch > 0 {
		parts = append(parts, "stretch on "+itoa(stretch)+" weak spots")
	}
	return "Today you'll " + strings.Join(parts, ", ") + "."
}

// nextDoseISO returns the ISO 8601 timestamp of the next dose boundary —
// the next midnight UTC after `now`. The FE renders this as a countdown
// chip ("next dose in 4h 23m").
func nextDoseISO(now time.Time) string {
	t := now.UTC()
	tomorrow := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	return tomorrow.Format(time.RFC3339)
}

// composeDoseID builds a deterministic dose id from (tenant, gcid, date).
// Same gcid + same date = same id so refreshes don't bump it.
func composeDoseID(tenantID, gcid string, now time.Time) string {
	return "dose-" + now.UTC().Format("2006-01-02") + "-" + shortHash(tenantID+"|"+gcid)
}

// shortHash returns the first 12 hex chars of a non-cryptographic FNV-1a
// digest of s. Stable across runs.
func shortHash(s string) string {
	const fnvOffset uint64 = 14695981039346656037
	const fnvPrime uint64 = 1099511628211
	h := fnvOffset
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime
	}
	return paddedHex(int64(h), 12)
}

func (s *Server) handleDailyDose(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}

	// F4 (Phyllis demo unblock, 2026-05-13) — graceful degradation per
	// docs/m13/HANDOFF_BE_F2_F4_PHYLLIS_BLOCKERS_2026-05-13.md §3.5:
	// when engine resources / clients aren't available the handler still
	// returns 200 with deterministic SM-2 + Ebbinghaus picks + templated
	// greeting + templated narrative. This is the explicit exception to
	// the m14.iter5 "big-bang fail-loud" directive — degraded > absent.
	enginesAvailable := s.CompanionEngineResource != "" &&
		s.RecommenderEngineResource != "" &&
		s.CompanionEngine != nil &&
		s.RecommenderEngine != nil

	now := time.Now().UTC()

	// L4 Fix-1 + fail-loud directive: a repo ERROR building the universe is a
	// 500 — never silently fall back to synthetic atoms on failure.
	rlsCtx := repoCtx(r, tenantID, gcid)

	// S5.1 — wire the TopicAccuracy + ActivePathTopics inputs into the 40/30/30
	// composer.
	//
	// CHO-2167: these are pg-backed in production, so the read needs the RLS ctx
	// (the policies on both tables are live for chora_consumption_app_rw — it is
	// not the table owner and cannot bypass RLS, so a missing tenant session
	// returns ZERO rows rather than erroring). Reads are now fallible and fail
	// loud: a storage error must be a 500, never an empty topic-set silently
	// degrading the learner's dose into "no weaknesses, no curiosity".
	var topicAccuracy map[string]float64
	if s.TopicAccuracy != nil {
		var accErr error
		topicAccuracy, accErr = s.TopicAccuracy.GetByLearner(rlsCtx, tenantID, gcid)
		if accErr != nil {
			writeError(w, http.StatusInternalServerError, "TOPIC_ACCURACY_READ_FAILED", accErr.Error())
			return
		}
	}
	var activePathTopics map[string]bool
	if s.ActivePathTopics != nil {
		var pathErr error
		activePathTopics, pathErr = s.ActivePathTopics.GetTopics(rlsCtx, tenantID, gcid)
		if pathErr != nil {
			writeError(w, http.StatusInternalServerError, "ACTIVE_PATH_TOPICS_READ_FAILED", pathErr.Error())
			return
		}
	}
	doseSeeds, seedsErr := s.doseAtomUniverse(rlsCtx, tenantID, gcid)
	if seedsErr != nil {
		writeError(w, http.StatusInternalServerError, "DOSE_UNIVERSE_FAILED", seedsErr.Error())
		return
	}
	sm2States, statesErr := s.SM2.AllStatesForLearner(rlsCtx, tenantID, gcid)
	if statesErr != nil {
		writeError(w, http.StatusInternalServerError, "DOSE_STATE_FAILED", statesErr.Error())
		return
	}
	seenTopics, seenErr := s.SM2.SeenTopics(rlsCtx, tenantID, gcid)
	if seenErr != nil {
		writeError(w, http.StatusInternalServerError, "DOSE_STATE_FAILED", seenErr.Error())
		return
	}

	doseInput := companion.DailyDoseInput{
		Seeds:                doseSeeds,
		States:               sm2States,
		SeenTopics:           seenTopics,
		ActivePathTopics:     activePathTopics,
		TopicAccuracy:        topicAccuracy,
		Now:                  now,
		WeaknessThresholdMin: companion.DefaultWeaknessThreshold,
		// ADR-224 (CHO-2045): when enabled, the weakness + curiosity slots are
		// subject/KG-balanced and sampled with a day-stable RNG seeded by
		// (date, gcid) — keeping the same-day dose (and its mana idempotency +
		// dose ID, both (tenant,gcid,date)-derived) stable while varying it
		// day-to-day. Dark by default ⇒ v1 strict fill (byte-identical).
		ComposerV2: s.ComposerV2Enabled,
		RandSeed:   companion.DoseSeed(doseclock.Key(now), gcid),
	}

	// Phase 2A — focused practice session: an optional ?growth_edge_id={uuid}
	// scopes the WHOLE dose to drilling ONE Growth Edge. When present we load
	// that single edge, resolve its GRADABLE cached drill atoms, and AUGMENT the
	// dose universe with them (CHO-1895) so they are servable even when not on an
	// enrolled LearningPath — then set FocusEdgeID so ComposeDailyDose fills the
	// weakness slot from those cached atoms ONLY (no slug fallback; every focused
	// weakness pick can recover its edge via RecoverByDrillAtomID). Absent ⇒ the
	// learner's top-10 Growth Edges feed the weakness slot (byte-identical to the
	// prior behaviour). Both paths fail-soft: a read error leaves the legacy
	// accuracy slot / a normal dose in charge — the dose NEVER breaks on overlay
	// enrichment.
	if growthEdgeID := r.URL.Query().Get("growth_edge_id"); growthEdgeID != "" {
		focusEdges, drillSeeds := s.doseFocusedGrowthEdge(repoCtx(r, tenantID, gcid), tenantID, gcid, growthEdgeID)
		doseInput.GrowthEdges = focusEdges
		doseInput.FocusEdgeID = growthEdgeID
		// ADR-242 D6: this merge is OUTSIDE any goal partition by
		// construction, and a focused drill is deliberately NOT goal-scoped
		// (an explicit focused request outranks the ambient goal context;
		// CHO-1895 keeps unenrolled drill atoms servable). When a goal_id
		// rides the same request, off-goal focused cards are counted Broader
		// by the D2 split, which is the required collision disclosure.
		doseInput.Seeds = mergeAtomSeeds(doseInput.Seeds, drillSeeds)
	} else {
		// W6 (Epic-1b): the learner's Growth Edges own the weakness slot
		// when present (cached drill atoms preferred). Fail-soft: a read
		// error leaves the legacy accuracy slot in charge.
		doseInput.GrowthEdges = s.doseGrowthEdges(repoCtx(r, tenantID, gcid), tenantID, gcid)

		// ADR-227 D12 (CHO-2081) + D13 (CHO-2082): thread TODAY's one
		// campaign march into the composer (focused practice above wins — no
		// campaign on a focused dose). WS-C3: the slot fills retrieval-first
		// at the serve rung; a true gap fires the bounded qgen request (the
		// trace context rides the cross-service envelope). Fail-soft: any
		// election failure leaves Campaign nil and the dose degenerates
		// byte-identically. Unenrolled gradable march atoms augment the
		// universe (CHO-1895 idiom).
		campTraceparent, campTracestate := traceFromHeaders(r)
		campaignIn, campaignSeeds := s.doseCampaignInput(rlsCtx, tenantID, gcid, doseInput.RandSeed, now, campTraceparent, campTracestate)
		doseInput.Campaign = campaignIn
		// ADR-242 D6: this merge is OUTSIDE any goal partition by
		// construction, and that is fine: the ADR-227/228 march is already
		// goal-scoped by its own upstream election, so its cards classify as
		// goal material in the D2 split whenever the elected goal matches.
		doseInput.Seeds = mergeAtomSeeds(doseInput.Seeds, campaignSeeds)
	}

	// WS-3 + ADR-242: an optional ?goal_id={uuid} scopes the dose to the goal
	// the learner is browsing: its material leads the biased slots and then the
	// served cards, and the response discloses the split (D2). It is a BIAS,
	// never a filter: a thin goal degrades to the learner-wide dose at full
	// budget rather than stunting it, so this is also safe on the
	// focused-practice path above. The goal's bound atoms augment the universe
	// (the CHO-1895 idiom: a goal's material need not be enrolled).
	// Deliberately does NOT re-elect the campaign march: the ADR-227 D12
	// election is shared with the ANSWER side, which has no goal_id, so pinning
	// it here would desynchronise the two and silently drop ladder progress.
	if goalID := doseGoalIDFromQuery(r); goalID != "" {
		// ADR-242 D3 (owner re-ruled 2026-08-17): a goal-resolution repo error
		// 500s. Fail-soft here silently serves an unscoped dose under a
		// goal-scoped heading, and the D2 scope block below could not be
		// rendered honestly for a goal that failed to read.
		goalScope, goalSeeds, goalErr := s.doseGoalScope(rlsCtx, tenantID, gcid, goalID)
		if goalErr != nil {
			writeError(w, http.StatusInternalServerError, "DOSE_GOAL_SCOPE_FAILED", goalErr.Error())
			return
		}
		doseInput.GoalScope = goalScope
		// ADR-242 D6: this merge happens AFTER the universe assembly, so the
		// goal's own bound atoms join the pool here; the focused-drill and
		// campaign merges above are OUTSIDE the goal partition by construction
		// (an off-goal focused card is counted Broader by the D2 split).
		doseInput.Seeds = mergeAtomSeeds(doseInput.Seeds, goalSeeds)
	}

	// BE-EP1 — when a learner has an active exam-prep goal AND the
	// orchestrator port is configured, swap the curiosity slot for
	// weakness-drill picks. Otherwise the 40/30/30 composition runs.
	// Trace context (W3C traceparent + tracestate) is propagated to the
	// orchestrator so spans link across the service boundary.
	traceparent, tracestate := traceFromHeaders(r)
	doseCtx := r.Context()
	doseCtx = clients.WithTraceparent(doseCtx, traceparent)

	var goal *companion.ExamPrepGoal
	if s.ExamPrepGoals != nil {
		if g, ok := s.ExamPrepGoals.Get(tenantID, gcid); ok {
			goal = g
		}
	}

	// BE-USR-2 — pre-flight mana debit on the LLM-backed coach path. The
	// idempotency key is composed from (gcid, tenant, date) so a same-day
	// refresh re-uses the key and never double-debits. Surface
	// ErrInsufficientMana as 402 with the upsell payload; other errors fall
	// through to a 500.
	idemKey := manaIdempotencyKey(tenantID, gcid, now)
	dose, err := companion.ComposeDailyDoseWithMana(
		doseCtx, doseInput, s.ExamPrepCoach, goal, s.ManaQuoter, idemKey, tenantID,
	)
	if err != nil {
		var ime *companion.ErrInsufficientMana
		if errors.As(err, &ime) {
			writeJSON(w, http.StatusPaymentRequired, map[string]any{
				"error":      "INSUFFICIENT_MANA",
				"required":   ime.RequiredUnits,
				"balance":    ime.CurrentBalance,
				"upsell_url": "/me/marketplace/companion-plans",
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "DOSE_COMPOSE_FAILED", err.Error())
		return
	}

	// Companion handle (for greeting + FE companionName / companionLevel).
	companionID := pickCompanionID(s, tenantID, gcid)
	manaTier := "standard" // engine's manaplugin overwrites with the live tier; this is just a hint

	// Resolve the user's Companion entity (for FE companionName + companionLevel
	// + the degraded-path templated greeting).
	var companionName string
	companionLevel := 1
	if s != nil && s.Companions != nil {
		if f, err := s.Companions.GetByGCID(tenantID, gcid); err == nil && f != nil {
			companionName = f.Name
			if f.Level > 0 {
				companionLevel = f.Level
			}
		}
	}
	if companionName == "" {
		companionName = "Eira"
	}

	// AI enrichment (Companion greeting + Recommender picks) no longer runs
	// SYNCHRONOUSLY on the dose hot path (dailyDoseSyncEngines=false). The old
	// 3s engineCtx (AC-F4.3) always cancelled the ~16s LLM round-trip → a wasted
	// gateway call with no picks. The FE now fetches the enrichment async from
	// GET /companion/daily-dose/ai (runs the engines to completion + meters). The
	// dose returns instantly with the deterministic SM-2 composition + templated
	// greeting. The legacy block is gated (not deleted) so the behaviour stays
	// reversible without re-threading this handler.
	engineCtx, engineCancel := context.WithTimeout(doseCtx, 3*time.Second)
	defer engineCancel()

	engineMeta := &dailyDoseEngineMetadata{}
	var (
		greeting             string
		aiPicks              []string
		recommenderNarrative string
		degraded             = !enginesAvailable
	)

	if enginesAvailable && dailyDoseSyncEngines {
		// Companion greeting.
		greetResp, gerr := s.CompanionEngine.Greet(engineCtx, clients.CompanionGreetRequest{
			TenantID:    tenantID,
			UserGCID:    gcid,
			CompanionID: companionID,
			ManaTier:    manaTier,
		})
		if gerr != nil {
			// Engine call failed — degrade to templated greeting instead
			// of 502'ing the entire request (F4 §3.5 graceful-degradation
			// authorisation). The Degraded flag surfaces the partial state
			// so the FE can render a banner if it wants.
			degraded = true
		} else {
			greeting = greetResp.Greeting
			engineMeta.CompanionModel = greetResp.Model
			engineMeta.CompanionOutputTokens = greetResp.OutputTokens
		}

		// Recommender picks / narrative. Inject pre-fetched atom_index candidates
		// (CHO-1662 session-state-injection RAG) here too so the legacy sync path
		// shares the mesh-free content source. Soft-fail (nil/error/empty → no
		// candidates → stub fallback).
		syncTopicHint, syncHintErr := pickTopicHint(rlsCtx, s, tenantID, gcid)
		if syncHintErr != nil {
			writeError(w, http.StatusInternalServerError, "DOSE_STATE_FAILED", syncHintErr.Error())
			return
		}
		recResp, rerr := s.RecommenderEngine.Recommend(engineCtx, clients.RecommendRequest{
			TenantID:       tenantID,
			UserGCID:       gcid,
			ManaTier:       manaTier,
			LearnerPersona: pickLearnerPersona(s, tenantID, gcid),
			TopicHint:      syncTopicHint,
			Candidates:     prefetchRecommendCandidates(engineCtx, s, tenantID, gcid, syncTopicHint),
		})
		if rerr != nil {
			degraded = true
		} else {
			aiPicks = recResp.AtomIDs
			recommenderNarrative = proseNarrative(recResp.Narrative)
			engineMeta.RecommenderModel = recResp.Model
			engineMeta.RecommenderOutputTokens = recResp.OutputTokens
		}
	}

	// Degraded-mode fallbacks — populated when engines are absent OR when
	// either engine errored. Templated copy keeps the FE rendering
	// happy-path UI without surfacing a network failure to the user.
	if greeting == "" {
		greeting = composeDeterministicGreeting(companionName, companionLevel)
	}
	if recommenderNarrative == "" && len(aiPicks) == 0 {
		recommenderNarrative = composeDeterministicNarrative(dose.Entries)
	}
	if degraded || !dailyDoseSyncEngines {
		// Zero the engine metadata so consumers don't get half-populated
		// fingerprints from a failed call. When the sync engines are off
		// (async enrichment path) there is no per-dose engine fingerprint —
		// the FE reads it from GET /companion/daily-dose/ai instead.
		engineMeta = nil
	}

	// N-Companion dispatch (CHO-1577) — given the composed dose topic
	// distribution, pick which hatched Companion greets the learner.
	//
	// We build a RosterCompanion slice from CompanionInstances (the 1:N
	// Instance repository). Pre-hatch Companions (Stage 0 / Mystery Egg)
	// are filtered inside SelectGreetingCompanion. When CompanionInstances
	// is not wired (e.g., legacy Companions-only mode) we fall back to the
	// existing companionName / companionLevel path with no greeting_from.
	atomBreakdown := companion.BuildAtomBreakdown(dose.Entries)
	var greetingFrom *companion.GreetingFrom
	if s.CompanionInstances != nil {
		// RLS-context propagation (debt #40 class): the pgx repo's
		// rls.ApplySession reads tenant_id + gcid via
		// tracing.{TenantID,GCID}FromContext and returns ErrNoTenantContext
		// when they are empty. Passing a bare r.Context() here made
		// ListRosterByOwner error against the pg repo, silently skipping the
		// N-Companion dispatch block and dropping greeting_from (CHO-1577).
		// Use repoCtx to inject the RLS context, mirroring /v1/me/companions.
		rosterEntries, rErr := s.CompanionInstances.ListRosterByOwner(
			repoCtx(r, tenantID, gcid), tenantID, gcid,
		)
		if rErr == nil && len(rosterEntries) > 0 {
			// Project to the lightweight RosterCompanion slice.
			rosterCompanions := make([]companion.RosterCompanion, 0, len(rosterEntries))
			for _, entry := range rosterEntries {
				if entry == nil || entry.Instance == nil {
					continue
				}
				stage := 0
				exp := 0
				if entry.Growth != nil {
					stage = entry.Growth.Stage
					exp = entry.Growth.Exp
				}
				rosterCompanions = append(rosterCompanions, companion.RosterCompanion{
					CompanionID:    entry.Instance.CompanionID,
					Name:           entry.Instance.Name,
					Specialization: entry.Instance.Specialization,
					GrowthStage:    stage,
					GrowthExp:      exp,
				})
			}
			// Run dispatch.
			greetCompanionID, tieReason := companion.SelectGreetingCompanion(rosterCompanions, dose)
			if greetCompanionID != "" {
				// Look up the winner's name + growth stage + voice_accent
				// from the roster.
				var greetName, voiceAccent string
				greetLevel := 0
				for _, rf := range rosterCompanions {
					if rf.CompanionID == greetCompanionID {
						greetName = rf.Name
						greetLevel = rf.GrowthStage
						break
					}
				}
				// voice_accent from the RosterEntry.Instance.ConfiguredRules
				for _, entry := range rosterEntries {
					if entry != nil && entry.Instance != nil &&
						entry.Instance.CompanionID == greetCompanionID {
						voiceAccent = entry.Instance.ConfiguredRules["voice_accent"]
						break
					}
				}
				greetingFrom = &companion.GreetingFrom{
					CompanionID:    greetCompanionID,
					Name:           greetName,
					VoiceAccent:    voiceAccent,
					TieBreakReason: tieReason,
				}
				// Override companionName / companionLevel from the winning Companion
				// so the legacy FE fields stay consistent. The response's
				// companion_level MUST reflect the GREETING Companion's
				// progression — its ADR-149 GrowthStage — mirroring how the
				// first Companion fed companionLevel from f.Level above.
				// SelectGreetingCompanion filters pre-hatch (Stage 0) candidates,
				// so a selected greeter is always Stage >= 1.
				if greetName != "" {
					companionName = greetName
				}
				if greetLevel > 0 {
					companionLevel = greetLevel
				}
				// Recompose the deterministic greeting prose from the GREETING
				// Companion's (now-overridden) name + level so companion_quote stays
				// consistent with companion_name / companion_level. Without this the
				// prose still names the first/legacy Companion composed pre-dispatch
				// — the speech bubble says "Newton here…" while the name/level chip
				// shows the greeter ("Aria / Stage 5"). Guarded to the dispatch
				// winner so the single-Companion path is unchanged; deterministic
				// path is the live one (sync engines default OFF).
				greeting = composeDeterministicGreeting(companionName, companionLevel)
				// Override companionID used by engine calls.
				companionID = greetCompanionID
			}
		}
	}

	// Emit chora.consumption.daily_dose.served.v1 with full IMDA D1
	// (accountability) + lifecycle_stage=runtime evidence per ADR-141.
	// CHO-1577: envelope now includes companion_id of the greeting Companion
	// + atom_breakdown for per-Companion cost attribution (IMDA D2).
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, "")
	atomIDs := make([]string, 0, len(dose.Entries))
	for _, e := range dose.Entries {
		atomIDs = append(atomIDs, e.AtomID)
	}
	greetingCompanionID := ""
	if greetingFrom != nil {
		greetingCompanionID = greetingFrom.CompanionID
	}
	if err := s.Publisher.Publish(events.TopicDailyDoseServed, env, map[string]any{
		"learner_gcid":          gcid,
		"atom_ids":              atomIDs,
		"size":                  len(dose.Entries),
		"served_at":             now,
		"chora_imda_dimension":  "accountability",
		"imda_lifecycle_stage":  "runtime",
		"composition_algorithm": "40_30_30_v1",
		// CHO-1577 additions.
		"companion_id":   greetingCompanionID,
		"atom_breakdown": atomBreakdown,
		"tie_break_reason": func() string {
			if greetingFrom != nil {
				return greetingFrom.TieBreakReason
			}
			return ""
		}(),
	}); err != nil {
		// Non-fatal: log + continue. Demo flow should not 500 on event
		// publish drop. Real Pub/Sub uses outbox in M12 (data-consistency).
		writeError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	// S5.1 Maya retention loop — record activity (drives streak counter)
	// and award the dose-completed XP bonus. Per the spec the bonus
	// fires when the learner reaches the dose surface (engagement signal),
	// not on per-card completion which is per-atom XP elsewhere.
	if s.Streaks != nil {
		if err := s.Streaks.RecordActivity(rlsCtx, tenantID, gcid, now); err != nil {
			writeError(w, http.StatusInternalServerError, "STREAK_WRITE_FAILED", err.Error())
			return
		}
	}
	if s.XP != nil {
		if err := s.XP.Award(rlsCtx, tenantID, gcid,
			companion.XPForActivity(companion.ActivityDailyDoseCompleted)); err != nil {
			writeError(w, http.StatusInternalServerError, "XP_WRITE_FAILED", err.Error())
			return
		}
	}

	feAtoms, totalXP := composeFEAtoms(s, dose.Entries)

	writeJSON(w, http.StatusOK, dailyDoseResp{
		// Legacy fields (back-compat).
		GeneratedAt:          dose.GeneratedAt,
		Message:              dose.Message,
		Entries:              dose.Entries,
		Greeting:             greeting,
		AIPicks:              aiPicks,
		RecommenderNarrative: recommenderNarrative,
		EngineMetadata:       engineMeta,
		Degraded:             degraded,
		// chora-web FE shape (per chora-web/src/app/features/surfaces/aplus/
		// daily-dose/daily-dose.model.ts).
		DoseID:           composeDoseID(tenantID, gcid, now),
		ServedOn:         now.Format("2006-01-02"),
		Atoms:            feAtoms,
		Composition:      composeDoseComposition(dose.Entries),
		TotalXPAvailable: totalXP,
		NextDoseAt:       nextDoseISO(now),
		CompanionName:    companionName,
		CompanionLevel:   companionLevel,
		CompanionQuote:   greeting,
		// N-Companion dispatch fields (CHO-1577).
		GreetingFrom:  greetingFrom,
		AtomBreakdown: atomBreakdown,
		// ADR-242 D2: the served-card split for a goal-scoped dose. Nil (and
		// therefore absent on the wire) when no goal was requested (D5).
		Scope: dose.Scope,
	})
}

// dailyDoseSyncEngines gates the LEGACY synchronous AI enrichment on the dose
// hot path. Default OFF — the Companion greeting + Recommender picks run async
// via GET /companion/daily-dose/ai (generous timeout; the gateway LLM call
// COMPLETES + meters). The old 3s synchronous budget always cancelled the ~16s
// round-trip (wasted gateway call, no picks). Kept as a flag so the legacy path
// stays reversible without re-threading handleDailyDose.
var dailyDoseSyncEngines = false

// dailyDoseAITimeout caps the async AI-enrichment engine round-trips. Generous
// (not page-load-blocking): the Recommender runs ~16s warm, longer on a cold
// model fallback. Must stay under the gateway daily-dose route timeout + the
// consumption http.Server WriteTimeout.
const dailyDoseAITimeout = 30 * time.Second

// dailyDoseAIResp is the async AI-enrichment payload the FE fetches AFTER the
// deterministic dose renders. greeting + ai_picks + narrative complete to the
// engine timeout (no 3s cancel) so the Recommender + Companion gateway LLM calls
// finish + meter (mana umbrella).
type dailyDoseAIResp struct {
	Greeting       string                   `json:"greeting"`
	AIPicks        []string                 `json:"ai_picks"`
	Narrative      string                   `json:"narrative"`
	EngineMetadata *dailyDoseEngineMetadata `json:"engine_metadata,omitempty"`
	Degraded       bool                     `json:"degraded"`
}

// handleDailyDoseAI runs the Companion greeting + Recommender picks to
// COMPLETION (generous timeout) and returns them for the FE to weave into the
// already-rendered deterministic dose (async enrichment). Auth: X-Tenant-Id +
// gcid (same as handleDailyDose). GET only.
func (s *Server) handleDailyDoseAI(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}

	// Use the learner's REAL instance id (not the stub canonical UUID) so the
	// greet's companion_config resolves + injects — otherwise the sidecar-less
	// agent refuses the turn (degraded greeting).
	companionID := resolveLearnerCompanionID(r.Context(), s, tenantID, gcid)
	persona := pickLearnerPersona(s, tenantID, gcid)

	companionName := "Eira"
	companionLevel := 1
	if s != nil && s.Companions != nil {
		if f, ferr := s.Companions.GetByGCID(tenantID, gcid); ferr == nil && f != nil {
			if f.Name != "" {
				companionName = f.Name
			}
			if f.Level > 0 {
				companionLevel = f.Level
			}
		}
	}

	// Propagate W3C trace context across the engine boundary (Cloud Trace).
	// RLS-scope the ctx too — the topic-hint SeenTopics read goes through the
	// pg-backed SM2Store (rls.ApplySession needs tenant_id on ctx).
	traceparent, _ := traceFromHeaders(r)
	ctx := clients.WithTraceparent(repoCtx(r, tenantID, gcid), traceparent)

	greeting, aiPicks, narrative, meta, degraded, engErr := runDailyDoseEngines(
		ctx, s, tenantID, gcid, companionID, persona, companionName, companionLevel,
	)
	if engErr != nil {
		// Fail loud: infrastructure (storage) failure, NOT LLM degradation.
		writeError(w, http.StatusInternalServerError, "DOSE_STATE_FAILED", engErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, dailyDoseAIResp{
		Greeting:       greeting,
		AIPicks:        aiPicks,
		Narrative:      narrative,
		EngineMetadata: meta,
		Degraded:       degraded,
	})
}

// runDailyDoseEngines runs the Companion greeting + Recommender picks with a
// GENEROUS timeout (async enrichment — NOT the hot dose load). Unlike the legacy
// 3s synchronous budget this lets the ~16s LLM round-trips COMPLETE so the
// gateway meters the calls (mana umbrella) and real picks/narrative return. On
// engine error / missing config it degrades to the templated greeting (same
// graceful-degradation contract as the dose); narrative stays empty so the FE
// keeps its already-rendered deterministic narrative.
func runDailyDoseEngines(
	ctx context.Context, s *Server, tenantID, gcid, companionID, persona, companionName string, companionLevel int,
) (greeting string, aiPicks []string, narrative string, meta *dailyDoseEngineMetadata, degraded bool, err error) {
	enginesAvailable := s.CompanionEngineResource != "" &&
		s.RecommenderEngineResource != "" &&
		s.CompanionEngine != nil &&
		s.RecommenderEngine != nil
	meta = &dailyDoseEngineMetadata{}
	degraded = !enginesAvailable

	if enginesAvailable {
		ec, cancel := context.WithTimeout(ctx, dailyDoseAITimeout)
		defer cancel()

		const manaTier = "standard" // engine manaplugin overwrites with the live tier

		// Resolve the per-Companion config so the sidecar-less Companion agent can
		// read it from session state (bug-3 mesh-sidestep, mirrors the chat path).
		// WITHOUT it the instancedispatch resolver refuses the turn → empty
		// greeting (iteration_count:0). Soft-fail (graceful-degradation contract):
		// a resolve error leaves the config empty → the greet degrades to the
		// templated line rather than 5xx'ing the async enrichment.
		var companionConfigJSON string
		if s.CompanionConfigJSONResolver != nil {
			if cfgJSON, cerr := s.CompanionConfigJSONResolver(ec, tenantID, companionID, gcid); cerr != nil {
				log.Printf("consumption: daily-dose/ai companion config resolve failed (companion=%s, non-fatal): %v", companionID, cerr)
			} else {
				companionConfigJSON = cfgJSON
			}
		}

		if greetResp, gerr := s.CompanionEngine.Greet(ec, clients.CompanionGreetRequest{
			TenantID: tenantID, UserGCID: gcid, CompanionID: companionID, ManaTier: manaTier,
			CompanionConfigJSON: companionConfigJSON,
		}); gerr != nil {
			degraded = true
		} else {
			greeting = greetResp.Greeting
			meta.CompanionModel = greetResp.Model
			meta.CompanionOutputTokens = greetResp.OutputTokens
		}

		// Derive a topic_hint from the learner's active path (option a): gives the
		// recommender a topic to rank around. Optional — the recommender returns
		// picks without it (the tool defaults the topic). A STORAGE error here
		// is infrastructure failure, not LLM degradation → propagate (fail loud).
		topicHint, hintErr := pickTopicHint(ctx, s, tenantID, gcid)
		if hintErr != nil {
			err = hintErr
			return
		}

		// CHO-1662 session-state-injection RAG: pre-fetch the candidate atoms
		// in-process from atom_index and inject them into the RecommendRequest.
		// consumption OWNS atom_index and already initiates this call, so the
		// sidecar-less crew never has to dial consumption's STRICT-mTLS :9090 back.
		// Soft-fail (graceful-degradation contract): a nil AtomIndex, an error, or
		// zero candidates leaves Candidates empty — the recommender degrades to its
		// stub searcher; the async enrichment never 5xx's.
		candidates := prefetchRecommendCandidates(ec, s, tenantID, gcid, topicHint)

		if recResp, rerr := s.RecommenderEngine.Recommend(ec, clients.RecommendRequest{
			TenantID: tenantID, UserGCID: gcid, ManaTier: manaTier, LearnerPersona: persona,
			TopicHint: topicHint, Candidates: candidates,
		}); rerr != nil {
			degraded = true
		} else {
			aiPicks = recResp.AtomIDs
			narrative = proseNarrative(recResp.Narrative)
			meta.RecommenderModel = recResp.Model
			meta.RecommenderOutputTokens = recResp.OutputTokens
		}
	}

	if greeting == "" {
		greeting = composeDeterministicGreeting(companionName, companionLevel)
	}
	if degraded {
		meta = nil
	}
	return
}

// Canonical Companion UUIDs known to the deployed Companion engine's
// POC skillregistry stub
// (services/chora-ai-kernel-orchestrator/agents/companion_chat_adk_go/internal/skillregistry/registry.go).
//
// M14.iter5F (2026-05-13) dress-rehearsal root cause: the BFF was sending
// the bare lowercase Companion name ("newton") in CompanionGreetRequest.CompanionID,
// but the engine's instancedispatch resolver only recognises these three
// canonical UUIDs. Mismatched ids return ErrCompanionNotFound from the
// resolver, which surfaces as an empty SSE stream + the fail-loud
// "companion engine returned empty greeting" 502.
//
// M14.iter6 will replace the static map with a chora_consumption.companion_instances
// projection keyed by (tenant_id, gcid); the values will then come from
// the live DB row instead of this constant block.
const (
	stubCompanionIDNewton   = "01957c8c-1111-7000-aaaa-1111aaaa1111"
	stubCompanionIDCurie    = "01957c8c-2222-7000-bbbb-2222bbbb2222"
	stubCompanionIDLovelace = "01957c8c-3333-7000-cccc-3333cccc3333"
)

// pickCompanionID derives the Companion instance id for the engine call.
//
// M14.iter6 will replace this with a lookup in chora_consumption.companion_instances;
// for now we map the Companion's name (Newton / Curie / Lovelace) to the
// canonical UUID the deployed engine's stub registry knows. Unknown names
// fall back to Newton — keeping the Phyllis happy-path live.
func pickCompanionID(s *Server, tenantID, gcid string) string {
	if s == nil || s.Companions == nil {
		return stubCompanionIDNewton
	}
	if f, err := s.Companions.GetByGCID(tenantID, gcid); err == nil && f != nil && f.Name != "" {
		return companionNameToCanonicalID(f.Name)
	}
	return stubCompanionIDNewton
}

// companionNameToCanonicalID resolves a Companion's display name to the
// canonical UUID the engine's POC stub registry recognises. Case-insensitive.
// Unknown names default to Newton so the daily dose stays available.
func companionNameToCanonicalID(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "curie":
		return stubCompanionIDCurie
	case "lovelace":
		return stubCompanionIDLovelace
	case "newton":
		return stubCompanionIDNewton
	default:
		// Until M14.iter6 wires the per-user Companion projection, every
		// summoned Companion (regardless of user-chosen name like "Eira")
		// maps to Newton — the canonical math Companion the engine ships
		// stub config for. Keeps Phyllis demo live without 502s.
		return stubCompanionIDNewton
	}
}

// pickLearnerPersona derives the recommender's persona axis. M14.iter6
// reads this from chora_consumption.learner_persona; for now we return
// the canonical Phyllis default.
func pickLearnerPersona(s *Server, _, _ string) string {
	// Intentionally accepts (tenantID, gcid) so the future signature
	// stays stable when the projection lands.
	_ = s
	return "curious-explorer"
}

// resolveLearnerCompanionID returns the learner's REAL companion instance id — the
// one CompanionInstances + ResolveCompanionInstanceConfig know — so the daily-dose
// greet's companion_config resolves and gets injected into the engine session.
// pickCompanionID maps the Companion's NAME to a stub canonical UUID
// (01957c8c-…), which is NOT a real CompanionInstances row → the resolver returns
// "companion.instance: not found" → no config → the sidecar-less agent refuses the
// greet (iteration_count:0, degraded). The chat path avoids this by carrying the
// real companion_id in its URL. Falls back to the stub when no real instance is
// found (greet then degrades to the templated line — graceful). Deterministic:
// a multi-Companion learner always greets via the lexicographically-first instance
// (IMDA D2; ListByOwner order is unspecified).
func resolveLearnerCompanionID(ctx context.Context, s *Server, tenantID, gcid string) string {
	if s != nil && s.CompanionInstances != nil {
		// Scope the ctx with tenant + gcid so the pg repo's RLS session GUC
		// (rls.ApplySession reads tracing.TenantIDFromContext) selects the
		// learner's rows. Without this the RLS predicate filters everything →
		// empty result → stub fallback → greet degrades. Mirrors how
		// ResolveCompanionInstanceConfig scopes its own ctx internally. The inmem
		// repo ignores the tenant GUC, so tests are unaffected.
		rctx := tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)
		if insts, err := s.CompanionInstances.ListByOwner(rctx, tenantID, gcid); err == nil && len(insts) > 0 {
			best := ""
			for _, inst := range insts {
				if inst == nil {
					continue
				}
				id := strings.TrimSpace(inst.CompanionID)
				if id != "" && (best == "" || id < best) {
					best = id
				}
			}
			if best != "" {
				return best
			}
		}
	}
	return pickCompanionID(s, tenantID, gcid)
}

// recommendCandidateLimit caps how many atom_index candidates the daily-dose/ai
// handler pre-fetches and injects into the recommender session state. The crew
// ranks/picks a smaller set (3-5) from these; 8 gives it headroom to reorder by
// persona/topic without a second round trip. Stays within the atom_index Repo's
// own 1..10 clamp.
const recommendCandidateLimit = 8

// prefetchRecommendCandidates pre-fetches candidate atoms from the local
// atom_index projection (CHO-1662 session-state-injection RAG) so the
// sidecar-less recommender crew never has to dial consumption's STRICT-mTLS
// :9090 back for content. consumption owns atom_index and already initiates the
// Recommend call, so this in-process read is lower-latency + mesh-free.
//
// The ctx is scoped with the tenant + gcid (tracing.WithTenantID/WithGCID) so
// the pg repo's rls.ApplySession selects the tenant's rows — WITHOUT this the
// RLS predicate filters everything → zero candidates (the bug-3 lesson). Soft-
// fail: a nil AtomIndex, an error, or zero results returns nil so the caller
// degrades to the recommender's stub searcher rather than 5xx'ing.
func prefetchRecommendCandidates(ctx context.Context, s *Server, tenantID, gcid, topicHint string) []clients.AtomCandidate {
	if s == nil || s.AtomIndex == nil {
		return nil
	}
	// RLS-bearing ctx — the pg repo reads tenant_id (+gcid) from the context GUC.
	rctx := tracing.WithTenantID(tracing.WithGCID(ctx, gcid), tenantID)
	atoms, err := s.AtomIndex.SearchForLearner(rctx, tenantID, topicHint, recommendCandidateLimit)
	if err != nil {
		log.Printf("consumption: daily-dose/ai atom_index pre-fetch failed (tenant=%s, non-fatal): %v", tenantID, err)
		return nil
	}
	if len(atoms) == 0 {
		return nil
	}
	candidates := make([]clients.AtomCandidate, 0, len(atoms))
	for _, a := range atoms {
		if a == nil || strings.TrimSpace(a.AtomID) == "" {
			continue
		}
		// Snippet = the joined topic tags (lightweight relevance hook); fall back
		// to the title when an atom carries no tags so the snippet is never empty.
		snippet := strings.Join(a.TopicTags, ", ")
		if strings.TrimSpace(snippet) == "" {
			snippet = a.Title
		}
		candidates = append(candidates, clients.AtomCandidate{
			AtomID:  a.AtomID,
			Title:   a.Title,
			Snippet: snippet,
		})
	}
	return candidates
}

// pickTopicHint derives a deterministic topic_hint for the recommender from the
// learner's active path topics (preferred) or, failing that, the topics already
// seen via SM-2. Keys are sorted so the same learner state always yields the
// same hint (IMDA D2 transparency). Returns "" when no topic is known — the
// recommender tolerates an absent topic (the tool defaults it), so the daily-dose
// /ai path still returns picks for a brand-new learner.
func pickTopicHint(ctx context.Context, s *Server, tenantID, gcid string) (string, error) {
	if s == nil {
		return "", nil
	}
	if s.ActivePathTopics != nil {
		// CHO-2167: pg-backed in production — fallible, and fail-loud. Swallowing
		// the error here would silently drop the learner's real topic hint and
		// hand the recommender a default, which reads as "working".
		topics, err := s.ActivePathTopics.GetTopics(ctx, tenantID, gcid)
		if err != nil {
			return "", err
		}
		if t := firstSortedTopic(topics); t != "" {
			return t, nil
		}
	}
	if s.SM2 != nil {
		seen, err := s.SM2.SeenTopics(ctx, tenantID, gcid)
		if err != nil {
			return "", err
		}
		if t := firstSortedTopic(seen); t != "" {
			return t, nil
		}
	}
	return "", nil
}

// firstSortedTopic returns the lexicographically-first key whose value is true,
// or "" for an empty/nil map. Deterministic (map iteration order is not).
func firstSortedTopic(topics map[string]bool) string {
	if len(topics) == 0 {
		return ""
	}
	keys := make([]string, 0, len(topics))
	for k, ok := range topics {
		if ok && strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// ----- /atoms/{id}/feedback -----

func (s *Server) handleAtomsRoot(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/atoms/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "feedback" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "")
		return
	}
	atomID := parts[0]
	s.handleAtomFeedback(w, r, atomID)
}

func (s *Server) handleAtomFeedback(w http.ResponseWriter, r *http.Request, atomID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}

	// Look up the atom seed — atom_index projection first (real published
	// atoms; L4 Fix-1 ships this WITH the dose-universe swap so real
	// atom_ids never 404 here), synthetic catalogue fallback.
	rlsCtx := repoCtx(r, tenantID, gcid)
	seed, ok, resolveErr := s.resolveAtomSeed(rlsCtx, atomID)
	if resolveErr != nil {
		// Fail loud: a storage error must not masquerade as a 404.
		writeError(w, http.StatusInternalServerError, "ATOM_RESOLVE_FAILED", resolveErr.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "ATOM_NOT_FOUND", "")
		return
	}

	var req feedbackReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	grade := companion.Grade(req.Grade)
	if !companion.IsValidGrade(grade) {
		writeError(w, http.StatusBadRequest, "INVALID_GRADE",
			"grade must be one of i_remember | not_sure | forgot")
		return
	}

	// Apply SM-2 update to per-learner state (fail loud on storage errors —
	// a dropped SM-2 write silently starves the Ebbinghaus review slot).
	state, exists, stateErr := s.SM2.GetState(rlsCtx, tenantID, gcid, atomID)
	if stateErr != nil {
		writeError(w, http.StatusInternalServerError, "SM2_READ_FAILED", stateErr.Error())
		return
	}
	if !exists {
		state = companion.NewSM2State(atomID)
	}
	now := time.Now().UTC()
	if err := state.ApplyGrade(grade, now); err != nil {
		writeError(w, http.StatusBadRequest, "SM2_FAILED", err.Error())
		return
	}
	if err := s.SM2.SaveState(rlsCtx, tenantID, gcid, state); err != nil {
		writeError(w, http.StatusInternalServerError, "SM2_WRITE_FAILED", err.Error())
		return
	}
	if err := s.SM2.MarkTopicSeen(rlsCtx, tenantID, gcid, seed.Topic); err != nil {
		writeError(w, http.StatusInternalServerError, "SM2_WRITE_FAILED", err.Error())
		return
	}

	// Award XP per grade. Reward learner more for perfect recall but still
	// reward effort on partial recall (engagement loop primitive).
	xp := xpForGrade(grade)
	f, ferr := s.Companions.GetByGCID(tenantID, gcid)
	if ferr != nil {
		// Auto-bond a default companion to keep the demo flow alive (per
		// existing /api/companions convention). Production rejects with 404.
		f, _ = companion.New(tenantID, gcid, "Companion")
		s.Companions.Save(f)
	}
	leveledUp, trait := f.AwardXPWithTrait(xp)
	s.Companions.Save(f)

	// Emit chora.consumption.atom_session.completed.v1.
	traceparent, tracestate := traceFromHeaders(r)
	idempotencyKey := atomID + "|" + gcid + "|" + now.Format(time.RFC3339Nano)
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, idempotencyKey)
	if err := s.Publisher.Publish(events.TopicAtomSessionCompleted, env, map[string]any{
		"atom_id":         atomID,
		"learner_gcid":    gcid,
		"grade":           string(grade),
		"answer_correct":  grade == companion.GradeIRemember,
		"completed_at":    now,
		"xp_earned":       xp,
		"companion_level": f.Level,
		"leveled_up":      leveledUp,
		"unlocked_trait":  trait,
		"interval_days":   state.IntervalDays,
		"next_review_at":  state.NextReviewAt,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	resp := feedbackResp{
		AtomID:         atomID,
		Grade:          string(grade),
		NextReviewAt:   state.NextReviewAt,
		IntervalDays:   state.IntervalDays,
		Repetitions:    state.Repetitions,
		XPEarned:       xp,
		LeveledUp:      leveledUp,
		UnlockedTrait:  trait,
		CompanionLevel: f.Level,
		CompanionStats: f.Stats,
	}
	if leveledUp && trait != "" {
		// Comic Ch6 P14 nudge text: "Eira leveled up to 5! New trait:
		// Pattern Recognition" — interpolated with the actual companion
		// name + new level + trait.
		resp.NudgeMessage = nudgeForLevelUp(f.Name, f.Level, trait)
	}
	writeJSON(w, http.StatusOK, resp)
}

func xpForGrade(g companion.Grade) int {
	switch g {
	case companion.GradeIRemember:
		return 100
	case companion.GradeNotSure:
		return 50
	case companion.GradeForgot:
		return 10 // small "showing-up" reward — engagement loop principle
	default:
		return 0
	}
}

func nudgeForLevelUp(name string, level int, trait string) string {
	if name == "" {
		name = "Your Companion"
	}
	if trait == "" {
		return name + " leveled up to " + itoa(level) + "!"
	}
	return name + " leveled up to " + itoa(level) + "! New trait: " + trait
}

func itoa(n int) string {
	// avoid strconv import for the single-call site
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// traceFromHeaders extracts W3C trace context from incoming headers, or
// synthesizes a placeholder traceparent if absent (so events never violate
// the envelope mandatory-field rule).
//
// Real OTel SDK wiring (M12) replaces this with span context propagation.
func traceFromHeaders(r *http.Request) (string, string) {
	tp := r.Header.Get("traceparent")
	ts := r.Header.Get("tracestate")
	if tp == "" {
		// Synthesize a deterministic-shape traceparent from current time
		// to satisfy envelope validation. Production: NEVER fall back —
		// SDK always provides one.
		tp = "00-" + paddedHex(time.Now().UnixNano(), 32) + "-" + paddedHex(time.Now().UnixNano()%1e9, 16) + "-00"
	}
	return tp, ts
}

func paddedHex(n int64, width int) string {
	if n < 0 {
		n = -n
	}
	const h = "0123456789abcdef"
	buf := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		buf[i] = h[n&0xf]
		n >>= 4
	}
	return string(buf)
}

// manaIdempotencyKey composes a per-call key for ManaService.DeductMana.
// The shape is (tenant|gcid|action|YYYY-MM-DD-UTC) so a same-day refresh
// re-uses the key and chora-identity dedups (no double-debit on retry).
//
// Per ADR-142, the key is mandatory; the proto contract enforces it as a
// required field. UUIDv7 would be ideal but we want determinism for the
// HTTP retry case (a learner refreshing the dose page within the same
// minute MUST NOT spend twice).
func manaIdempotencyKey(tenantID, gcid string, now time.Time) string {
	return tenantID + "|" + gcid + "|" + companion.ActionDailyDoseCoach + "|" + now.UTC().Format("2006-01-02")
}

// Compile-time assertion that the publisher type is wired (defensive).
var _ events.Publisher = (*events.InMemoryPublisher)(nil)

// Compile-time assertion that we use the inmem package.
var _ = inmem.NewAtomCatalogue

var _ = errors.New
