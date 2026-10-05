// companion_chat_handler.go — ADR-154 conversational chat SSE handler.
//
// Endpoint:
//
//	POST /v1/me/companions/{id}/chat
//
// Wire contract (per ADR-154 D1 / D4):
//
//	Content-Type:     text/event-stream
//	Cache-Control:    no-cache
//	X-Accel-Buffering: no
//
// Frames (one of):
//
//	event: session_open   data: {"engine_session_id":"...","turn_id":"..."}
//	event: tool_call      data: {"tool":"atom_search","args":{...}}
//	event: token          data: {"text":"..."}
//	event: turn_complete  data: {"engine_session_id":"...","output_tokens":N,"mana_charged":N,"model":"...","finish_reason":"STOP"}
//	event: error          data: {"code":"ENGINE_STREAM_ABORTED","message":"..."}
//
// Mana (per ADR-154 D3): tier-aware pre-flight DeductMana — 5/15/30
// per turn for basic/standard/premium. On ErrInsufficientMana the
// handler returns 402 with the canonical InsufficientManaUpsell envelope
// from learner-economy.yaml (NOT an SSE frame — JSON only).
//
// Session reuse (per ADR-154 D2): the handler queries
// companion_chat_sessions for the open session for (gcid, companion_id);
// on a hit the engine_session_id is forwarded as ExistingSessionID;
// on a miss (ErrChatSessionNotFound) the engine client mints a fresh
// session and the handler persists the new row.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// chatRequest is the inbound JSON body shape.
type chatRequest struct {
	Message         string `json:"message"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	Locale          string `json:"locale,omitempty"`
	// TurnID is the client-minted turn id (ADR-254 D8: the SPA mints it so a
	// retried POST of the same turn is answered from the stored result instead
	// of dispatching a second turn). Empty = consumption mints one.
	TurnID string `json:"turn_id,omitempty"`
}

// Default per-turn cap matches the OpenAPI default. Engine-side cap is the
// hard guard; this is a polite default for the LLM.
const defaultChatMaxOutputTokens = 512

// handleCompanionChat handles POST /v1/me/companions/{id}/chat.
//
// The companion_id is extracted from the path by the router prefix matcher
// (handleCompanionInstanceByID dispatches the /chat leaf to this method).
func (s *Server) handleCompanionChat(w http.ResponseWriter, r *http.Request, companionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "GATEWAY_METHOD_NOT_ALLOWED", "POST only")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if strings.TrimSpace(companionID) == "" {
		writeJSONError(w, http.StatusNotFound, "COMPANION_NOT_FOUND",
			"companion_id missing from path")
		return
	}
	if s.CompanionEngine == nil || s.CompanionEngineResource == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "ENGINE_NOT_CONFIGURED",
			"companion turn lane not wired at chora-consumption boot")
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSONError(w, http.StatusBadRequest, "BAD_REQUEST", "message required")
		return
	}
	if req.MaxOutputTokens <= 0 {
		req.MaxOutputTokens = defaultChatMaxOutputTokens
	}

	// Resolve mana tier from the learner's wallet state. For build-out the
	// engine's manaplugin authoritatively overrides per the actual
	// subscription tier; this hint defaults to basic to keep the unfunded
	// Phyllis user on the 5-mana action_code (the 402 educational moment).
	manaTier := resolveManaTier(s, tenantID, gcid)
	actionCode := companion.ChatTurnActionCodeForTier(manaTier)

	// 0. ADR-252 containment (ADR-254 D11), ADVISORY and deliberately FIRST.
	// The gateway is the control and will refuse the turn anyway; refusing it
	// here buys the learner an honest 403 with the operator's reason instead of
	// a raw upstream error mid-stream, and it costs no mana read, no session
	// write and no SSE header (once those flush the body can no longer carry a
	// 403). It also outranks the 402 upsell: a paused turn cannot run at any
	// balance, so selling mana for it would be dishonest.
	//
	// A Status() ERROR is UNKNOWN, never "not paused" and never a refusal: the
	// projection must not become a second, local, fail-closed control that
	// turns one bad read into a chat outage nobody ordered.
	if s.CompanionSuspension != nil {
		if st, serr := s.CompanionSuspension.Status(repoCtx(r, tenantID, gcid), tenantID, actionCode); serr != nil {
			log.Printf("consumption: companion suspension advisory read FAILED tenant=%s gcid=%s action=%s: %v (proceeding; chora-model-gateway remains the control)",
				tenantID, gcid, actionCode, serr)
		} else if st.Paused {
			log.Printf("consumption: companion chat REFUSED by the advisory suspension projection tenant=%s gcid=%s companion=%s action=%s scope=%s",
				tenantID, gcid, companionID, actionCode, st.Scope)
			writeJSONError(w, http.StatusForbidden, companion.ErrCodeCompanionSuspended,
				"your Companion is paused ("+string(st.Scope)+"): "+st.Reason)
			return
		}
	}

	// 1. Pre-flight mana AFFORDABILITY check (read-only) — ADR-177 full umbrella.
	// The model-gateway is now the SOLE mana DEBITER for the Companion chat turn
	// (the agent stamps `mana_action_code` onto the gateway Invoke and the
	// gateway debits ONCE per turn). We no longer debit here — that would
	// double-charge. We keep a READ-ONLY balance check ONLY to surface the
	// canonical 402 upsell BEFORE the SSE stream opens: once the 200 + SSE
	// headers flush (step 4) the body can no longer carry a 402, and an
	// in-stream gateway mana-block would otherwise surface as a silent empty
	// reply. No debit ⇒ no double-charge; the gateway remains the ledger truth.
	// ADR-254 D8: the SPA mints turn_id; a retried POST with the same id replays
	// the stored result (the engine never re-publishes a known turn). Absent =
	// consumption mints.
	// The id is CLIENT input on this handler alone (the only site reading
	// req.TurnID). Absent means consumption mints one, per D8. Present but not a
	// UUID is the caller's mistake and is named as such here, because the
	// alternative is the pg uuid cast on `turn_id UUID PRIMARY KEY` rejecting it
	// at insert and surfacing a learner's typo as an internal error. NOT
	// substituted with a minted id on failure: a client-minted id exists so a
	// retried POST replays the stored result, and quietly swapping in a fresh one
	// would turn a typo into a duplicate dispatch.
	turnID := strings.TrimSpace(req.TurnID)
	if req.TurnID == "" {
		turnID = domain.NewUUIDv7()
	} else {
		validated, verr := companion.ValidateTurnID(req.TurnID)
		if verr != nil {
			writeJSONError(w, http.StatusBadRequest, companion.TurnErrCodeInvalidTurnID,
				"turn_id must be a UUID")
			return
		}
		turnID = validated
	}
	if s.ManaQuoter != nil {
		balCtx := r.Context()
		balCtx = clients.WithTraceparent(balCtx, r.Header.Get("traceparent"))
		if snap, berr := s.ManaQuoter.GetBalance(balCtx, gcid); berr == nil {
			if cost, lerr := companion.LookupCost(actionCode); lerr == nil && snap.BalanceUnits < cost {
				writeInsufficientManaEnvelope(w, &companion.ErrInsufficientMana{
					RequiredUnits:  cost,
					CurrentBalance: snap.BalanceUnits,
				})
				return
			}
		} else {
			// Balance read failed — fail-open per ADR-142 §8 (medium-tier action
			// backstop). The gateway still gates + debits server-side.
			log.Printf("consumption: companion chat balance pre-check non-fatal error: %v", berr)
		}
	}

	// 2. Session lookup / reuse.
	var existingEngineSessionID string
	var sessionID string
	if s.CompanionChatSessions != nil {
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		ctx = tracing.WithGCID(ctx, gcid)
		if existing, gerr := s.CompanionChatSessions.Get(ctx, gcid, companionID); gerr == nil {
			existingEngineSessionID = existing.EngineSessionID
			sessionID = existing.ID
		} else if !errors.Is(gerr, companion.ErrChatSessionNotFound) {
			// Persistence error — fail-loud to surface the problem instead
			// of silently re-creating sessions every turn.
			log.Printf("consumption: companion chat session repo Get error: %v — fail-loud", gerr)
			writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", gerr.Error())
			return
		}
	}
	if sessionID == "" {
		sessionID = domain.NewUUIDv7()
	}

	// 2b. Resolve the Companion's session-bootstrap config locally + inject it
	// into the engine session state (bug-3 mesh-sidestep, 2026-06-02). The
	// sidecar-less Companion agent reads its config (specialization / persona /
	// rules / allowed skills / growth stage) from session state — it cannot
	// call back over consumption's STRICT-mTLS gRPC. Fail-loud: a resolve error
	// means the agent would refuse the turn (0-output) anyway, so surface it.
	var companionConfigJSON string
	if s.CompanionConfigJSONResolver != nil {
		cfgJSON, cerr := s.CompanionConfigJSONResolver(r.Context(), tenantID, companionID, gcid)
		if cerr != nil {
			log.Printf("consumption: companion chat config resolve failed (companion=%s): %v", companionID, cerr)
			writeJSONError(w, http.StatusBadGateway, "COMPANION_CONFIG_RESOLVE_FAILED", cerr.Error())
			return
		}
		companionConfigJSON = cfgJSON
	}

	// 2c. F4 (ADR-173) — recall per-Companion memory + inject it into the engine
	// session state (mesh-sidestep, mirrors companion_config). Requires BOTH the
	// embedder and the pgvector store; either nil ⇒ memory disabled (skip).
	// Soft-fail throughout: memory is supplementary, never load-bearing — a
	// recall miss/error must never block or 5xx the chat turn.
	var companionMemoryJSON string
	// recalledMemories survives past this block on purpose (CHO-2192): these exact
	// rows are what the turn is grounded on, so they are also what the turn must
	// DISCLOSE. The grounding frame is built from them at step 4b.
	var recalledMemories []companion.MemoryRow
	if s.Embedder != nil && s.CompanionMemory != nil {
		memCtx := tracing.WithTenantID(r.Context(), tenantID)
		memCtx = tracing.WithGCID(memCtx, gcid)
		if qvec, eerr := s.Embedder.Embed(memCtx, companion.EmbedInput{
			Text: req.Message, TaskType: companion.EmbedTaskQuery, TenantID: tenantID,
		}); eerr != nil {
			log.Printf("consumption: companion memory recall embed failed (non-fatal): %v", eerr)
		} else {
			topK := s.CompanionMemoryRecallTopK
			if topK <= 0 {
				topK = defaultMemoryRecallTopK
			}
			if rows, rerr := s.CompanionMemory.Recall(memCtx, companionID, qvec, topK); rerr != nil {
				log.Printf("consumption: companion memory recall failed (non-fatal): %v", rerr)
			} else if len(rows) > 0 {
				// R3 (gap F5): screen retrieved content BEFORE it becomes context.
				// recalledMemories is set to the SURVIVORS, not the raw rows, so
				// the grounding frame at step 4b attributes only what was actually
				// injected (companion_chat_grounding.go's stated invariant).
				kept, dropped := screenRecalledMemories(rows)
				if dropped > 0 {
					// Never silent. A refusal that leaves no trace is
					// indistinguishable from a recall miss, and the difference is
					// the whole signal this gate exists to produce.
					log.Printf("consumption: companion memory screen refused %d of %d recalled memories (tenant=%s companion=%s)",
						dropped, len(rows), tenantID, companionID)
				}
				recalledMemories = kept
				companionMemoryJSON = buildMemoryContextJSON(kept)
			}
		}
	}

	// 2d. W7 (Epic-1b) — fetch the learner's top Growth Edges + inject them
	// into the engine session state (mesh-sidestep, mirrors 2c memory).
	// Soft-fail: supplementary context only, never blocks the turn.
	lwCtx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	learnerWeaknessJSON := s.chatLearnerWeaknessJSON(lwCtx, tenantID, gcid)

	// 3. Open the engine stream (ADR-254 D8: the BusTurnEngine writes the turn
	// row + the outbox request in one tx and emits session_open + accepted the
	// moment that is durable; the reply arrives as turn_complete | error).
	traceparentHdr, tracestateHdr := traceFromHeaders(r)
	chatReq := clients.CompanionChatRequest{
		TenantID:            tenantID,
		UserGCID:            gcid,
		CompanionID:         companionID,
		ManaTier:            manaTier,
		Message:             req.Message,
		ExistingSessionID:   existingEngineSessionID,
		MaxOutputTokens:     req.MaxOutputTokens,
		TurnID:              turnID,
		CompanionConfigJSON: companionConfigJSON,
		CompanionMemoryJSON: companionMemoryJSON,
		// W7: top Growth Edges for the agent's prompt weave (empty = absent).
		LearnerWeaknessJSON: learnerWeaknessJSON,
		// ADR-177: stamp the per-turn action_code so the gateway debits the
		// chat turn ONCE (this handler no longer debits — see step 1).
		ManaActionCode: actionCode,
		// ADR-254 D8: the consumption chat-session row IS the conversation the
		// agent persists under; locale rides the request; trace context goes
		// on the request envelope.
		ConversationID: sessionID,
		Locale:         req.Locale,
		Traceparent:    traceparentHdr,
		Tracestate:     tracestateHdr,
		TurnKind:       string(companion.TurnKindTyped),
	}
	frames, err := s.CompanionEngine.StreamChat(r.Context(), chatReq)
	if err != nil {
		if errors.Is(err, agentengine.ErrEngineNotConfigured) {
			writeJSONError(w, http.StatusServiceUnavailable, "ENGINE_NOT_CONFIGURED",
				err.Error())
			return
		}
		writeJSONError(w, http.StatusBadGateway, "ENGINE_STREAM_FAILED", err.Error())
		return
	}

	// 4. Stream SSE frames. Once we start writing the body we can no
	// longer change the status code or content-type — flush headers first.
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	// 4b. CHO-2192 (ADR-231 D4/D5, IMDA D2) — disclose the grounded sources behind
	// anything this turn is about to restate from a recalled research note.
	//
	// Emitted BEFORE the engine's frames, which is the point: the learner meets the
	// sources before they read the claim, not after. The rows were recalled at 2c,
	// so nothing is fetched here and no engine round-trip is added.
	//
	// Emitted IF AND ONLY IF a grounded note was recalled — see buildGroundingFrame.
	// A marshal failure is loud in the log but never fatal: attribution is owed to
	// the learner, yet failing their whole turn over it would be a worse answer than
	// a late one, and the log names it so it cannot rot silently.
	//
	// ADR-254 D8 frame order (the SPA contract): session_open, accepted,
	// grounding, then turn_complete | error. The grounding frame is therefore
	// written right after `accepted` is forwarded (inside the loop below), not
	// before the engine's frames.
	var groundingData []byte
	if gframe, grounded := buildGroundingFrame(recalledMemories); grounded {
		if data, merr := json.Marshal(gframe); merr != nil {
			log.Printf("consumption: companion chat grounding frame marshal failed (non-fatal): %v", merr)
		} else {
			groundingData = data
		}
	}

	// Track per-turn rollups for the persisted ChatSession + the
	// CompanionChatTurnCompleted event.
	manaCharged := chatTurnCost(manaTier)
	turnStarted := time.Now().UTC()
	var (
		toolCallsCount int
		model          string
		outputTokens   int
		finishReason   string
		engineSession  = existingEngineSessionID
		replyText      strings.Builder // F4 — assembled reply for memory record
	)

	for frame := range frames {
		// Inspect for the session_open + turn_complete payloads so we can
		// snapshot the engine_session_id + token counts. The frame Data
		// remains forwarded unchanged.
		switch frame.Type {
		case clients.ChatFrameSessionOpen:
			var p struct {
				EngineSessionID string `json:"engine_session_id"`
			}
			_ = json.Unmarshal(frame.Data, &p)
			if p.EngineSessionID != "" {
				engineSession = p.EngineSessionID
			}
		case clients.ChatFrameToolCall:
			toolCallsCount++
		case clients.ChatFrameToken:
			// F4 — accumulate the streamed reply for the post-turn memory record.
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(frame.Data, &p)
			replyText.WriteString(p.Text)
		case clients.ChatFrameTurnComplete:
			var p struct {
				Model        string `json:"model"`
				OutputTokens int    `json:"output_tokens"`
				FinishReason string `json:"finish_reason"`
				ReplyText    string `json:"reply_text"`
			}
			_ = json.Unmarshal(frame.Data, &p)
			model = p.Model
			outputTokens = p.OutputTokens
			finishReason = p.FinishReason
			// ADR-254 D8: on the bus path the WHOLE reply rides turn_complete
			// (token frames are retired); it is the memory record's source.
			if p.ReplyText != "" {
				replyText.Reset()
				replyText.WriteString(p.ReplyText)
			}
		}
		// Re-marshal turn_complete to inject mana_charged + turn_id so
		// the FE can render the running ledger without a side fetch.
		data := frame.Data
		if frame.Type == clients.ChatFrameTurnComplete {
			data = augmentTurnCompleteFrame(frame.Data, turnID, manaCharged)
		}
		// 4b (continued): the grounding disclosure is written BEFORE the first
		// content frame (the learner meets the sources before the reply) and
		// right AFTER `accepted` on the bus path, so the SPA's order is
		// session_open, accepted, grounding, then turn_complete | error.
		if groundingData != nil && frame.Type != clients.ChatFrameSessionOpen && frame.Type != clients.ChatFrameAccepted {
			writeSSEFrame(w, chatFrameGrounding, groundingData)
			groundingData = nil
		}
		writeSSEFrame(w, frame.Type, data)
		if frame.Type == clients.ChatFrameAccepted && groundingData != nil {
			writeSSEFrame(w, chatFrameGrounding, groundingData)
			groundingData = nil
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	// 5. Persist session row + emit CompanionChatTurnCompleted event.
	if s.CompanionChatSessions != nil && engineSession != "" {
		now := time.Now().UTC()
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		ctx = tracing.WithGCID(ctx, gcid)
		// Build / update the value object.
		var sess companion.ChatSession
		if existing, gerr := s.CompanionChatSessions.Get(ctx, gcid, companionID); gerr == nil {
			sess = *existing
		} else {
			sess, _ = companion.NewChatSession(companion.NewChatSessionInput{
				ID:              sessionID,
				TenantID:        tenantID,
				OwnerGCID:       gcid,
				CompanionID:     companionID,
				EngineSessionID: engineSession,
				Now:             turnStarted,
			})
		}
		updated := sess.RecordTurn(manaCharged, now)
		if perr := s.CompanionChatSessions.Save(ctx, updated); perr != nil {
			log.Printf("consumption: companion chat session save error: %v", perr)
		}
	}

	// 6. Outbox publish chora.consumption.companion.chat_turn_completed.v1.
	if s.Publisher != nil {
		traceparent, tracestate := traceFromHeaders(r)
		env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate,
			fmt.Sprintf("chat-turn:%s", turnID))
		payload := map[string]any{
			"session_id":        sessionID,
			"turn_id":           turnID,
			"gcid":              gcid,
			"tenant_id":         tenantID,
			"companion_id":      companionID,
			"engine_session_id": engineSession,
			"model":             model,
			"output_tokens":     outputTokens,
			"mana_charged":      manaCharged,
			"mana_tier":         manaTier,
			"tool_calls_count":  toolCallsCount,
			"finish_reason":     finishReason,
			"started_at":        turnStarted,
			"completed_at":      time.Now().UTC(),
		}
		if perr := s.Publisher.Publish(events.TopicCompanionChatTurnCompleted, env, payload); perr != nil {
			log.Printf("consumption: companion chat publish error: %v", perr)
		}
	}

	// 7. F4 (ADR-173) — record this turn's memory (learner message + assembled
	// reply) for future recall. Runs after the reply has fully streamed, so a
	// failure here NEVER affects the learner's turn. Soft-fail throughout.
	if s.Embedder != nil && s.CompanionMemory != nil {
		assembled := strings.TrimSpace(replyText.String())
		if assembled != "" {
			content := "Learner: " + req.Message + "\nCompanion: " + assembled
			memCtx := tracing.WithTenantID(r.Context(), tenantID)
			memCtx = tracing.WithGCID(memCtx, gcid)
			if dvec, eerr := s.Embedder.Embed(memCtx, companion.EmbedInput{
				Text: content, TaskType: companion.EmbedTaskDocument, TenantID: tenantID,
			}); eerr != nil {
				log.Printf("consumption: companion memory record embed failed (non-fatal): %v", eerr)
			} else if rerr := s.CompanionMemory.Record(memCtx, companion.RecordMemoryInput{
				TenantID:        tenantID,
				OwnerGCID:       gcid,
				CompanionID:     companionID,
				MemoryType:      "chat_turn",
				ContentText:     content,
				Embedding:       dvec,
				ModelID:         s.CompanionEmbeddingModelID,
				SourceTurnID:    turnID,
				SourceSessionID: sessionID,
				Now:             time.Now().UTC(),
			}); rerr != nil {
				log.Printf("consumption: companion memory record failed (non-fatal): %v", rerr)
			}
		}
	}
}

// defaultMemoryRecallTopK is the per-turn cap on recalled memories when the
// Server's CompanionMemoryRecallTopK is unset (<=0).
const defaultMemoryRecallTopK = 5

// screenRecalledMemories applies the R3 retrieved-content gate to the turn's
// recalled set, returning the memories that may be injected plus how many were
// refused. Content on a survivor is the SCREENED text.
//
// Gap F5: "the mandatory PII and policy gate on retrieved content does not exist
// anywhere". StepAssurance3 was meant to be that gate and was dropped 2026-06-01
// for latency. This is it, at the point of INJECTION rather than at the prompt:
// the moment consumption assembles the context that becomes the turn.
//
// It is NOT the fence. chora-companion already wraps recalled memories in a
// data-not-instructions preamble between BEGIN/END markers (CHO-2041). This is
// the content layer beneath it, a service earlier, so a known-hostile memory is
// never carried at all rather than merely never reaching an instruction
// position. See companion.ScreenRecalledMemory for the false-positive budget.
//
// ⚠ THE RETURNED SLICE FEEDS BOTH the injected JSON and the grounding frame, and
// that is load-bearing rather than convenience. companion_chat_grounding.go's
// invariant is that we attribute what we INJECTED; a grounded note the screen
// refused was never injected, so citing it would tell the learner the answer
// drew on a source the turn never saw.
func screenRecalledMemories(rows []companion.MemoryRow) ([]companion.MemoryRow, int) {
	kept := make([]companion.MemoryRow, 0, len(rows))
	dropped := 0
	for _, m := range rows {
		// CHO-2060 first (strip any baked-in ref UUID), then the policy gate over
		// the flattened text. The UUID strip cannot mask a policy pattern, and
		// running it first keeps the pre-R3 behaviour intact for survivors.
		verdict := companion.ScreenRecalledMemory(lp.SanitizeLearnerText(m.ContentText))
		if verdict.Dropped {
			dropped++
			continue
		}
		m.ContentText = verdict.Content
		kept = append(kept, m)
	}
	return kept, dropped
}

// buildMemoryContextJSON renders recalled memories as compact JSON the
// Companion agent's InstructionProvider weaves into the per-turn system prompt
// as a "Relevant past context" section. Order is preserved (nearest-first from
// the cosine recall). Returns "" on marshal error (caller omits the key).
//
// Callers pass the SCREENED set (screenRecalledMemories). The CHO-2060 UUID
// strip is re-applied here anyway, and that redundancy is deliberate: it is
// idempotent, and moving it out entirely made this function silently stop
// honouring CHO-2060 for any caller that forgot to screen first. The R3 gate is
// NOT duplicated here, because a policy refusal has to remove the memory from
// the turn as a whole (the grounding frame reads the same set), which is a
// decision only the caller can take.
func buildMemoryContextJSON(rows []companion.MemoryRow) string {
	items := make([]map[string]any, 0, len(rows))
	for _, m := range rows {
		items = append(items, map[string]any{
			// CHO-2060: no baked-in ref UUID reaches this learner-facing system
			// prompt. Date only, so a full RFC3339 stamp cannot carry a raw clock
			// time into it either.
			"content":     lp.SanitizeLearnerText(m.ContentText),
			"recorded_at": m.CreatedAt.UTC().Format("2006-01-02"),
		})
	}
	out, err := json.Marshal(map[string]any{"memories": items})
	if err != nil {
		return ""
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeSSEFrame writes one SSE event onto w in the canonical W3C wire format.
func writeSSEFrame(w http.ResponseWriter, eventType string, data []byte) {
	if eventType != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", eventType)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

// writeJSONError writes a JSON error envelope with the canonical
// {error:{code,message}} shape. Used for ALL non-200 responses on the chat
// route (the 402 envelope has its own writer that carries upsell).
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// writeInsufficientManaEnvelope writes the canonical 402 envelope from
// learner-economy.yaml#InsufficientManaErrorResponse. Render-compatible with
// the FE upsell component used across P5 / P6 / P7.5 / daily-dose-coach.
func writeInsufficientManaEnvelope(w http.ResponseWriter, ime *companion.ErrInsufficientMana) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)
	body := map[string]any{
		"error": map[string]any{
			"code":    "insufficient_mana",
			"message": "top-up required",
			"upsell": map[string]any{
				"required_units":        ime.RequiredUnits,
				"current_balance_units": ime.CurrentBalance,
				"recommended_plan_code": "companion_basic",
			},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// augmentTurnCompleteFrame re-marshals the turn_complete frame data with
// the per-turn turn_id + mana_charged injected. The engine doesn't know
// these (mana_charged is BFF-resolved; turn_id is FE-correlation).
func augmentTurnCompleteFrame(in json.RawMessage, turnID string, manaCharged int) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(in, &m); err != nil {
		return in
	}
	m["turn_id"] = turnID
	m["mana_charged"] = manaCharged
	out, _ := json.Marshal(m)
	return out
}

// resolveManaTier picks the mana tier hint for this turn.
//
// Task 2(b): the fallback tier is now CONFIG-DRIVEN (Server.DefaultManaTier,
// sourced from COMPANION_DEFAULT_MANA_TIER at boot per feedback_no_inline_config)
// rather than the previously-hardcoded "basic" literal. A per-user
// subscription lookup (M14.iter6) will take precedence over the default when
// wired; until then the default holds (so unfunded Phyllis demos still surface
// the 402 educational moment on the 5-mana action code under the default
// "basic" plan).
//
// Sane default: when Server.DefaultManaTier is empty (defensive — NewServer
// always seeds it), fall back to the canonical growth.DefaultManaTier so the
// handler never returns an empty tier the cost map can't price.
func resolveManaTier(s *Server, _, _ string) string {
	if s != nil && s.DefaultManaTier != "" {
		return s.DefaultManaTier
	}
	return growth.DefaultManaTier
}

// chatTurnCost mirrors the in-memory canonical cost map for the chat tier
// action codes (5 / 15 / 30). The mana_action_pricing table in chora_identity
// is authoritative at the gRPC layer; this is the local-projection used for
// the persisted session counter + the chat_turn_completed event payload.
func chatTurnCost(tier string) int {
	switch tier {
	case "premium":
		return 30
	case "standard":
		return 15
	default:
		return 5
	}
}

// Compile-time stamp so the otherwise-unused context import in test paths
// is anchored to the production handler too.
var _ = context.Background
