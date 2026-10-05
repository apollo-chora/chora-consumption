package clients

// ritual_step_citations_test.go: the ritual step stamp carries the REAL
// citations the turn grounded on (UX Track U, D2 / N9 slice a).
//
// Where they come from, traced rather than assumed. The production path is the
// bus (ADR-254 D8): BusTurnEngine.emitTerminal synthesises the turn_complete
// frame inside consumption from the stored turn row, and it ALREADY puts
// `grounding` on that frame. The agent has emitted it all along, the Python
// kennel lane forwards it, and companion.ParseTurnResultJSON parses it. The
// only break in the chain was here: chatTurnCompletePayload did not carry the
// field, so the ritual step dropped verified citations on the floor.
//
// These are validator-CONFIRMED sources, not tool names. Per ADR-249 the agent
// emits a grounding ref only when cite_atom confirmed the atom exists for the
// tenant; a rejected id is dropped from grounding and stays visible in
// tool_calls. So a citation stamped here is a claim the platform can stand
// behind, which is exactly why it must never be fabricated when absent.

import (
	"encoding/json"
	"testing"
)

// rawFrame builds a frame from literal wire JSON rather than from a Go struct,
// so these tests pin the shape emitTerminal actually emits and would fail if
// the payload struct and the wire drifted apart.
func rawFrame(typ, body string) ChatStreamFrame {
	return ChatStreamFrame{Type: typ, Data: json.RawMessage(body)}
}

func TestCollectStepTurnResponse_CarriesGroundingAsCitations(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- rawFrame(ChatFrameTurnComplete, `{
		"turn_id": "t-1",
		"model": "flash",
		"reply_text": "Fractions are parts of a whole.",
		"grounding": [
			{"atom_id": "01920000-0000-7000-8000-000000000001", "revision_id": "r-1", "title": "Fractions basics"},
			{"atom_id": "01920000-0000-7000-8000-000000000002", "revision_id": "r-2"}
		],
		"tool_calls": []
	}`)
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(out.Citations) != 2 {
		t.Fatalf("Citations = %v; want 2 entries", out.Citations)
	}
	// A ref carrying a title cites the title; a ref carrying only ids cites the
	// atom id, because that is the only concrete source we hold. The learner
	// projection is what decides whether a bare id is fit to SHOW; the stamp is
	// the O+ record and keeps the id either way.
	if out.Citations[0] != "Fractions basics" {
		t.Errorf("Citations[0] = %q; want the title when the ref carries one", out.Citations[0])
	}
	if out.Citations[1] != "01920000-0000-7000-8000-000000000002" {
		t.Errorf("Citations[1] = %q; want the atom id when the ref carries no title", out.Citations[1])
	}
}

// A frame with no grounding key at all stamps NOTHING. Absent means empty,
// never fabricated: a citation invented for an ungrounded answer is worse than
// no citation, because it is the platform vouching for a source that was never
// consulted.
func TestCollectStepTurnResponse_AbsentGroundingStampsNoCitations(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- rawFrame(ChatFrameTurnComplete, `{"turn_id":"t-1","model":"flash","reply_text":"hi"}`)
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(out.Citations) != 0 {
		t.Errorf("Citations = %v; want none (absent is never fabricated)", out.Citations)
	}
}

// An EMPTY grounding list is the ordinary case for an ungrounded turn and must
// behave exactly like an absent one. emitTerminal normalises nil to [], so this
// is the shape most real frames carry.
func TestCollectStepTurnResponse_EmptyGroundingStampsNoCitations(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- rawFrame(ChatFrameTurnComplete, `{"turn_id":"t-1","model":"flash","grounding":[],"tool_calls":[]}`)
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(out.Citations) != 0 {
		t.Errorf("Citations = %v; want none", out.Citations)
	}
}

// A grounding ref with neither a title nor an atom id contributes nothing. It
// cannot: there is no source to name, and an empty string in the list would
// render as a blank bullet the learner cannot act on.
func TestCollectStepTurnResponse_UnusableGroundingRefIsDropped(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- rawFrame(ChatFrameTurnComplete, `{"model":"flash","grounding":[{"revision_id":"r-9"},{"atom_id":"  "}]}`)
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(out.Citations) != 0 {
		t.Errorf("Citations = %v; want none from refs naming no source", out.Citations)
	}
}
