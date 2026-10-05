// companion_retire_handler.go — CHO-2033: learner retire/release of a roster
// companion.
//
//	POST /v1/me/companions/{id}/retire
//
// Frees a cap-3 roster slot without operator intervention. Retire = SOFT-delete
// (deleted_at set; the row + growth ledger persist — pseudonymise-not-delete,
// ddd-enforcement #5). Get already excludes soft-deleted rows, so a
// double-retire renders 404. Emits companion.retired.v1 (audit + future
// consumers) via the outbox; the retire is authoritative — a publish failure is
// logged loud, never un-retires the companion or 500s the request.
package http

import (
	"log"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// retireCompanion handles POST /v1/me/companions/{id}/retire.
func (s *Server) retireCompanion(w http.ResponseWriter, r *http.Request, companionID string) {
	if s.CompanionInstances == nil {
		writeError(w, http.StatusServiceUnavailable, "COMPANION_RETIRE_UNAVAILABLE",
			"companion roster not wired")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := repoCtx(r, tenantID, gcid)

	// Owned, non-deleted instance only — a foreign or already-retired companion
	// renders 404 (no-leak; Get excludes soft-deleted).
	inst, ferr := s.loadOwnedInstance(ctx, tenantID, gcid, companionID)
	if ferr != nil {
		s.writeSkillError(w, ferr)
		return
	}

	// Detach the retired companion from any map (Goal) it's bonded to BEFORE the
	// soft-delete, so a Goals failure aborts the retire (nothing changed) rather
	// than stranding a dead companion on a live map (map↔roster consistency). A
	// nil detacher skips with a loud log — the roster slot is still freed.
	if s.CompanionMaps != nil {
		n, derr := s.CompanionMaps.DetachCompanionFromMaps(ctx, tenantID, gcid, companionID)
		if derr != nil {
			writeError(w, http.StatusInternalServerError, "COMPANION_RETIRE_DETACH_FAILED", derr.Error())
			return
		}
		if n > 0 {
			log.Printf("consumption: retire detached companion_id=%s from %d map(s)", companionID, n)
		}
	} else {
		log.Printf("consumption: retire detach SKIPPED (CompanionMaps unwired) for companion_id=%s — map binding may be stale", companionID)
	}

	// CHO-2096 — purge the companion's retained memories (soft-delete every live
	// companion_memory_recall row) BEFORE the instance soft-delete: the retire
	// confirm promises the memories are permanently deleted, and a purge that
	// ran after SoftDelete could never be retried (double-retire 404s). A purge
	// failure aborts the retire loudly — the companion stays (re-summonable if
	// the earlier detach landed) and the learner retries. Nil retirer = memory
	// was never wired (nothing recorded) — loud skip, parity with CompanionMaps.
	if s.CompanionMemoryRetire != nil {
		n, merr := s.CompanionMemoryRetire.SoftDeleteByCompanion(ctx, tenantID, companionID)
		if merr != nil {
			writeError(w, http.StatusInternalServerError, "COMPANION_RETIRE_MEMORY_PURGE_FAILED", merr.Error())
			return
		}
		if n > 0 {
			log.Printf("consumption: retire purged %d memory row(s) for companion_id=%s (CHO-2096)", n, companionID)
		}
	} else {
		log.Printf("consumption: retire memory purge SKIPPED (MemoryRetirer unwired) for companion_id=%s", companionID)
	}

	if derr := s.CompanionInstances.SoftDelete(ctx, companionID); derr != nil {
		writeError(w, http.StatusInternalServerError, "COMPANION_RETIRE_FAILED", derr.Error())
		return
	}

	s.publishCompanionRetired(r, tenantID, gcid, inst)

	writeJSON(w, http.StatusOK, map[string]any{
		"companion_id": companionID,
		"retired":      true,
	})
}

// publishCompanionRetired emits companion.retired.v1 (best-effort, fail-loud on
// error). The retire already committed — the event is audit + future
// consumers, so a publish failure must not un-retire or 500 the request.
func (s *Server) publishCompanionRetired(r *http.Request, tenantID, gcid string, inst *companion.Instance) {
	if s.Publisher == nil {
		return
	}
	tp := tracing.EnsureTraceparent(r.Header.Get("traceparent"))
	env := events.NewEnvelope(tenantID, gcid, tp, r.Header.Get("tracestate"),
		"companion.retired."+inst.CompanionID)
	// Emit exactly the CompanionRetired proto shape (tenant lives in the
	// envelope; the consumption Instance carries no species enum / int level,
	// so those proto3-default). reason = learner-initiated release.
	payload := map[string]any{
		"companion_id": inst.CompanionID,
		"owner_gcid":   gcid,
		"reason":       "owner_choice",
		"retired_at":   time.Now().UTC(),
	}
	if err := s.Publisher.Publish(events.TopicCompanionRetired, env, payload); err != nil {
		log.Printf("consumption: companion.retired.v1 publish failed for companion_id=%s: %v (retire committed)",
			inst.CompanionID, err)
	}
}
