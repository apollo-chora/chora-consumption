// companion_memory_delete_handler.go, register 6.4 R6: the learner's own delete
// over their conversation memory.
//
//	DELETE /v1/me/companions/{id}/memory/{memoryId}
//
// The learner can read their Companion's memories (companion_memory_read_handler.go)
// but until now had no way to remove one. Retirement purged EVERYTHING for a
// companion (CHO-2096 MemoryRetirer) and closure is a whole-account event, so a
// learner who simply wanted one remembered thing forgotten had to give up the
// companion or the account. This is the missing middle.
//
// Learner-scoped: the GCID is the validated session subject taken from the
// request context, NEVER a path/query/body param. The repo additionally fences
// the UPDATE on owner_gcid, because RLS fences only the TENANT and would happily
// let one learner erase a co-tenant's memory by guessing an id.
//
// It is an erase, not a hard DELETE (ddd-enforcement #4): the row is tombstoned
// and its content_text + embedding are redacted, so the conversation is gone
// while the audit skeleton survives. See pg.DeleteOwnedMemory for why the
// embedding goes too.
package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
)

// MemoryEraser is the narrow driven port this handler needs: erase ONE memory
// the caller owns, returning rows affected. Deliberately separate from
// companion.CompanionMemory (chat's recall/record) and from companion.MemoryRetirer
// (retire's purge-everything), so none of the three can be wired by accident in
// place of another.
type MemoryEraser interface {
	DeleteOwnedMemory(ctx context.Context, tenantID, ownerGCID, memoryID string) (int64, error)
}

// CompanionMemoryDeleteHandler serves DELETE /v1/me/companions/{id}/memory/{memoryId}.
type CompanionMemoryDeleteHandler struct {
	Memories MemoryEraser
}

var _ http.Handler = (*CompanionMemoryDeleteHandler)(nil)

// NewCompanionMemoryDeleteHandler constructs the handler.
func NewCompanionMemoryDeleteHandler(m MemoryEraser) *CompanionMemoryDeleteHandler {
	return &CompanionMemoryDeleteHandler{Memories: m}
}

// ServeHTTP erases one owned memory.
//
// Every non-success path is explicit, and that is the point of the endpoint: a
// learner who asks for their conversation to be forgotten must never be told it
// happened when it did not.
//   - not wired (no pgx pool at boot) → 503
//   - no live row matched             → 404
//   - backing-store error             → 500
func (h *CompanionMemoryDeleteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Memories == nil {
		extWriteError(w, http.StatusServiceUnavailable, "COMPANION_MEMORY_UNAVAILABLE",
			"companion memory delete not wired")
		return
	}
	if r.Method != http.MethodDelete {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	memoryID := memoryIDFromPath(r)
	if memoryID == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "memory_id required")
		return
	}

	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	rows, err := h.Memories.DeleteOwnedMemory(ctx, tenantID, gcid, memoryID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "MEMORY_DELETE_FAILED", err.Error())
		return
	}
	if rows == 0 {
		// Unknown id, another learner's memory, or already erased. All three
		// collapse to 404: reporting success here would claim an erasure that
		// never happened, and distinguishing them would confirm the existence of
		// a memory belonging to someone else.
		extWriteError(w, http.StatusNotFound, "MEMORY_NOT_FOUND", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// memoryIDFromPath extracts {memoryId} from
// /v1/me/companions/{id}/memory/{memoryId}. Prefers the mux path value (Go 1.22
// wildcard) and falls back to trimming the literal path so the handler stays
// unit-testable without the mux.
func memoryIDFromPath(r *http.Request) string {
	if v := strings.TrimSpace(r.PathValue("memoryId")); v != "" {
		return v
	}
	p := strings.TrimPrefix(r.URL.Path, companionMemoryPathPrefix)
	_, rest, found := strings.Cut(p, companionMemoryPathSuffix+"/")
	if !found {
		return ""
	}
	rest = strings.Trim(rest, "/")
	if rest == "" || strings.Contains(rest, "/") {
		return ""
	}
	return rest
}
