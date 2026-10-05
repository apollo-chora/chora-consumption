package companion

// turn_prompt_provenance_test.go: the completed turn carries the prompt
// provenance the agent stamped (UX Track U, D2 / N9 step 2).
//
// The agent has stamped prompt_version and prompt_source on its completion
// envelope all along. The kennel lane dropped both when it built
// companion_turn.completed.v1, and this parser had no field for them either,
// so chatTurnCompletePayload.PromptVersion has been reading a key nothing
// sends since CHO-2368 and every ritual step stamped an empty version.
//
// prompt_source is worth as much as the version: "registry" means a resolved
// override shaped the prompt, "embedded_fallback" means the built-in default
// did because no override resolved. A version without its source cannot tell
// an operator whether their override was actually in effect.

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestParseTurnResultJSON_CarriesPromptProvenance(t *testing.T) {
	body := []byte(`{
		"turn_id": "t-1",
		"status": "OK",
		"reply_text": "Reciprocals.",
		"generated_by_model_id": "gemini-2.5-flash",
		"prompt_version": "v1",
		"prompt_source": "registry",
		"grounding": [],
		"tool_calls": []
	}`)
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Result.PromptVersion != "v1" {
		t.Errorf("PromptVersion = %q; want v1", parsed.Result.PromptVersion)
	}
	if parsed.Result.PromptSource != "registry" {
		t.Errorf("PromptSource = %q; want registry", parsed.Result.PromptSource)
	}
}

// Both-shape tolerance, the older-lane direction. A completed event from a
// kennel image that predates the forwarding carries neither key, and both must
// stay EMPTY rather than be invented: a fabricated version is a false
// provenance claim and is indistinguishable from a real one.
func TestParseTurnResultJSON_AbsentPromptProvenanceStaysEmpty(t *testing.T) {
	body := []byte(`{"turn_id":"t-1","status":"OK","reply_text":"hi","generated_by_model_id":"m"}`)
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Result.PromptVersion != "" || parsed.Result.PromptSource != "" {
		t.Errorf("PromptVersion = %q, PromptSource = %q; want both empty (never fabricated)",
			parsed.Result.PromptVersion, parsed.Result.PromptSource)
	}
}

// The parse must not REFUSE an event carrying keys it does not know. The three
// images roll separately in a deploy window, so an older consumption will meet
// a newer lane, and rejecting the turn over an unknown key would break every
// learner's chat for the length of the window.
func TestParseTurnResultJSON_UnknownFieldsDoNotRefuseTheTurn(t *testing.T) {
	body := []byte(`{"turn_id":"t-1","status":"OK","reply_text":"hi",
		"generated_by_model_id":"m","prompt_version":"v2","some_future_field":{"a":1}}`)
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn, body)
	if err != nil {
		t.Fatalf("an unknown field refused the turn: %v", err)
	}
	if parsed.Result.PromptVersion != "v2" {
		t.Errorf("PromptVersion = %q; want v2", parsed.Result.PromptVersion)
	}
}

// The stored result is the WIRE body, so the provenance has to survive being
// re-parsed from snake_case rather than from Go field names.
//
// Worth pinning because the tempting version of this test marshals a
// TurnResult and re-parses it, which round-trips through Go FIELD NAMES and
// would pass against a parser that reads no snake_case key at all. The sibling
// column `steps` on companion_ritual_runs is stored in exactly that Go-name
// shape, so the confusion is live in this codebase.
func TestParseTurnResultJSON_ReadsProvenanceFromTheSnakeCaseWire(t *testing.T) {
	fromGoNames, err := json.Marshal(TurnResult{PromptVersion: "v1", PromptSource: "registry"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(fromGoNames, []byte("prompt_version")) {
		t.Fatal("TurnResult marshals to snake_case; this test's premise is stale")
	}
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn,
		[]byte(`{"turn_id":"t-1","status":"OK","reply_text":"hi","prompt_version":"v1","prompt_source":"registry"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Result.PromptVersion != "v1" || parsed.Result.PromptSource != "registry" {
		t.Errorf("wire parse lost provenance: version %q source %q",
			parsed.Result.PromptVersion, parsed.Result.PromptSource)
	}
}

// N9 step 3. The hash is the agent's, computed over the prompt it actually
// composed and ran, and consumption only carries it. There is deliberately no
// code here that could produce one.
func TestParseTurnResultJSON_CarriesThePromptHash(t *testing.T) {
	body := []byte(`{"turn_id":"t-1","status":"OK","reply_text":"hi",
		"generated_by_model_id":"m","prompt_hash":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}`)
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	if parsed.Result.PromptHash != want {
		t.Errorf("PromptHash = %q; want %q", parsed.Result.PromptHash, want)
	}
}

func TestParseTurnResultJSON_AbsentPromptHashStaysEmpty(t *testing.T) {
	body := []byte(`{"turn_id":"t-1","status":"OK","reply_text":"hi","generated_by_model_id":"m"}`)
	parsed, err := ParseTurnResultJSON(TurnLaneCompanionTurn, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Result.PromptHash != "" {
		t.Errorf("PromptHash = %q; want empty: consumption must never produce one",
			parsed.Result.PromptHash)
	}
}
