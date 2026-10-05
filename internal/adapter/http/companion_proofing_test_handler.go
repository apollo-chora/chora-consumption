// companion_proofing_test_handler.go — CHO-2040 (owner rulings R8-6 + R8-7):
// the Virgin Proofing Test COMPOSED runner.
//
//	POST /v1/me/companions/{id}/proofing-test   {"goal_id":"<uuid>",
//	                                            "mcq_count"?: 0..10,
//	                                            "oe_count"?:  0..10}
//	GET  /v1/me/proofing-tests?goal_id=
//
// The loop-closer: a learner (via their Companion) triggers generation of a
// fresh MCQ+OE assessment targeting the growth edges they ticked at the
// goal-binding ceremony. Per R8-6 this is a composed goal-driven runner in
// the ceremony edge-scout's posture — NO catalogue Skill row, NO
// grant/equip/stage gate. The generation machinery is entirely the LIVE qgen
// crew + TestSetComposer: the runner publishes ONE
// chora.creation.ai_assist.started.v2 batch event (the allowlisted
// cross-domain request lane) and the terminal completed/refused events land
// back via the proofing-test-terminal push inbox.
//
// Flow (each gate fail-loud, ordered cheapest-first):
//
//  1. Wiring gate → 503 PROOFING_TEST_NOT_WIRED (fail-soft at boot,
//     fail-loud per request — the EDGE_SCOUT_NOT_WIRED pattern).
//  2. Learner-authed → 401; body/plan validation → 400.
//  3. Companion owned → 404 COMPANION_NOT_FOUND (writeSkillError mapping).
//  4. Goal owned, READ-ONLY → 404 GOAL_NOT_FOUND (no-leak rule: foreign
//     goals render 404, never 403).
//  5. Ticked-edge read (intent-tagged ConceptNodes in the goal root's
//     subtree, migration 0066) → none ⇒ 422 NO_TICKED_EDGES.
//  6. R8-7 gate: the composer passes concept KEYS, never titles — any
//     un-keyed edge ⇒ 422 EDGES_LACK_KEYS (the KG session is adding
//     key-at-mint separately; until it lands this runner refuses honestly).
//  7. Mana reserve (proofing_test_gen, reserve→settle/refund per spec §3):
//     insufficient ⇒ 402 envelope; infra error ⇒ 502 (paid action — never
//     fail-open).
//  8. Persist the requested ProofingTest row; failure refunds ⇒ 500.
//  9. Publish the batch request; failure refunds + fails the row ⇒ 502.
//  10. 202 with the aggregate projection.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// proofingTestReq is the inbound body. Unknown fields are ignored (mirrors
// the ceremony decode style); goal_id is REQUIRED + UUID-shaped; the counts
// are the spec §1 type_plan knobs (both absent ⇒ the default 4 MCQ + 2 OE).
type proofingTestReq struct {
	GoalID   string `json:"goal_id"`
	MCQCount int    `json:"mcq_count"`
	OECount  int    `json:"oe_count"`
}

// proofingEdgeResp is one ticked target edge on the wire.
type proofingEdgeResp struct {
	ConceptID string `json:"concept_id"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	Intent    string `json:"intent"`
}

// proofingTestResp is the aggregate projection (202 create + list items).
type proofingTestResp struct {
	ID             string             `json:"id"`
	Status         string             `json:"status"`
	GoalID         string             `json:"goal_id"`
	CompanionID    string             `json:"companion_id"`
	AssistID       string             `json:"assist_id"`
	TargetEdges    []proofingEdgeResp `json:"target_edges"`
	ManaReserved   int64              `json:"mana_reserved"`
	FailureReason  string             `json:"failure_reason,omitempty"`
	TestSetPayload json.RawMessage    `json:"testset_payload,omitempty"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
}

// topicProofingAiAssistStartedV2 is the qgen crew's request lane — the ONE
// cross-domain topic consumption publishes (outbox allowlist + binary encoder
// in protomarshal/ai_assist_started_v2.go keep this name in lockstep).
const topicProofingAiAssistStartedV2 = "chora.creation.ai_assist.started.v2"

// proofingTestWired reports whether every composed port is real. A missing
// port must refuse the WHOLE runner — a silently narrowed compose would
// fabricate a thinner request than the learner paid for.
func (s *Server) proofingTestWired() bool {
	return s.ProofingTests != nil && s.ProofingTickedEdges != nil &&
		s.ProofingMana != nil && s.ProofingPublisher != nil && s.Goals != nil
}

// createProofingTest handles POST /v1/me/companions/{id}/proofing-test.
func (s *Server) createProofingTest(w http.ResponseWriter, r *http.Request, companionID string) {
	if !s.proofingTestWired() {
		writeError(w, http.StatusServiceUnavailable, "PROOFING_TEST_NOT_WIRED",
			"proofing-test runner ports not wired (repo/edges/mana/publisher/goals) — check cmd/server/proofing_test_wiring.go")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req proofingTestReq
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil && !errors.Is(derr, io.EOF) {
		writeError(w, http.StatusBadRequest, "BAD_JSON", derr.Error())
		return
	}
	goalID := strings.TrimSpace(req.GoalID)
	if goalID == "" || !uuidShaped(goalID) {
		writeError(w, http.StatusBadRequest, "INVALID_GOAL_REF",
			"goal_id required and must be a UUID")
		return
	}
	plan, perr := pt.BuildTypePlan(req.MCQCount, req.OECount)
	if perr != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLAN", perr.Error())
		return
	}

	ctx := repoCtx(r, tenantID, gcid)
	if _, ferr := s.loadOwnedInstance(ctx, tenantID, gcid, companionID); ferr != nil {
		s.writeSkillError(w, ferr)
		return
	}

	// Goal gate — owned by the caller, READ-ONLY (mirrors the edge-scout's
	// no-leak rule: any mismatch renders 404, never 403).
	g, gerr := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, "GOAL_READ_FAILED", gerr.Error())
		return
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	// Ticked learning-edges: intent-tagged ConceptNodes inside the goal
	// root's hierarchy subtree (migration 0066). A rootless goal HAS no
	// subtree — same honest refusal as an un-ticked map.
	rootConceptID := ""
	if g.RootConceptID != nil {
		rootConceptID = strings.TrimSpace(*g.RootConceptID)
	}
	var edges []pt.TargetEdge
	if rootConceptID != "" {
		edges, err = s.ProofingTickedEdges.ListTicked(ctx, tenantID, gcid, rootConceptID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PROOFING_EDGES_READ_FAILED", err.Error())
			return
		}
	}
	if verr := pt.ValidateTargetEdges(edges); verr != nil {
		switch {
		case errors.Is(verr, pt.ErrNoTickedEdges):
			writeError(w, http.StatusUnprocessableEntity, "NO_TICKED_EDGES",
				"the goal has no ticked (remediate/explore) learning edges — run the binding ceremony first")
		case errors.Is(verr, pt.ErrEdgesLackKeys):
			// R8-7 — the composer passes concept KEYS, never titles. The KG
			// key-at-mint change has not landed for these edges: refuse
			// loudly rather than forge keys from titles.
			writeError(w, http.StatusUnprocessableEntity, "EDGES_LACK_KEYS", verr.Error())
		default:
			writeError(w, http.StatusInternalServerError, "PROOFING_EDGES_INVALID", verr.Error())
		}
		return
	}

	// Within-request title-dedupe: cross-ceremony ConceptNode dupes share a
	// title; collapse them so the ONE published request (field-19 keys +
	// prompt) and the stored row probe each concept once. Runs AFTER the R8-7
	// validate so an un-keyed edge still refuses loudly; NewRequested re-dedupes
	// defensively for direct callers.
	edges = pt.DedupeTargetEdges(edges)

	// Cross-request idempotency: a double-submit / FE retry for the SAME goal +
	// edge-title signature while a generation is still in flight must NOT mint a
	// second row or charge mana twice — return the in-flight one. Only
	// non-terminal rows collide; a ready|failed test lets a fresh request
	// through (the learner can regenerate). The probe fails LOUD — a paid
	// action never fails open into a double charge on an unverifiable read.
	sig := pt.EdgeSignature(goalID, edges)
	if existing, perr := s.inflightProofingBySignature(ctx, tenantID, gcid, goalID, sig); perr != nil {
		writeError(w, http.StatusInternalServerError, "PROOFING_TEST_LIST_FAILED", perr.Error())
		return
	} else if existing != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"proofing_test": toProofingTestResp(existing)})
		return
	}

	// Aggregate first (mints the id the reservation nonce needs), then
	// reserve → persist → publish, refunding on any downstream failure.
	proofing, nerr := pt.NewRequested(pt.NewRequestedInput{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		GoalID:      goalID,
		CompanionID: companionID,
		TargetEdges: edges,
	})
	if nerr != nil {
		writeError(w, http.StatusInternalServerError, "PROOFING_TEST_INVALID", nerr.Error())
		return
	}

	reserve, rerr := s.ProofingMana.Reserve(ctx, pt.ReserveInput{
		TenantID:       tenantID,
		GCID:           gcid,
		ProofingTestID: proofing.ID,
	})
	if rerr != nil {
		var insf *pt.ErrInsufficientMana
		if errors.As(rerr, &insf) {
			writeInsufficientManaEnvelope(w, &companion.ErrInsufficientMana{
				RequiredUnits:  insf.RequiredUnits,
				CurrentBalance: insf.CurrentBalance,
			})
			return
		}
		// Paid action: an unreachable identity is a 502, never a free pass.
		writeError(w, http.StatusBadGateway, "PROOFING_MANA_RESERVE_FAILED", rerr.Error())
		return
	}
	proofing.ManaReserved = reserve.PriceUnits
	proofing.ReservationID = reserve.ReservationID

	refund := func(reason string) {
		if ferr := s.ProofingMana.Refund(ctx, pt.RefundInput{
			TenantID:      tenantID,
			GCID:          gcid,
			ReservationID: reserve.ReservationID,
			Units:         reserve.PriceUnits,
			Reason:        reason,
		}); ferr != nil {
			// The refund is idempotent by key but this request cannot retry
			// it — surface loudly for the ops trail (the learner support
			// path replays CreditMana against the reservation handle).
			logProofingRefundFailure(proofing.ID, ferr)
		}
	}

	if cerr := s.ProofingTests.Create(ctx, proofing); cerr != nil {
		// Refund either way — the reservation must never strand.
		if errors.Is(cerr, pt.ErrDuplicateInFlight) {
			// Lost a truly-concurrent race: the DB partial-unique index rejected
			// this row after our read-probe missed the winner. Return the
			// in-flight winner instead of double-charging or 500-ing.
			refund("proofing test superseded by a concurrent in-flight request")
			if winner, werr := s.inflightProofingBySignature(ctx, tenantID, gcid, goalID, sig); werr == nil && winner != nil {
				writeJSON(w, http.StatusAccepted, map[string]any{"proofing_test": toProofingTestResp(winner)})
				return
			}
			// The winner raced to terminal/deleted before we could read it —
			// surface honestly rather than fabricate a row.
			writeError(w, http.StatusConflict, "PROOFING_TEST_CONFLICT",
				"a concurrent proofing-test request superseded this one; retry")
			return
		}
		refund("proofing test row persist failed")
		writeError(w, http.StatusInternalServerError, "PROOFING_TEST_PERSIST_FAILED", cerr.Error())
		return
	}

	// The ONE generation request — mirrors chora-creation's batch publisher
	// shape (question_subscriber.runBatchSourceMaterial), promptless-grounded:
	// the growth-edge keys ride BOTH the prompt and wire field 19 (the
	// existing qgen bias seam), the mixed plan rides field 21, and the
	// ADR-195 compose trio routes the v2 event to the batch/set lane.
	rootTitle := "" // goal anchor for the generator prompt: the root concept's title when readable
	if rootConceptID != "" && s.ConceptNodes != nil {
		if node, nerr2 := s.ConceptNodes.GetByID(ctx, tenantID, gcid, rootConceptID); nerr2 == nil && node != nil {
			rootTitle = node.Title
		}
	}
	keys := make([]string, 0, len(edges))
	for _, e := range edges {
		keys = append(keys, strings.TrimSpace(e.Key))
	}
	wirePlan := make([]map[string]any, 0, len(plan))
	for _, q := range plan {
		wirePlan = append(wirePlan, map[string]any{
			"question_type": q.QuestionType,
			"count":         q.Count,
		})
	}
	prompt := pt.BuildPrompt(rootTitle, g.ConceptSet, edges, plan)
	tp := tracing.EnsureTraceparent(r.Header.Get("traceparent"))
	env := events.NewEnvelope(tenantID, gcid, tp, r.Header.Get("tracestate"),
		"proofing_test.requested."+proofing.AssistID)
	payload := map[string]any{
		"assist_id":    proofing.AssistID,
		"tenant_id":    tenantID,
		"author_gcid":  gcid,
		"content_type": "mixed",
		// Payload-parity key only (NOT on the v2 wire): the primary type for
		// legacy readers; the orchestrator derives routing from the plan.
		"question_type":       "mcq",
		"prompt":              prompt,
		"requested_count":     pt.RequestedCount(plan),
		"max_retries":         0,
		"started_at":          time.Now().UTC(),
		"metadata":            proofingMetadata(rootTitle),
		"target_growth_edges": keys,
		"type_plan":           wirePlan,
		// ADR-195 WS7 compose model — {count>1 | type_plan} routes BATCH.
		"operation":  "compose",
		"intent":     "new_question",
		"input_kind": "prompt",
	}
	if perr := s.ProofingPublisher.Publish(topicProofingAiAssistStartedV2, env, payload); perr != nil {
		refund("proofing test publish failed")
		// The row exists — land it honestly failed rather than stranded in
		// requested with no crew pickup (best-effort; the 502 stands either way).
		if ferr := proofing.MarkFailed("publish failed: "+perr.Error(), time.Time{}); ferr == nil {
			if uerr := s.ProofingTests.Update(ctx, proofing); uerr != nil {
				logProofingRefundFailure(proofing.ID, uerr)
			}
		}
		writeError(w, http.StatusBadGateway, "PROOFING_TEST_PUBLISH_FAILED", perr.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"proofing_test": toProofingTestResp(proofing),
	})
}

// inflightProofingBySignature returns the learner's non-terminal proofing test
// on this goal whose edge-title signature matches, or (nil, nil) if none.
// Backs both the pre-flight idempotency probe and the post-conflict lookup
// (the DB partial-unique index is the concurrency backstop; this resolves the
// winning row to return).
func (s *Server) inflightProofingBySignature(ctx context.Context, tenantID, gcid, goalID, sig string) (*pt.ProofingTest, error) {
	rows, err := s.ProofingTests.ListByLearner(ctx, tenantID, gcid, goalID)
	if err != nil {
		return nil, err
	}
	for _, ex := range rows {
		if ex.Terminal() {
			continue
		}
		if pt.EdgeSignature(ex.GoalID, ex.TargetEdges) == sig {
			return ex, nil
		}
	}
	return nil, nil
}

// proofingMetadata builds the started.v2 metadata hints (stable keys per the
// qgen prompt contract: category / subject).
func proofingMetadata(rootTitle string) map[string]string {
	md := map[string]string{"category": "proofing_test"}
	if t := strings.TrimSpace(rootTitle); t != "" {
		md["subject"] = t
	}
	return md
}

// handleMeProofingTests handles GET /v1/me/proofing-tests?goal_id=.
func (s *Server) handleMeProofingTests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	if s.ProofingTests == nil {
		writeError(w, http.StatusServiceUnavailable, "PROOFING_TEST_NOT_WIRED",
			"proofing-test repository not wired — check cmd/server/proofing_test_wiring.go")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	goalID := strings.TrimSpace(r.URL.Query().Get("goal_id"))
	if goalID != "" && !uuidShaped(goalID) {
		writeError(w, http.StatusBadRequest, "INVALID_GOAL_REF", "goal_id filter must be a UUID")
		return
	}
	ctx := repoCtx(r, tenantID, gcid)
	rows, lerr := s.ProofingTests.ListByLearner(ctx, tenantID, gcid, goalID)
	if lerr != nil {
		writeError(w, http.StatusInternalServerError, "PROOFING_TEST_LIST_FAILED", lerr.Error())
		return
	}
	items := make([]proofingTestResp, 0, len(rows))
	for _, row := range rows {
		items = append(items, toProofingTestResp(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// toProofingTestResp maps the aggregate onto the wire shape (target_edges is
// always a non-nil array — the FE never sees null).
func toProofingTestResp(p *pt.ProofingTest) proofingTestResp {
	edges := make([]proofingEdgeResp, 0, len(p.TargetEdges))
	for _, e := range p.TargetEdges {
		edges = append(edges, proofingEdgeResp{
			ConceptID: e.ConceptID,
			Key:       e.Key,
			Title:     e.Title,
			Intent:    e.Intent,
		})
	}
	out := proofingTestResp{
		ID:            p.ID,
		Status:        string(p.Status),
		GoalID:        p.GoalID,
		CompanionID:   p.CompanionID,
		AssistID:      p.AssistID,
		TargetEdges:   edges,
		ManaReserved:  p.ManaReserved,
		FailureReason: p.FailureReason,
		CreatedAt:     p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if len(p.TestSetPayload) > 0 {
		out.TestSetPayload = json.RawMessage(p.TestSetPayload)
	}
	return out
}

// logProofingRefundFailure keeps the compensation trail loud (the refund is
// idempotent by key — a support runbook can replay CreditMana against the
// reservation handle). Var-hooked so tests can observe without log noise.
var logProofingRefundFailure = func(proofingTestID string, err error) {
	log.Printf("consumption: proofing test %s compensation error (manual CreditMana replay may be needed): %v",
		proofingTestID, err)
}
