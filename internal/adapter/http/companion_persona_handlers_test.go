package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	consumptionmodelarmor "github.com/apollo-chora/chora-consumption/internal/adapter/modelarmor"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---- fakes ----------------------------------------------------------------

// handlerPersonaRepo is a minimal in-memory InstanceRepository for the
// persona handler tests.
type handlerPersonaRepo struct {
	byID      map[string]*companion.Instance
	updateErr error
}

func newHandlerPersonaRepo() *handlerPersonaRepo {
	return &handlerPersonaRepo{byID: map[string]*companion.Instance{}}
}
func (f *handlerPersonaRepo) Create(_ context.Context, inst *companion.Instance, _ int) error {
	f.byID[inst.CompanionID] = inst
	return nil
}
func (f *handlerPersonaRepo) Get(_ context.Context, companionID string) (*companion.Instance, error) {
	inst, ok := f.byID[companionID]
	if !ok {
		return nil, companion.ErrInstanceNotFound
	}
	return inst, nil
}
func (f *handlerPersonaRepo) ListByOwner(_ context.Context, _, _ string) ([]*companion.Instance, error) {
	return nil, nil
}
func (f *handlerPersonaRepo) ListRosterByOwner(_ context.Context, _, _ string) ([]*companion.RosterEntry, error) {
	return nil, nil
}
func (f *handlerPersonaRepo) Update(_ context.Context, inst *companion.Instance) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.byID[inst.CompanionID] = inst
	return nil
}
func (f *handlerPersonaRepo) SoftDelete(_ context.Context, _ string) error { return nil }

// fakePersonaGuard implements the consumption modelarmor.GuardrailPort.
type fakePersonaGuard struct {
	verdict consumptionmodelarmor.ScreenVerdict
	err     error
	calls   int
	lastReq consumptionmodelarmor.ScreenRequest
}

func (g *fakePersonaGuard) Screen(_ context.Context, req consumptionmodelarmor.ScreenRequest) (consumptionmodelarmor.ScreenVerdict, error) {
	g.calls++
	g.lastReq = req
	return g.verdict, g.err
}

func approvedGuard() *fakePersonaGuard {
	return &fakePersonaGuard{verdict: consumptionmodelarmor.ScreenVerdict{Verdict: consumptionmodelarmor.VerdictApproved}}
}

// fakePersonaOutbox captures the persona_updated emit (companion.LoadoutOutbox).
type fakePersonaOutbox struct {
	calls   int
	topic   string
	payload map[string]any
	err     error
}

func (f *fakePersonaOutbox) PublishLoadoutEvent(_ context.Context, topic string, payload map[string]any, _ companion.LoadoutEnvelope) error {
	f.calls++
	f.topic = topic
	f.payload = payload
	return f.err
}

// ---- harness --------------------------------------------------------------

func seedPersonaServer(t *testing.T, companionID, ownerGCID string, guard consumptionmodelarmor.GuardrailPort) (*Server, *handlerPersonaRepo) {
	t.Helper()
	repo := newHandlerPersonaRepo()
	if companionID != "" {
		repo.byID[companionID] = &companion.Instance{
			CompanionID:     companionID,
			TenantID:        "tenant-1",
			OwnerGCID:       ownerGCID,
			ConfiguredRules: map[string]string{},
		}
	}
	svc, err := companion.NewPersonaService(repo)
	if err != nil {
		t.Fatalf("NewPersonaService: %v", err)
	}
	srv := NewServer()
	srv.Persona = svc
	srv.PersonaGuardrail = guard
	return srv, repo
}

func doPersonaReq(t *testing.T, srv *Server, method, path string, body any, tenantID, gcid string) *http.Response {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, buf)
	if tenantID != "" {
		r.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	r.Header.Set("traceparent", "00-aaa-bbb-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	return w.Result()
}

func validPersonaBody() map[string]any {
	return map[string]any{
		"tone":                 "encouraging",
		"hintProgression":      "ladder",
		"maxHintsBeforeReveal": 2,
		"difficultyCap":        "intermediate",
		"language":             "en",
		"citationStrictness":   "strict",
		"archetype":            "curious-explorer",
		"addressStyle":         "first_name",
		"interestChips":        []string{"space", "dinosaurs"},
		"guidanceNote":         "Use space analogies when you can.",
	}
}

// ---- GET ------------------------------------------------------------------

func TestGetPersona_HappyPath_DefaultView(t *testing.T) {
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	res := doPersonaReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/persona", nil, "tenant-1", "owner-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	var got personaViewDTO
	json.NewDecoder(res.Body).Decode(&got)
	if got.Tone != "encouraging" || got.DifficultyCap != "intermediate" || got.CitationStrictness != "strict" {
		t.Errorf("default view = %+v; want encouraging/intermediate/strict", got)
	}
	if got.Version != 0 {
		t.Errorf("Version = %d; want 0 on never-edited", got.Version)
	}
	if got.InterestChips == nil {
		t.Error("InterestChips = nil; want non-nil (empty array) in JSON")
	}
}

func TestGetPersona_NotFound(t *testing.T) {
	srv, _ := seedPersonaServer(t, "", "", approvedGuard())
	res := doPersonaReq(t, srv, http.MethodGet, "/v1/me/companions/missing/persona", nil, "tenant-1", "owner-1")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", res.StatusCode)
	}
}

func TestGetPersona_MissingContext_401(t *testing.T) {
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	res := doPersonaReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/persona", nil, "", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", res.StatusCode)
	}
}

func TestGetPersona_NotWired_503(t *testing.T) {
	srv := NewServer() // Persona nil
	res := doPersonaReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/persona", nil, "tenant-1", "owner-1")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", res.StatusCode)
	}
}

// ---- PUT ------------------------------------------------------------------

func TestUpdatePersona_HappyPath_ScreensAndPersists(t *testing.T) {
	guard := approvedGuard()
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", guard)
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	var got personaViewDTO
	json.NewDecoder(res.Body).Decode(&got)
	if got.Version != 1 {
		t.Errorf("Version = %d; want 1 after first save", got.Version)
	}
	if got.GuidanceNote != "Use space analogies when you can." {
		t.Errorf("GuidanceNote = %q; unexpected", got.GuidanceNote)
	}
	if guard.calls != 1 {
		t.Errorf("guard.calls = %d; want 1 (note screened)", guard.calls)
	}
	if guard.lastReq.AgentID != consumptionmodelarmor.AgentIDCompanionPersona {
		t.Errorf("screen agent_id = %q; want companion_persona", guard.lastReq.AgentID)
	}
	if repo.byID["fam-1"].PersonaVersion != 1 {
		t.Errorf("persisted PersonaVersion = %d; want 1", repo.byID["fam-1"].PersonaVersion)
	}
}

func TestUpdatePersona_InvalidShape_422(t *testing.T) {
	guard := approvedGuard()
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", guard)
	body := validPersonaBody()
	body["tone"] = "sarcastic" // not in the bounded grammar
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", body, "tenant-1", "owner-1")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", res.StatusCode)
	}
	if guard.calls != 0 {
		t.Errorf("guard.calls = %d; want 0 — reject bad shape before spending an Armor call", guard.calls)
	}
}

func TestUpdatePersona_NoteBlocked_422(t *testing.T) {
	guard := &fakePersonaGuard{verdict: consumptionmodelarmor.ScreenVerdict{
		Verdict: consumptionmodelarmor.VerdictBlocked,
		Reason:  "rai:HATE_SPEECH",
	}}
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", guard)
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (note blocked)", res.StatusCode)
	}
	// A blocked note must persist NOTHING.
	if repo.byID["fam-1"].PersonaVersion != 0 {
		t.Errorf("PersonaVersion = %d; want 0 — a blocked note must not persist", repo.byID["fam-1"].PersonaVersion)
	}
}

func TestUpdatePersona_NonEmptyNote_GuardrailNotWired_503(t *testing.T) {
	// Never persist an unscreened note into a kid-facing system prompt.
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", nil)
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503 (guardrail not wired + non-empty note)", res.StatusCode)
	}
	if repo.byID["fam-1"].PersonaVersion != 0 {
		t.Errorf("PersonaVersion = %d; want 0 — unscreened note must not persist", repo.byID["fam-1"].PersonaVersion)
	}
}

func TestUpdatePersona_EmptyNote_NoGuardrail_OK(t *testing.T) {
	// An empty note is trivially safe — no screening needed, so a nil
	// guardrail must NOT block a knob-only edit.
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", nil)
	body := validPersonaBody()
	body["guidanceNote"] = ""
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", body, "tenant-1", "owner-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 (empty note needs no screening)", res.StatusCode)
	}
	if repo.byID["fam-1"].PersonaVersion != 1 {
		t.Errorf("PersonaVersion = %d; want 1", repo.byID["fam-1"].PersonaVersion)
	}
}

func TestUpdatePersona_WrongOwner_404(t *testing.T) {
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "intruder")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (non-owner indistinguishable from missing)", res.StatusCode)
	}
}

func TestUpdatePersona_BadJSON_400(t *testing.T) {
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	r := httptest.NewRequest(http.MethodPut, "/v1/me/companions/fam-1/persona", bytes.NewReader([]byte("{not json")))
	r.Header.Set("X-Tenant-Id", "tenant-1")
	r.Header.Set("gcid", "owner-1")
	r.Header.Set("traceparent", "00-aaa-bbb-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Result().StatusCode)
	}
}

func TestPersona_MethodNotAllowed_405(t *testing.T) {
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	res := doPersonaReq(t, srv, http.MethodPost, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", res.StatusCode)
	}
}

func TestUpdatePersona_ScreenError_502(t *testing.T) {
	// A Model Armor call that ERRORS (vs blocks) is a fail-loud 502 — we
	// refuse to persist a note we could not screen.
	guard := &fakePersonaGuard{err: errors.New("armor down")}
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", guard)
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d; want 502 (screen failed)", res.StatusCode)
	}
	if repo.byID["fam-1"].PersonaVersion != 0 {
		t.Errorf("PersonaVersion = %d; want 0 — an unscreenable note must not persist", repo.byID["fam-1"].PersonaVersion)
	}
}

func TestUpdatePersona_EmitsPersonaUpdatedEvent(t *testing.T) {
	srv, _ := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	ob := &fakePersonaOutbox{}
	srv.LoadoutEvents = ob
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", res.StatusCode)
	}
	if ob.calls != 1 {
		t.Fatalf("emit calls = %d; want 1", ob.calls)
	}
	if ob.topic != "chora.consumption.companion.persona_updated.v1" {
		t.Errorf("topic = %q", ob.topic)
	}
	if ob.payload["persona_version"] != 1 {
		t.Errorf("persona_version = %v; want 1", ob.payload["persona_version"])
	}
	if ob.payload["has_guidance_note"] != true {
		t.Errorf("has_guidance_note = %v; want true (validPersonaBody has a note)", ob.payload["has_guidance_note"])
	}
	// PRIVACY: the note TEXT must never ride the event payload.
	if _, leaked := ob.payload["guidance_note"]; leaked {
		t.Errorf("guidance_note TEXT leaked onto the event payload")
	}
}

func TestUpdatePersona_EmitFailure_DoesNotFailSave(t *testing.T) {
	// Best-effort: a failed enqueue of the consumer-less event must NOT fail
	// the learner's persona save.
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", approvedGuard())
	srv.LoadoutEvents = &fakePersonaOutbox{err: errors.New("outbox down")}
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 (emit failure must be non-fatal)", res.StatusCode)
	}
	if repo.byID["fam-1"].PersonaVersion != 1 {
		t.Errorf("save must persist despite emit failure; version = %d", repo.byID["fam-1"].PersonaVersion)
	}
}

func TestUpdatePersona_RepoError_500(t *testing.T) {
	// A repo failure (not a domain sentinel) surfaces as a fail-loud 500.
	guard := approvedGuard()
	srv, repo := seedPersonaServer(t, "fam-1", "owner-1", guard)
	repo.updateErr = errors.New("pg down")
	res := doPersonaReq(t, srv, http.MethodPut, "/v1/me/companions/fam-1/persona", validPersonaBody(), "tenant-1", "owner-1")
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (repo error)", res.StatusCode)
	}
}
