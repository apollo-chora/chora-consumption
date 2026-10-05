// ext_mux.go — minimal route dispatcher for the EXT server.
//
// We do NOT reuse net/http.ServeMux directly because the EXT scope needs
// to dispatch verb-style suffix patterns like
// `POST /sessions/{id}:abandon` and `PATCH /sessions/{id}/answer`,
// which the standard ServeMux's longest-prefix logic doesn't model
// cleanly. The mux delegates dispatch to per-route handlers that own
// path-suffix parsing.
package http

import (
	"encoding/json"
	"log"
	"net/http"
)

// extMux is a thin path-prefix router.
type extMux struct {
	routes map[string]http.HandlerFunc
}

func newExtMux() *extMux {
	return &extMux{routes: make(map[string]http.HandlerFunc)}
}

// Handle registers a handler for the prefix.
func (m *extMux) Handle(prefix string, h http.HandlerFunc) {
	m.routes[prefix] = h
}

// ServeHTTP dispatches by longest-matching registered prefix.
//
// Its logfmt-to-stderr access line was REMOVED 2026-08-07 (FINDING-02) for
// the reasons in router.go's middleware comment. It was additionally
// misleading here: ComposeHandlers replays every request through this mux
// speculatively, so a legacy-owned path produced an EXT log line describing
// a 404 that was never served. The one canonical structured entry is emitted
// by observability.AccessLogMiddleware around the composite handler, which
// sees the status that actually went on the wire.
func (m *extMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// First try exact-match (highest priority).
	if h, ok := m.routes[path]; ok {
		h(w, r)
		return
	}
	// Otherwise longest-prefix match (`/sessions/` covers `/sessions/{id}`).
	var bestPrefix string
	var bestHandler http.HandlerFunc
	for prefix, h := range m.routes {
		if len(prefix) > 0 && prefix[len(prefix)-1] == '/' {
			if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
				if len(prefix) > len(bestPrefix) {
					bestPrefix = prefix
					bestHandler = h
				}
			}
		}
	}
	if bestHandler != nil {
		bestHandler(w, r)
		return
	}
	extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
}

// extWriteJSON writes a JSON response. Distinct name to avoid collision
// with the legacy router.go writeJSON.
func extWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode error: %v", err)
	}
}

func extWriteError(w http.ResponseWriter, status int, code, message string) {
	extWriteJSON(w, status, map[string]string{
		"code":    code,
		"message": message,
	})
}
