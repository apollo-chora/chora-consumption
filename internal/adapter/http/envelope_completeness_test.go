// envelope_completeness_test.go — assert mandatory envelope fields are
// populated on every event published by the S4.2 /v1/me/* endpoints.
//
// Per .claude/rules/ddd-enforcement.md "Event envelope mandatory fields":
//
//	event_id, idempotency_key, tenant_id, gcid, occurred_at, published_at,
//	traceparent, tracestate, source_project, source_service, schema_version
//
// And: W3C traceparent must propagate from the inbound HTTP request
// header into the event envelope.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

const incomingTraceparent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"

func TestEnvelope_StartedEventCarriesAllMandatoryFields(t *testing.T) {
	srv := newMeServer()
	pub, ok := srv.Publisher.(*events.InMemoryPublisher)
	if !ok {
		t.Fatal("publisher must be in-memory for this test")
	}

	r := httptest.NewRequest("POST", "/v1/me/atom-sessions",
		bytes.NewBufferString(`{"atom_id":"`+meAtomID+`"}`))
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	r.Header.Set("traceparent", incomingTraceparent)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}

	evs := pub.Events()
	if len(evs) == 0 {
		t.Fatal("no events published")
	}
	ev := evs[0]
	if ev.Topic != events.TopicAtomSessionStarted {
		t.Errorf("topic = %q; want %q", ev.Topic, events.TopicAtomSessionStarted)
	}
	assertEnvelope(t, ev.Envelope)
	if ev.Envelope.Traceparent != incomingTraceparent {
		t.Errorf("traceparent = %q; want %q (W3C propagation)",
			ev.Envelope.Traceparent, incomingTraceparent)
	}
}

func TestEnvelope_CompletedEventCarriesIMDADimension(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-a",
		AnswerCount:     2,
		Status:          atom_index.StatusPublished, // CHO-2273 — learner take gates on Playable()
		PublishedAt:     time.Now().UTC(),
	})
	srv.AtomIndex.Save(context.Background(), a)
	pub, _ := srv.Publisher.(*events.InMemoryPublisher)

	// Start session.
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest("POST", "/v1/me/atom-sessions",
		map[string]string{"atom_id": meAtomID}))
	var sess map[string]any
	json.NewDecoder(w.Body).Decode(&sess)
	sid := sess["session_id"].(string)

	// Submit correct answer.
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, newMeRequest("POST",
		"/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "ans-1", "selected_option_id": "opt-a"}))
	if w2.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", w2.Code, w2.Body.String())
	}

	// Look for the IMDA accountability evidence event.
	imdaSeen := false
	for _, ev := range pub.Events() {
		if ev.Topic != events.TopicAtomSessionCompletedV1 {
			continue
		}
		assertEnvelope(t, ev.Envelope)
		dim, _ := ev.Payload["chora_imda_dimension"].(string)
		if dim == "accountability" {
			imdaSeen = true
		}
	}
	if !imdaSeen {
		t.Error("expected an IMDA accountability evidence event on completion")
	}
}

// TestEnvelope_KGClusterCreatedCarriesIMDA — S5.2 KG events must populate
// envelope mandatory fields and tag IMDA dimension on cluster created.
func TestEnvelope_KGClusterCreatedCarriesIMDA(t *testing.T) {
	srv := NewExtServer(nil)
	pub, ok := srv.Publisher.(*events.InMemoryPublisher)
	if !ok {
		t.Fatal("publisher must be in-memory")
	}
	r := httptest.NewRequest("POST", "/v1/me/knowledge-graph/clusters",
		bytes.NewBufferString(`{"seed_topic":"agile","seed_atom_id":"`+meAtomID+`"}`))
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	r.Header.Set("traceparent", incomingTraceparent)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	imdaSeen := false
	for _, ev := range pub.Events() {
		if ev.Topic != events.TopicKGMapClusterCreated {
			continue
		}
		assertEnvelope(t, ev.Envelope)
		if ev.Envelope.Traceparent != incomingTraceparent {
			t.Errorf("traceparent = %q (W3C propagation broken)", ev.Envelope.Traceparent)
		}
		dim, _ := ev.Payload["chora_imda_dimension"].(string)
		if dim == "accountability" {
			imdaSeen = true
		}
	}
	if !imdaSeen {
		t.Errorf("expected KGMapClusterCreated event with IMDA accountability tag; events=%d", len(pub.Events()))
	}
}

func assertEnvelope(t *testing.T, env events.Envelope) {
	t.Helper()
	if env.EventID == "" {
		t.Error("EventID empty")
	}
	if env.IdempotencyKey == "" {
		t.Error("IdempotencyKey empty")
	}
	if env.TenantID == "" {
		t.Error("TenantID empty")
	}
	if env.OccurredAt.IsZero() {
		t.Error("OccurredAt zero")
	}
	if env.PublishedAt.IsZero() {
		t.Error("PublishedAt zero")
	}
	if env.Traceparent == "" {
		t.Error("Traceparent empty (W3C trace context mandatory)")
	}
	if env.SourceProject == "" {
		t.Error("SourceProject empty")
	}
	if env.SourceService == "" {
		t.Error("SourceService empty")
	}
	if env.SchemaVersion < 1 {
		t.Errorf("SchemaVersion = %d; want >= 1", env.SchemaVersion)
	}
}
