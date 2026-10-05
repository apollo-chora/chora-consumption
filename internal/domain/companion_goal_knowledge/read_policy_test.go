package companiongoalknowledge

import (
	"testing"
	"time"
)

const (
	floor      = 6 * time.Hour
	maxAge     = 14 * 24 * time.Hour
	pendingTTL = 15 * time.Minute
)

func freshRow(t *testing.T, generatedAt time.Time) *GoalKnowledge {
	t.Helper()
	k, err := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	if err := k.RecordSynthesis("I remember you circling fractions.", "run-1", "gemini-x", "v1", "hash-abc", generatedAt); err != nil {
		t.Fatalf("RecordSynthesis: %v", err)
	}
	return k
}

func TestDecideRead_NoSignalNeverSpendsAnLLMCall(t *testing.T) {
	// The single most important guard: with no memories AND no shaky concepts the
	// Companion has nothing TRUE to say. Requesting a synthesis here would be
	// paying a model to fabricate one. Honest empty state instead (ADR-207).
	now := time.Now().UTC()

	d := DecideRead(nil, false, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.RequestSynthesis {
		t.Error("requested a synthesis with no signal — that pays a model to invent a memory")
	}
	if d.Status != ReadStatusNone {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusNone)
	}
	if d.Text != "" {
		t.Errorf("text = %q, want empty", d.Text)
	}
}

func TestDecideRead_NoSignalSuppressesEvenACachedReflection(t *testing.T) {
	// The learner had memories, got a reflection, then the memories were evicted.
	// The cached text is now about a past that no longer exists in the record —
	// serving it would be the Companion "remembering" something we deleted.
	now := time.Now().UTC()
	k := freshRow(t, now)

	d := DecideRead(k, false, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.Text != "" {
		t.Errorf("served a cached reflection with no signal left: %q", d.Text)
	}
	if d.Status != ReadStatusNone {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusNone)
	}
}

func TestDecideRead_FirstReadRequestsAndServesTier1(t *testing.T) {
	now := time.Now().UTC()

	d := DecideRead(nil, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if !d.RequestSynthesis {
		t.Error("first read with signal must request a synthesis")
	}
	if d.Status != ReadStatusReflecting {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusReflecting)
	}
	if d.Text != "" {
		t.Error("nothing has been synthesised yet — there is no text to serve")
	}
}

func TestDecideRead_CacheHitServesWithoutAnLLMCall(t *testing.T) {
	now := time.Now().UTC()
	k := freshRow(t, now)

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.RequestSynthesis {
		t.Error("a fresh cache hit must NOT trigger an LLM call — that is the whole point of the cache")
	}
	if d.Status != ReadStatusFresh {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusFresh)
	}
	if d.Text == "" {
		t.Error("a fresh row must serve its text")
	}
}

func TestDecideRead_StaleServesLastGoodTextWhileRegenerating(t *testing.T) {
	// serve-stale-while-regen: the learner keeps seeing the previous reflection
	// (marked "reflecting"), never a blank panel.
	now := time.Now().UTC()
	k := freshRow(t, now.Add(-7*time.Hour)) // past the floor
	k.Invalidate(ReasonWeaknessGrown)

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.Text == "" {
		t.Error("stale row must still serve its last good text")
	}
	if d.Status != ReadStatusReflecting {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusReflecting)
	}
	if !d.RequestSynthesis {
		t.Error("past the floor, an invalidated row must trigger a regen")
	}
}

func TestDecideRead_StaleInsideTheFloorServesButDoesNotRegen(t *testing.T) {
	// chat_turn_completed fires on EVERY chat turn. Inside the floor we still
	// serve the text, but we must NOT pay for a fresh synthesis.
	now := time.Now().UTC()
	k := freshRow(t, now.Add(-1*time.Hour)) // well inside the 6h floor
	k.Invalidate(ReasonChatTurnCompleted)

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.RequestSynthesis {
		t.Error("regen fired inside the TTL floor — chat-turn churn would be unbounded")
	}
	if d.Text == "" {
		t.Error("must still serve the last good text inside the floor")
	}
}

func TestDecideRead_ReRootedGoalNeverServesTheOldReflection(t *testing.T) {
	// ADR-214: a re-rooted goal is about a DIFFERENT subtree. The cached text
	// describes the old one, so it must not be served as current no matter how
	// fresh it looks — it would be a confidently wrong reflection.
	now := time.Now().UTC()
	k := freshRow(t, now) // fresh, un-invalidated
	newRoot := "019f9999-9999-7000-8000-000000000002"

	d := DecideRead(k, true, &newRoot, now, floor, maxAge, pendingTTL)

	if d.Text != "" {
		t.Errorf("served a reflection about the OLD root after a re-root: %q", d.Text)
	}
	if !d.RequestSynthesis {
		t.Error("a re-rooted goal must request a fresh synthesis")
	}
	if d.Status != ReadStatusReflecting {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusReflecting)
	}
}

func TestDecideRead_InFlightRequestIsNotReRequested(t *testing.T) {
	now := time.Now().UTC()
	k, _ := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	k.MarkRequested(now.Add(-1 * time.Minute))

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.RequestSynthesis {
		t.Error("a synthesis already in flight was re-requested — duplicate LLM spend per concurrent tab read")
	}
	if d.Status != ReadStatusReflecting {
		t.Errorf("status = %q, want %q", d.Status, ReadStatusReflecting)
	}
}

func TestDecideRead_MaxAgeRefreshesASilentlyStaleReflection(t *testing.T) {
	now := time.Now().UTC()
	k := freshRow(t, now.Add(-15*24*time.Hour)) // never invalidated, but ancient

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if !d.RequestSynthesis {
		t.Error("max-age did not refresh a 15-day-old reflection")
	}
	if d.Text == "" {
		t.Error("must keep serving the old text while the refresh runs")
	}
}

// ── CHO-2180 regression: a declined synthesis must not read as "reflecting" ──

func TestDecideRead_AConcludedSilenceReadsAsNoneNotReflecting(t *testing.T) {
	// THE bug. The model looked, had nothing true to say about THIS goal, and
	// said so. That verdict IS the answer — not a waiting state. Reporting
	// "reflecting" stranded the learner on a marker that could never resolve,
	// because nothing was ever coming.
	now := time.Now().UTC()
	k := silentRow(t, now.Add(-time.Hour))

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.Status != ReadStatusNone {
		t.Fatalf("status = %q, want %q — a concluded silence is an answer, not a pending state (CHO-2180)", d.Status, ReadStatusNone)
	}
	if d.Text != "" {
		t.Errorf("Text = %q, want empty — there is no reflection", d.Text)
	}
	if d.RequestSynthesis {
		t.Error("re-bought the LLM call the model already declined (CHO-2180)")
	}
}

func TestDecideRead_ADeclinedRowStaysSilentAcrossRepeatedReads(t *testing.T) {
	// The cost bug, stated as the learner experiences it: opening the tab again
	// and again — including long after the 15-minute claim lapsed — must not
	// re-buy a single Gemini call.
	base := time.Now().UTC()
	k := silentRow(t, base.Add(-time.Hour))

	for _, after := range []time.Duration{0, 20 * time.Minute, 7 * 24 * time.Hour, 30 * 24 * time.Hour} {
		d := DecideRead(k, true, ptr(rootID), base.Add(after), floor, maxAge, pendingTTL)
		if d.RequestSynthesis {
			t.Fatalf("re-requested a declined synthesis %s after it concluded (CHO-2180)", after)
		}
		if d.Status != ReadStatusNone {
			t.Fatalf("status %s after conclusion = %q, want %q", after, d.Status, ReadStatusNone)
		}
	}
}

func TestDecideRead_ADeclinedRowReAsksOnceItsInputsChange(t *testing.T) {
	// Silence is not permanent. The learner chats or grows a weakness on this
	// goal; the view the model declined has changed, so ask again — while STILL
	// telling the truth ("none") rather than promising prose that may not come.
	now := time.Now().UTC()
	k := silentRow(t, now.Add(-8*time.Hour)) // past the floor
	k.Invalidate(ReasonWeaknessAnalyzed)

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if !d.RequestSynthesis {
		t.Error("did not re-ask after the inputs genuinely changed")
	}
	if d.Status != ReadStatusNone {
		t.Errorf("status = %q, want %q — we hold no prose, so say so", d.Status, ReadStatusNone)
	}
}

func TestDecideRead_AClaimedButUnfinishedRowStillReadsAsReflecting(t *testing.T) {
	// Regression guard: the genuine in-flight case must be untouched. A synthesis
	// that really IS running still shows the "reflecting…" marker.
	now := time.Now().UTC()
	k, err := NewGoalKnowledge(tenant, learner, companion, goalID, ptr(rootID))
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	k.MarkRequested(now.Add(-time.Minute)) // claim live (TTL 15m)

	d := DecideRead(k, true, ptr(rootID), now, floor, maxAge, pendingTTL)

	if d.Status != ReadStatusReflecting {
		t.Fatalf("status = %q, want %q — a synthesis really is in flight", d.Status, ReadStatusReflecting)
	}
	if d.RequestSynthesis {
		t.Error("stampeded a second LLM call over a live claim")
	}
}
