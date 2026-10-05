// goal_knowledge_claimless_test.go: C4, the claimless read on the reflection tab.
//
//	GET /v1/me/goals/{id}/knowledge?generate=false
//
// ADR-235's rule is "never fetch a reflection for a panel the learner is not
// looking at", because this endpoint CLAIMS a row and SCHEDULES an LLM call on
// read. Today that rule lives entirely in the frontend caller, so it is a
// convention: nothing in the backend can enforce it, and nothing can prove a
// caller obeyed it.
//
// ?generate=false makes it enforceable. The read serves the same tier-1 block
// and the same cached text, and it writes NOTHING and publishes NOTHING. A
// caller that must not spend can now say so, and be held to it.
//
// The default is pinned in the same file and in both directions, because the
// whole value of an additive parameter is that the wire without it did not move.
package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
)

// gkGetQuery drives the handler with a raw query string.
func (h *gkHarness) getQuery(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/v1/me/goals/" + gkGoal + "/knowledge"
	if query != "" {
		path += "?" + query
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Tenant-Id", gkTenant)
	r.Header.Set("gcid", gkGCID)
	w := httptest.NewRecorder()
	h.ext.handleMeGoalByID(w, r)
	return w
}

// THE new invariant. A row that has never been synthesised is the strongest
// case: the policy wants a synthesis, and generate=false must still neither
// claim nor publish. Tier-1 is served exactly as before.
func TestGoalKnowledgeClaimless_NeverClaimsAndNeverPublishes(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)

	w := h.getQuery(t, "generate=false")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(h.repo.upserts) != 0 {
		t.Errorf("claimless read wrote %d row(s); want 0 (it must never claim)", len(h.repo.upserts))
	}
	if len(h.pub.events) != 0 {
		t.Errorf("claimless read published %d event(s); want 0 (it must never buy an LLM call)", len(h.pub.events))
	}
	if len(h.seq) != 0 {
		t.Errorf("claimless read performed writes %v; want none", h.seq)
	}
	// Tier-1 is the response, and it is unaffected by the parameter.
	got := decodeGK(t, w)
	if got.GoalID != gkGoal {
		t.Errorf("goalId = %q; want %q (tier-1 still served)", got.GoalID, gkGoal)
	}
	if len(got.ShakyConcepts) == 0 {
		t.Errorf("shakyConcepts empty; want the tier-1 signal served unchanged")
	}
}

// The other half of "pinned both ways": the DEFAULT still claims and publishes.
// Without this the first test could pass because generation broke entirely.
func TestGoalKnowledgeClaimless_DefaultStillClaimsAndPublishes(t *testing.T) {
	for _, query := range []string{"", "generate=true"} {
		h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)

		if w := h.getQuery(t, query); w.Code != http.StatusOK {
			t.Fatalf("query %q: status = %d body=%s", query, w.Code, w.Body.String())
		}
		if len(h.repo.upserts) != 1 {
			t.Errorf("query %q: claims = %d; want 1 (default behaviour must not move)", query, len(h.repo.upserts))
		}
		if len(h.pub.events) != 1 {
			t.Errorf("query %q: publishes = %d; want 1 (default behaviour must not move)", query, len(h.pub.events))
		}
		// Claim BEFORE publish, the invariant the default path already carries.
		if len(h.seq) != 2 || h.seq[0] != "upsert" || h.seq[1] != "publish" {
			t.Errorf("query %q: write order = %v; want [upsert publish]", query, h.seq)
		}
	}
}

// Cached text is still served: claimless means "spend nothing", not "tell me
// nothing". A learner reopening a tab with a fresh reflection sees it.
func TestGoalKnowledgeClaimless_StillServesTheCachedReflection(t *testing.T) {
	now := time.Now().UTC()
	row := &fgk.GoalKnowledge{
		TenantID: gkTenant, LearnerGCID: gkGCID, CompanionID: gkFam, GoalID: gkGoal,
		SynthesisText: "You are steadier on fractions than last week.",
		Status:        fgk.StatusFresh, GeneratedAt: &now, RootConceptID: nil,
	}
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), row)

	got := decodeGK(t, h.getQuery(t, "generate=false"))
	if got.Reflection.Text != row.SynthesisText {
		t.Errorf("reflection text = %q; want the cached text served unchanged", got.Reflection.Text)
	}
	if len(h.repo.upserts) != 0 || len(h.pub.events) != 0 {
		t.Errorf("a cache hit under generate=false wrote %d and published %d; want 0 and 0",
			len(h.repo.upserts), len(h.pub.events))
	}
}

// The response says plainly that this read bought nothing, so a caller can never
// mistake a "reflecting" status for work its own read set in motion. Nothing was
// asked for, and a spinner rendered from that would never resolve: the CHO-2180
// failure in a new costume.
func TestGoalKnowledgeClaimless_ResponseDeclaresItBoughtNothing(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)

	if got := decodeGK(t, h.getQuery(t, "generate=false")); !got.Reflection.Claimless {
		t.Errorf("reflection.claimless = false on a claimless read; want true")
	}
}

// The default wire must not move: the flag is ABSENT (not false) when the read
// was a normal one, so an existing consumer sees a byte-identical reflection
// block.
func TestGoalKnowledgeClaimless_TheFlagIsAbsentOnADefaultRead(t *testing.T) {
	h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)

	w := h.getQuery(t, "")
	if body := w.Body.String(); contains(body, "claimless") {
		t.Errorf("default read carries the claimless key; want it omitted entirely. body=%s", body)
	}
}

// An unparseable value is a 400, not a guess. Guessing has no safe direction:
// defaulting to generate would charge a caller that asked not to be charged, and
// defaulting to claimless would silently stop generating reflections for a
// caller with a typo, which is invisible until somebody notices the panel never
// fills.
func TestGoalKnowledgeClaimless_AnUnparseableValueIs400(t *testing.T) {
	for _, bad := range []string{"generate=fasle", "generate=1", "generate=", "generate=no"} {
		h := newGKHarness(t, gkGoalWithCompanion(), gkViewWithSignal(), nil)

		w := h.getQuery(t, bad)
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d; want 400 (never guess which way the caller meant)", bad, w.Code)
		}
		if len(h.repo.upserts) != 0 || len(h.pub.events) != 0 {
			t.Errorf("query %q: a refused read still wrote %d and published %d; want 0 and 0",
				bad, len(h.repo.upserts), len(h.pub.events))
		}
	}
}
