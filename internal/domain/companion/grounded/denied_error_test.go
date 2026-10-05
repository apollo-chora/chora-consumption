// denied_error_test.go — DeniedError is the typed GOVERNANCE refusal carried
// across the GroundedSearchPort (CHO-2148 close-out). It exists so the HTTP edge
// can answer an honest 4xx without importing the gateway's transport; these pin
// its shape and its message contract.
package grounded

import (
	"errors"
	"fmt"
	"testing"
)

func TestDeniedError_MessageCarriesReasonAndDetail(t *testing.T) {
	err := &DeniedError{Reason: DenyKillSwitch, Detail: "platform egress halted"}
	got := err.Error()
	want := "grounded: egress denied (kill_switch): platform egress halted"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDeniedError_MessageWithoutDetail(t *testing.T) {
	for _, detail := range []string{"", "   "} {
		err := &DeniedError{Reason: DenyEgressOff, Detail: detail}
		got := err.Error()
		want := "grounded: egress denied (egress_off)"
		if got != want {
			t.Errorf("Error() with detail %q = %q, want %q", detail, got, want)
		}
	}
}

// A nil receiver must not panic — a deny travels through errors.As/%v on paths
// that may hold a typed-nil, and a panic in the DENY path would turn a correct
// refusal into a 500.
func TestDeniedError_NilReceiverDoesNotPanic(t *testing.T) {
	var err *DeniedError
	if got := err.Error(); got != "grounded: egress denied" {
		t.Errorf("nil-receiver Error() = %q", got)
	}
}

// The port contract: a deny must survive wrapping, so the HTTP edge's errors.As
// finds it however the adapter chose to annotate the failure.
func TestDeniedError_SurvivesWrapping(t *testing.T) {
	base := &DeniedError{Reason: DenyCeilingReached, Detail: "50/50 used"}
	wrapped := fmt.Errorf("clients: GroundedSearch rpc: %w", base)

	var deny *DeniedError
	if !errors.As(wrapped, &deny) {
		t.Fatalf("errors.As failed on a wrapped deny (%v) — the HTTP edge would blanket-502 it", wrapped)
	}
	if deny.Reason != DenyCeilingReached {
		t.Errorf("Reason = %q, want %q", deny.Reason, DenyCeilingReached)
	}
}

// The reason tokens are a contract shared with the HTTP edge + the FE error keys;
// a silent rename would re-break the surface, so pin them.
func TestDenyReason_Tokens(t *testing.T) {
	for _, tc := range []struct {
		reason DenyReason
		want   string
	}{
		{DenyEgressOff, "egress_off"},
		{DenyKillSwitch, "kill_switch"},
		{DenyCeilingReached, "ceiling_reached"},
		{DenyQueryBlocked, "query_blocked"},
		{DenyInvalidQuery, "invalid_query"},
	} {
		if string(tc.reason) != tc.want {
			t.Errorf("reason = %q, want %q", tc.reason, tc.want)
		}
	}
}
