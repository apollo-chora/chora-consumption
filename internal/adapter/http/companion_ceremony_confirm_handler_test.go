// companion_ceremony_confirm_handler_test.go — CHO-2040 (owner ruling R8-5):
// the ceremony confirm hook, effect (c).
//
//	POST /v1/me/companions/{id}/ceremony/edge-scout/confirm
//	{"goal_id":"<uuid>","edges":[{"concept_id":"<uuid>","title":"...","intent":"remediate|explore"}]}
//
// The FE orchestrates: CHO-2038 POST (mint the ticked learning edges on the
// map) FIRST, then this hook with the SAME response. Remediate ticks →
// chora.consumption.weakness.analyzed.v1 through the outbox (the live
// analyzed subscriber upserts the LearnerWeakness aggregate AND — because the
// event carries output_selection.familiar_coaching=true — writes the
// per-Companion RAG memory). Explore ticks → ONE plain ceremony memory-note on
// the companion. No mana (no LLM turn). Failures are loud; partial success is
// named.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ----------------------------------------------------------------------------
// Seeding
// ----------------------------------------------------------------------------

const (
	ccCID1 = "01970000-cccc-7000-8000-000000000001"
	ccCID2 = "01970000-cccc-7000-8000-000000000002"
	ccCID3 = "01970000-cccc-7000-8000-000000000003"
)

// errPublisher fakes events.Publisher with a settable error.
type errPublisher struct {
	err    error
	topics []string
}

func (p *errPublisher) Publish(topic string, _ events.Envelope, _ map[string]any) error {
	p.topics = append(p.topics, topic)
	return p.err
}

// seedConfirmServer wires a Server with an owned companion, the seeded goal
// (root concept + concept set), a concept-node title for the anchor, an
// embedder, a memory sink, and the default InMemoryPublisher. No engine and
// no mana quoter — the confirm hook must depend on neither.
func seedConfirmServer(t *testing.T) (*Server, string, *fakeCompanionMemory, *events.InMemoryPublisher) {
	t.Helper()
	srv := NewServer()
	id := seedSkillCompanion(t, srv)
	srv.CompanionEngine = nil // no LLM turn on confirm — a nil engine must be fine
	srv.ManaQuoter = nil      // no mana on confirm — a nil quoter must be fine

	root := esRootID
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{
		esGoalID: {
			GoalID: esGoalID, TenantID: testTenant, LearnerGCID: testGCID,
			Kind: goal.KindCuriosity, Status: goal.StatusActive,
			RootConceptID: &root,
			ConceptSet:    []string{"hydrology", "drainage design"},
		},
	}}
	srv.ConceptNodes = &fakeConceptReader{nodes: map[string]*conceptgraph.ConceptNode{
		esRootID: {ConceptID: esRootID, Title: "Flood risk engineering"},
	}}
	srv.Embedder = &fakeEmbedder{vec: []float32{1, 0}}
	mem := &fakeCompanionMemory{}
	srv.CompanionMemory = mem
	srv.CompanionEmbeddingModelID = "text-embedding-004"
	pub := events.NewInMemoryPublisher()
	srv.Publisher = pub
	return srv, id, mem, pub
}

func ccPath(companionID string) string {
	return "/v1/me/companions/" + companionID + "/ceremony/edge-scout/confirm"
}

func ccEdge(id, title, intent string) map[string]any {
	return map[string]any{"concept_id": id, "title": title, "intent": intent}
}

// ccBody is the canonical mixed confirm: 2 remediate + 1 explore.
func ccBody() map[string]any {
	return map[string]any{
		"goal_id": esGoalID,
		"edges": []map[string]any{
			ccEdge(ccCID1, "Recursion base cases", "remediate"),
			ccEdge(ccCID2, "Tail calls", "explore"),
			ccEdge(ccCID3, "Unit conversions", "remediate"),
		},
	}
}

// ----------------------------------------------------------------------------
// Auth / routing / validation gates
// ----------------------------------------------------------------------------

func TestCeremonyConfirm_RequiresAuth(t *testing.T) {
	srv, id, _, _ := seedConfirmServer(t)
	w := plainReq(t, srv, http.MethodPost, ccPath(id), nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyConfirm_MethodNotAllowed(t *testing.T) {
	srv, id, _, _ := seedConfirmServer(t)
	w := authedReq(t, srv, http.MethodGet, ccPath(id), nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyConfirm_GoalRefValidation(t *testing.T) {
	srv, id, _, _ := seedConfirmServer(t)
	for name, body := range map[string]map[string]any{
		"missing":   {"edges": []map[string]any{ccEdge(ccCID1, "T", "explore")}},
		"blank":     {"goal_id": "  ", "edges": []map[string]any{ccEdge(ccCID1, "T", "explore")}},
		"malformed": {"goal_id": "not-a-uuid", "edges": []map[string]any{ccEdge(ccCID1, "T", "explore")}},
	} {
		w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
			continue
		}
		if code := errCode(t, w.Body.Bytes()); code != "INVALID_GOAL_REF" {
			t.Errorf("%s: code = %q, want INVALID_GOAL_REF", name, code)
		}
	}
}

func TestCeremonyConfirm_EdgesValidation(t *testing.T) {
	srv, id, mem, pub := seedConfirmServer(t)
	over := make([]map[string]any, edgescout.MaxCandidates+1)
	for i := range over {
		over[i] = ccEdge(ccCID1[:len(ccCID1)-1]+string(rune('a'+i)), "T", "explore")
	}
	for name, edges := range map[string]any{
		"missing":             nil,
		"empty":               []map[string]any{},
		"over cap":            over,
		"bad intent":          []map[string]any{ccEdge(ccCID1, "T", "boost")},
		"bad concept id":      []map[string]any{ccEdge("nope", "T", "explore")},
		"blank title":         []map[string]any{ccEdge(ccCID1, "   ", "remediate")},
		"unkeyable remediate": []map[string]any{ccEdge(ccCID1, "###", "remediate")},
	} {
		body := map[string]any{"goal_id": esGoalID}
		if edges != nil {
			body["edges"] = edges
		}
		w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
			continue
		}
		if code := errCode(t, w.Body.Bytes()); code != "INVALID_EDGES" {
			t.Errorf("%s: code = %q, want INVALID_EDGES", name, code)
		}
	}
	if len(pub.Events()) != 0 || len(mem.recorded) != 0 {
		t.Errorf("validation failures must have NO side effects (events=%d notes=%d)", len(pub.Events()), len(mem.recorded))
	}
}

func TestCeremonyConfirm_UnknownCompanion(t *testing.T) {
	srv, _, _, _ := seedConfirmServer(t)
	w := authedReq(t, srv, http.MethodPost,
		ccPath("01970000-dead-7000-8000-000000000009"), ccBody())
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyConfirm_GoalNotFoundOrForeign(t *testing.T) {
	srv, id, _, _ := seedConfirmServer(t)
	body := ccBody()
	body["goal_id"] = "01970000-aaaa-7000-8000-00000000dead"
	w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown: status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "GOAL_NOT_FOUND" {
		t.Errorf("unknown: code = %q, want GOAL_NOT_FOUND", code)
	}
	// A goal owned by another learner must 404 (no cross-owner disclosure).
	foreign := *srv.Goals.(*esGoalReader).goals[esGoalID]
	foreign.LearnerGCID = "01970000-0000-7000-9000-00000000ffff"
	srv.Goals.(*esGoalReader).goals[esGoalID] = &foreign
	w = authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign: status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestCeremonyConfirm_NotWired(t *testing.T) {
	t.Run("no goals port", func(t *testing.T) {
		srv, id, _, _ := seedConfirmServer(t)
		srv.Goals = nil
		w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
		}
		if code := errCode(t, w.Body.Bytes()); code != "CEREMONY_CONFIRM_NOT_WIRED" {
			t.Errorf("code = %q, want CEREMONY_CONFIRM_NOT_WIRED", code)
		}
	})
	t.Run("no publisher", func(t *testing.T) {
		srv, id, _, _ := seedConfirmServer(t)
		srv.Publisher = nil
		w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("explore ticks need the memory sink", func(t *testing.T) {
		srv, id, _, pub := seedConfirmServer(t)
		srv.CompanionMemory = nil
		w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503, body=%s", w.Code, w.Body.String())
		}
		// The gate must fire BEFORE any side effect — no half-confirm.
		if len(pub.Events()) != 0 {
			t.Errorf("events published despite the unwired note sink: %d", len(pub.Events()))
		}
	})
	t.Run("remediate-only works without the memory sink", func(t *testing.T) {
		srv, id, _, _ := seedConfirmServer(t)
		srv.CompanionMemory = nil
		srv.Embedder = nil
		body := map[string]any{"goal_id": esGoalID, "edges": []map[string]any{
			ccEdge(ccCID1, "Recursion base cases", "remediate"),
		}}
		w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
		}
	})
}

// ----------------------------------------------------------------------------
// Happy paths — publish + note, and the two pure cases
// ----------------------------------------------------------------------------

func TestCeremonyConfirm_MixedTicks(t *testing.T) {
	srv, id, mem, pub := seedConfirmServer(t)
	w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Published int `json:"published"`
		Noted     int `json:"noted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Published != 2 || resp.Noted != 1 {
		t.Errorf("resp = %+v, want published=2 noted=1", resp)
	}

	// ---- The weakness.analyzed event (remediate ticks → per-Companion RAG).
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("events = %d, want exactly 1 weakness.analyzed", len(evs))
	}
	ev := evs[0]
	if ev.Topic != "chora.consumption.weakness.analyzed.v1" {
		t.Errorf("topic = %q", ev.Topic)
	}
	wantUploadID := edgescout.ConfirmUploadID(testTenant, esGoalID, []string{ccCID1, ccCID3})
	if got := ev.Payload["upload_id"]; got != wantUploadID {
		t.Errorf("upload_id = %v, want the deterministic %s", got, wantUploadID)
	}
	if ev.Envelope.IdempotencyKey == "" || !strings.Contains(ev.Envelope.IdempotencyKey, wantUploadID) {
		t.Errorf("idempotency_key = %q, want it derived from the deterministic upload id", ev.Envelope.IdempotencyKey)
	}
	if ev.Envelope.TenantID != testTenant || ev.Envelope.GCID != testGCID {
		t.Errorf("envelope identity = %s/%s", ev.Envelope.TenantID, ev.Envelope.GCID)
	}
	if ev.Payload["tenant_id"] != testTenant || ev.Payload["learner_gcid"] != testGCID {
		t.Errorf("payload identity = %v/%v", ev.Payload["tenant_id"], ev.Payload["learner_gcid"])
	}
	sel, ok := ev.Payload["output_selection"].(map[string]any)
	if !ok || sel["familiar_coaching"] != true {
		t.Errorf("output_selection = %v — familiar_coaching MUST be true (it gates the RAG hook)", ev.Payload["output_selection"])
	}
	edges, ok := ev.Payload["edges"].([]map[string]any)
	if !ok || len(edges) != 2 {
		t.Fatalf("edges = %v, want the 2 remediate ticks", ev.Payload["edges"])
	}
	if edges[0]["concept_label"] != "Recursion base cases" || edges[1]["concept_label"] != "Unit conversions" {
		t.Errorf("edge labels = %v / %v", edges[0]["concept_label"], edges[1]["concept_label"])
	}
	if edges[0]["concept_key"] != "recursion-base-cases" {
		t.Errorf("concept_key = %v, want the normalised slug", edges[0]["concept_key"])
	}
	if s, _ := edges[0]["strength"].(float64); s != edgescout.CeremonyRemediateStrength {
		t.Errorf("strength = %v, want the documented ceremony default %v", edges[0]["strength"], edgescout.CeremonyRemediateStrength)
	}
	desc, _ := edges[0]["descriptor_json"].(string)
	if !strings.Contains(desc, "summary") || !strings.Contains(desc, "ceremony") {
		t.Errorf("descriptor_json = %q, want a factual ceremony summary", desc)
	}

	// ---- The explore note (ONE plain ceremony memory on the companion).
	if len(mem.recorded) != 1 {
		t.Fatalf("memory notes = %d, want exactly 1", len(mem.recorded))
	}
	note := mem.recorded[0]
	if note.MemoryType != "ceremony" {
		t.Errorf("memory_type = %q, want ceremony", note.MemoryType)
	}
	if note.TenantID != testTenant || note.OwnerGCID != testGCID || note.CompanionID != id {
		t.Errorf("note identity = %s/%s/%s", note.TenantID, note.OwnerGCID, note.CompanionID)
	}
	if note.ModelID != "text-embedding-004" {
		t.Errorf("note model_id = %q", note.ModelID)
	}
	for _, want := range []string{"Tail calls", "Flood risk engineering", "explore"} {
		if !strings.Contains(note.ContentText, want) {
			t.Errorf("note lacks %q:\n%s", want, note.ContentText)
		}
	}
	if strings.Contains(note.ContentText, "Recursion base cases") {
		t.Errorf("remediate ticks must NOT leak into the explore note:\n%s", note.ContentText)
	}
	if len(note.Embedding) == 0 {
		t.Errorf("note embedding missing")
	}
}

func TestCeremonyConfirm_ExploreOnly_NoEvent(t *testing.T) {
	srv, id, mem, pub := seedConfirmServer(t)
	body := map[string]any{"goal_id": esGoalID, "edges": []map[string]any{
		ccEdge(ccCID1, "Tail calls", "explore"),
		ccEdge(ccCID2, "Graph traversal", "explore"),
	}}
	w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Published int `json:"published"`
		Noted     int `json:"noted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Published != 0 || resp.Noted != 2 {
		t.Errorf("resp = %+v, want published=0 noted=2", resp)
	}
	if len(pub.Events()) != 0 {
		t.Errorf("no event may publish for explore-only ticks (got %d)", len(pub.Events()))
	}
	if len(mem.recorded) != 1 {
		t.Errorf("notes = %d, want the ONE ceremony note", len(mem.recorded))
	}
}

func TestCeremonyConfirm_RemediateOnly_NoNote(t *testing.T) {
	srv, id, mem, pub := seedConfirmServer(t)
	body := map[string]any{"goal_id": esGoalID, "edges": []map[string]any{
		ccEdge(ccCID1, "Recursion base cases", "remediate"),
	}}
	w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Published int `json:"published"`
		Noted     int `json:"noted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Published != 1 || resp.Noted != 0 {
		t.Errorf("resp = %+v, want published=1 noted=0", resp)
	}
	if len(pub.Events()) != 1 {
		t.Errorf("events = %d, want 1", len(pub.Events()))
	}
	if len(mem.recorded) != 0 {
		t.Errorf("notes = %d, want 0 (no explore ticks)", len(mem.recorded))
	}
}

// ----------------------------------------------------------------------------
// Idempotency + failure semantics (loud, partial success named)
// ----------------------------------------------------------------------------

func TestCeremonyConfirm_ReconfirmCarriesSameDedupKeys(t *testing.T) {
	// The deterministic (tenant, goal, sorted remediate concept_ids) key is
	// the contract that a re-confirm cannot double-feed: the outbox dedups on
	// idempotency_key and the analyzed subscriber's inbox dedups on
	// (tenant|gcid|upload_id).
	srv, id, _, pub := seedConfirmServer(t)
	for i := 0; i < 2; i++ {
		w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, body=%s", i, w.Code, w.Body.String())
		}
	}
	evs := pub.Events()
	if len(evs) != 2 {
		t.Fatalf("in-memory publisher events = %d, want 2 (it does not dedup)", len(evs))
	}
	if evs[0].Envelope.IdempotencyKey != evs[1].Envelope.IdempotencyKey {
		t.Errorf("idempotency keys differ across identical confirms: %q vs %q",
			evs[0].Envelope.IdempotencyKey, evs[1].Envelope.IdempotencyKey)
	}
	if evs[0].Payload["upload_id"] != evs[1].Payload["upload_id"] {
		t.Errorf("upload ids differ across identical confirms")
	}
}

func TestCeremonyConfirm_DuplicateOutboxRowIsSuccess(t *testing.T) {
	// The production outbox returns ErrDuplicateIdempotencyKey on a re-confirm;
	// the prior emission is already durably queued — that IS success.
	srv, id, _, _ := seedConfirmServer(t)
	srv.Publisher = &errPublisher{err: outbox.ErrDuplicateIdempotencyKey}
	body := map[string]any{"goal_id": esGoalID, "edges": []map[string]any{
		ccEdge(ccCID1, "Recursion base cases", "remediate"),
	}}
	w := authedReq(t, srv, http.MethodPost, ccPath(id), body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (already queued = success), body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Published int `json:"published"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Published != 1 {
		t.Errorf("published = %d, want 1 (the prior emission carries these ticks)", resp.Published)
	}
}

func TestCeremonyConfirm_PublishFailureIsLoud(t *testing.T) {
	srv, id, mem, _ := seedConfirmServer(t)
	srv.Publisher = &errPublisher{err: errors.New("outbox down")}
	w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "CONFIRM_PUBLISH_FAILED" {
		t.Errorf("code = %q, want CONFIRM_PUBLISH_FAILED", code)
	}
	// Publish comes FIRST — a publish failure must leave no note behind.
	if len(mem.recorded) != 0 {
		t.Errorf("notes = %d, want 0 after a publish failure", len(mem.recorded))
	}
}

func TestCeremonyConfirm_NoteFailureNamesPublished(t *testing.T) {
	// Effect ordering is publish → note. When the note write fails AFTER the
	// event went out, the error must NAME the partial success so the FE (and
	// the learner) know the remediate feed already happened.
	srv, id, mem, pub := seedConfirmServer(t)
	mem.recordErr = errors.New("pg down")
	w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "CONFIRM_NOTE_FAILED" {
		t.Errorf("code = %q, want CONFIRM_NOTE_FAILED", code)
	}
	if !strings.Contains(w.Body.String(), "2 remediate") {
		t.Errorf("error must name the already-published remediate ticks: %s", w.Body.String())
	}
	if len(pub.Events()) != 1 {
		t.Errorf("events = %d, want the already-published 1", len(pub.Events()))
	}
}

func TestCeremonyConfirm_EmbedFailureNamesPublished(t *testing.T) {
	srv, id, _, _ := seedConfirmServer(t)
	srv.Embedder = &fakeEmbedder{err: errors.New("vertex quota")}
	w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "CONFIRM_NOTE_FAILED" {
		t.Errorf("code = %q, want CONFIRM_NOTE_FAILED", code)
	}
	if !strings.Contains(w.Body.String(), "2 remediate") {
		t.Errorf("error must name the already-published remediate ticks: %s", w.Body.String())
	}
}

func TestCeremonyConfirm_AnchorReadFailureIsLoud(t *testing.T) {
	// The goal anchor rides both the descriptor and the note — it resolves
	// BEFORE any side effect, so a read failure is a clean 500 (nothing
	// published, nothing noted).
	srv, id, mem, pub := seedConfirmServer(t)
	srv.ConceptNodes = &fakeConceptReader{err: errors.New("pg down")}
	w := authedReq(t, srv, http.MethodPost, ccPath(id), ccBody())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if code := errCode(t, w.Body.Bytes()); code != "CONFIRM_CONCEPT_READ_FAILED" {
		t.Errorf("code = %q, want CONFIRM_CONCEPT_READ_FAILED", code)
	}
	if len(pub.Events()) != 0 || len(mem.recorded) != 0 {
		t.Errorf("anchor failure must precede all side effects (events=%d notes=%d)", len(pub.Events()), len(mem.recorded))
	}
}
