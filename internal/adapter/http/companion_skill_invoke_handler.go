// companion_skill_invoke_handler.go — CHO-2013 P1.B (R4-4): the
// consumption-side single-step Skill invoke runner.
//
//	POST /v1/me/companions/{id}/skills/{key}/invoke
//
// Spec-§6 Ritual-runner semantics at N=1 (P4 generalises this runner to
// multi-step Rituals; it never replaces it):
//
//  1. Validate — owned instance, active-kind catalogue row, a runner
//     builder for the key, grant owned AND equipped, catalogue active
//     (ADR-174 release gate), min_growth_stage ≤ stage (invoke-time gate).
//  2. Affordability — read-only balance pre-check on the Skill's price key
//     (spec §7 `companion_skill_{key}`); the model-gateway is the SOLE
//     debiter (ADR-177): the runner stamps ManaActionCode on the turn and
//     never debits locally.
//  3. Pre-fetch read context — the sidecar-less companion agent cannot dial
//     consumption's STRICT-mTLS surfaces (the R4-3 → R4-4 pivot), so the
//     runner reads each Skill's context locally (profile slice / session
//     log / target concept+atoms) and injects it into the turn message.
//  4. Drive ONE agent turn over the same engine client the chat handler
//     uses (fresh session, full state, per-turn action code).
//  5. Runner writes the sink — chat → the JSON reply; memory_note
//     (recap_scribe) → a `memory_type='recap'` companion-memory row, LOUD
//     on failure (the note IS the point — never a silent skip).
//
// The runner deliberately does NOT touch companion_chat_sessions, does NOT
// record a chat_turn memory (recap's own note is the sink), and publishes
// no event (ritual_run_completed.v1 is the P4 contract; the mana ledger
// truth lives gateway-side).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// LearnerProfileReader is the runner's ADR-200 projection read slice
// (progress_mirror). Satisfied by the pg LearnerProfileRepo — the same repo
// the gRPC ReadLearnerProfile RPC uses.
type LearnerProfileReader interface {
	ListFacts(ctx context.Context, tenantID, learnerGCID string) ([]*lp.Fact, error)
	RecentActivity(ctx context.Context, tenantID, learnerGCID string, limit int) ([]*lp.ActivityEntry, error)
}

// CourseDirectoryReader is the OPTIONAL course_id → title resolver
// (course_directory projection; CHO-2059 follow-up). Satisfied by the pg
// CourseDirectoryRepo. nil ⇒ progress_mirror renders the leak-free type-generic
// noun ("a course") EXACTLY as before — the projection is purely additive.
type CourseDirectoryReader interface {
	LookupTitles(ctx context.Context, courseIDs []string) (map[string]string, error)
}

// resolveCourseTitles batch-resolves course_id → title for the supplied facts +
// activity through the optional CourseDirectory reader. A nil reader — or no
// course references — yields an empty map, so the presenter receives
// resolvedName="" and the output is byte-identical to the pre-projection
// behaviour (leak-free generic noun). A reader error is logged LOUD but is
// non-fatal: the enrichment is additive and must never fail the progress mirror.
func (s *Server) resolveCourseTitles(ctx context.Context, facts []*lp.Fact, activity []*lp.ActivityEntry) map[string]string {
	if s.CourseDirectory == nil {
		return map[string]string{}
	}
	ids := lp.CollectCourseIDs(facts, activity)
	if len(ids) == 0 {
		return map[string]string{}
	}
	titles, err := s.CourseDirectory.LookupTitles(ctx, ids)
	if err != nil {
		log.Printf("consumption: progress_mirror course-title resolve failed (non-fatal, additive enrichment): %v", err)
		return map[string]string{}
	}
	return titles
}

// ConceptNodeReader resolves a learner-scoped ConceptNode (explain_anew
// concept targets). Satisfied by conceptgraph.ConceptNodeRepository (pg).
// GetByID returns (nil, nil) on a live-miss per the repo contract.
type ConceptNodeReader interface {
	GetByID(ctx context.Context, tenantID, learnerGCID, conceptID string) (*conceptgraph.ConceptNode, error)
}

// skillInvokeReq is the inbound body. Params are the Skill's spec-§2 sheet
// parameters (all string-typed enums/refs for the P1 three); an absent body
// or params object means "all defaults".
type skillInvokeReq struct {
	Params map[string]string `json:"params"`
}

// Result kinds — the skillInvokeResp discriminator (CHO-2016). Every existing
// skill (sight / progress_mirror / recap_scribe / explain_anew …) is "chat":
// the response is the Companion's narration and carries NO items channel. The
// answerable Skills (quiz_me / socratic_drill) are "answerable": the narration
// FRAMES a channel of atom references the learner then answers through the
// EXISTING server-graded session flow.
const (
	resultKindChat       = "chat"
	resultKindAnswerable = "answerable"
	// resultKindSuggestion is the kg_explore discriminator (SinkSuggestionInbox):
	// the reply narrates the pool-grounded concepts written to the learner's WS-4
	// curation inbox this invoke — the FE refreshes the inbox on this kind. It
	// carries NO items channel.
	resultKindSuggestion = "suggestion"
)

// invokeItem is one atom REFERENCE in an answerable response's items[] channel
// (CHO-2016). It carries atom REFERENCES + learner-safe metadata ONLY — NEVER a
// rendered question/stem/options. The FE fetches the stem+options per atom from
// chora_creation (GET /api/atoms/{atomId}, learner-safe mode); consumption holds
// no stem/options (see atom_index.go). Title + topic are learner-influenced
// content, so both are neutralised before they leave the service.
type invokeItem struct {
	AtomID     string `json:"atom_id"`
	Title      string `json:"title"`
	Topic      string `json:"topic"`
	Difficulty int    `json:"difficulty"`
	Reason     string `json:"reason"`
}

// skillInvokeResp is the wire response. recorded is true only when a
// memory_note-sink Skill actually persisted its note this invoke.
//
// ResultKind + Items are ADDITIVE (CHO-2016): chat skills keep their exact prior
// field set plus ResultKind="chat" and OMIT items (a nil *Items marshals away),
// so their wire shape is unchanged bar the discriminator. Answerable skills set
// ResultKind="answerable" and a non-nil Items pointer — an empty pick marshals as
// `[]` (honest empty-state), NOT `null` (a nil slice) nor an omitted key.
type skillInvokeResp struct {
	SkillKey    string        `json:"skill_key"`
	Reply       string        `json:"reply"`
	Recorded    bool          `json:"recorded"`
	ManaCharged int           `json:"mana_charged"`
	TurnID      string        `json:"turn_id"`
	ResultKind  string        `json:"result_kind"`
	Items       *[]invokeItem `json:"items,omitempty"`
	// Citations — the Seeker family's grounded sources (P5 Far Sight, CHO-2017;
	// IMDA D2). ADDITIVE + omitempty: non-Seeker skills leave it nil ⇒ the key is
	// omitted, so every existing wire shape is unchanged.
	Citations *[]invokeCitation `json:"citations,omitempty"`
	// SearchEntryPointHTML — the Google Search-Suggestions chip HTML the Far Sight
	// FE renders when a Seeker presents grounded sources (Google ToS display
	// obligation, ADR-231 D5). ADDITIVE + omitempty: nil ⇒ key omitted.
	SearchEntryPointHTML *string `json:"search_entry_point_html,omitempty"`
	// WebSearchQueries — the queries the model actually issued (CHO-2179). The
	// chip above SHOWS these as clickable pills, but the chip's links expire
	// (~30 days, ADR-231 D4); these plain strings do not. They are what the
	// surface renders as "what I searched", and what a durable research note
	// persists to explain itself once the chip is dead.
	//
	// ⚠ Transparency metadata, NEVER knowledge — the surface must render them
	// visually distinct from the answer. ADDITIVE + omitempty: nil ⇒ key omitted
	// (no fabrication when the vendor issued none).
	WebSearchQueries *[]string `json:"web_search_queries,omitempty"`
}

// invokeError is a typed handler error (status + wire code).
type invokeError struct {
	status int
	code   string
	msg    string
}

func invokeErrf(status int, code, format string, args ...any) *invokeError {
	return &invokeError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

// invokeTurn is a builder's output: the composed agent turn + sink hints.
type invokeTurn struct {
	message   string
	maxTokens int
	// recordNote — memory_note-sink Skills only: false when there is
	// genuinely nothing to persist (e.g. recap with zero session facts, or a
	// Seeker whose grounded search yielded zero citations), so an empty result
	// never mints a hollow note row.
	recordNote bool
	// noteMemoryType — the companion_memory_recall.memory_type for a memory_note
	// sink write. Empty ⇒ the handler defaults to "recap", so recap_scribe is
	// byte-identical; web_research (the cited Seeker research note) sets
	// "research" so its notes are distinguishable from session recaps in the
	// learner's memory view (mirrors the ceremony handler's "ceremony" type).
	noteMemoryType string
	// resultKind — the skillInvokeResp discriminator. Empty ⇒ the handler
	// defaults to resultKindChat, so EVERY existing builder is unchanged and
	// keeps returning "chat". Answerable builders set resultKindAnswerable.
	resultKind string
	// items — the answerable channel (CHO-2016), computed DETERMINISTICALLY
	// server-side by the builder (the LLM never picks). Non-nil (possibly empty)
	// ⇒ the handler emits it, so an honest empty pick marshals as `[]`. nil for
	// every chat builder ⇒ the items key is omitted.
	items []invokeItem
	// scoutPool — the kg_explore deterministic candidate pool the builder stashed
	// for the SinkSuggestionInbox reconcile (pool-truth-wins hallucination floor;
	// companion_skill_invoke_scout.go). nil/empty for every other builder AND for an
	// honest empty-map scout (the sink then writes nothing).
	scoutPool []edgescout.Candidate
	// citations — the Seeker family's structured citation channel (P5 Far Sight,
	// CHO-2017; IMDA D2 mandate). Non-nil ⇒ the handler emits it so the learner
	// always sees the grounded sources; nil for every non-Seeker builder AND for
	// a Seeker's no-citation hedge (nothing grounded to cite).
	citations []invokeCitation
	// searchEntryPointHTML — the Google Search-Suggestions chip HTML the FE MUST
	// render when a Seeker turn presents grounded sources (Google ToS display
	// obligation, ADR-231 D5). Empty for non-Seeker builders AND for a Seeker's
	// no-citation hedge (nothing grounded was shown ⇒ no chip to attribute).
	searchEntryPointHTML string
	// webSearchQueries — the queries the Seeker's grounded search actually issued
	// (CHO-2179). Unlike the chip, these SURVIVE the no-citation hedge: the
	// companion did search, and telling the learner what it searched is what makes
	// the hedge's "try rephrasing" actionable. Empty for every non-Seeker builder.
	webSearchQueries []string
}

// groundedProvenance lifts a Seeker turn's DURABLE provenance onto the memory-note
// row (ADR-231 D4): the issued queries + the citations' domain/title/snippet.
//
// ⚠ The redirect URI is deliberately NOT carried. It expires (~30 days), so a note
// that persisted it would rot to dead links — and a dead link still LOOKS like a
// working source, which is worse than no link at all. domain+title+snippet and the
// queries never expire, so they are the record.
//
// nil for a non-grounded note (recap / ceremony) ⇒ source_metadata stays NULL
// rather than carrying an empty husk.
func groundedProvenance(t *invokeTurn) *companion.MemoryProvenance {
	if len(t.citations) == 0 && len(t.webSearchQueries) == 0 {
		return nil
	}
	cits := make([]companion.MemoryCitation, 0, len(t.citations))
	for _, c := range t.citations {
		cits = append(cits, companion.MemoryCitation{
			Domain:  c.Domain,
			Title:   c.Title,
			Snippet: c.Snippet,
		})
	}
	return &companion.MemoryProvenance{
		WebSearchQueries: t.webSearchQueries,
		Citations:        cits,
	}
}

// invokeParams is the validated per-skill parameter set (defaults applied).
type invokeParams map[string]string

// skillParamSpec validates one Skill's params: closed enum values keyed by
// param name, UUID-shape checks for ref params, with defaults. Unknown
// param keys are rejected (fail-loud — params ride into the prompt, keep
// the surface closed per spec §6 Armor discipline).
type skillParamSpec struct {
	// enums: param name → allowed values (nil slice = free value; pair with
	// refs for shape-checked identifiers).
	enums map[string][]string
	// refs: params that must be UUID-shaped (concept_ref / atom_ref) — a
	// malformed ref 400s here and never reaches a repo.
	refs []string
	// defaults: applied when the param is absent.
	defaults map[string]string
	// required: params that must be present after defaults.
	required []string
}

func (sp skillParamSpec) validate(in map[string]string) (invokeParams, *invokeError) {
	out := invokeParams{}
	for k, v := range sp.defaults {
		out[k] = v
	}
	for k, v := range in {
		allowed, known := sp.enums[k]
		if !known {
			return nil, invokeErrf(http.StatusBadRequest, "INVALID_SKILL_PARAMS",
				"unknown param %q", k)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue // absent-equivalent → default stands
		}
		if allowed != nil {
			ok := false
			for _, a := range allowed {
				if v == a {
					ok = true
					break
				}
			}
			if !ok {
				return nil, invokeErrf(http.StatusBadRequest, "INVALID_SKILL_PARAMS",
					"param %q must be one of %v", k, allowed)
			}
		}
		out[k] = v
	}
	for _, k := range sp.required {
		if strings.TrimSpace(out[k]) == "" {
			return nil, invokeErrf(http.StatusBadRequest, "INVALID_SKILL_PARAMS",
				"param %q required", k)
		}
	}
	for _, k := range sp.refs {
		if v := out[k]; v != "" && !uuidShaped(v) {
			return nil, invokeErrf(http.StatusBadRequest, "INVALID_SKILL_PARAMS",
				"param %q must be a UUID ref", k)
		}
	}
	return out, nil
}

// uuidShaped reports whether s is a hyphenated 8-4-4-4-12 hex UUID (the
// concept_ref/atom_ref wire shape; mirrors conceptgraph's ref discipline).
func uuidShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// invokeBuilder pre-fetches one Skill's read context and composes the turn.
type invokeBuilder struct {
	params skillParamSpec
	build  func(s *Server, ctx context.Context, tenantID, gcid, companionID string, p invokeParams) (*invokeTurn, *invokeError)
}

// paramSpecFromSheet derives one Skill's closed invoke spec from the domain
// sheet table (companion.SkillParamSheets, CHO-2362) - the single authority the
// ritual gates, the 0106 catalogue seed, and the FE editor schema also derive
// from. Semantics are unchanged from the former inline literals: enums close
// membership, ints enumerate their tiny inclusive range, text params stay
// free-form here (length-checked in-builder), refs are UUID-shape-checked.
func paramSpecFromSheet(skillKey string) skillParamSpec {
	sheet, ok := companion.SkillParamSheets[skillKey]
	if !ok {
		// A builder without a sheet is a build gap - fail loud at init, never a
		// silent unvalidated params surface.
		panic(fmt.Sprintf("companion_skill_invoke: no param sheet for builder %q", skillKey))
	}
	sp := skillParamSpec{enums: map[string][]string{}}
	for _, p := range sheet {
		switch p.Kind {
		case companion.SkillParamEnum:
			sp.enums[p.Name] = p.Values
		case companion.SkillParamInt:
			vals := make([]string, 0, p.Max-p.Min+1)
			for v := p.Min; v <= p.Max; v++ {
				vals = append(vals, strconv.Itoa(v))
			}
			sp.enums[p.Name] = vals
		case companion.SkillParamText:
			sp.enums[p.Name] = nil // free-text - bounded in the builder
		case companion.SkillParamConceptRef, companion.SkillParamGrowthEdgeRef:
			sp.enums[p.Name] = nil
			sp.refs = append(sp.refs, p.Name)
		}
		if p.Default != "" {
			if sp.defaults == nil {
				sp.defaults = map[string]string{}
			}
			sp.defaults[p.Name] = p.Default
		}
		if p.Required {
			sp.required = append(sp.required, p.Name)
		}
	}
	return sp
}

// skillInvokeBuilders — the P1 closed set (R4-1: the st2 THREE; reminder_bell
// → P2). A key absent here but present+active in the catalogue is a build
// gap: 501, never a silent generic turn.
var skillInvokeBuilders = map[string]invokeBuilder{
	"progress_mirror": {
		params: paramSpecFromSheet("progress_mirror"),
		build:  buildProgressMirrorTurn,
	},
	"recap_scribe": {
		params: paramSpecFromSheet("recap_scribe"),
		build:  buildRecapScribeTurn,
	},
	// explain_anew: target = concept_ref | atom_ref (shape-checked via refs).
	"explain_anew": {
		params: paramSpecFromSheet("explain_anew"),
		build:  buildExplainAnewTurn,
	},
	// CHO-2014 SIGHT builders (sink=chat narration). weakness_sight reads
	// Growth Edges (weakness.read); `edge` is a growth_edge_ref or empty=auto-top.
	// weakness_sight: edge = growth_edge_ref | "" (auto-top), UUID-shape checked
	// when present.
	"weakness_sight": {
		params: paramSpecFromSheet("weakness_sight"),
		build:  buildWeaknessSightTurn,
	},
	// map_sight reads the learner's own concept map (kg.read_map), ring-scoped
	// around `focus` (concept_ref) or the resonant centre when empty.
	"map_sight": {
		params: paramSpecFromSheet("map_sight"),
		build:  buildMapSightTurn,
	},
	// CHO-2016 ANSWERABLE builders (result_kind=answerable). quiz_me RETRIEVE
	// only this wave: mode=generate is a valid enum value the builder rejects
	// with a 4xx (its qgen.invoke crew is unbuilt). scope∈{weak,concept_ref,due};
	// count∈3..5; optional `concept` (concept_ref) targets a specific ConceptNode
	// for scope=concept_ref (defaults to the Companion's resonant concept).
	"quiz_me": {
		params: paramSpecFromSheet("quiz_me"),
		build:  buildQuizMeTurn,
	},
	// socratic_drill = the IDENTICAL deterministic pick (weak/concept only — no
	// due), differing ONLY in framing + hint behaviour. rounds∈3..7 bounds the
	// item count + the Socratic ladder.
	"socratic_drill": {
		params: paramSpecFromSheet("socratic_drill"),
		build:  buildSocraticDrillTurn,
	},
	// kg_explore (Weaver, st4) — GROUNDED-RECONCILE suggestion scout
	// (sink=suggestion_inbox; companion_skill_invoke_scout.go). count∈3..5 caps the
	// pool-grounded concepts written to the learner's WS-4 inbox. The pool is
	// assembled deterministically from the learner's map-adjacent atom topics; the
	// LLM only selects + rationalises, and edgescout.Reconcile is the hallucination
	// floor. HELD DARK until its §8 eval passes (migration 0085).
	"kg_explore": {
		params: paramSpecFromSheet("kg_explore"),
		build:  buildKgExploreTurn,
	},
	// P5 Far Sight (CHO-2017, ADR-220) — Seeker fact_check (st5, external_egress,
	// sink=chat). `claim` is free-text (≤200 chars, length-checked in-builder;
	// the gateway Armor-INSPECTs the egress centrally). Grounded via the gateway
	// grounded-search seam; the citation mandate hedges when nothing is cited.
	// HELD DARK until its §8 eval passes (migration 0087).
	// fact_check: claim = free-text, length-checked in buildFactCheckTurn.
	"fact_check": {
		params: paramSpecFromSheet("fact_check"),
		build:  buildFactCheckTurn,
	},
	// P5 Far Sight (CHO-2017, ADR-220) — Seeker web_research "Far Sight" (st5,
	// external_egress, sink=memory_note, price 80). `direction` is free-text
	// (concept_ref UUID | query ≤120 chars, length + concept resolution handled
	// in-builder); `depth ∈ {survey, deep}` widens the grounded hit budget. Grounded
	// via the gateway grounded-search seam (shared with fact_check); the citation
	// mandate hedges (and mints NO note) when nothing is cited. ⚠ Owner ruling
	// 2026-07-10: ONE sink = memory_note; the spec §2.4 map-suggestion inbox
	// (kg.suggest) was a doc error, DEFERRED. HELD DARK until its §8 eval passes
	// (migration 0088).
	// web_research: direction = free-text OR concept_ref (resolved in
	// buildWebResearchTurn); depth = grounded hit-budget knob.
	"web_research": {
		params: paramSpecFromSheet("web_research"),
		build:  buildWebResearchTurn,
	},
}

const (
	invokeDefaultMaxTokens = 512
	invokeFullMaxTokens    = 1024
	// invokeSessionLogCap bounds how many recent chat turns feed a recap.
	invokeSessionLogCap = 10
	// invokeConceptAtomCap bounds resolved atom refs per concept target.
	invokeConceptAtomCap = 6
)

// invokeCompanionSkill handles POST /v1/me/companions/{id}/skills/{key}/invoke.
func (s *Server) invokeCompanionSkill(w http.ResponseWriter, r *http.Request, companionID, skillKey string) {
	if !s.loadoutDeps(w) {
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req skillInvokeReq
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil && !errors.Is(derr, io.EOF) {
		writeError(w, http.StatusBadRequest, "BAD_JSON", derr.Error())
		return
	}

	ctx := repoCtx(r, tenantID, gcid)
	if _, err := s.loadOwnedInstance(ctx, tenantID, gcid, companionID); err != nil {
		s.writeSkillError(w, err)
		return
	}

	catalogue, cerr := s.SkillCatalog.ListCatalogue(ctx)
	if cerr != nil {
		writeError(w, http.StatusInternalServerError, "CATALOGUE_READ_FAILED", cerr.Error())
		return
	}
	entry, ok := catalogue[skillKey]
	if !ok {
		writeError(w, http.StatusBadRequest, "SKILL_NOT_IN_CATALOGUE",
			"skill_key not in the platform catalogue")
		return
	}
	if entry.SkillKind != companion.SkillKindActive {
		writeError(w, http.StatusConflict, "SKILL_NOT_INVOKABLE",
			"craft skills are passive designer grammar — they cannot be invoked")
		return
	}
	builder, ok := skillInvokeBuilders[skillKey]
	if !ok {
		writeError(w, http.StatusNotImplemented, "SKILL_INVOKE_UNSUPPORTED",
			"this build has no invoke runner for "+skillKey)
		return
	}

	grants, gerr := s.Loadouts.ListGrants(ctx, tenantID, companionID)
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, "LOADOUT_READ_FAILED", gerr.Error())
		return
	}
	var grant *companion.SkillGrant
	for i := range grants {
		if grants[i].SkillKey == skillKey {
			grant = &grants[i]
			break
		}
	}
	if grant == nil {
		writeError(w, http.StatusConflict, "SKILL_NOT_OWNED",
			"skill not unlocked on this companion yet")
		return
	}
	if !grant.Equipped {
		writeError(w, http.StatusConflict, "SKILL_NOT_EQUIPPED",
			"skill is owned but not equipped — equip it first")
		return
	}
	if !entry.Active {
		writeError(w, http.StatusConflict, "SKILL_NOT_ACTIVE",
			"skill is not released yet (ADR-174 eval gate)")
		return
	}
	stage, serr := s.loadGrowthStage(r, tenantID, companionID, gcid)
	if serr != nil {
		s.writeSkillError(w, serr)
		return
	}
	if stage < entry.MinGrowthStage {
		writeError(w, http.StatusConflict, "SKILL_STAGE_LOCKED",
			fmt.Sprintf("skill requires growth stage %d (companion is %d)", entry.MinGrowthStage, stage))
		return
	}

	params, perr := builder.params.validate(req.Params)
	if perr != nil {
		writeError(w, perr.status, perr.code, perr.msg)
		return
	}

	// Affordability pre-check (read-only; ADR-177 — the gateway debits once
	// via the stamped action code). PriceKey is seeded from the catalogue;
	// an unknown code here is registry drift → fail loud, never un-metered.
	actionCode := entry.PriceKey
	// ADR-231 D6 — Seeker grounded-egress metering. A grounded-search Seeker
	// (fact_check / web_research) is metered ONCE, at the gateway grounded-search
	// egress: grounded.Query.ActionCode carries this skill's OWN price
	// (entry.PriceKey — fact_check 40 / web_research 80) and the gateway debits it
	// there (the SOLE meter per ADR-177). So the fenced verify/research turn must
	// NOT also debit — un-meter it. The pre-check + reported mana_charged stay the
	// skill's own spec-§2.4 price (the metering is re-homed, not re-priced).
	turnActionCode := actionCode // stamped on the fenced turn (gateway Invoke meters)
	if isGroundedEgressSkill(skillKey) {
		turnActionCode = "" // grounded egress already metered this price — un-meter the fenced turn
	}
	cost, lerr := companion.LookupCost(actionCode)
	if lerr != nil {
		writeError(w, http.StatusInternalServerError, "SKILL_PRICE_UNKNOWN",
			fmt.Sprintf("no canonical cost for action code %q", actionCode))
		return
	}
	if cost > 0 && s.ManaQuoter != nil {
		balCtx := clients.WithTraceparent(r.Context(), r.Header.Get("traceparent"))
		if snap, berr := s.ManaQuoter.GetBalance(balCtx, gcid); berr == nil {
			if snap.BalanceUnits < cost {
				writeInsufficientManaEnvelope(w, &companion.ErrInsufficientMana{
					RequiredUnits:  cost,
					CurrentBalance: snap.BalanceUnits,
				})
				return
			}
		} else {
			// Balance read failed — fail-open per ADR-142 §8 (the gateway
			// still gates + debits server-side). Mirrors the chat handler.
			log.Printf("consumption: skill invoke balance pre-check non-fatal error: %v", berr)
		}
	}

	if s.CompanionEngine == nil || s.CompanionEngineResource == "" {
		writeError(w, http.StatusServiceUnavailable, "ENGINE_NOT_CONFIGURED",
			"companion turn lane not wired at chora-consumption boot")
		return
	}

	turn, berr := builder.build(s, ctx, tenantID, gcid, companionID, params)
	if berr != nil {
		writeError(w, berr.status, berr.code, berr.msg)
		return
	}

	// Session-bootstrap config (bug-3 mesh-sidestep, mirrors the chat
	// handler): without it the sidecar-less agent refuses the turn.
	var companionConfigJSON string
	if s.CompanionConfigJSONResolver != nil {
		cfgJSON, rerr := s.CompanionConfigJSONResolver(r.Context(), tenantID, companionID, gcid)
		if rerr != nil {
			writeError(w, http.StatusBadGateway, "COMPANION_CONFIG_RESOLVE_FAILED", rerr.Error())
			return
		}
		companionConfigJSON = cfgJSON
	}

	turnID := domain.NewUUIDv7()
	frames, serr2 := s.CompanionEngine.StreamChat(r.Context(), clients.CompanionChatRequest{
		TenantID:            tenantID,
		UserGCID:            gcid,
		CompanionID:         companionID,
		ManaTier:            resolveManaTier(s, tenantID, gcid),
		Message:             turn.message,
		MaxOutputTokens:     turn.maxTokens,
		TurnID:              turnID,
		CompanionConfigJSON: companionConfigJSON,
		// ADR-177: the agent stamps this onto the gateway Invoke so the gateway
		// debits this Skill use ONCE. The runner never debits. For a Seeker
		// grounded-egress skill this is EMPTY — the gateway already metered the use
		// at the grounded-search egress (ADR-231 D6), so the fenced turn rides
		// un-metered (no double-debit).
		ManaActionCode: turnActionCode,
	})
	if serr2 != nil {
		if errors.Is(serr2, agentengine.ErrEngineNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "ENGINE_NOT_CONFIGURED", serr2.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "ENGINE_STREAM_FAILED", serr2.Error())
		return
	}

	reply, model, engineErrMsg := drainInvokeTurnModel(frames)
	if engineErrMsg != "" {
		writeError(w, http.StatusBadGateway, "SKILL_INVOKE_ENGINE_ERROR", engineErrMsg)
		return
	}
	if reply == "" {
		// A Skill use with no output is a failed use — surface it (mirrors
		// the Greet empty-reply guard); the FE must never render a blank.
		writeError(w, http.StatusBadGateway, "SKILL_INVOKE_EMPTY_REPLY",
			"companion engine returned an empty reply for the skill turn")
		return
	}

	// replyOut is the learner-facing reply. For chat/memory/answerable skills it
	// is the engine reply verbatim; the kg_explore suggestion sink OVERRIDES it with
	// a deterministic rendering of the grounded concepts (never the raw JSON).
	replyOut := reply

	// Runner-side sink write (R4-4). memory_note → persist the reply as a durable
	// companion-memory note, LOUD on failure — unlike the chat handler's soft-fail
	// post-turn record, the durable note IS this Skill's purpose. The memory_type
	// is builder-supplied (default "recap"): recap_scribe → "recap"; the
	// web_research Seeker → "research" (its cited research note). The persist error
	// code stays RECAP_PERSIST_FAILED for recap (existing contract) and the
	// note-generic MEMORY_NOTE_PERSIST_FAILED for every other memory_note Skill.
	recorded := false
	if entry.OutputSink == companion.SinkMemoryNote && turn.recordNote {
		memType := turn.noteMemoryType
		if memType == "" {
			memType = "recap"
		}
		persistErrCode := "MEMORY_NOTE_PERSIST_FAILED"
		if memType == "recap" {
			persistErrCode = "RECAP_PERSIST_FAILED"
		}
		vec, eerr := s.Embedder.Embed(ctx, companion.EmbedInput{
			Text: reply, TaskType: companion.EmbedTaskDocument, TenantID: tenantID,
		})
		if eerr != nil {
			writeError(w, http.StatusInternalServerError, persistErrCode,
				fmt.Sprintf("embed %s note: %v", memType, eerr))
			return
		}
		if rerr := s.CompanionMemory.Record(ctx, companion.RecordMemoryInput{
			TenantID:     tenantID,
			OwnerGCID:    gcid,
			CompanionID:  companionID,
			MemoryType:   memType,
			ContentText:  reply,
			Embedding:    vec,
			ModelID:      s.CompanionEmbeddingModelID,
			SourceTurnID: turnID,
			Now:          time.Now().UTC(),
			// Grounded notes (web_research) carry WHERE THEY CAME FROM (ADR-231 D4):
			// the durable citations + the issued queries, so the note can still
			// explain itself after the chip's links expire. nil for a plain recap.
			// It rides the source_metadata JSONB column, NEVER ContentText — that is
			// what gets vector-embedded above, and provenance in the prose would both
			// poison recall and read to the learner as knowledge, which it is not.
			Provenance: groundedProvenance(turn),
		}); rerr != nil {
			writeError(w, http.StatusInternalServerError, persistErrCode,
				fmt.Sprintf("record %s note: %v", memType, rerr))
			return
		}
		recorded = true
	}

	// kg_explore (SinkSuggestionInbox) — ground the strict-JSON extraction against
	// the deterministic pool the builder stashed, then WRITE the reconciled
	// pool-grounded concepts to the learner's WS-4 curation inbox (ADR-212 D4),
	// stamping the model/run decision-stamps (ADR-197) + the source Companion +
	// rationale (ADR-215). An empty pool (honest empty-map scout) skips the parse:
	// the engine narrated "nothing to scout" and the narration stands. A malformed
	// or hallucinated reply is fail-loud 502 (never a silent partial write); a sink
	// failure is a loud 500. The raw JSON is NEVER surfaced — replyOut becomes a
	// deterministic rendering of the grounded concepts (the §8 eval scores it).
	if entry.OutputSink == companion.SinkSuggestionInbox && len(turn.scoutPool) > 0 {
		final, rerr := reconcileKgExplore(reply, turn.scoutPool, fogScoutCount(params["count"]))
		if rerr != nil {
			writeError(w, http.StatusBadGateway, "KG_EXPLORE_BAD_REPLY", rerr.Error())
			return
		}
		sugs, berr2 := fogScoutSuggestions(final, tenantID, gcid, companionID, model, turnID, time.Now().UTC())
		if berr2 != nil {
			writeError(w, http.StatusInternalServerError, "KG_EXPLORE_PERSIST_FAILED",
				fmt.Sprintf("build scout suggestions: %v", berr2))
			return
		}
		if werr := s.Suggestions.CreateBatch(ctx, sugs); werr != nil {
			writeError(w, http.StatusInternalServerError, "KG_EXPLORE_PERSIST_FAILED",
				fmt.Sprintf("write scout suggestions: %v", werr))
			return
		}
		replyOut = fogScoutReply(final)
		recorded = len(sugs) > 0
	}

	// result_kind discriminator (CHO-2016): an empty builder resultKind defaults
	// to "chat", so every existing skill is unchanged. A non-nil items slice
	// (answerable builders only, possibly empty) is emitted so an honest empty
	// pick marshals as `[]`; chat builders leave it nil ⇒ the key is omitted.
	resultKind := turn.resultKind
	if resultKind == "" {
		resultKind = resultKindChat
	}
	resp := skillInvokeResp{
		SkillKey:    skillKey,
		Reply:       replyOut,
		Recorded:    recorded,
		ManaCharged: int(cost),
		TurnID:      turnID,
		ResultKind:  resultKind,
	}
	if turn.items != nil {
		resp.Items = &turn.items
	}
	// Seeker citation channel (P5 Far Sight): a non-nil citations slice (fact_check
	// with grounded sources) is emitted so the learner always sees them; the
	// no-citation hedge leaves it nil ⇒ the key is omitted.
	if turn.citations != nil {
		resp.Citations = &turn.citations
	}
	// Search-Suggestions chip (P5 Far Sight): emitted only when the Seeker turn
	// carries the grounded chip HTML (Google ToS display obligation, ADR-231 D5);
	// empty for non-Seekers AND the no-citation hedge ⇒ the key is omitted.
	if turn.searchEntryPointHTML != "" {
		resp.SearchEntryPointHTML = &turn.searchEntryPointHTML
	}
	// Issued web-search queries (CHO-2179): the durable "what I searched" surface
	// that outlives the chip's ~30-day links. Emitted whenever the Seeker's search
	// issued any — INCLUDING the no-citation hedge, which is precisely when the
	// learner most needs to see what was tried. Omitted when the vendor issued
	// none: never fabricate a query.
	if len(turn.webSearchQueries) > 0 {
		resp.WebSearchQueries = &turn.webSearchQueries
	}
	writeJSON(w, http.StatusOK, resp)
}

// drainInvokeTurn drains one engine turn, assembling the reply text (dropping the
// model attribution). Thin wrapper over drainInvokeTurnModel for the callers that
// do not stamp a model (the ceremony edge-scout runner).
func drainInvokeTurn(frames <-chan clients.ChatStreamFrame) (reply, engineErrMsg string) {
	reply, _, engineErrMsg = drainInvokeTurnModel(frames)
	return reply, engineErrMsg
}

// drainInvokeTurnModel drains one engine turn, assembling the reply text AND the
// model attribution from the terminal turn_complete frame (the ADR-197
// decision-stamp kg_explore stamps onto each written suggestion). The buffered GKE
// agent emits a single aggregate token frame (the client's buffered-agent
// fallback); a streaming agent emits many. An error frame is terminal — its
// message is returned and the reply discarded.
func drainInvokeTurnModel(frames <-chan clients.ChatStreamFrame) (reply, model, engineErrMsg string) {
	var b strings.Builder
	for frame := range frames {
		switch frame.Type {
		case clients.ChatFrameToken:
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(frame.Data, &p)
			b.WriteString(p.Text)
		case clients.ChatFrameTurnComplete:
			// ADR-254 D8 (bus path): the WHOLE reply rides turn_complete.reply_text
			// (token frames are retired); a streamed engine still accumulates via
			// the token arm above, so whichever path produced the turn, the reply
			// is what the frame says it is.
			var p struct {
				Model     string `json:"model"`
				ReplyText string `json:"reply_text"`
			}
			_ = json.Unmarshal(frame.Data, &p)
			if p.Model != "" {
				model = p.Model
			}
			if p.ReplyText != "" {
				b.Reset()
				b.WriteString(p.ReplyText)
			}
		case clients.ChatFrameError:
			var p struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(frame.Data, &p)
			if p.Message == "" {
				p.Message = "companion engine aborted the skill turn"
			}
			engineErrMsg = p.Message
			// Keep draining to release the goroutine; the turn is dead.
		}
	}
	if engineErrMsg != "" {
		return "", "", engineErrMsg
	}
	return strings.TrimSpace(b.String()), model, ""
}

// ----------------------------------------------------------------------------
// Per-skill turn builders
// ----------------------------------------------------------------------------

// invokeFrameHeader opens every runner-composed turn: the agent's [SKILLS]
// fragment (554d9442b) primes the persona; this frame scopes the turn to
// exactly one Skill's behaviour.
func invokeFrameHeader(skillKey, skillName string, p invokeParams) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+p[k])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[SKILL INVOCATION: %s — %q]\n", skillKey, skillName)
	b.WriteString("The learner invoked this equipped Skill directly (not free chat). Perform exactly this Skill's behaviour, in persona, and nothing else this turn.\n")
	if len(pairs) > 0 {
		b.WriteString("Params: " + strings.Join(pairs, " ") + "\n")
	}
	return b.String()
}

// invokeWindowCutoff maps a profile window to a cutoff; zero = no filter.
// Mirrors the gRPC ReadLearnerProfile window semantics (P1.B R4-3 RPC).
func invokeWindowCutoff(window string, now time.Time) time.Time {
	switch window {
	case "week":
		return now.AddDate(0, 0, -7)
	case "month":
		return now.AddDate(0, -1, 0)
	default:
		return time.Time{}
	}
}

// buildProgressMirrorTurn — sink=chat, price 0. Context = the ADR-200
// verified profile (facts + recent activity), window-filtered. Missing rows
// surface honestly ("not recorded") — never fabricated (spec §2.2).
func buildProgressMirrorTurn(s *Server, ctx context.Context, tenantID, gcid, _ string, p invokeParams) (*invokeTurn, *invokeError) {
	if s.LearnerProfiles == nil {
		return nil, invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"learner profile projection not wired (progress_mirror)")
	}
	facts, err := s.LearnerProfiles.ListFacts(ctx, tenantID, gcid)
	if err != nil {
		return nil, invokeErrf(http.StatusInternalServerError, "PROFILE_READ_FAILED", "list facts: %v", err)
	}
	activity, err := s.LearnerProfiles.RecentActivity(ctx, tenantID, gcid, lp.MaxRecentActivity)
	if err != nil {
		return nil, invokeErrf(http.StatusInternalServerError, "PROFILE_READ_FAILED", "recent activity: %v", err)
	}

	// CHO-2059: batch-resolve real course NAMES from the course_directory
	// projection so the Companion says "Algebra I" instead of "a course". Nil
	// reader ⇒ empty map ⇒ resolvedName="" ⇒ byte-identical generic-noun output.
	titles := s.resolveCourseTitles(ctx, facts, activity)

	window := p["window"]
	cutoff := invokeWindowCutoff(window, time.Now().UTC())
	var lines []string
	for _, f := range facts {
		if f == nil || (!cutoff.IsZero() && f.OccurredAt.Before(cutoff)) {
			continue
		}
		// lp.FactDisplayLabel is the learner-SAFE label: a raw cross-domain ref
		// UUID (course/cert id) is never surfaced to the Companion. A resolved
		// course name (when the directory knows it) WINS over the generic noun;
		// an un-named ref still reads as a type-generic noun ("a course").
		line := fmt.Sprintf("- [%s] %s", f.Type, lp.FactDisplayLabel(f, titles[lp.CourseIDForFact(f)]))
		if f.Detail.Score != nil {
			line += fmt.Sprintf(" (score %.2f)", *f.Detail.Score)
		}
		if f.Detail.Passed != nil {
			line += fmt.Sprintf(" (passed=%t)", *f.Detail.Passed)
		}
		line += " — " + f.OccurredAt.UTC().Format("2006-01-02")
		lines = append(lines, line)
	}
	for _, a := range activity {
		if a == nil || (!cutoff.IsZero() && a.OccurredAt.Before(cutoff)) {
			continue
		}
		// lp.ActivityDisplay regenerates the line from the structured Kind —
		// never the stored summary, which baked the raw ref UUID into free text.
		// A resolved course name wins for enrolled/completed_course activities.
		lines = append(lines, fmt.Sprintf("- [activity: %s] %s — %s",
			a.Kind, lp.ActivityDisplay(a, titles[lp.CourseIDForActivity(a)]), a.OccurredAt.UTC().Format("2006-01-02")))
	}

	var b strings.Builder
	b.WriteString(invokeFrameHeader("progress_mirror", "Progress Mirror", p))
	b.WriteString("[VERIFIED LEARNER PROFILE — read on your behalf via profile.read]\n")
	if len(lines) == 0 {
		b.WriteString("(no verified profile entries recorded for this window)\n")
	} else {
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	b.WriteString("[INSTRUCTION]\n")
	b.WriteString("Narrate how the learner is doing using ONLY the verified entries above — courses, scores, certifications, activity, honest trends. Anything not recorded above, say it is not recorded yet. NEVER fabricate a number, date, or achievement. Keep the mirror kind but truthful.")
	return &invokeTurn{message: b.String(), maxTokens: invokeDefaultMaxTokens}, nil
}

// buildRecapScribeTurn — sink=memory_note, price 5. Context = the recent
// chat_turn session log (recency read, chat_turn rows ONLY — a prior recap
// must never feed a new one). Zero session facts ⇒ the turn still runs (an
// honest in-character reply) but no note is minted.
func buildRecapScribeTurn(s *Server, ctx context.Context, tenantID, gcid, companionID string, p invokeParams) (*invokeTurn, *invokeError) {
	if s.CompanionMemoryView == nil || s.Embedder == nil || s.CompanionMemory == nil {
		return nil, invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"companion memory view/store/embedder not wired (recap_scribe)")
	}
	rows, err := s.CompanionMemoryView.RecentMemories(ctx, companionmind.Query{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		CompanionID: companionID,
		MemoryLimit: companionmind.MaxMemoryLimit,
	})
	if err != nil {
		return nil, invokeErrf(http.StatusInternalServerError, "MEMORY_READ_FAILED", "recent memories: %v", err)
	}
	// chat_turn rows only, newest-first from the adapter → keep the newest
	// invokeSessionLogCap and re-order chronologically for the prompt.
	var session []companionmind.EpisodicMemory
	for _, m := range rows {
		if m.MemoryType == "chat_turn" {
			session = append(session, m)
			if len(session) == invokeSessionLogCap {
				break
			}
		}
	}
	for i, j := 0, len(session)-1; i < j; i, j = i+1, j-1 {
		session[i], session[j] = session[j], session[i]
	}

	var b strings.Builder
	b.WriteString(invokeFrameHeader("recap_scribe", "Recap Scribe", p))
	b.WriteString("[SESSION LOG — recent study turns between you and the learner]\n")
	if len(session) == 0 {
		b.WriteString("(no recorded session turns yet)\n")
		b.WriteString("[INSTRUCTION]\n")
		b.WriteString("There is nothing recorded to recap. Tell the learner that honestly, in persona, and invite them to study with you so there is something worth remembering. Do NOT invent a recap.")
		return &invokeTurn{message: b.String(), maxTokens: invokeDefaultMaxTokens, recordNote: false}, nil
	}
	for _, m := range session {
		// CHO-2060: render the date only (drop the raw clock time) and sanitize the
		// content so a baked-in ref UUID never reaches this learner-facing recap.
		b.WriteString(fmt.Sprintf("- (%s) %s\n", m.CreatedAt.UTC().Format("2006-01-02"), lp.SanitizeLearnerText(m.Content)))
	}
	b.WriteString("[INSTRUCTION]\n")
	b.WriteString("Compose a short recap (3-5 sentences) containing ONLY things that actually appear in the session log above — no invented facts. Write it as the note itself, in persona. Your reply will be saved to your memory as a recap the learner can see, correct, or forget in the memory view — tell them so in the final sentence.")
	return &invokeTurn{message: b.String(), maxTokens: invokeDefaultMaxTokens, recordNote: true}, nil
}

// buildExplainAnewTurn — sink=chat, price 10. Target = concept_ref (learner-
// scoped ConceptNode, atoms resolved through the local atom_index) or
// atom_ref (published atom_index row). The agent's atom.search/atom.cite
// tools are POC stubs — grounding rides THIS injected context (Jira
// CHO-2013 comment 4; flagged for the eval design).
func buildExplainAnewTurn(s *Server, ctx context.Context, tenantID, gcid, _ string, p invokeParams) (*invokeTurn, *invokeError) {
	if s.ConceptNodes == nil && s.AtomIndex == nil {
		return nil, invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"concept/atom read ports not wired (explain_anew)")
	}
	target := p["target"]

	var contextLines []string
	resolved := false

	if s.ConceptNodes != nil {
		node, err := s.ConceptNodes.GetByID(ctx, tenantID, gcid, target)
		if err != nil {
			return nil, invokeErrf(http.StatusInternalServerError, "CONCEPT_READ_FAILED", "concept read: %v", err)
		}
		if node != nil {
			resolved = true
			contextLines = append(contextLines,
				fmt.Sprintf("- Target concept (learner-named): %q", node.Title))
			if s.AtomIndex != nil {
				n := 0
				for _, ref := range node.AtomRefs {
					if n == invokeConceptAtomCap {
						break
					}
					atom, aerr := s.AtomIndex.Get(ctx, ref)
					if aerr != nil {
						if errors.Is(aerr, atom_index.ErrNotFound) {
							continue // unprojected ref — honest skip
						}
						return nil, invokeErrf(http.StatusInternalServerError, "ATOM_READ_FAILED", "atom read: %v", aerr)
					}
					if atom == nil || atom.TenantID != tenantID || !atom.Playable() {
						continue
					}
					contextLines = append(contextLines, atomContextLine(atom))
					n++
				}
			}
		}
	}
	if !resolved && s.AtomIndex != nil {
		atom, aerr := s.AtomIndex.Get(ctx, target)
		if aerr != nil && !errors.Is(aerr, atom_index.ErrNotFound) {
			return nil, invokeErrf(http.StatusInternalServerError, "ATOM_READ_FAILED", "atom read: %v", aerr)
		}
		if aerr == nil && atom != nil && atom.TenantID == tenantID && atom.Playable() {
			resolved = true
			contextLines = append(contextLines, atomContextLine(atom))
		}
	}
	if !resolved {
		return nil, invokeErrf(http.StatusNotFound, "TARGET_NOT_FOUND",
			"target resolves to no concept on the learner's map and no published atom")
	}

	length := p["length"]
	maxTokens := invokeDefaultMaxTokens
	if length == "full" {
		maxTokens = invokeFullMaxTokens
	}

	var b strings.Builder
	b.WriteString(invokeFrameHeader("explain_anew", "Explain It Differently", p))
	b.WriteString("[TARGET CONTEXT — resolved on your behalf]\n")
	b.WriteString(strings.Join(contextLines, "\n") + "\n")
	b.WriteString("[INSTRUCTION]\n")
	fmt.Fprintf(&b, "Re-explain the target through a fresh %s lens, %s form, tuned to the learner's interests. Ground every factual claim in the context above and name the atoms you drew on. If the context is too thin to explain faithfully, say so in character and suggest the nearest step — never invent facts.", p["style"], length)
	return &invokeTurn{message: b.String(), maxTokens: maxTokens}, nil
}

// atomContextLine renders one atom_index projection line (the skinny
// projection carries title/type/difficulty/topics — no body; the body lives
// in chora_creation and P2's real atom.search closes that gap). The raw
// atom_id is deliberately NOT emitted: it is a platform UUID the learner must
// never see, and the agent grounds/cites by title, not id (the atom.cite tool
// is a POC stub with no live id consumer).
func atomContextLine(a *atom_index.AtomIndex) string {
	line := fmt.Sprintf("- Atom %q (type %s, difficulty %d", a.Title, a.AtomType, a.Difficulty)
	if len(a.TopicTags) > 0 {
		line += ", topics: " + strings.Join(a.TopicTags, " / ")
	}
	return line + ")"
}
