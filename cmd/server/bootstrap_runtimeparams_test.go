// bootstrap_runtimeparams_test.go — the consumption pool's fail-loud GUCs.
//
// Regression for the PATCH /v1/me/goals/{id} 504 (CHO-2005): an UPDATE
// blocked on a goal row lock hung forever (no lock_timeout anywhere) and
// leaked the request goroutine. These per-connection GUCs make such a wait
// fail loud and reap leaked idle-in-transaction holders.
package main

import "testing"

func TestConsumptionDBRuntimeParams_BoundLockAndIdleTx(t *testing.T) {
	t.Parallel()
	p := consumptionDBRuntimeParams()

	// lock_timeout must be set and sit UNDER the gateway's 6s per-call
	// timeout so consumption errors out (500) before the gateway 504s.
	if got := p["lock_timeout"]; got != "3s" {
		t.Errorf("lock_timeout = %q, want 3s", got)
	}
	// idle_in_transaction_session_timeout reaps a leaked open transaction
	// so its row locks release (the root cause of the walk-era hang).
	if got := p["idle_in_transaction_session_timeout"]; got != "60s" {
		t.Errorf("idle_in_transaction_session_timeout = %q, want 60s", got)
	}
	// No statement_timeout: long read paths must not be capped (owner steer).
	if _, set := p["statement_timeout"]; set {
		t.Errorf("statement_timeout must not be set, got %q", p["statement_timeout"])
	}
}
