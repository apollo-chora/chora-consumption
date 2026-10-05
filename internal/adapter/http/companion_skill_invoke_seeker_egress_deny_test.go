// companion_skill_invoke_seeker_egress_deny_test.go — a GOVERNANCE deny of the
// grounded egress is a 4xx, NOT a 502 (CHO-2148 close-out, live-caught
// 2026-07-14).
//
// Why this file exists: the Far Sight live walk engaged the O+ kill-switch with
// the tenant STILL opted in, and the A+ surface rendered "The grounded search
// couldn't complete. Please try again." The invoke had returned 502
// FACT_CHECK_SEARCH_FAILED, chora-gateway then normalised it to
// GATEWAY_UPSTREAM_5XX (its documented contract: 5xx is masked, 2xx+4xx pass
// through verbatim), and the reason was erased before A+ could render it. The
// learner was told to retry a gate that was deliberately shut, and an engaged
// emergency stop was indistinguishable from a platform crash on the 5xx SLOs.
//
// A deny is a correct decision by the chokepoint. It must surface as a 4xx that
// the surface can explain. A genuine upstream failure (vendor down, timeout)
// stays 502 — TestInvokeFactCheck_SearchFailureIsLoud502 still guards that.
package http

import (
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/grounded"
)

// denyCases are shared by both Seekers — the mapping is per-reason, not per-skill.
var denyCases = []struct {
	name       string
	reason     grounded.DenyReason
	wantStatus int
	wantCode   string
}{
	{"tenant not entitled", grounded.DenyEgressOff, http.StatusForbidden, "EXTERNAL_EGRESS_DISABLED"},
	{"platform kill-switch", grounded.DenyKillSwitch, http.StatusForbidden, "EXTERNAL_EGRESS_KILL_SWITCH"},
	{"daily ceiling spent", grounded.DenyCeilingReached, http.StatusTooManyRequests, "EXTERNAL_EGRESS_CEILING_REACHED"},
	{"armor refused query", grounded.DenyQueryBlocked, http.StatusForbidden, "GROUNDED_QUERY_BLOCKED"},
	{"malformed query", grounded.DenyInvalidQuery, http.StatusBadRequest, "INVALID_SKILL_PARAMS"},
}

func TestInvokeFactCheck_EgressDenyIs4xxNot502(t *testing.T) {
	for _, tc := range denyCases {
		t.Run(tc.name, func(t *testing.T) {
			srv, id, engine, gs := seedFactCheckServer(t, "x", grounded.Result{})
			gs.err = &grounded.DeniedError{Reason: tc.reason, Detail: "denied by the chokepoint"}
			equipForInvoke(t, srv, id, "fact_check")

			w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/fact_check/invoke",
				map[string]any{"params": map[string]string{"claim": "is the sky blue"}})

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s, want %d — a governance deny must be a 4xx the surface can explain "+
					"(a 5xx is masked to GATEWAY_UPSTREAM_5XX by chora-gateway and the reason is lost)",
					w.Code, w.Body.String(), tc.wantStatus)
			}
			if code := errCode(t, w.Body.Bytes()); code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if engine.calls != 0 {
				t.Errorf("engine called %d times after a deny; want 0 (never an ungrounded verdict)", engine.calls)
			}
		})
	}
}

func TestInvokeWebResearch_EgressDenyIs4xxNot502(t *testing.T) {
	for _, tc := range denyCases {
		t.Run(tc.name, func(t *testing.T) {
			srv, id, engine, gs, _, _ := seedWebResearchServer(t, "x", grounded.Result{}, nil)
			gs.err = &grounded.DeniedError{Reason: tc.reason, Detail: "denied by the chokepoint"}

			w := authedReq(t, srv, http.MethodPost, "/v1/me/companions/"+id+"/skills/web_research/invoke",
				map[string]any{"params": map[string]string{"direction": "photosynthesis"}})

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", w.Code, w.Body.String(), tc.wantStatus)
			}
			if code := errCode(t, w.Body.Bytes()); code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if engine.calls != 0 {
				t.Errorf("engine called %d times after a deny; want 0", engine.calls)
			}
		})
	}
}
