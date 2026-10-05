package http

// companion_roster_cap_test.go: the roster cap the list SERVES is the cap the
// create handler ENFORCES (UX Track U, C1a follow-up for C3's denominator).
//
// C3 renders "2 of N". A wrong N is worse than none: it either tells a learner
// they cannot hold a companion they have paid for, or offers one the create
// handler will refuse with a 409. So the served cap is a PROJECTION of the same
// predicate the enforcement path calls, never a second calculation, which is
// the shape B6's BudgetFor established.
//
// ⚠ cap_source can only be "default" today, and that is a finding rather than a
// simplification. Nothing in this service reads tenant_entitlements: the create
// handler hardcodes the constant behind a comment naming an M12.3 follow-up,
// and a grep for max_companions_per_user finds only prose. The "entitlement"
// value is named in the contract so the wire does not change when that read
// lands, but nothing emits it, and a branch that could not run would be exactly
// the speculative wiring this track keeps refusing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type capWire struct {
	RosterCap int    `json:"roster_cap"`
	CapSource string `json:"cap_source"`
	Items     []struct {
		CompanionID string `json:"companion_id"`
	} `json:"items"`
}

func rosterCapOf(t *testing.T, s *Server) capWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/companions", nil)
	req.Header.Set("X-Tenant-Id", "t-1")
	req.Header.Set("gcid", "gcid-1")
	w := httptest.NewRecorder()
	s.handleCompanionInstances(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", w.Code, w.Body.String())
	}
	var got capWire
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestCompanionList_ServesTheRosterCapAndItsSource(t *testing.T) {
	s := unlockServer(t, nil, nil)
	got := rosterCapOf(t, s)

	if got.RosterCap != companion.DefaultMaxCompanionsPerUser {
		t.Errorf("roster_cap = %d; want %d", got.RosterCap, companion.DefaultMaxCompanionsPerUser)
	}
	if got.CapSource != "default" {
		t.Errorf("cap_source = %q; want default", got.CapSource)
	}
}

// The one that matters. The served cap and the ENFORCED cap come from the same
// call, so they cannot drift apart in a later edit that touches only one.
func TestRosterCap_ServedAndEnforcedNeverDisagree(t *testing.T) {
	cases := []struct {
		name string
		srv  *Server
	}{
		{"unit server, nothing wired", &Server{}},
		{"list server", unlockServer(t, nil, nil)},
		{"server with an entitlement seam that is still nil", &Server{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			servedCap, servedSource := tc.srv.rosterCap()
			enforcedCap := tc.srv.enforcedRosterCap()
			if servedCap != enforcedCap {
				t.Errorf("served cap %d but the create handler enforces %d: "+
					"a learner is told a number the enforcement will refuse",
					servedCap, enforcedCap)
			}
			if servedSource == "" {
				t.Error("cap_source is empty; an unlabelled cap cannot be explained to a learner")
			}
		})
	}
}

// cap_source is never "entitlement" while nothing reads entitlements. Emitting
// it would claim a provenance that does not exist, and the O plus reader could
// not tell it from a real one.
func TestRosterCap_NeverClaimsAnEntitlementSourceThatDoesNotExist(t *testing.T) {
	s := unlockServer(t, nil, nil)
	if got := rosterCapOf(t, s).CapSource; got == "entitlement" {
		t.Errorf("cap_source = %q, but no entitlement read exists in this service", got)
	}
}

// The profile's roster_context carries the SAME cap, so the two screens cannot
// tell a learner different numbers.
func TestRosterCap_ProfileAndListAgree(t *testing.T) {
	s := unlockServer(t, nil, nil)
	listCap := rosterCapOf(t, s).RosterCap
	profileCap, _ := s.rosterCap()
	if listCap != profileCap {
		t.Errorf("list serves %d and the profile serves %d", listCap, profileCap)
	}
}
