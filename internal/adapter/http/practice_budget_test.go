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
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
)

// fakeBudgetCounter records what was asked and returns scripted counts.
type fakeBudgetCounter struct {
	byOrigin  map[cq.RequestOrigin]int
	err       error
	calls     int
	gotTenant string
	gotGCID   string
	gotDay    time.Time
}

func (f *fakeBudgetCounter) CountRequestedOn(_ context.Context, tenantID, learnerGCID string, origin cq.RequestOrigin, day time.Time) (int, error) {
	f.calls++
	f.gotTenant, f.gotGCID, f.gotDay = tenantID, learnerGCID, day
	if f.err != nil {
		return 0, f.err
	}
	return f.byOrigin[origin], nil
}

const (
	budgetTenant = "11111111-1111-7111-8111-111111111111"
	budgetGCID   = "22222222-2222-7222-8222-222222222222"
)

func budgetServer(c httpadapter.PracticeBudgetCounter) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.PracticeBudget = c
	return ext
}

func budgetReq(withCtx bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/me/practice-budget", nil)
	if withCtx {
		r.Header.Set("X-Tenant-Id", budgetTenant)
		r.Header.Set("gcid", budgetGCID)
	}
	return r
}

type budgetBody struct {
	ResetsAt string `json:"resets_at"`
	Budgets  []struct {
		Origin    string `json:"origin"`
		Cap       int    `json:"cap"`
		Used      int    `json:"used"`
		Remaining int    `json:"remaining"`
		Exhausted bool   `json:"exhausted"`
	} `json:"budgets"`
}

func decodeBudget(t *testing.T, w *httptest.ResponseRecorder) budgetBody {
	t.Helper()
	var b budgetBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return b
}

func TestPracticeBudget_ReportsBothOrigins(t *testing.T) {
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{cq.OriginTap: 1, cq.OriginMarch: 1}}
	w := httptest.NewRecorder()
	budgetServer(c).Routes().ServeHTTP(w, budgetReq(true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	b := decodeBudget(t, w)
	if len(b.Budgets) != 2 {
		t.Fatalf("got %d budgets, want 2 (march and tap are independent)", len(b.Budgets))
	}
	byOrigin := map[string]int{}
	for _, x := range b.Budgets {
		byOrigin[x.Origin] = x.Remaining
	}
	if byOrigin["tap"] != cq.DefaultDailyTapCap-1 {
		t.Errorf("tap remaining = %d, want %d", byOrigin["tap"], cq.DefaultDailyTapCap-1)
	}
	if byOrigin["march"] != 0 {
		t.Errorf("march remaining = %d, want 0", byOrigin["march"])
	}
}

func TestPracticeBudget_ASpentMarchDoesNotStarveTheTap(t *testing.T) {
	// The two budgets are independent by design. If the read model ever
	// collapsed them, the card would tell a learner they are out of practice
	// taps because the automatic dose ran, which is the opposite of the rule.
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{cq.OriginMarch: cq.DefaultDailyMarchCap}}
	w := httptest.NewRecorder()
	budgetServer(c).Routes().ServeHTTP(w, budgetReq(true))

	b := decodeBudget(t, w)
	for _, x := range b.Budgets {
		if x.Origin == "tap" {
			if x.Remaining != cq.DefaultDailyTapCap || x.Exhausted {
				t.Errorf("tap budget = %+v, want a full untouched budget", x)
			}
		}
		if x.Origin == "march" && !x.Exhausted {
			t.Error("a spent march must report Exhausted")
		}
	}
}

func TestPracticeBudget_ScopesToTheCallersOwnIdentity(t *testing.T) {
	// The positive control on identity: both halves come from verified
	// headers, never from the query, or a learner could read another's budget.
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{}}
	w := httptest.NewRecorder()
	r := budgetReq(true)
	r.URL.RawQuery = "gcid=99999999-9999-7999-8999-999999999999"
	budgetServer(c).Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if c.gotGCID != budgetGCID {
		t.Errorf("counted for %q, want the header gcid", c.gotGCID)
	}
	if c.gotTenant != budgetTenant {
		t.Errorf("tenant = %q, want the header tenant", c.gotTenant)
	}
}

func TestPracticeBudget_CountsAgainstTheCurrentUTCDay(t *testing.T) {
	// The cap is a per-UTC-day bound. Passing a local-zone day would reset a
	// learner's budget at the wrong hour, in one direction or the other
	// depending on where the pod runs.
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{}}
	w := httptest.NewRecorder()
	budgetServer(c).Routes().ServeHTTP(w, budgetReq(true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if c.gotDay.Location() != time.UTC {
		t.Errorf("counted against %v, want a UTC instant", c.gotDay.Location())
	}
}

func TestPracticeBudget_ResetsAtIsTheNextUTCMidnight(t *testing.T) {
	// The card says when the budget comes back. Without it, an exhausted lane
	// says "none left" and gives the learner nothing to wait for.
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{}}
	w := httptest.NewRecorder()
	budgetServer(c).Routes().ServeHTTP(w, budgetReq(true))

	b := decodeBudget(t, w)
	resets, err := time.Parse(time.RFC3339, b.ResetsAt)
	if err != nil {
		t.Fatalf("resets_at %q is not RFC3339: %v", b.ResetsAt, err)
	}
	if resets.Hour() != 0 || resets.Minute() != 0 || resets.Second() != 0 {
		t.Errorf("resets_at = %v, want a UTC midnight", resets)
	}
	if !resets.After(time.Now().UTC()) {
		t.Errorf("resets_at = %v is not in the future", resets)
	}
}

func TestPracticeBudget_CounterErrorIs500NotAFullBudget(t *testing.T) {
	// Reporting a full budget when the count failed would invite a tap the
	// enforcement path then refuses. Fail loud instead.
	c := &fakeBudgetCounter{err: errors.New("connection refused")}
	w := httptest.NewRecorder()
	budgetServer(c).Routes().ServeHTTP(w, budgetReq(true))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", w.Code, w.Body.String())
	}
}

func TestPracticeBudget_UnwiredPortIs503(t *testing.T) {
	ext := httpadapter.NewExtServer(nil)
	w := httptest.NewRecorder()
	ext.Routes().ServeHTTP(w, budgetReq(true))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", w.Code, w.Body.String())
	}
}

func TestPracticeBudget_MissingContextIs400(t *testing.T) {
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{}}
	w := httptest.NewRecorder()
	budgetServer(c).Routes().ServeHTTP(w, budgetReq(false))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if c.calls != 0 {
		t.Error("a request with no identity must not reach the repository")
	}
}

func TestPracticeBudget_WrongMethodIs405(t *testing.T) {
	c := &fakeBudgetCounter{byOrigin: map[cq.RequestOrigin]int{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/me/practice-budget", nil)
	r.Header.Set("X-Tenant-Id", budgetTenant)
	r.Header.Set("gcid", budgetGCID)
	budgetServer(c).Routes().ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}
