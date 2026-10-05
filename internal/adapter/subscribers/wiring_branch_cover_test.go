// wiring_branch_cover_test.go — coverage for the small pure / setter /
// free-function branches in internal/adapter/subscribers that are reachable
// WITHOUT a real broker:
//
//   - CampaignQuestionTerminalSubscriber.WithNow + ProofingTestTerminalSubscriber.WithNow
//     (the 0% clock-injector setters),
//   - the bounded-concept-embedding-cache public constructor wrapper,
//   - the pure free function validateWaveOneXP,
//   - CampaignNodeWonSubscriber.resolveTheme (all four resolution branches).
//
// It reuses the same-package fakes already established by the per-feature
// test files (cqFakeRepo, terminalManaFake, seedTerminal, countingEmbedder,
// nwConcepts / nwNode).
package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	"github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// --- WithNow (campaign question terminal) ----------------------------------

func TestCampaignQuestionTerminalSubscriber_WithNow(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqFakeRepo{})
	if sub.now == nil {
		t.Fatal("constructor now clock is nil")
	}
	want := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	if got := sub.WithNow(func() time.Time { return want }); got != sub {
		t.Fatal("WithNow must return the same subscriber (chaining contract)")
	}
	if got := sub.now(); !got.Equal(want) {
		t.Fatalf("now() = %v, want %v", got, want)
	}
	// nil does not clobber an injected clock.
	sub.WithNow(nil)
	if got := sub.now(); !got.Equal(want) {
		t.Fatalf("now() after WithNow(nil) = %v, want %v (unchanged)", got, want)
	}
}

// --- WithNow (proofing test terminal) --------------------------------------

func TestProofingTestTerminalSubscriber_WithNow(t *testing.T) {
	sub := NewProofingTestTerminalSubscriber(&inmem.ProofingTestRepo{}, &terminalManaFake{})
	if sub.now == nil {
		t.Fatal("constructor now clock is nil")
	}
	want := time.Date(2030, 6, 2, 9, 30, 0, 0, time.UTC)
	if got := sub.WithNow(func() time.Time { return want }); got != sub {
		t.Fatal("WithNow must return the same subscriber (chaining contract)")
	}
	if got := sub.now(); !got.Equal(want) {
		t.Fatalf("now() = %v, want %v", got, want)
	}
	sub.WithNow(nil)
	if got := sub.now(); !got.Equal(want) {
		t.Fatalf("now() after WithNow(nil) = %v, want %v (unchanged)", got, want)
	}
}

// --- NewConceptEmbeddingCache (public wrapper) ------------------------------

// The bounded cache logic is covered by concept_embedding_cache_test.go via the
// inner constructor (newConceptEmbeddingCache); this only exercises the
// composition-root wrapper default (4096) so its single statement is covered.
func TestNewConceptEmbeddingCache_DefaultSizeWrapsAndCaches(t *testing.T) {
	inner := &countingEmbedder{}
	c := NewConceptEmbeddingCache(inner)
	if c == nil {
		t.Fatal("NewConceptEmbeddingCache returned nil")
	}
	v1, err := c.Embed(context.Background(), "hello", "t1")
	if err != nil {
		t.Fatalf("first Embed: %v", err)
	}
	v2, err := c.Embed(context.Background(), "hello", "t1")
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}
	if inner.count() != 1 {
		t.Fatalf("inner embedder called %d times; want 1 (second call should be a cache hit)", inner.count())
	}
	if len(v1) == 0 || len(v2) == 0 {
		t.Fatal("embedding vectors empty")
	}
}

// --- validateWaveOneXP (pure free function) ---------------------------------

func TestValidateWaveOneXP(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		eventID string
		tenant  string
		gcid    string
		wantErr bool
	}{
		{name: "valid", kind: "submission_graded", eventID: "ev-1", tenant: "t", gcid: "g", wantErr: false},
		{name: "missing eventID", kind: "submission_graded", eventID: "  ", tenant: "t", gcid: "g", wantErr: true},
		{name: "missing tenant", kind: "submission_graded", eventID: "ev-1", tenant: "", gcid: "g", wantErr: true},
		{name: "missing gcid", kind: "submission_graded", eventID: "ev-1", tenant: "t", gcid: " ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWaveOneXP(tt.kind, tt.eventID, tt.tenant, tt.gcid)
			if tt.wantErr && err == nil {
				t.Fatalf("validateWaveOneXP(%q,%q,%q,%q) = nil, want error", tt.kind, tt.eventID, tt.tenant, tt.gcid)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateWaveOneXP(%q,%q,%q,%q) = %v, want nil", tt.kind, tt.eventID, tt.tenant, tt.gcid, err)
			}
			if err != nil && !strings.Contains(err.Error(), tt.kind) {
				t.Errorf("error %q does not mention kind %q", err, tt.kind)
			}
		})
	}
}

// --- resolveTheme (campaign node won) ---------------------------------------

func TestResolveTheme_RootConceptTitleWins(t *testing.T) {
	sub := &CampaignNodeWonSubscriber{
		concepts: &nwConcepts{nodes: []*conceptgraph.ConceptNode{nwNode("root", "Algebra")}},
	}
	root := "root"
	g := &goal.Goal{RootConceptID: &root, NorthStarNote: "NorthStar should lose"}
	if got := sub.resolveTheme(context.Background(), nwTenant, nwGCID, g); got != "Algebra" {
		t.Fatalf("resolveTheme = %q, want %q (root concept title)", got, "Algebra")
	}
}

func TestResolveTheme_RootMissingFallsBackToNorthStarNote(t *testing.T) {
	sub := &CampaignNodeWonSubscriber{
		concepts: &nwConcepts{nodes: []*conceptgraph.ConceptNode{nwNode("other", "x")}},
	}
	root := "root" // not present in the repo
	g := &goal.Goal{RootConceptID: &root, NorthStarNote: "Own algebra"}
	if got := sub.resolveTheme(context.Background(), nwTenant, nwGCID, g); got != "Own algebra" {
		t.Fatalf("resolveTheme = %q, want %q (NorthStarNote fallback)", got, "Own algebra")
	}
}

func TestResolveTheme_RootConceptBlankFallsBackToNorthStarNote(t *testing.T) {
	sub := &CampaignNodeWonSubscriber{
		concepts: &nwConcepts{nodes: []*conceptgraph.ConceptNode{nwNode("root", "Algebra")}},
	}
	root := "   " // blank root id
	g := &goal.Goal{RootConceptID: &root, NorthStarNote: "note"}
	if got := sub.resolveTheme(context.Background(), nwTenant, nwGCID, g); got != "note" {
		t.Fatalf("resolveTheme = %q, want %q", got, "note")
	}
}

func TestResolveTheme_RootConceptLoadErrorFallsBackToNorthStarNote(t *testing.T) {
	sub := &CampaignNodeWonSubscriber{concepts: errConceptRepo{}}
	root := "root"
	g := &goal.Goal{RootConceptID: &root, NorthStarNote: "note from err"}
	if got := sub.resolveTheme(context.Background(), nwTenant, nwGCID, g); got != "note from err" {
		t.Fatalf("resolveTheme = %q, want %q", got, "note from err")
	}
}

func TestResolveTheme_NoRootNoNoteReturnsDiscovery(t *testing.T) {
	sub := &CampaignNodeWonSubscriber{concepts: &nwConcepts{}}
	g := &goal.Goal{} // nil root, empty north star
	if got := sub.resolveTheme(context.Background(), nwTenant, nwGCID, g); got != "discovery" {
		t.Fatalf("resolveTheme = %q, want %q", got, "discovery")
	}
}

// errConceptRepo is a conceptgraph.ConceptNodeRepository whose GetByID always
// fails, exercising the error arm of resolveTheme.
type errConceptRepo struct{}

func (errConceptRepo) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (errConceptRepo) GetByID(context.Context, string, string, string) (*conceptgraph.ConceptNode, error) {
	return nil, errors.New("boom")
}
func (errConceptRepo) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return nil, nil
}
func (errConceptRepo) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

// --- campaign_question_terminal error / reason arms ------------------------

// cqBridgeRepo is a controllable campaignquestion.Repository: getErr/saveErr
// let a test force the resolve / persist error arms of the terminal bridge.
type cqBridgeRepo struct {
	byAssist map[string]*cq.QuestionSet
	getErr   error
	saveErr  error
	saved    []*cq.QuestionSet
}

func (r *cqBridgeRepo) GetByConceptRung(context.Context, string, string, string, int) (*cq.QuestionSet, error) {
	return nil, nil
}
func (r *cqBridgeRepo) GetByAssistID(_ context.Context, _, assistID string) (*cq.QuestionSet, error) {
	return r.byAssist[assistID], r.getErr
}
func (r *cqBridgeRepo) CountRequestedOn(context.Context, string, string, cq.RequestOrigin, time.Time) (int, error) {
	return 0, nil
}
func (r *cqBridgeRepo) Save(_ context.Context, s *cq.QuestionSet) error {
	r.saved = append(r.saved, s)
	return r.saveErr
}

func TestCampaignTerminal_CompletedEmptyAssistIDErrors(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqBridgeRepo{})
	err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{AssistID: "  "})
	if err == nil || !strings.Contains(err.Error(), "empty assist_id") {
		t.Fatalf("HandleCompleted with blank assist_id = %v, want empty assist_id error", err)
	}
}

func TestCampaignTerminal_CompletedEmptyTenantErrors(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqBridgeRepo{byAssist: map[string]*cq.QuestionSet{"a1": cqRequestedSet(t, "a1")}})
	err := sub.HandleCompleted(context.Background(), events.Envelope{}, ProofingCompletedPayload{AssistID: "a1"})
	if err == nil || !strings.Contains(err.Error(), "empty tenant_id") {
		t.Fatalf("HandleCompleted with empty tenant = %v, want empty tenant_id error", err)
	}
}

func TestCampaignTerminal_CompletedGetErrorNACKs(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqBridgeRepo{getErr: errors.New("db down")})
	err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{AssistID: "a1"})
	if err == nil || !strings.Contains(err.Error(), "resolve assist_id") {
		t.Fatalf("HandleCompleted with get error = %v, want resolve error", err)
	}
}

func TestCampaignTerminal_CompletedSaveErrorNACKs(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqBridgeRepo{
		byAssist: map[string]*cq.QuestionSet{"a1": cqRequestedSet(t, "a1")},
		saveErr:  errors.New("write failed"),
	})
	err := sub.HandleCompleted(context.Background(), cqEnv(), ProofingCompletedPayload{
		AssistID: "a1", CandidatePayloadJSON: `{"candidate":"x"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "persist ready") {
		t.Fatalf("HandleCompleted with save error = %v, want persist ready error", err)
	}
}

func TestCampaignTerminal_RefusedEmptyReasonDefaults(t *testing.T) {
	repo := &cqBridgeRepo{byAssist: map[string]*cq.QuestionSet{"a1": cqRequestedSet(t, "a1")}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{AssistID: "a1", RefusalReason: "  "})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if len(repo.saved) == 0 {
		t.Fatal("no set saved")
	}
	if got := repo.saved[len(repo.saved)-1].FailureReason; got != "qgen batch refused" {
		t.Fatalf("FailureReason = %q, want %q", got, "qgen batch refused")
	}
}

func TestCampaignTerminal_RefusedAppendsUserFacingMessage(t *testing.T) {
	repo := &cqBridgeRepo{byAssist: map[string]*cq.QuestionSet{"a1": cqRequestedSet(t, "a1")}}
	sub := NewCampaignQuestionTerminalSubscriber(repo)
	err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{
		AssistID: "a1", RefusalReason: "crew rejected", UserFacingMessage: "try again",
	})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if got := repo.saved[len(repo.saved)-1].FailureReason; got != "crew rejected: try again" {
		t.Fatalf("FailureReason = %q, want %q", got, "crew rejected: try again")
	}
}

func TestCampaignTerminal_RefusedSaveErrorNACKs(t *testing.T) {
	sub := NewCampaignQuestionTerminalSubscriber(&cqBridgeRepo{
		byAssist: map[string]*cq.QuestionSet{"a1": cqRequestedSet(t, "a1")},
		saveErr:  errors.New("write failed"),
	})
	err := sub.HandleRefused(context.Background(), cqEnv(), ProofingRefusedPayload{AssistID: "a1", RefusalReason: "no"})
	if err == nil || !strings.Contains(err.Error(), "persist failed") {
		t.Fatalf("HandleRefused with save error = %v, want persist failed error", err)
	}
}

// --- proofing_test_terminal error / reason arms ----------------------------

// ptUpdateErrRepo delegates every read to an inmem repo but forces an error on
// Update, exercising the persist-error arm of the proofing terminal bridge.
type ptUpdateErrRepo struct {
	inner *inmem.ProofingTestRepo
	err   error
}

func (r *ptUpdateErrRepo) Create(ctx context.Context, p *pt.ProofingTest) error {
	return r.inner.Create(ctx, p)
}
func (r *ptUpdateErrRepo) GetByID(ctx context.Context, a, b, c string) (*pt.ProofingTest, error) {
	return r.inner.GetByID(ctx, a, b, c)
}
func (r *ptUpdateErrRepo) GetByAssistID(ctx context.Context, a, b string) (*pt.ProofingTest, error) {
	return r.inner.GetByAssistID(ctx, a, b)
}
func (r *ptUpdateErrRepo) ListByLearner(ctx context.Context, a, b, c string) ([]*pt.ProofingTest, error) {
	return r.inner.ListByLearner(ctx, a, b, c)
}
func (r *ptUpdateErrRepo) Update(context.Context, *pt.ProofingTest) error { return r.err }

func TestTerminal_CompletedUpdateErrorNACKs(t *testing.T) {
	repo := inmem.NewProofingTestRepo()
	row, err := pt.NewRequested(pt.NewRequestedInput{
		TenantID: ptTenant, LearnerGCID: ptLearner,
		GoalID: "33333333-3333-4333-8333-333333333333", CompanionID: "44444444-4444-4444-8444-444444444444",
		TargetEdges: []pt.TargetEdge{{ConceptID: "55555555-5555-4555-8555-555555555555", Key: "k", Title: "t", Intent: "remediate"}},
		Now:         time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewRequested: %v", err)
	}
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wrapped := &ptUpdateErrRepo{inner: repo, err: errors.New("update down")}
	sub := NewProofingTestTerminalSubscriber(wrapped, &terminalManaFake{})
	err = sub.HandleCompleted(context.Background(), terminalEnv(row.AssistID), ProofingCompletedPayload{
		AssistID: row.AssistID, CandidatePayloadJSON: composedPayload,
	})
	if err == nil || !strings.Contains(err.Error(), "persist ready") {
		t.Fatalf("HandleCompleted with update error = %v, want persist ready error", err)
	}
}

func TestTerminal_ResolveRowEmptyAssistIDErrors(t *testing.T) {
	_, _, mana, row := seedTerminal(t)
	sub := NewProofingTestTerminalSubscriber(inmem.NewProofingTestRepo(), mana)
	err := sub.HandleCompleted(context.Background(), terminalEnv(row.AssistID), ProofingCompletedPayload{AssistID: "  "})
	if err == nil || !strings.Contains(err.Error(), "empty assist_id") {
		t.Fatalf("HandleCompleted with blank assist_id = %v, want empty assist_id error", err)
	}
}

func TestTerminal_ResolveRowEmptyTenantErrors(t *testing.T) {
	_, _, mana, row := seedTerminal(t)
	sub := NewProofingTestTerminalSubscriber(inmem.NewProofingTestRepo(), mana)
	err := sub.HandleCompleted(context.Background(), events.Envelope{}, ProofingCompletedPayload{AssistID: row.AssistID})
	if err == nil || !strings.Contains(err.Error(), "empty tenant_id") {
		t.Fatalf("HandleCompleted with empty tenant = %v, want empty tenant_id error", err)
	}
}

func TestTerminal_RefusedEmptyReasonDefaults(t *testing.T) {
	sub, repo, _, row := seedTerminal(t)
	err := sub.HandleRefused(context.Background(), terminalEnv(row.AssistID), ProofingRefusedPayload{AssistID: row.AssistID, RefusalReason: ""})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if got.Status != pt.StatusFailed || !strings.Contains(got.FailureReason, "qgen batch refused") {
		t.Fatalf("row status=%q reason=%q, want failed + default reason", got.Status, got.FailureReason)
	}
}

func TestTerminal_RefusedAppendsUserFacingMessage(t *testing.T) {
	sub, repo, _, row := seedTerminal(t)
	err := sub.HandleRefused(context.Background(), terminalEnv(row.AssistID), ProofingRefusedPayload{
		AssistID: row.AssistID, RefusalReason: "guardrail_block", UserFacingMessage: "see notes",
	})
	if err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	got, _ := repo.GetByAssistID(context.Background(), ptTenant, row.AssistID)
	if !strings.Contains(got.FailureReason, "guardrail_block: see notes") {
		t.Fatalf("failure reason = %q, want appended user message", got.FailureReason)
	}
}

// --- goal_graduation graduate / progress error arms ------------------------

// failingPub is an events.Publisher that always errors, forcing the publish
// arms of graduate/progress.
type failingPub struct{}

func (failingPub) Publish(string, events.Envelope, map[string]any) error {
	return errors.New("publish down")
}

func TestGoalGraduation_GraduateUpdateErrorNACKs(t *testing.T) {
	g := ggActiveGoal("g1", []string{"Single Responsibility", "Open Closed"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}, updateErr: errors.New("db down")}
	lister := &ggFakeGrownLister{keys: []string{"single-responsibility", "open-closed"}}
	err := ggSub(repo, lister, events.NewInMemoryPublisher()).Handle(context.Background(), ggEnv(), ggPayload())
	if err == nil || !strings.Contains(err.Error(), "persist graduated goal") {
		t.Fatalf("Handle with graduate update error = %v, want persist graduated goal error", err)
	}
}

func TestGoalGraduation_GraduatePublishErrorNACKs(t *testing.T) {
	g := ggActiveGoal("g1", []string{"Single Responsibility", "Open Closed"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"single-responsibility", "open-closed"}}
	err := ggSub(repo, lister, failingPub{}).Handle(context.Background(), ggEnv(), ggPayload())
	if err == nil || !strings.Contains(err.Error(), "publish goal.graduated") {
		t.Fatalf("Handle with graduate publish error = %v, want publish goal.graduated error", err)
	}
}

func TestGoalGraduation_ProgressUpdateErrorNACKs(t *testing.T) {
	g := ggActiveGoal("g2", []string{"a", "b", "c"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}, updateErr: errors.New("db down")}
	lister := &ggFakeGrownLister{keys: []string{"a"}}
	err := ggSub(repo, lister, events.NewInMemoryPublisher()).Handle(context.Background(), ggEnv(), ggPayload())
	if err == nil || !strings.Contains(err.Error(), "persist goal progress") {
		t.Fatalf("Handle with progress update error = %v, want persist goal progress error", err)
	}
}

func TestGoalGraduation_ProgressPublishErrorNACKs(t *testing.T) {
	g := ggActiveGoal("g2", []string{"a", "b", "c"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"a"}}
	err := ggSub(repo, lister, failingPub{}).Handle(context.Background(), ggEnv(), ggPayload())
	if err == nil || !strings.Contains(err.Error(), "publish goal.progress_updated") {
		t.Fatalf("Handle with progress publish error = %v, want publish goal.progress_updated error", err)
	}
}

func TestGoalGraduation_GraduateProtocolTargetRefPublishesTarget(t *testing.T) {
	g := ggActiveGoal("g1", []string{"Single Responsibility", "Open Closed"}, 0)
	ref := "https://chora/curriculum/protocols/theorem-proving"
	g.ChoraTargetRef = &ref
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"single-responsibility", "open-closed"}}
	pub := events.NewInMemoryPublisher()
	if err := ggSub(repo, lister, pub).Handle(context.Background(), ggEnv(), ggPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	if got := evs[0].Payload["chora_target_ref"]; got != ref {
		t.Fatalf("chora_target_ref = %v, want %q", got, ref)
	}
}
