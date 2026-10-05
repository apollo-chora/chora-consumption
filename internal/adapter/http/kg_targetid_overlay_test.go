// kg_targetid_overlay_test.go — ADR-238 M-D3: the read-side id-join. The paint
// lights a concept node's Growth Edge by the edge's resolved on-map
// TargetConceptID (HIGHEST precedence), so a node stays painted even when the
// concept_key slug no longer matches the live node title (rename / merge /
// pre-0098 edges). matchFEByNode falls back to the slug-join (matchFE) when no
// id resolves, and produces an IDENTICAL enriched DTO either way.
package http

import (
	"testing"
	"time"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	tr "github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// kgTargetEdge builds a Growth Edge carrying an explicit resolved on-map
// TargetConceptID (ADR-238) plus a DELIBERATELY unrelated concept_key/label, so
// the id-join can be proven independent of the concept_key slug join.
func kgTargetEdge(t *testing.T, label, conceptKey, targetConceptID string, strength float64) lw.LearnerWeakness {
	t.Helper()
	w, err := lw.New(lw.UpsertInput{
		TenantID:        kgTenantID,
		LearnerGCID:     kgGCID,
		ConceptLabel:    label,
		ConceptKey:      conceptKey,
		Embedding:       []float32{0.1},
		Strength:        strength,
		TargetConceptID: targetConceptID,
		Source:          lw.SourceDerived,
		Now:             time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	return *w
}

// The id-join lights a node whose concept_key slug MISMATCHES its live title.
func TestMatchFEByNode_IdJoinIndependentOfSlug(t *testing.T) {
	const nodeID = "01980000-0000-7000-8000-0000000000c1"
	edges := []lw.LearnerWeakness{kgTargetEdge(t, "Renamed Old Concept", "renamed-old", nodeID, 0.8)}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: nil, now: dueNow}

	// Precondition: the slug-join alone MISSES the live node title (real mismatch).
	if dto := paint.matchFE("Live Node Title"); dto != nil {
		t.Fatalf("precondition: slug-join must miss the renamed edge, got %+v", dto)
	}
	// The id-join paints the node despite no label slug matching.
	dto := paint.matchFEByNode(nodeID, "Live Node Title")
	if dto == nil || dto.ConceptKey != "renamed-old" || dto.Strength != 0.8 {
		t.Fatalf("id-join must paint the edge by TargetConceptID, got %+v", dto)
	}
}

// When BOTH an id-join edge and a DIFFERENT slug-join edge exist for a node, the
// id-join wins — even when the slug edge is stronger.
func TestMatchFEByNode_IdJoinWinsOverSlugJoin(t *testing.T) {
	const nodeID = "01980000-0000-7000-8000-0000000000c2"
	idEdge := kgTargetEdge(t, "Id Target", "id-target", nodeID, 0.4)
	slugEdge := kgOverlayEdge(t, "Live Node Title", 0.9) // slug-matches the label below; no TargetConceptID
	paint := &growthPaint{edges: lw.BuildOverlayIndex([]lw.LearnerWeakness{idEdge, slugEdge}), retention: nil, now: dueNow}

	dto := paint.matchFEByNode(nodeID, "Live Node Title")
	if dto == nil || dto.ConceptKey != "id-target" || dto.Strength != 0.4 {
		t.Fatalf("id-join must win over the slug-join, got %+v", dto)
	}
	// Sanity: the slug-join alone still resolves the stronger sibling edge.
	if s := paint.matchFE("Live Node Title"); s == nil || s.ConceptKey != "live-node-title" || s.Strength != 0.9 {
		t.Fatalf("slug-join precondition = %+v", s)
	}
}

// No id match → fall back to the slug-join (blank id, unknown id, then no match).
func TestMatchFEByNode_FallsBackToSlugWhenNoIdMatch(t *testing.T) {
	edges := []lw.LearnerWeakness{kgOverlayEdge(t, "Fractions", 0.6)}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: nil, now: dueNow}

	if dto := paint.matchFEByNode("", "Fractions"); dto == nil || dto.ConceptKey != "fractions" {
		t.Fatalf("blank id must fall back to slug-join, got %+v", dto)
	}
	if dto := paint.matchFEByNode("no-such-node", "Fractions"); dto == nil || dto.ConceptKey != "fractions" {
		t.Fatalf("unknown id must fall back to slug-join, got %+v", dto)
	}
	if dto := paint.matchFEByNode("no-such-node", "unrelated"); dto != nil {
		t.Fatalf("no id + no slug → nil, got %+v", dto)
	}
}

// The id-join DTO carries the SAME due-⏰ enrichment as matchFE (shared helper):
// a decayed concept_key retention row behind the id-joined edge lights isDue.
func TestMatchFEByNode_CarriesDuePaintLikeMatchFE(t *testing.T) {
	const nodeID = "01980000-0000-7000-8000-0000000000c3"
	edge := kgTargetEdge(t, "Algebra", "algebra-basics", nodeID, 0.8)
	ret := map[string]*tr.TopicScore{
		// Reviewed 48h ago at S=1 → R=e^-2≈0.135 < DueThreshold → due.
		"algebra-basics": mustScore(t, "algebra-basics", dueNow.Add(-48*time.Hour), 1.0),
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex([]lw.LearnerWeakness{edge}), retention: ret, now: dueNow}

	dto := paint.matchFEByNode(nodeID, "irrelevant-label")
	if dto == nil || !dto.IsDue || dto.RetentionScore == nil {
		t.Fatalf("id-join DTO must carry due enrichment identical to matchFE, got %+v", dto)
	}
	if dto.RetentionScore != nil && *dto.RetentionScore >= tr.DueThreshold {
		t.Fatalf("retentionScore = %v; want present and < %v", *dto.RetentionScore, tr.DueThreshold)
	}
}

// Nil-safe: a nil paint bundle yields nil (no panic).
func TestMatchFEByNode_NilSafe(t *testing.T) {
	var p *growthPaint
	if dto := p.matchFEByNode("node", "label"); dto != nil {
		t.Fatalf("nil paint must be nil-safe, got %+v", dto)
	}
}
