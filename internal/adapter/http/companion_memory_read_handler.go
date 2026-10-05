// companion_memory_read_handler.go — ADR-215 WS-1 tier-(a) learner-facing
// Companion memory + context READ API (CHO-1997).
//
//	GET /v1/me/companions/{id}/memory
//
// Returns the designated Companion's recent episodic recalls (recency-ordered) +
// its persona/rules/focus + visible KG-neighbour concepts, with atom/KG UUIDs
// resolved to learner-readable titles + topic paths (the FE Citation{atomId,
// atomTitle, topicNodePath} shape). Learner-appropriate depth (ADR-215 D5): no
// raw vectors / cosine distances / prompt text / model internals. An empty
// recall set is returned honestly as hasMemory=false ("no memory yet"), never a
// fabricated placeholder.
//
// This is a STANDALONE http.Handler (not a Server method) built by
// cmd/server/companion_memory_read_wiring.go and registered on the router.go mux
// as the more-specific `GET /v1/me/companions/{id}/memory` pattern (which wins
// over the existing `/v1/me/companions/` subtree). It composes:
//   - companionmind.MemoryReader   (recency read over companion_memory_recall)
//   - companion.InstanceRepository (persona / rules / focus + owner-leak guard)
//   - conceptgraph.ConceptNodeRepository (visible KG neighbours — fail-soft)
//   - atom_index.Repo             (atom UUID → title/topic — fail-soft)
//
// Learner-scoped: the GCID is the validated session subject (headers via
// extRequireContext), NEVER a body/query param; a learner reads ONLY their own
// Companion (RLS + the by-owner leak guard enforce isolation).
package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	atomindex "github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const companionMemoryPathPrefix = "/v1/me/companions/"
const companionMemoryPathSuffix = "/memory"

// CompanionMemoryReadHandler serves GET /v1/me/companions/{id}/memory.
//
// Memories + Instances are ESSENTIAL (nil ⇒ 503). Concepts + Atoms are optional
// enrichment (nil or read-error ⇒ empty neighbours, never a failed request).
type CompanionMemoryReadHandler struct {
	Memories  companionmind.MemoryReader
	Instances companion.InstanceRepository
	Concepts  conceptgraph.ConceptNodeRepository
	Atoms     atomindex.Repo

	// ADR-214 goal scoping for "What it can see". A ConceptNode carries no goal
	// column (the graph is one flat learner-owned space; a map is a ROOTED VIEW
	// of it), so scoping is the same subtree walk every other goal-scoped reader
	// in this service performs. Both nil ⇒ the handler can only serve unscoped,
	// which is legal ONLY when no goal was asked for.
	Goals        GoalReader
	ConceptEdges ConceptEdgeLister
}

// ConceptEdgeLister is the narrow edge read the goal-subtree walk needs (mirrors
// GoalReader — the handler has no business with the full EdgeRepository).
type ConceptEdgeLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.Edge, error)
}

// NewCompanionMemoryReadHandler constructs the handler from its collaborators.
func NewCompanionMemoryReadHandler(
	mem companionmind.MemoryReader,
	inst companion.InstanceRepository,
	concepts conceptgraph.ConceptNodeRepository,
	atoms atomindex.Repo,
) *CompanionMemoryReadHandler {
	return &CompanionMemoryReadHandler{Memories: mem, Instances: inst, Concepts: concepts, Atoms: atoms}
}

var _ http.Handler = (*CompanionMemoryReadHandler)(nil)

// wire DTOs — camelCase; mirror the FE Citation{atomId, atomTitle, topicNodePath}.

// provenanceCitationDTO is one DURABLE source behind a grounded note (ADR-231 D4).
//
// ⚠ There is NO url field, deliberately. The live citation's uri is a Google
// grounding-api-redirect that expires in ~30 days; a note re-opened after that
// would serve a dead link that still looks like a working source. It was never
// persisted, and it must never be invented here.
type provenanceCitationDTO struct {
	Domain  string `json:"domain,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// sourceMetadataDTO is a grounded note's persisted provenance (CHO-2179, mig 0094).
type sourceMetadataDTO struct {
	WebSearchQueries []string                `json:"webSearchQueries,omitempty"`
	Citations        []provenanceCitationDTO `json:"citations,omitempty"`
}

type episodicMemoryDTO struct {
	ID         string    `json:"id"`
	MemoryType string    `json:"memoryType"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"createdAt"`
	// SourceMetadata is present IF AND ONLY IF the note actually has provenance.
	// An absent key is the honest "we did not record this" — which is the state of
	// every note written before mig 0094. Emitting an empty object instead would
	// render as an attribution block with nothing in it, implying a web search
	// that never happened.
	SourceMetadata *sourceMetadataDTO `json:"sourceMetadata,omitempty"`
}

type citationDTO struct {
	AtomID        string `json:"atomId"`
	AtomTitle     string `json:"atomTitle"`
	TopicNodePath string `json:"topicNodePath"`
}

type neighborConceptDTO struct {
	ConceptID    string        `json:"conceptId"`
	ConceptTitle string        `json:"conceptTitle"`
	Citations    []citationDTO `json:"citations"`
}

type companionMemoryViewResp struct {
	CompanionID      string               `json:"companionId"`
	Name             string               `json:"name"`
	Focus            string               `json:"focus"`
	Persona          string               `json:"persona"`
	Rules            map[string]string    `json:"rules"`
	EvolutionTier    string               `json:"evolutionTier"`
	Skills           []string             `json:"skills"`
	HasMemory        bool                 `json:"hasMemory"`
	Memories         []episodicMemoryDTO  `json:"memories"`
	VisibleNeighbors []neighborConceptDTO `json:"visibleNeighbors"`
	// ResearchNotes — the Companion's grounded notes, each with the provenance
	// behind it (CHO-2185). Always an array, never null.
	ResearchNotes []episodicMemoryDTO `json:"researchNotes"`
}

// ServeHTTP handles GET /v1/me/companions/{id}/memory.
func (h *CompanionMemoryReadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Essential collaborators absent (no pgx pool at boot) → 503, never a panic.
	if h == nil || h.Memories == nil || h.Instances == nil {
		extWriteError(w, http.StatusServiceUnavailable, "COMPANION_MEMORY_UNAVAILABLE", "companion memory read not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	id := companionIDFromMemoryPath(r)
	if id == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "companion_id required")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	// Load the Companion (persona/rules/focus) + enforce the owner-leak guard: a
	// learner only reads their OWN Companion. Not-found, cross-owner, or
	// cross-tenant all collapse to 404 (never reveal another learner's Companion).
	inst, err := h.Instances.Get(ctx, id)
	if err != nil {
		if errors.Is(err, companion.ErrInstanceNotFound) {
			extWriteError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
			return
		}
		extWriteError(w, http.StatusInternalServerError, "COMPANION_READ_FAILED", err.Error())
		return
	}
	if inst == nil || inst.OwnerGCID != gcid || inst.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
		return
	}

	// Recent episodic recalls (recency-ordered, RLS-scoped). Load-bearing — a
	// read failure here is a real error (fail-loud).
	memories, err := h.Memories.RecentMemories(ctx, companionmind.Query{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		CompanionID: id,
		MemoryLimit: memoryLimitFromQuery(r),
	})
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "MEMORY_READ_FAILED", err.Error())
		return
	}

	// Grounded research notes + their durable provenance (CHO-2185), read as their
	// OWN memory_type-filtered list. Sieving them out of `memories` above would not
	// do: that read is a shared recency window, so a learner with a lot of chat
	// turns would push their research notes out of it and watch them silently
	// disappear. Also load-bearing — this is the provenance the story exists to
	// deliver, so a failure to read it is an error, not a quietly empty panel.
	researchNotes, err := h.Memories.RecentMemories(ctx, companionmind.Query{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		CompanionID: id,
		MemoryLimit: memoryLimitFromQuery(r),
		MemoryTypes: []string{companionmind.MemoryTypeResearch},
	})
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "MEMORY_READ_FAILED", err.Error())
		return
	}

	// Visible KG neighbours + resolved atom citations — enrichment, fail-soft on a
	// READ error, but fail-CLOSED on a scope it was asked for and cannot honour.
	neighbors, scopeErr := h.visibleNeighbors(ctx, tenantID, gcid, goalIDFromQuery(r))
	if scopeErr != nil {
		extWriteError(w, http.StatusServiceUnavailable, "GOAL_SCOPE_UNAVAILABLE", scopeErr.Error())
		return
	}

	view := companionmind.AssembleView(instanceContext(inst), memories, neighbors, researchNotes)
	extWriteJSON(w, http.StatusOK, toMemoryViewResp(view))
}

// companionIDFromMemoryPath extracts {id} from /v1/me/companions/{id}/memory. It
// prefers the mux path value (Go 1.22 wildcard) and falls back to trimming the
// literal prefix + suffix so the handler is unit-testable without the mux. A
// deeper/other subpath yields "" (rejected upstream).
func companionIDFromMemoryPath(r *http.Request) string {
	if v := strings.TrimSpace(r.PathValue("id")); v != "" {
		return v
	}
	p := strings.TrimPrefix(r.URL.Path, companionMemoryPathPrefix)
	p = strings.TrimSuffix(p, companionMemoryPathSuffix)
	p = strings.Trim(p, "/")
	if p == "" || strings.Contains(p, "/") {
		return ""
	}
	return p
}

// memoryLimitFromQuery parses an optional ?limit=N (the adapter clamps the
// range); a missing/invalid value yields 0 (⇒ the domain default).
func memoryLimitFromQuery(r *http.Request) int {
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return 0
}

// instanceContext lifts the persona/rules/focus fields off the Companion
// aggregate into the read model's decoupled InstanceContext.
func instanceContext(inst *companion.Instance) companionmind.InstanceContext {
	return companionmind.InstanceContext{
		CompanionID:   inst.CompanionID,
		Name:          inst.Name,
		Focus:         inst.Specialization,
		Persona:       inst.PersonaSummary,
		Rules:         inst.ConfiguredRules,
		EvolutionTier: string(inst.EvolutionTier),
		Skills:        inst.SkillGrants,
	}
}

// visibleNeighbors returns the learner's visible KG concept neighbours with
// their atom refs resolved to citations. FAIL-SOFT (mirrors the goals + KG
// growth overlay): a nil repo OR a read error yields NO neighbours and NEVER
// breaks the view — the neighbours are enrichment, not load-bearing data.
//
// Two different registers, deliberately:
//   - a READ failure degrades to empty (enrichment, fail-soft);
//   - a SCOPE that was asked for and cannot be honoured returns an error so the
//     caller 503s (fail-CLOSED). Serving the unscoped set there would silently
//     leak every one of the learner's other maps into this one, which is the
//     exact defect this scoping closes.
func (h *CompanionMemoryReadHandler) visibleNeighbors(
	ctx context.Context, tenantID, gcid, goalID string,
) ([]companionmind.NeighborConcept, error) {
	if h.Concepts == nil {
		return nil, nil
	}
	nodes, err := h.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		// enrichment read failed — degrade to empty, do not fail the request.
		return nil, nil
	}
	inGoal, err := h.goalSubtree(ctx, tenantID, gcid, goalID, nodes)
	if err != nil {
		return nil, err
	}
	out := make([]companionmind.NeighborConcept, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		// Filter BEFORE the cap. Capping first would fill the panel with the
		// first MaxNeighbors nodes of the flat graph and then filter them away,
		// so an on-map concept could be evicted by an off-map one.
		if inGoal != nil && !inGoal[n.ConceptID] {
			continue
		}
		if len(out) >= companionmind.MaxNeighbors {
			break
		}
		out = append(out, companionmind.NeighborConcept{
			ConceptID:    n.ConceptID,
			ConceptTitle: n.Title,
			Citations:    h.resolveAtomCitations(ctx, n.AtomRefs),
		})
	}
	return out, nil
}

// goalSubtree resolves the goal's concept-id set, or nil when no goal was asked
// for (⇒ the whole learner graph, the pre-ADR-214 behaviour). An unrooted or
// foreign goal also yields nil: there is no subtree to scope to, and the goals
// door already owns the ownership 404, so this read does not re-litigate it.
func (h *CompanionMemoryReadHandler) goalSubtree(
	ctx context.Context, tenantID, gcid, goalID string, nodes []*conceptgraph.ConceptNode,
) (map[string]bool, error) {
	if strings.TrimSpace(goalID) == "" {
		return nil, nil
	}
	if h.Goals == nil || h.ConceptEdges == nil {
		return nil, errors.New("goal-scope ports not wired; refusing to serve an unscoped concept list")
	}
	g, err := h.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		return nil, err
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID ||
		g.RootConceptID == nil || strings.TrimSpace(*g.RootConceptID) == "" {
		return nil, nil
	}
	edges, err := h.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	return conceptgraph.SubtreeConceptIDs(derefConcepts(nodes), derefEdges(edges), *g.RootConceptID), nil
}

// goalIDFromQuery reads the OPTIONAL ?goal_id= scope. Absent ⇒ the whole learner
// graph (unchanged behaviour for any non-map caller).
func goalIDFromQuery(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("goal_id"))
}

// resolveAtomCitations resolves each atom UUID to a learner-readable citation
// via the local atom_index projection. An unresolved atom (absent from the
// index, ErrNotFound, or any read error) keeps a BLANK title — shown honestly
// as "unknown", NEVER fabricated (ADR-215 D5). Cross-DB queries are forbidden,
// so unresolved is a legitimate steady state for atoms authored in other tenants
// or not yet projected.
func (h *CompanionMemoryReadHandler) resolveAtomCitations(ctx context.Context, atomRefs []string) []companionmind.Citation {
	cits := make([]companionmind.Citation, 0, len(atomRefs))
	for _, atomID := range atomRefs {
		atomID = strings.TrimSpace(atomID)
		if atomID == "" {
			continue
		}
		if len(cits) >= companionmind.MaxCitationsPerNeighbor {
			break
		}
		title := ""
		var tags []string
		if h.Atoms != nil {
			if a, err := h.Atoms.Get(ctx, atomID); err == nil && a != nil {
				title = a.Title
				tags = a.TopicTags
			}
			// err (incl. atom_index.ErrNotFound) → leave title blank (unknown).
		}
		cits = append(cits, companionmind.NewCitation(atomID, title, tags))
	}
	return cits
}

// toMemoryDTO maps one recalled memory onto the wire, provenance included.
//
// The keys are already camelCase, which matters: the gateway's CompanionBridge
// runs SnakeToCamelJSON over this whole body, and that conversion is a no-op on
// a key with no underscore. Emitting the persisted snake_case JSONB verbatim
// would instead have the gateway silently rewrite it.
func toMemoryDTO(m companionmind.EpisodicMemory) episodicMemoryDTO {
	return episodicMemoryDTO{
		ID:             m.ID,
		MemoryType:     m.MemoryType,
		Content:        m.Content,
		CreatedAt:      m.CreatedAt,
		SourceMetadata: toSourceMetadataDTO(m.Provenance),
	}
}

// toSourceMetadataDTO emits provenance IF AND ONLY IF there is any — nil for a
// pre-0094 note and, defensively, for an empty husk. One wire shape for "nothing
// to attribute" means the FE has exactly one branch for it and can never render
// an empty attribution block.
func toSourceMetadataDTO(p *companionmind.Provenance) *sourceMetadataDTO {
	if p.IsEmpty() {
		return nil
	}
	cits := make([]provenanceCitationDTO, 0, len(p.Citations))
	for _, c := range p.Citations {
		cits = append(cits, provenanceCitationDTO{
			Domain:  c.Domain,
			Title:   c.Title,
			Snippet: c.Snippet,
		})
	}
	return &sourceMetadataDTO{WebSearchQueries: p.WebSearchQueries, Citations: cits}
}

// toMemoryViewResp maps the domain view onto the camelCase wire shape.
func toMemoryViewResp(v companionmind.MemoryView) companionMemoryViewResp {
	mems := make([]episodicMemoryDTO, 0, len(v.Memories))
	for _, m := range v.Memories {
		mems = append(mems, toMemoryDTO(m))
	}
	notes := make([]episodicMemoryDTO, 0, len(v.ResearchNotes))
	for _, m := range v.ResearchNotes {
		notes = append(notes, toMemoryDTO(m))
	}
	nbrs := make([]neighborConceptDTO, 0, len(v.VisibleNeighbors))
	for _, n := range v.VisibleNeighbors {
		cits := make([]citationDTO, 0, len(n.Citations))
		for _, c := range n.Citations {
			cits = append(cits, citationDTO{AtomID: c.AtomID, AtomTitle: c.AtomTitle, TopicNodePath: c.TopicNodePath})
		}
		nbrs = append(nbrs, neighborConceptDTO{ConceptID: n.ConceptID, ConceptTitle: n.ConceptTitle, Citations: cits})
	}
	return companionMemoryViewResp{
		CompanionID:      v.CompanionID,
		Name:             v.Name,
		Focus:            v.Focus,
		Persona:          v.Persona,
		Rules:            v.Rules,
		EvolutionTier:    v.EvolutionTier,
		Skills:           v.Skills,
		HasMemory:        v.HasMemory,
		Memories:         mems,
		VisibleNeighbors: nbrs,
		ResearchNotes:    notes,
	}
}
