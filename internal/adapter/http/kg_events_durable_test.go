// kg_events_durable_test.go — ADR-204 Slice F.2: KG mutation events must be
// DURABLE. NewExtServer builds KGEvents over the in-memory publisher; cmd/server
// swaps ext.Publisher to the outbox publisher for weakness but (pre-F.2) never
// rebuilt KGEvents, so every KG mutation event was buffered in process and
// silently dropped. RewireKGEvents rebinds KGEvents (and the junction detector)
// to the durable outbox publisher — mirrors the weakness.grown durability path.
package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

func TestExtServer_RewireKGEvents_KGMutationEnqueuesToOutbox(t *testing.T) {
	srv := NewExtServer(nil)

	// Stand up the SAME durable path cmd/server wires: an outbox-backed
	// Publisher over an InMemoryStore (the dispatcher drains this store).
	store := outbox.NewInMemoryStore()
	srv.Publisher = outbox.NewPublisher(outbox.PublisherConfig{
		Store:         store,
		AggregateType: "learner_weakness",
	})
	srv.RewireKGEvents(events.NewKGPublisher(srv.Publisher))

	// A real KG mutation through the handler: archive a cluster.
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive = %d body=%s", w.Code, w.Body.String())
	}

	// The archived event must be DURABLY enqueued in the outbox store.
	rows, err := store.FetchPending(context.Background(), 100)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Topic == events.TopicKGMapClusterArchived {
			found = true
		}
	}
	if !found {
		t.Fatalf("KG mutation did not enqueue to the durable outbox; pending rows=%d", len(rows))
	}
}

// Without RewireKGEvents the mutation lands on the ORIGINAL in-memory KGEvents
// (from NewExtServer), NOT the durable publisher — the bug F.2 fixes. This
// pins the contract: rebinding ext.Publisher alone is insufficient.
func TestExtServer_KGEvents_NotDurableUntilRewired(t *testing.T) {
	srv := NewExtServer(nil)
	store := outbox.NewInMemoryStore()
	srv.Publisher = outbox.NewPublisher(outbox.PublisherConfig{Store: store, AggregateType: "learner_weakness"})
	// NOTE: deliberately NOT calling RewireKGEvents.

	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive = %d body=%s", w.Code, w.Body.String())
	}
	rows, _ := store.FetchPending(context.Background(), 100)
	if len(rows) != 0 {
		t.Fatalf("KG mutation reached the outbox without RewireKGEvents (rows=%d); the field swap must be required", len(rows))
	}
}
