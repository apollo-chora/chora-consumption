package companion

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// turn_test.go - ADR-254 D4/D8: the Turn aggregate is the per-request state of
// a bus-dispatched agent turn. These tests pin the invariants the waiter, the
// pull consumer and the SSE replay rely on: a turn is accepted at creation,
// completes exactly once from a wire result, times out only while accepted,
// and maps its terminal state to the caller-facing error code contract
// (COMPANION_SUSPENDED / COMPANION_TURN_REJECTED / COMPANION_TURN_FAILED /
// COMPANION_TURN_TIMEOUT).

func newTestTurn(t *testing.T) *Turn {
	t.Helper()
	turn, err := NewTurn(NewTurnInput{
		TurnID:         "01933b5e-0000-7000-8000-000000000001",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		OwnerGCID:      "22222222-2222-7222-8222-222222222222",
		CompanionID:    "33333333-3333-7333-8333-333333333333",
		ConversationID: "44444444-4444-7444-8444-444444444444",
		Kind:           TurnKindTyped,
		Lane:           TurnLaneCompanionTurn,
		Request:        json.RawMessage(`{"message":"hi"}`),
		Now:            time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC),
		Deadline:       130 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewTurn: %v", err)
	}
	return turn
}

func TestNewTurn_AcceptedWithDeadline(t *testing.T) {
	turn := newTestTurn(t)
	if turn.Status != TurnStatusAccepted {
		t.Fatalf("status = %q, want accepted", turn.Status)
	}
	if !turn.DeadlineAt.Equal(turn.RequestedAt.Add(130 * time.Second)) {
		t.Fatalf("deadline = %v, want requested_at + 130s (%v)", turn.DeadlineAt, turn.RequestedAt)
	}
	if turn.IsTerminal() {
		t.Fatal("a fresh turn must not be terminal")
	}
}

func TestNewTurn_RejectsInvalidInput(t *testing.T) {
	base := NewTurnInput{
		TurnID: "t", TenantID: "ten", OwnerGCID: "g", CompanionID: "c",
		Kind: TurnKindSkill, Lane: TurnLaneCompanionTurn,
		Request: json.RawMessage(`{}`), Now: time.Now(), Deadline: time.Second,
	}
	cases := map[string]func(in *NewTurnInput){
		"missing turn_id":       func(in *NewTurnInput) { in.TurnID = "" },
		"missing tenant":        func(in *NewTurnInput) { in.TenantID = "" },
		"missing gcid":          func(in *NewTurnInput) { in.OwnerGCID = "" },
		"bad kind":              func(in *NewTurnInput) { in.Kind = TurnKind("voice") },
		"bad lane":              func(in *NewTurnInput) { in.Lane = TurnLane("weather") },
		"kind/lane mismatch":    func(in *NewTurnInput) { in.Kind = TurnKindDose },
		"empty request":         func(in *NewTurnInput) { in.Request = nil },
		"non-positive deadline": func(in *NewTurnInput) { in.Deadline = 0 },
		"typed needs companion": func(in *NewTurnInput) { in.Kind = TurnKindTyped; in.CompanionID = "" },
	}
	for name, mutate := range cases {
		in := base
		mutate(&in)
		if _, err := NewTurn(in); !errors.Is(err, ErrTurnInvalid) {
			t.Errorf("%s: err = %v, want ErrTurnInvalid", name, err)
		}
	}
	// A dose turn has no Companion and rides the dose lane.
	in := base
	in.Kind, in.Lane, in.CompanionID = TurnKindDose, TurnLaneDoseRecommendation, ""
	if _, err := NewTurn(in); err != nil {
		t.Errorf("dose turn without companion: %v", err)
	}
}

func TestTurn_CompleteMapsWireStatus(t *testing.T) {
	cases := []struct {
		wire     string
		kennel   string
		want     TurnStatus
		wantCode string
	}{
		{"OK", "", TurnStatusCompleted, ""},
		{"FAILED", "", TurnStatusFailed, TurnErrCodeFailed},
		{"REJECTED", "tenant_in_flight_cap", TurnStatusRejected, TurnErrCodeRejected},
		{"REJECTED", "surface_unstamped", TurnStatusRejected, TurnErrCodeRejected},
		{"REJECTED", "companion_suspended", TurnStatusRejected, TurnErrCodeSuspended},
	}
	for _, c := range cases {
		turn := newTestTurn(t)
		done := time.Date(2026, 8, 23, 4, 0, 5, 0, time.UTC)
		err := turn.Complete(TurnResult{
			WireStatus: c.wire, ErrorCode: c.kennel, WorkflowID: "wf-1",
			GeneratedByModelID: "gemini-2.5-flash", ReplyText: "hello", CompletedAt: done,
			Raw: json.RawMessage(`{"reply_text":"hello"}`),
		})
		if err != nil {
			t.Fatalf("%s: Complete: %v", c.wire, err)
		}
		if turn.Status != c.want {
			t.Errorf("%s/%s: status = %q, want %q", c.wire, c.kennel, turn.Status, c.want)
		}
		if got := turn.CallerErrorCode(); got != c.wantCode {
			t.Errorf("%s/%s: caller code = %q, want %q", c.wire, c.kennel, got, c.wantCode)
		}
		if turn.CompletedAt == nil || !turn.CompletedAt.Equal(done) {
			t.Errorf("%s: completed_at not stamped", c.wire)
		}
		if !turn.IsTerminal() {
			t.Errorf("%s: must be terminal", c.wire)
		}
		if turn.WorkflowID != "wf-1" || turn.GeneratedByModelID != "gemini-2.5-flash" {
			t.Errorf("%s: workflow/model not carried", c.wire)
		}
	}
}

func TestTurn_CompleteTwiceIsRefused(t *testing.T) {
	turn := newTestTurn(t)
	if err := turn.Complete(TurnResult{WireStatus: "OK", CompletedAt: time.Now()}); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	err := turn.Complete(TurnResult{WireStatus: "FAILED", CompletedAt: time.Now()})
	if !errors.Is(err, ErrTurnAlreadyTerminal) {
		t.Fatalf("second complete err = %v, want ErrTurnAlreadyTerminal", err)
	}
	if turn.Status != TurnStatusCompleted {
		t.Fatalf("status changed on refused complete: %q", turn.Status)
	}
}

func TestTurn_CompleteRefusesUnknownWireStatus(t *testing.T) {
	turn := newTestTurn(t)
	if err := turn.Complete(TurnResult{WireStatus: "DONE", CompletedAt: time.Now()}); !errors.Is(err, ErrTurnBadResult) {
		t.Fatalf("err = %v, want ErrTurnBadResult", err)
	}
	if turn.Status != TurnStatusAccepted {
		t.Fatalf("status must stay accepted on a bad result, got %q", turn.Status)
	}
}

func TestTurn_TimeoutOnlyWhileAccepted(t *testing.T) {
	turn := newTestTurn(t)
	now := turn.DeadlineAt.Add(time.Second)
	if !turn.Timeout(now) {
		t.Fatal("timeout on an accepted turn must apply")
	}
	if turn.Status != TurnStatusTimeout || turn.CallerErrorCode() != TurnErrCodeTimeout {
		t.Fatalf("status/code = %q/%q", turn.Status, turn.CallerErrorCode())
	}
	if turn.Timeout(now.Add(time.Second)) {
		t.Fatal("timeout on a terminal turn must be a no-op")
	}
	// A completion arriving after the reap is refused (the store records it
	// as late; the status never flips back).
	if err := turn.Complete(TurnResult{WireStatus: "OK", CompletedAt: now}); !errors.Is(err, ErrTurnAlreadyTerminal) {
		t.Fatalf("late completion err = %v", err)
	}
}

func TestParseTurnResultJSON_CompanionTurnLane(t *testing.T) {
	body := []byte(`{
	  "turn_id":"01933b5e-0000-7000-8000-000000000001",
	  "conversation_id":"44444444-4444-7444-8444-444444444444",
	  "companion_id":"33333333-3333-7333-8333-333333333333",
	  "status":"OK","error_code":"","workflow_id":"wf-9",
	  "generated_by_model_id":"gemini-2.5-flash",
	  "reply_text":"Here is the thing.",
	  "grounding":[{"atom_id":"a1","revision_id":"r1","title":"T","snippet":"S"}],
	  "tool_calls":[{"name":"weakness.read","summary":"read 3 edges"}],
	  "refusal_reason":"",
	  "completed_at":"2026-08-23T04:00:05Z"
	}`)
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.ID != "01933b5e-0000-7000-8000-000000000001" {
		t.Errorf("id = %q", parsed.ID)
	}
	r := parsed.Result
	if r.WireStatus != "OK" || r.WorkflowID != "wf-9" || r.GeneratedByModelID != "gemini-2.5-flash" || r.ReplyText != "Here is the thing." {
		t.Errorf("common fields not parsed: %+v", r)
	}
	if len(r.Grounding) != 1 || r.Grounding[0].AtomID != "a1" || len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "weakness.read" {
		t.Errorf("grounding/tool_calls not parsed: %+v", r)
	}
	if !r.CompletedAt.Equal(time.Date(2026, 8, 23, 4, 0, 5, 0, time.UTC)) {
		t.Errorf("completed_at = %v", r.CompletedAt)
	}
	if len(r.Raw) == 0 {
		t.Error("raw body must be kept for replay")
	}
}

func TestParseTurnResultJSON_DoseLane(t *testing.T) {
	body := []byte(`{"dose_request_id":"d-1","goal_id":"g-1","status":"OK","workflow_id":"wf-2",
	  "generated_by_model_id":"gemini-2.5-flash","recommended_atom_ids":["a1","a2"],"rationale":"because",
	  "completed_at":"2026-08-23T04:00:05Z"}`)
	parsed, err := ParseTurnResultJSON(TurnLaneDoseRecommendation, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.ID != "d-1" || len(parsed.Result.RecommendedAtomIDs) != 2 || parsed.Result.Rationale != "because" {
		t.Errorf("dose fields not parsed: %+v", parsed)
	}
}

func TestParseTurnResultJSON_FailsLoud(t *testing.T) {
	if _, err := ParseTurnResultJSON(TurnLaneCompanionTurn, []byte(`not json`)); err == nil {
		t.Error("malformed JSON must fail")
	}
	if _, err := ParseTurnResultJSON(TurnLaneCompanionTurn, []byte(`{"status":"OK"}`)); err == nil {
		t.Error("a result without its id must fail")
	}
	if _, err := ParseTurnResultJSON(TurnLaneCompanionTurn, []byte(`{"turn_id":"t"}`)); err == nil {
		t.Error("a result without a status must fail")
	}
}
