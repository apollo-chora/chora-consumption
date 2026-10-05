package companiongoalknowledge

import (
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

const (
	tenant    = "11111111-1111-7111-8111-111111111111"
	learner   = "00000000-0000-7000-8000-000000001999"
	companion = "019f4283-b936-73ba-83be-aa700737ae27"
	goalID    = "019f278f-5acb-7415-a526-eb852b6409c7"
	rootID    = "019e5905-50da-7000-8000-000000000001"
)

func TestNewGoalKnowledge_MintsPendingRowWithIdentity(t *testing.T) {
	k, err := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if err != nil {
		t.Fatalf("NewGoalKnowledge: unexpected error: %v", err)
	}
	if k.ID == "" {
		t.Error("want a minted UUIDv7 id, got empty")
	}
	// A fresh row has never been generated: it is PENDING synthesis, carries no
	// text, and its reason is the never_generated pseudo-state.
	if k.Status != StatusPending {
		t.Errorf("status = %q, want %q", k.Status, StatusPending)
	}
	if k.SynthesisText != "" {
		t.Errorf("SynthesisText = %q, want empty on a never-generated row", k.SynthesisText)
	}
	if k.InvalidationReason != ReasonNeverGenerated {
		t.Errorf("reason = %q, want %q", k.InvalidationReason, ReasonNeverGenerated)
	}
	if k.GeneratedAt != nil {
		t.Error("GeneratedAt must be nil on a never-generated row")
	}
	if k.IsCacheFresh() {
		t.Error("a never-generated row must not report a fresh cache")
	}
	if k.Servable() {
		t.Error("a never-generated row has no text and must not be servable")
	}
}

func TestNewGoalKnowledge_RejectsMissingIdentity(t *testing.T) {
	cases := map[string]struct{ tenant, learner, companion, goal string }{
		"no tenant":    {"", learner, companion, goalID},
		"no learner":   {tenant, "", companion, goalID},
		"no companion": {tenant, learner, "", goalID},
		"no goal":      {tenant, learner, companion, ""},
		"blank goal":   {tenant, learner, companion, "   "},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewGoalKnowledge(c.tenant, c.learner, c.companion, c.goal, ptr(rootID)); err == nil {
				t.Fatal("want a fail-loud error on missing identity, got nil")
			}
		})
	}
}

func TestRecordSynthesis_MakesRowFreshAndServable(t *testing.T) {
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	now := time.Now().UTC()

	err := k.RecordSynthesis("You have been circling fractions with me.", "run-1", "gemini-x", "v1", "hash-abc", now)
	if err != nil {
		t.Fatalf("RecordSynthesis: unexpected error: %v", err)
	}
	if k.Status != StatusFresh {
		t.Errorf("status = %q, want %q", k.Status, StatusFresh)
	}
	if !k.IsCacheFresh() {
		t.Error("a just-synthesised row must report a fresh cache")
	}
	if !k.Servable() {
		t.Error("a synthesised row must be servable")
	}
	if k.InvalidatedAt != nil {
		t.Error("InvalidatedAt must be cleared by a successful synthesis")
	}
	if k.GeneratedAt == nil || !k.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, want %v", k.GeneratedAt, now)
	}
	// The decision stamp is what makes the row auditable (ADR-197): a row that
	// cannot say which prompt + model produced it is not explainable.
	if k.PromptVersion != "v1" || k.GeneratedByModelID != "gemini-x" || k.GeneratedByRunID != "run-1" {
		t.Errorf("decision stamp not recorded: run=%q model=%q prompt=%q",
			k.GeneratedByRunID, k.GeneratedByModelID, k.PromptVersion)
	}
}

func TestRecordSynthesis_RejectsEmptyOrOversizeText(t *testing.T) {
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	now := time.Now().UTC()

	// Fail loud on an empty synthesis rather than caching a blank reflection —
	// a fabricated/empty success is exactly what the engineering standard bans.
	if err := k.RecordSynthesis("   ", "run-1", "gemini-x", "v1", "h", now); err == nil {
		t.Error("want an error on empty synthesis text, got nil")
	}
	oversize := make([]byte, MaxSynthesisChars+1)
	for i := range oversize {
		oversize[i] = 'a'
	}
	if err := k.RecordSynthesis(string(oversize), "run-1", "gemini-x", "v1", "h", now); err == nil {
		t.Errorf("want an error on synthesis text over %d chars, got nil", MaxSynthesisChars)
	}
}

func TestRecordSynthesis_RequiresDecisionStamp(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]struct{ run, model, prompt string }{
		"no run id":     {"", "gemini-x", "v1"},
		"no model id":   {"run-1", "", "v1"},
		"no prompt ver": {"run-1", "gemini-x", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
			if err := k.RecordSynthesis("text", c.run, c.model, c.prompt, "h", now); err == nil {
				t.Fatal("want an error when the decision stamp is incomplete, got nil")
			}
		})
	}
}

func TestInvalidate_HonoursReasonPriority(t *testing.T) {
	// A weaker, noisier signal must never mask a stronger one in the audit
	// trail — same semantics as the hexagon fog cache.
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	_ = k.RecordSynthesis("text", "run-1", "gemini-x", "v1", "h", time.Now().UTC())

	k.Invalidate(ReasonGoalRerooted) // strongest
	if k.InvalidationReason != ReasonGoalRerooted {
		t.Fatalf("reason = %q, want %q", k.InvalidationReason, ReasonGoalRerooted)
	}
	k.Invalidate(ReasonChatTurnCompleted) // weakest real signal — must not override
	if k.InvalidationReason != ReasonGoalRerooted {
		t.Errorf("a weak chat-turn signal overrode a re-root: reason = %q", k.InvalidationReason)
	}

	// ...but a stronger signal arriving later DOES take over.
	k2, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	_ = k2.RecordSynthesis("text", "run-1", "gemini-x", "v1", "h", time.Now().UTC())
	k2.Invalidate(ReasonChatTurnCompleted)
	k2.Invalidate(ReasonWeaknessGrown)
	if k2.InvalidationReason != ReasonWeaknessGrown {
		t.Errorf("a stronger weakness-grown signal did not take over: reason = %q", k2.InvalidationReason)
	}
}

func TestInvalidate_MarksStaleButKeepsTextServable(t *testing.T) {
	// serve-stale-while-regen: an invalidated row keeps its last good text so
	// the learner sees a reflection (marked "reflecting…") rather than a blank.
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	_ = k.RecordSynthesis("last good text", "run-1", "gemini-x", "v1", "h", time.Now().UTC())

	k.Invalidate(ReasonWeaknessGrown)

	if k.Status != StatusStale {
		t.Errorf("status = %q, want %q", k.Status, StatusStale)
	}
	if k.IsCacheFresh() {
		t.Error("an invalidated row must not report a fresh cache")
	}
	if !k.Servable() {
		t.Error("an invalidated row must still be servable (serve-stale-while-regen)")
	}
	if k.SynthesisText != "last good text" {
		t.Errorf("stale text was destroyed: %q", k.SynthesisText)
	}
	if k.InvalidatedAt == nil {
		t.Error("InvalidatedAt must be stamped")
	}
}

func TestNeedsSynthesis_TTLFloorBoundsChatTurnChurn(t *testing.T) {
	now := time.Now().UTC()
	floor, maxAge, pendingTTL := 6*time.Hour, 14*24*time.Hour, 15*time.Minute

	// Generated 1h ago, then invalidated by a chat turn. The floor must SUPPRESS
	// the regen — otherwise every chat turn triggers an LLM call, which is the
	// cost blow-out the floor exists to prevent.
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	_ = k.RecordSynthesis("text", "run-1", "gemini-x", "v1", "h", now.Add(-1*time.Hour))
	k.Invalidate(ReasonChatTurnCompleted)

	if k.NeedsSynthesis(now, floor, maxAge, pendingTTL) {
		t.Error("regen fired inside the TTL floor — chat-turn churn is unbounded")
	}

	// Same row, 7h after generation: past the floor, so the pending invalidation
	// is now allowed to drive a regen.
	if !k.NeedsSynthesis(now.Add(6*time.Hour), floor, maxAge, pendingTTL) {
		t.Error("regen did not fire past the TTL floor on an invalidated row")
	}
}

func TestNeedsSynthesis_MaxAgeForcesRegenWithoutInvalidation(t *testing.T) {
	now := time.Now().UTC()
	floor, maxAge, pendingTTL := 6*time.Hour, 14*24*time.Hour, 15*time.Minute

	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	_ = k.RecordSynthesis("text", "run-1", "gemini-x", "v1", "h", now.Add(-15*24*time.Hour))
	// Never invalidated — but 15 days old. Max-age must force a refresh.
	if k.Status != StatusFresh {
		t.Fatalf("precondition: status = %q, want fresh", k.Status)
	}
	if !k.NeedsSynthesis(now, floor, maxAge, pendingTTL) {
		t.Error("max-age did not force a regen on a 15-day-old row")
	}
}

func TestNeedsSynthesis_NeverGeneratedAlwaysNeedsSynthesis(t *testing.T) {
	now := time.Now().UTC()
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if !k.NeedsSynthesis(now, 6*time.Hour, 14*24*time.Hour, 15*time.Minute) {
		t.Error("a never-generated row must always need synthesis")
	}
}

func TestNeedsSynthesis_InFlightRequestIsNotReRequested(t *testing.T) {
	now := time.Now().UTC()
	floor, maxAge, pendingTTL := 6*time.Hour, 14*24*time.Hour, 15*time.Minute

	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	k.MarkRequested(now.Add(-1 * time.Minute)) // claim: a request is in flight

	if k.NeedsSynthesis(now, floor, maxAge, pendingTTL) {
		t.Error("a request already in flight was re-requested — duplicate LLM spend")
	}

	// ...but a request that never came back (crash window) must be re-issued
	// rather than leaving the learner stuck on "reflecting…" forever.
	if !k.NeedsSynthesis(now.Add(20*time.Minute), floor, maxAge, pendingTTL) {
		t.Error("a timed-out in-flight request was not re-issued (stuck pending)")
	}
}

func TestRootMatches_GuardsAgainstGoalRerooting(t *testing.T) {
	// ADR-214: goal evolution = re-rooting. A synthesis generated against the old
	// root describes a different subtree and must never be served as current.
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	_ = k.RecordSynthesis("text", "run-1", "gemini-x", "v1", "h", time.Now().UTC())

	if !k.RootMatches(ptr(rootID)) {
		t.Error("root should match the root the row was generated against")
	}
	other := "019f9999-9999-7000-8000-000000000002"
	if k.RootMatches(&other) {
		t.Error("a re-rooted goal must NOT match the stored root")
	}
	if k.RootMatches(nil) {
		t.Error("a goal that lost its root must not match a rooted row")
	}
}

// ── CHO-2180: an honestly-declined synthesis must TERMINATE ──────────────────
//
// The bug this guards: the model looks, finds nothing about this goal worth
// saying, and declines (NO_MEMORY_YET). The fog crew published nothing, so the
// row kept RequestedAt set and GeneratedAt nil — i.e. "never generated" — and
// NeedsSynthesis' second guard returns true UNCONDITIONALLY for that. Once the
// 15-minute claim lapsed, every single tab read re-bought the same doomed LLM
// call, forever, and the learner sat on "reflecting…" that could never resolve.

func silentRow(t *testing.T, generatedAt time.Time) *GoalKnowledge {
	t.Helper()
	k, err := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	k.MarkRequested(generatedAt.Add(-time.Minute))
	if err := k.RecordNoReflection("run-9", "gemini-x", "v1", "hash-abc", generatedAt); err != nil {
		t.Fatalf("RecordNoReflection: %v", err)
	}
	return k
}

func TestRecordNoReflection_IsATerminalConcludedState(t *testing.T) {
	at := time.Now().UTC()
	k := silentRow(t, at)

	if !k.ConcludedSilent() {
		t.Fatal("a declined synthesis must read as CONCLUDED-silent, not as pending")
	}
	if k.GeneratedAt == nil {
		t.Error("GeneratedAt must be stamped — a row that never 'generated' is retried unconditionally forever")
	}
	if k.RequestedAt != nil {
		t.Error("the in-flight claim must be released — the synthesis is over, it just said nothing")
	}
	if k.Status != StatusSilent {
		t.Errorf("Status = %q, want %q", k.Status, StatusSilent)
	}
	if k.SynthesisText != "" {
		t.Errorf("a declined synthesis must carry NO text, got %q", k.SynthesisText)
	}
	// It is emphatically not a cache hit, and there is nothing to serve.
	if k.IsCacheFresh() {
		t.Error("IsCacheFresh must be false — there is no reflection to hit")
	}
	if k.Servable() {
		t.Error("Servable must be false — there is no text to show")
	}
}

func TestRecordNoReflection_StillDemandsTheADR197Stamp(t *testing.T) {
	// Silence is a model DECISION. A row that cannot say which prompt and model
	// concluded it is exactly as unexplainable as an unattributable reflection.
	k, err := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	if err := k.RecordNoReflection("", "gemini-x", "v1", "h", time.Now()); err == nil {
		t.Error("accepted a declined synthesis with no run_id")
	}
	if err := k.RecordNoReflection("run-1", "", "v1", "h", time.Now()); err == nil {
		t.Error("accepted a declined synthesis with no model_id")
	}
	if err := k.RecordNoReflection("run-1", "gemini-x", "", "h", time.Now()); err == nil {
		t.Error("accepted a declined synthesis with no prompt_version")
	}
}

func TestNeedsSynthesis_ADeclinedRowIsNeverRetriedOnATimer(t *testing.T) {
	// THE anti-loop guard. A concluded silence is an ANSWER, not a stale cache
	// entry: only a change to what the model LOOKED AT can change it, and every
	// such change arrives as an invalidation event. Re-running max-age over an
	// unchanged view would buy the same "nothing to say" again, forever.
	at := time.Now().UTC().Add(-30 * 24 * time.Hour) // long past max-age
	k := silentRow(t, at)

	if k.NeedsSynthesis(time.Now().UTC(), floor, maxAge, pendingTTL) {
		t.Fatal("re-bought an LLM call for a row the model already declined, on a timer alone (CHO-2180)")
	}
}

func TestNeedsSynthesis_ADeclinedRowDOESRegenerateOnceInvalidated(t *testing.T) {
	// The other half: the learner chats, or grows a weakness on this goal. The
	// view the model declined has genuinely changed, so ask again (floor-gated).
	at := time.Now().UTC().Add(-8 * time.Hour) // past the 6h floor
	k := silentRow(t, at)
	k.Invalidate(ReasonChatTurnCompleted)

	if !k.NeedsSynthesis(time.Now().UTC(), floor, maxAge, pendingTTL) {
		t.Fatal("a declined row whose inputs changed must be re-asked — silence is not permanent")
	}
}

func TestNeedsSynthesis_ADeclinedRowInvalidatedInsideTheFloorHolds(t *testing.T) {
	// chat_turn_completed fires on EVERY turn. The floor still bounds cost.
	at := time.Now().UTC().Add(-1 * time.Hour) // inside the 6h floor
	k := silentRow(t, at)
	k.Invalidate(ReasonChatTurnCompleted)

	if k.NeedsSynthesis(time.Now().UTC(), floor, maxAge, pendingTTL) {
		t.Fatal("regenerated inside the TTL floor — a chatty learner would regenerate all day")
	}
}

func TestNeedsSynthesis_ANeverGeneratedRowStillRetries(t *testing.T) {
	// Regression: a genuinely LOST synthesis (crash, DLQ) must still be re-issued
	// once the claim lapses. A refusal-to-speak and a broken pipe must NOT
	// collapse into the same state.
	k, err := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	k.MarkRequested(time.Now().UTC().Add(-30 * time.Minute)) // claim lapsed (TTL 15m)

	if !k.NeedsSynthesis(time.Now().UTC(), floor, maxAge, pendingTTL) {
		t.Fatal("a lost synthesis must be retried — only a CONCLUDED silence is terminal")
	}
	if k.ConcludedSilent() {
		t.Fatal("a merely-claimed row must not masquerade as a concluded silence")
	}
}

func TestRecordNoReflection_SupersedesAPreviousReflection(t *testing.T) {
	// The memories behind a reflection were evicted; the model now honestly has
	// nothing to say. Keeping the old prose would have the Companion "remember"
	// what the record no longer contains.
	k := freshRow(t, time.Now().UTC().Add(-7*time.Hour))
	k.Invalidate(ReasonMemoryEvicted)

	if err := k.RecordNoReflection("run-2", "gemini-x", "v1", "hash-new", time.Now().UTC()); err != nil {
		t.Fatalf("RecordNoReflection: %v", err)
	}
	if k.SynthesisText != "" {
		t.Errorf("kept prose the model just declined to stand behind: %q", k.SynthesisText)
	}
	if !k.ConcludedSilent() {
		t.Error("must read as concluded-silent")
	}
	if k.InvalidatedAt != nil {
		t.Error("the invalidation was answered — the row must not stay marked stale")
	}
}
