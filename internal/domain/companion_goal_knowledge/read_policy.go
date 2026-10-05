// read_policy.go — the whole lazy-regen-on-read policy, as one pure function.
//
// It lives in the domain rather than the handler because it IS the product
// decision: when does the learner see a reflection, when do they see the honest
// empty state, and — the expensive one — when do we spend an LLM call. A handler
// can be re-read; a policy scattered across one has to be re-derived.
package companiongoalknowledge

import "time"

// What the read surface should show.
const (
	// ReadStatusFresh — a current reflection, served from cache, zero LLM cost.
	ReadStatusFresh = "fresh"
	// ReadStatusReflecting — a synthesis is wanted or in flight. The FE shows the
	// last good text (if any) with a subtle "reflecting…" marker, over the tier-1
	// deterministic block. Never an error card.
	ReadStatusReflecting = "reflecting"
	// ReadStatusNone — there is honestly nothing to reflect on. The FE shows
	// "no memory yet" (ADR-207 Unknown-is-a-state), never a fabricated line.
	ReadStatusNone = "none"
	// ReadStatusPaused (ADR-252 Q5) - an operator has CONTAINED the Companion,
	// so this read neither claims the row nor publishes a synthesis request.
	// It is not a decision DecideRead can reach: the read handler consults the
	// advisory suspension projection and overrides the status, because the
	// containment lives in chora_observability and this policy is pure. The FE
	// says "your Companion is paused" instead of showing a reflecting marker
	// for work that will never start. Any cached text still rides along, since
	// a pause stops NEW synthesis rather than retracting what the learner has
	// already been shown.
	ReadStatusPaused = "paused"
)

// ReadDecision is the handler's marching order. The handler does not reason; it
// obeys.
type ReadDecision struct {
	// Text is the reflection to serve, or "" when there is none fit to serve.
	Text string
	// Status is the render mode (fresh / reflecting / none).
	Status string
	// RequestSynthesis: publish a synthesis request and claim the row.
	RequestSynthesis bool
}

// DecideRead resolves one tab read.
//
// `k` is the cached row (nil before the first read). `hasSignal` comes from the
// assembled tier-1 view: does this learner have ANY memory or shaky concept on
// this goal. `currentRoot` is the goal's root concept RIGHT NOW.
//
// Three guards carry the weight, in this order:
//
//  1. **No signal ⇒ never call the model.** With no memories and no shaky
//     concepts there is nothing true to say, so a synthesis could only invent
//     something. This also suppresses a previously-cached reflection: if the
//     memories behind it are gone, serving it would have the Companion "remember"
//     what the record no longer contains.
//  2. **Re-rooted goal ⇒ never serve the old text.** ADR-214 goal evolution is a
//     re-root; the cached reflection is about a different subtree. Freshness is
//     irrelevant — a confidently wrong reflection is worse than none.
//  3. **The floor bounds cost, not correctness.** An invalidated row keeps
//     serving its last good text; the floor only decides whether we PAY for a new
//     one yet. chat_turn_completed fires on every chat turn, so without this a
//     chatty learner regenerates their reflection all day.
func DecideRead(
	k *GoalKnowledge,
	hasSignal bool,
	currentRoot *string,
	now time.Time,
	floor, maxAge, pendingTTL time.Duration,
) ReadDecision {
	// (1) Nothing true to say — and that is a fine thing to say.
	if !hasSignal {
		return ReadDecision{Status: ReadStatusNone}
	}

	// Never synthesised for this (companion, goal) yet.
	if k == nil {
		return ReadDecision{Status: ReadStatusReflecting, RequestSynthesis: true}
	}

	// (2) The goal re-rooted under the cached reflection. Withhold the text and
	// ask for one about the goal the learner actually has now.
	//
	// This deliberately does NOT go through NeedsSynthesis. A re-rooted reflection
	// is not stale, it is WRONG — it describes a different subtree — and the TTL
	// floor exists to bound churn cost, not to delay replacing incorrect content.
	// Deferring to the floor here would strand a freshly-generated row: too young
	// to regenerate, too wrong to serve, so the learner would sit on tier-1 until
	// the floor elapsed. Only the in-flight claim may suppress it, so a re-root
	// still cannot stampede the model.
	if !k.RootMatches(currentRoot) {
		return ReadDecision{
			Status:           ReadStatusReflecting,
			RequestSynthesis: !k.SynthesisInFlight(now, pendingTTL),
		}
	}

	// (3) Cost gate. Note this is evaluated even on a fresh row, so max-age can
	// refresh a reflection that nothing ever invalidated.
	needs := k.NeedsSynthesis(now, floor, maxAge, pendingTTL)

	// (4) The model looked and honestly had nothing to say about this goal. That
	// verdict IS the answer — not a waiting state — so say so plainly (CHO-2180).
	//
	// Reporting "reflecting" here was the bug: nothing was ever coming, so the
	// learner sat on a marker that could never resolve, while every read past the
	// claim window re-bought the same guaranteed-to-decline LLM call. `needs` is
	// still honoured, so an invalidated silence DOES get re-asked — but we keep
	// telling the truth in the meantime rather than promising prose that may never
	// arrive. When a later synthesis does find something, it supersedes this.
	if k.ConcludedSilent() {
		return ReadDecision{Status: ReadStatusNone, RequestSynthesis: needs}
	}

	if k.IsCacheFresh() && !needs {
		return ReadDecision{Text: k.SynthesisText, Status: ReadStatusFresh}
	}
	if k.Servable() {
		// serve-stale-while-regen — the learner keeps the previous reflection.
		return ReadDecision{Text: k.SynthesisText, Status: ReadStatusReflecting, RequestSynthesis: needs}
	}
	// Claimed but nothing synthesised yet (first read still in flight).
	return ReadDecision{Status: ReadStatusReflecting, RequestSynthesis: needs}
}
