// dose_campaign.go — Campaign axis of the Daily Dose (ADR-227 D12, CHO-2081).
//
// The Campaign turns the focused goal's focus node into today's march: the
// composer serves it ahead of the other slots at the rung decided by the
// campaign grader (internal/domain/campaign), and with N focused goals the
// seeded daily RNG elects exactly ONE campaign per day, weighted by
// due-ness/recency. Scheduling stays deterministic — the LLM never schedules
// (ADR-202). The adapter assembles the CampaignInput/CampaignCandidate
// carriers so this file stays decoupled from the campaign + retention
// repositories (mirrors the GrowthEdgeInput pattern).
package companion

import (
	"math/rand"
	"sort"
)

// CampaignInput is the composer's view of today's ONE campaign march: the
// goal elected by PickCampaignGoal + the serve decision from the campaign
// grader (rung + refresher, ADR-227 D8). CandidateAtomIDs are pre-resolved
// content candidates (the WS-C3 retrieval/QuestionBank lane feeds these);
// when they run short the composer falls back to seeds whose topic slug
// matches the focus concept's key/label (mirrors the growth-edge resolver).
type CampaignInput struct {
	GoalID           string
	ConceptID        string
	ConceptKey       string
	ConceptLabel     string
	Rung             int
	IsRefresher      bool
	CandidateAtomIDs []string
}

// pickCampaignSlots fills up to DoseCampaignCount campaign slots for the
// march (ADR-227 D12). Resolution order:
//  1. explicit CandidateAtomIDs, in order (present in the seed universe,
//     unpicked) — the WS-C3 lane feeds rung-targeted content here;
//  2. slug fallback — seeds whose topic slug matches the focus concept's
//     ConceptKey or ConceptLabel, AtomID ascending (deterministic).
//
// Fail-soft: a short pool simply leaves slots for the cascade + top-ups
// (the 5-card budget always holds).
func pickCampaignSlots(entries []DailyDoseEntry, picked map[string]bool, in DailyDoseInput) []DailyDoseEntry {
	c := in.Campaign
	if c == nil {
		return entries
	}
	seedByID := seedIndex(in.Seeds)

	// 1) Explicit candidates, in order.
	for _, atomID := range c.CandidateAtomIDs {
		if len(entries) >= DoseTargetSize ||
			countByReason(entries, DoseReasonCampaign) >= DoseCampaignCount {
			return entries
		}
		seed, ok := seedByID[atomID]
		if !ok || picked[seed.AtomID] {
			continue
		}
		entries = appendCampaignEntry(entries, picked, c, seed)
	}

	// 2) Slug fallback — focus-concept topic match, AtomID ascending.
	slugs := make(map[string]bool, 2)
	if k := normalizeTopicSlug(c.ConceptKey); k != "" {
		slugs[k] = true
	}
	if k := normalizeTopicSlug(c.ConceptLabel); k != "" {
		slugs[k] = true
	}
	pool := make([]AtomSeed, 0, len(in.Seeds))
	for _, seed := range in.Seeds {
		if picked[seed.AtomID] || !slugs[normalizeTopicSlug(seed.Topic)] {
			continue
		}
		pool = append(pool, seed)
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].AtomID < pool[j].AtomID })
	for _, seed := range pool {
		if len(entries) >= DoseTargetSize ||
			countByReason(entries, DoseReasonCampaign) >= DoseCampaignCount {
			return entries
		}
		entries = appendCampaignEntry(entries, picked, c, seed)
	}
	return entries
}

// appendCampaignEntry appends a campaign-tagged entry stamped with the march
// metadata (goal + concept + rung) so the answer path can route grading into
// the campaign grader, and marks the atom picked.
func appendCampaignEntry(entries []DailyDoseEntry, picked map[string]bool, c *CampaignInput, seed AtomSeed) []DailyDoseEntry {
	picked[seed.AtomID] = true
	return append(entries, DailyDoseEntry{
		AtomID:             seed.AtomID,
		Topic:              seed.Topic,
		Title:              seed.Title,
		DoseReason:         DoseReasonCampaign,
		CampaignGoalID:     c.GoalID,
		CampaignConceptID:  c.ConceptID,
		CampaignConceptKey: c.ConceptKey,
		CampaignRung:       c.Rung,
		CampaignRefresher:  c.IsRefresher,
	})
}

// CampaignCandidate is one goal eligible for today's march: a focus node is
// assigned, the node is unwon, and the goal's map is not dose-pref-excluded.
// The ADAPTER enforces eligibility — this carrier only ranks. RetentionR is
// the focus node's topic_retention R(t) (nil = no row planted yet);
// DaysSinceAdvance counts from campaign_node_progress.last_advance_date
// (0 = the ladder advanced today).
type CampaignCandidate struct {
	GoalID           string
	ConceptID        string
	ConceptKey       string
	ConceptLabel     string
	RetentionR       *float64
	DaysSinceAdvance int
}

// campaignWeight ranks a candidate by due-ness + staleness (ADR-227 D12
// "weighted by due-ness/recency"): the colder the focus node's retention and
// the longer since its ladder advanced, the likelier today's march. A small
// floor keeps warm/fresh goals reachable (fat tail); unknown retention is
// mid-weighted (0.5) so a brand-new campaign neither dominates nor starves.
func campaignWeight(c CampaignCandidate) float64 {
	r := 0.5
	if c.RetentionR != nil {
		r = *c.RetentionR
		if r < 0 {
			r = 0
		}
		if r > 1 {
			r = 1
		}
	}
	staleDays := float64(c.DaysSinceAdvance)
	if staleDays > 7 {
		staleDays = 7 // a week of staleness saturates the recency boost
	}
	return 0.1 + (1 - r) + 0.5*(staleDays/7)
}

// PickCampaignGoal elects today's ONE campaign among the eligible goals
// (ADR-227 D12) via the seeded daily RNG (seed = DoseSeed(date, gcid)): the
// pick is stable within a day, rotates day-to-day, and is independent of
// candidate order (candidates are sorted by GoalID before sampling; the RNG
// stream is private to this election, mirroring how the weakness + curiosity
// samplers each seed their own). Returns ok=false when no goal is eligible —
// no campaign day, and the composer degenerates byte-identically.
func PickCampaignGoal(cands []CampaignCandidate, seed int64) (CampaignCandidate, bool) {
	if len(cands) == 0 {
		return CampaignCandidate{}, false
	}
	sorted := append([]CampaignCandidate(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].GoalID < sorted[j].GoalID })
	rng := rand.New(rand.NewSource(seed))
	order := weightedOrder(len(sorted), func(i int) float64 { return campaignWeight(sorted[i]) }, rng)
	return sorted[order[0]], true
}
