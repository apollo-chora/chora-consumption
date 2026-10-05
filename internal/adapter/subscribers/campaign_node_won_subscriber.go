// campaign_node_won_subscriber.go - the free-on-win fog reveal (WS-C4,
// CHO-2083, ADR-227 D2 + addendum #1). Consumes
// chora.consumption.campaign.node_won.v1 and folds each node win into exactly
// ONE free concept_suggestion.requested.v1 with the WON node as focal - the
// reveal drives the concept_suggestions pipeline (fog orchestrator, ns
// ai-kernel), NEVER the retiring kg_hexagon_nodes atom-fog, and it emits no
// legacy kg.hexagon_expanded.v1 (the WS-C5 hex_expand XP source cannot
// double-fire off this path - addendum #6).
//
// Mana: the request carries request_source=campaign_free_reveal - the fog
// orchestrator selects an un-catalogued action_code for it at the
// model-gateway, so the reveal is exempt at the METERING seam while Armor and
// the gateway chokepoint stay fully in force (ADR-177 honoured, no bypass).
//
// Idempotency (binding: the node_won event id): claim-then-mark on the
// RevealLedger. Claim first serialises concurrent redeliveries; a claimed row
// with PublishedAt set acks as a duplicate; a claimed row with PublishedAt nil
// is the crash-window (publish never happened) and is re-published. The
// requested.v1 idempotency_key is deterministic on the node_won event id so
// the rare publish-retry after a mark failure stays downstream-dedupable.
//
// Reveals are PERMANENT: this subscriber only ever appends (a ledger row + a
// suggestion request) - nothing here (or anywhere in WS-C4) re-hides revealed
// territory.
package subscribers

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// CampaignNodeWonPayload mirrors the BINARY-proto
// chora.consumption.campaign.node_won.v1 body (decoded at the push handler;
// EventID/Traceparent/Tracestate come from its embedded envelope).
type CampaignNodeWonPayload struct {
	TenantID    string
	LearnerGCID string
	GoalID      string
	ConceptID   string
	ConceptKey  string
	EventID     string
	Traceparent string
	Tracestate  string
}

// CampaignRevealEmitter emits node_revealed.v1 after a successful free-on-win
// reveal publish (WS-C7, CHO-2083 follow-up). Implemented by
// *events.CampaignEmitter.
type CampaignRevealEmitter interface {
	NodeRevealed(ctx context.Context, in events.CampaignNodeRevealedInput) error
}

// CampaignNodeWonSubscriber folds node wins into free fog reveals.
type CampaignNodeWonSubscriber struct {
	ledger   campaign.RevealLedger
	goals    goal.Repository
	concepts conceptgraph.ConceptNodeRepository
	pub      events.Publisher
	reveal   CampaignRevealEmitter
	// catalogue resolves the ADR-244 D2 entitled atom catalogue. OPTIONAL, and
	// the only optional dep here: without it the free-on-win reveal still
	// publishes and the Companion still proposes concepts, it just cannot cite
	// atoms. That is the pre-ADR-244 behaviour, strictly better than refusing
	// the learner their reveal. Its absence is logged at wire time, because an
	// always-empty catalogue is otherwise indistinguishable from a Companion
	// choosing not to cite anything.
	catalogue SuggestionCatalogueResolver

	// weakness + edges enrich the free-on-win reveal with the ADR-247 Capability B
	// (CHO-2328) learner-aware fog inputs, mirroring the learner door. weakness is
	// the narrow WeaknessContextLoader (the focal node's diagnosed weakness, F2);
	// edges backs the goal/ancestor lineage walk (F3). BOTH are OPTIONAL (attached
	// via WithWeakness / WithEdges): absent, they degrade the request toward the
	// sub-goal + theme framing and the reveal still publishes. They are deliberately
	// NOT in the mandatory-nil constructor guard - a win must always reveal.
	weakness lw.WeaknessContextLoader
	edges    conceptgraph.EdgeRepository
}

// SuggestionCatalogueResolver hands the free-on-win reveal the same ADR-243
// entitled set the manual picker and the learner door use. Sharing one
// resolution is the point: if they drifted, the Companion could propose an atom
// the learner is then refused on accept.
//
// ADR-245: mapTheme + focalTitle carry the goal's topic identity so the
// catalogue comes back ORDERED by affinity. The free-on-win reveal passes the
// same two values the learner door does, because a Companion that cites
// on-theme atoms at the door and cross-domain ones on a win is worse than one
// that is consistently wrong.
type SuggestionCatalogueResolver interface {
	ResolveSuggestionCatalogue(ctx context.Context, tenantID, gcid, mapTheme, focalTitle string, focalAtomRefs []string) []events.CatalogueAtom
}

// WithCatalogue attaches the catalogue resolver (ADR-244 D2). Separate from the
// constructor so existing wiring and tests are untouched.
func (s *CampaignNodeWonSubscriber) WithCatalogue(r SuggestionCatalogueResolver) *CampaignNodeWonSubscriber {
	s.catalogue = r
	return s
}

// WithWeakness attaches the ADR-247 Capability B narrow weakness loader (F2).
// Optional (mirrors WithCatalogue) so existing wiring and tests are untouched; a
// nil loader simply leaves the reveal's weakness unset (JSON null).
func (s *CampaignNodeWonSubscriber) WithWeakness(w lw.WeaknessContextLoader) *CampaignNodeWonSubscriber {
	s.weakness = w
	return s
}

// WithEdges attaches the edge repo backing the ADR-247 Capability B goal/ancestor
// lineage walk (F3). Optional (mirrors WithCatalogue); absent, the reveal carries
// an empty lineage.
func (s *CampaignNodeWonSubscriber) WithEdges(e conceptgraph.EdgeRepository) *CampaignNodeWonSubscriber {
	s.edges = e
	return s
}

// NewCampaignNodeWonSubscriber wires the subscriber. All deps are mandatory
// (feedback_no_stubs_real_wiring) - a nil dep is a wiring bug, so panic at
// construction rather than fail-open at consume time.
func NewCampaignNodeWonSubscriber(
	ledger campaign.RevealLedger,
	goals goal.Repository,
	concepts conceptgraph.ConceptNodeRepository,
	pub events.Publisher,
	reveal CampaignRevealEmitter,
) *CampaignNodeWonSubscriber {
	if ledger == nil || goals == nil || concepts == nil || pub == nil || reveal == nil {
		panic("subscribers: CampaignNodeWonSubscriber requires ledger, goals, concepts, publisher and reveal emitter")
	}
	return &CampaignNodeWonSubscriber{ledger: ledger, goals: goals, concepts: concepts, pub: pub, reveal: reveal}
}

// Handle processes one node_won delivery. Returning an error NACKs (Pub/Sub
// redelivers, DLQ after max attempts); returning nil acks.
func (s *CampaignNodeWonSubscriber) Handle(ctx context.Context, p CampaignNodeWonPayload) error {
	if strings.TrimSpace(p.TenantID) == "" || strings.TrimSpace(p.LearnerGCID) == "" ||
		strings.TrimSpace(p.GoalID) == "" || strings.TrimSpace(p.ConceptID) == "" ||
		strings.TrimSpace(p.EventID) == "" {
		return fmt.Errorf("campaign_node_won: missing mandatory fields (tenant=%q gcid=%q goal=%q concept=%q event=%q)",
			p.TenantID, p.LearnerGCID, p.GoalID, p.ConceptID, p.EventID)
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)

	claim, err := campaign.NewRevealClaim(p.TenantID, p.LearnerGCID, p.GoalID, p.ConceptID, p.EventID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("campaign_node_won: %w", err)
	}
	claimed, existing, err := s.ledger.Claim(ctx, claim)
	if err != nil {
		return fmt.Errorf("campaign_node_won: claim reveal: %w", err)
	}
	if !claimed {
		if existing != nil && existing.PublishedAt != nil {
			return nil // genuine redelivery of an already-revealed win - ack
		}
		// Crash-window: claimed on a prior delivery, publish never marked -
		// fall through and (re-)publish. The deterministic idempotency_key
		// keeps the rare double-publish downstream-dedupable.
	}

	// The won node is the focal. A vanished node (soft-deleted post-win, e.g.
	// merge/split ahead of WS-C6) has nothing to reveal - ack loudly; the
	// unpublished ledger row stays as the audit trace of the skip.
	focal, err := s.concepts.GetByID(ctx, p.TenantID, p.LearnerGCID, p.ConceptID)
	if err != nil {
		return fmt.Errorf("campaign_node_won: load focal concept: %w", err)
	}
	if focal == nil {
		log.Printf("consumption: campaign free reveal SKIPPED - won node %s no longer live (tenant=%s gcid=%s goal=%s event=%s)",
			p.ConceptID, p.TenantID, p.LearnerGCID, p.GoalID, p.EventID)
		return nil
	}

	// Attribution mirrors the learner door: companion + theme from the Goal
	// (best-effort - an unattached goal yields an unattributed fog request).
	companionID := ""
	mapTheme := "discovery"
	g, err := s.goals.GetByID(ctx, p.TenantID, p.LearnerGCID, p.GoalID)
	if err != nil {
		return fmt.Errorf("campaign_node_won: load goal: %w", err)
	}
	if g != nil {
		if g.AttachedCompanionID != nil {
			companionID = *g.AttachedCompanionID
		}
		mapTheme = s.resolveTheme(ctx, p.TenantID, p.LearnerGCID, g)
	}

	// The learner's existing concepts (bounded, focal excluded) so the fog
	// doesn't re-propose them - same contract as the learner door.
	roster, err := s.concepts.ListByLearner(ctx, p.TenantID, p.LearnerGCID)
	if err != nil {
		return fmt.Errorf("campaign_node_won: list concepts: %w", err)
	}
	existingConcepts := make([]events.ExistingConcept, 0, len(roster))
	for _, c := range roster {
		if c == nil || c.ConceptID == p.ConceptID {
			continue
		}
		existingConcepts = append(existingConcepts, events.ExistingConcept{ConceptID: c.ConceptID, Title: c.Title})
		if len(existingConcepts) >= events.MaxExistingConceptsInSuggestionRequest {
			break
		}
	}

	// ADR-247 Capability B (CHO-2328): frame the free-on-win reveal with the WON
	// node's sub-goal + diagnosed weakness + goal/ancestor lineage, mirroring the
	// learner door. Best-effort + nil-tolerant (see buildRevealEnrichment); the
	// reveal still publishes when the optional deps are absent.
	subGoal, weakness, goalTitle, ancestors := s.buildRevealEnrichment(ctx, p.TenantID, p.LearnerGCID, focal, g, roster)

	requestID := claim.ID
	env := events.NewEnvelope(p.TenantID, p.LearnerGCID,
		tracing.EnsureTraceparent(p.Traceparent), p.Tracestate,
		"concept_suggestion.requested.campaign."+p.EventID)
	payload := events.SuggestionRequestPayload(events.SuggestionRequestInput{
		TenantID:       p.TenantID,
		LearnerGCID:    p.LearnerGCID,
		CompanionID:    companionID,
		MapTheme:       mapTheme,
		FocalConceptID: p.ConceptID,
		FocalTitle:     focal.Title,
		FocalAtomRefs:  focal.AtomRefs,
		Existing:       existingConcepts,
		AtomCatalogue:  s.resolveCatalogue(ctx, p.TenantID, p.LearnerGCID, mapTheme, focal.Title, focal.AtomRefs),
		RequestedAt:    time.Now().UTC(),
		RequestSource:  events.SuggestionSourceCampaignFreeReveal,
		SubGoal:        subGoal,
		GoalTitle:      goalTitle,
		Ancestors:      ancestors,
		Weakness:       weakness,
	})
	if err := s.pub.Publish(events.TopicConceptSuggestionRequested, env, payload); err != nil {
		return fmt.Errorf("campaign_node_won: publish free reveal: %w", err)
	}
	// Mirror the reveal as node_revealed.v1 (CHO-2083 follow-up): the reveal has
	// happened (suggestion_count unknown at trigger → 0). Emitted BEFORE
	// MarkPublished so it rides the SAME crash-window as the reveal publish
	// above - an emit failure NACKs (return error) and the redelivery re-runs
	// both under their deterministic won-event-id idempotency keys, exactly the
	// posture the MarkPublished failure below uses (never a silently-lost
	// event, never an ack-early on redelivery that would skip the re-emit).
	if err := s.reveal.NodeRevealed(ctx, events.CampaignNodeRevealedInput{
		TenantID: p.TenantID, LearnerGCID: p.LearnerGCID, GoalID: p.GoalID,
		ConceptID: p.ConceptID, ConceptKey: p.ConceptKey,
		WonEventID: p.EventID, RevealedAt: time.Now().UTC(),
		Traceparent: p.Traceparent, Tracestate: p.Tracestate,
	}); err != nil {
		return fmt.Errorf("campaign_node_won: emit node_revealed: %w", err)
	}
	if err := s.ledger.MarkPublished(ctx, p.TenantID, p.LearnerGCID, p.GoalID, p.ConceptID, requestID, time.Now().UTC()); err != nil {
		// NACK: redelivery lands in the crash-window branch and re-publishes
		// under the same idempotency_key - never a silently unmarked reveal.
		return fmt.Errorf("campaign_node_won: mark reveal published: %w", err)
	}
	return nil
}

// resolveTheme mirrors the learner door's map-theme resolution: root concept
// title → NorthStarNote → "discovery".
func (s *CampaignNodeWonSubscriber) resolveTheme(ctx context.Context, tenantID, gcid string, g *goal.Goal) string {
	if g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" {
		if c, err := s.concepts.GetByID(ctx, tenantID, gcid, *g.RootConceptID); err == nil && c != nil {
			if t := strings.TrimSpace(c.Title); t != "" {
				return t
			}
		}
	}
	if n := strings.TrimSpace(g.NorthStarNote); n != "" {
		return n
	}
	return "discovery"
}

// resolveCatalogue returns the ADR-244 D2 catalogue, or nil (loudly) when the
// resolver is unwired. Never fails the reveal.
func (s *CampaignNodeWonSubscriber) resolveCatalogue(ctx context.Context, tenantID, gcid, mapTheme, focalTitle string, focalAtomRefs []string) []events.CatalogueAtom {
	if s.catalogue == nil {
		log.Printf("campaign_node_won: free-reveal atom catalogue SKIPPED (resolver not wired) tenant=%s", tenantID)
		return nil
	}
	return s.catalogue.ResolveSuggestionCatalogue(ctx, tenantID, gcid, mapTheme, focalTitle, focalAtomRefs)
}

// buildRevealEnrichment assembles the ADR-247 Capability B fog inputs for the WON
// focal node: its sub-goal (F1), the diagnosed weakness for its concept_key (F2),
// and the goal + ancestor lineage up to the goal's root (F3). Mirrors the learner
// door's ExtServer.buildSuggestionEnrichment. Every piece is best-effort and
// nil-tolerant: an absent weakness / edge dep, a nil goal, or a load error degrades
// that piece to its empty form. It never fails the reveal - a win must always
// publish (the wire builder normalises ancestors -> [] and weakness -> JSON null).
func (s *CampaignNodeWonSubscriber) buildRevealEnrichment(ctx context.Context, tenantID, gcid string, focal *conceptgraph.ConceptNode, g *goal.Goal, roster []*conceptgraph.ConceptNode) (subGoal string, weakness *events.WeaknessPayload, goalTitle string, ancestors []string) {
	if focal == nil {
		return "", nil, "", nil
	}
	subGoal = focal.SubGoal
	if s.weakness != nil {
		if wc, err := s.weakness.LoadWeaknessContextByConceptKey(ctx, tenantID, gcid, focal.ConceptKey); err == nil && wc != nil {
			weakness = &events.WeaknessPayload{
				Descriptor:     wc.Descriptor,
				Misconceptions: wc.Misconceptions,
				Evidence:       wc.Evidence,
			}
		}
	}
	if s.edges != nil && g != nil && g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" {
		if edges, err := s.edges.ListByLearner(ctx, tenantID, gcid); err == nil {
			anc := conceptgraph.WalkAncestors(focal.ConceptID, *g.RootConceptID, roster, edges)
			goalTitle = anc.GoalTitle
			ancestors = anc.Ancestors
		}
	}
	return subGoal, weakness, goalTitle, ancestors
}
