// growth_edge_uploads_mana_test.go — WS-4 (CHO-1956) RED tests for the upfront
// mana reserve at the Growth-Edge upload door (ADR-205 D6).
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- gate fakes (implement the weakness_upload ports) ------------------------

type gateCompanion struct {
	has bool
	err error
}

func (g gateCompanion) HasActiveCompanion(context.Context, string, string) (bool, error) {
	return g.has, g.err
}

type gateReserver struct {
	res   wu.ReserveResult
	err   error
	calls int
	got   wu.ReserveInput
}

func (g *gateReserver) ReserveAnalysis(_ context.Context, in wu.ReserveInput) (wu.ReserveResult, error) {
	g.calls++
	g.got = in
	return g.res, g.err
}

func ueServerWithGate(repo wu.Repository, up storage.BlobUploader, gate *wu.ManaGate) *httpadapter.ExtServer {
	ext := ueServer(repo, up)
	ext.WeaknessMana = gate
	return ext
}

// Disabled gate (default): no reservation, no mana fields, event reservation_id
// empty — today's behaviour preserved.
func TestUpload_ManaDisabled_NoReservation(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{}) // no gate ⇒ nil ⇒ disabled
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["mana_reserved"]; ok {
		t.Errorf("disabled gate must omit mana_reserved, resp=%v", resp)
	}
	evs := srv.Publisher.(*events.InMemoryPublisher).Events()
	if rid, _ := evs[0].Payload["reservation_id"].(string); rid != "" {
		t.Errorf("disabled gate must publish empty reservation_id, got %q", rid)
	}
}

// Enabled + active Companion + affordable: reserves, stamps reservation_id on the
// event + DTO, exposes the price, and forwards the real upload_id to the reserver.
func TestUpload_ManaEnabled_ReservesAndStamps(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	reserver := &gateReserver{res: wu.ReserveResult{ReservationID: "rsv-77", PriceUnits: 50}}
	gate := wu.NewManaGate(true, gateCompanion{has: true}, reserver)
	srv := ueServerWithGate(repo, up, gate)
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["reservation_id"] != "rsv-77" {
		t.Errorf("DTO reservation_id=%v want rsv-77", resp["reservation_id"])
	}
	if resp["mana_reserved"] != float64(50) {
		t.Errorf("DTO mana_reserved=%v want 50", resp["mana_reserved"])
	}
	// reservation_id stamped on the published event (proto field 12).
	evs := srv.Publisher.(*events.InMemoryPublisher).Events()
	if evs[0].Payload["reservation_id"] != "rsv-77" {
		t.Errorf("event reservation_id=%v want rsv-77", evs[0].Payload["reservation_id"])
	}
	// reserver received the SAME upload_id minted for the job/event.
	if reserver.got.UploadID != repo.inserted[0].UploadID {
		t.Errorf("reserver upload_id=%q != job upload_id=%q", reserver.got.UploadID, repo.inserted[0].UploadID)
	}
	if reserver.got.TenantID != geTenant || reserver.got.GCID != geGCID {
		t.Errorf("reserver identity = %+v", reserver.got)
	}
}

// Enabled + no Companion: 402, and NOTHING happens (no blob, no job, no event) —
// the gate runs BEFORE the side effects so a refused upload leaves no orphans.
func TestUpload_ManaEnabled_NoCompanion_402_NoSideEffects(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	reserver := &gateReserver{}
	gate := wu.NewManaGate(true, gateCompanion{has: false}, reserver)
	srv := ueServerWithGate(repo, up, gate)
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d want 402", w.Code)
	}
	if up.called {
		t.Error("no blob must be uploaded when the Companion gate refuses")
	}
	if len(repo.inserted) != 0 {
		t.Error("no job must be inserted when the Companion gate refuses")
	}
	if len(srv.Publisher.(*events.InMemoryPublisher).Events()) != 0 {
		t.Error("no event must be published when the Companion gate refuses")
	}
	if reserver.calls != 0 {
		t.Error("reserver must NOT be called when no Companion")
	}
}

// Enabled + insufficient mana: 402, no side effects.
func TestUpload_ManaEnabled_InsufficientMana_402(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	reserver := &gateReserver{err: &wu.ErrInsufficientMana{RequiredUnits: 50, CurrentBalance: 5}}
	gate := wu.NewManaGate(true, gateCompanion{has: true}, reserver)
	srv := ueServerWithGate(repo, up, gate)
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d want 402 body=%s", w.Code, w.Body.String())
	}
	if up.called || len(repo.inserted) != 0 {
		t.Error("insufficient mana must not produce blob/job side effects")
	}
}

// Enabled but ports unwired (misconfig): 503, never a silent free pass.
func TestUpload_ManaEnabled_Misconfigured_503(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	gate := wu.NewManaGate(true, nil, nil) // enabled, no ports
	srv := ueServerWithGate(repo, up, gate)
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
	if up.called {
		t.Error("misconfig must not upload a blob (fail-loud before side effects)")
	}
}
