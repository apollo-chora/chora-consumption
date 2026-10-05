// companion_engine_client.go - the Companion engine PORT + its request /
// response / frame types.
//
// ADR-254 D8: the production implementation is the BusTurnEngine
// (bus_turn_engine.go): every Companion turn rides the companion_turn lane on
// the bus; the direct Vertex / GKE web-mode client that used to live here was
// deleted with the direct-invoke estate (ADR-254 D12/D13). Handlers depend on
// the port only and substitute fakes in tests.
package clients

import (
	"context"
	"encoding/json"
	"strings"
)

// CompanionEnginePort is the abstraction over the Companion Reasoning
// Engine call. Production uses agentengine.Client; tests substitute a fake.
//
// The port covers BOTH the daily-dose path (Greet) AND the ADR-154
// conversational chat path (StreamChat). Handlers that depend on the
// port can substitute fakes in tests without dragging in agentengine.
type CompanionEnginePort interface {
	Greet(ctx context.Context, req CompanionGreetRequest) (CompanionGreetResponse, error)
	StreamChat(ctx context.Context, req CompanionChatRequest) (<-chan ChatStreamFrame, error)
}

// CompanionGreetRequest carries the per-learner state required by the
// Companion engine's manaplugin + instancedispatch + tieredmodelplugin
// chain. All fields are mandatory; missing keys cause the engine to
// refuse the session.
type CompanionGreetRequest struct {
	TenantID    string
	UserGCID    string
	CompanionID string // identifies which Companion instance to dispatch to
	ManaTier    string // basic | standard | premium
	UserPrompt  string // typically "Greet me for today's daily dose."

	// CompanionConfigJSON is the protojson of the resolved consumption
	// CompanionInstanceConfig (bug-3 mesh-sidestep, mirrors StreamChat). When
	// non-empty it is injected into the engine CreateSession state under
	// "companion_config" so the sidecar-less Companion agent reads its per-Companion
	// config from session state instead of dialling consumption's STRICT-mTLS
	// gRPC (which the mesh wall blocks). WITHOUT it the instancedispatch resolver
	// refuses the turn → empty greeting (iteration_count:0). Empty ⇒ the key is
	// omitted (the agent falls back to its stub registry resolver, if any).
	CompanionConfigJSON string

	// ADR-254 D8 (bus path): the greeting is a typed companion_turn like any
	// other. TurnID is optional (minted when empty); ConversationID is the
	// consumption chat-session id the agent persists under (minted per greeting
	// when empty); Traceparent/Tracestate ride the request envelope.
	TurnID         string
	ConversationID string
	Traceparent    string
	Tracestate     string
}

// CompanionGreetResponse carries the engine's reply text + per-call usage.
type CompanionGreetResponse struct {
	Greeting     string
	Model        string // e.g. gemini-2.5-flash-lite
	OutputTokens int
	FinishReason string
}

// ChatStreamFrame is one outbound chat-stream event. The Data is the
// JSON-encoded payload (no `data:` prefix; the HTTP layer adds the SSE wire
// framing). Type values are one of the ChatFrame* constants.
type ChatStreamFrame struct {
	// Type is the SSE event name (session_open / tool_call / token /
	// turn_complete / error).
	Type string
	// Data is the JSON-encoded payload for this frame.
	Data json.RawMessage
}

// ChatFrame* — canonical SSE event names per ADR-154 D4, amended by ADR-254 D8
// (chat on the bus): `accepted` is sent the moment the turn request is durably
// in the outbox; `turn_complete` carries the WHOLE reply (reply_text,
// grounding[], tool_calls[], refusal_reason, generated_by_model_id); `token`
// streaming is retired on the bus path (kept as a name so the retired frame is
// recognisable, never emitted by the BusTurnEngine).
const (
	ChatFrameSessionOpen  = "session_open"
	ChatFrameAccepted     = "accepted"
	ChatFrameToolCall     = "tool_call"
	ChatFrameToken        = "token"
	ChatFrameTurnComplete = "turn_complete"
	ChatFrameError        = "error"
)

// CompanionChatRequest carries the per-turn state required by the
// Companion engine + the optional ExistingSessionID for the long-lived
// session reuse path (ADR-154 D2).
type CompanionChatRequest struct {
	TenantID    string
	UserGCID    string
	CompanionID string // identifies which Companion instance to dispatch to
	ManaTier    string // basic | standard | premium

	// Message is the learner's prompt for this turn (REQUIRED).
	Message string

	// ExistingSessionID, when non-empty, tells the client to REUSE this
	// engine session_id instead of calling CreateSession. The chat-history
	// the engine has accumulated across prior turns is preserved.
	ExistingSessionID string

	// MaxOutputTokens caps the engine's per-turn output. Default 512.
	MaxOutputTokens int

	// TurnID is the per-turn UUIDv7 the caller already minted for trace
	// correlation. Forwarded into the session_open + turn_complete frames.
	TurnID string

	// CompanionConfigJSON is the protojson of the resolved consumption
	// CompanionInstanceConfig (bug-3 mesh-sidestep, 2026-06-02). When non-empty
	// it is injected into the ADK CreateSession state under "companion_config"
	// so the sidecar-less Companion agent reads its per-Companion config from
	// session state instead of calling consumption's STRICT-mTLS gRPC.
	CompanionConfigJSON string

	// CompanionMemoryJSON is the JSON-encoded "relevant past context" recalled
	// for this turn (F4 pgvector RAG, ADR-173). When non-empty it is injected
	// into the ADK CreateSession state under "companion_memory" — mirroring the
	// CompanionConfigJSON mesh-sidestep — so the sidecar-less Companion agent can
	// weave it into the per-turn system prompt without dialling back into
	// consumption. Empty ⇒ the key is omitted (no recalled memory this turn).
	CompanionMemoryJSON string

	// LearnerWeaknessJSON carries the learner's top Growth Edges (W7,
	// Epic-1b) for injection into the ADK CreateSession state under
	// "learner_weakness" — the same mesh-sidestep as companion_memory: the
	// sidecar-less agent reads it from session state and weaves a "growth
	// edges" section into the per-turn system prompt. Empty ⇒ key omitted.
	// Shape: {"growth_edges":[{"label","concept_key","strength","summary",
	// "suggested_angles"}...]} (shakiest first, capped).
	LearnerWeaknessJSON string

	// ManaActionCode is the per-turn mana action_code (e.g.
	// companion_chat_turn_basic) the gateway debits for this chat turn (ADR-177
	// full umbrella). Injected into the ADK CreateSession state under
	// "mana_action_code"; the Companion agent's tenant-propagation plugin reads
	// it and stamps it onto the gateway Invoke so the gateway debits ONCE per
	// turn. Empty ⇒ the gateway un-meters this turn (caller has not cut its own
	// ad-hoc debit yet — safe sequencing per ADR-142 §4 addendum).
	ManaActionCode string

	// ADR-254 D8 (bus path). ConversationID is the consumption chat-session
	// row id (per owner + Companion, reused across turns); the kennel/agent
	// persists the conversation under it (required for typed chat). Locale is
	// the learner's request locale (empty when the client omits it).
	// Traceparent/Tracestate ride the request envelope (the SDK always sends
	// traceparent; the handler synthesises one when absent). TurnKind is
	// consumption's bookkeeping kind (typed | skill | ceremony | ritual |
	// greeting; default typed); the wire discriminator is always "typed".
	ConversationID string
	Locale         string
	Traceparent    string
	Tracestate     string
	TurnKind       string
}

// chatSessionOpenPayload is the session_open frame body.
type chatSessionOpenPayload struct {
	EngineSessionID string `json:"engine_session_id"`
	TurnID          string `json:"turn_id,omitempty"`
}

// chatToolCallPayload mirrors the SSE tool_call frame body.
type chatToolCallPayload struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// chatTokenPayload — one token chunk body.
type chatTokenPayload struct {
	Text string `json:"text"`
}

// chatTurnCompletePayload — final frame body.
type chatTurnCompletePayload struct {
	TurnID          string `json:"turn_id,omitempty"`
	EngineSessionID string `json:"engine_session_id"`
	Model           string `json:"model"`
	OutputTokens    int    `json:"output_tokens"`
	FinishReason    string `json:"finish_reason"`
	// ReplyText is the WHOLE reply on the bus path (ADR-254 D8; token frames
	// are retired there). Empty on the legacy streamed path.
	ReplyText string `json:"reply_text,omitempty"`
	// ADR-197 P3 (CHO-2368): the resolved prompt version this client injected
	// into the session state at CreateSession - what the agent's composer +
	// stamping plugin actually ran. Empty when no resolver is configured
	// (never fabricated); consumed by the ritual step stamp (G4).
	//
	// FED as of N9 step 2, after being dark from CHO-2368 to 2026-09-02. The
	// agent stamped it all along; _completed_event in the kennel's
	// orchestrators/consumption_lanes.py dropped it when building
	// companion_turn.completed.v1, and companion.ParseTurnResultJSON had no
	// field for it, so this parsed a key nothing sent. All three hops carry it
	// now, and emitTerminal puts it on the frame.
	PromptVersion string `json:"prompt_version,omitempty"`

	// PromptSource says WHICH prompt shaped the turn: "registry" when a
	// resolved override did, "embedded_fallback" when the agent's built-in
	// default did because none resolved. Carried beside the version because a
	// version alone cannot tell an operator whether their override was actually
	// in effect, which is the first question an unexpected answer raises.
	PromptSource string `json:"prompt_source,omitempty"`

	// PromptHash is the SHA-256 of the instruction the AGENT ran. Carried, never
	// computed on this side: consumption does not hold that prompt.
	PromptHash string `json:"prompt_hash,omitempty"`

	// Grounding is the ADR-249 verified source list for this turn: the atoms
	// the answer was actually grounded on. Unlike prompt_version above, this
	// one IS fed on the production path (emitTerminal already puts it on the
	// frame), and it is field-compatible with companion.TurnGrounding and with
	// the agent's own GroundingRef by explicit design on all three sides.
	//
	// These are validator-CONFIRMED sources. Per ADR-249 the agent emits a ref
	// only when cite_atom confirmed the atom exists for the tenant; a rejected
	// id is dropped here and stays visible in tool_calls. That is precisely why
	// an absent list must stamp nothing: a citation invented for an ungrounded
	// answer is the platform vouching for a source that was never consulted.
	Grounding []chatGroundingRef `json:"grounding,omitempty"`
}

// chatGroundingRef is one cited source on the turn-complete frame.
//
// Title and Snippet are optional and, as of 2026-09-02, the agent sets
// NEITHER: citationFrom in the agent's boot/agents.go builds a ref from the
// cite_atom response's atom_id and current_revision_id only. They are carried
// because the wire type on all three sides declares them, and a ref that gains
// a title later must not need a schema change to be used.
type chatGroundingRef struct {
	AtomID     string `json:"atom_id"`
	RevisionID string `json:"revision_id,omitempty"`
	Title      string `json:"title,omitempty"`
	Snippet    string `json:"snippet,omitempty"`
}

// citation renders one ref as the concrete source to record, preferring a
// human-readable title and falling back to the atom id, which is the only
// other thing that names a real source. A ref naming neither returns "" and is
// dropped by the caller: an empty entry would render as a blank source the
// learner cannot act on.
func (g chatGroundingRef) citation() string {
	if t := strings.TrimSpace(g.Title); t != "" {
		return t
	}
	return strings.TrimSpace(g.AtomID)
}

// chatErrorPayload — terminal error frame body.
type chatErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
