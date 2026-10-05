// companion_ritual_run_completed_test — CHO-2136: ritual_run_completed.v1 must
// have a BINARY encoder case in MarshalPayload so the outbox does NOT
// JSON-fall-back (boot WARN "no binary protobuf encoder") — CHO-2125's flagged
// sibling (that story closed the same gap for ritual_published.v1). This topic
// additionally has a LIVE consumer (chora-observability ritual_run_audit,
// ADR-215) — the cutover pairs this encoder with the consumer's proto-first
// dual decode.
//
// Payload keys mirror companion.RitualRunner.publishCompleted EXACTLY
// (ritual_runner.go): run_id / ritual_id / companion_id / owner_gcid /
// revision_no / trigger_source / status / mana_charged / sink_ref / error /
// stamps (wire-ready snake_case maps) — timestamps ride on the envelope.
//
// Field layout — chora-contracts/proto/events-flat/consumption/companion/ritual_run_completed.proto
//
//	1  bytes  Envelope envelope
//	2  string run_id
//	3  string ritual_id
//	4  string companion_id
//	5  string owner_gcid
//	6  varint int32 revision_no
//	7  string trigger_source
//	8  varint RitualRunStatus status (enum)
//	9  varint int32 mana_charged
//	10 string sink_ref
//	11 string error
//	12 bytes  repeated RitualStepStamp stamps
//
// Generated Go bindings EXIST (chora-contracts gen ritual.pb.go), so we
// round-trip through proto.Unmarshal into consumptionv1.RitualRunCompleted —
// the load-bearing assertion that Schema Registry will accept the bytes
// (wire_compat_test.go idiom, same as ritual_published).
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestMarshalCompanionRitualRunCompleted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"run_id":         "run-1",
		"ritual_id":      "rit-morning-review",
		"companion_id":   "fam-ember",
		"owner_gcid":     "gcid-dale",
		"revision_no":    3,
		"trigger_source": "manual",
		"status":         "completed",
		"mana_charged":   35,
		"sink_ref":       "atom://draft/42",
		"error":          "",
		"stamps": []map[string]any{
			{
				"step_index":     0,
				"skill_key":      "explain_anew",
				"prompt_version": "v3",
				"prompt_hash":    "sha256:abc",
				"tools_invoked":  []string{"cite_atom", "search_memory"},
				"citations":      []string{"atom://a1"},
			},
			{
				"step_index":     1,
				"skill_key":      "flashcard_forge",
				"prompt_version": "v1",
				"prompt_hash":    "sha256:def",
			},
		},
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.ritual_run_completed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload(ritual_run_completed): %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty ritual_run_completed bytes")
	}

	var msg consumptionv1.RitualRunCompleted
	if uerr := proto.Unmarshal(bz, &msg); uerr != nil {
		t.Fatalf("generated-binding round-trip failed (Schema Registry would reject): %v", uerr)
	}
	if got := msg.GetEnvelope().GetEventId(); got != env.EventID {
		t.Errorf("envelope.event_id = %q, want %q", got, env.EventID)
	}
	if msg.GetRunId() != "run-1" || msg.GetRitualId() != "rit-morning-review" {
		t.Errorf("run/ritual = %q/%q", msg.GetRunId(), msg.GetRitualId())
	}
	if msg.GetCompanionId() != "fam-ember" || msg.GetOwnerGcid() != "gcid-dale" {
		t.Errorf("companion/owner = %q/%q", msg.GetCompanionId(), msg.GetOwnerGcid())
	}
	if msg.GetRevisionNo() != 3 || msg.GetTriggerSource() != "manual" {
		t.Errorf("revision/trigger = %d/%q", msg.GetRevisionNo(), msg.GetTriggerSource())
	}
	if msg.GetStatus() != consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_COMPLETED {
		t.Errorf("status = %v, want COMPLETED", msg.GetStatus())
	}
	if msg.GetManaCharged() != 35 || msg.GetSinkRef() != "atom://draft/42" || msg.GetError() != "" {
		t.Errorf("mana/sink/error = %d/%q/%q", msg.GetManaCharged(), msg.GetSinkRef(), msg.GetError())
	}
	stamps := msg.GetStamps()
	if len(stamps) != 2 {
		t.Fatalf("stamps = %d, want 2", len(stamps))
	}
	s0 := stamps[0]
	if s0.GetStepIndex() != 0 || s0.GetSkillKey() != "explain_anew" ||
		s0.GetPromptVersion() != "v3" || s0.GetPromptHash() != "sha256:abc" {
		t.Errorf("stamp[0] = %+v", s0)
	}
	if len(s0.GetToolsInvoked()) != 2 || s0.GetToolsInvoked()[0] != "cite_atom" {
		t.Errorf("stamp[0].tools_invoked = %v", s0.GetToolsInvoked())
	}
	if len(s0.GetCitations()) != 1 || s0.GetCitations()[0] != "atom://a1" {
		t.Errorf("stamp[0].citations = %v", s0.GetCitations())
	}
	if stamps[1].GetStepIndex() != 1 || stamps[1].GetSkillKey() != "flashcard_forge" {
		t.Errorf("stamp[1] = %+v", stamps[1])
	}
}

// Every terminal RunStatus string the domain can emit maps to its enum — an
// unknown status is a loud encode error (never a silent UNSPECIFIED).
func TestMarshalCompanionRitualRunCompleted_StatusEnumMapping(t *testing.T) {
	cases := map[string]consumptionv1.RitualRunStatus{
		"running":        consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_RUNNING,
		"completed":      consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_COMPLETED,
		"failed":         consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_FAILED,
		"skipped_budget": consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_SKIPPED_BUDGET,
		"blocked":        consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_BLOCKED,
	}
	for status, want := range cases {
		bz, err := protomarshal.MarshalPayload(
			"chora.consumption.companion.ritual_run_completed.v1", fixedEnvelope(),
			map[string]any{"run_id": "run-1", "status": status})
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		var msg consumptionv1.RitualRunCompleted
		if uerr := proto.Unmarshal(bz, &msg); uerr != nil {
			t.Fatalf("status %q round-trip: %v", status, uerr)
		}
		if msg.GetStatus() != want {
			t.Errorf("status %q = %v, want %v", status, msg.GetStatus(), want)
		}
	}
	if _, err := protomarshal.MarshalPayload(
		"chora.consumption.companion.ritual_run_completed.v1", fixedEnvelope(),
		map[string]any{"run_id": "run-1", "status": "exploded"}); err == nil {
		t.Error("unknown status must fail loud, not encode as UNSPECIFIED")
	}
}
