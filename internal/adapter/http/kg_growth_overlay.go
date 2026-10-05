// kg_growth_overlay.go — W5: Growth-Edge read-overlay for the per-user KG
// paint (Epic-1b) + the orthogonal due-⏰ terrain (CHO-1950, ADR-204 P2). The
// cluster-card + hexagon-fog + canvas read handlers annotate nodes whose labels
// slug-match one of the learner's ACTIVE Growth Edges with {edge id, concept_key,
// strength} so A+ can paint shaky frontiers, and — orthogonally — flag the edge
// "due for review" when its resolved topic's Ebbinghaus retention has decayed
// below topic_retention.DueThreshold. Strictly read-time: NO KG node is mutated,
// and an overlay failure NEVER breaks the KG read (fail-soft — the paint is
// enrichment, not data).
package http

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// kgOverlayPageSize bounds the per-read edge fetch. Sorted strength-desc, so
// truncation drops the least shaky edges — exactly the ones that paint last.
const kgOverlayPageSize = 100

// kgRetentionIndexLimit caps the learner's retention scores folded into the
// due-⏰ index. 0 = unbounded: the rows are scoped to ONE (tenant, gcid) by RLS
// + an explicit gcid predicate, so the set is inherently small, and an
// unbounded read guarantees every painted edge's topic resolves (a recency cap
// could miss the exact topic behind a matched edge).
const kgRetentionIndexLimit = 0

// kgGrowthEdgeFEDTO is the camelCase overlay shape on cluster cards + the canvas
// layout (the FE `{data}` BFF surface).
type kgGrowthEdgeFEDTO struct {
	EdgeID     string  `json:"edgeId"`
	ConceptKey string  `json:"conceptKey"`
	Strength   float64 `json:"strength"`
	// IsDue is the orthogonal due-⏰ terrain flag (ADR-204 P2): true when the
	// edge's resolved topic has decayed below topic_retention.DueThreshold.
	// Independent of Strength — a concept can be weak AND due. Omitted when not
	// due (the FE treats absent as not-due).
	IsDue bool `json:"isDue,omitempty"`
	// RetentionScore is the edge topic's Ebbinghaus retention at read time
	// (0..1), present only when the edge resolves to a scored topic. Null/absent
	// = no retention data (unresolved topic) → no due paint.
	RetentionScore *float64 `json:"retentionScore,omitempty"`
}

// kgGrowthEdgeDTO is the snake_case overlay shape on the fog hexagon (matches
// the legacy fog wire shape).
type kgGrowthEdgeDTO struct {
	EdgeID         string   `json:"edge_id"`
	ConceptKey     string   `json:"concept_key"`
	Strength       float64  `json:"strength"`
	IsDue          bool     `json:"is_due,omitempty"`
	RetentionScore *float64 `json:"retention_score,omitempty"`
}

// growthPaint bundles the per-request Growth-Edge match index with the learner's
// topic-retention scores so a matched edge can be painted with both its
// {strength} colour band and the orthogonal {isDue} ⏰ in one place. Built once
// per KG read (learnerGrowthPaint); both lookups are fail-soft (a nil/err lookup
// simply yields no paint). now is stamped once so a whole read paints against a
// single wall-clock.
type growthPaint struct {
	edges *lw.OverlayIndex
	// retention is keyed by each row's raw TopicID value — a TopicNode UUID
	// for session-resolved rows, or a concept_key slug for the rows campaign
	// practice grading plants (WS-C7). dueFor joins on both vocabularies.
	retention map[string]*topic_retention.TopicScore
	now       time.Time
}

// learnerGrowthOverlay loads the learner's active edges into an OverlayIndex.
// Fail-soft: nil repo or a read error yields an empty index (no paint).
func (s *ExtServer) learnerGrowthOverlay(ctx context.Context, tenantID, gcid string) *lw.OverlayIndex {
	if s.LearnerWeakness == nil {
		return lw.BuildOverlayIndex(nil)
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)
	res, err := s.LearnerWeakness.List(ctx, lw.ListQuery{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Sort:        lw.SortStrengthDesc,
		PageSize:    kgOverlayPageSize,
	})
	if err != nil {
		return lw.BuildOverlayIndex(nil)
	}
	return lw.BuildOverlayIndex(res.Items)
}

// learnerRetentionIndex loads the learner's live topic-retention scores into a
// topic_id-keyed map for the due-⏰ join. Fail-soft: a nil repo or read error
// yields an empty map (no due paint). Blank/unresolved topic ids are skipped.
func (s *ExtServer) learnerRetentionIndex(ctx context.Context, tenantID, gcid string) map[string]*topic_retention.TopicScore {
	if s.Retention == nil {
		return nil
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)
	scores, err := s.Retention.ListByLearner(ctx, tenantID, gcid, kgRetentionIndexLimit)
	if err != nil {
		return nil
	}
	idx := make(map[string]*topic_retention.TopicScore, len(scores))
	for _, sc := range scores {
		if sc != nil && sc.TopicID != "" {
			idx[sc.TopicID] = sc
		}
	}
	return idx
}

// learnerGrowthPaint composes the active-edge overlay + the retention index + a
// single read-time `now` into the paint bundle the KG read handlers thread
// through their DTO conversions.
func (s *ExtServer) learnerGrowthPaint(ctx context.Context, tenantID, gcid string) *growthPaint {
	return &growthPaint{
		edges:     s.learnerGrowthOverlay(ctx, tenantID, gcid),
		retention: s.learnerRetentionIndex(ctx, tenantID, gcid),
		now:       time.Now().UTC(),
	}
}

// Empty reports whether the bundle holds no paintable edges. Nil-safe.
func (p *growthPaint) Empty() bool { return p == nil || p.edges.Empty() }

// dueFor resolves the matched edge's topic retention. The resolved TopicID join
// keeps FIRST precedence; when TopicID is unresolved ("") or its lookup misses,
// the join falls back to the edge's ConceptKey — campaign practice grading
// (WS-C7, campaign.Grader.RecordAnswer) plants topic_retention rows keyed by
// the node's concept_key, the same normalised-slug vocabulary as
// LearnerWeakness.ConceptKey (CHO-2108). ok=false when neither join hits
// (graceful partial coverage — no due paint, never an error). When ok, due =
// retention decayed below DueThreshold and retention is the read-time R(t)
// snapshot for the FE.
func (p *growthPaint) dueFor(e *lw.LearnerWeakness) (due bool, retention float64, ok bool) {
	if p == nil || e == nil || p.retention == nil {
		return false, 0, false
	}
	sc := p.retention[e.TopicID] // index skips blank keys, so "" never hits
	if sc == nil {
		sc = p.retention[e.ConceptKey]
	}
	if sc == nil {
		return false, 0, false
	}
	return sc.IsDueAt(p.now), sc.RetentionAt(p.now), true
}

// enrichFE builds the painted camelCase DTO for one matched edge (strength band
// + orthogonal due ⏰), or nil when e is nil. Shared by the slug-join (matchFE)
// and the id-join (matchFEByNode) so both vocabularies produce an IDENTICAL DTO.
func (p *growthPaint) enrichFE(e *lw.LearnerWeakness) *kgGrowthEdgeFEDTO {
	dto := toKGGrowthEdgeFEDTO(e)
	if dto != nil {
		if due, ret, ok := p.dueFor(e); ok {
			dto.IsDue = due
			dto.RetentionScore = &ret
		}
	}
	return dto
}

// matchFE returns the painted camelCase DTO for the strongest edge matching any
// label (strength band + orthogonal due ⏰), or nil when no edge matches.
func (p *growthPaint) matchFE(labels ...string) *kgGrowthEdgeFEDTO {
	if p == nil {
		return nil
	}
	return p.enrichFE(p.edges.Match(labels...))
}

// matchFEByNode paints a concept node's Growth Edge with the ADR-238 id-join at
// HIGHEST precedence: when conceptID resolves an edge by its
// TargetConceptID, that edge paints regardless of any slug — lighting the
// correct node through a rename / merge / pre-0098 concept_key drift. Falls back
// to the slug-join (matchFE) when conceptID is blank or resolves nothing. The
// enrichment is shared with matchFE, so id-join and slug-join yield an identical
// DTO. Nil-safe.
func (p *growthPaint) matchFEByNode(conceptID string, labels ...string) *kgGrowthEdgeFEDTO {
	if p == nil {
		return nil
	}
	if conceptID != "" {
		if e := p.edges.MatchConceptID(conceptID); e != nil {
			return p.enrichFE(e)
		}
	}
	return p.matchFE(labels...)
}

// matchFog mirrors matchFE for the snake_case fog wire shape.
func (p *growthPaint) matchFog(labels ...string) *kgGrowthEdgeDTO {
	if p == nil {
		return nil
	}
	e := p.edges.Match(labels...)
	dto := toKGGrowthEdgeDTO(e)
	if dto != nil {
		if due, ret, ok := p.dueFor(e); ok {
			dto.IsDue = due
			dto.RetentionScore = &ret
		}
	}
	return dto
}

func toKGGrowthEdgeFEDTO(e *lw.LearnerWeakness) *kgGrowthEdgeFEDTO {
	if e == nil {
		return nil
	}
	return &kgGrowthEdgeFEDTO{EdgeID: e.ID, ConceptKey: e.ConceptKey, Strength: e.Strength}
}

func toKGGrowthEdgeDTO(e *lw.LearnerWeakness) *kgGrowthEdgeDTO {
	if e == nil {
		return nil
	}
	return &kgGrowthEdgeDTO{EdgeID: e.ID, ConceptKey: e.ConceptKey, Strength: e.Strength}
}

// annotateFogOverlay paints the hexagon DTO: the focal atom by its atom_index
// topic tags, each neighbor by its LLM fog label — with the strength band + the
// orthogonal due-⏰. No mutation of the underlying HexagonNode — DTO-only.
func (s *ExtServer) annotateFogOverlay(ctx context.Context, dto *kgHexagonDTO, paint *growthPaint) {
	if paint.Empty() {
		return
	}
	if s.AtomIndex != nil {
		if atom, err := s.AtomIndex.Get(ctx, dto.FocalAtomID); err == nil && atom != nil {
			dto.FocalGrowthEdge = paint.matchFog(atom.TopicTags...)
		}
	}
	for i := range dto.Neighbors {
		dto.Neighbors[i].GrowthEdge = paint.matchFog(dto.Neighbors[i].FogLabel)
	}
}
