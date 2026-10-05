// transcript_seen_handler.go: POST /v1/me/transcript/{entryID}:seen
// (UX refactor Phase B, package B6 item 2).
//
// Marks one of the learner's own results as opened, so the home card can stop
// telling them about a grade they have already read.
//
// Both halves of the identity come from VERIFIED headers. The tenant scopes
// RLS; the gcid scopes the row. Neither is read from the path or the query,
// and a test passes a decoy gcid in the query string to prove it is ignored.
// On a write this matters more than on a read: RLS alone would still let one
// learner mark another learner's result as read inside the same tenant.
package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
)

// tracingWithIdentity stamps the tenant + learner the RLS session reads.
func tracingWithIdentity(r *http.Request, tenantID, gcid string) context.Context {
	return tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
}

// seenActionSuffix is the action verb on the entry subresource. Google AIP
// style, matching the sibling `:abandon` and `:teardown` actions already in
// this service.
const seenActionSuffix = ":seen"

// handleMeTranscriptEntryAction routes the /v1/me/transcript/ subtree.
//
// The subtree mount must not swallow paths it does not own: an unknown action
// is a 404, not a silent mark-as-seen. Only the exact `:seen` suffix routes.
func (s *ExtServer) handleMeTranscriptEntryAction(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/transcript/")
	if !strings.HasSuffix(rest, seenActionSuffix) {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "unknown transcript action")
		return
	}
	entryID := strings.TrimSpace(strings.TrimSuffix(rest, seenActionSuffix))
	if entryID == "" || strings.Contains(entryID, "/") {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "entry id required")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	if s.TranscriptSeen == nil {
		extWriteError(w, http.StatusServiceUnavailable, "TRANSCRIPT_SEEN_NOT_WIRED", "")
		return
	}

	// The pg repo derives its RLS session from the context, so the wrap is
	// load-bearing: without it the UPDATE runs with no tenant set and the
	// RLS-bound role silently affects zero rows (mirrors handleMeTranscript).
	ctx := tracingWithIdentity(r, tenantID, gcid)
	if err := s.TranscriptSeen.MarkSeen(ctx, tenantID, gcid, entryID, time.Now().UTC()); err != nil {
		extWriteError(w, http.StatusInternalServerError, "TRANSCRIPT_SEEN_FAILED", err.Error())
		return
	}
	// 204 for both the first mark and every later one. Already-seen is the
	// normal second tap, not a conflict: the learner did nothing wrong and the
	// end state is exactly what they asked for.
	w.WriteHeader(http.StatusNoContent)
}
