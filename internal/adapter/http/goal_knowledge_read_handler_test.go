// goal_knowledge_read_handler_test.go — CHO-2118 sub-phase B, the read path +
// request emitter.
//
// The invariants worth breaking a build over:
//   - CLAIM-THEN-PUBLISH. A failed claim must NEVER emit a request (that is how
//     N tab reads become N LLM calls).
//   - No signal / no Companion ⇒ the model is NEVER called.
//   - A cache hit costs ZERO LLM calls, no matter how often the tab re-renders.
//   - The published content_hash is the hash of the SAME view that was served.
//   - traceparent is never blank (a blank one silently fails envelope validation).
//   - No English copy in the response — status only; chora-web renders the i18n.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

const (
	gkTenant = "11111111-1111-1111-1111-111111111111"
	gkGCID   = "22222222-2222-2222-2222-222222222222"
	gkFam    = "33333333-3333-3333-3333-333333333333"
	gkGoal   = "44444444-4444-4444-4444-444444444444"
)

// --- fakes ---

type gkFakeGoals struct {
	g   *goal.Goal
	err error
}

func (f gkFakeGoals) Create(context.Context, *goal.Goal) error { return nil }
func (f gkFakeGoals) GetByID(_ context.Context, _, _, _ string) (*goal.Goal, error) {
	return f.g, f.err
}
func (f gkFakeGoals) ListByLearner(_ context.Context, _, _ string) ([]*goal.Goal, error) {
	return nil, nil
}
func (f gkFakeGoals) Update(context.Context, *goal.Goal) error { return nil }

// gkFakeCache records the ORDER of writes so claim-then-publish is provable.
type gkFakeCache struct {
	row       *fgk.GoalKnowledge
	findErr   error
	upsertErr error
	upserts   []*fgk.GoalKnowledge
	seq       *[]string
}

func (c *gkFakeCache) FindByGoal(_ context.Context, _, _, _, _ string) (*fgk.GoalKnowledge, error) {
	return c.row, c.findErr
}

func (c *gkFakeCache) Upsert(_ context.Context, k *fgk.GoalKnowledge) error {
	if c.upsertErr != nil {
		return c.upsertErr
	}
	c.upserts = append(c.upserts, k)
	if c.seq != nil {
		*c.seq = append(*c.seq, "upsert")
	}
	return nil
}

type gkFakeViews struct {
	view companionmind.GoalScopedView
	err  error
}

func (v gkFakeViews) AssembleForGoal(_ context.Context, _, _, _ string, _ *goal.Goal) (companionmind.GoalScopedView, error) {
	return v.view, v.err
}

type gkFakePublisher struct {
	events []events.Event
	err    error
	seq    *[]string
}

func (p *gkFakePublisher) Publish(topic string, env events.Envelope, payload map[string]any) error {
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, events.Event{Topic: topic, Envelope: env, Payload: payload})
	if p.seq != nil {
		*p.seq = append(*p.seq, "publish")
	}
	return nil
}

// --- helpers ---

func gkGoalWithCompanion() *goal.Goal {
	f := gkFam
	return &goal.Goal{GoalID: gkGoal, TenantID: gkTenant, LearnerGCID: gkGCID, AttachedCompanionID: &f, ConceptSet: []string{"fractions"}}
}

// a view with real signal (a shaky concept) — the model has something true to say.
func gkViewWithSignal() companionmind.GoalScopedView {
	return companionmind.AssembleGoalScopedView(companionmind.GoalScopedInput{
		GoalID:          gkGoal,
		CompanionID:     gkFam,
		CompanionName:   "Ember",
		GoalConceptKeys: []string{"fractions"},
		Weaknesses: []companionmind.WeaknessSignal{
			{ConceptKey: "fractions", ConceptLabel: "comparing fractions", Strength: 0.8, Active: true},
		},
	})
}

func gkEmptyView() companionmind.GoalScopedView {
	return companionmind.AssembleGoalScopedView(companionmind.GoalScopedInput{GoalID: gkGoal, CompanionID: gkFam})
}

type gkHarness struct {
	ext  *ExtServer
	pub  *gkFakePublisher
	repo *gkFakeCache
	seq  []string
}

func newGKHarness(t *testing.T, g *goal.Goal, view companionmind.GoalScopedView, row *fgk.GoalKnowledge) *gkHarness {
	t.Helper()
	h := &gkHarness{}
	h.pub = &gkFakePublisher{seq: &h.seq}
	h.repo = &gkFakeCache{row: row, seq: &h.seq}
	h.ext = &ExtServer{
		Goals:     gkFakeGoals{g: g},
		Publisher: h.pub,
		GoalKnowledgeRead: &GoalKnowledgeReadService{
			Cache:      h.repo,
			Views:      gkFakeViews{view: view},
			Floor:      fgk.DefaultRegenFloor,
			MaxAge:     fgk.DefaultMaxAge,
			PendingTTL: fgk.DefaultPendingTTL,
		},
	}
	return h
}

func (h *gkHarness) get(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/goals/"+gkGoal+"/knowledge", nil)
	r.Header.Set("X-Tenant-Id", gkTenant)
	r.Header.Set("gcid", gkGCID)
	w := httptest.NewRecorder()
	h.ext.handleMeGoalByID(w, r)
	return w
}

func decodeGK(t *testing.T, w *httptest.ResponseRecorder) goalKnowledgeResp {
	t.Helper()
	var got goalKnowledgeResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return got
}

// --- the anti-stampede + cost invariants ---

// THE invariant: a failed claim must never emit a request. Publishing without a
// durable claim means every concurrent tab read fires its own LLM call.
func TestGoalKnowledge_ClaimFailure_NeverPublishes(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	h.repo.upsertErr = errors.New("db down")

	w := h.get(t)

	if len(h.pub.events) != 0 {
		t.Fatalf("published %d events after a FAILED claim; want 0 — an unclaimed request is an unbounded LLM call", len(h.pub.events))
	}
	// The tier-1 block is real and assembled; a cache-claim failure must not blank
	// the learner's tab (the story's fail-soft AC: never an error card).
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 — tier-1 still renders when the claim fails", w.Code)
	}
}

// Claim BEFORE publish, always — proven by write order, not by reading the code.
func TestGoalKnowledge_ClaimsBeforeItPublishes(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)

	h.get(t)

	if len(h.seq) != 2 || h.seq[0] != "upsert" || h.seq[1] != "publish" {
		t.Fatalf("write order = %v; want [upsert publish] — the claim IS what stops the stampede", h.seq)
	}
	if len(h.repo.upserts) != 1 || h.repo.upserts[0].RequestedAt == nil {
		t.Fatal("the claimed row must carry RequestedAt (the in-flight marker)")
	}
}

// A cache hit costs ZERO LLM calls, however often the tab re-renders (story AC).
func TestGoalKnowledge_FreshRow_ServesCachedTextAndNeverCallsTheModel(t *testing.T) {
	now := time.Now().UTC()
	row := &fgk.GoalKnowledge{
		ID: "r1", TenantID: gkTenant, LearnerGCID: gkGCID, CompanionID: gkFam, GoalID: gkGoal,
		SynthesisText: "You have been chipping away at fractions.", Status: fgk.StatusFresh,
		GeneratedAt: &now,
	}
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), row)

	for i := 0; i < 3; i++ { // re-render the tab
		w := h.get(t)
		got := decodeGK(t, w)
		if got.Reflection.Status != fgk.ReadStatusFresh {
			t.Errorf("status = %q; want fresh", got.Reflection.Status)
		}
		if got.Reflection.Text != row.SynthesisText {
			t.Errorf("text = %q; want the cached reflection", got.Reflection.Text)
		}
	}
	if len(h.pub.events) != 0 {
		t.Fatalf("published %d synthesis requests on a cache hit; want 0", len(h.pub.events))
	}
}

// No Companion bound ⇒ nothing to reflect. Serve tier-1, status none, no LLM.
func TestGoalKnowledge_NoCompanionBound_ServesTier1AndRequestsNothing(t *testing.T) {
	g := gkGoalWithCompanion()
	g.AttachedCompanionID = nil
	h := newGKHarness(t, g, gkViewWithSignal(), nil)

	w := h.get(t)
	got := decodeGK(t, w)

	if got.Reflection.Status != fgk.ReadStatusNone {
		t.Errorf("status = %q; want none — no Companion is bound", got.Reflection.Status)
	}
	if len(h.pub.events) != 0 {
		t.Errorf("published %d events with no Companion bound; want 0", len(h.pub.events))
	}
	if len(h.repo.upserts) != 0 {
		t.Errorf("claimed the cache %d times with no Companion bound; want 0", len(h.repo.upserts))
	}
	// tier-1 still renders.
	if got.GoalID != gkGoal {
		t.Errorf("goalId = %q; the deterministic block must still be served", got.GoalID)
	}
}

// No signal ⇒ the model is never called: it could only invent a memory.
func TestGoalKnowledge_NoSignal_NeverCallsTheModel(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkEmptyView(), nil)

	w := h.get(t)
	got := decodeGK(t, w)

	if got.Reflection.Status != fgk.ReadStatusNone {
		t.Errorf("status = %q; want none (honest empty state)", got.Reflection.Status)
	}
	if got.Reflection.Text != "" {
		t.Errorf("text = %q; want empty — never a fabricated reflection", got.Reflection.Text)
	}
	if len(h.pub.events) != 0 {
		t.Fatalf("published %d events with no signal; want 0", len(h.pub.events))
	}
}

// An in-flight claim suppresses a second request (N tab reads, 1 LLM call).
func TestGoalKnowledge_InFlightClaim_DoesNotRepublish(t *testing.T) {
	just := time.Now().UTC().Add(-time.Minute)
	row := &fgk.GoalKnowledge{
		ID: "r1", TenantID: gkTenant, LearnerGCID: gkGCID, CompanionID: gkFam, GoalID: gkGoal,
		Status: fgk.StatusPending, RequestedAt: &just,
	}
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), row)

	got := decodeGK(t, h.get(t))

	if got.Reflection.Status != fgk.ReadStatusReflecting {
		t.Errorf("status = %q; want reflecting", got.Reflection.Status)
	}
	if len(h.pub.events) != 0 {
		t.Fatalf("published %d events while a claim was in flight; want 0", len(h.pub.events))
	}
}

// Serve-stale-while-regen: the learner keeps the last good text AND a regen fires.
func TestGoalKnowledge_StaleRow_ServesLastGoodTextAndRegenerates(t *testing.T) {
	old := time.Now().UTC().Add(-24 * time.Hour) // older than the 6h floor
	inval := time.Now().UTC().Add(-time.Hour)
	row := &fgk.GoalKnowledge{
		ID: "r1", TenantID: gkTenant, LearnerGCID: gkGCID, CompanionID: gkFam, GoalID: gkGoal,
		SynthesisText: "last good reflection", Status: fgk.StatusStale,
		GeneratedAt: &old, InvalidatedAt: &inval, InvalidationReason: fgk.ReasonWeaknessGrown,
	}
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), row)

	got := decodeGK(t, h.get(t))

	if got.Reflection.Text != "last good reflection" {
		t.Errorf("text = %q; want the last good text (serve-stale-while-regen, never a blank)", got.Reflection.Text)
	}
	if got.Reflection.Status != fgk.ReadStatusReflecting {
		t.Errorf("status = %q; want reflecting", got.Reflection.Status)
	}
	if len(h.pub.events) != 1 {
		t.Fatalf("published %d events; want exactly 1 regen", len(h.pub.events))
	}
}

// --- the wire contract ---

// The published request must describe the view that was ACTUALLY served, and the
// envelope must be complete — a blank traceparent silently fails envelope
// validation downstream.
func TestGoalKnowledge_PublishedRequest_IsHonestAndComplete(t *testing.T) {
	view := gkViewWithSignal()
	h := newGKHarness(t, gkGoalWithCompanion(), view, nil)

	h.get(t)

	if len(h.pub.events) != 1 {
		t.Fatalf("published %d events; want 1", len(h.pub.events))
	}
	ev := h.pub.events[0]
	if ev.Topic != events.TopicGoalKnowledgeSynthesisRequested {
		t.Errorf("topic = %q; want %q", ev.Topic, events.TopicGoalKnowledgeSynthesisRequested)
	}
	// content_hash must be the hash of the served view, not a re-derived one.
	if got := ev.Payload["content_hash"]; got != view.ContentHash() {
		t.Errorf("content_hash = %v; want the served view's hash %s — otherwise the completion's drift guard refuses every synthesis", got, view.ContentHash())
	}
	if got := ev.Payload["familiar_id"]; got != gkFam {
		t.Errorf("companion_id = %v; want %s", got, gkFam)
	}
	if got := ev.Payload["goal_id"]; got != gkGoal {
		t.Errorf("goal_id = %v; want %s", got, gkGoal)
	}
	// The fog orchestrator hard-requires these (its _REQUIRED tuple).
	for _, k := range []string{"tenant_id", "learner_gcid", "familiar_id", "goal_id", "content_hash"} {
		if v, ok := ev.Payload[k]; !ok || v == "" {
			t.Errorf("payload[%q] = %v; the synthesiser rejects a request missing it", k, v)
		}
	}
	if ev.Envelope.Traceparent == "" {
		t.Error("envelope traceparent is BLANK — envelope validation fails silently downstream")
	}
	if ev.Envelope.TenantID != gkTenant || ev.Envelope.GCID != gkGCID {
		t.Errorf("envelope identity = (%s, %s); want (%s, %s)", ev.Envelope.TenantID, ev.Envelope.GCID, gkTenant, gkGCID)
	}
	if ev.Envelope.IdempotencyKey == "" {
		t.Error("envelope idempotency_key is blank — the completion dedupes on it")
	}
}

// The emitted event must satisfy the PLATFORM's envelope contract, not merely a
// fake's expectations — so this one publishes through the real validating
// publisher (events.validateEnvelope, which the durable outbox publisher mirrors
// field-for-field). It is the guard against the trap this repo has hit before: a
// blank traceparent is REJECTED at publish, so the request never reaches the
// synthesiser and the reflection silently never generates.
func TestGoalKnowledge_EmittedEvent_PassesTheRealEnvelopeValidator(t *testing.T) {
	real := events.NewInMemoryPublisher()
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	h.ext.Publisher = real

	if w := h.get(t); w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}

	// The real publisher records an event ONLY if the envelope validated. An
	// empty tail here means the platform rejected what we emitted.
	got := real.Events()
	if len(got) != 1 {
		t.Fatalf("the real (validating) publisher recorded %d events; want 1 — the envelope was REJECTED by the platform contract", len(got))
	}
	if got[0].Topic != events.TopicGoalKnowledgeSynthesisRequested {
		t.Errorf("topic = %q; want %q", got[0].Topic, events.TopicGoalKnowledgeSynthesisRequested)
	}
}

// A publish failure after a successful claim must not break the read: the claim
// self-expires via pendingTTL, so the learner is never stranded on "reflecting…".
func TestGoalKnowledge_PublishFailure_StillServesTheRead(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	h.pub.err = errors.New("pubsub down")

	w := h.get(t)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — a failed publish must never become an error card", w.Code)
	}
	if len(h.repo.upserts) != 1 {
		t.Error("the claim should still have been written")
	}
}

// --- guards ---

func TestGoalKnowledge_Guards(t *testing.T) {
	t.Run("essential deps nil ⇒ 503, never a panic", func(t *testing.T) {
		ext := &ExtServer{Goals: gkFakeGoals{g: gkGoalWithCompanion()}} // GoalKnowledgeRead nil
		r := httptest.NewRequest(http.MethodGet, "/v1/me/goals/"+gkGoal+"/knowledge", nil)
		r.Header.Set("X-Tenant-Id", gkTenant)
		r.Header.Set("gcid", gkGCID)
		w := httptest.NewRecorder()
		ext.handleMeGoalByID(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d; want 503", w.Code)
		}
	})

	t.Run("wrong method ⇒ 405", func(t *testing.T) {
		h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
		r := httptest.NewRequest(http.MethodPost, "/v1/me/goals/"+gkGoal+"/knowledge", nil)
		r.Header.Set("X-Tenant-Id", gkTenant)
		r.Header.Set("gcid", gkGCID)
		w := httptest.NewRecorder()
		h.ext.handleMeGoalByID(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d; want 405", w.Code)
		}
	})

	t.Run("missing session context ⇒ 400", func(t *testing.T) {
		h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
		r := httptest.NewRequest(http.MethodGet, "/v1/me/goals/"+gkGoal+"/knowledge", nil)
		w := httptest.NewRecorder()
		h.ext.handleMeGoalByID(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400", w.Code)
		}
	})

	t.Run("absent goal ⇒ 404", func(t *testing.T) {
		h := newGKHarness(t, nil, gkViewWithSignal(), nil)
		if w := h.get(t); w.Code != http.StatusNotFound {
			t.Errorf("status = %d; want 404", w.Code)
		}
	})

	t.Run("another learner's goal ⇒ 404, never revealed", func(t *testing.T) {
		g := gkGoalWithCompanion()
		g.LearnerGCID = "99999999-9999-9999-9999-999999999999"
		h := newGKHarness(t, g, gkViewWithSignal(), nil)
		if w := h.get(t); w.Code != http.StatusNotFound {
			t.Errorf("status = %d; want 404", w.Code)
		}
	})

	t.Run("view assembly failure ⇒ 500, never a half-view", func(t *testing.T) {
		h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
		h.ext.GoalKnowledgeRead.Views = gkFakeViews{err: errors.New("db down")}
		if w := h.get(t); w.Code != http.StatusInternalServerError {
			t.Errorf("status = %d; want 500", w.Code)
		}
	})
}

// The backend ships DATA, not copy. The disclosure line, the "reflecting…"
// marker and the honest "no memory yet" are i18n values in chora-web; a Go
// string here would be an untranslatable second source of user-facing wording.
func TestGoalKnowledge_ResponseCarriesNoEnglishCopy(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkEmptyView(), nil)

	body := h.get(t).Body.String()

	for _, forbidden := range []string{"no memory yet", "reflecting…", "reflection of", "your companion"} {
		if containsFold(body, forbidden) {
			t.Errorf("response body carries user-facing English copy (%q) — that belongs in en.json, not in Go:\n%s", forbidden, body)
		}
	}
}

func containsFold(haystack, needle string) bool {
	hl, nl := []rune(haystack), []rune(needle)
	if len(nl) == 0 || len(hl) < len(nl) {
		return false
	}
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(nl) <= len(hl); i++ {
		match := true
		for j := range nl {
			if lower(hl[i+j]) != lower(nl[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
