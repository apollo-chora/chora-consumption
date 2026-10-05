// daily_dose_ai_test.go — GET /companion/daily-dose/ai (async AI enrichment).
//
// The dose hot path (/companion/daily-dose) returns the deterministic SM-2
// composition instantly; this endpoint runs the Companion greeting + Recommender
// picks to COMPLETION (generous timeout) so the gateway LLM calls finish + meter
// (mana umbrella) and the FE can weave the AI layer in async.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestDailyDoseAI_ReturnsGreetingAndPicks(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	rec := srv.RecommenderEngine.(*fakeDailyDoseRecommender)

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Greeting string   `json:"greeting"`
		AIPicks  []string `json:"ai_picks"`
		Degraded bool     `json:"degraded"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Greeting != "Welcome back, Phyllis — today's dose awaits." {
		t.Errorf("greeting = %q; want the engine greeting", resp.Greeting)
	}
	if len(resp.AIPicks) != 2 || resp.AIPicks[0] != "atom-rec-001" {
		t.Errorf("ai_picks = %v; want the recommender picks", resp.AIPicks)
	}
	if resp.Degraded {
		t.Errorf("degraded = true; want false when both engines succeed")
	}
	// The recommender ran to COMPLETION (the whole point — not cancelled).
	if rec.calls != 1 {
		t.Errorf("recommender calls = %d; want 1 (completed, not cancelled)", rec.calls)
	}
}

func TestDailyDoseAI_DegradesWhenEnginesAbsent(t *testing.T) {
	srv := NewServer() // no engines seeded
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (graceful degrade), body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Greeting string `json:"greeting"`
		Degraded bool   `json:"degraded"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Degraded {
		t.Errorf("degraded = false; want true when engines absent")
	}
	if resp.Greeting == "" {
		t.Errorf("greeting empty; want a templated fallback greeting")
	}
}

// The async /ai path must thread the per-Companion config (so the sidecar-less
// Companion agent resolves its instance → real greeting, not iteration_count:0)
// AND a topic_hint derived from the learner's active path (so the recommender
// has a topic to rank around). Both are the §2.1/§2.2 follow-ups to the
// recommender/moderation umbrella (2026-06-05).
func TestDailyDoseAI_ThreadsCompanionConfigAndTopicHint(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	mustRecordPathTopics(t, srv.ActivePathTopics, testTenant, testGCID, []string{"kubernetes"})
	srv.CompanionConfigJSONResolver = func(_ context.Context, tenantID, companionID, callerGCID string) (string, error) {
		return `{"companion_id":"` + companionID + `","specialization":"devops"}`, nil
	}
	fam := srv.CompanionEngine.(*fakeDailyDoseCompanion)
	rec := srv.RecommenderEngine.(*fakeDailyDoseRecommender)

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose/ai", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if fam.lastReq.CompanionConfigJSON == "" {
		t.Errorf("Greet request carried no CompanionConfigJSON; the sidecar-less agent will refuse the turn")
	}
	if rec.lastReq.TopicHint != "kubernetes" {
		t.Errorf("Recommend TopicHint = %q; want kubernetes (from active path topics)", rec.lastReq.TopicHint)
	}
}

// The daily-dose greet must use the learner's REAL companion instance id (the one
// CompanionInstances + the config resolver know), NOT pickCompanionID's stub
// canonical UUID — otherwise ResolveCompanionInstanceConfig returns
// "companion.instance: not found", no companion_config is injected, and the
// sidecar-less Companion agent refuses the greet (iteration_count:0, degraded).
// Observed live 2026-06-05 (companion=01957c8c-… not found).
func TestResolveLearnerCompanionID_PrefersRealInstance(t *testing.T) {
	srv := NewServer()
	srv.CompanionInstances = inmem.NewCompanionInstanceRepo()
	if err := srv.CompanionInstances.Create(context.Background(), &companion.Instance{
		CompanionID: "real-inst-7", TenantID: testTenant, OwnerGCID: testGCID,
		Name: "Newton", Specialization: "math", EvolutionTier: companion.EvolutionTier("apprentice"),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, 5); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if got := resolveLearnerCompanionID(context.Background(), srv, testTenant, testGCID); got != "real-inst-7" {
		t.Errorf("resolveLearnerCompanionID = %q; want the real instance id (not the stub canonical UUID)", got)
	}
}

func TestResolveLearnerCompanionID_FallsBackToStubWhenNoInstance(t *testing.T) {
	srv := NewServer() // no real instance for this learner
	if got := resolveLearnerCompanionID(context.Background(), srv, testTenant, testGCID); got == "" {
		t.Errorf("fallback must return a non-empty stub companion id")
	}
}

func TestDailyDoseAI_RequiresAuth(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	req := httptest.NewRequest(http.MethodGet, "/companion/daily-dose/ai", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (missing context)", w.Code)
	}
}
