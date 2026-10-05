// bus_turn_engine.go - ADR-254 D4/D8: the Companion engine port on the BUS.
//
// Every Companion turn consumption makes (typed chat, skill invoke, ceremony
// edge scout, ritual step, daily-dose greeting) leaves through this one path:
//
//  1. build the BINDING request payload (the kennel passes it VERBATIM to the
//     chat binary; keys are the pre-rename wire names inside that lane:
//     familiar_id / familiar_config / familiar_memory / ..., ADR-254 D6);
//  2. TurnStore.Begin: the companion_turns row + the outbox request row in ONE
//     transaction (accepted = durably requested);
//  3. emit session_open {conversation_id, turn_id} + accepted {turn_id,
//     accepted_at} at once (no bus "accepted" topic; ADR-254 D4);
//  4. wait on the STORE (any pod's pull consumer may complete the row) until
//     the result or the deadline, then emit tool_call* + turn_complete (whole
//     reply) or error {turn_id, code, message};
//  5. a retried turn_id is answered from the stored result without a second
//     publish (the kennel dedupes the request key for seven days and never
//     re-dispatches); an in-flight turn_id attaches to the existing turn.
//
// It implements CompanionEnginePort (StreamChat + Greet), so the SSE handler
// and the blocking callers (skill invoke, ceremony, ritual step, dose greeting)
// keep their frame-draining code; `token` frames are never emitted (streaming
// is gone until the chunked-events upgrade, ADR-254 D12), the reply rides
// turn_complete.reply_text.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TurnRequestPublisher writes the outbox REQUEST row inside the transaction
// that TurnStore.Begin opened (the ambient querier): pg.TxOutbox adapted at the
// composition root. A publisher that cannot join the transaction must fail.
type TurnRequestPublisher interface {
	PublishInTx(ctx context.Context, topic string, env events.Envelope, payload map[string]any) error
}

// TurnRequestPublisherFunc adapts a function to TurnRequestPublisher.
type TurnRequestPublisherFunc func(ctx context.Context, topic string, env events.Envelope, payload map[string]any) error

// PublishInTx satisfies TurnRequestPublisher.
func (f TurnRequestPublisherFunc) PublishInTx(ctx context.Context, topic string, env events.Envelope, payload map[string]any) error {
	return f(ctx, topic, env, payload)
}

// BusTurnEngineConfig wires the engine.
type BusTurnEngineConfig struct {
	Store   companion.TurnStore
	Publish TurnRequestPublisher
	// PromptOverrides is the ADR-197 P3 registry resolver (optional, fail-open:
	// unresolved = the keys are omitted and the agent renders its baseline).
	PromptOverrides PromptOverridesResolver
	// TurnDeadline bounds the wait for the kennel's result. Default 130s: the
	// kennel's park ledger tightens typed chat to 120s (ADR-254 D5) so the
	// learner gets FAILED, not silence; the margin lets that FAILED arrive
	// before consumption's own TIMEOUT.
	TurnDeadline time.Duration
	// PollInterval is the store poll period while waiting. Default 400ms.
	PollInterval time.Duration
	// Now is the clock (tests).
	Now func() time.Time
}

// DefaultTurnDeadline / DefaultTurnPollInterval are the config defaults.
const (
	DefaultTurnDeadline     = 130 * time.Second
	DefaultTurnPollInterval = 400 * time.Millisecond
	defaultGreetingPrompt   = "Greet me for today's daily dose."
	wireTurnKindTyped       = "typed"
)

// BusTurnEngine is the production CompanionEnginePort.
type BusTurnEngine struct {
	cfg BusTurnEngineConfig
}

var _ CompanionEnginePort = (*BusTurnEngine)(nil)

// NewBusTurnEngine validates the wiring and applies defaults.
func NewBusTurnEngine(cfg BusTurnEngineConfig) (*BusTurnEngine, error) {
	if cfg.Store == nil {
		return nil, errors.New("bus turn engine: TurnStore required")
	}
	if cfg.Publish == nil {
		return nil, errors.New("bus turn engine: TurnRequestPublisher required")
	}
	if cfg.TurnDeadline <= 0 {
		cfg.TurnDeadline = DefaultTurnDeadline
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultTurnPollInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &BusTurnEngine{cfg: cfg}, nil
}

// StreamChat publishes one typed companion turn and streams its frames:
// session_open, accepted, then (after the result) tool_call* + turn_complete,
// or error. The returned channel is closed after the terminal frame, or when
// ctx is cancelled (the turn itself keeps running; a reconnect replays it).
func (e *BusTurnEngine) StreamChat(ctx context.Context, req CompanionChatRequest) (<-chan ChatStreamFrame, error) {
	if err := validateBusChatRequest(req); err != nil {
		return nil, err
	}
	turnID := strings.TrimSpace(req.TurnID)
	if turnID == "" {
		turnID = domain.NewUUIDv7()
	}
	kind := companion.TurnKind(strings.TrimSpace(req.TurnKind))
	if kind == "" {
		kind = companion.TurnKindTyped
	}

	// Replay / attach: a known turn_id is never re-published.
	if existing, err := e.cfg.Store.Get(ctx, req.TenantID, turnID); err == nil {
		if existing.OwnerGCID != req.UserGCID {
			return nil, fmt.Errorf("%w: turn_id %s belongs to another learner", agentengine.ErrInvalidRequest, turnID)
		}
		return e.streamExisting(ctx, existing), nil
	} else if !errors.Is(err, companion.ErrTurnNotFound) {
		return nil, fmt.Errorf("bus turn engine: lookup turn %s: %w", turnID, err)
	}

	now := e.cfg.Now().UTC()
	overrides := e.resolveOverrides(ctx, req.TenantID)
	payload, err := BuildCompanionTurnRequestPayload(req, turnID, now, overrides)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("bus turn engine: marshal request: %w", err)
	}
	turn, err := companion.NewTurn(companion.NewTurnInput{
		TurnID:         turnID,
		TenantID:       req.TenantID,
		OwnerGCID:      req.UserGCID,
		CompanionID:    req.CompanionID,
		ConversationID: req.ConversationID,
		Kind:           kind,
		Lane:           companion.TurnLaneCompanionTurn,
		Request:        body,
		Now:            now,
		Deadline:       e.cfg.TurnDeadline,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", agentengine.ErrInvalidRequest, err)
	}
	traceparent := strings.TrimSpace(req.Traceparent)
	if traceparent == "" {
		traceparent = syntheticTraceparent(turnID)
	}
	env := events.NewEnvelope(req.TenantID, req.UserGCID, traceparent, req.Tracestate, turnID)
	publish := func(ctx context.Context) error {
		return e.cfg.Publish.PublishInTx(ctx, events.TopicCompanionTurnRequested, env, payload)
	}
	if err := e.cfg.Store.Begin(ctx, turn, publish); err != nil {
		if errors.Is(err, companion.ErrTurnExists) {
			// Lost a race with an identical retry: attach to the winner.
			if existing, gerr := e.cfg.Store.Get(ctx, req.TenantID, turnID); gerr == nil {
				return e.streamExisting(ctx, existing), nil
			}
		}
		return nil, fmt.Errorf("bus turn engine: begin turn %s: %w", turnID, err)
	}

	out := make(chan ChatStreamFrame, 16)
	e.pushOpenAccepted(out, turn)
	go e.waitAndEmit(ctx, turn.TenantID, turn.TurnID, out)
	return out, nil
}

// Greet is the blocking form: one typed turn whose message is the greeting
// prompt; it returns the whole reply or the caller-facing error.
func (e *BusTurnEngine) Greet(ctx context.Context, req CompanionGreetRequest) (CompanionGreetResponse, error) {
	prompt := strings.TrimSpace(req.UserPrompt)
	if prompt == "" {
		prompt = defaultGreetingPrompt
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = domain.NewUUIDv7()
	}
	frames, err := e.StreamChat(ctx, CompanionChatRequest{
		TenantID:            req.TenantID,
		UserGCID:            req.UserGCID,
		CompanionID:         req.CompanionID,
		ManaTier:            req.ManaTier,
		Message:             prompt,
		TurnID:              req.TurnID,
		ConversationID:      conversationID,
		CompanionConfigJSON: req.CompanionConfigJSON,
		TurnKind:            string(companion.TurnKindGreeting),
		Traceparent:         req.Traceparent,
		Tracestate:          req.Tracestate,
	})
	if err != nil {
		return CompanionGreetResponse{}, err
	}
	var resp CompanionGreetResponse
	for f := range frames {
		switch f.Type {
		case ChatFrameTurnComplete:
			var p struct {
				ReplyText    string `json:"reply_text"`
				Model        string `json:"model"`
				FinishReason string `json:"finish_reason"`
			}
			if uerr := json.Unmarshal(f.Data, &p); uerr != nil {
				return CompanionGreetResponse{}, fmt.Errorf("bus turn engine: greet turn_complete decode: %w", uerr)
			}
			resp.Greeting = p.ReplyText
			resp.Model = p.Model
			resp.FinishReason = p.FinishReason
		case ChatFrameError:
			var p chatErrorPayload
			_ = json.Unmarshal(f.Data, &p)
			return CompanionGreetResponse{}, fmt.Errorf("bus turn engine: greet %s: %s", p.Code, p.Message)
		}
	}
	if resp.Greeting == "" && resp.Model == "" {
		return CompanionGreetResponse{}, errors.New("bus turn engine: greet ended without a result (caller cancelled)")
	}
	return resp, nil
}

func validateBusChatRequest(req CompanionChatRequest) error {
	switch {
	case strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.UserGCID) == "" ||
		strings.TrimSpace(req.CompanionID) == "" || strings.TrimSpace(req.ManaTier) == "" ||
		strings.TrimSpace(req.Message) == "":
		return fmt.Errorf("%w: tenant_id + user_gcid + companion_id + mana_tier + message all required",
			agentengine.ErrInvalidRequest)
	case strings.TrimSpace(req.ConversationID) == "":
		return fmt.Errorf("%w: conversation_id required (the agent persists the session under it)",
			agentengine.ErrInvalidRequest)
	}
	return nil
}

func (e *BusTurnEngine) resolveOverrides(ctx context.Context, tenantID string) PromptOverridesResolution {
	if e.cfg.PromptOverrides == nil {
		return PromptOverridesResolution{}
	}
	res, err := e.cfg.PromptOverrides.ResolvePromptOverrides(ctx, tenantID)
	if err != nil {
		log.Printf("WARN bus turn engine: prompt override resolve failed (fail-open, embedded baseline renders): %v", err)
		return PromptOverridesResolution{}
	}
	return res
}

// BuildCompanionTurnRequestPayload renders the BINDING companion_turn.requested
// body (coordinator 2026-08-22 17:31Z; chora-contracts
// proto/events/consumption/companion_turn.proto, UseProtoNames): the keys the
// chat binary's agentdispatch.SessionState + plugins decode by NAME.
// growth_stage / species / aha_moment_active_until are lifted out of the
// resolved CompanionInstanceConfig (protojson); visible_kg_neighbors is OMITTED
// by ruling (RETIRED R3-1: consumption has no live list). Empty optional keys
// are omitted (absent = none), never sent as "".
func BuildCompanionTurnRequestPayload(req CompanionChatRequest, turnID string, now time.Time, overrides PromptOverridesResolution) (map[string]any, error) {
	payload := map[string]any{
		"tenant_id":       req.TenantID,
		"gcid":            req.UserGCID,
		"familiar_id":     req.CompanionID,
		"conversation_id": req.ConversationID,
		"turn_id":         turnID,
		"turn_kind":       wireTurnKindTyped,
		"message":         req.Message,
		"mana_tier":       req.ManaTier,
		"requested_at":    now.UTC().Format(time.RFC3339Nano),
	}
	putIf := func(key, val string) {
		if strings.TrimSpace(val) != "" {
			payload[key] = val
		}
	}
	putIf("locale", req.Locale)
	putIf("mana_action_code", req.ManaActionCode)
	putIf("familiar_memory", req.CompanionMemoryJSON)
	putIf("learner_weakness", req.LearnerWeaknessJSON)
	putIf("prompt_overrides_json", overrides.OverridesJSON)
	putIf("resolved_prompt_version", overrides.Version)
	if strings.TrimSpace(req.CompanionConfigJSON) != "" {
		var cfg consumptionv1.CompanionInstanceConfig
		if err := protojson.Unmarshal([]byte(req.CompanionConfigJSON), &cfg); err != nil {
			return nil, fmt.Errorf("%w: familiar_config is not a CompanionInstanceConfig: %v", agentengine.ErrInvalidRequest, err)
		}
		payload["familiar_config"] = req.CompanionConfigJSON
		// A JSON NUMBER: the growth-stage plugin refuses a string permanently.
		payload["growth_stage"] = int(cfg.GetGrowthStage())
		putIf("species", cfg.GetSpecies())
		if ts := cfg.GetAhaMomentActiveUntil(); ts != nil && ts.IsValid() {
			payload["aha_moment_active_until"] = ts.AsTime().UTC().Format(time.RFC3339Nano)
		}
	}
	return payload, nil
}

// streamExisting serves a known turn: terminal -> replay; in flight -> attach.
func (e *BusTurnEngine) streamExisting(ctx context.Context, turn *companion.Turn) <-chan ChatStreamFrame {
	out := make(chan ChatStreamFrame, 16)
	e.pushOpenAccepted(out, turn)
	if turn.IsTerminal() {
		e.emitTerminal(turn, out)
		close(out)
		return out
	}
	go e.waitAndEmit(ctx, turn.TenantID, turn.TurnID, out)
	return out
}

func (e *BusTurnEngine) pushOpenAccepted(out chan<- ChatStreamFrame, turn *companion.Turn) {
	open, _ := json.Marshal(map[string]any{
		"conversation_id":   turn.ConversationID,
		"turn_id":           turn.TurnID,
		"engine_session_id": turn.ConversationID, // legacy key: the agent session IS the conversation
	})
	out <- ChatStreamFrame{Type: ChatFrameSessionOpen, Data: open}
	acc, _ := json.Marshal(map[string]any{
		"turn_id":     turn.TurnID,
		"accepted_at": turn.RequestedAt.UTC().Format(time.RFC3339Nano),
	})
	out <- ChatStreamFrame{Type: ChatFrameAccepted, Data: acc}
}

// waitAndEmit polls the store until the turn is terminal or past its deadline.
// Store errors are logged and retried until the deadline (a transient pg blip
// must not fail a turn the kennel is still answering). On ctx cancellation the
// channel closes silently: the turn keeps running; a reconnect replays it.
func (e *BusTurnEngine) waitAndEmit(ctx context.Context, tenantID, turnID string, out chan<- ChatStreamFrame) {
	defer close(out)
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	for {
		turn, err := e.cfg.Store.Get(ctx, tenantID, turnID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("WARN bus turn engine: poll turn %s: %v (retrying)", turnID, err)
		} else {
			if turn.IsTerminal() {
				e.emitTerminal(turn, out)
				return
			}
			if now := e.cfg.Now().UTC(); !now.Before(turn.DeadlineAt) {
				applied, terr := e.cfg.Store.Timeout(ctx, tenantID, turnID, now)
				if terr != nil {
					log.Printf("WARN bus turn engine: timeout turn %s: %v (retrying)", turnID, terr)
				} else {
					// Re-read: either our timeout applied or the result beat it.
					if again, gerr := e.cfg.Store.Get(ctx, tenantID, turnID); gerr == nil && again.IsTerminal() {
						if applied {
							log.Printf("consumption: companion turn %s TIMEOUT after %s (no result from the kennel)", turnID, e.cfg.TurnDeadline)
						}
						e.emitTerminal(again, out)
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// emitTerminal renders a terminal turn as frames (tool_call* + turn_complete,
// or error).
func (e *BusTurnEngine) emitTerminal(turn *companion.Turn, out chan<- ChatStreamFrame) {
	if turn.Status != companion.TurnStatusCompleted {
		ep, _ := json.Marshal(map[string]any{
			"turn_id": turn.TurnID,
			"code":    turn.CallerErrorCode(),
			"message": turn.CallerErrorMessage(),
		})
		out <- ChatStreamFrame{Type: ChatFrameError, Data: ep}
		return
	}
	// A COMPLETED turn whose stored body cannot be read is an ERROR, never a
	// blank success (D2 tail b).
	//
	// This used to log a WARN and carry on with a zero TurnResult, which served
	// the learner a turn_complete with empty reply_text, no model, no grounding
	// and no provenance. That is a fail-loud violation twice over: the learner
	// sees a companion that answered nothing and looks broken, and a ritual
	// step stamps a SUCCESSFUL step that produced nothing, which is exactly the
	// empty-result-versus-error confusion ADR-252 D6 calls the single most
	// likely way to build this wrong.
	//
	// An empty stored body takes the same road for the same reason: there is
	// nothing to render, so rendering a success claims the companion answered
	// and said nothing.
	if len(turn.Result) == 0 {
		log.Printf("ERROR bus turn engine: turn %s is completed with NO stored result; "+
			"refusing to serve it as an empty success", turn.TurnID)
		emitUnreadableResult(turn, out)
		return
	}
	parsed, perr := companion.ParseTurnResultJSON(turn.Lane, turn.Result)
	if perr != nil {
		log.Printf("ERROR bus turn engine: stored result of turn %s unreadable: %v; "+
			"refusing to serve it as an empty success", turn.TurnID, perr)
		emitUnreadableResult(turn, out)
		return
	}
	res := parsed.Result
	for _, tc := range res.ToolCalls {
		tp, _ := json.Marshal(map[string]any{"tool": tc.Name, "summary": tc.Summary})
		out <- ChatStreamFrame{Type: ChatFrameToolCall, Data: tp}
	}
	model := res.GeneratedByModelID
	if model == "" {
		model = turn.GeneratedByModelID
	}
	finish := "stop"
	if strings.TrimSpace(res.RefusalReason) != "" {
		finish = "refusal"
	}
	tools := res.ToolCalls
	if tools == nil {
		tools = []companion.TurnToolCall{}
	}
	grounding := res.Grounding
	if grounding == nil {
		grounding = []companion.TurnGrounding{}
	}
	tc, _ := json.Marshal(map[string]any{
		"turn_id":               turn.TurnID,
		"conversation_id":       turn.ConversationID,
		"engine_session_id":     turn.ConversationID,
		"reply_text":            res.ReplyText,
		"grounding":             grounding,
		"tool_calls":            tools,
		"refusal_reason":        res.RefusalReason,
		"generated_by_model_id": model,
		"model":                 model,
		"finish_reason":         finish,
		// N9: the ADR-197 prompt provenance the agent stamped. This frame is
		// SYNTHESISED here, not forwarded, so a field missing from this map is
		// missing from the wire the ritual step reads however good the upstream
		// is. Empty when the stored turn predates the forwarding, never
		// fabricated.
		"prompt_version": res.PromptVersion,
		"prompt_source":  res.PromptSource,
		"prompt_hash":    res.PromptHash,
		"workflow_id":    turn.WorkflowID,
	})
	out <- ChatStreamFrame{Type: ChatFrameTurnComplete, Data: tc}
}

// emitUnreadableResult renders a completed-but-unreadable turn as the failure
// it is.
//
// It reuses TurnErrCodeFailed and its learner copy rather than minting a new
// code: from the learner's side this IS a turn the companion could not finish,
// and a code no client has ever seen would render as an unknown error. The
// distinguishing detail belongs in the operator log above, which names the turn
// and the parse error; the learner is owed a reason, not a diagnosis.
func emitUnreadableResult(turn *companion.Turn, out chan<- ChatStreamFrame) {
	ep, _ := json.Marshal(map[string]any{
		"turn_id": turn.TurnID,
		"code":    companion.TurnErrCodeFailed,
		"message": "The Companion could not finish this turn.",
	})
	out <- ChatStreamFrame{Type: ChatFrameError, Data: ep}
}

// syntheticTraceparent derives a valid W3C traceparent from the turn id when
// the caller supplied none (the envelope validator refuses an empty field; the
// SDK always sends one, so this is the test/probe path).
func syntheticTraceparent(turnID string) string {
	hexid := strings.ReplaceAll(turnID, "-", "")
	pad := func(s string, n int) string {
		s = strings.ToLower(s)
		for len(s) < n {
			s += "0"
		}
		return s[:n]
	}
	return "00-" + pad(hexid, 32) + "-" + pad(hexid, 16) + "-01"
}
