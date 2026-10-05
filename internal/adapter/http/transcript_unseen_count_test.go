// transcript_unseen_count_test.go: the unseen badge counts the learner's
// transcript, not the page it happened to fetch (UX Track U, D2 follow-up to
// package B6 item 2).
//
// The B6 commit message and the code comment both claimed unseen_count was a
// roll-up that "a paged list cannot badge a number that only describes its
// page". The handler computed it with CountUnseen over the slice ListByGCID
// had just returned, and that read is `ORDER BY occurred_at DESC LIMIT $3`
// with the limit defaulting to 50. So the claim was false for exactly the
// learner it mattered most for: the one with a backlog.
package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// unseenBacklog builds n unseen entries, newest first, so the default page of
// 50 cannot hold them all.
func unseenBacklog(n int) []*st.TranscriptEntry {
	out := make([]*st.TranscriptEntry, 0, n)
	base := time.Now().UTC()
	for i := 0; i < n; i++ {
		out = append(out, &st.TranscriptEntry{
			EntryID: fmt.Sprintf("e-%02d", i), TenantID: seenTenant, GCID: seenGCID,
			Kind: st.KindAssessment, OccurredAt: base.Add(-time.Duration(i) * time.Hour),
		})
	}
	return out
}

func TestTranscriptUnseenCount_CountsTheTranscriptNotThePage(t *testing.T) {
	const backlog = 60 // more than the default page of 50
	repo := &seenListRepo{entries: unseenBacklog(backlog)}
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
		UnseenCount int              `json:"unseen_count"`
		Items       []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	// The page is capped. That part is correct and stays.
	if len(body.Items) != 50 {
		t.Fatalf("items = %d, want the capped page of 50", len(body.Items))
	}
	// The badge is not. A learner with 60 unread results must not be told 50.
	if body.UnseenCount != backlog {
		t.Errorf("unseen_count = %d, want %d: the badge is counting the PAGE, not the transcript",
			body.UnseenCount, backlog)
	}
	if body.UnseenCount <= len(body.Items) {
		t.Errorf("unseen_count (%d) never exceeds the page (%d), so nothing here proves a roll-up",
			body.UnseenCount, len(body.Items))
	}
}

// A count the store could not produce is an ERROR, never a zero. Zero is the
// one answer that renders as "you are all caught up", which is precisely the
// false reassurance a failed read must not give.
func TestTranscriptUnseenCount_CountFailureIsNotZero(t *testing.T) {
	repo := &countErrorTranscriptRepo{seenListRepo{entries: unseenBacklog(3)}}
	ext := httpadapter.NewExtServer(nil)
	ext.Transcript = repo

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/transcript", nil)
	r.Header.Set("X-Tenant-Id", seenTenant)
	r.Header.Set("gcid", seenGCID)
	ext.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", w.Code, w.Body.String())
	}
	// A 500 carries an error envelope, not a count. The assertion that matters
	// is simply that the handler refused rather than serving a page with a
	// zero badge beside it.
	if code := errCodeBytes(t, w.Body.Bytes()); code != "TRANSCRIPT_READ_FAILED" {
		t.Errorf("code = %q; want TRANSCRIPT_READ_FAILED", code)
	}
}

// errCodeBytes reads the error envelope's code. Named apart from the package
// http helper of a similar name, which lives in the internal test package.
func errCodeBytes(t *testing.T, b []byte) string {
	t.Helper()
	var env struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, b)
	}
	return env.Code
}

type countErrorTranscriptRepo struct{ seenListRepo }

func (countErrorTranscriptRepo) CountUnseenByGCID(context.Context, string, string) (int, error) {
	return 0, errors.New("connection refused")
}
