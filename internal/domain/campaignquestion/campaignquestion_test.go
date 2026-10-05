// campaignquestion_test.go — CHO-2082 / ADR-227 D13 (WS-C3): the campaign
// question-set aggregate. One live set per (tenant, learner, concept, rung)
// carrying the level-matched retrieval cache + the qgen gap-fill state
// machine (idle → requested → ready | failed; failed may re-request on a
// later day). ready is permanent — generate once, retrieve forever.
package campaignquestion

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
)

var qNow = time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)

func freshSet(t *testing.T) *QuestionSet {
	t.Helper()
	s, err := New("11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222",
		"33333333-3333-7333-8333-333333333333", "addition", 3, qNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNew_ValidatesAndMints(t *testing.T) {
	s := freshSet(t)
	if s.ID == "" || s.GenerationStatus != StatusIdle || s.Rung != 3 || s.ConceptKey != "addition" {
		t.Fatalf("fresh set malformed: %+v", s)
	}
	for _, bad := range []struct {
		tenant, gcid, concept, key string
		rung                       int
	}{
		{"", "g", "c", "k", 1},
		{"t", "", "c", "k", 1},
		{"t", "g", "", "k", 1},
		{"t", "g", "c", "", 1},
		{"t", "g", "c", "k", 0},
		{"t", "g", "c", "k", 7},
	} {
		if _, err := New(bad.tenant, bad.gcid, bad.concept, bad.key, bad.rung, qNow); err == nil {
			t.Fatalf("New must reject %+v", bad)
		}
	}
}

func TestSetRetrieved_CachesAndStamps(t *testing.T) {
	s := freshSet(t)
	s.SetRetrieved([]string{"atom-1", "atom-2"}, qNow)
	if len(s.RetrievedAtomIDs) != 2 || s.RetrievalCheckedAt == nil || !s.RetrievalCheckedAt.Equal(qNow) {
		t.Fatalf("retrieval cache not stamped: %+v", s)
	}
}

func TestLifecycle_RequestReadyReuse(t *testing.T) {
	s := freshSet(t)
	if err := s.MarkRequested("assist-1", qNow); err != nil {
		t.Fatalf("MarkRequested: %v", err)
	}
	if s.GenerationStatus != StatusRequested || s.AssistID != "assist-1" ||
		s.RequestedOn == nil || s.RequestedOn.Format("2006-01-02") != "2026-07-09" {
		t.Fatalf("requested state malformed: %+v", s)
	}
	// Double-request while in flight must refuse (no duplicate publishes).
	if err := s.MarkRequested("assist-2", qNow); err == nil {
		t.Fatal("in-flight set must refuse a second request")
	}
	if err := s.MarkReady([]byte(`{"candidates":[{"q":"?"}]}`), qNow.Add(time.Minute)); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if s.GenerationStatus != StatusReady || !s.CanServe() {
		t.Fatalf("ready set must serve: %+v", s)
	}
	// ready is permanent — no re-request, no re-fail.
	if err := s.MarkRequested("assist-3", qNow); err == nil {
		t.Fatal("ready set must refuse re-request (reuse forever)")
	}
	if err := s.MarkFailed("late refusal", qNow); err == nil {
		t.Fatal("ready set must refuse a late fail")
	}
}

func TestMarkReady_RefusesEmptyPayloadAndIdle(t *testing.T) {
	s := freshSet(t)
	if err := s.MarkReady([]byte(`{}`), qNow); err == nil {
		t.Fatal("idle set must refuse MarkReady (no fabricated ready)")
	}
	_ = s.MarkRequested("assist-1", qNow)
	if err := s.MarkReady(nil, qNow); err == nil {
		t.Fatal("empty payload must refuse MarkReady")
	}
	if err := s.MarkReady([]byte("   "), qNow); err == nil {
		t.Fatal("blank payload must refuse MarkReady")
	}
}

func TestMarkFailed_ThenRetryNextRequest(t *testing.T) {
	s := freshSet(t)
	_ = s.MarkRequested("assist-1", qNow)
	if err := s.MarkFailed("crew refused", qNow); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if s.GenerationStatus != StatusFailed || s.FailureReason == "" || s.CanServe() {
		t.Fatalf("failed state malformed: %+v", s)
	}
	// A failed set may honestly retry (a later day's cap decides WHEN).
	if err := s.MarkRequested("assist-2", qNow.Add(24*time.Hour)); err != nil {
		t.Fatalf("failed set must allow a fresh request: %v", err)
	}
	if s.AssistID != "assist-2" || s.FailureReason != "" {
		t.Fatalf("retry must re-key + clear the old failure: %+v", s)
	}
}

func TestStaleRequested_LostTerminalDayBoundary(t *testing.T) {
	s := freshSet(t)
	if s.StaleRequested(qNow) {
		t.Fatal("an idle set is never stale-requested")
	}
	_ = s.MarkRequested("assist-1", qNow)
	if s.StaleRequested(qNow) {
		t.Fatal("a same-day request is still honestly in flight")
	}
	if s.StaleRequested(qNow.Add(10 * time.Hour)) {
		t.Fatal("later the same UTC day is still in flight")
	}
	if !s.StaleRequested(qNow.Add(24 * time.Hour)) {
		t.Fatal("a request from an earlier UTC day is stale (terminal lost)")
	}
	// Terminal states are never stale-requested — ready serves forever and
	// failed already has its own retry lane.
	_ = s.MarkReady([]byte(`{"candidates":[{"q":"?"}]}`), qNow)
	if s.StaleRequested(qNow.Add(48 * time.Hour)) {
		t.Fatal("a ready set is never stale-requested")
	}
	f := freshSet(t)
	_ = f.MarkRequested("assist-1", qNow)
	_ = f.MarkFailed("crew refused", qNow)
	if f.StaleRequested(qNow.Add(48 * time.Hour)) {
		t.Fatal("a failed set is never stale-requested")
	}
	// A requested set missing its day stamp is an orphan — stale.
	o := freshSet(t)
	_ = o.MarkRequested("assist-1", qNow)
	o.RequestedOn = nil
	if !o.StaleRequested(qNow) {
		t.Fatal("a requested set without its day stamp must count as stale")
	}
}

func TestMarkRequested_SupersedesStaleRequest(t *testing.T) {
	s := freshSet(t)
	_ = s.MarkRequested("assist-lost", qNow)
	nextDay := qNow.Add(24 * time.Hour)
	if err := s.MarkRequested("assist-retry", nextDay); err != nil {
		t.Fatalf("a stale request must be supersedable on a later day: %v", err)
	}
	if s.GenerationStatus != StatusRequested || s.AssistID != "assist-retry" {
		t.Fatalf("supersede must re-key the set: %+v", s)
	}
	if s.RequestedOn == nil || s.RequestedOn.Format("2006-01-02") != "2026-07-10" {
		t.Fatalf("supersede must restamp the request day: %v", s.RequestedOn)
	}
	// Same-day supersede must still refuse (no duplicate publishes today).
	if err := s.MarkRequested("assist-dup", nextDay.Add(time.Hour)); err == nil {
		t.Fatal("a same-day in-flight request must still refuse")
	}
}

func TestStaleRequested_AcceleratedBuckets(t *testing.T) {
	if err := doseclock.Init(600); err != nil {
		t.Fatalf("doseclock.Init: %v", err)
	}
	t.Cleanup(doseclock.Reset)
	s := freshSet(t)
	_ = s.MarkRequested("assist-1", qNow)
	if s.StaleRequested(qNow.Add(5 * time.Minute)) {
		t.Fatal("the same 10-minute dose day is still in flight")
	}
	if !s.StaleRequested(qNow.Add(11 * time.Minute)) {
		t.Fatal("a request from an earlier 10-minute dose day is stale")
	}
}

// Regression (CHO-2319): under an accelerated SUB-MINUTE dose clock the dose day
// rolls between practice-lane polls, but a healthy qgen run (~2min) must NOT be
// superseded on every tick. Staleness floors on real request age, so a rolled
// dose day alone no longer declares a young in-flight run lost.
func TestStaleRequested_SubMinuteBucket_RealAgeFloorHolds(t *testing.T) {
	if err := doseclock.Init(1); err != nil { // 1-second dose days
		t.Fatalf("doseclock.Init: %v", err)
	}
	t.Cleanup(doseclock.Reset)
	s := freshSet(t)
	_ = s.MarkRequested("assist-1", qNow)
	// 2s later the 1s dose day has rolled twice, but the crew has had 2s.
	if s.StaleRequested(qNow.Add(2 * time.Second)) {
		t.Fatal("a 2s-old request must not be stale at a 1s dose day (real-age floor)")
	}
	// 3 minutes later: still under the ~10min floor, still generating.
	if s.StaleRequested(qNow.Add(3 * time.Minute)) {
		t.Fatal("a 3min-old request (under the real-age floor) must not be stale")
	}
	// Past the floor with a rolled dose day: the terminal is truly lost.
	if !s.StaleRequested(qNow.Add(11 * time.Minute)) {
		t.Fatal("a request older than the real-age floor with a rolled dose day is stale")
	}
}

// The two entrances draw on SEPARATE daily budgets: the automatic dose-march
// (1/day) and the explicit hex tap (a modest larger budget), so a spent march
// budget never starves a proactive learner's tap.
func TestCanRequestToday_PerOriginCaps(t *testing.T) {
	if DefaultDailyTapCap <= DefaultDailyMarchCap {
		t.Fatalf("the tap budget must exceed the march budget (tap=%d march=%d)",
			DefaultDailyTapCap, DefaultDailyMarchCap)
	}
	if !CanRequestToday(OriginMarch, 0) || !CanRequestToday(OriginTap, 0) {
		t.Fatal("zero same-origin requests must allow one")
	}
	if CanRequestToday(OriginMarch, DefaultDailyMarchCap) {
		t.Fatal("at-cap march learner must be refused (1/day)")
	}
	// A spent march budget must NOT block a tap.
	if !CanRequestToday(OriginTap, DefaultDailyMarchCap) {
		t.Fatal("the tap budget is independent of the march budget")
	}
	if CanRequestToday(OriginTap, DefaultDailyTapCap) {
		t.Fatal("at-cap tap learner must be refused")
	}
	if CanRequestToday(OriginTap, DefaultDailyTapCap+1) {
		t.Fatal("over-cap tap learner must be refused")
	}
}

func TestRequestOrigin_ValidAndNewDefaultsMarch(t *testing.T) {
	if !OriginMarch.Valid() || !OriginTap.Valid() || RequestOrigin("bogus").Valid() {
		t.Fatal("origin validity must accept march|tap and reject others")
	}
	// New mints an idle row whose origin defaults to march (an idle row is
	// never counted; MarkRequested stamps the authoritative origin later).
	if freshSet(t).RequestOrigin != OriginMarch {
		t.Fatalf("New must seed the march-default origin, got %q", freshSet(t).RequestOrigin)
	}
}

func TestBuildPrompt_CarriesThemeLevelAndCount(t *testing.T) {
	p := BuildPrompt("Addition Facts", "addition", "application", QuestionsPerRequest)
	for _, must := range []string{"Addition Facts", "addition", "application", "2"} {
		if !strings.Contains(p, must) {
			t.Fatalf("prompt missing %q:\n%s", must, p)
		}
	}
	// The generator must never be told to invent a different level.
	if strings.Contains(strings.ToLower(p), "synthesis") {
		t.Fatalf("prompt must target ONLY the requested level:\n%s", p)
	}
}

func TestStatusValid_AndZeroNowFallbacks(t *testing.T) {
	for _, s := range []Status{StatusIdle, StatusRequested, StatusReady, StatusFailed} {
		if !s.Valid() {
			t.Fatalf("%s must be valid", s)
		}
	}
	if Status("bogus").Valid() {
		t.Fatal("bogus status must be invalid")
	}
	s, err := New("t", "g", "c", "k", 1, time.Time{}) // zero now → stamped
	if err != nil || s.CreatedAt.IsZero() {
		t.Fatalf("zero-now New must self-stamp: %+v err=%v", s, err)
	}
	s.SetRetrieved([]string{"a"}, time.Time{})
	if s.UpdatedAt.IsZero() {
		t.Fatal("zero-now touch must self-stamp")
	}
	if err := s.MarkFailed("x", qNow); err == nil {
		t.Fatal("idle set must refuse MarkFailed")
	}
	if err := s.MarkRequested("", qNow); err == nil {
		t.Fatal("blank assist_id must refuse")
	}
}
