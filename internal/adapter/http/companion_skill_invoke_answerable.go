// companion_skill_invoke_answerable.go — CHO-2016: invoke-runner builders for the
// two "answerable-pipe" Scholar Skills, quiz_me + socratic_drill (RETRIEVE mode
// only this wave). Unlike the chat/sight builders (which return a narration and
// nothing else), these return result_kind="answerable": a Companion FRAMING
// narration PLUS an items[] channel of atom REFERENCES the learner then answers
// through the EXISTING server-graded session flow (session → deterministic MCQ
// grade → atom_session.completed → Companion EXP + topic_accuracy + Growth-Edge
// recovery). There is ZERO new submit path and ZERO new EXP source here.
//
// The runner PRE-FETCHES the items DETERMINISTICALLY, SERVER-SIDE — the LLM never
// picks. Three scopes, all deterministic: `weak` (top Growth Edges' gradable
// cached drill atoms), `concept_ref` (a ConceptNode's AtomRefs), `due` (the
// Ebbinghaus spaced-repetition due-set, via the SAME SM-2 decay line the daily
// dose's review slot uses — daily_dose.go DoseDecayThreshold; we do NOT invent a
// new forgetting curve). Every pick keeps ONLY gradable atoms (IsMCQ + a primary
// topic + playable + tenant-owned), mirroring doseFocusedGrowthEdge, so the FE
// never renders a dead-end drill. The framing turn voices encouragement ONLY; it
// injects the learner-safe TITLES (never a raw atom_id / UUID) and carries the
// sight anti-injection guard because titles are learner-influenced content.
//
// HONEST CAVEAT (do not paper over): goal progress is INDIRECT — a completed atom
// moves a Goal only if its Growth Edge crosses active→grown. The `weak` scope
// yields edge-backed atoms, so it moves goals; the `concept_ref` and `due` picks
// are NOT necessarily edge-backed and so may not move a goal even when answered
// correctly. This is retrieval practice + EXP + retention recovery, not a
// universal goal-mover.
package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// Answerable scopes (closed; param-validated before a builder runs).
const (
	answerableScopeWeak       = "weak"
	answerableScopeConceptRef = "concept_ref"
	answerableScopeDue        = "due"

	// answerableModeGenerate is the pre-declared but UNBUILT quiz_me mode this
	// wave (its qgen.invoke crew does not exist) — the builder 4xx-rejects it.
	answerableModeGenerate = "generate"

	// Item reasons (machine-readable provenance for the FE, per item).
	answerableReasonWeak     = "weak_spot"
	answerableReasonConcept  = "concept_ref"
	answerableReasonDue      = "due_for_review"
	answerableReasonFallback = "fresh_pick"

	// Pick sizing (spec §2: quiz 3..5, socratic 3..7).
	minAnswerableCount    = 3
	maxQuizCount          = 5
	maxSocraticRounds     = 7
	defaultQuizCount      = 3
	defaultSocraticRounds = 3

	// answerableWeakEdgeTopN bounds the Growth-Edge read for the weak scope
	// (mirrors weaknessSightTopN — a handful of shaky concepts, not the whole map).
	answerableWeakEdgeTopN = 5
)

// buildQuizMeTurn — sink=chat framing + answerable items. price 0 (retrieve;
// spec §7). scope∈{weak,concept_ref,due}, count∈3..5. mode=generate is rejected
// with a 4xx (its qgen.invoke crew is unbuilt — the price is pre-declared at 25).
func buildQuizMeTurn(s *Server, ctx context.Context, tenantID, gcid, companionID string, p invokeParams) (*invokeTurn, *invokeError) {
	if p["mode"] == answerableModeGenerate {
		return nil, invokeErrf(http.StatusUnprocessableEntity, "QUIZ_MODE_GENERATE_UNAVAILABLE",
			"quiz_me generate mode is not available yet — use mode=retrieve, which quizzes the learner from their existing atoms")
	}
	count := answerableCount(p["count"], defaultQuizCount, maxQuizCount)
	items, ierr := s.pickAnswerableItems(ctx, tenantID, gcid, companionID, p["scope"], p["concept"], count)
	if ierr != nil {
		return nil, ierr
	}
	msg := composeAnswerableFraming("quiz_me", "Quick Quiz", p, items, quizFramingInstruction, quizEmptyInstruction)
	return &invokeTurn{
		message:    msg,
		maxTokens:  invokeDefaultMaxTokens,
		resultKind: resultKindAnswerable,
		items:      items,
	}, nil
}

// buildSocraticDrillTurn — sink=chat framing + answerable items. price 15
// (spec §7). scope∈{weak,concept_ref} (NO due). The item pick is IDENTICAL to
// quiz_me's; only the framing + hint behaviour differ. rounds∈3..7 bounds the
// item count + the Socratic ladder.
func buildSocraticDrillTurn(s *Server, ctx context.Context, tenantID, gcid, companionID string, p invokeParams) (*invokeTurn, *invokeError) {
	rounds := answerableCount(p["rounds"], defaultSocraticRounds, maxSocraticRounds)
	items, ierr := s.pickAnswerableItems(ctx, tenantID, gcid, companionID, p["scope"], p["concept"], rounds)
	if ierr != nil {
		return nil, ierr
	}
	msg := composeAnswerableFraming("socratic_drill", "Socratic Drill", p, items, socraticFramingInstruction(rounds), socraticEmptyInstruction)
	return &invokeTurn{
		message:    msg,
		maxTokens:  invokeDefaultMaxTokens,
		resultKind: resultKindAnswerable,
		items:      items,
	}, nil
}

// pickAnswerableItems is the DETERMINISTIC, SERVER-SIDE item pick shared by
// quiz_me + socratic_drill. It always returns a NON-NIL slice (so an honest empty
// pick marshals as `[]`, never null). Cold/empty ⇒ fall back to the recent
// gradable pool (SearchForLearner); still empty ⇒ items:[] (never fabricated).
func (s *Server) pickAnswerableItems(ctx context.Context, tenantID, gcid, companionID, scope, conceptRef string, count int) ([]invokeItem, *invokeError) {
	if s.AtomIndex == nil {
		return nil, invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"atom index projection not wired (answerable pick)")
	}
	items := make([]invokeItem, 0, count)
	seen := make(map[string]bool, count)

	var topicHint string
	var ierr *invokeError
	switch scope {
	case answerableScopeWeak:
		topicHint, ierr = s.pickWeakAnswerable(ctx, tenantID, gcid, count, &items, seen)
	case answerableScopeConceptRef:
		topicHint, ierr = s.pickConceptAnswerable(ctx, tenantID, gcid, companionID, conceptRef, count, &items, seen)
	case answerableScopeDue:
		topicHint, ierr = s.pickDueAnswerable(ctx, tenantID, gcid, count, &items, seen)
	default:
		// Param validation is closed, so this is unreachable — fail loud rather
		// than silently return an empty pick for an unknown scope.
		return nil, invokeErrf(http.StatusBadRequest, "INVALID_SKILL_PARAMS", "unknown scope %q", scope)
	}
	if ierr != nil {
		return nil, ierr
	}

	// Cold/empty ⇒ recent gradable atoms (topic-biased where the scope gave a
	// hint). Never fabricate — an empty result stays items:[] (honest state).
	if len(items) == 0 {
		if ierr := s.pickFallbackAnswerable(ctx, tenantID, topicHint, count, &items, seen); ierr != nil {
			return nil, ierr
		}
	}
	return items, nil
}

// pickWeakAnswerable resolves the learner's top Growth Edges (shakiest first)
// and appends their gradable cached drill atoms. Returns the shakiest edge's
// concept label as the cold-fallback topic hint. Mirrors buildWeaknessSightTurn's
// read + doseFocusedGrowthEdge's gradable-resolve.
func (s *Server) pickWeakAnswerable(ctx context.Context, tenantID, gcid string, count int, items *[]invokeItem, seen map[string]bool) (string, *invokeError) {
	if s.LearnerWeakness == nil {
		return "", invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"growth-edge read repo not wired (answerable weak scope)")
	}
	page, err := s.LearnerWeakness.List(ctx, lw.ListQuery{
		TenantID:     tenantID,
		LearnerGCID:  gcid,
		Sort:         lw.SortStrengthDesc, // shakiest first
		IncludeGrown: false,               // mastered edges are not weaknesses
		PageSize:     answerableWeakEdgeTopN,
	})
	if err != nil {
		return "", invokeErrf(http.StatusInternalServerError, "WEAKNESS_READ_FAILED", "list growth edges: %v", err)
	}
	var topicHint string
	for _, e := range page.Items {
		if topicHint == "" && strings.TrimSpace(e.ConceptLabel) != "" {
			topicHint = e.ConceptLabel
		}
		for _, atomID := range e.CachedDrillAtomIDs {
			reached, ierr := s.appendGradableRef(ctx, tenantID, atomID, answerableReasonWeak, count, items, seen)
			if ierr != nil {
				return "", ierr
			}
			if reached {
				return topicHint, nil
			}
		}
	}
	return topicHint, nil
}

// pickConceptAnswerable resolves a ConceptNode's AtomRefs. The concept is the
// explicit `concept` ref when given, else the Companion's resonant concept (the
// map_sight focus-empty idiom). An explicit ref that resolves to nothing 404s
// (fail-loud on a bad target); a stale/absent resonant pointer is an honest
// empty (falls through to the cold pool). Returns the concept title as the hint.
func (s *Server) pickConceptAnswerable(ctx context.Context, tenantID, gcid, companionID, conceptRef string, count int, items *[]invokeItem, seen map[string]bool) (string, *invokeError) {
	if s.ConceptNodes == nil {
		return "", invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"concept read port not wired (answerable concept_ref scope)")
	}
	conceptID := strings.TrimSpace(conceptRef)
	explicit := conceptID != ""
	if conceptID == "" && s.Growth != nil {
		st, err := s.Growth.GetCompanionGrowth(ctx, tenantID, companionID, gcid)
		if err != nil {
			return "", invokeErrf(http.StatusInternalServerError, "GROWTH_READ_FAILED", "read growth state: %v", err)
		}
		conceptID = strings.TrimSpace(st.ResonantConceptID)
	}
	if conceptID == "" {
		return "", nil // no target + no resonant centre → honest empty (cold fallback, no hint)
	}
	node, err := s.ConceptNodes.GetByID(ctx, tenantID, gcid, conceptID)
	if err != nil {
		return "", invokeErrf(http.StatusInternalServerError, "CONCEPT_READ_FAILED", "concept read: %v", err)
	}
	if node == nil {
		if explicit {
			return "", invokeErrf(http.StatusNotFound, "CONCEPT_NOT_FOUND",
				"that concept is not on the learner's map")
		}
		return "", nil // stale resonant pointer → honest empty
	}
	for _, ref := range node.AtomRefs {
		reached, ierr := s.appendGradableRef(ctx, tenantID, ref, answerableReasonConcept, count, items, seen)
		if ierr != nil {
			return "", ierr
		}
		if reached {
			break
		}
	}
	return node.Title, nil
}

// pickDueAnswerable resolves the Ebbinghaus spaced-repetition DUE-SET. It uses
// the EXISTING per-atom decay computation the daily-dose review slot uses
// (companion.EbbinghausDecay crossing companion.DoseDecayThreshold, i.e. retention
// R(t)=e^(-t/S) below the shared 0.6 due-terrain line) — no new forgetting curve.
// Ordering is deterministic: most-forgotten first, atom_id ascending as a stable
// tiebreak (SM-2 state is a map — sort so the pick never flaps).
func (s *Server) pickDueAnswerable(ctx context.Context, tenantID, gcid string, count int, items *[]invokeItem, seen map[string]bool) (string, *invokeError) {
	if s.SM2 == nil {
		return "", invokeErrf(http.StatusServiceUnavailable, "SKILL_INVOKE_NOT_WIRED",
			"spaced-repetition store not wired (answerable due scope)")
	}
	states, err := s.SM2.AllStatesForLearner(ctx, tenantID, gcid)
	if err != nil {
		return "", invokeErrf(http.StatusInternalServerError, "RETENTION_READ_FAILED", "read spaced-repetition state: %v", err)
	}
	now := time.Now().UTC()
	type dueCand struct {
		atomID string
		decay  float64
	}
	cands := make([]dueCand, 0, len(states))
	for atomID, st := range states {
		decay := companion.EbbinghausDecay(st, now)
		if decay > companion.DoseDecayThreshold {
			cands = append(cands, dueCand{atomID: atomID, decay: decay})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].decay == cands[j].decay {
			return cands[i].atomID < cands[j].atomID
		}
		return cands[i].decay > cands[j].decay
	})
	for _, c := range cands {
		reached, ierr := s.appendGradableRef(ctx, tenantID, c.atomID, answerableReasonDue, count, items, seen)
		if ierr != nil {
			return "", ierr
		}
		if reached {
			break
		}
	}
	return "", nil // due carries no natural topic hint for the cold fallback
}

// pickFallbackAnswerable tops up an empty scope pick with the tenant's recent
// gradable atoms (SearchForLearner), topic-biased where a hint exists. The
// returned atoms are already resolved — no per-ref Get.
func (s *Server) pickFallbackAnswerable(ctx context.Context, tenantID, topicHint string, count int, items *[]invokeItem, seen map[string]bool) *invokeError {
	atoms, err := s.AtomIndex.SearchForLearner(ctx, tenantID, topicHint, count)
	if err != nil {
		return invokeErrf(http.StatusInternalServerError, "ATOM_SEARCH_FAILED", "search atoms: %v", err)
	}
	for _, a := range atoms {
		if s.appendResolvedGradable(a, tenantID, answerableReasonFallback, count, items, seen) {
			break
		}
	}
	return nil
}

// appendGradableRef resolves ONE atom ref via the atom_index projection and,
// when gradable, appends a learner-safe item. ErrNotFound (unprojected atom) is
// an honest skip; a real storage error fails loud (never a silently smaller
// quiz). Returns whether the count cap is now reached.
func (s *Server) appendGradableRef(ctx context.Context, tenantID, atomID, reason string, count int, items *[]invokeItem, seen map[string]bool) (bool, *invokeError) {
	if len(*items) >= count {
		return true, nil
	}
	if atomID == "" || seen[atomID] {
		return false, nil
	}
	a, err := s.AtomIndex.Get(ctx, atomID)
	if err != nil {
		if errors.Is(err, atom_index.ErrNotFound) {
			return false, nil // unprojected — can't render/grade; honest skip
		}
		return false, invokeErrf(http.StatusInternalServerError, "ATOM_READ_FAILED", "atom read: %v", err)
	}
	return s.appendResolvedGradable(a, tenantID, reason, count, items, seen), nil
}

// appendResolvedGradable appends an ALREADY-resolved atom if it is a gradable,
// playable, tenant-owned MCQ (deduped by atom_id). Returns whether the cap is now
// reached.
func (s *Server) appendResolvedGradable(a *atom_index.AtomIndex, tenantID, reason string, count int, items *[]invokeItem, seen map[string]bool) bool {
	if len(*items) >= count {
		return true
	}
	// Gradable = a published, tenant-owned MCQ with an answer key AND a primary
	// topic — mirrors doseFocusedGrowthEdge (IsMCQ() && PrimaryTopic()!="") so the
	// player can render it and the projector can auto-grade it (never a dead end).
	if a == nil || a.TenantID != tenantID || !a.Playable() || !a.IsMCQ() || a.PrimaryTopic() == "" {
		return false
	}
	if seen[a.AtomID] {
		return false
	}
	seen[a.AtomID] = true
	// Learner-SAFE: neutralise the title AND topic (both are learner-influenced
	// content — a learner-forged atom title can carry a prompt injection). The
	// raw atom_id rides the items[] channel to the FE, NEVER into the prompt.
	*items = append(*items, invokeItem{
		AtomID:     a.AtomID,
		Title:      neutralizeSightText(a.Title),
		Topic:      neutralizeSightText(a.PrimaryTopic()),
		Difficulty: a.Difficulty,
		Reason:     reason,
	})
	return len(*items) >= count
}

// answerableCount parses a validated count/rounds enum string, clamping into
// [minAnswerableCount, hi]. An absent/garbage value (should not occur after
// param validation) falls back to def.
func answerableCount(raw string, def, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < minAnswerableCount {
		return def
	}
	if n > hi {
		return hi
	}
	return n
}

// composeAnswerableFraming builds the framing turn: the skill frame + the
// learner-safe TITLES (never a raw atom_id / UUID — those ride items[] to the FE)
// + a framing-only instruction guarded against prompt injection. The `concept`
// ref param is stripped from the echoed params (a raw UUID must never enter a
// learner-facing prompt — CHO-2059 discipline).
func composeAnswerableFraming(skillKey, skillName string, p invokeParams, items []invokeItem, instruction, emptyInstruction string) string {
	framed := invokeParams{}
	for k, v := range p {
		if k == "concept" {
			continue
		}
		framed[k] = v
	}
	var b strings.Builder
	b.WriteString(invokeFrameHeader(skillKey, skillName, framed))
	b.WriteString("[ANSWERABLE SET — selected on the learner's behalf from their own atoms; the learner answers these IN THE APP, not in this chat]\n")
	if len(items) == 0 {
		b.WriteString("(nothing to practise in this scope yet)\n")
		b.WriteString("[INSTRUCTION]\n")
		b.WriteString(emptyInstruction)
		return b.String()
	}
	for i, it := range items {
		fmt.Fprintf(&b, "%d. %s", i+1, it.Title)
		if it.Topic != "" {
			fmt.Fprintf(&b, " (topic: %s)", it.Topic)
		}
		b.WriteString("\n")
	}
	b.WriteString("[INSTRUCTION]\n")
	// sightDataGuard: the titles above are the learner's own UNTRUSTED content —
	// stop the model from OBEYING an instruction embedded in a title (the
	// structural half is neutralizeSightText, already applied to each title).
	b.WriteString(instruction + sightDataGuard)
	return b.String()
}

// quizFramingInstruction bounds the Quick Quiz framing turn: frame + encourage,
// but NEVER surface the questions/options/answers (they are answered + graded in
// the app — the runner adds ZERO new submit path).
const quizFramingInstruction = "You have selected the learner a set of practice questions from their OWN atoms, listed above. In persona, warmly and briefly frame this quiz: name what it covers using ONLY the titles/topics shown (never invent a topic), and encourage the learner to answer it in the app. You MUST NOT write, reveal, restate, hint at, or answer any question, option, or solution — the questions live in the app and are graded there. Keep it to two or three sentences."

// quizEmptyInstruction — honest empty-state framing for quiz_me.
const quizEmptyInstruction = "You found nothing to quiz the learner on in this scope yet. Tell them so honestly and warmly, in persona, and invite them to study or add atoms so you can build a quiz next time. Do NOT invent any questions."

// socraticEmptyInstruction — honest empty-state framing for socratic_drill.
const socraticEmptyInstruction = "You found nothing to drill with the learner in this scope yet. Say so honestly and warmly, in persona, and invite them to study or add atoms so you can run a Socratic drill next time. Do NOT invent any questions."

// socraticFramingInstruction bounds the Socratic Drill framing turn: it caps the
// Companion at ONE gentle guiding question, ANCHORS that question to the shown
// titles (no new concept/claim past them — the CHO-2016 18d facts_groundedness
// fix: a free-ranging guiding question over-elaborated beyond the thin injected
// context), and forbids revealing the app's questions/answers.
func socraticFramingInstruction(rounds int) string {
	return fmt.Sprintf("You have selected %d concept(s) for a Socratic drill from the learner's OWN atoms, listed above. In persona, frame the drill warmly: name what you will explore together using ONLY the titles/topics shown (never invent one), and invite the learner to work through it in the app, one step at a time. You MAY offer AT MOST ONE gentle guiding question to set the tone, but keep it ANCHORED to the shown titles/topics — introduce NO new concept, fact, or characterisation beyond what those titles name (do not generalise past them). Do NOT reveal, restate, or answer any of the app's questions, options, or solutions. Keep it to three or four sentences.", rounds)
}
