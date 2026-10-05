package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

type fakeSeenMarker struct {
	err        error
	calls      int
	gotTenant  string
	gotGCID    string
	gotEntryID string
	gotAt      time.Time
}

func (f *fakeSeenMarker) MarkSeen(_ context.Context, tenantID, gcid, entryID string, at time.Time) error {
	f.calls++
	f.gotTenant, f.gotGCID, f.gotEntryID, f.gotAt = tenantID, gcid, entryID, at
	return f.err
}

const (
	seenTenant = "11111111-1111-7111-8111-111111111111"
	seenGCID   = "22222222-2222-7222-8222-222222222222"
)

func seenServer(m st.SeenMarker) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.TranscriptSeen = m
	return ext
}

func seenReq(path string, withCtx bool) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	if withCtx {
		r.Header.Set("X-Tenant-Id", seenTenant)
		r.Header.Set("gcid", seenGCID)
	}
	return r
}

func TestTranscriptSeen_MarksTheEntry(t *testing.T) {
	m := &fakeSeenMarker{}
	w := httptest.NewRecorder()
	seenServer(m).Routes().ServeHTTP(w, seenReq("/v1/me/transcript/entry-1:seen", true))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", w.Code, w.Body.String())
	}
	if m.calls != 1 {
		t.Fatalf("MarkSeen called %d times, want 1", m.calls)
	}
	if m.gotEntryID != "entry-1" {
		t.Errorf("entry = %q, want entry-1", m.gotEntryID)
	}
	if m.gotAt.IsZero() {
		t.Error("the handler must supply a non-zero timestamp")
	}
}

func TestTranscriptSeen_ScopesToTheCallersOwnIdentity(t *testing.T) {
	// The positive control on identity. Both the tenant and the learner come
	// from verified headers; neither may be taken from the path or the query,
	// or one learner could mark another's result as read.
	m := &fakeSeenMarker{}
	w := httptest.NewRecorder()
	seenServer(m).Routes().ServeHTTP(w,
		seenReq("/v1/me/transcript/entry-1:seen?gcid=99999999-9999-7999-8999-999999999999", true))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if m.gotGCID != seenGCID {
		t.Errorf("marked for %q, want the header gcid", m.gotGCID)
	}
	if m.gotTenant != seenTenant {
		t.Errorf("tenant = %q, want the header tenant", m.gotTenant)
	}
}

func TestTranscriptSeen_SecondTapIsStillSuccess(t *testing.T) {
	// Already-seen is the normal second tap. The repository reports it as
	// success, and the handler must not invent an error for it.
	m := &fakeSeenMarker{}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		seenServer(m).Routes().ServeHTTP(w, seenReq("/v1/me/transcript/entry-1:seen", true))
		if w.Code != http.StatusNoContent {
			t.Fatalf("tap %d: status = %d, want 204", i+1, w.Code)
		}
	}
}

func TestTranscriptSeen_MissingEntryIDIs404(t *testing.T) {
	m := &fakeSeenMarker{}
	w := httptest.NewRecorder()
	seenServer(m).Routes().ServeHTTP(w, seenReq("/v1/me/transcript/:seen", true))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", w.Code, w.Body.String())
	}
	if m.calls != 0 {
		t.Error("a malformed path must not reach the repository")
	}
}

func TestTranscriptSeen_UnknownSubpathIs404(t *testing.T) {
	// The subtree mount must not swallow paths it does not own, or a future
	// /v1/me/transcript/<something-else> would silently mark things seen.
	m := &fakeSeenMarker{}
	w := httptest.NewRecorder()
	seenServer(m).Routes().ServeHTTP(w, seenReq("/v1/me/transcript/entry-1:archive", true))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if m.calls != 0 {
		t.Error("an unknown action must not reach the repository")
	}
}

func TestTranscriptSeen_WrongMethodIs405(t *testing.T) {
	m := &fakeSeenMarker{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/transcript/entry-1:seen", nil)
	r.Header.Set("X-Tenant-Id", seenTenant)
	r.Header.Set("gcid", seenGCID)
	seenServer(m).Routes().ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestTranscriptSeen_MissingContextIs400(t *testing.T) {
	m := &fakeSeenMarker{}
	w := httptest.NewRecorder()
	seenServer(m).Routes().ServeHTTP(w, seenReq("/v1/me/transcript/entry-1:seen", false))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if m.calls != 0 {
		t.Error("a request with no identity must not reach the repository")
	}
}

func TestTranscriptSeen_UnwiredPortIs503(t *testing.T) {
	ext := httpadapter.NewExtServer(nil)
	w := httptest.NewRecorder()
	ext.Routes().ServeHTTP(w, seenReq("/v1/me/transcript/entry-1:seen", true))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", w.Code, w.Body.String())
	}
}

func TestTranscriptSeen_RepositoryErrorIs500(t *testing.T) {
	m := &fakeSeenMarker{err: errors.New("deadlock detected")}
	w := httptest.NewRecorder()
	seenServer(m).Routes().ServeHTTP(w, seenReq("/v1/me/transcript/entry-1:seen", true))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestTranscriptList_CarriesTheUnseenFlagAndCount(t *testing.T) {
	// The home card needs both: per-entry so it can render, and a roll-up so
	// it can badge without the client counting.
	seen := time.Now().UTC()
	repo := &seenListRepo{entries: []*st.TranscriptEntry{
		{EntryID: "a", TenantID: seenTenant, GCID: seenGCID, Kind: st.KindAssessment, OccurredAt: seen},
		{EntryID: "b", TenantID: seenTenant, GCID: seenGCID, Kind: st.KindAssessment, OccurredAt: seen, SeenAt: &seen},
	}}
	ext := httpadapter.NewExtServer(nil)
	ext.Transcript = repo

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/transcript", nil)
	r.Header.Set("X-Tenant-Id", seenTenant)
	r.Header.Set("gcid", seenGCID)
	ext.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var body struct {
		UnseenCount int `json:"unseen_count"`
		Items       []struct {
			EntryID string  `json:"entry_id"`
			Unseen  bool    `json:"unseen"`
			SeenAt  *string `json:"seen_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	if body.UnseenCount != 1 {
		t.Errorf("unseen_count = %d, want 1", body.UnseenCount)
	}
	if len(body.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(body.Items))
	}
	if !body.Items[0].Unseen || body.Items[0].SeenAt != nil {
		t.Errorf("item a must be unseen with a null seen_at, got %+v", body.Items[0])
	}
	if body.Items[1].Unseen || body.Items[1].SeenAt == nil {
		t.Errorf("item b must be seen with a seen_at, got %+v", body.Items[1])
	}
}

// seenListRepo is a minimal st.Repository for the list assertion above.
type seenListRepo struct {
	entries      []*st.TranscriptEntry
	byAssessment []*st.TranscriptEntry
}

func (r *seenListRepo) Upsert(context.Context, *st.TranscriptEntry) error { return nil }

// ListByGCID HONOURS the limit, as the pg repo does (`LIMIT $3`). A fake that
// ignored it could not catch a cap defect, and that is exactly how a page count
// passed for a whole-transcript roll-up.
func (r *seenListRepo) ListByGCID(_ context.Context, _, _ string, limit int) ([]*st.TranscriptEntry, error) {
	if limit > 0 && len(r.entries) > limit {
		return r.entries[:limit], nil
	}
	return r.entries, nil
}
// CountUnseenByGCID counts every entry the fake holds, deliberately WITHOUT
// applying the page limit ListByGCID applies. That asymmetry is the whole
// point: the badge counts the transcript, the list returns a page.
func (r *seenListRepo) CountUnseenByGCID(context.Context, string, string) (int, error) {
	return st.CountUnseen(r.entries), nil
}

func (r *seenListRepo) ListByAssessmentIDs(context.Context, string, []string) ([]*st.TranscriptEntry, error) {
	return r.byAssessment, nil
}

func TestTranscriptByAssessments_NeverClaimsUnseen(t *testing.T) {
	// The cross-learner gradebook read does not select seen_at, so every entry
	// arrives with a nil stamp. Left to the shared mapper, Unseen() would then
	// report TRUE for every student on the roster: not a leak, a confident lie.
	// This pins that the instructor read carries neither field as a claim.
	repo := &seenListRepo{byAssessment: []*st.TranscriptEntry{
		{EntryID: "a", TenantID: seenTenant, GCID: "learner-1", Kind: st.KindAssessment},
		{EntryID: "b", TenantID: seenTenant, GCID: "learner-2", Kind: st.KindAssessment},
	}}
	ext := httpadapter.NewExtServer(nil)
	ext.Transcript = repo

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/transcript/by-assessments?assessment_ids=as-1", nil)
	r.Header.Set("X-Tenant-Id", seenTenant)
	r.Header.Set("x-mesh-user-roles", "instructor")
	ext.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			Unseen bool    `json:"unseen"`
			SeenAt *string `json:"seen_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(body.Items))
	}
	for i, it := range body.Items {
		if it.Unseen {
			t.Errorf("item %d claims unseen; the gradebook read cannot know that", i)
		}
		if it.SeenAt != nil {
			t.Errorf("item %d carries a seen_at; the gradebook read must not", i)
		}
	}
}
