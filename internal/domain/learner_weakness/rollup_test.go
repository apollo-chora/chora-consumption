// rollup_test.go — RED tests for the read-time Growth-Edge rollup (the promise
// in learner_weakness.go §"rolled up at read time by category / tag / topic" +
// migration 0046 that was never implemented). Pure-domain: no I/O, no clock.
package learner_weakness

import (
	"encoding/base64"
	"testing"
	"time"
)

var (
	tRoll0 = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tRoll1 = time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	tRoll2 = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
)

func rollEdge(id string, over func(*LearnerWeakness)) LearnerWeakness {
	e := LearnerWeakness{
		ID:              id,
		TenantID:        "t",
		LearnerGCID:     "g",
		ConceptKey:      "ck-" + id,
		ConceptLabel:    "Label " + id,
		Strength:        0.5,
		Sources:         []Source{SourceDerived},
		Status:          StatusActive,
		FirstSeenAt:     tRoll1,
		LastEvidencedAt: tRoll1,
		UpdatedAt:       tRoll1,
	}
	if over != nil {
		over(&e)
	}
	return e
}

func rollIDs(items []LearnerWeakness) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.ID
	}
	return out
}

func rollContains[T comparable](xs []T, want T) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// Same (category, topic) edges collapse to ONE representative = the strongest
// member, with unioned tags + sources, max strength, min first_seen, max
// last_evidenced.
func TestRollup_CollapsesSameCategoryTopic(t *testing.T) {
	a := rollEdge("a", func(e *LearnerWeakness) {
		e.Category, e.Strength, e.FirstSeenAt, e.LastEvidencedAt = "Scrum", 0.5, tRoll1, tRoll1
		e.Tags, e.Sources = []string{"alpha"}, []Source{SourceExplicit}
	})
	b := rollEdge("b", func(e *LearnerWeakness) {
		e.Category, e.Strength, e.FirstSeenAt, e.LastEvidencedAt = "Scrum", 0.9, tRoll0, tRoll2
		e.Tags, e.Sources = []string{"beta"}, []Source{SourceDerived}
		e.ConceptLabel = "Strongest Scrum Edge"
	})
	c := rollEdge("c", func(e *LearnerWeakness) {
		e.Category, e.Strength, e.FirstSeenAt, e.LastEvidencedAt = "scrum", 0.3, tRoll2, tRoll0 // case-insensitive cat
		e.Tags, e.Sources = []string{"alpha", "gamma"}, []Source{SourceClassroom}
	})

	got := Rollup([]LearnerWeakness{a, b, c})
	if len(got) != 1 {
		t.Fatalf("want 1 rolled-up edge; got %d (%v)", len(got), rollIDs(got))
	}
	rep := got[0]
	if rep.ID != "b" || rep.ConceptLabel != "Strongest Scrum Edge" {
		t.Errorf("representative = %q/%q; want strongest member b", rep.ID, rep.ConceptLabel)
	}
	if rep.Strength != 0.9 {
		t.Errorf("strength = %v; want max 0.9", rep.Strength)
	}
	if !rep.FirstSeenAt.Equal(tRoll0) {
		t.Errorf("first_seen = %v; want min %v", rep.FirstSeenAt, tRoll0)
	}
	if !rep.LastEvidencedAt.Equal(tRoll2) {
		t.Errorf("last_evidenced = %v; want max %v", rep.LastEvidencedAt, tRoll2)
	}
	if len(rep.Tags) != 3 || !rollContains(rep.Tags, "alpha") || !rollContains(rep.Tags, "beta") || !rollContains(rep.Tags, "gamma") {
		t.Errorf("tags = %v; want union {alpha,beta,gamma}", rep.Tags)
	}
	if len(rep.Sources) != 3 {
		t.Errorf("sources = %v; want union of all 3", rep.Sources)
	}
}

func TestRollup_DistinctCategoriesStaySeparate(t *testing.T) {
	a := rollEdge("a", func(e *LearnerWeakness) { e.Category = "Scrum" })
	b := rollEdge("b", func(e *LearnerWeakness) { e.Category = "Kanban" })
	got := Rollup([]LearnerWeakness{a, b})
	if len(got) != 2 {
		t.Fatalf("distinct categories must not merge; got %d (%v)", len(got), rollIDs(got))
	}
}

func TestRollup_SameCategoryDistinctTopicsStaySeparate(t *testing.T) {
	a := rollEdge("a", func(e *LearnerWeakness) { e.Category, e.TopicID = "Math", "topic-1" })
	b := rollEdge("b", func(e *LearnerWeakness) { e.Category, e.TopicID = "Math", "topic-2" })
	got := Rollup([]LearnerWeakness{a, b})
	if len(got) != 2 {
		t.Fatalf("same category but distinct resolved topics must not merge; got %d", len(got))
	}
}

func TestRollup_GroupsByTopicWhenNoCategory(t *testing.T) {
	a := rollEdge("a", func(e *LearnerWeakness) { e.TopicID, e.Strength = "topic-9", 0.4 })
	b := rollEdge("b", func(e *LearnerWeakness) { e.TopicID, e.Strength = "topic-9", 0.7 })
	got := Rollup([]LearnerWeakness{a, b})
	if len(got) != 1 {
		t.Fatalf("same topic (no category) must merge; got %d", len(got))
	}
	if got[0].ID != "b" {
		t.Errorf("representative = %q; want strongest b", got[0].ID)
	}
}

// Edges with NEITHER category NOR topic are NOT a rollup group — each stands
// alone (otherwise every unkeyed edge would collapse into one).
func TestRollup_KeylessEdgesStandalone(t *testing.T) {
	a := rollEdge("a", nil)
	b := rollEdge("b", nil)
	got := Rollup([]LearnerWeakness{a, b})
	if len(got) != 2 {
		t.Fatalf("keyless edges must each stand alone; got %d (%v)", len(got), rollIDs(got))
	}
}

func TestRollup_StatusRecomputedFromMaxStrength(t *testing.T) {
	// A grown (mastered) edge + an active edge on the same topic → the rep is
	// active (statusFor(max strength)); it is still a live frontier.
	grown := rollEdge("g", func(e *LearnerWeakness) {
		e.Category, e.Strength, e.Status = "Geo", 0.05, StatusGrown
	})
	active := rollEdge("a", func(e *LearnerWeakness) {
		e.Category, e.Strength, e.Status = "Geo", 0.8, StatusActive
	})
	got := Rollup([]LearnerWeakness{grown, active})
	if len(got) != 1 {
		t.Fatalf("want 1; got %d", len(got))
	}
	if got[0].Status != StatusActive {
		t.Errorf("status = %q; want active (recomputed from max strength)", got[0].Status)
	}
}

// The span timestamps aggregate across the WHOLE group even when the strongest
// member does not hold the extremes (first_seen = min, last_evidenced = max).
func TestRollup_AggregatesTimestampsAcrossGroup(t *testing.T) {
	strongest := rollEdge("rep", func(e *LearnerWeakness) {
		e.Category, e.Strength = "Hist", 0.9
		e.FirstSeenAt, e.LastEvidencedAt, e.UpdatedAt = tRoll1, tRoll1, tRoll1
	})
	weakerButOlderAndFresher := rollEdge("other", func(e *LearnerWeakness) {
		e.Category, e.Strength = "Hist", 0.4
		e.FirstSeenAt, e.LastEvidencedAt, e.UpdatedAt = tRoll0, tRoll2, tRoll2
	})
	got := Rollup([]LearnerWeakness{strongest, weakerButOlderAndFresher})
	if len(got) != 1 || got[0].ID != "rep" {
		t.Fatalf("want 1 rep 'rep'; got %v", rollIDs(got))
	}
	if !got[0].FirstSeenAt.Equal(tRoll0) {
		t.Errorf("first_seen = %v; want min from the non-rep member %v", got[0].FirstSeenAt, tRoll0)
	}
	if !got[0].LastEvidencedAt.Equal(tRoll2) {
		t.Errorf("last_evidenced = %v; want max from the non-rep member %v", got[0].LastEvidencedAt, tRoll2)
	}
	if !got[0].UpdatedAt.Equal(tRoll2) {
		t.Errorf("updated_at = %v; want max %v", got[0].UpdatedAt, tRoll2)
	}
}

func TestRollup_EmptyAndNil(t *testing.T) {
	if got := Rollup(nil); len(got) != 0 {
		t.Errorf("nil -> %d; want 0", len(got))
	}
	if got := Rollup([]LearnerWeakness{}); len(got) != 0 {
		t.Errorf("empty -> %d; want 0", len(got))
	}
}

// RollupPage: rollup the full set, sort, then offset-paginate. 5 keyless edges
// (each standalone) page in chunks of 2.
func TestRollupPage_SortsAndPaginates(t *testing.T) {
	all := []LearnerWeakness{
		rollEdge("e3", func(e *LearnerWeakness) { e.Strength = 0.7 }),
		rollEdge("e1", func(e *LearnerWeakness) { e.Strength = 0.9 }),
		rollEdge("e5", func(e *LearnerWeakness) { e.Strength = 0.5 }),
		rollEdge("e2", func(e *LearnerWeakness) { e.Strength = 0.8 }),
		rollEdge("e4", func(e *LearnerWeakness) { e.Strength = 0.6 }),
	}

	p1 := RollupPage(all, SortStrengthDesc, "", 2)
	if got := rollIDs(p1.Items); len(got) != 2 || got[0] != "e1" || got[1] != "e2" {
		t.Fatalf("page1 = %v; want [e1 e2] strength-desc", got)
	}
	if p1.NextPageToken == "" {
		t.Fatal("page1 must carry a next token")
	}

	p2 := RollupPage(all, SortStrengthDesc, p1.NextPageToken, 2)
	if got := rollIDs(p2.Items); len(got) != 2 || got[0] != "e3" || got[1] != "e4" {
		t.Fatalf("page2 = %v; want [e3 e4]", got)
	}
	if p2.NextPageToken == "" {
		t.Fatal("page2 must carry a next token")
	}

	p3 := RollupPage(all, SortStrengthDesc, p2.NextPageToken, 2)
	if got := rollIDs(p3.Items); len(got) != 1 || got[0] != "e5" {
		t.Fatalf("page3 = %v; want [e5]", got)
	}
	if p3.NextPageToken != "" {
		t.Errorf("last page must have no token; got %q", p3.NextPageToken)
	}
}

func TestRollupPage_SortVariants(t *testing.T) {
	all := []LearnerWeakness{
		rollEdge("old", func(e *LearnerWeakness) { e.FirstSeenAt, e.LastEvidencedAt = tRoll0, tRoll0 }),
		rollEdge("new", func(e *LearnerWeakness) { e.FirstSeenAt, e.LastEvidencedAt = tRoll2, tRoll2 }),
	}
	if got := rollIDs(RollupPage(all, SortLastEvidencedDesc, "", 10).Items); got[0] != "new" {
		t.Errorf("last_evidenced_desc first = %q; want new", got[0])
	}
	if got := rollIDs(RollupPage(all, SortFirstSeenDesc, "", 10).Items); got[0] != "new" {
		t.Errorf("first_seen_desc first = %q; want new", got[0])
	}
}

func TestRollupPage_PageSizeClamp(t *testing.T) {
	all := make([]LearnerWeakness, 0, 130)
	for i := 0; i < 130; i++ {
		all = append(all, rollEdge("id"+time.Duration(i).String()+string(rune('a'+i%26)), nil))
	}
	if n := len(RollupPage(all, SortStrengthDesc, "", 0).Items); n != 20 {
		t.Errorf("pageSize<=0 -> %d; want default 20", n)
	}
	if n := len(RollupPage(all, SortStrengthDesc, "", 999).Items); n != 100 {
		t.Errorf("pageSize>100 -> %d; want clamped 100", n)
	}
}

func TestRollupOffsetToken_RoundTripAndGuards(t *testing.T) {
	if decodeRollupOffset("") != 0 {
		t.Error("empty -> 0")
	}
	if decodeRollupOffset("@@not-base64@@") != 0 {
		t.Error("garbage -> 0")
	}
	tok := encodeRollupOffset(42)
	if decodeRollupOffset(tok) != 42 {
		t.Errorf("round-trip = %d; want 42", decodeRollupOffset(tok))
	}
	// non "o:" prefix and negative offsets decode defensively to 0.
	if decodeRollupOffset(base64.RawURLEncoding.EncodeToString([]byte("x:5"))) != 0 {
		t.Error("non o: prefix -> 0")
	}
	if decodeRollupOffset(base64.RawURLEncoding.EncodeToString([]byte("o:-3"))) != 0 {
		t.Error("negative offset -> 0")
	}
}

// Tie-break: equal strength → most-recent evidence wins; equal both → larger id.
func TestRollup_RepresentativeTieBreak(t *testing.T) {
	older := rollEdge("aaa", func(e *LearnerWeakness) { e.Category, e.Strength, e.LastEvidencedAt = "X", 0.5, tRoll1 })
	newer := rollEdge("bbb", func(e *LearnerWeakness) { e.Category, e.Strength, e.LastEvidencedAt = "X", 0.5, tRoll2 })
	if got := Rollup([]LearnerWeakness{older, newer}); len(got) != 1 || got[0].ID != "bbb" {
		t.Errorf("equal strength → recency wins; got %v", rollIDs(got))
	}

	loId := rollEdge("aaa", func(e *LearnerWeakness) { e.Category, e.Strength, e.LastEvidencedAt = "Y", 0.5, tRoll1 })
	hiId := rollEdge("zzz", func(e *LearnerWeakness) { e.Category, e.Strength, e.LastEvidencedAt = "Y", 0.5, tRoll1 })
	if got := Rollup([]LearnerWeakness{loId, hiId}); len(got) != 1 || got[0].ID != "zzz" {
		t.Errorf("equal strength+recency → larger id wins; got %v", rollIDs(got))
	}
}

func TestRollupPage_OffsetPastEnd(t *testing.T) {
	all := []LearnerWeakness{rollEdge("only", nil)}
	res := RollupPage(all, SortStrengthDesc, encodeRollupOffset(5), 10)
	if len(res.Items) != 0 || res.NextPageToken != "" {
		t.Errorf("offset past end → empty page no token; got %d items / %q", len(res.Items), res.NextPageToken)
	}
}
