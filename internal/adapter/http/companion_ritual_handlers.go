// companion_ritual_handlers.go — CHO-2016 G5 item 3: the Grimoire Rituals BFF
// handlers under /v1/me/companions/{companionId}/rituals. Standard REST, clean
// camelCase JSON (the drag-drop designer FE calls these).
//
//	GET  /v1/me/companions/{fid}/rituals                 → list
//	POST /v1/me/companions/{fid}/rituals                 → create draft
//	GET  /v1/me/companions/{fid}/rituals/{rid}           → get one
//	POST /v1/me/companions/{fid}/rituals/{rid}/publish   → publish a revision (auto-enable within quota)
//	POST /v1/me/companions/{fid}/rituals/{rid}/run       → start a run: 202 + the running record (the
//	                                                      steps execute detached; poll …/runs for the
//	                                                      terminal) — 200 when it is terminal at ack
//	                                                      (skipped_budget). CHO-2145.
//	GET  /v1/me/companions/{fid}/rituals/{rid}/runs       → run history
//
// Handlers do their own method check + requireContext + rlsCtx stamping (the
// pg repos SET LOCAL chora.tenant_id inside their tx). A nil port ⇒ 503
// RITUALS_NOT_WIRED (fail-loud, never a fake path).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---- DTOs (camelCase) ------------------------------------------------------

// ritualStepDTO is the wire shape of one composed step.
//
// The ADR-257 reserved fields are on the wire deliberately, even though the
// domain refuses every one of them today. Without them, encoding/json would
// silently DROP a client's fromStepId and publish an unchained ritual with no
// error, which is the same silent-acceptance failure the domain guard exists to
// prevent. Carried and refused end to end is honest; carried and dropped is not.
// When chaining ships behind the ADR-257 section 5 gate, the refusal lifts and
// the wire does not change.
type ritualStepDTO struct {
	SkillKey string         `json:"skillKey"`
	Params   map[string]any `json:"params,omitempty"`

	StepID          string `json:"stepId,omitempty"`
	StepKind        string `json:"stepKind,omitempty"`
	FromStepID      string `json:"fromStepId,omitempty"`
	Actor           string `json:"actor,omitempty"`
	CapabilityScope string `json:"capabilityScope,omitempty"`
}

type ritualDTO struct {
	RitualID            string          `json:"ritualId"`
	CompanionID         string          `json:"companionId"`
	Name                string          `json:"name"`
	Trigger             string          `json:"trigger"`
	Sink                string          `json:"sink"`
	Enabled             bool            `json:"enabled"`
	PublishedPriceUnits int             `json:"publishedPriceUnits"`
	CurrentRevision     int             `json:"currentRevision"`
	Steps               []ritualStepDTO `json:"steps"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type createRitualReq struct {
	Name    string `json:"name"`
	Trigger string `json:"trigger"`
	Sink    string `json:"sink"`
}

type publishRitualReq struct {
	Steps        []ritualStepDTO `json:"steps"`
	ArmorVerdict string          `json:"armorVerdict,omitempty"`
}

type runRitualReq struct {
	TriggerSource string `json:"triggerSource,omitempty"` // default "manual"
}

type runStepStoryDTO struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Sources []string `json:"sources,omitempty"`
}

type runStoryDTO struct {
	Title    string            `json:"title"`
	Status   string            `json:"status"`
	Steps    []runStepStoryDTO `json:"steps"`
	ManaCost int               `json:"manaCost"`
}

type ritualRunDTO struct {
	RunID       string       `json:"runId"`
	RitualID    string       `json:"ritualId"`
	RevisionNo  int          `json:"revisionNo"`
	Status      string       `json:"status"`
	ManaCharged int          `json:"manaCharged"`
	SinkRef     string       `json:"sinkRef,omitempty"`
	Error       string       `json:"error,omitempty"`
	StartedAt   time.Time    `json:"startedAt"`
	CompletedAt *time.Time   `json:"completedAt,omitempty"`
	Story       *runStoryDTO `json:"story,omitempty"`
}

type listRitualsResp struct {
	Rituals []ritualDTO `json:"rituals"`
	// WiredSinks is the sinks THIS deployment can write (B3b). Additive, and
	// deliberately NOT omitempty: an unwired server must serve [] rather than
	// omit the key, because the composer greys anything absent from this list
	// and needs to tell "nothing is wired" apart from "field not served".
	WiredSinks []string `json:"wiredSinks"`
}

type listRunsResp struct {
	Runs []ritualRunDTO `json:"runs"`
}

// ---- dispatch --------------------------------------------------------------

// handleRitualsRoute parses the rituals subtree (sub == "rituals" |
// "rituals/{rid}[/action]") and dispatches by method + action.
func (s *Server) handleRitualsRoute(w http.ResponseWriter, r *http.Request, companionID, sub string) {
	// Designer surface (list/create/get/publish) needs the publisher + repo;
	// the RUN handler additionally guards the runner + caps resolver (so the
	// designer stays usable even if the run engine/mana lane isn't wired yet).
	if s.RitualPublisher == nil || s.Rituals == nil {
		writeError(w, http.StatusServiceUnavailable, "RITUALS_NOT_WIRED", "")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(sub, "rituals"), "/") // "" | "{rid}" | "{rid}/{action}"
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			s.handleListRituals(w, r, companionID)
		case http.MethodPost:
			s.handleCreateRitual(w, r, companionID)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		}
		return
	}
	seg := strings.SplitN(rest, "/", 2)
	ritualID := seg[0]
	if len(seg) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleGetRitual(w, r, companionID, ritualID)
		return
	}
	// seg[1] is EVERYTHING after the ritual id, so it has to be cut again to
	// address a run: "runs" is the collection, "runs/{runId}" the single read.
	// The actions are cut by the SAME split and must then REFUSE a tail, or
	// ".../publish/anything" would match a bare "publish" case and perform the
	// publish. That is the hazard this cut introduces; it is why both actions
	// carry a test.
	action, tail := seg[1], ""
	if i := strings.Index(seg[1], "/"); i >= 0 {
		action, tail = seg[1][:i], seg[1][i+1:]
	}
	switch action {
	case "publish":
		if tail != "" {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "")
			return
		}
		s.postOnly(w, r, func() { s.handlePublishRitual(w, r, companionID, ritualID) })
	case "run":
		if tail != "" {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "")
			return
		}
		s.postOnly(w, r, func() { s.handleRunRitual(w, r, companionID, ritualID) })
	case "runs":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		if tail == "" {
			s.handleListRitualRuns(w, r, companionID, ritualID)
			return
		}
		// A run id is one segment. Anything deeper is not a run.
		if strings.Contains(tail, "/") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "")
			return
		}
		s.handleGetRitualRun(w, r, companionID, ritualID, tail)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "")
	}
}

func (s *Server) postOnly(w http.ResponseWriter, r *http.Request, fn func()) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	fn()
}

// ---- handlers --------------------------------------------------------------

func (s *Server) handleListRituals(w http.ResponseWriter, r *http.Request, companionID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	rituals, err := s.Rituals.ListByCompanion(rlsCtx(r, tenantID, gcid), tenantID, companionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_LIST_FAILED", err.Error())
		return
	}
	out := make([]ritualDTO, 0, len(rituals))
	for _, rt := range rituals {
		out = append(out, toRitualDTO(rt))
	}
	writeJSON(w, http.StatusOK, listRitualsResp{Rituals: out, WiredSinks: s.wiredSinks()})
}

func (s *Server) handleCreateRitual(w http.ResponseWriter, r *http.Request, companionID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req createRitualReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	rt, err := s.RitualPublisher.CreateDraft(rlsCtx(r, tenantID, gcid), tenantID, companionID, req.Name, companion.RitualTrigger(req.Trigger), req.Sink)
	if err != nil {
		mapRitualError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRitualDTO(rt))
}

func (s *Server) handleGetRitual(w http.ResponseWriter, r *http.Request, companionID, ritualID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	rt, err := s.Rituals.GetByID(rlsCtx(r, tenantID, gcid), tenantID, ritualID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_READ_FAILED", err.Error())
		return
	}
	if rt == nil {
		writeError(w, http.StatusNotFound, "RITUAL_NOT_FOUND", "")
		return
	}
	writeJSON(w, http.StatusOK, toRitualDTO(rt))
}

func (s *Server) handlePublishRitual(w http.ResponseWriter, r *http.Request, companionID, ritualID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req publishRitualReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	ctx := rlsCtx(r, tenantID, gcid)
	if _, _, err := s.RitualPublisher.Publish(ctx, companion.PublishRitualInput{
		TenantID: tenantID, CompanionID: companionID, OwnerGCID: gcid, RitualID: ritualID,
		Steps:        toDomainSteps(req.Steps),
		ArmorVerdict: req.ArmorVerdict,
		// EnsureTraceparent mints a root W3C trace when the caller (browser via
		// the CompanionBridge) sends none — the ritual_published outbox event's
		// envelope REQUIRES a traceparent, so a raw empty header 500s the publish.
		Traceparent: tracing.EnsureTraceparent(r.Header.Get("traceparent")), Tracestate: r.Header.Get("tracestate"),
	}); err != nil {
		mapRitualError(w, err)
		return
	}
	// Auto-enable within quota for the "active" UX (best-effort — a
	// quota-exhausted ritual stays disabled but published + runnable on demand).
	if err := s.RitualPublisher.SetEnabled(ctx, tenantID, companionID, gcid, ritualID, true); err != nil && !errors.Is(err, companion.ErrRitualEnabledQuotaReached) {
		mapRitualError(w, err)
		return
	}
	// Reload the full aggregate (with revisions) for the response.
	rt, err := s.Rituals.GetByID(ctx, tenantID, ritualID)
	if err != nil || rt == nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_READ_FAILED", "")
		return
	}
	writeJSON(w, http.StatusOK, toRitualDTO(rt))
}

func (s *Server) handleRunRitual(w http.ResponseWriter, r *http.Request, companionID, ritualID string) {
	if s.RitualRunner == nil || s.RitualCaps == nil {
		writeError(w, http.StatusServiceUnavailable, "RITUALS_RUN_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req runRitualReq
	_ = json.NewDecoder(r.Body).Decode(&req) // body optional
	trigger := req.TriggerSource
	if trigger == "" {
		trigger = string(companion.TriggerManual)
	}
	ctx := rlsCtx(r, tenantID, gcid)
	rt, err := s.Rituals.GetByID(ctx, tenantID, ritualID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_READ_FAILED", err.Error())
		return
	}
	if rt == nil {
		writeError(w, http.StatusNotFound, "RITUAL_NOT_FOUND", "")
		return
	}
	caps, err := s.RitualCaps.ResolveCapability(ctx, tenantID, companionID, gcid, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_CAPABILITY_FAILED", err.Error())
		return
	}
	// ASYNC ACK (CHO-2145). StartRun does only the fast, bounded work —
	// pre-flight guards, the mana reserve, the durable 'running' row — so we can
	// answer inside the api-gateway's 5s route budget. The slow phase (one LLM
	// turn per step, ~30s) then runs DETACHED: previously it ran on the request
	// ctx, the gateway 504'd the route at 5s, and the learner got a "Something
	// went wrong" toast on a perfectly healthy run.
	pending, err := s.RitualRunner.StartRun(ctx, companion.RunInput{
		TenantID: tenantID, CompanionID: companionID, OwnerGCID: gcid,
		Ritual: rt, Caps: caps, TriggerSource: trigger,
		// EnsureTraceparent: the run emits ritual_run_completed via the outbox,
		// whose envelope requires a traceparent — mint a root when absent.
		Traceparent: tracing.EnsureTraceparent(r.Header.Get("traceparent")), Tracestate: r.Header.Get("tracestate"),
	})
	if err != nil {
		mapRitualError(w, err) // pre-flight guard (unpublished / stage / since-unequipped)
		return
	}
	cat := s.learnerCatalogue(ctx)

	// Terminal at ack (skipped_budget — the reserve refused): nothing to execute,
	// so hand back the final record as a 200. A designed pause, never an error.
	if pending.Terminal() {
		// No source titles: a terminal-at-ack run is skipped_budget, the reserve
		// refused, no step ran and nothing was cited. Resolving here would be a
		// read that can only return nothing.
		writeJSON(w, http.StatusOK, toRunDTO(pending.Run, cat, nil))
		return
	}

	// Snapshot the ack BEFORE handing the run to the goroutine: Execute takes
	// ownership of pending.Run and mutates it (stamps, status, terminal fields),
	// so reading it after the `go` below would race the detached execution.
	// Everything sequenced before a `go` statement happens-before the goroutine,
	// so this snapshot is safe; the DTO shares nothing with the live run.
	// No source titles on the ACK either: the steps have not run, so nothing is
	// cited yet. The citations arrive with the terminal, and the run reads that
	// follow resolve them.
	ack := toRunDTO(pending.Run, cat, nil)
	runID := pending.Run.RunID

	// net/http cancels r.Context() the moment this response completes, so the
	// slow phase MUST NOT ride it: WithoutCancel keeps the ctx values (the tenant
	// GUC source for RLS + the trace span) and drops the cancellation. The run is
	// still bounded — Execute applies the runner's RunTimeout — and terminate()
	// persists on a ctx of its own, so the row always reaches a terminal state
	// (CHO-2138). A pod death mid-run is backstopped by the stale-sweep.
	execCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			// A panic in a detached goroutine would take the whole pod down and
			// with it every other in-flight run. Log it LOUD and leave the row to
			// the stale-sweep (which reclaims it to failed + refunds).
			if rec := recover(); rec != nil {
				log.Printf("consumption: ritual run %s PANIC in detached execution (left to the stale-sweep): %v\n%s",
					runID, rec, debug.Stack())
			}
		}()
		if _, execErr := pending.Execute(execCtx); execErr != nil {
			// The run itself already terminated (failed/blocked are terminal
			// OUTCOMES, returned with a nil error) — reaching here means the
			// terminal write or publish failed, which is an infra fault.
			log.Printf("consumption: ritual run %s detached execution failed after the 202 ack: %v", runID, execErr)
		}
	}()

	// 202 + the running record: the FE renders a non-error "running" state and
	// polls GET …/runs for the terminal.
	writeJSON(w, http.StatusAccepted, ack)
}

func (s *Server) handleListRitualRuns(w http.ResponseWriter, r *http.Request, companionID, ritualID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if s.RitualRuns == nil {
		writeError(w, http.StatusServiceUnavailable, "RITUALS_NOT_WIRED", "")
		return
	}
	ctx := rlsCtx(r, tenantID, gcid)
	// Opportunistic self-heal (CHO-2138): reclaim this tenant's stranded 'running'
	// rows before listing, so run history shows terminal states. Best-effort +
	// RLS-scoped (no cross-tenant scan) — the detached terminate prevents NEW
	// strands, this backstops any that slip through (e.g. a pod crash mid-run).
	// The dedicated cross-tenant scheduler is a flagged follow-up (mirrors the
	// WS-5 weakness-blob TTL-sweep pattern).
	if s.RitualRunner != nil {
		if n, sErr := s.RitualRunner.SweepStaleNow(ctx); sErr != nil {
			log.Printf("consumption: ritual stale-sweep (tenant=%s) non-fatal: %v", tenantID, sErr)
		} else if n > 0 {
			log.Printf("consumption: ritual stale-sweep reclaimed %d stranded ritual run(s) (tenant=%s)", n, tenantID)
		}
	}
	runs, err := s.RitualRuns.ListRunsByRitual(ctx, tenantID, ritualID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_RUNS_FAILED", err.Error())
		return
	}
	cat := s.learnerCatalogue(ctx)
	titles := s.ritualSourceTitles(ctx, runs...)
	out := make([]ritualRunDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunDTO(run, cat, titles))
	}
	writeJSON(w, http.StatusOK, listRunsResp{Runs: out})
}

// handleGetRitualRun serves GET …/rituals/{ritualID}/runs/{runID}: the single
// run a replay opens from a link.
//
// It exists rather than reusing the list because the list is CAPPED at 50 and
// ordered newest-first, so an older run is not addressable there at all, and
// because pulling fifty rows to render one is a read the editor should not
// have to make.
//
// Deliberately NOT run through the stale-sweep the list handler fires: the
// sweep is a tenant-wide write, and a read of one run is the wrong place to
// trigger it. A run still 'running' here reads as running, which is true.
func (s *Server) handleGetRitualRun(w http.ResponseWriter, r *http.Request, companionID, ritualID, runID string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if s.RitualRuns == nil {
		writeError(w, http.StatusServiceUnavailable, "RITUALS_NOT_WIRED", "")
		return
	}
	ctx := rlsCtx(r, tenantID, gcid)
	run, err := s.RitualRuns.GetRun(ctx, tenantID, runID)
	if err != nil {
		if errors.Is(err, companion.ErrRitualRunNotFound) {
			writeError(w, http.StatusNotFound, "RITUAL_RUN_NOT_FOUND", "")
			return
		}
		// A store that could not be read is 500, never 404: telling a learner
		// their run does not exist because the database was unreachable is a
		// confident false claim about their history.
		writeError(w, http.StatusInternalServerError, "RITUAL_RUN_READ_FAILED", err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusInternalServerError, "RITUAL_RUN_READ_FAILED",
			"repository returned no run and no error")
		return
	}
	// The path asserts a relationship and the handler holds it: tenant scoping
	// (RLS + the explicit predicate) already stops a cross-tenant read, but it
	// would happily serve run X under any ritual id the caller typed, so a link
	// to one ritual's replay could render another ritual's run. Same 404 as an
	// unknown id, deliberately: whether the run exists under a DIFFERENT ritual
	// is not this caller's business to learn.
	if run.RitualID != ritualID {
		writeError(w, http.StatusNotFound, "RITUAL_RUN_NOT_FOUND", "")
		return
	}
	writeJSON(w, http.StatusOK, toRunDTO(run, s.learnerCatalogue(ctx), s.ritualSourceTitles(ctx, run)))
}

// ---- mapping ---------------------------------------------------------------

func (s *Server) learnerCatalogue(ctx context.Context) map[string]companion.CatalogEntry {
	if s.SkillCatalog == nil {
		return nil
	}
	cat, err := s.SkillCatalog.ListCatalogue(ctx)
	if err != nil {
		return nil // learner story degrades to skill keys (never fails the run response)
	}
	return cat
}

func toDomainSteps(in []ritualStepDTO) []companion.RitualStep {
	out := make([]companion.RitualStep, 0, len(in))
	for _, s := range in {
		out = append(out, companion.RitualStep{
			SkillKey: s.SkillKey, Params: s.Params,
			StepID:          s.StepID,
			StepKind:        companion.RitualStepKind(s.StepKind),
			FromStepID:      s.FromStepID,
			Actor:           s.Actor,
			CapabilityScope: s.CapabilityScope,
		})
	}
	return out
}

func toRitualDTO(r *companion.Ritual) ritualDTO {
	steps := make([]ritualStepDTO, 0)
	for _, s := range r.CurrentSteps() {
		steps = append(steps, ritualStepDTO{
			SkillKey: s.SkillKey, Params: s.Params,
			StepID:          s.StepID,
			StepKind:        string(s.StepKind),
			FromStepID:      s.FromStepID,
			Actor:           s.Actor,
			CapabilityScope: s.CapabilityScope,
		})
	}
	return ritualDTO{
		RitualID: r.RitualID, CompanionID: r.CompanionID, Name: r.Name,
		Trigger: string(r.Trigger), Sink: r.Sink, Enabled: r.Enabled,
		PublishedPriceUnits: r.PublishedPriceUnits, CurrentRevision: r.CurrentRevision,
		Steps: steps, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// ritualSourceTitles resolves the atom ids a run cites to the names a learner
// would recognise (D2 tail a), through the SAME reader D3's character sheet
// uses. One resolver in this service, not two.
//
// Best-effort exactly like learnerCatalogue above: an unwired or failing reader
// yields no name and never fails the read. A run the learner can see without
// source names beats a 500 they cannot see at all, and the projection omits an
// unnamed id rather than printing a UUID either way.
//
// One lookup per DISTINCT id across every step, so a run whose ten steps cite
// the same atom makes one read and not ten.
//
// It reads Server.AtomTitles, the ATOM arm alone, not the two-arm
// ResonanceTitles the profile uses. That separation is the fix for a real
// defect this path shipped with for one commit: the two-arm reader binds only
// when BOTH projections are live, which protects the profile from a half
// resolver and, applied here, left every grounded step nameless wherever the
// concept graph was absent. A liveness fact about CONCEPTS must not gate
// ATOMS. Same resolver type behind both ports, so there is still one
// atom-title implementation in this service.
func (s *Server) ritualSourceTitles(ctx context.Context, runs ...*companion.RitualRun) map[string]string {
	if s.AtomTitles == nil {
		return nil
	}
	titles := map[string]string{}
	for _, run := range runs {
		if run == nil {
			continue
		}
		for _, stamp := range run.Stamps {
			for _, citation := range stamp.Citations {
				// The domain's own predicate, so the set collected here is
				// exactly the set it will filter.
				if !companion.CitationNeedsTitle(citation) {
					continue
				}
				if _, seen := titles[citation]; seen {
					continue
				}
				title, err := s.AtomTitles.AtomTitle(ctx, citation)
				if err != nil {
					log.Printf("consumption: ritual source title for atom %s non-fatal: %v", citation, err)
					title = "" // absent, and the projection omits it
				}
				titles[citation] = title
			}
		}
	}
	return titles
}

func toRunDTO(run *companion.RitualRun, catalogue map[string]companion.CatalogEntry, titles map[string]string) ritualRunDTO {
	story := companion.LearnerRunStoryWithTitles(run, catalogue, titles)
	steps := make([]runStepStoryDTO, 0, len(story.Steps))
	for _, st := range story.Steps {
		steps = append(steps, runStepStoryDTO{Title: st.Title, Summary: st.Summary, Sources: st.Sources})
	}
	return ritualRunDTO{
		RunID: run.RunID, RitualID: run.RitualID, RevisionNo: run.RevisionNo,
		Status: string(run.Status), ManaCharged: run.ManaCharged, SinkRef: run.SinkRef,
		Error: run.Error, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		Story: &runStoryDTO{Title: story.Title, Status: story.Status, Steps: steps, ManaCost: story.ManaCost},
	}
}

// wiredSinks reports what this deployment can write, always as a non-nil slice
// so the JSON carries [] rather than null. A nil registry (rituals lane not
// wired) answers "nothing", which greys every sink in the composer: the honest
// reading, since publish would refuse all six anyway.
func (s *Server) wiredSinks() []string {
	if s.RitualSinks == nil {
		return []string{}
	}
	out := s.RitualSinks.WiredSinks()
	if out == nil {
		return []string{}
	}
	return out
}

// mapRitualError maps domain sentinels to HTTP status + code (the FE renders
// the code + message).
func mapRitualError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrRitualNotFound):
		writeError(w, http.StatusNotFound, "RITUAL_NOT_FOUND", err.Error())
	case errors.Is(err, companion.ErrRitualUnlockStage):
		writeError(w, http.StatusForbidden, "RITUALS_LOCKED", err.Error())
	case errors.Is(err, companion.ErrRitualEnabledQuotaReached):
		writeError(w, http.StatusConflict, "RITUAL_QUOTA_REACHED", err.Error())
	// N5: a real platform sink with no writer in THIS deployment. Given its own
	// code, not folded into RITUAL_INVALID, because the learner did nothing
	// wrong and the FE greys this set rather than showing a validation error.
	case errors.Is(err, companion.ErrRitualSinkNotWired):
		writeError(w, http.StatusUnprocessableEntity, "RITUAL_SINK_NOT_WIRED", err.Error())
	// ADR-257 reserved shape: the caller reached for a field or a step kind the
	// runner does not honour yet. Also its own code, so the FE can say "not yet"
	// instead of "invalid".
	case errors.Is(err, companion.ErrRitualStepFieldReserved),
		errors.Is(err, companion.ErrRitualStepKindNotV1):
		writeError(w, http.StatusUnprocessableEntity, "RITUAL_STEP_RESERVED", err.Error())
	case errors.Is(err, companion.ErrRitualStepKindUnknown):
		writeError(w, http.StatusUnprocessableEntity, "RITUAL_INVALID", err.Error())
	case errors.Is(err, companion.ErrRitualNoSteps),
		errors.Is(err, companion.ErrRitualTooManySteps),
		errors.Is(err, companion.ErrRitualStepSkillNotEquipped),
		errors.Is(err, companion.ErrRitualStepSkillInactive),
		errors.Is(err, companion.ErrRitualUnknownSink),
		errors.Is(err, companion.ErrRitualUnknownTrigger),
		errors.Is(err, companion.ErrRitualTriggerNotV1),
		errors.Is(err, companion.ErrRitualNameRequired),
		errors.Is(err, companion.ErrRitualNameTooLong),
		errors.Is(err, companion.ErrRitualStepParamsInvalid):
		writeError(w, http.StatusUnprocessableEntity, "RITUAL_INVALID", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "RITUAL_ERROR", err.Error())
	}
}
