// maps_handler.go — WS-A2 (My Knowledge unification, CHO-2005): the learner's
// map read-model. A map = a Goal (ADR-214 D1): the goal is anchored to a root
// ConceptNode and "the goal's scope = the sub-tree under that root". This surface
// projects each Goal as a map card (with concept / shaky / mastered counts) and
// serves the root-scoped painted subgraph — reusing the WS-A1 overlay engine
// (kg_growth_overlay) + the canonical MasteredConceptKeys derivation, and the
// pure conceptgraph.SubtreeConceptIDs traversal.
//
//	GET /v1/me/maps                 list maps (goals) + per-map counts
//	GET /v1/me/maps/{goalId}/graph  the sub-tree under the goal's root, painted
//
// Learner-scoped: the GCID is the validated session subject (extRequireContext),
// never a body/query param. Concepts are a SHARED flat space (no per-map column);
// a map is a rooted VIEW, so overlap across maps is the natural junction feature
// (ADR-212). All overlay reads are fail-soft (a nil/erroring weakness or retention
// repo yields an unpainted map, never a 5xx).
package http

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

const mapsByIDPrefix = "/v1/me/maps/"

// mapCardDTO is the Atlas card for one map (camelCase). tenant + learner are
// implicit (the session subject) and never echoed. Title is the root concept's
// title (the map's evolving name); NorthStarNote is the goal's motivational note.
type mapCardDTO struct {
	GoalID              string    `json:"goalId"`
	Title               string    `json:"title"`
	NorthStarNote       string    `json:"northStarNote"`
	RootConceptID       *string   `json:"rootConceptId,omitempty"`
	Kind                string    `json:"kind"`
	Status              string    `json:"status"`
	AttachedCompanionID *string   `json:"attachedCompanionId,omitempty"`
	ConceptCount        int       `json:"conceptCount"`
	ShakyCount          int       `json:"shakyCount"`
	MasteredCount       int       `json:"masteredCount"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
	// AttachedCompanionName is the stationed companion's learner-safe display
	// name, so the Atlas card and the home's defend card can NAME the defender
	// rather than print a UUID. Fail-soft: empty on an unbound goal or any miss.
	AttachedCompanionName string `json:"attachedCompanionName,omitempty"`
	// CoolingCount is how many hexes in this map's sub-tree are refresh-gated
	// (ADR-227 D15 frontier cue). CoolingHexLabel names one of them, picked
	// deterministically. Both read 0 / "" when coolingPartial is set: see
	// maps_cooling.go for why that is not the same as nothing cooling.
	CoolingCount    int    `json:"coolingCount"`
	CoolingHexLabel string `json:"coolingHexLabel,omitempty"`
}

type mapsListResp struct {
	Items []mapCardDTO `json:"items"`
	// CoolingPartial marks the cooling counts as UNREAD this request. Without
	// it a zero is ambiguous, and the home would rank the defend card off the
	// page on the strength of a read that never happened.
	CoolingPartial bool `json:"coolingPartial,omitempty"`
}

// mapGraphResp is the root-scoped painted subgraph. The embedded conceptGraphResp
// flattens {concepts, edges} (the SAME painted shape the /v1/me/concept-graph read
// serves), so the FE canvas renders a map identically to the whole graph.
type mapGraphResp struct {
	GoalID        string  `json:"goalId"`
	Title         string  `json:"title"`
	RootConceptID *string `json:"rootConceptId,omitempty"`
	// Goal-level lens context carried alongside the graph so the canvas needs one
	// call: attachedCompanionId (WS-E summon/Companion lens) + personalCompletedAt
	// (WS-D Mastery lens "done for me", ADR-213).
	AttachedCompanionID *string `json:"attachedCompanionId,omitempty"`
	// AttachedCompanionName is the bound companion's learner-safe display name
	// (CHO-2109 — the campaign HUD names the marcher). Name only, never a
	// companion UUID beyond the id above; omitted fail-soft on any miss.
	AttachedCompanionName *string    `json:"attachedCompanionName,omitempty"`
	PersonalCompletedAt   *time.Time `json:"personalCompletedAt,omitempty"`
	// Campaign is the ADR-227 D15/D16 campaign overlay (WS-C7): present whenever
	// the goal has a root; omitted (nil) on any fail-soft read error so the map
	// still renders, never a 5xx.
	Campaign *mapCampaignDTO `json:"campaign,omitempty"`
	// RoadsPartial marks the Roads layer as UNREAD this request (C4, plan §8).
	// Without it an empty roads[] is ambiguous: the drawer cannot tell "this
	// concept is on no certificate path" from "we could not look", and would
	// tell the learner the first while meaning the second. Mirrors leadPartial
	// on GET /v1/me/goals.
	RoadsPartial bool `json:"roadsPartial,omitempty"`
	conceptGraphResp
}

// mapCampaignDTO overlays the campaign state on a rooted map (ADR-227 D15/D16,
// camelCase). frontier* summarise the goal subtree; nodes carries per-concept
// ladder state (an ABSENT key = an unstarted frontier node, rungs 0).
type mapCampaignDTO struct {
	FocusConceptID   *string                       `json:"focusConceptId,omitempty"`
	CampaignSealedAt *time.Time                    `json:"campaignSealedAt,omitempty"`
	FrontierTotal    int                           `json:"frontierTotal"`
	FrontierWon      int                           `json:"frontierWon"`
	CanSeal          bool                          `json:"canSeal"`
	Nodes            map[string]mapCampaignNodeDTO `json:"nodes"`
}

// mapCampaignNodeDTO is one node's ladder state. cooling is the D15 refresh cue
// computed from the campaign's own concept-key retention (never
// growthEdge.isDue).
type mapCampaignNodeDTO struct {
	RungsCleared       int        `json:"rungsCleared"`
	CurrentRungCorrect int        `json:"currentRungCorrect"`
	WonAt              *time.Time `json:"wonAt,omitempty"`
	Cooling            bool       `json:"cooling"`
	AdvancedToday      bool       `json:"advancedToday"`
}

// handleMeMaps — GET /v1/me/maps. Lists the learner's goals as maps, each with
// the counts driving the Atlas card (concepts in the rooted sub-tree, shaky,
// mastered). Reuses the A1 overlay paint + mastered-key set, computed ONCE per
// request and shared across every card.
func (s *ExtServer) handleMeMaps(w http.ResponseWriter, r *http.Request) {
	if s.Goals == nil || s.Concepts == nil || s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "MAPS_UNAVAILABLE", "maps repos not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	goals, err := s.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	nodes, edges, err := s.listLearnerGraph(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}

	paint := s.learnerGrowthPaint(ctx, tenantID, gcid)
	mastered := s.masteredConceptKeys(ctx, tenantID, gcid)
	ancestors := s.learnerLineageKeys(ctx, tenantID, gcid)
	concepts := derefConcepts(nodes)
	edgeVals := derefEdges(edges)
	byID := indexLiveConceptsByID(nodes)

	// C4: the cooling counts behind the home's defend card. Both learner-wide
	// reads happen ONCE here and are shared across every card, rather than a
	// retention round trip per climbing node per map.
	cool, coolingPartial := s.newCoolingCalc(ctx, tenantID, gcid)

	items := make([]mapCardDTO, 0, len(goals))
	for _, g := range goals {
		if g == nil {
			continue
		}
		card := buildMapCard(*g, concepts, edgeVals, byID, paint, mastered, ancestors, cool)
		if name := s.attachedCompanionName(ctx, tenantID, gcid, g); name != "" {
			card.AttachedCompanionName = name
		}
		items = append(items, card)
	}
	extWriteJSON(w, http.StatusOK, mapsListResp{Items: items, CoolingPartial: coolingPartial})
}

// handleMeMapByID — GET /v1/me/maps/{goalId}/graph. Returns the sub-tree under
// the goal's root concept (ADR-214 D1), filtered from the learner's shared graph
// and painted with the A1 overlay. A rootless goal yields an empty graph (a map
// still being built); a missing/cross-learner goal is 404 (never leak).
func (s *ExtServer) handleMeMapByID(w http.ResponseWriter, r *http.Request) {
	if s.Goals == nil || s.Concepts == nil || s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "MAPS_UNAVAILABLE", "maps repos not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	goalID, ok := parseMapGraphPath(r.URL.Path)
	if !ok {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	// Not found OR cross-learner/tenant leak → 404 (never reveal another's map).
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "MAP_NOT_FOUND", "")
		return
	}

	resp := mapGraphResp{
		GoalID:              g.GoalID,
		RootConceptID:       g.RootConceptID,
		AttachedCompanionID: g.AttachedCompanionID,
		PersonalCompletedAt: g.PersonalCompletedAt,
	}
	if name := s.attachedCompanionName(ctx, tenantID, gcid, g); name != "" {
		resp.AttachedCompanionName = &name
	}
	// A rootless goal has no sub-tree yet — an honest empty map (stable wire shape).
	if g.RootConceptID == nil || strings.TrimSpace(*g.RootConceptID) == "" {
		resp.conceptGraphResp = conceptGraphResp{Concepts: []conceptNodeDTO{}, Edges: []conceptEdgeDTO{}}
		extWriteJSON(w, http.StatusOK, resp)
		return
	}

	nodes, edges, err := s.listLearnerGraph(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	set := conceptgraph.SubtreeConceptIDs(derefConcepts(nodes), derefEdges(edges), *g.RootConceptID)
	byID := indexLiveConceptsByID(nodes)
	if root := byID[*g.RootConceptID]; root != nil {
		resp.Title = root.Title
	}

	paint := s.learnerGrowthPaint(ctx, tenantID, gcid)
	mastered := s.masteredConceptKeys(ctx, tenantID, gcid)
	ancestors := s.learnerLineageKeys(ctx, tenantID, gcid)
	resp.conceptGraphResp = toConceptGraphRespPainted(
		filterConceptsInSet(nodes, set), filterEdgesInSet(edges, set), paint, mastered, ancestors)
	// C4 Roads layer (plan §8): the in-domain intersection of each concept's
	// AtomRefs with the learner's COURSE-BOUND paths. Painted after the graph is
	// built so a failure degrades the overlay alone: the map still renders, and
	// roadsPartial says the layer was not read rather than serving an empty
	// roads[] as the fact that the concept is on no certificate path.
	resp.RoadsPartial = s.attachRoads(ctx, tenantID, gcid, resp.Concepts)
	// ADR-227 D15/D16 campaign overlay (WS-C7): fail-soft — a nil block omits
	// the "campaign" key without touching the (already-built) painted graph.
	resp.Campaign = s.buildMapCampaignBlock(ctx, tenantID, gcid, g, set, byID)
	extWriteJSON(w, http.StatusOK, resp)
}

// buildMapCampaignBlock computes the ADR-227 campaign overlay for a rooted goal
// (the caller only reaches here for a rooted goal). Fail-soft: an unwired or
// erroring ladder / retention read logs loudly and returns nil (the block is
// omitted, the map still renders). Nodes carry per-concept ladder state; a
// subtree node with no ladder row is unstarted (absent from the map). The
// cooling cue reads the CAMPAIGN's own concept-key retention — never
// growthEdge.isDue (the dormant KG due-join).
func (s *ExtServer) buildMapCampaignBlock(ctx context.Context, tenantID, gcid string, g *goal.Goal, subtree map[string]bool, byID map[string]*conceptgraph.ConceptNode) *mapCampaignDTO {
	if s.CampaignProgress == nil {
		return nil // campaign lane not wired — omit (fail-soft, no 5xx)
	}
	progresses, err := s.CampaignProgress.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		log.Printf("consumption: map campaign overlay OMITTED — ladder read failed (tenant=%s gcid=%s goal=%s): %v",
			tenantID, gcid, g.GoalID, err)
		return nil
	}
	var retention topic_retention.Repository
	if s.CampaignDose != nil {
		retention = s.CampaignDose.Retention
	}
	now := time.Now().UTC()

	won := map[string]bool{}
	nodes := map[string]mapCampaignNodeDTO{}
	for _, p := range progresses {
		if p == nil || !subtree[p.ConceptID] {
			continue
		}
		dto := mapCampaignNodeDTO{
			RungsCleared:       p.RungsCleared,
			CurrentRungCorrect: p.CurrentRungCorrect,
			WonAt:              p.WonAt,
			AdvancedToday:      p.AdvancedOn(now),
		}
		if p.Won() {
			won[p.ConceptID] = true
		} else if p.RungsCleared > 0 {
			// Cooling (D15): a climbing node whose retention has decayed past
			// the refresh floor (or has no row yet — the conservative refresher
			// default, mirroring campaign.DecideServe).
			cooling, err := campaignNodeCooling(ctx, retention, tenantID, gcid, byID[p.ConceptID], now)
			if err != nil {
				log.Printf("consumption: map campaign overlay OMITTED — cooling read failed (tenant=%s gcid=%s concept=%s): %v",
					tenantID, gcid, p.ConceptID, err)
				return nil
			}
			dto.Cooling = cooling
		}
		nodes[p.ConceptID] = dto
	}

	frontier := campaign.ComputeFrontier(subtree, won)
	return &mapCampaignDTO{
		FocusConceptID:   g.FocusConceptID,
		CampaignSealedAt: g.CampaignSealedAt,
		FrontierTotal:    frontier.Total,
		FrontierWon:      frontier.Won,
		CanSeal:          frontier.Total > 0 && len(frontier.Remaining) == 0,
		Nodes:            nodes,
	}
}

// campaignNodeCooling reports whether an unwon climbing node is refresh-gated.
// A missing concept-key or an unwired retention repo reads as "row absent" →
// cooling (the conservative refresher default). A retention Get error is
// surfaced so the caller omits the whole block (fail-soft), never a fabricated
// cue.
func campaignNodeCooling(ctx context.Context, retention topic_retention.Repository, tenantID, gcid string, node *conceptgraph.ConceptNode, now time.Time) (bool, error) {
	if node == nil || strings.TrimSpace(node.ConceptKey) == "" || retention == nil {
		return true, nil
	}
	score, err := retention.Get(ctx, tenantID, gcid, node.ConceptKey)
	if err != nil {
		return false, err
	}
	return score == nil || score.RetentionAt(now) < campaign.RefreshThreshold, nil
}

// attachedCompanionName resolves the goal-bound companion's learner-safe display
// name for the map read (CHO-2109). Fail-soft by contract: an unwired repo, an
// unbound goal, a read error, a tenant/owner mismatch or a blank name all
// return "" — the map read never 5xxs on the name path and never surfaces
// another owner's companion.
func (s *ExtServer) attachedCompanionName(ctx context.Context, tenantID, gcid string, g *goal.Goal) string {
	if s.CompanionInstances == nil || g == nil || g.AttachedCompanionID == nil || strings.TrimSpace(*g.AttachedCompanionID) == "" {
		return ""
	}
	inst, err := s.CompanionInstances.Get(ctx, *g.AttachedCompanionID)
	if err != nil {
		log.Printf("consumption: map read companion-name OMITTED — instance read failed (tenant=%s gcid=%s goal=%s): %v",
			tenantID, gcid, g.GoalID, err)
		return ""
	}
	if inst == nil || inst.TenantID != tenantID || inst.OwnerGCID != gcid {
		return ""
	}
	return strings.TrimSpace(inst.Name)
}

// listLearnerGraph loads the learner's live concepts + edges (the shared flat
// space a map is a rooted view of).
func (s *ExtServer) listLearnerGraph(ctx context.Context, tenantID, gcid string) ([]*conceptgraph.ConceptNode, []*conceptgraph.Edge, error) {
	nodes, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		return nil, nil, err
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

// buildMapCard projects one Goal into its Atlas card, computing the counts over
// the sub-tree under its root. A rootless goal is an empty map (0 counts, no
// title). Shaky = an ACTIVE Growth Edge paints the concept's STORED key or a
// lineage ancestor's (WS-C6, addendum #7 — never the live title); mastered = a
// grown edge resolved through the same key lineage.
func buildMapCard(g goal.Goal, concepts []conceptgraph.ConceptNode, edges []conceptgraph.Edge, byID map[string]*conceptgraph.ConceptNode, paint *growthPaint, mastered map[string]bool, ancestorKeys map[string][]string, cool *coolingCalc) mapCardDTO {
	card := mapCardDTO{
		GoalID:              g.GoalID,
		NorthStarNote:       g.NorthStarNote,
		RootConceptID:       g.RootConceptID,
		Kind:                string(g.Kind),
		Status:              string(g.Status),
		AttachedCompanionID: g.AttachedCompanionID,
		CreatedAt:           g.CreatedAt,
		UpdatedAt:           g.UpdatedAt,
	}
	if g.RootConceptID == nil || strings.TrimSpace(*g.RootConceptID) == "" {
		return card
	}
	if root := byID[*g.RootConceptID]; root != nil {
		card.Title = root.Title
	}
	set := conceptgraph.SubtreeConceptIDs(concepts, edges, *g.RootConceptID)
	shaky, masteredCount := 0, 0
	for id := range set {
		c := byID[id]
		if c == nil {
			continue
		}
		labels := conceptPaintLabels(c, ancestorKeys)
		// Shaky = an ACTIVE evidenced Growth Edge paints the concept — by the
		// ADR-238 id-join (edge.TargetConceptID == c.ConceptID, HIGHEST precedence)
		// or the stored-key / lineage-ancestor slug fallback (WS-C6, addendum #7 —
		// never the live title) — OR the concept is a ceremony remediate
		// learning-edge (CHO-2038 — complements the overlay).
		if paint.matchFEByNode(c.ConceptID, labels...) != nil || c.Intent == conceptgraph.IntentRemediate {
			shaky++
		}
		if masteredByLabels(labels, mastered) {
			masteredCount++
		}
	}
	card.ConceptCount = len(set)
	card.ShakyCount = shaky
	card.MasteredCount = masteredCount
	// C4: the defend signal, over the sub-tree already computed above. A retired
	// map is the archive and is never defended, so it carries no count even
	// though its ladder rows are still there.
	if cool != nil && defensibleGoal(g) {
		card.CoolingCount, card.CoolingHexLabel = cool.forSubtree(set, byID)
	}
	return card
}

// indexLiveConceptsByID indexes live (non-deleted, non-nil) concepts by id.
func indexLiveConceptsByID(nodes []*conceptgraph.ConceptNode) map[string]*conceptgraph.ConceptNode {
	out := make(map[string]*conceptgraph.ConceptNode, len(nodes))
	for _, n := range nodes {
		if n != nil && n.DeletedAt == nil {
			out[n.ConceptID] = n
		}
	}
	return out
}

// filterConceptsInSet keeps the live concept pointers whose id is in set.
func filterConceptsInSet(nodes []*conceptgraph.ConceptNode, set map[string]bool) []*conceptgraph.ConceptNode {
	out := make([]*conceptgraph.ConceptNode, 0, len(set))
	for _, n := range nodes {
		if n != nil && n.DeletedAt == nil && set[n.ConceptID] {
			out = append(out, n)
		}
	}
	return out
}

// filterEdgesInSet keeps the live edges whose BOTH endpoints are in set (intra-
// map edges of either class; cross-tree lateral junctions are dropped from the
// map view).
func filterEdgesInSet(edges []*conceptgraph.Edge, set map[string]bool) []*conceptgraph.Edge {
	out := make([]*conceptgraph.Edge, 0, len(edges))
	for _, e := range edges {
		if e != nil && e.DeletedAt == nil && set[e.SourceConceptID] && set[e.TargetConceptID] {
			out = append(out, e)
		}
	}
	return out
}

// parseMapGraphPath extracts {goalId} from /v1/me/maps/{goalId}/graph. Any other
// suffix under the by-id subtree is unrecognised (ok=false → 404).
func parseMapGraphPath(path string) (goalID string, ok bool) {
	rest := strings.Trim(strings.TrimPrefix(path, mapsByIDPrefix), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || parts[1] != "graph" {
		return "", false
	}
	return parts[0], true
}
