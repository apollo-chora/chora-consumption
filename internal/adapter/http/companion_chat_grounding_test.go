// companion_chat_grounding_test.go — CHO-2192 (ADR-231 D4 / D5, IMDA D2).
//
// THE DEFECT: the Companion vector-recalls a research note it wrote weeks ago,
// restates the web-researched claim inside it, and the learner sees a bare
// assertion with NO source. CHO-2185 gave the note a reader; nothing attributed
// the note at the moment it was actually USED.
//
// THE TRANSPORT (decided here, per the story's entry criteria): a new `grounding`
// SSE frame, emitted by the HANDLER — not the engine — because the handler already
// holds the recalled rows at step 2c, BEFORE the stream opens. It therefore needs
// no engine change, no round-trip, and no gateway route (ChatStream is a
// byte-for-byte SSE pass-through on an existing path).
//
// ⚠ snake_case keys, unlike CHO-2185's camelCase read DTO. That is not sloppiness:
// the gateway's CompanionBridge runs SnakeToCamelJSON over JSON *bodies*, which is
// why the read endpoint pre-camelises — but the SSE path is copied VERBATIM
// (companionbridge.go: "copy frames byte-for-byte"), so this wire keeps the
// snake_case of its five sibling frames.
//
// Reuses fakeChatEngine / newMemChatServer / fakeEmbedder / fakeCompanionMemory /
// chatFramesWithReply / authedChatReq / parseSSE / companionTestID.
package http

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// groundingFrameFrom returns the decoded `grounding` frame, or nil when the
// stream carried none.
func groundingFrameFrom(t *testing.T, body string) map[string]any {
	t.Helper()
	for _, f := range parseSSE(body) {
		if f.Type != "grounding" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(f.Data), &m); err != nil {
			t.Fatalf("grounding frame is not valid JSON: %v\ndata=%s", err, f.Data)
		}
		return m
	}
	return nil
}

// researchRow is a grounded note as the widened recall path now returns it.
func researchRow(id, content string, p *companion.MemoryProvenance) companion.MemoryRow {
	return companion.MemoryRow{
		ID:          id,
		MemoryType:  companion.MemoryTypeResearch,
		ContentText: content,
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Provenance:  p,
	}
}

// wireMemChat wires a chat server whose recall returns the given rows.
func wireMemChat(t *testing.T, rows []companion.MemoryRow) (*Server, *fakeChatEngine) {
	t.Helper()
	engine := &fakeChatEngine{frames: chatFramesWithReply("An incubating egg sits near 37.5C.")}
	srv := newMemChatServer(t, engine)
	srv.Embedder = &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	srv.CompanionMemory = &fakeCompanionMemory{recall: rows}
	srv.CompanionEmbeddingModelID = "text-embedding-004"
	return srv, engine
}

// ── THE HEADLINE: attribution reaches the learner ───────────────────────────

// A turn that recalls a grounded note emits its DURABLE sources.
func TestCompanionChat_Grounding_AttributesARecalledResearchNote(t *testing.T) {
	srv, _ := wireMemChat(t, []companion.MemoryRow{
		researchRow("m1", "an incubating egg is held near 37.5C", &companion.MemoryProvenance{
			WebSearchQueries: []string{"how warm should an egg be"},
			Citations: []companion.MemoryCitation{
				{Domain: "cdc.gov", Title: "Incubation basics", Snippet: "near 37.5C"},
			},
		}),
	})

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "how warm should the egg be?"})
	if w.Code != 200 {
		t.Fatalf("status = %d; want 200\nbody=%s", w.Code, w.Body.String())
	}

	g := groundingFrameFrom(t, w.Body.String())
	if g == nil {
		t.Fatal("no `grounding` frame — the Companion answered from a web-researched note with NO attribution")
	}

	cits, _ := g["citations"].([]any)
	if len(cits) != 1 {
		t.Fatalf("citations = %#v; want the one durable source behind the claim", g["citations"])
	}
	c, _ := cits[0].(map[string]any)
	if c["domain"] != "cdc.gov" {
		t.Errorf("citation domain = %v; want cdc.gov (the DURABLE identity)", c["domain"])
	}
	if c["title"] != "Incubation basics" {
		t.Errorf("citation title = %v; want the headline", c["title"])
	}

	qs, _ := g["web_search_queries"].([]any)
	if len(qs) != 1 || qs[0] != "how warm should an egg be" {
		t.Errorf("web_search_queries = %#v; want the query the model actually issued", g["web_search_queries"])
	}
}

// The attribution must arrive BEFORE the answer it attributes. A source that
// lands after the learner has read the claim has already failed its job.
func TestCompanionChat_Grounding_FramePrecedesTheAnswerTokens(t *testing.T) {
	srv, _ := wireMemChat(t, []companion.MemoryRow{
		researchRow("m1", "note", &companion.MemoryProvenance{
			Citations: []companion.MemoryCitation{{Domain: "cdc.gov"}},
		}),
	})

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "how warm?"})

	frames := parseSSE(w.Body.String())
	groundingAt, tokenAt := -1, -1
	for i, f := range frames {
		if f.Type == "grounding" && groundingAt == -1 {
			groundingAt = i
		}
		if f.Type == "token" && tokenAt == -1 {
			tokenAt = i
		}
	}
	if groundingAt == -1 {
		t.Fatal("no grounding frame emitted")
	}
	if tokenAt == -1 {
		t.Fatal("no token frame emitted — fixture broken")
	}
	if groundingAt > tokenAt {
		t.Errorf("grounding frame at %d arrives AFTER the first token at %d; the learner reads the claim before its source",
			groundingAt, tokenAt)
	}
}

// ── No recall, no claim ─────────────────────────────────────────────────────

// A turn that recalled only ordinary chat memories emits NO attribution block.
// An empty one would imply a web search that never happened.
func TestCompanionChat_Grounding_NoFrameWhenNoGroundedNoteRecalled(t *testing.T) {
	srv, _ := wireMemChat(t, []companion.MemoryRow{
		{ID: "m1", MemoryType: "chat_turn", ContentText: "Learner: I love astronomy", CreatedAt: time.Now().UTC()},
		{ID: "m2", MemoryType: "recap", ContentText: "session recap", CreatedAt: time.Now().UTC()},
	})

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "hello"})
	if w.Code != 200 {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if g := groundingFrameFrom(t, w.Body.String()); g != nil {
		t.Errorf("grounding frame = %#v; want NONE — no grounded note was recalled, so there is nothing to disclose", g)
	}
}

// Memory disabled entirely (no embedder) ⇒ nothing recalled ⇒ nothing to attribute.
func TestCompanionChat_Grounding_NoFrameWhenMemoryDisabled(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("hi")}
	srv := newMemChatServer(t, engine)
	srv.CompanionMemory = &fakeCompanionMemory{recall: []companion.MemoryRow{
		researchRow("m1", "note", &companion.MemoryProvenance{
			Citations: []companion.MemoryCitation{{Domain: "cdc.gov"}},
		}),
	}}
	// srv.Embedder intentionally nil — memory is off, so Recall never runs.

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "hello"})
	if g := groundingFrameFrom(t, w.Body.String()); g != nil {
		t.Errorf("grounding frame = %#v; want NONE — memory is disabled, nothing was recalled", g)
	}
}

// ── Unrecorded provenance: say so, never invent ─────────────────────────────

// A research note written BEFORE mig 0094 has no recoverable sources. We do NOT
// fabricate them — and we do NOT stay silent either, because silence is
// indistinguishable from "no web search happened", which is the very
// untraceable-assertion bug this ticket exists to kill. We say so.
func TestCompanionChat_Grounding_PreMigrationNote_ReportsUnrecorded_NeverFabricates(t *testing.T) {
	srv, _ := wireMemChat(t, []companion.MemoryRow{
		researchRow("m1", "an old researched claim", nil), // source_metadata NULL
	})

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "tell me about eggs"})

	g := groundingFrameFrom(t, w.Body.String())
	if g == nil {
		t.Fatal("no grounding frame — an unrecorded-source note still needs disclosing; silence reads as 'no search happened'")
	}
	if n, _ := g["unrecorded_notes"].(float64); int(n) != 1 {
		t.Errorf("unrecorded_notes = %v; want 1 — the learner must be told the sources were not recorded", g["unrecorded_notes"])
	}
	if cits, _ := g["citations"].([]any); len(cits) != 0 {
		t.Errorf("citations = %#v; want NONE fabricated for a note whose sources were never recorded", cits)
	}
	if qs, _ := g["web_search_queries"].([]any); len(qs) != 0 {
		t.Errorf("web_search_queries = %#v; want NONE fabricated", qs)
	}
}

// ── ADR-231 D4: no expiring links, ever ─────────────────────────────────────

// The wire must carry NO url. The Vertex grounding-api-redirect expires (~30
// days) and was deliberately never persisted — re-serving one from a months-old
// note hands the learner a dead link that still LOOKS live, which is worse than
// no link at all.
func TestCompanionChat_Grounding_CarriesNoExpiringURL(t *testing.T) {
	srv, _ := wireMemChat(t, []companion.MemoryRow{
		researchRow("m1", "note", &companion.MemoryProvenance{
			WebSearchQueries: []string{"how warm should an egg be"},
			Citations: []companion.MemoryCitation{
				{Domain: "cdc.gov", Title: "Incubation basics", Snippet: "near 37.5C"},
			},
		}),
	})

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "how warm?"})

	var raw string
	for _, f := range parseSSE(w.Body.String()) {
		if f.Type == "grounding" {
			raw = f.Data
		}
	}
	if raw == "" {
		t.Fatal("no grounding frame emitted")
	}
	if strings.Contains(raw, `"url"`) {
		t.Errorf("grounding frame carries a url key — the redirect expires and would rot to a dead link:\n%s", raw)
	}
	if strings.Contains(raw, "vertexaisearch") || strings.Contains(raw, "grounding-api-redirect") {
		t.Errorf("grounding frame carries a Vertex redirect:\n%s", raw)
	}
}

// ── Merge + dedup across the recalled set ───────────────────────────────────

// Several recalled notes routinely cite the SAME source. The learner should see
// each source once, nearest-first — not the same domain five times.
func TestCompanionChat_Grounding_DedupsAcrossRecalledNotes(t *testing.T) {
	srv, _ := wireMemChat(t, []companion.MemoryRow{
		researchRow("m1", "note one", &companion.MemoryProvenance{
			WebSearchQueries: []string{"egg warmth"},
			Citations: []companion.MemoryCitation{
				{Domain: "cdc.gov", Title: "Incubation basics"},
				{Domain: "aap.org", Title: "Brooding"},
			},
		}),
		researchRow("m2", "note two", &companion.MemoryProvenance{
			WebSearchQueries: []string{"egg warmth"}, // same query again
			Citations: []companion.MemoryCitation{
				{Domain: "cdc.gov", Title: "Incubation basics"}, // same source again
				{Domain: "ufl.edu", Title: "Poultry science"},
			},
		}),
	})

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "how warm?"})

	g := groundingFrameFrom(t, w.Body.String())
	if g == nil {
		t.Fatal("no grounding frame emitted")
	}
	cits, _ := g["citations"].([]any)
	if len(cits) != 3 {
		t.Fatalf("citations = %d; want 3 unique (cdc.gov, aap.org, ufl.edu) — the duplicate must collapse\n%#v", len(cits), cits)
	}
	first, _ := cits[0].(map[string]any)
	if first["domain"] != "cdc.gov" {
		t.Errorf("citations[0].domain = %v; want cdc.gov — nearest-first order must survive the dedup", first["domain"])
	}
	qs, _ := g["web_search_queries"].([]any)
	if len(qs) != 1 {
		t.Errorf("web_search_queries = %#v; want the repeated query collapsed to one", qs)
	}
}

// ── Transparency metadata, never model input ────────────────────────────────

// Provenance must NOT be woven into the prompt. It is a channel to the LEARNER,
// not to the model: in the prompt it would read to the Companion as knowledge and
// come back out restated as fact — laundering "where I looked" into "what is
// true". CHO-2179 keeps it out of content_text for the same reason (that column
// is what gets embedded).
func TestCompanionChat_Grounding_ProvenanceIsNotInjectedIntoThePrompt(t *testing.T) {
	srv, engine := wireMemChat(t, []companion.MemoryRow{
		researchRow("m1", "an incubating egg is held near 37.5C", &companion.MemoryProvenance{
			WebSearchQueries: []string{"how warm should an egg be"},
			Citations:        []companion.MemoryCitation{{Domain: "cdc.gov", Snippet: "near 37.5C"}},
		}),
	})

	authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat",
		map[string]string{"message": "how warm?"})

	prompt := engine.gotReq.CompanionMemoryJSON
	if !strings.Contains(prompt, "incubating egg") {
		t.Fatalf("the note's CONTENT must still reach the prompt; got %q", prompt)
	}
	if strings.Contains(prompt, "cdc.gov") || strings.Contains(prompt, "how warm should an egg be") {
		t.Errorf("provenance leaked into the model prompt — it must reach the LEARNER, not the model:\n%s", prompt)
	}
}
