// atom_refresh_subscriber.go - the ADR-244 D5 standing refresh trigger.
// Consumes chora.creation.atom.published.v1 / atom.updated.v1 and folds each
// into AT MOST atomrefresh.MaxProposalsPerAtomEvent mana-exempt
// concept_suggestion.requested.v1 events, one per reachable (learner, focal)
// pairing. Proposals only, zero atom_refs writes: per ADR-212 D9 the atom
// events are refresh SUGGESTIONS the Companion may offer, never authority that
// mutates the learner's graph, and ADR-244 D1's learner-accepted single
// writer stays the sole binding path.
//
// Reachability is a design input (2026-08-16 UI walk): pending suggestions
// only render Add/Dismiss on a WON focal hex inside a companion-attached
// goal, so the trigger targets exactly those pairings instead of shipping
// supply into a blocked pipe. Lexical affinity (atomrefresh.RankCandidates)
// then drops off-topic pairings before any LLM spend.
//
// Mana: the request carries request_source=atom_refresh; the fog orchestrator
// maps it to a dedicated un-catalogued action_code at the model gateway, so
// the trigger is exempt at the METERING seam while Armor and the gateway
// chokepoint stay fully in force (the campaign_free_reveal posture, ADR-177
// honoured, no bypass).
//
// Idempotency (binding: the semantic pairing, NOT the event id): claim-then-
// mark on the atom_refresh_ledger keyed (tenant, learner, atom, focal).
// Creation's backfill-published / backfill-topic-tags re-emit atom.published
// for every atom of a tenant under FRESH event ids, so event-id dedupe alone
// would storm; the semantic key bounds each pairing to one proposal, ever.
// The per-learner-per-day cap (CountClaimedSince) bounds the LLM spend any
// publish burst can create. The requested.v1 idempotency_key is deterministic
// on (trigger event id, focal) so the rare crash-window re-publish stays
// downstream-dedupable.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// AtomRefreshTrigger reasons, stamped for logs/traces only (both reasons run
// the identical policy).
const (
	AtomRefreshReasonPublished = "published"
	AtomRefreshReasonUpdated   = "updated"
)

// AtomRefreshTrigger is one decoded atom event (published or updated).
// EventID is the creation-side event id; the ledger records it as audit and
// the requested.v1 idempotency key derives from it.
type AtomRefreshTrigger struct {
	TenantID    string
	AtomID      string
	EventID     string
	Traceparent string
	Tracestate  string
	Reason      string
}

// TenantGoalSource lists every live companion-attached rooted goal of the
// tenant (pg.GoalRepo.ListCompanionedByTenant). Narrow on purpose: the
// trigger never needs the full goal.Repository surface.
type TenantGoalSource interface {
	ListCompanionedByTenant(ctx context.Context, tenantID string) ([]*goal.Goal, error)
}

// AtomIndexSource reads the triggering atom's projection row (title + tags +
// servability). Narrow view of atom_index.Repository.
type AtomIndexSource interface {
	Get(ctx context.Context, atomID string) (*atom_index.AtomIndex, error)
}

// AtomRefreshSubscriber folds atom events into bounded, reachable, mana-exempt
// binding proposals.
type AtomRefreshSubscriber struct {
	ledger   atomrefresh.Ledger
	goals    TenantGoalSource
	concepts conceptgraph.ConceptNodeRepository
	edges    conceptgraph.EdgeRepository
	progress campaign.ProgressRepository
	atoms    AtomIndexSource
	pub      events.Publisher
	// catalogue resolves the ADR-244 D2 entitled atom catalogue (the same
	// resolution the learner door and the free-on-win reveal use). OPTIONAL
	// like the campaign subscriber's: absent, the trigger can neither prove
	// entitlement nor let the Companion cite the atom, so it emits nothing
	// (loudly). Sharing one resolution is the point: the trigger must never
	// propose an atom the learner is then refused on accept.
	catalogue SuggestionCatalogueResolver
}

// NewAtomRefreshSubscriber wires the subscriber. All listed deps are
// mandatory (feedback_no_stubs_real_wiring): a nil dep is a wiring bug, so
// panic at construction rather than fail-open at consume time.
func NewAtomRefreshSubscriber(
	ledger atomrefresh.Ledger,
	goals TenantGoalSource,
	concepts conceptgraph.ConceptNodeRepository,
	edges conceptgraph.EdgeRepository,
	progress campaign.ProgressRepository,
	atoms AtomIndexSource,
	pub events.Publisher,
) *AtomRefreshSubscriber {
	if ledger == nil || goals == nil || concepts == nil || edges == nil || progress == nil || atoms == nil || pub == nil {
		panic("subscribers: AtomRefreshSubscriber requires ledger, goals, concepts, edges, progress, atom index and publisher")
	}
	return &AtomRefreshSubscriber{
		ledger: ledger, goals: goals, concepts: concepts, edges: edges,
		progress: progress, atoms: atoms, pub: pub,
	}
}

// WithCatalogue attaches the catalogue resolver (ADR-244 D2). Separate from
// the constructor so wiring and tests mirror the campaign subscriber.
func (s *AtomRefreshSubscriber) WithCatalogue(r SuggestionCatalogueResolver) *AtomRefreshSubscriber {
	s.catalogue = r
	return s
}

// learnerGraph caches one learner's per-call reads so several goals of the
// same learner share them.
type learnerGraph struct {
	nodes []*conceptgraph.ConceptNode
	edges []*conceptgraph.Edge
	won   map[string]bool
}

// Handle processes one atom event delivery. Returning an error NACKs
// (Pub/Sub redelivers, DLQ after max attempts); returning nil acks.
func (s *AtomRefreshSubscriber) Handle(ctx context.Context, t AtomRefreshTrigger) error {
	if strings.TrimSpace(t.TenantID) == "" || strings.TrimSpace(t.AtomID) == "" || strings.TrimSpace(t.EventID) == "" {
		return fmt.Errorf("atom_refresh: missing mandatory fields (tenant=%q atom=%q event=%q)",
			t.TenantID, t.AtomID, t.EventID)
	}
	ctx = tracing.WithTenantID(ctx, t.TenantID)

	atom, err := s.atoms.Get(ctx, t.AtomID)
	if err != nil {
		if errors.Is(err, atom_index.ErrNotFound) {
			// Honest omission (mirrors resolveEntitledAtoms): an atom the
			// projection has never seen has nothing a learner could bind;
			// NACKing would DLQ-loop a permanently absent row.
			log.Printf("consumption: atom refresh SKIPPED - atom %s not in atom_index (tenant=%s event=%s)",
				t.AtomID, t.TenantID, t.EventID)
			return nil
		}
		return fmt.Errorf("atom_refresh: load atom index: %w", err)
	}
	if atom == nil || !atom.Servable() {
		// Nothing a learner could ever bind: ack quietly (the playability
		// flip owns projection state; this trigger only proposes).
		log.Printf("consumption: atom refresh SKIPPED - atom %s not servable (tenant=%s reason=%s event=%s)",
			t.AtomID, t.TenantID, t.Reason, t.EventID)
		return nil
	}

	goals, err := s.goals.ListCompanionedByTenant(ctx, t.TenantID)
	if err != nil {
		return fmt.Errorf("atom_refresh: list companioned goals: %w", err)
	}
	if len(goals) == 0 {
		return nil // no reachable pipe anywhere in the tenant
	}

	// Enumerate reachable candidates: WON concepts inside each companioned
	// goal's live subtree, excluding foci that already carry the atom.
	graphs := map[string]*learnerGraph{}
	type target struct {
		g     *goal.Goal
		focal *conceptgraph.ConceptNode
		theme string
	}
	targets := map[string]target{} // keyed learner|focal, first goal wins
	var cands []atomrefresh.Candidate
	for _, g := range goals {
		if g == nil || g.AttachedCompanionID == nil || g.RootConceptID == nil {
			continue // defensive; the SQL already filters
		}
		lg, err := s.learnerGraph(ctx, t.TenantID, g.LearnerGCID, graphs)
		if err != nil {
			return err
		}
		subtree := conceptgraph.SubtreeConceptIDs(derefNodes(lg.nodes), derefEdges(lg.edges), *g.RootConceptID)
		theme := s.resolveGoalTheme(lg, g)
		for _, n := range lg.nodes {
			if n == nil || n.DeletedAt != nil || !subtree[n.ConceptID] || !lg.won[n.ConceptID] {
				continue
			}
			if containsString(n.AtomRefs, t.AtomID) {
				continue // already bound here; nothing to propose
			}
			key := g.LearnerGCID + "|" + n.ConceptID
			if _, seen := targets[key]; seen {
				continue
			}
			targets[key] = target{g: g, focal: n, theme: theme}
			cands = append(cands, atomrefresh.Candidate{
				LearnerGCID:    g.LearnerGCID,
				GoalID:         g.GoalID,
				FocalConceptID: n.ConceptID,
				FocalTitle:     n.Title,
				Theme:          theme,
			})
		}
	}
	if len(cands) == 0 {
		return nil
	}

	ranked := atomrefresh.RankCandidates(atomrefresh.AtomSignal{Title: atom.Title, Tags: atom.TopicTags}, cands)
	if len(ranked) == 0 {
		log.Printf("consumption: atom refresh SKIPPED - atom %s shares no vocabulary with any reachable focal (tenant=%s event=%s)",
			t.AtomID, t.TenantID, t.EventID)
		return nil
	}

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dailyCount := map[string]int{}
	emitted := 0
	for _, c := range ranked {
		if emitted >= atomrefresh.MaxProposalsPerAtomEvent {
			break
		}
		tgt := targets[c.LearnerGCID+"|"+c.FocalConceptID]
		lctx := tracing.WithGCID(ctx, c.LearnerGCID)

		// Per-learner daily cost cap, read once per learner then tracked
		// locally so one event cannot overshoot it either.
		used, ok := dailyCount[c.LearnerGCID]
		if !ok {
			used, err = s.ledger.CountClaimedSince(lctx, t.TenantID, c.LearnerGCID, dayStart)
			if err != nil {
				return fmt.Errorf("atom_refresh: count daily claims: %w", err)
			}
		}
		if used >= atomrefresh.MaxProposalsPerLearnerPerDay {
			log.Printf("consumption: atom refresh SKIPPED - learner %s over the daily cap (tenant=%s atom=%s)",
				c.LearnerGCID, t.TenantID, t.AtomID)
			dailyCount[c.LearnerGCID] = used
			continue
		}

		claim, err := atomrefresh.NewRefreshClaim(t.TenantID, c.LearnerGCID, t.AtomID, c.FocalConceptID, t.EventID, now)
		if err != nil {
			return fmt.Errorf("atom_refresh: %w", err)
		}
		claimed, existing, err := s.ledger.Claim(lctx, claim)
		if err != nil {
			return fmt.Errorf("atom_refresh: claim: %w", err)
		}
		if claimed {
			// A fresh claim consumes daily budget; an existing row was
			// already inside the CountClaimedSince read.
			dailyCount[c.LearnerGCID] = used + 1
		} else {
			dailyCount[c.LearnerGCID] = used
			if existing != nil && existing.PublishedAt != nil {
				continue // once-ever bound already satisfied (redelivery or backfill re-emit)
			}
			// Crash-window (claimed, never published) or a prior post-claim
			// skip: fall through and re-decide; the deterministic
			// idempotency_key keeps a re-publish downstream-dedupable.
		}

		published, err := s.publishProposal(lctx, t, tgt.g, tgt.focal, tgt.theme, graphs[c.LearnerGCID], claim.ID, now)
		if err != nil {
			return err
		}
		if published {
			emitted++
		}
	}
	return nil
}

// publishProposal resolves the entitled catalogue for (learner, focal),
// requires the triggering atom in it, then publishes the suggestion request
// and marks the ledger. An unentitled atom is a quiet skip (false, nil): the
// unpublished ledger row stays as the audit trace, mirroring the campaign
// subscriber's vanished-focal posture.
func (s *AtomRefreshSubscriber) publishProposal(
	ctx context.Context,
	t AtomRefreshTrigger,
	g *goal.Goal,
	focal *conceptgraph.ConceptNode,
	theme string,
	lg *learnerGraph,
	requestID string,
	now time.Time,
) (bool, error) {
	if s.catalogue == nil {
		log.Printf("consumption: atom refresh proposal SKIPPED - catalogue resolver not wired (tenant=%s)", t.TenantID)
		return false, nil
	}
	cat := s.catalogue.ResolveSuggestionCatalogue(ctx, t.TenantID, g.LearnerGCID, theme, focal.Title, focal.AtomRefs)
	if !catalogueContains(cat, t.AtomID) {
		// ADR-244 D2: an atom the learner could not attach manually is an
		// atom the Companion may not propose. The claim row stays unpublished
		// as the audit trace of the skip.
		log.Printf("consumption: atom refresh proposal SKIPPED - atom %s not in the entitled catalogue (tenant=%s learner=%s focal=%s)",
			t.AtomID, t.TenantID, g.LearnerGCID, focal.ConceptID)
		return false, nil
	}

	existingConcepts := make([]events.ExistingConcept, 0, len(lg.nodes))
	for _, c := range lg.nodes {
		if c == nil || c.DeletedAt != nil || c.ConceptID == focal.ConceptID {
			continue
		}
		existingConcepts = append(existingConcepts, events.ExistingConcept{ConceptID: c.ConceptID, Title: c.Title})
		if len(existingConcepts) >= events.MaxExistingConceptsInSuggestionRequest {
			break
		}
	}

	// ADR-247 lineage framing, best-effort like the campaign subscriber.
	goalTitle := ""
	var ancestors []string
	if g.RootConceptID != nil {
		anc := conceptgraph.WalkAncestors(focal.ConceptID, *g.RootConceptID, lg.nodes, lg.edges)
		goalTitle = anc.GoalTitle
		ancestors = anc.Ancestors
	}

	companionID := ""
	if g.AttachedCompanionID != nil {
		companionID = *g.AttachedCompanionID
	}
	env := events.NewEnvelope(t.TenantID, g.LearnerGCID,
		tracing.EnsureTraceparent(t.Traceparent), t.Tracestate,
		"concept_suggestion.requested.atom_refresh."+t.EventID+"."+focal.ConceptID)
	payload := events.SuggestionRequestPayload(events.SuggestionRequestInput{
		TenantID:       t.TenantID,
		LearnerGCID:    g.LearnerGCID,
		CompanionID:    companionID,
		MapTheme:       theme,
		FocalConceptID: focal.ConceptID,
		FocalTitle:     focal.Title,
		FocalAtomRefs:  focal.AtomRefs,
		Existing:       existingConcepts,
		AtomCatalogue:  cat,
		RequestedAt:    now,
		RequestSource:  events.SuggestionSourceAtomRefresh,
		SubGoal:        focal.SubGoal,
		GoalTitle:      goalTitle,
		Ancestors:      ancestors,
	})
	if err := s.pub.Publish(events.TopicConceptSuggestionRequested, env, payload); err != nil {
		return false, fmt.Errorf("atom_refresh: publish proposal: %w", err)
	}
	if err := s.ledger.MarkPublished(ctx, t.TenantID, g.LearnerGCID, t.AtomID, focal.ConceptID, requestID, time.Now().UTC()); err != nil {
		// NACK: redelivery lands in the crash-window branch and re-publishes
		// under the same idempotency_key, never a silently unmarked proposal.
		return false, fmt.Errorf("atom_refresh: mark proposal published: %w", err)
	}
	return true, nil
}

// learnerGraph loads (once per learner per delivery) the roster, edges and
// won-set backing subtree + reachability checks.
func (s *AtomRefreshSubscriber) learnerGraph(ctx context.Context, tenantID, learnerGCID string, cache map[string]*learnerGraph) (*learnerGraph, error) {
	if lg, ok := cache[learnerGCID]; ok {
		return lg, nil
	}
	lctx := tracing.WithGCID(ctx, learnerGCID)
	nodes, err := s.concepts.ListByLearner(lctx, tenantID, learnerGCID)
	if err != nil {
		return nil, fmt.Errorf("atom_refresh: list concepts: %w", err)
	}
	edges, err := s.edges.ListByLearner(lctx, tenantID, learnerGCID)
	if err != nil {
		return nil, fmt.Errorf("atom_refresh: list edges: %w", err)
	}
	progress, err := s.progress.ListByLearner(lctx, tenantID, learnerGCID)
	if err != nil {
		return nil, fmt.Errorf("atom_refresh: list campaign progress: %w", err)
	}
	won := make(map[string]bool, len(progress))
	for _, p := range progress {
		if p != nil && p.Won() {
			won[p.ConceptID] = true
		}
	}
	lg := &learnerGraph{nodes: nodes, edges: edges, won: won}
	cache[learnerGCID] = lg
	return lg, nil
}

// resolveGoalTheme mirrors the learner door + campaign subscriber: root
// concept title, NorthStarNote fallback, then "discovery".
func (s *AtomRefreshSubscriber) resolveGoalTheme(lg *learnerGraph, g *goal.Goal) string {
	if g.RootConceptID != nil {
		for _, n := range lg.nodes {
			if n != nil && n.ConceptID == *g.RootConceptID && n.DeletedAt == nil {
				if t := strings.TrimSpace(n.Title); t != "" {
					return t
				}
			}
		}
	}
	if n := strings.TrimSpace(g.NorthStarNote); n != "" {
		return n
	}
	return "discovery"
}

func derefNodes(in []*conceptgraph.ConceptNode) []conceptgraph.ConceptNode {
	out := make([]conceptgraph.ConceptNode, 0, len(in))
	for _, n := range in {
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}

func derefEdges(in []*conceptgraph.Edge) []conceptgraph.Edge {
	out := make([]conceptgraph.Edge, 0, len(in))
	for _, e := range in {
		if e != nil {
			out = append(out, *e)
		}
	}
	return out
}

func containsString(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func catalogueContains(cat []events.CatalogueAtom, atomID string) bool {
	for _, c := range cat {
		if c.AtomID == atomID {
			return true
		}
	}
	return false
}
