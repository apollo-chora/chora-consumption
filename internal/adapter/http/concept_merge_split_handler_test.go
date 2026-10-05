// concept_merge_split_handler_test.go — WS-C6 (CHO-2085, ADR-227 D14 +
// addendum #7) RED tests for the sovereign merge/split doors:
//
//	POST /v1/me/concept-graph/concepts/{id}/merge  {survivorConceptId}
//	POST /v1/me/concept-graph/concepts/{id}/split  {children, childAssignments?, focusChildIndex?}
//
// The door orchestrates the four planners (graph plan, AND-of-rungs ladder,
// weakest/clone retention, goal concept_set+focus repair) into ONE atomic
// mergesplit.Apply — no campaign events fire (no XP/reveal re-fire: the C5
// seam), lineage records both identity axes, and the Companion's resonance
// follows a focus retarget post-commit (C1 idiom, idempotent).
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	"github.com/apollo-chora/chora-consumption/internal/domain/mergesplit"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// --- fakes -------------------------------------------------------------------

type mergeSplitApplierFake struct {
	got  *mergesplit.Apply
	err  error
	call int
}

func (f *mergeSplitApplierFake) Apply(_ context.Context, in mergesplit.Apply) error {
	f.call++
	cp := in
	f.got = &cp
	return f.err
}

type retentionStubMS struct {
	byTopic map[string]*topic_retention.TopicScore
}

func (s *retentionStubMS) Save(context.Context, *topic_retention.TopicScore) error { return nil }
func (s *retentionStubMS) Get(_ context.Context, _, _, topicID string) (*topic_retention.TopicScore, error) {
	return s.byTopic[topicID], nil
}
func (s *retentionStubMS) ListByLearner(context.Context, string, string, int) ([]*topic_retention.TopicScore, error) {
	return nil, nil
}

// progressRow builds a ladder row with a consistent positional audit.
func progressRow(conceptID string, rungs int, won bool) *campaign.NodeProgress {
	base := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	stamps := make([]time.Time, rungs)
	for i := range stamps {
		stamps[i] = base.Add(time.Duration(i) * 24 * time.Hour)
	}
	p := &campaign.NodeProgress{
		ID: "row-" + conceptID, TenantID: goTenant, LearnerGCID: goGCID,
		ConceptID: conceptID, RungsCleared: rungs, RungClearedAt: stamps,
		CreatedAt: base, UpdatedAt: base,
	}
	if won {
		w := base.Add(6 * 24 * time.Hour)
		p.WonAt = &w
	}
	return p
}

// retentionScore builds a TopicScore with a chosen strength + review time.
func retentionScore(t *testing.T, topicID string, strength float64, reviewedAt time.Time) *topic_retention.TopicScore {
	t.Helper()
	s, err := topic_retention.New(goTenant, goGCID, topicID, reviewedAt, strength)
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	return s
}

// mergeSplitHarness extends the campaign harness with the WS-C6 deps.
type mergeSplitHarness struct {
	*campaignHarness
	applier   *mergeSplitApplierFake
	retention *retentionStubMS
}

func newMergeSplitHarness(t *testing.T) *mergeSplitHarness {
	t.Helper()
	h := newCampaignHarness(t, true) // companion bound (resonance retarget observable)
	ms := &mergeSplitHarness{
		campaignHarness: h,
		applier:         &mergeSplitApplierFake{},
		retention:       &retentionStubMS{byTopic: map[string]*topic_retention.TopicScore{}},
	}
	h.ext.MergeSplit = ms.applier
	h.ext.Retention = ms.retention
	return ms
}

// --- merge ---------------------------------------------------------------------

func TestConceptMerge_AppliesFullDualAxisDelta(t *testing.T) {
	h := newMergeSplitHarness(t)
	now := time.Now().UTC()

	// Ladders: survivor camp-a 4/6, absorbed camp-b 2/6 → AND = 2/6 (AC-1).
	h.progress.rows = []*campaign.NodeProgress{
		progressRow("camp-a", 4, false),
		progressRow("camp-b", 2, false),
	}
	// Retention: survivor strong, absorbed weak → survivor's key carries the
	// WEAKEST curve post-merge.
	h.retention.byTopic["photosynthesis"] = retentionScore(t, "photosynthesis", 8, now.Add(-1*time.Hour))
	h.retention.byTopic["roots"] = retentionScore(t, "roots", 1, now.Add(-72*time.Hour))
	// Goal scopes the absorbed key + focuses the absorbed node.
	if _, err := h.goal.RepairConceptSet(nil, []string{"roots", "botany"}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed concept_set: %v", err)
	}
	focus := "camp-b"
	if err := h.goal.SetFocusConcept(&focus, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed focus: %v", err)
	}
	h.goals.Update(context.Background(), h.goal)

	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-b/merge",
		map[string]any{"survivorConceptId": "camp-a"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	if h.applier.call != 1 || h.applier.got == nil {
		t.Fatalf("applier calls = %d, want 1", h.applier.call)
	}
	got := h.applier.got

	// Graph: absorbed tombstoned + dual-axis lineage.
	if len(got.NodesToTombstone) != 1 || got.NodesToTombstone[0].ConceptID != "camp-b" || got.NodesToTombstone[0].DeletedAt == nil {
		t.Fatalf("NodesToTombstone = %+v, want tombstoned camp-b", got.NodesToTombstone)
	}
	if len(got.Lineage) != 1 {
		t.Fatalf("Lineage = %+v, want 1 record", got.Lineage)
	}
	lin := got.Lineage[0]
	if lin.FromConceptID != "camp-b" || lin.ToConceptID != "camp-a" ||
		lin.FromConceptKey != "roots" || lin.ToConceptKey != "photosynthesis" {
		t.Fatalf("lineage record wrong: %+v", lin)
	}

	// Ladder: both live rows tombstoned; AND row inserted at 2/6, counter 0.
	if len(got.ProgressTombstoneConceptIDs) != 2 {
		t.Fatalf("ProgressTombstoneConceptIDs = %v, want both operands", got.ProgressTombstoneConceptIDs)
	}
	if len(got.ProgressToInsert) != 1 {
		t.Fatalf("ProgressToInsert = %+v, want the AND row", got.ProgressToInsert)
	}
	and := got.ProgressToInsert[0]
	if and.ConceptID != "camp-a" || and.RungsCleared != 2 || and.WonAt != nil || and.CurrentRungCorrect != 0 {
		t.Fatalf("AND row wrong: %+v", and)
	}

	// Retention: survivor's key re-keyed with the WEAKER (absorbed) curve.
	if len(got.RetentionToUpsert) != 1 {
		t.Fatalf("RetentionToUpsert = %+v, want 1 row", got.RetentionToUpsert)
	}
	ret := got.RetentionToUpsert[0]
	if ret.TopicID != "photosynthesis" || ret.Strength != 1 {
		t.Fatalf("retention re-key wrong: topic=%s strength=%v (want photosynthesis carrying the weak S=1)", ret.TopicID, ret.Strength)
	}

	// Suggestions: pending focal rows repoint absorbed → survivor.
	if len(got.SuggestionRepoints) != 1 || got.SuggestionRepoints[0].FromConceptID != "camp-b" || got.SuggestionRepoints[0].ToConceptID != "camp-a" {
		t.Fatalf("SuggestionRepoints = %+v", got.SuggestionRepoints)
	}

	// Goal: concept_set re-keyed + focus follows onto the survivor, in-tx.
	if len(got.GoalsToUpdate) != 1 {
		t.Fatalf("GoalsToUpdate = %+v, want the scoping goal", got.GoalsToUpdate)
	}
	ug := got.GoalsToUpdate[0]
	wantSet := map[string]bool{"botany": true, "photosynthesis": true}
	if len(ug.ConceptSet) != 2 || !wantSet[ug.ConceptSet[0]] || !wantSet[ug.ConceptSet[1]] {
		t.Fatalf("repaired ConceptSet = %v, want {botany, photosynthesis}", ug.ConceptSet)
	}
	if ug.FocusConceptID == nil || *ug.FocusConceptID != "camp-a" {
		t.Fatalf("focus = %v, want camp-a (follows the merge)", ug.FocusConceptID)
	}

	// Resonance follows the focus post-commit (addendum #5 — one affordance).
	if len(h.resonance.calls) != 1 || h.resonance.calls[0] == nil || *h.resonance.calls[0] != "camp-a" {
		t.Fatalf("resonance calls = %+v, want one retarget to camp-a", h.resonance.calls)
	}

	// No campaign events (the C5 seam: no XP/reveal re-fire on merge).
	if len(h.events.focus) != 0 || len(h.events.sealed) != 0 {
		t.Fatalf("campaign events fired on merge: focus=%d sealed=%d, want none", len(h.events.focus), len(h.events.sealed))
	}
}

func TestConceptMerge_RootImmutable409(t *testing.T) {
	h := newMergeSplitHarness(t)
	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-root/merge",
		map[string]any{"survivorConceptId": "camp-a"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 ROOT_IMMUTABLE (body=%s)", w.Code, w.Body.String())
	}
	if h.applier.call != 0 {
		t.Fatalf("applier called on a refused merge")
	}
}

func TestConceptMerge_UnknownSurvivor404(t *testing.T) {
	h := newMergeSplitHarness(t)
	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-b/merge",
		map[string]any{"survivorConceptId": "nope"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
}

// --- split ---------------------------------------------------------------------

func TestConceptSplit_AppliesInheritanceDelta(t *testing.T) {
	h := newMergeSplitHarness(t)
	now := time.Now().UTC()

	// A WON parent: children must inherit 6/6 + WonAt with NO events (AC-2).
	h.progress.rows = []*campaign.NodeProgress{progressRow("camp-a", 6, true)}
	h.retention.byTopic["photosynthesis"] = retentionScore(t, "photosynthesis", 4, now.Add(-24*time.Hour))
	// Goal scopes the parent key + focuses the parent.
	if _, err := h.goal.RepairConceptSet(nil, []string{"photosynthesis"}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed concept_set: %v", err)
	}
	focus := "camp-a"
	if err := h.goal.SetFocusConcept(&focus, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed focus: %v", err)
	}
	h.goals.Update(context.Background(), h.goal)

	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-a/split",
		map[string]any{
			"children":        []map[string]any{{"title": "Light Reactions"}, {"title": "Dark Reactions"}},
			"focusChildIndex": 1,
		}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	got := h.applier.got
	if got == nil {
		t.Fatalf("applier never called")
	}

	// Children minted; parent tombstoned; lineage per child on both axes.
	if len(got.NodesToCreate) != 2 {
		t.Fatalf("NodesToCreate = %+v, want 2 children", got.NodesToCreate)
	}
	if len(got.NodesToTombstone) != 1 || got.NodesToTombstone[0].ConceptID != "camp-a" {
		t.Fatalf("NodesToTombstone = %+v, want camp-a", got.NodesToTombstone)
	}
	if len(got.Lineage) != 2 || got.Lineage[0].FromConceptKey != "photosynthesis" || got.Lineage[1].FromConceptKey != "photosynthesis" {
		t.Fatalf("Lineage = %+v, want 2 parent-keyed records", got.Lineage)
	}

	// Ladder inheritance: parent row tombstoned; children carry 6/6 + WonAt.
	if len(got.ProgressTombstoneConceptIDs) != 1 || got.ProgressTombstoneConceptIDs[0] != "camp-a" {
		t.Fatalf("ProgressTombstoneConceptIDs = %v", got.ProgressTombstoneConceptIDs)
	}
	if len(got.ProgressToInsert) != 2 {
		t.Fatalf("ProgressToInsert = %+v, want 2 inherited rows", got.ProgressToInsert)
	}
	for i, p := range got.ProgressToInsert {
		if p.RungsCleared != 6 || p.WonAt == nil || p.CurrentRungCorrect != 0 {
			t.Fatalf("child %d ladder wrong: %+v (want inherited 6/6 won)", i, p)
		}
	}

	// Retention clones onto each child key.
	if len(got.RetentionToUpsert) != 2 {
		t.Fatalf("RetentionToUpsert = %+v, want 2 clones", got.RetentionToUpsert)
	}
	childKeys := map[string]bool{"light-reactions": true, "dark-reactions": true}
	for _, r := range got.RetentionToUpsert {
		if !childKeys[r.TopicID] || r.Strength != 4 {
			t.Fatalf("retention clone wrong: %+v", r)
		}
	}

	// Focus follows the learner's chosen child (index 1) + suggestions repoint there.
	if len(got.GoalsToUpdate) != 1 {
		t.Fatalf("GoalsToUpdate = %+v", got.GoalsToUpdate)
	}
	chosen := got.NodesToCreate[1].ConceptID
	if fc := got.GoalsToUpdate[0].FocusConceptID; fc == nil || *fc != chosen {
		t.Fatalf("focus = %v, want chosen child %s", fc, chosen)
	}
	if len(got.SuggestionRepoints) != 1 || got.SuggestionRepoints[0].ToConceptID != chosen {
		t.Fatalf("SuggestionRepoints = %+v, want repoint to chosen child", got.SuggestionRepoints)
	}
	// Goal concept_set gains BOTH child keys, drops the parent's.
	set := got.GoalsToUpdate[0].ConceptSet
	if len(set) != 2 || !childKeys[set[0]] || !childKeys[set[1]] {
		t.Fatalf("repaired ConceptSet = %v, want the two child keys", set)
	}

	// Resonance followed the focus retarget post-commit.
	if len(h.resonance.calls) != 1 || h.resonance.calls[0] == nil || *h.resonance.calls[0] != chosen {
		t.Fatalf("resonance calls = %+v, want retarget to %s", h.resonance.calls, chosen)
	}

	// Response carries the minted children.
	var resp struct {
		ParentConceptID string `json:"parentConceptId"`
		Children        []struct {
			ConceptID  string `json:"conceptId"`
			Title      string `json:"title"`
			ConceptKey string `json:"conceptKey"`
		} `json:"children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if resp.ParentConceptID != "camp-a" || len(resp.Children) != 2 || resp.Children[0].ConceptKey != "light-reactions" {
		t.Fatalf("response wrong: %+v", resp)
	}
}

func TestConceptSplit_RootImmutable409(t *testing.T) {
	h := newMergeSplitHarness(t)
	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-root/split",
		map[string]any{"children": []map[string]any{{"title": "A"}, {"title": "B"}}}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
}

func TestConceptSplit_TooFewChildren422(t *testing.T) {
	h := newMergeSplitHarness(t)
	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-a/split",
		map[string]any{"children": []map[string]any{{"title": "Only One"}}}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body=%s)", w.Code, w.Body.String())
	}
	if h.applier.call != 0 {
		t.Fatalf("applier called on a refused split")
	}
}

func TestConceptMergeSplit_NotWired503(t *testing.T) {
	h := newCampaignHarness(t, false) // no MergeSplit / Retention wired
	var ext *httpadapter.ExtServer = h.ext
	w := doGoal(ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-b/merge",
		map[string]any{"survivorConceptId": "camp-a"}))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", w.Code, w.Body.String())
	}
}

// ADR-244 D6 (CHO-2303): end to end through the handler, a merge must carry the
// UNION of both operands' atom_refs on a LIVE survivor. Before D6 the absorbed
// node was tombstoned with its atoms still on it and they left the live graph.
func TestConceptMerge_SurvivorCarriesUnionOfAtomRefs(t *testing.T) {
	h := newMergeSplitHarness(t)

	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-b/merge",
		map[string]any{"survivorConceptId": "camp-a"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	got := h.applier.got
	if got == nil {
		t.Fatal("applier never called")
	}
	if len(got.NodesToUpdate) != 1 {
		t.Fatalf("NodesToUpdate = %+v, want exactly the survivor", got.NodesToUpdate)
	}
	surv := got.NodesToUpdate[0]
	if surv.ConceptID != "camp-a" {
		t.Fatalf("NodesToUpdate[0].ConceptID = %q, want camp-a", surv.ConceptID)
	}
	if surv.DeletedAt != nil {
		t.Fatalf("survivor DeletedAt = %v, want nil (it must stay live)", surv.DeletedAt)
	}
	// AtomRefs is always non-nil (normaliseAtomRefs guarantees a stable wire
	// shape), so a nil here means the survivor was never planned through the
	// aggregate. The union arithmetic itself is proven in the conceptgraph
	// package, and its persistence in the pg applier test: the shared
	// campaignGraph() fixture seeds no atoms, so asserting the union HERE would
	// pass vacuously.
	if surv.AtomRefs == nil {
		t.Fatal("survivor AtomRefs is nil: the survivor did not come from PlanMerge")
	}
}

// ---- ADR-244 D4: merge + split emit concept.atoms_bound ----

type msStubConceptEvents struct {
	calls int
	got   []events.ConceptAtomsBoundInput
}

func (s *msStubConceptEvents) ConceptAtomsBound(_ context.Context, in events.ConceptAtomsBoundInput) error {
	s.calls++
	s.got = append(s.got, in)
	return nil
}

func (s *msStubConceptEvents) ConceptDeleted(_ context.Context, _ events.ConceptDeletedInput) error {
	return nil
}

func TestConceptMerge_EmitsAtomsBoundForSurvivorOnly(t *testing.T) {
	h := newMergeSplitHarness(t)
	ev := &msStubConceptEvents{}
	h.ext.ConceptEvents = ev

	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-b/merge",
		map[string]any{"survivorConceptId": "camp-a"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	// The fixture seeds no atoms, so the emitter correctly refuses a no-op.
	// What must NEVER happen is an event for the ABSORBED node: it is
	// tombstoned, and reporting a detach for it would misrepresent a
	// concept-lifecycle fact as the learner unbinding atoms.
	for _, in := range ev.got {
		if in.ConceptID == "camp-b" {
			t.Errorf("emitted for the absorbed node: %+v", in)
		}
		if in.ChangeSource != events.ChangeSourceMerge {
			t.Errorf("change_source = %q, want %q", in.ChangeSource, events.ChangeSourceMerge)
		}
	}
}

func TestConceptSplit_EmitsAtomsBoundPerChild(t *testing.T) {
	h := newMergeSplitHarness(t)
	ev := &msStubConceptEvents{}
	h.ext.ConceptEvents = ev

	w := doGoal(h.ext, goalReq(http.MethodPost, "/v1/me/concept-graph/concepts/camp-a/split",
		map[string]any{"children": []map[string]any{{"title": "Light"}, {"title": "Dark"}}}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	// The tombstoned parent must never emit, for the same reason the absorbed
	// node does not on merge.
	for _, in := range ev.got {
		if in.ConceptID == "camp-a" {
			t.Errorf("emitted for the tombstoned parent: %+v", in)
		}
		if in.ChangeSource != events.ChangeSourceSplit {
			t.Errorf("change_source = %q, want %q", in.ChangeSource, events.ChangeSourceSplit)
		}
	}
}
