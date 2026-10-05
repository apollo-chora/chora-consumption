// dose_campaign_qgen_test.go — WS-C3 (CHO-2082): the retrieval-first question
// lane inside the campaign compose feeder + the qgen gap trigger.
//
//	AC1 retrieval-first: level-matched atoms exist → served, NO LLM call.
//	AC2 gap generation:  nothing matches → ONE ≤2-question batch request on
//	    the live ai_assist lane, bank row → requested (daily-capped).
//	AC3 reuse:           a ready/requested set never re-publishes.
//	AC4 fail-loud/soft:  a publish failure marks the set failed + the dose
//	    still composes (cascade fill) — never a fabricated question.
package http

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// ---------- fakes ----------

type qgenBank struct {
	rows       map[string]*cq.QuestionSet // key concept|rung
	saved      []*cq.QuestionSet
	countToday int // march-origin requests today
	tapToday   int // tap-origin requests today
}

func qgenKey(conceptID string, rung int) string { return conceptID + "|" + string(rune('0'+rung)) }

func (b *qgenBank) GetByConceptRung(_ context.Context, _, _, conceptID string, rung int) (*cq.QuestionSet, error) {
	if b.rows == nil {
		return nil, nil
	}
	return b.rows[qgenKey(conceptID, rung)], nil
}
func (b *qgenBank) GetByAssistID(context.Context, string, string) (*cq.QuestionSet, error) {
	return nil, nil
}
func (b *qgenBank) CountRequestedOn(_ context.Context, _, _ string, origin cq.RequestOrigin, _ time.Time) (int, error) {
	if origin == cq.OriginTap {
		return b.tapToday, nil
	}
	return b.countToday, nil
}
func (b *qgenBank) Save(_ context.Context, s *cq.QuestionSet) error {
	b.saved = append(b.saved, s)
	if b.rows == nil {
		b.rows = map[string]*cq.QuestionSet{}
	}
	b.rows[qgenKey(s.ConceptID, s.Rung)] = s
	return nil
}

type qgenSearcher struct {
	ids   []string
	calls int
}

func (s *qgenSearcher) SearchByEmbedding(context.Context, string, []float32, int) ([]string, error) {
	s.calls++
	return s.ids, nil
}

type qgenEmbedder struct{ texts []string }

func (e *qgenEmbedder) Embed(_ context.Context, text, _ string) ([]float32, error) {
	e.texts = append(e.texts, text)
	return []float32{0.1, 0.2}, nil
}

type qgenPublisher struct {
	topics   []string
	envs     []events.Envelope
	payloads []map[string]any
	err      error
}

func (p *qgenPublisher) Publish(topic string, env events.Envelope, payload map[string]any) error {
	if p.err != nil {
		return p.err
	}
	p.topics = append(p.topics, topic)
	p.envs = append(p.envs, env)
	p.payloads = append(p.payloads, payload)
	return nil
}

// qgenServer upgrades the WS-C2 campaignServer with the question-lane deps.
// The focus node keeps NO usable AtomRefs by default (the retrieval/gap paths
// are under test); rung is 1 (knowledge) — the fresh-ladder serve decision.
func qgenServer(t *testing.T) (*Server, *qgenBank, *qgenSearcher, *qgenEmbedder, *qgenPublisher) {
	t.Helper()
	srv, _ := campaignServer(t)
	srv.CampaignDose.Concepts = &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {ConceptID: campConceptID, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "CSPO Basics", ConceptKey: "cspo-basics"},
	}}
	bank := &qgenBank{}
	search := &qgenSearcher{}
	embed := &qgenEmbedder{}
	pub := &qgenPublisher{}
	srv.CampaignDose.Bank = bank
	srv.CampaignDose.Searcher = search
	srv.CampaignDose.Embedder = embed
	srv.CampaignDose.QGen = pub
	return srv, bank, search, embed, pub
}

// levelAtom projects a published gradable atom at one Bloom level into the
// server's atom index (outside the enrolled universe — the augment serves it).
func levelAtom(t *testing.T, srv *Server, atomID, level string) {
	t.Helper()
	a, err := atom_index.New(atom_index.NewParams{
		AtomID:          atomID,
		TenantID:        testTenant,
		AtomType:        "mcq",
		CorrectOptionID: "opt-a",
		AnswerCount:     4,
		Status:          atom_index.StatusPublished,
		PublishedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("atom_index.New: %v", err)
	}
	a.CognitiveLevel = level
	a.TopicTags = []string{"cspo"}
	if err := srv.AtomIndex.Save(context.Background(), a); err != nil {
		t.Fatalf("index save: %v", err)
	}
}

// ---------- AC1: retrieval first ----------

func TestDoseCampaign_RetrievalFillsLevelMatched_NoLLM(t *testing.T) {
	srv, bank, search, embed, pub := qgenServer(t)
	const lvOK1, lvOK2, lvNo = "01990000-0000-7000-a000-0000000000a1",
		"01990000-0000-7000-a000-0000000000a2", "01990000-0000-7000-a000-0000000000a3"
	levelAtom(t, srv, lvOK1, "knowledge")
	levelAtom(t, srv, lvOK2, "knowledge")
	levelAtom(t, srv, lvNo, "analysis")
	search.ids = []string{lvOK1, lvNo, lvOK2}

	resp := decodeCampDose(t, authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil))
	var camp []string
	for _, e := range resp.Entries {
		if e.DoseReason == "campaign" {
			camp = append(camp, e.AtomID)
		}
	}
	if len(camp) != 2 || camp[0] != lvOK1 || camp[1] != lvOK2 {
		t.Fatalf("campaign must serve the level-matched retrieval hits in order, got %v", camp)
	}
	if search.calls != 1 || len(embed.texts) != 1 || !strings.Contains(embed.texts[0], "CSPO Basics") {
		t.Fatalf("retrieval must embed the node theme once (calls=%d texts=%v)", search.calls, embed.texts)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("AC1: atoms matched — NO LLM request may fire, got %v", pub.topics)
	}
	// The level-matched hits are cached on the bank row for future composes.
	row := bank.rows[qgenKey(campConceptID, 1)]
	if row == nil || len(row.RetrievedAtomIDs) != 2 {
		t.Fatalf("retrieval cache must persist, got %+v", row)
	}
}

func TestDoseCampaign_CachedRetrievalSkipsSearch(t *testing.T) {
	srv, bank, search, _, pub := qgenServer(t)
	const cached = "01990000-0000-7000-a000-0000000000b1"
	levelAtom(t, srv, cached, "knowledge")
	row, err := cq.New(testTenant, testGCID, campConceptID, "cspo-basics", 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("cq.New: %v", err)
	}
	row.SetRetrieved([]string{cached}, time.Now().UTC())
	bank.rows = map[string]*cq.QuestionSet{qgenKey(campConceptID, 1): row}

	resp := decodeCampDose(t, authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil))
	served := false
	for _, e := range resp.Entries {
		if e.DoseReason == "campaign" && e.AtomID == cached {
			served = true
		}
	}
	if !served {
		t.Fatal("cached retrieval hit must serve")
	}
	if search.calls != 0 {
		t.Fatalf("a warm cache must skip the search RPC, got %d calls", search.calls)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("no gap — no LLM, got %v", pub.topics)
	}
}

func TestDoseCampaign_KnownMismatchAtomRefExcluded(t *testing.T) {
	srv, _, search, _, _ := qgenServer(t)
	const refMismatch, refUnknown = "01990000-0000-7000-a000-0000000000c1", "01990000-0000-7000-a000-0000000000c2"
	levelAtom(t, srv, refMismatch, "analysis") // known ≠ rung-1 knowledge
	levelAtom(t, srv, refUnknown, "")          // unknown level — benefit of the doubt
	srv.CampaignDose.Concepts = &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {ConceptID: campConceptID, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "CSPO Basics", ConceptKey: "cspo-basics", AtomRefs: []string{refMismatch, refUnknown}},
	}}
	search.ids = nil

	resp := decodeCampDose(t, authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil))
	var camp []string
	for _, e := range resp.Entries {
		if e.DoseReason == "campaign" {
			camp = append(camp, e.AtomID)
		}
	}
	if len(camp) != 1 || camp[0] != refUnknown {
		t.Fatalf("known-mismatch ref must be excluded, unknown kept: %v", camp)
	}
}

// ---------- AC2: gap generation ----------

func TestDoseCampaign_GapTriggersOneBoundedRequest(t *testing.T) {
	srv, bank, _, _, pub := qgenServer(t)

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("dose: %d", w.Code)
	}
	if len(pub.topics) != 1 || pub.topics[0] != "chora.creation.ai_assist.started.v2" {
		t.Fatalf("gap must publish ONE started.v2 request, got %v", pub.topics)
	}
	payload := pub.payloads[0]
	if payload["content_type"] != "mcq" || payload["requested_count"] != int32(cq.QuestionsPerRequest) {
		t.Fatalf("request must ask %d MCQs, got %v", cq.QuestionsPerRequest, payload)
	}
	keys, _ := payload["target_growth_edges"].([]string)
	if len(keys) != 1 || keys[0] != "cspo-basics" {
		t.Fatalf("target_growth_edges must carry the concept key, got %v", payload["target_growth_edges"])
	}
	meta, _ := payload["metadata"].(map[string]string)
	if meta["cognitive_level"] != "knowledge" {
		t.Fatalf("metadata must hint the rung level, got %v", meta)
	}
	prompt, _ := payload["prompt"].(string)
	if !strings.Contains(prompt, "CSPO Basics") || !strings.Contains(prompt, "knowledge") {
		t.Fatalf("prompt must carry theme + level:\n%s", prompt)
	}
	if pub.envs[0].TenantID != testTenant || pub.envs[0].GCID != testGCID {
		t.Fatalf("envelope must carry tenant + gcid, got %+v", pub.envs[0])
	}
	row := bank.rows[qgenKey(campConceptID, 1)]
	if row == nil || row.GenerationStatus != cq.StatusRequested || row.AssistID != payload["assist_id"] {
		t.Fatalf("bank row must be requested + keyed to the assist id, got %+v", row)
	}
	if row.RequestOrigin != cq.OriginMarch {
		t.Fatalf("the dose feeder must stamp origin=march, got %q", row.RequestOrigin)
	}
}

func TestDoseCampaign_DailyCapBlocksGeneration(t *testing.T) {
	srv, bank, _, _, pub := qgenServer(t)
	bank.countToday = cq.DefaultDailyMarchCap

	if w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil); w.Code != http.StatusOK {
		t.Fatalf("dose: %d", w.Code)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("at-cap learner must not generate (D13 ≤2 questions/day), got %v", pub.topics)
	}
}

// ---------- AC3: reuse ----------

func TestDoseCampaign_ReadyOrRequestedRowNeverRepublishes(t *testing.T) {
	for _, status := range []string{"ready", "requested"} {
		srv, bank, _, _, pub := qgenServer(t)
		row, err := cq.New(testTenant, testGCID, campConceptID, "cspo-basics", 1, time.Now().UTC())
		if err != nil {
			t.Fatalf("cq.New: %v", err)
		}
		if err := row.MarkRequested("assist-prev", time.Now().UTC()); err != nil {
			t.Fatalf("MarkRequested: %v", err)
		}
		if status == "ready" {
			if err := row.MarkReady([]byte(`{"candidates":[]}`), time.Now().UTC()); err != nil {
				t.Fatalf("MarkReady: %v", err)
			}
		}
		bank.rows = map[string]*cq.QuestionSet{qgenKey(campConceptID, 1): row}

		if w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil); w.Code != http.StatusOK {
			t.Fatalf("dose: %d", w.Code)
		}
		if len(pub.topics) != 0 {
			t.Fatalf("a %s set must never republish (reuse forever), got %v", status, pub.topics)
		}
	}
}

// staleRequestedRow mints a bank row whose request day is over: the batch
// was requested on an EARLIER UTC day and its terminal never landed (pod
// death / mesh 403 window) — the walk-found "node bricked forever" shape.
func staleRequestedRow(t *testing.T) *cq.QuestionSet {
	t.Helper()
	row, err := cq.New(testTenant, testGCID, campConceptID, "cspo-basics", 1, time.Now().UTC().Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("cq.New: %v", err)
	}
	if err := row.MarkRequested("assist-lost", time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("MarkRequested: %v", err)
	}
	return row
}

func TestDoseCampaign_StaleRequestedRowRerequests(t *testing.T) {
	srv, bank, _, _, pub := qgenServer(t)
	bank.rows = map[string]*cq.QuestionSet{qgenKey(campConceptID, 1): staleRequestedRow(t)}

	if w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil); w.Code != http.StatusOK {
		t.Fatalf("dose: %d", w.Code)
	}
	if len(pub.topics) != 1 || pub.topics[0] != "chora.creation.ai_assist.started.v2" {
		t.Fatalf("a stale REQUESTED set (terminal lost) must re-request, got %v", pub.topics)
	}
	got := bank.rows[qgenKey(campConceptID, 1)]
	if got == nil || got.GenerationStatus != cq.StatusRequested ||
		got.AssistID == "assist-lost" || got.AssistID != pub.payloads[0]["assist_id"] {
		t.Fatalf("re-request must re-key the row to a fresh assist id, got %+v", got)
	}
}

func TestDoseCampaign_StaleRequestedStillDailyCapped(t *testing.T) {
	srv, bank, _, _, pub := qgenServer(t)
	bank.rows = map[string]*cq.QuestionSet{qgenKey(campConceptID, 1): staleRequestedRow(t)}
	bank.countToday = cq.DefaultDailyMarchCap

	if w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil); w.Code != http.StatusOK {
		t.Fatalf("dose: %d", w.Code)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("a stale re-request must still respect the D13 daily cap, got %v", pub.topics)
	}
}

// ---------- AC4: fail-soft compose, honest failure ----------

func TestDoseCampaign_PublishFailureFailsSoftAndMarksFailed(t *testing.T) {
	srv, bank, _, _, pub := qgenServer(t)
	pub.err = context.DeadlineExceeded

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("a publish failure must never break the dose, got %d", w.Code)
	}
	row := bank.rows[qgenKey(campConceptID, 1)]
	if row == nil || row.GenerationStatus != cq.StatusFailed ||
		!strings.Contains(row.FailureReason, "publish") {
		t.Fatalf("a failed publish must be recorded honestly, got %+v", row)
	}
}
