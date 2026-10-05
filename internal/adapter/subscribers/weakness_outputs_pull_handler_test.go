package subscribers

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
)

// weakness_outputs_pull_handler_test.go - ADR-254 D4 (W4 consumption cut): the
// kennel's diagnosis crew publishes chora.consumption.weakness.outputs_generated.v1
// as BINARY proto (flat twin of WeaknessOutputsGenerated) and the coordinator
// provisioned the PULL subscription chora-consumption.consumption-weakness-
// outputs-generated. This adapter binds the existing projector (Handle) to the
// eventbus: decode the binary body, lift the envelope from the message
// headers (the message envelope is authoritative; the body's nested
// envelope is a mirror), project the artifacts.

func outputsMsg(t *testing.T, env envelope.Envelope, body *consumptionv1.WeaknessOutputsGenerated) eventbus.Message {
	t.Helper()
	bz, err := proto.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return eventbus.Message{Subject: "chora.consumption.weakness.outputs_generated.v1", Envelope: env, Payload: bz}
}

func libsEnv() envelope.Envelope {
	now := time.Now().UTC()
	return envelope.Envelope{
		EventID: "01970000-0000-7000-8000-0000000000e1", IdempotencyKey: "weakness.outputs_generated:u-1",
		TenantID: "01970000-0000-7000-8000-0000000000ab", GCID: "01970000-0000-7000-9000-0000000000ab",
		OccurredAt: now, PublishedAt: now,
		Traceparent:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject: "chora-489812", SourceService: "chora-ai-kernel-orchestrator", SchemaVersion: 1,
	}
}

func TestWeaknessOutputsPullHandler_DecodesBinaryAndProjects(t *testing.T) {
	repo := &geoFakeRepo{newRows: 2}
	h := WeaknessOutputsPullHandler(NewWeaknessOutputsGeneratedSubscriber(repo))
	body := &consumptionv1.WeaknessOutputsGenerated{
		Envelope:    &commonv1.EventEnvelope{EventId: "01970000-0000-7000-8000-0000000000e1"},
		UploadId:    "u-1",
		TenantId:    "01970000-0000-7000-8000-0000000000ab",
		LearnerGcid: "01970000-0000-7000-9000-0000000000ab",
		Outputs: []*consumptionv1.GeneratedLearnerOutput{
			{Kind: "study_aids", ContentJson: `{"advice":"x"}`, Metered: true},
			{Kind: "companion_voice", ContentJson: `{"text":"You have got this."}`, Metered: false},
		},
		GeneratedAt: timestamppb.New(time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC)),
	}
	if err := h(context.Background(), outputsMsg(t, libsEnv(), body)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(repo.batches) != 1 || len(repo.batches[0]) != 2 {
		t.Fatalf("want one batch of 2 artifacts, got %+v", repo.batches)
	}
	if repo.batches[0][1].Kind != "companion_voice" {
		t.Fatalf("second artifact kind = %q, want companion_voice (ADR-254 D4 new output kind)", repo.batches[0][1].Kind)
	}
	if repo.batches[0][0].UploadID != "u-1" {
		t.Fatalf("upload id not carried: %+v", repo.batches[0][0])
	}
}

func TestWeaknessOutputsPullHandler_MalformedBodyNacks(t *testing.T) {
	h := WeaknessOutputsPullHandler(NewWeaknessOutputsGeneratedSubscriber(&geoFakeRepo{}))
	msg := eventbus.Message{Subject: "chora.consumption.weakness.outputs_generated.v1", Envelope: libsEnv(), Payload: []byte("not a proto")}
	if err := h(context.Background(), msg); err == nil {
		t.Fatal("a body that is neither binary proto nor JSON must NACK (dead-letter), never ack")
	}
}

func TestWeaknessOutputsPullHandler_NilGuards(t *testing.T) {
	h := WeaknessOutputsPullHandler(nil)
	if err := h(context.Background(), eventbus.Message{}); err == nil {
		t.Fatal("nil subscriber must fail loud")
	}
}
