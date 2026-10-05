// dose_preferences_event_test.go — WS1 preferences leg producer (ADR-200 /
// CHO-2049): PUT /v1/me/dose-preferences must publish
// chora.consumption.preferences.updated.v1 so the LearnerProfile projection can
// mint a FactPreference. Reuses dosePrefStub (dose_preferences_test.go).
package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

func TestDosePreferences_PUT_PublishesPreferencesUpdated(t *testing.T) {
	srv := NewServer()
	srv.DoseKGPrefs = &dosePrefStub{}
	srv.Goals = nil // skip the ownership gate for this producer-focused test

	req := httptest.NewRequest(http.MethodPut, "/v1/me/dose-preferences",
		bytes.NewBufferString(`{"mapId":"map-123","included":false}`))
	req.Header.Set("X-Tenant-Id", "11111111-1111-1111-1111-111111111111")
	req.Header.Set("gcid", "22222222-2222-2222-2222-222222222222")
	rec := httptest.NewRecorder()

	srv.handleDosePreferences(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}

	pub, ok := srv.Publisher.(*events.InMemoryPublisher)
	if !ok {
		t.Fatalf("expected in-memory publisher, got %T", srv.Publisher)
	}
	var found *events.Event
	for i := range pub.Events() {
		if pub.Events()[i].Topic == events.TopicPreferencesUpdated {
			e := pub.Events()[i]
			found = &e
			break
		}
	}
	if found == nil {
		t.Fatalf("no %s event published", events.TopicPreferencesUpdated)
	}
	if found.Payload["learner_gcid"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("learner_gcid = %v", found.Payload["learner_gcid"])
	}
	prefs, ok := found.Payload["preferences"].([]map[string]any)
	if !ok || len(prefs) != 1 {
		t.Fatalf("preferences payload wrong: %#v", found.Payload["preferences"])
	}
	// Q2 stable key + Q3 excluded value.
	if prefs[0]["key"] != "dose.map.map-123" || prefs[0]["value"] != "excluded" {
		t.Errorf("pref entry wrong: %#v", prefs[0])
	}
	// Envelope carries the verified tenant + gcid (mandatory-field validation
	// already ran inside InMemoryPublisher.Publish).
	if found.Envelope.TenantID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("envelope tenant = %q", found.Envelope.TenantID)
	}
	if found.Envelope.GCID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("envelope gcid = %q", found.Envelope.GCID)
	}
}

// An include (re-include a previously-muted map) publishes value="included" (Q3
// — never a delete).
func TestDosePreferences_PUT_IncludePublishesIncludedValue(t *testing.T) {
	srv := NewServer()
	srv.DoseKGPrefs = &dosePrefStub{}
	srv.Goals = nil

	req := httptest.NewRequest(http.MethodPut, "/v1/me/dose-preferences",
		bytes.NewBufferString(`{"mapId":"map-777","included":true}`))
	req.Header.Set("X-Tenant-Id", "11111111-1111-1111-1111-111111111111")
	req.Header.Set("gcid", "22222222-2222-2222-2222-222222222222")
	rec := httptest.NewRecorder()

	srv.handleDosePreferences(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	pub := srv.Publisher.(*events.InMemoryPublisher)
	evs := pub.Events()
	last := evs[len(evs)-1]
	if last.Topic != events.TopicPreferencesUpdated {
		t.Fatalf("last topic = %q", last.Topic)
	}
	prefs := last.Payload["preferences"].([]map[string]any)
	if prefs[0]["key"] != "dose.map.map-777" || prefs[0]["value"] != "included" {
		t.Errorf("include pref entry wrong: %#v", prefs[0])
	}
}
