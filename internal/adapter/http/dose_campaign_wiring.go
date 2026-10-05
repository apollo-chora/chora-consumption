// dose_campaign_wiring.go — CHO-2081 / ADR-227 D12 (WS-C2): the campaign
// axis adapters over the WS-C1 domain core.
//
// ONE deterministic daily election is shared by both sides so they always
// agree on which goal marches today:
//
//   - compose side (Server.doseCampaignInput): threads a CampaignInput into
//     the composer + augments the universe with the focus node's gradable
//     unenrolled atoms (the CHO-1895 focused-practice idiom). FAIL-SOFT —
//     the dose NEVER breaks on campaign enrichment (doseGrowthEdges
//     contract).
//   - answer side (ExtServer.recordCampaignAnswer): folds a server-graded
//     answer on today's campaign material into the campaign Grader
//     (ladder + concept-key retention + verified events). FAIL-LOUD —
//     verified progress must never be silently lost (the SessionCompletion
//     precedent); ErrAlreadyWon/ErrDeleted are benign no-ops (D9 — the
//     node exited the game).
//
// Scheduling stays deterministic: the election rides the same
// DoseSeed(date, gcid) day seed as the composer (ADR-202 — the LLM never
// schedules).
package http

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/dose_pref"
	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// campaignStaleDays ranks a never-advanced ladder as fully stale (a week
// saturates the domain recency boost) so brand-new campaigns surface soon.
const campaignStaleDays = 7

// CampaignGoalLister is the narrow goal surface the election needs
// (satisfied by the pg GoalRepo and goal.Repository).
type CampaignGoalLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*goal.Goal, error)
}

// CampaignAtomSearcher resolves the nearest published atoms for a concept
// embedding (satisfied by clients.ContentRetrievalClient — the drillcache
// searcher).
type CampaignAtomSearcher interface {
	SearchByEmbedding(ctx context.Context, tenantID string, query []float32, limit int) ([]string, error)
}

// CampaignEmbedder mints the focus-node theme embedding (satisfied by
// clients.LearnerWeaknessEmbedder — same text-embedding space creation
// indexes atoms in).
type CampaignEmbedder interface {
	Embed(ctx context.Context, text, tenantID string) ([]float32, error)
}

// CampaignQGenPublisher emits the ONE cross-service qgen batch request
// (satisfied by the proofing outbox publisher — events.Publisher).
type CampaignQGenPublisher interface {
	Publish(topic string, env events.Envelope, payload map[string]any) error
}

// CampaignDose bundles the campaign-axis dependencies. cmd/server builds ONE
// instance over the pg repos + the campaign Grader and hands the same
// pointer to the dose Server (compose) and the ExtServer (answer fold), so
// both sides elect from identical state. The WS-C3 question-lane deps
// (Bank/Searcher/Embedder/QGen) are individually optional — any nil member
// disables its lane (retrieval or generation) with the dose composing
// exactly as WS-C2 did.
type CampaignDose struct {
	Goals     CampaignGoalLister
	Prefs     dose_pref.Repository // nil ⇒ no per-map exclusions
	Progress  campaign.ProgressRepository
	Retention topic_retention.Repository
	Concepts  conceptgraph.ConceptNodeRepository
	Grader    *campaign.Grader

	// ADR-247 Cap A: the weakness-load + edge repos enrich the qgen prompt with
	// the node's diagnosed weakness (F2) and its goal + ancestor lineage (F3).
	// Both nil-tolerant: absent (or a load error) degrades the prompt toward the
	// sub-goal/title framing (BuildPromptV2 falls back to v1).
	Weakness lw.WeaknessContextLoader
	Edges    conceptgraph.EdgeRepository

	// WS-C3 (CHO-2082) — the retrieval-first question lane.
	Bank     campaignquestion.Repository // question sets + retrieval cache
	Searcher CampaignAtomSearcher        // nearest-atom retrieval
	Embedder CampaignEmbedder            // theme embedding
	QGen     CampaignQGenPublisher       // ai_assist.started.v2 gap requests
}

// electedCampaign is today's march, fully resolved: the goal, its focus
// node, and the grader's serve decision (rung + refresher, D8).
type electedCampaign struct {
	Goal  *goal.Goal
	Node  *conceptgraph.ConceptNode
	Serve campaign.ServeDecision
}

// electTodaysCampaign runs the deterministic daily election (ADR-227 D12):
// candidates = the learner's goals with a focus node, minus dose-pref
// excluded maps (map_id ≡ goal_id), minus won focus nodes (D9); the seeded
// daily RNG picks ONE, weighted by due-ness/recency (domain ranking).
// (nil, nil) = no campaign today; an error = a failed read (the CALLER
// chooses the posture: compose degrades, the answer fold fails loud).
func electTodaysCampaign(ctx context.Context, d *CampaignDose, tenantID, gcid string, seed int64, now time.Time) (*electedCampaign, error) {
	if d == nil || d.Grader == nil || d.Goals == nil || d.Concepts == nil ||
		d.Progress == nil || d.Retention == nil {
		return nil, nil
	}
	goals, err := d.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		return nil, fmt.Errorf("campaign: list goals: %w", err)
	}
	var excluded map[string]bool
	if d.Prefs != nil {
		excluded, err = d.Prefs.ExcludedMapIDs(ctx, tenantID, gcid)
		if err != nil {
			return nil, fmt.Errorf("campaign: read dose prefs: %w", err)
		}
	}

	cands := make([]companion.CampaignCandidate, 0, len(goals))
	nodeByGoal := make(map[string]*conceptgraph.ConceptNode, len(goals))
	goalByID := make(map[string]*goal.Goal, len(goals))
	for _, g := range goals {
		if g == nil || g.FocusConceptID == nil || strings.TrimSpace(*g.FocusConceptID) == "" {
			continue
		}
		if excluded[g.GoalID] {
			continue // learner excluded this map from their dose — never elected
		}
		node, nerr := d.Concepts.GetByID(ctx, tenantID, gcid, *g.FocusConceptID)
		if nerr != nil {
			return nil, fmt.Errorf("campaign: load focus node: %w", nerr)
		}
		if node == nil {
			continue // stale focus pointer — not eligible today
		}
		p, perr := d.Progress.GetByConcept(ctx, tenantID, gcid, node.ConceptID)
		if perr != nil {
			return nil, fmt.Errorf("campaign: load ladder: %w", perr)
		}
		if p != nil && p.Won() {
			continue // D9 — a won node exits the game
		}
		var retentionR *float64
		score, rerr := d.Retention.Get(ctx, tenantID, gcid, node.ConceptKey)
		if rerr != nil {
			return nil, fmt.Errorf("campaign: load retention: %w", rerr)
		}
		if score != nil {
			v := score.RetentionAt(now)
			retentionR = &v
		}
		days := campaignStaleDays // no ladder / never advanced ⇒ fully stale
		if p != nil && p.LastAdvanceDate != nil {
			days = int(now.Sub(*p.LastAdvanceDate).Hours() / 24)
			if days < 0 {
				days = 0
			}
		}
		cands = append(cands, companion.CampaignCandidate{
			GoalID:           g.GoalID,
			ConceptID:        node.ConceptID,
			ConceptKey:       node.ConceptKey,
			ConceptLabel:     node.Title,
			RetentionR:       retentionR,
			DaysSinceAdvance: days,
		})
		nodeByGoal[g.GoalID] = node
		goalByID[g.GoalID] = g
	}

	winner, ok := companion.PickCampaignGoal(cands, seed)
	if !ok {
		return nil, nil
	}
	serve, serr := d.Grader.ServeDecisionFor(ctx, tenantID, gcid, winner.ConceptID, winner.ConceptKey, now)
	if serr != nil {
		if errors.Is(serr, campaign.ErrAlreadyWon) || errors.Is(serr, campaign.ErrDeleted) {
			return nil, nil // benign race — the node left the game since assembly
		}
		return nil, fmt.Errorf("campaign: serve decision: %w", serr)
	}
	return &electedCampaign{Goal: goalByID[winner.GoalID], Node: nodeByGoal[winner.GoalID], Serve: serve}, nil
}

// campaignRetrievalWideN widens the nearest-atom search so the level filter
// rarely starves the candidate set (the drillcache 5× idiom); the matched
// hits are capped back to campaignRetrievalTopN.
const (
	campaignRetrievalWideN = 25
	campaignRetrievalTopN  = 5
)

// campaignAiAssistStartedV2 is the qgen crew's request lane (creation-owned;
// consumption is an allowlisted cross-domain REQUEST publisher — the
// proofing runner precedent, CHO-2040).
const campaignAiAssistStartedV2 = "chora.creation.ai_assist.started.v2"

// genOutcome reports WHY maybeRequestCampaignGen returned, so the explicit-tap
// door can render an honest at-cap status distinct from an empty miss. The
// dose feeder ignores it.
type genOutcome int

const (
	genNoRequest   genOutcome = iota // served-already or same-day in-flight (reuse); zero value
	genRequested                     // freshly minted + published
	genCapped                        // THIS origin's daily budget is spent
	genUnavailable                   // nil wiring / cap-read / mint / publish failure (fail-soft)
)

// doseCampaignInput assembles TODAY's campaign march for the composer plus
// the gradable unenrolled seeds that augment the dose universe (mirrors
// doseFocusedGrowthEdge — march material need not be enrolled).
//
// WS-C3 (CHO-2082, ADR-227 D13) — the slot fills RETRIEVAL-FIRST:
//
//  1. the focus node's own AtomRefs (published + gradable; a KNOWN
//     level mismatch is excluded, unknown levels get the benefit of
//     the doubt — pre-0079 rows are honest unknowns);
//  2. level-matched retrieval at the serve rung — the bank row's cache
//     when warm, else ONE embed+search round-trip whose level-matched
//     hits are cached on the row (drillcache idiom);
//  3. nothing matched ⇒ the qgen gap trigger (maybeRequestCampaignGen):
//     ≤1 two-question batch request per learner per UTC day, bank row →
//     requested; TODAY's slot stays empty (cascade fills the budget)
//     and the questions serve from the bank once the crew lands them.
//
// Fail-soft contract: any election/read failure returns (nil, nil) and the
// dose degenerates byte-identically (the dose NEVER breaks on enrichment).
func (s *Server) doseCampaignInput(ctx context.Context, tenantID, gcid string, seed int64, now time.Time, traceparent, tracestate string) (*companion.CampaignInput, []companion.AtomSeed) {
	if s.CampaignDose == nil || !s.ComposerV2Enabled {
		return nil, nil
	}
	d := s.CampaignDose
	elected, err := electTodaysCampaign(ctx, d, tenantID, gcid, seed, now)
	if err != nil || elected == nil {
		return nil, nil
	}
	rungLabel := elected.Serve.Rung.Label()

	// 1) The focus node's own atoms (C2 lane, now level-aware).
	var candidates []string
	var extraSeeds []companion.AtomSeed
	appendCandidate := func(a *atom_index.AtomIndex) {
		candidates = append(candidates, a.AtomID)
		extraSeeds = append(extraSeeds, companion.AtomSeed{
			AtomID: a.AtomID,
			Topic:  a.PrimaryTopic(),
			Title:  a.Title,
		})
	}
	servable := func(a *atom_index.AtomIndex) bool {
		return a != nil && a.Status == atom_index.StatusPublished && a.IsMCQ() && a.PrimaryTopic() != ""
	}
	if s.AtomIndex != nil {
		for _, atomID := range elected.Node.AtomRefs {
			a, gerr := s.AtomIndex.Get(ctx, atomID)
			if gerr != nil || !servable(a) {
				continue
			}
			if a.CognitiveLevel != "" && !a.MatchesCognitiveLevel(rungLabel) {
				continue // KNOWN mismatch never serves the rung
			}
			appendCandidate(a)
		}
	}

	// 2) Level-matched retrieval (cache-first; one search round-trip cold).
	var bankRow *campaignquestion.QuestionSet
	if d.Bank != nil {
		bankRow, err = d.Bank.GetByConceptRung(ctx, tenantID, gcid, elected.Node.ConceptID, int(elected.Serve.Rung))
		if err != nil {
			log.Printf("campaign: bank read failed (fail-soft, lane skipped): %v", err)
			bankRow, err = nil, nil
		}
	}
	if s.AtomIndex != nil {
		switch {
		case bankRow != nil && len(bankRow.RetrievedAtomIDs) > 0:
			for _, atomID := range bankRow.RetrievedAtomIDs {
				a, gerr := s.AtomIndex.Get(ctx, atomID)
				if gerr != nil || !servable(a) || !a.MatchesCognitiveLevel(rungLabel) {
					continue
				}
				appendCandidate(a)
			}
		case d.Searcher != nil && d.Embedder != nil:
			if hits := s.campaignRetrieve(ctx, d, elected, rungLabel, tenantID, gcid, now, &bankRow); len(hits) > 0 {
				for _, a := range hits {
					appendCandidate(a)
				}
			}
		}
	}

	// 3) The gap trigger — only when NOTHING matched theme × level.
	if len(candidates) == 0 {
		d.maybeRequestCampaignGen(ctx, elected.Node, int(elected.Serve.Rung), rungLabel, tenantID, gcid, now, traceparent, tracestate, bankRow, derefString(elected.Goal.RootConceptID), campaignquestion.OriginMarch)
	}

	return &companion.CampaignInput{
		GoalID:           elected.Goal.GoalID,
		ConceptID:        elected.Node.ConceptID,
		ConceptKey:       elected.Node.ConceptKey,
		ConceptLabel:     elected.Node.Title,
		Rung:             int(elected.Serve.Rung),
		IsRefresher:      elected.Serve.IsRefresher,
		CandidateAtomIDs: candidates,
	}, extraSeeds
}

// campaignRetrieve runs ONE embed+search round-trip for the focus node's
// theme, post-filters to published + gradable + level-matched (the
// SearchEmbeddings RPC cannot filter by level — addendum #4), and caches the
// matched ids on the bank row (minting it when absent) so subsequent
// composes skip the RPCs. Entirely fail-soft.
func (s *Server) campaignRetrieve(ctx context.Context, d *CampaignDose, elected *electedCampaign, rungLabel, tenantID, gcid string, now time.Time, bankRow **campaignquestion.QuestionSet) []*atom_index.AtomIndex {
	vec, err := d.Embedder.Embed(ctx, elected.Node.Title, tenantID)
	if err != nil {
		log.Printf("campaign: theme embed failed (fail-soft): %v", err)
		return nil
	}
	ids, err := d.Searcher.SearchByEmbedding(ctx, tenantID, vec, campaignRetrievalWideN)
	if err != nil {
		log.Printf("campaign: retrieval search failed (fail-soft): %v", err)
		return nil
	}
	matched := make([]*atom_index.AtomIndex, 0, campaignRetrievalTopN)
	matchedIDs := make([]string, 0, campaignRetrievalTopN)
	for _, atomID := range ids {
		if len(matched) >= campaignRetrievalTopN {
			break
		}
		a, gerr := s.AtomIndex.Get(ctx, atomID)
		if gerr != nil || a == nil || a.Status != atom_index.StatusPublished ||
			!a.IsMCQ() || a.PrimaryTopic() == "" || !a.MatchesCognitiveLevel(rungLabel) {
			continue
		}
		matched = append(matched, a)
		matchedIDs = append(matchedIDs, a.AtomID)
	}
	if len(matched) == 0 || d.Bank == nil {
		return matched
	}
	// Cache best-effort (a lost cache just re-searches next compose).
	row := *bankRow
	if row == nil {
		row, err = campaignquestion.New(tenantID, gcid, elected.Node.ConceptID, elected.Node.ConceptKey, int(elected.Serve.Rung), now)
		if err != nil {
			log.Printf("campaign: mint question set failed (cache skipped): %v", err)
			return matched
		}
	}
	row.SetRetrieved(matchedIDs, now)
	if err := d.Bank.Save(ctx, row); err != nil {
		log.Printf("campaign: retrieval cache write failed (non-fatal): %v", err)
		return matched
	}
	*bankRow = row
	return matched
}

// maybeRequestCampaignGen fires the D13 gap generation: ONE ≤2-question MCQ
// batch on the LIVE ai_assist lane, bounded PER ORIGIN (dose-march vs explicit
// tap) per learner per UTC day, mana-EXEMPT, bank row → requested. A ready or
// same-day in-flight set never re-publishes (reuse forever); a STALE
// in-flight set — requested on an earlier UTC day, terminal lost — is a gap
// again and re-requests under the same-origin daily cap (the walk-found "node
// bricked forever" fix). A publish failure marks the set failed (honest
// record) and the caller's flow continues (fail-soft).
//
// Shared by BOTH generation entrances, each with its OWN daily budget: the
// dose feeder (OriginMarch, the elected focus node, nothing matched theme ×
// level) and the practice serve door (OriginTap, an explicit hex tap that
// resolved a gap; the node need not be focus). Returns the row plus a
// genOutcome so the serve door can report the live status and map genCapped →
// an honest tap_capped (a nil row only when nothing fired and none existed).
// buildCampaignPromptContext assembles the ADR-247 Cap A enrichment for the
// focus node: its sub-goal (F1), the diagnosed weakness for its concept_key
// (F2), and the goal + ancestor lineage from the node up to rootConceptID (F3).
// Every piece is best-effort: a nil repo or a load error degrades that piece to
// empty and BuildPromptV2 falls back toward today's title+rung prompt. It never
// fails the dose (enrichment is additive telemetry on the generation prompt).
func (d *CampaignDose) buildCampaignPromptContext(ctx context.Context, tenantID, gcid string, node *conceptgraph.ConceptNode, rootConceptID string) campaignquestion.PromptContext {
	pc := campaignquestion.PromptContext{SubGoal: node.SubGoal}
	if d.Weakness != nil {
		if wc, err := d.Weakness.LoadWeaknessContextByConceptKey(ctx, tenantID, gcid, node.ConceptKey); err == nil && wc != nil {
			pc.Weakness = &campaignquestion.WeaknessInput{
				Descriptor:     wc.Descriptor,
				Misconceptions: wc.Misconceptions,
				Evidence:       wc.Evidence,
			}
		}
	}
	if d.Edges != nil && d.Concepts != nil && strings.TrimSpace(rootConceptID) != "" {
		nodes, nErr := d.Concepts.ListByLearner(ctx, tenantID, gcid)
		edges, eErr := d.Edges.ListByLearner(ctx, tenantID, gcid)
		if nErr == nil && eErr == nil {
			anc := conceptgraph.WalkAncestors(node.ConceptID, rootConceptID, nodes, edges)
			pc.GoalTitle = anc.GoalTitle
			pc.Ancestors = anc.Ancestors
		}
	}
	return pc
}

func (d *CampaignDose) maybeRequestCampaignGen(ctx context.Context, node *conceptgraph.ConceptNode, rung int, rungLabel, tenantID, gcid string, now time.Time, traceparent, tracestate string, bankRow *campaignquestion.QuestionSet, rootConceptID string, origin campaignquestion.RequestOrigin) (*campaignquestion.QuestionSet, genOutcome) {
	if d.Bank == nil || d.QGen == nil {
		return bankRow, genUnavailable
	}
	if bankRow != nil && (bankRow.GenerationStatus == campaignquestion.StatusReady ||
		(bankRow.GenerationStatus == campaignquestion.StatusRequested && !bankRow.StaleRequested(now))) {
		return bankRow, genNoRequest // AC3: generate once, retrieve forever / already in flight
	}
	requestsToday, err := d.Bank.CountRequestedOn(ctx, tenantID, gcid, origin, now)
	if err != nil {
		log.Printf("campaign: daily-cap read failed (generation skipped): %v", err)
		return bankRow, genUnavailable
	}
	if !campaignquestion.CanRequestToday(origin, requestsToday) {
		log.Printf("campaign: qgen daily cap reached (origin=%s); slot cascades today", origin)
		return bankRow, genCapped
	}
	row := bankRow
	if row == nil {
		row, err = campaignquestion.New(tenantID, gcid, node.ConceptID, node.ConceptKey, rung, now)
		if err != nil {
			log.Printf("campaign: mint question set failed (generation skipped): %v", err)
			return bankRow, genUnavailable
		}
	}
	assistID := domain.NewUUIDv7()
	if err := row.MarkRequested(assistID, now); err != nil {
		log.Printf("campaign: mark requested failed (generation skipped): %v", err)
		return bankRow, genUnavailable
	}
	// Stamp the requesting entrance so the two daily budgets stay independent
	// (a tap on a march-seeded idle row must count against the TAP budget).
	row.RequestOrigin = origin
	if err := d.Bank.Save(ctx, row); err != nil {
		log.Printf("campaign: persist requested set failed (generation skipped): %v", err)
		return bankRow, genUnavailable
	}

	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, assistID)
	payload := map[string]any{
		"assist_id":       assistID,
		"tenant_id":       tenantID,
		"author_gcid":     gcid,
		"content_type":    "mcq",
		"prompt":          campaignquestion.BuildPromptV2(node.Title, node.ConceptKey, rungLabel, campaignquestion.QuestionsPerRequest, d.buildCampaignPromptContext(ctx, tenantID, gcid, node, rootConceptID)),
		"requested_count": int32(campaignquestion.QuestionsPerRequest),
		"started_at":      now,
		"metadata": map[string]string{
			"cognitive_level": rungLabel,
			"surface":         "campaign",
		},
		"target_growth_edges": []string{node.ConceptKey},
		"type_plan": []map[string]any{
			{"question_type": "mcq", "count": campaignquestion.QuestionsPerRequest},
		},
		"operation":  "compose",
		"intent":     "new_question",
		"input_kind": "prompt",
	}
	if err := d.QGen.Publish(campaignAiAssistStartedV2, env, payload); err != nil {
		log.Printf("campaign: qgen publish FAILED (set marked failed; slot cascades): %v", err)
		if ferr := row.MarkFailed("qgen publish failed: "+err.Error(), now); ferr == nil {
			if serr := d.Bank.Save(ctx, row); serr != nil {
				log.Printf("campaign: persist failed set also failed: %v", serr)
			}
		}
		return row, genUnavailable
	}
	log.Printf("campaign: qgen gap request published (assist=%s concept=%s rung=%s origin=%s)", assistID, node.ConceptKey, rungLabel, origin)
	return row, genRequested
}

// doseCampaignOutcomeResp is the campaign side-effect of a dose answer, surfaced
// on the answer response so the dose lane renders the SAME won / cleared / paced
// / counted feedback as the hex-tap practice lane (CHO-2315). Absent (nil) when
// the answer folded into no campaign.
type doseCampaignOutcomeResp struct {
	ConceptID   string `json:"concept_id"`
	ConceptKey  string `json:"concept_key"`
	Rung        int    `json:"rung"`
	ClearedRung int    `json:"cleared_rung"`
	Won         bool   `json:"won"`
	PacedToday  bool   `json:"paced_today"`
	Counted     bool   `json:"counted"`
	IsRefresher bool   `json:"is_refresher"`
}

// recordCampaignAnswer folds one SERVER-graded answer on today's campaign
// material into the campaign Grader (D6: rungs clear on server-graded correct
// answers only) and returns the ladder outcome so the dose lane can render it
// (CHO-2315). Returns (nil, nil) on the no-op paths: campaign not wired, no
// march today, the atom is not campaign material, or a benign won/deleted node
// (D9). Every other failure returns the error, so the answers door 500s rather
// than silently losing verified ladder progress.
func (s *ExtServer) recordCampaignAnswer(ctx context.Context, tenantID, gcid, atomID, atomTopic string, correct bool, now time.Time) (*doseCampaignOutcomeResp, error) {
	d := s.CampaignDose
	if d == nil {
		return nil, nil
	}
	seed := companion.DoseSeed(doseclock.Key(now), gcid)
	elected, err := electTodaysCampaign(ctx, d, tenantID, gcid, seed, now)
	if err != nil {
		return nil, err
	}
	if elected == nil || !campaignAtomMatches(elected.Node, atomID, atomTopic) {
		return nil, nil
	}
	res, rerr := d.Grader.RecordAnswer(ctx, campaign.RecordAnswerInput{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		GoalID:      elected.Goal.GoalID,
		ConceptID:   elected.Node.ConceptID,
		ConceptKey:  elected.Node.ConceptKey,
		Rung:        elected.Serve.Rung,
		Correct:     correct,
		Now:         now,
	})
	if rerr != nil {
		// A won/deleted node has left the campaign: benign no-op (D9), no banner.
		if errors.Is(rerr, campaign.ErrAlreadyWon) || errors.Is(rerr, campaign.ErrDeleted) {
			return nil, nil
		}
		return nil, rerr
	}
	o := res.Outcome
	return &doseCampaignOutcomeResp{
		ConceptID:   elected.Node.ConceptID,
		ConceptKey:  elected.Node.ConceptKey,
		Rung:        int(elected.Serve.Rung),
		ClearedRung: int(o.ClearedRung),
		Won:         o.Won,
		PacedToday:  o.PacedToday,
		Counted:     o.Counted,
		IsRefresher: o.IsRefresher,
	}, nil
}

// campaignAtomMatches reports whether a graded atom is today's campaign
// material: one of the focus node's own atoms, or a topic slug match on the
// node's key/title (the same surfaces the composer serves — D7: any graded
// practice on the march's concept counts; pacing gates the advance).
func campaignAtomMatches(node *conceptgraph.ConceptNode, atomID, atomTopic string) bool {
	if node == nil {
		return false
	}
	for _, ref := range node.AtomRefs {
		if ref == atomID {
			return true
		}
	}
	slug := lw.NormalizeConceptKey(atomTopic)
	if slug == "" {
		return false
	}
	return slug == lw.NormalizeConceptKey(node.ConceptKey) || slug == lw.NormalizeConceptKey(node.Title)
}
