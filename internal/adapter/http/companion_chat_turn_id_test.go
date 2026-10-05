// companion_chat_turn_id_test.go: the client-minted turn_id is a UUID or the
// request is refused at the edge.
//
// ADR-254 D8 has the SPA mint turn_id so a retried POST replays the stored
// result instead of re-dispatching. That makes it CLIENT INPUT on the one
// handler that accepts it, and migration 0111 declares
// `turn_id UUID PRIMARY KEY`, so the only thing rejecting a malformed id used
// to be the Postgres cast at insert. That is fail-loud (nothing corrupt is
// stored and no kennel lane ever sees a non-UUID) but it blames the wrong
// actor: the learner's bad input surfaces as an internal error.
//
// These pin the edge: a malformed id is a 400 the caller can act on, an absent
// id still gets a minted one exactly as D8 specifies, and a well-formed id is
// passed through untouched so the replay contract still works.
package http

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// chatWithTurnID posts one chat request carrying the given turn_id verbatim.
func chatWithTurnID(t *testing.T, srv *Server, turnID string) (int, map[string]any) {
	t.Helper()
	body := map[string]string{"message": "hi"}
	if turnID != "" {
		body["turn_id"] = turnID
	}
	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", body)
	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return w.Code, parsed
}

func newTurnIDChatServer(t *testing.T) (*Server, *fakeChatEngine) {
	t.Helper()
	engine := &fakeChatEngine{}
	srv := NewServer()
	seedChatEngine(srv, engine, newFakeChatRepo())
	return srv, engine
}

// A malformed client id is the caller's mistake and must be named as such,
// BEFORE any engine work.
func TestCompanionChat_MalformedTurnID_Returns400(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"not a uuid at all", "hello"},
		{"truncated uuid", "01957c8c-1111-7000-aaaa"},
		{"uuid with trailing junk", "01957c8c-1111-7000-aaaa-1111aaaa1111x"},
		{"sql-ish", "'; DROP TABLE companion_turns; --"},
		{"whitespace only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, engine := newTurnIDChatServer(t)

			code, body := chatWithTurnID(t, srv, tc.id)

			if code != http.StatusBadRequest {
				t.Fatalf("status = %d for turn_id %q, want 400 (the caller minted it, so the caller must hear about it)", code, tc.id)
			}
			errMap, _ := body["error"].(map[string]any)
			if errMap == nil {
				t.Fatalf("body.error missing; body=%v", body)
			}
			if errMap["code"] != companion.TurnErrCodeInvalidTurnID {
				t.Errorf("error.code = %v; want %s", errMap["code"], companion.TurnErrCodeInvalidTurnID)
			}
			if engine.calls != 0 {
				t.Errorf("engine.StreamChat called %d times on a malformed turn_id; want 0", engine.calls)
			}
		})
	}
}

// Whitespace-only is NOT the same as absent. Trimming it to "" and then
// minting would silently accept input the caller got wrong.
func TestCompanionChat_WhitespaceTurnID_IsRejectedNotMinted(t *testing.T) {
	srv, _ := newTurnIDChatServer(t)

	code, body := chatWithTurnID(t, srv, "\t \n")

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d for a whitespace turn_id, want 400 (present but unusable is not absent)", code)
	}
	errMap, _ := body["error"].(map[string]any)
	if errMap == nil || errMap["code"] != companion.TurnErrCodeInvalidTurnID {
		t.Errorf("error.code = %v; want %s", body["error"], companion.TurnErrCodeInvalidTurnID)
	}
}

// ADR-254 D8 unchanged: an ABSENT id is still minted by consumption.
func TestCompanionChat_AbsentTurnID_StillMinted(t *testing.T) {
	srv, _ := newTurnIDChatServer(t)

	code, _ := chatWithTurnID(t, srv, "")

	if code == http.StatusBadRequest {
		t.Fatalf("status = 400 with NO turn_id; ADR-254 D8 says consumption mints one when it is absent")
	}
}

// A well-formed id must survive untouched: the replay contract keys on it.
func TestCompanionChat_ValidTurnID_IsAccepted(t *testing.T) {
	srv, _ := newTurnIDChatServer(t)

	code, _ := chatWithTurnID(t, srv, "01957c8c-2222-7000-aaaa-222222222222")

	if code == http.StatusBadRequest {
		t.Fatalf("status = 400 for a well-formed UUIDv7 turn_id; want it accepted")
	}
}
