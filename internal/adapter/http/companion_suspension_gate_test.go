// companion_suspension_gate_test.go: the three ADR-252 / ADR-254 D11 call
// sites of the ADVISORY suspension projection.
//
//  1. POST /v1/me/companions/{id}/chat refuses 403 COMPANION_SUSPENDED before
//     the mana pre-check, the session lookup and any SSE header.
//  2. GET /v1/me/goals/{id}/knowledge skips the ADR-235 claim AND the publish
//     while paused (ADR-252 Q5) and renders the `paused` status.
//  3. GET /v1/me/companions/{id} renders companion_status {paused, reason,
//     scope}.
//
// The invariant these pin: the projection is ADVISORY. chora-model-gateway is
// the control (ADR-252 D1, uncached, fail-closed). A Status() ERROR therefore
// means UNKNOWN and the request proceeds to the gateway; it must never become
// a second, local, fail-closed control, and it must never be coerced into a
// silent "not paused" without a loud log.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ----------------------------------------------------------------------------
// Fakes
// ----------------------------------------------------------------------------

// suspAsk records one advisory question.
type suspAsk struct {
	TenantID   string
	ActionCode string
}

// fakeSuspensionAdvisor is a scripted companion.SuspensionAdvisor. It records
// every question so a test can pin WHICH question a call site asks (the chat
// turn asks about its own action code; the reflection, being mana-exempt, asks
// the whole-companion question with an empty code).
type fakeSuspensionAdvisor struct {
	status companion.SuspensionStatus
	err    error
	asks   []suspAsk
}

func (f *fakeSuspensionAdvisor) Status(_ context.Context, tenantID, actionCode string) (companion.SuspensionStatus, error) {
	f.asks = append(f.asks, suspAsk{TenantID: tenantID, ActionCode: actionCode})
	if f.err != nil {
		return companion.SuspensionStatus{}, f.err
	}
	return f.status, nil
}

// pausedAdvisor is the common "platform, every companion turn" containment.
func pausedAdvisor() *fakeSuspensionAdvisor {
	return &fakeSuspensionAdvisor{status: companion.SuspensionStatus{
		Paused: true,
		Reason: "vendor incident 2026-08-23",
		Scope:  companion.SuspensionScopePlatform,
	}}
}

// seedSuspensionProjection builds the REAL in-memory projection from real
// events, so the wiring is exercised end to end and not only against a fake.
func seedSuspensionProjection(t *testing.T, evs ...companion.SuspensionChanged) *inmem.CompanionSuspensionProjection {
	t.Helper()
	p := inmem.NewCompanionSuspensionProjection()
	for _, ev := range evs {
		if _, err := p.Apply(context.Background(), ev); err != nil {
			t.Fatalf("seed Apply(%s v%d): %v", ev.SuspensionID, ev.Version, err)
		}
	}
	return p
}

func platformSuspension(version int64, engaged bool) companion.SuspensionChanged {
	return companion.SuspensionChanged{
		EventID:      "01957c8c-9999-7000-aaaa-00000000000" + string(rune('0'+version)),
		SuspensionID: "01957c8c-8888-7000-aaaa-888888888888",
		Scope:        companion.SuspensionScopePlatform,
		SkillKey:     "",
		Engaged:      engaged,
		Version:      version,
		Reason:       "vendor incident 2026-08-23",
		ActorGCID:    "01957c8c-7777-7000-aaaa-777777777777",
		ChangedAt:    time.Date(2026, 8, 23, 5, int(version), 0, 0, time.UTC),
	}
}

// ----------------------------------------------------------------------------
// 1. Chat turn
// ----------------------------------------------------------------------------

// A paused companion is refused BEFORE any work: no engine call, no session
// write, and a JSON body (never an SSE stream, which could not carry a 403).
func TestCompanionChat_Suspended_Returns403BeforeAnyTurnWork(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	adv := pausedAdvisor()
	srv.CompanionSuspension = adv

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got == "text/event-stream" {
		t.Errorf("Content-Type = %q; the refusal must be JSON, never an opened SSE stream", got)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	errMap, _ := body["error"].(map[string]any)
	if errMap == nil {
		t.Fatalf("body.error missing; body=%v", body)
	}
	if errMap["code"] != companion.ErrCodeCompanionSuspended {
		t.Errorf("error.code = %v; want %s", errMap["code"], companion.ErrCodeCompanionSuspended)
	}
	if engine.calls != 0 {
		t.Errorf("engine.StreamChat called %d times; want 0 while paused", engine.calls)
	}
	if len(repo.saves) != 0 {
		t.Errorf("chat session written %d times; want 0 while paused", len(repo.saves))
	}
	if len(adv.asks) == 0 {
		t.Fatal("the advisor was never asked")
	}
	if adv.asks[0].ActionCode != companion.ChatTurnActionCodeForTier(resolveManaTier(srv, testTenant, testGCID)) {
		t.Errorf("chat asked about action %q; want the turn's own action code", adv.asks[0].ActionCode)
	}
	if adv.asks[0].TenantID != testTenant {
		t.Errorf("chat asked for tenant %q; want %q", adv.asks[0].TenantID, testTenant)
	}
}

// The gate runs BEFORE the mana pre-check: a paused companion with an empty
// wallet is 403 (containment), not 402 (upsell). Charging or upselling a turn
// that can never run would be dishonest.
func TestCompanionChat_Suspended_BeatsTheManaPreCheck(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.ManaQuoter = &fakeManaQuoter{balance: companion.BalanceSnapshot{BalanceUnits: 0}}
	srv.CompanionSuspension = pausedAdvisor()

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (the pause outranks the 402 upsell), body=%s", w.Code, w.Body.String())
	}
}

// ADVISORY, not a control: a projection read error is UNKNOWN, so the turn
// proceeds and the gateway decides. A local fail-closed here would turn one
// broken pg read into a total chat outage the operator never asked for.
func TestCompanionChat_AdvisoryReadError_ProceedsToTheGateway(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.CompanionSuspension = &fakeSuspensionAdvisor{err: errors.New("projection unavailable")}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code == http.StatusForbidden {
		t.Fatalf("status = 403 on an advisory READ ERROR; the projection must not become a second control")
	}
}

// A LIFT is the same suspension id at a higher version with engaged=false.
// After it the turn runs again: proven through the real projection, not a fake.
func TestCompanionChat_ReleasedSuspension_NoLongerPauses(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.CompanionSuspension = seedSuspensionProjection(t,
		platformSuspension(1, true),
		platformSuspension(2, false),
	)

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code == http.StatusForbidden {
		t.Fatalf("status = 403 after the suspension was LIFTED; body=%s", w.Body.String())
	}
}

// An engaged platform suspension read through the real projection pauses the
// turn (the fake above pins the handler; this pins handler plus adapter).
func TestCompanionChat_EngagedSuspensionThroughTheRealProjection_Pauses(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)
	srv.CompanionSuspension = seedSuspensionProjection(t, platformSuspension(1, true))

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 through the real projection, body=%s", w.Code, w.Body.String())
	}
}

// No advisor wired (unit server) leaves the chat exactly as it was.
func TestCompanionChat_NoAdvisorWired_IsUnchanged(t *testing.T) {
	engine := &fakeChatEngine{}
	repo := newFakeChatRepo()
	srv := NewServer()
	seedChatEngine(srv, engine, repo)

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hi",
	})

	if w.Code == http.StatusForbidden {
		t.Fatalf("status = 403 with NO advisor wired; the gate must be nil-safe")
	}
}

// ----------------------------------------------------------------------------
// 2. ADR-235 reflection (ADR-252 Q5)
// ----------------------------------------------------------------------------

// THE Q5 invariant: skip the claim AND the publish. The skip must happen
// BEFORE the claim, because the handler's own rule is "claim fails, do NOT
// publish"; turning a pause into a claim failure would leave the pause
// indistinguishable from a broken database.
func TestGoalKnowledge_Paused_SkipsTheClaimAndThePublish(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	adv := pausedAdvisor()
	h.ext.CompanionSuspension = adv

	w := h.get(t)

	if len(h.seq) != 0 {
		t.Fatalf("write/publish sequence = %v while paused; want [] (ADR-252 Q5: skip the claim AND the publish)", h.seq)
	}
	if len(h.pub.events) != 0 {
		t.Fatalf("published %d events while paused; want 0", len(h.pub.events))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (tier-1 still renders while paused)", w.Code)
	}
	got := decodeGK(t, w)
	if got.Reflection.Status != fgk.ReadStatusPaused {
		t.Errorf("reflection.status = %q; want %q alongside fresh / reflecting / none", got.Reflection.Status, fgk.ReadStatusPaused)
	}
}

// The reflection lane is mana-EXEMPT (no action code on the wire), so it asks
// the whole-companion question: only an all-skills suspension pauses it.
func TestGoalKnowledge_AsksTheWholeCompanionQuestion(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	adv := pausedAdvisor()
	h.ext.CompanionSuspension = adv

	h.get(t)

	if len(adv.asks) == 0 {
		t.Fatal("the advisor was never asked by the reflection read")
	}
	if adv.asks[0].ActionCode != "" {
		t.Errorf("reflection asked about action %q; want \"\" (the lane is mana-exempt, so only an all-skills suspension covers it)", adv.asks[0].ActionCode)
	}
	if adv.asks[0].TenantID != gkTenant {
		t.Errorf("reflection asked for tenant %q; want %q", adv.asks[0].TenantID, gkTenant)
	}
}

// Advisory again: a read error must not silence the reflection lane.
func TestGoalKnowledge_AdvisoryReadError_StillClaimsAndPublishes(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	h.ext.CompanionSuspension = &fakeSuspensionAdvisor{err: errors.New("projection unavailable")}

	h.get(t)

	if len(h.seq) != 2 || h.seq[0] != "upsert" || h.seq[1] != "publish" {
		t.Fatalf("write order = %v on an advisory READ ERROR; want [upsert publish] (unknown is not paused)", h.seq)
	}
}

// A lifted suspension restores the lane.
func TestGoalKnowledge_ReleasedSuspension_ClaimsAgain(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)
	h.ext.CompanionSuspension = seedSuspensionProjection(t,
		platformSuspension(1, true),
		platformSuspension(2, false),
	)

	h.get(t)

	if len(h.seq) != 2 || h.seq[0] != "upsert" || h.seq[1] != "publish" {
		t.Fatalf("write order = %v after the suspension was LIFTED; want [upsert publish]", h.seq)
	}
}

// ----------------------------------------------------------------------------
// 3. Companion profile
// ----------------------------------------------------------------------------

func TestGetCompanionInstance_Paused_RendersCompanionStatus(t *testing.T) {
	srv := NewServer()
	srv.CompanionSuspension = pausedAdvisor()
	created := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id, _ := createdBody["companion_id"].(string)

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	st, _ := resp["companion_status"].(map[string]any)
	if st == nil {
		t.Fatalf("companion_status missing from the profile; body=%s", w.Body.String())
	}
	if paused, _ := st["paused"].(bool); !paused {
		t.Errorf("companion_status.paused = %v; want true", st["paused"])
	}
	if st["reason"] != "vendor incident 2026-08-23" {
		t.Errorf("companion_status.reason = %v; want the operator reason", st["reason"])
	}
	if st["scope"] != string(companion.SuspensionScopePlatform) {
		t.Errorf("companion_status.scope = %v; want %q", st["scope"], companion.SuspensionScopePlatform)
	}
}

func TestGetCompanionInstance_NotPaused_RendersPausedFalseWithoutReason(t *testing.T) {
	srv := NewServer()
	srv.CompanionSuspension = inmem.NewCompanionSuspensionProjection()
	created := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id, _ := createdBody["companion_id"].(string)

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	st, _ := resp["companion_status"].(map[string]any)
	if st == nil {
		t.Fatalf("companion_status missing; the FE binds it unconditionally when the projection is wired")
	}
	if paused, _ := st["paused"].(bool); paused {
		t.Errorf("companion_status.paused = true with an empty projection; want false")
	}
	if _, ok := st["reason"]; ok {
		t.Errorf("companion_status.reason present while not paused; want it omitted")
	}
	if _, ok := st["scope"]; ok {
		t.Errorf("companion_status.scope present while not paused; want it omitted")
	}
}

// An advisory read error must not 500 the whole profile, and must not claim a
// pause it could not read.
func TestGetCompanionInstance_AdvisoryReadError_Still200AndNotPaused(t *testing.T) {
	srv := NewServer()
	srv.CompanionSuspension = &fakeSuspensionAdvisor{err: errors.New("projection unavailable")}
	created := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id, _ := createdBody["companion_id"].(string)

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an advisory read never fails the profile), body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	st, _ := resp["companion_status"].(map[string]any)
	if st == nil {
		t.Fatalf("companion_status missing on an advisory read error; want {\"paused\": false}")
	}
	if paused, _ := st["paused"].(bool); paused {
		t.Errorf("companion_status.paused = true on a READ ERROR; want false (unknown is never a claimed pause)")
	}
}

// No advisor wired (unit server): the field is omitted rather than asserting a
// state nothing measured.
func TestGetCompanionInstance_NoAdvisorWired_OmitsCompanionStatus(t *testing.T) {
	srv := NewServer()
	created := authedReq(t, srv, http.MethodPost, "/v1/me/companions",
		instanceCreateReq{Name: "Newton", Specialization: "math"})
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id, _ := createdBody["companion_id"].(string)

	w := authedReq(t, srv, http.MethodGet, "/v1/me/companions/"+id, nil)
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if _, ok := resp["companion_status"]; ok {
		t.Errorf("companion_status present with NO advisor wired; want it omitted (nothing measured it)")
	}
}
