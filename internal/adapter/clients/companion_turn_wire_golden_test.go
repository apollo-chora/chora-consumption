// companion_turn_wire_golden_test.go: the EXACT bytes chora-consumption puts
// on chora.consumption.companion_turn.requested.v1, captured end to end by
// production code.
//
// # Why a golden file rather than a hand-written fixture
//
// The kennel lane that consumes this topic does not exist yet, and its only
// real producer (this engine) cannot be deployed until the lane does. That
// circularity tempts everyone toward the same bad proof: the lane author
// invents a payload, publishes it, observes their own invention arrive and
// calls the wire proven. It proves nothing about THIS producer.
//
// So the bytes here are not written by anyone. They are produced by the same
// three production calls the live path makes, in the same order:
//
//	BuildCompanionTurnRequestPayload  (the payload map, incl. the prompt
//	                                   overrides consumption resolves)
//	events.NewEnvelope                (the envelope)
//	outbox.BuildRow                   (the serialiser, which routes this topic
//	                                   to encodeJSONWire: ADR-254 D4 declares
//	                                   the two caller-facing REQUEST lanes
//	                                   JSON-wire and schemaless, decoded by the
//	                                   kennel on the proto FIELD NAMES)
//
// The only things pinned by hand are the inputs a caller supplies anyway (ids,
// message, tier) and the clock, because a golden file cannot contain "now".
//
// # What it guards
//
// Two directions. If this producer's wire shape changes, this test fails and
// the golden must be regenerated deliberately, which is the moment to tell
// whoever consumes it. And the file itself is the artifact a lane author
// decodes against, so their decoder is exercised on real producer output
// rather than on their own understanding of it.
//
// ⚠ WIRE KEYS ARE DELIBERATELY PRE-RENAME. familiar_id, familiar_memory and
// familiar_config are decoded BY NAME by the kennel; ADR-254 D6 cuts them in a
// coordinated change, never silently. A golden file that "helpfully" showed
// companion_* here would be a lie about what is on the bus today.
package clients

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

const (
	companionTurnGolden    = "testdata/companion_turn_requested.wire.json"
	companionTurnEnvGolden = "testdata/companion_turn_requested.envelope.json"
)

// goldenTurnRequest is one realistic turn: every optional field populated, so
// the golden shows the WIDEST shape a consumer must handle rather than the
// narrowest. A decoder written against a minimal sample breaks on the first
// real turn that carries memory or a prompt override.
func goldenTurnRequest() CompanionChatRequest {
	return CompanionChatRequest{
		TenantID:            "11111111-1111-7111-8111-111111111111",
		UserGCID:            "00000000-0000-7000-8000-000000001999",
		CompanionID:         "01957c8c-1111-7000-aaaa-1111aaaa1111",
		ConversationID:      "01957c8c-4444-7000-aaaa-444444444444",
		ManaTier:            "basic",
		ManaActionCode:      "companion_chat_turn_basic",
		Message:             "why do fractions flip when you divide?",
		Locale:              "en-SG",
		CompanionMemoryJSON: `{"recent":["asked about fractions last week"]}`,
		LearnerWeaknessJSON: `{"edges":[{"concept":"dividing fractions","strength":0.8}]}`,
	}
}

func TestCompanionTurnRequested_WireGolden(t *testing.T) {
	// A fixed instant and turn id: everything else comes from production code.
	now := time.Date(2026, 8, 23, 6, 30, 0, 0, time.UTC)
	const turnID = "01957c8c-2222-7000-aaaa-222222222222"

	payload, err := BuildCompanionTurnRequestPayload(goldenTurnRequest(), turnID, now,
		PromptOverridesResolution{
			OverridesJSON: `{"system":"you are Ember, a patient maths companion"}`,
			Version:       "v3",
		})
	if err != nil {
		t.Fatalf("BuildCompanionTurnRequestPayload: %v", err)
	}

	env := events.NewEnvelope(
		goldenTurnRequest().TenantID, goldenTurnRequest().UserGCID,
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "", turnID)

	row, err := outbox.BuildRow("companion_turn", events.TopicCompanionTurnRequested, env, payload)
	if err != nil {
		t.Fatalf("outbox.BuildRow: %v", err)
	}

	// Re-encode through a canonical marshal so the comparison is against the
	// SHAPE, not against Go's map iteration order.
	var decoded map[string]any
	if err := json.Unmarshal(row.Payload, &decoded); err != nil {
		t.Fatalf("the produced body is not JSON, so the lane is not JSON-wire: %v", err)
	}
	got, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	got = append(got, '\n')

	// The ENVELOPE half. It does NOT ride in the body: the outbox stores it
	// beside the payload and the dispatcher turns it into Pub/Sub message
	// ATTRIBUTES, plus a "topic" attribute the push consumers strict-match on.
	// A consumer that expects envelope fields inside the JSON body finds none.
	//
	// ⚠ tracestate, correlation_id and causation_id are OMITTED when empty
	// rather than sent blank, so a decoder that requires them breaks on the
	// first turn without one. This capture has no tracestate for exactly that
	// reason: the common case is the one worth showing.
	envKeys := make([]string, 0, len(row.Envelope))
	for k := range row.Envelope {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	envShape := make(map[string]string, len(row.Envelope))
	for _, k := range envKeys {
		envShape[k] = row.Envelope[k]
	}
	gotEnv, err := json.MarshalIndent(envShape, "", "  ")
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	gotEnv = append(gotEnv, '\n')

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(companionTurnGolden), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(companionTurnGolden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		if err := os.WriteFile(companionTurnEnvGolden, gotEnv, 0o644); err != nil {
			t.Fatalf("write envelope golden: %v", err)
		}
		t.Logf("goldens REGENERATED at %s and %s; commit them and tell whoever decodes this lane",
			companionTurnGolden, companionTurnEnvGolden)
		return
	}

	want, err := os.ReadFile(companionTurnGolden)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with UPDATE_GOLDEN=1)", companionTurnGolden, err)
	}
	wantEnv, err := os.ReadFile(companionTurnEnvGolden)
	if err != nil {
		t.Fatalf("read envelope golden %s: %v (regenerate with UPDATE_GOLDEN=1)", companionTurnEnvGolden, err)
	}
	// event_id, occurred_at and published_at are minted per publish, so the
	// envelope golden pins the KEY SET and the stable values, not the volatile
	// ones. A missing key is the failure that matters: a consumer strict-
	// matching an attribute that stopped being sent drops every event silently.
	var wantEnvMap map[string]string
	if err := json.Unmarshal(wantEnv, &wantEnvMap); err != nil {
		t.Fatalf("golden envelope is not JSON: %v", err)
	}
	for k := range wantEnvMap {
		if _, ok := envShape[k]; !ok {
			t.Errorf("envelope attribute %q is no longer emitted; a consumer strict-matching it would drop every event", k)
		}
	}
	for k := range envShape {
		if _, ok := wantEnvMap[k]; !ok {
			t.Errorf("envelope attribute %q is NEW; regenerate the golden and tell whoever decodes this lane", k)
		}
	}

	if string(got) != string(want) {
		t.Errorf("the companion_turn.requested wire shape CHANGED.\n--- got ---\n%s\n--- golden ---\n%s\n"+
			"If this is intended, regenerate with UPDATE_GOLDEN=1 and tell whoever decodes this lane, because "+
			"they decode it by FIELD NAME and nothing on the bus will reject a shape they cannot read "+
			"(the topic is schemaless by ADR-254 D4).", got, want)
	}
}

// The keys the kennel decodes by name must be present and pre-rename. Pinned
// separately from the golden so the REASON survives even if someone
// regenerates the file without reading why it looked like that.
func TestCompanionTurnRequested_WireKeysAreDeliberatelyPreRename(t *testing.T) {
	payload, err := BuildCompanionTurnRequestPayload(goldenTurnRequest(),
		"01957c8c-2222-7000-aaaa-222222222222", time.Now().UTC(), PromptOverridesResolution{})
	if err != nil {
		t.Fatalf("BuildCompanionTurnRequestPayload: %v", err)
	}
	for _, key := range []string{"familiar_id", "familiar_memory", "turn_id", "turn_kind", "tenant_id", "gcid"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("wire key %q is MISSING; the kennel decodes this lane by field name (ADR-254 D6 cuts the familiar_* keys in a coordinated change, never silently)", key)
		}
	}
	for _, key := range []string{"companion_id", "companion_memory"} {
		if _, ok := payload[key]; ok {
			t.Errorf("wire key %q appeared EARLY; renaming it here without the coordinated D6 cut would silently break the kennel decode", key)
		}
	}
}
