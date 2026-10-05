// kg_due_overlay_test.go — CHO-1950 (ADR-204 P2): the orthogonal due-⏰
// terrain overlay. The growth paint layers a "due for review" flag onto a
// matched Growth Edge whenever the edge resolves to a topic whose Ebbinghaus
// retention has decayed below topic_retention.DueThreshold. Strictly read-time
// + fail-soft: an unresolved topic, an absent score, or a nil retention repo
// simply yields no due paint (the colour band still renders from strength).
package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	tr "github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// dueNow is the fixed wall-clock for the deterministic paint-bundle unit tests.
var dueNow = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

// kgDueEdge builds a Growth Edge with an optional resolved TopicID (""=none),
// so the retention join can (or cannot) light a due flag.
func kgDueEdge(t *testing.T, label string, strength float64, topicID string) lw.LearnerWeakness {
	t.Helper()
	w, err := lw.New(lw.UpsertInput{
		TenantID:     kgTenantID,
		LearnerGCID:  kgGCID,
		ConceptLabel: label,
		Embedding:    []float32{0.1},
		Strength:     strength,
		TopicID:      topicID,
		Source:       lw.SourceDerived,
		Now:          dueNow.Add(-72 * time.Hour),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	return *w
}

// mustScore mints a per-(tenant,gcid,topic) retention score reviewed at the
// supplied time with the supplied strength (days).
func mustScore(t *testing.T, topicID string, lastReviewed time.Time, strength float64) *tr.TopicScore {
	t.Helper()
	s, err := tr.New(kgTenantID, kgGCID, topicID, lastReviewed, strength)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	return s
}

func TestGrowthPaint_MatchFE_DueAndFresh(t *testing.T) {
	const decayedTopic = "01970000-0000-7000-d000-000000000001"
	const freshTopic = "01970000-0000-7000-d000-000000000002"
	edges := []lw.LearnerWeakness{
		kgDueEdge(t, "Decayed Topic", 0.8, decayedTopic),
		kgDueEdge(t, "Fresh Topic", 0.5, freshTopic),
	}
	ret := map[string]*tr.TopicScore{
		decayedTopic: mustScore(t, decayedTopic, dueNow.Add(-48*time.Hour), 1.0), // R=e^-2≈0.135 < 0.6 → due
		freshTopic:   mustScore(t, freshTopic, dueNow, 1.0),                      // R=1.0 → not due
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	due := paint.matchFE("Decayed Topic")
	if due == nil || !due.IsDue {
		t.Fatalf("decayed matchFE.IsDue = false; want true: %+v", due)
	}
	if due.RetentionScore == nil || *due.RetentionScore >= tr.DueThreshold {
		t.Fatalf("decayed retentionScore = %v; want present and < %v", due.RetentionScore, tr.DueThreshold)
	}

	fresh := paint.matchFE("Fresh Topic")
	if fresh == nil || fresh.IsDue {
		t.Fatalf("fresh matchFE.IsDue = true; want false: %+v", fresh)
	}
	if fresh.RetentionScore == nil {
		t.Fatalf("fresh retentionScore = nil; want present (resolved topic)")
	}
}

func TestGrowthPaint_MatchFog_SnakeCaseDue(t *testing.T) {
	const topic = "01970000-0000-7000-d000-000000000003"
	edges := []lw.LearnerWeakness{kgDueEdge(t, "Algebra", 0.9, topic)}
	ret := map[string]*tr.TopicScore{topic: mustScore(t, topic, dueNow.Add(-72*time.Hour), 1.0)}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	dto := paint.matchFog("Algebra")
	if dto == nil || !dto.IsDue || dto.RetentionScore == nil {
		t.Fatalf("matchFog due paint = %+v; want is_due + retention_score", dto)
	}
}

func TestGrowthPaint_NoDueWhenTopicUnresolvedOrNoScore(t *testing.T) {
	const resolvedNoScore = "01970000-0000-7000-d000-000000000004"
	edges := []lw.LearnerWeakness{
		kgDueEdge(t, "Unresolved", 0.8, ""),            // no TopicID
		kgDueEdge(t, "No Score", 0.8, resolvedNoScore), // resolved but absent from the index
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: map[string]*tr.TopicScore{}, now: dueNow}

	unr := paint.matchFE("Unresolved")
	if unr == nil || unr.IsDue || unr.RetentionScore != nil {
		t.Fatalf("unresolved-topic edge should carry no due paint: %+v", unr)
	}
	noScore := paint.matchFE("No Score")
	if noScore == nil || noScore.IsDue || noScore.RetentionScore != nil {
		t.Fatalf("resolved-but-unscored edge should carry no due paint: %+v", noScore)
	}
}

func TestGrowthPaint_NoMatchReturnsNil(t *testing.T) {
	paint := &growthPaint{edges: lw.BuildOverlayIndex(nil), retention: nil, now: dueNow}
	if dto := paint.matchFE("anything"); dto != nil {
		t.Fatalf("matchFE on empty overlay = %+v; want nil", dto)
	}
	if dto := paint.matchFog("anything"); dto != nil {
		t.Fatalf("matchFog on empty overlay = %+v; want nil", dto)
	}
}

// --- CHO-2108: concept_key fallback join ------------------------------------
//
// Campaign practice grading (WS-C7) plants topic_retention rows keyed by the
// node's concept_key (campaign.Grader.RecordAnswer → Retention.Save with
// in.ConceptKey), while nothing populates learner_weakness.topic_id
// organically. The due-⏰ join therefore falls back to the edge's ConceptKey
// (the shared normalised-slug vocabulary) when the TopicID join is empty or
// misses. TopicID keeps first precedence; neither join hitting stays
// fail-soft (no cue, never an error).

func TestGrowthPaint_DueFallsBackToConceptKey(t *testing.T) {
	// Edge with NO resolved TopicID; the retention row is keyed by the
	// concept_key slug ("algebra-basics"), decayed well past DueThreshold.
	edges := []lw.LearnerWeakness{kgDueEdge(t, "Algebra Basics", 0.8, "")}
	ret := map[string]*tr.TopicScore{
		"algebra-basics": mustScore(t, "algebra-basics", dueNow.Add(-48*time.Hour), 1.0), // R=e^-2≈0.135 < 0.6 → due
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	dto := paint.matchFE("Algebra Basics")
	if dto == nil || !dto.IsDue {
		t.Fatalf("concept_key fallback matchFE.IsDue = false; want true: %+v", dto)
	}
	if dto.RetentionScore == nil || *dto.RetentionScore >= tr.DueThreshold {
		t.Fatalf("concept_key fallback retentionScore = %v; want present and < %v", dto.RetentionScore, tr.DueThreshold)
	}
}

func TestGrowthPaint_DueFallsBackWhenTopicIDMisses(t *testing.T) {
	// TopicID resolved but absent from the retention index → the join falls
	// through to the concept_key row instead of dropping the cue.
	const missingTopic = "01970000-0000-7000-d000-000000000030"
	edges := []lw.LearnerWeakness{kgDueEdge(t, "Fractions", 0.8, missingTopic)}
	ret := map[string]*tr.TopicScore{
		"fractions": mustScore(t, "fractions", dueNow.Add(-48*time.Hour), 1.0),
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	dto := paint.matchFE("Fractions")
	if dto == nil || !dto.IsDue || dto.RetentionScore == nil {
		t.Fatalf("TopicID-miss fallback = %+v; want isDue + retentionScore from the concept_key row", dto)
	}
}

func TestGrowthPaint_TopicIDJoinKeepsPrecedence(t *testing.T) {
	// BOTH joins available: the fresh TopicID row (not due) must win over the
	// decayed concept_key decoy — topic_id stays first precedence.
	const topic = "01970000-0000-7000-d000-000000000031"
	edges := []lw.LearnerWeakness{kgDueEdge(t, "Geometry", 0.8, topic)}
	ret := map[string]*tr.TopicScore{
		topic:      mustScore(t, topic, dueNow, 1.0),                         // R=1.0 → not due
		"geometry": mustScore(t, "geometry", dueNow.Add(-48*time.Hour), 1.0), // decayed decoy
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	dto := paint.matchFE("Geometry")
	if dto == nil || dto.IsDue {
		t.Fatalf("TopicID row must win over the concept_key decoy: %+v", dto)
	}
	if dto.RetentionScore == nil || *dto.RetentionScore != 1.0 {
		t.Fatalf("retentionScore = %v; want 1.0 from the TopicID row", dto.RetentionScore)
	}
}

func TestGrowthPaint_ConceptKeyRowFreshNotDue(t *testing.T) {
	// The fallback join hits a FRESH concept_key row (R >= DueThreshold):
	// resolved (score present) but not due.
	edges := []lw.LearnerWeakness{kgDueEdge(t, "Statistics", 0.8, "")}
	ret := map[string]*tr.TopicScore{
		"statistics": mustScore(t, "statistics", dueNow, 1.0), // R=1.0 → not due
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	dto := paint.matchFE("Statistics")
	if dto == nil || dto.IsDue {
		t.Fatalf("fresh concept_key row must not paint due: %+v", dto)
	}
	if dto.RetentionScore == nil || *dto.RetentionScore < tr.DueThreshold {
		t.Fatalf("retentionScore = %v; want present and >= %v (join hit, not due)", dto.RetentionScore, tr.DueThreshold)
	}
}

func TestGrowthPaint_NeitherJoinNoCue(t *testing.T) {
	// Neither the TopicID nor the concept_key row exists → fail-soft: no due
	// paint, shaky strength band untouched, never an error.
	edges := []lw.LearnerWeakness{kgDueEdge(t, "Trigonometry", 0.8, "")}
	ret := map[string]*tr.TopicScore{
		"unrelated-key": mustScore(t, "unrelated-key", dueNow.Add(-48*time.Hour), 1.0),
	}
	paint := &growthPaint{edges: lw.BuildOverlayIndex(edges), retention: ret, now: dueNow}

	dto := paint.matchFE("Trigonometry")
	if dto == nil || dto.IsDue || dto.RetentionScore != nil {
		t.Fatalf("neither join → no cue, got %+v", dto)
	}
	if dto.Strength != 0.8 {
		t.Fatalf("shaky paint must be unaffected by the due miss: %+v", dto)
	}
}

// learnerRetentionIndex reads the learner's live scores into a topic_id-keyed
// map (skipping unresolved/blank topics). Fail-soft on a nil repo.
func TestLearnerRetentionIndex_KeysByTopicID(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	const topicA = "01970000-0000-7000-d000-000000000010"
	if err := srv.Retention.Save(ctx, mustScore(t, topicA, dueNow.Add(-24*time.Hour), 1.0)); err != nil {
		t.Fatal(err)
	}
	idx := srv.learnerRetentionIndex(ctx, kgTenantID, kgGCID)
	if idx[topicA] == nil {
		t.Fatalf("index missing topic %s: %+v", topicA, idx)
	}

	srv.Retention = nil
	if idx := srv.learnerRetentionIndex(ctx, kgTenantID, kgGCID); len(idx) != 0 {
		t.Fatalf("nil retention repo must yield empty index, got %+v", idx)
	}
}

// End-to-end through the real cluster-list handler: a decayed topic behind the
// matched edge surfaces isDue + retentionScore on the camelCase cluster card.
func TestKGClusters_List_DueOverlay(t *testing.T) {
	srv := newKGSrv()
	const topic = "01970000-0000-7000-d000-000000000020"
	srv.LearnerWeakness = &kgLWStub{items: []lw.LearnerWeakness{kgDueEdge(t, "Agile", 0.7, topic)}}
	// Reviewed 3 days ago at S=1 → R≈0.05 < 0.6 → robustly due regardless of now.
	if err := srv.Retention.Save(context.Background(), mustScore(t, topic, time.Now().UTC().Add(-72*time.Hour), 1.0)); err != nil {
		t.Fatal(err)
	}
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	if err := srv.KGClusters.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	w := serveKG(srv, newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Clusters []struct {
				GrowthEdge *struct {
					ConceptKey     string   `json:"conceptKey"`
					IsDue          bool     `json:"isDue"`
					RetentionScore *float64 `json:"retentionScore"`
				} `json:"growthEdge"`
			} `json:"clusters"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Clusters) != 1 {
		t.Fatalf("clusters = %d", len(resp.Data.Clusters))
	}
	ge := resp.Data.Clusters[0].GrowthEdge
	if ge == nil || !ge.IsDue || ge.RetentionScore == nil {
		t.Fatalf("cluster growthEdge due paint = %+v; want isDue + retentionScore", ge)
	}
}
