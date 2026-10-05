// compose.go — composite HTTP handler for chora-consumption.
//
// chora-consumption mounts TWO route trees on a single Cloud Run port:
//
//  1. EXT scope (/v1/me/*, /learning-paths/*, /sessions/*,
//     /knowledge-graph/*, /me/knowledge-graph/*) — the S4.2 +
//     M13 EXT-scope endpoints
//  2. Legacy / Phyllis-MVP scope (/api/*, /healthz, /readyz,
//     /companion/*, /atoms/*, /api/companions/*, /api/sessions/*,
//     /api/learning-paths/*) — the older Companion/DailyDose handlers
//     reusing the canonical aggregate types post-S4.2 cleanup
//
// The composite tries EXT first; on NOT_FOUND it falls through to the
// legacy server. Both share the same publisher / repo state in
// production via the wiring in cmd/server/main.go.
package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
)

// ComposeHandlers returns an http.Handler that tries `primary` first
// and falls back to `fallback` if the primary returns 404 with the
// EXT server's "NOT_FOUND" code.
//
// We use httptest.ResponseRecorder to peek at the primary's response;
// if it's a 404 we replay against the fallback. This is a small
// perf hit on miss — the typical hot path is single-digit μs and the
// MVP traffic envelope is well within budget.
//
// fallbackFirst declares path prefixes the fallback OWNS (health probes,
// internal pubsub push inboxes, the companions family): those skip the
// primary pass entirely — the recorder replay is wasted work there, and
// the primary's OTel middleware would log a spurious `status=404` for
// every hit (the walk day-1 "404 pairs" red herring — probes + growth
// pushes each left an ext-side 404 line next to their real 200).
//
// primaryExact (CHO-2137) declares exact paths the PRIMARY owns INSIDE an
// otherwise fallback-owned prefix family — checked before fallbackFirst so
// e.g. /v1/me/companions/acquire + /bindings stay EXT-served while the
// "/v1/me/companions" prefix routes everything else straight to the
// fallback. Pass nil when no carve-outs are needed.
func ComposeHandlers(primary, fallback http.Handler, primaryExact []string, fallbackFirst ...string) http.Handler {
	exact := make(map[string]struct{}, len(primaryExact))
	for _, p := range primaryExact {
		exact[p] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := exact[r.URL.Path]; ok {
			primary.ServeHTTP(w, r)
			return
		}
		for _, prefix := range fallbackFirst {
			if strings.HasPrefix(r.URL.Path, prefix) {
				fallback.ServeHTTP(w, r)
				return
			}
		}
		rec := httptest.NewRecorder()
		primary.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			// Primary handled it.
			for k, vv := range rec.Header() {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		// Primary 404 — see if it's specifically the NOT_FOUND from
		// extWriteError. If yes, fall through. If it's a sub-route 404
		// (e.g., SESSION_NOT_FOUND), surface that.
		body := rec.Body.Bytes()
		if !bytes.Contains(body, []byte("\"NOT_FOUND\"")) {
			// Specific 404 — surface as-is.
			for k, vv := range rec.Header() {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(body)
			return
		}
		fallback.ServeHTTP(w, r)
	})
}
