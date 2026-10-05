// cognitive_level_decode_test.go — CHO-2082 (WS-C3): the binary
// atom.published.v1 decode must project cognitive_level (f22, enum) onto the
// flat map as the ORIGINAL-Bloom lowercase label, so the cross-domain push
// handler can thread it into the atom_index projection. UNSPECIFIED/absent
// projects nothing (the map key is simply missing — unknown level).
package protodecode

import (
	"testing"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

func TestDecodeAtomPublished_ProjectsCognitiveLevel(t *testing.T) {
	msg := &creationv1.AtomPublished{
		AtomId:         "01980000-0000-7000-a000-00000000c0de",
		CognitiveLevel: creationv1.CognitiveLevel_COGNITIVE_LEVEL_APPLICATION,
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := DecodePayloadMapWithAttrs("chora.creation.atom.published.v1", raw, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out["cognitive_level"]; got != "application" {
		t.Fatalf("cognitive_level = %v, want application", got)
	}
}

func TestDecodeAtomPublished_UnspecifiedLevelOmitsKey(t *testing.T) {
	msg := &creationv1.AtomPublished{AtomId: "01980000-0000-7000-a000-00000000c0df"}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := DecodePayloadMapWithAttrs("chora.creation.atom.published.v1", raw, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := out["cognitive_level"]; present {
		t.Fatalf("unspecified level must omit the key, got %v", out["cognitive_level"])
	}
}
