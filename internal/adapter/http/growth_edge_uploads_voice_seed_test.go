package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// growth_edge_uploads_voice_seed_test.go - ADR-254 D4 (W4 consumption cut):
// the upload door seeds the diagnosis crew's Companion VOICE with two JSON keys
// on weakness_doc.uploaded.v1, `familiar_id` (the pre-rename wire name inside
// the kennel lane) + `companion_name`, valued as they stand at upload time: the
// learner's first HATCHED Companion; both present and EMPTY when the learner
// has none (no Companion = no voice, never a fabricated id).

type voiceRosterStub struct {
	companion.InstanceRepository
	entries []*companion.RosterEntry
	err     error
}

func (s *voiceRosterStub) ListRosterByOwner(context.Context, string, string) ([]*companion.RosterEntry, error) {
	return s.entries, s.err
}

func TestUploadGrowthEdgeDoc_SeedsCompanionVoiceKeys(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	srv := ueServer(repo, up)
	srv.CompanionInstances = &voiceRosterStub{entries: []*companion.RosterEntry{
		{Instance: &companion.Instance{CompanionID: "egg-1", Name: "Shell"}, Growth: &companion.GrowthSnapshot{Stage: 0}},
		{Instance: &companion.Instance{CompanionID: "comp-7", Name: "Ember"}, Growth: &companion.GrowthSnapshot{Stage: 3}},
	}}
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	evs := srv.Publisher.(*events.InMemoryPublisher).Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events want 1", len(evs))
	}
	if evs[0].Payload["familiar_id"] != "comp-7" || evs[0].Payload["companion_name"] != "Ember" {
		t.Fatalf("voice seed = familiar_id %v companion_name %v (want the first HATCHED Companion, not the egg)",
			evs[0].Payload["familiar_id"], evs[0].Payload["companion_name"])
	}
}

func TestUploadGrowthEdgeDoc_NoCompanionMeansEmptyVoiceKeys(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	srv := ueServer(repo, up)
	srv.CompanionInstances = &voiceRosterStub{}
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	evs := srv.Publisher.(*events.InMemoryPublisher).Events()
	fid, ok1 := evs[0].Payload["familiar_id"]
	name, ok2 := evs[0].Payload["companion_name"]
	if !ok1 || !ok2 || fid != "" || name != "" {
		t.Fatalf("no Companion must publish EMPTY familiar_id + companion_name (present, empty): %v", evs[0].Payload)
	}
}
