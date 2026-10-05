// collection_converted_decode_test.go — RED-phase test for the BINARY decoder
// of chora.creation.collection.converted_to_study_list.v1 (ADR-233 / WS-4).
//
// The topic is SCHEMA-BOUND (PROTOCOL_BUFFER) in the Pub/Sub Schema Registry, so
// chora-creation puts protobuf BYTES on the wire. An unregistered topic falls
// through to the JSON path — json.Unmarshal on protobuf bytes fails on EVERY
// real event, so the study list would never be built and every push would DLQ.
package protodecode

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

func TestDecode_CollectionConvertedToStudyList_Binary(t *testing.T) {
	const (
		topic    = "chora.creation.collection.converted_to_study_list.v1"
		tenant   = "01970000-0000-7000-8000-000000000001"
		learner  = "01970000-0000-7000-9000-000000000001"
		collID   = "01970000-0000-7000-b000-000000000001"
		evID     = "01970000-0000-7000-c000-000000000001"
		atom1    = "01970000-0000-7000-a000-000000000001"
		atom2    = "01970000-0000-7000-a000-000000000002"
		tracepar = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	)

	msg := &creationv1.CollectionConvertedToStudyList{
		Envelope: &commonv1.EventEnvelope{
			EventId:     evID,
			TenantId:    tenant,
			Gcid:        learner,
			Traceparent: tracepar,
			OccurredAt:  timestamppb.Now(),
		},
		CollectionId:     collID,
		OwnerGcid:        learner,
		AtomIds:          []string{atom1, atom2},
		StudyListEventId: evID,
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := DecodePayloadMap(topic, payload)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v — the topic is schema-bound (BINARY); an "+
			"unregistered topic falls back to JSON and fails on every real event", err)
	}

	if got["collection_id"] != collID {
		t.Errorf("collection_id = %v; want %q", got["collection_id"], collID)
	}
	if got["owner_gcid"] != learner {
		t.Errorf("owner_gcid = %v; want %q", got["owner_gcid"], learner)
	}
	if got["study_list_event_id"] != evID {
		t.Errorf("study_list_event_id = %v; want %q (the durable dedupe anchor)", got["study_list_event_id"], evID)
	}
	if got["tenant_id"] != tenant {
		t.Errorf("tenant_id = %v; want %q (projected from the nested envelope)", got["tenant_id"], tenant)
	}
	atoms, ok := got["atom_ids"].([]string)
	if !ok {
		t.Fatalf("atom_ids = %#v (%T); want []string", got["atom_ids"], got["atom_ids"])
	}
	if len(atoms) != 2 || atoms[0] != atom1 || atoms[1] != atom2 {
		t.Errorf("atom_ids = %v; want [%s %s] in collection order", atoms, atom1, atom2)
	}
}
