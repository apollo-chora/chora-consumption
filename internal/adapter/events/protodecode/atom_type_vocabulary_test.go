// CHO-2178 — the atom-type decoder totality guard, twin of the one in
// chora-sharing. This decoder was the LESS broken of the two (it did handle
// ATOM_TYPE_ESSAY, which sharing's did not — so much for "keep in lockstep"),
// but it was still a hand-written reverse table, and it went blind the moment
// the contract gained ATOM_TYPE_OUTLINE / FLASHCARD / VIDEO.
//
// The rule these tests encode: a decoder must be TOTAL over the contract it
// compiles against. Anything less means a producer can emit a value this
// service silently drops on the floor — which is precisely how ten essay atoms
// came to sit in chora_sharing with a blank question_type while every unit test
// stayed green.
package protodecode

import (
	"strings"
	"testing"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

// TestAtomTypeToDomain_TotalOverContract — every declared value must decode to
// a non-empty label. UNSPECIFIED is the sole legitimate "no opinion".
func TestAtomTypeToDomain_TotalOverContract(t *testing.T) {
	for value, name := range creationv1.AtomType_name {
		v := creationv1.AtomType(value)
		if v == creationv1.AtomType_ATOM_TYPE_UNSPECIFIED {
			continue
		}
		if got := atomTypeToDomain(v); got == "" {
			t.Errorf("%s (%d) decodes to \"\" — chora-creation can emit this value, so the "+
				"flavour is silently lost. The decoder must be TOTAL over the contract.", name, value)
		}
	}
}

// TestAtomTypeToDomain_DerivesLabelFromDescriptor — labels derive from the enum
// constant name, with MULTIPLE_CHOICE -> "mcq" the one documented exception.
// Derivation is what keeps this decoder correct for contract values that did
// not exist when it was written.
func TestAtomTypeToDomain_DerivesLabelFromDescriptor(t *testing.T) {
	for value, name := range creationv1.AtomType_name {
		v := creationv1.AtomType(value)
		if v == creationv1.AtomType_ATOM_TYPE_UNSPECIFIED {
			continue
		}
		want := strings.ToLower(strings.TrimPrefix(name, "ATOM_TYPE_"))
		if v == creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE {
			want = "mcq"
		}
		if got := atomTypeToDomain(v); got != want {
			t.Errorf("%s (%d) decodes to %q, want %q", name, value, got, want)
		}
	}
}

// TestAtomTypeToDomain_DomainFlavours — the five flavours chora-creation
// actually authors, spelled out so a rename of a contract constant cannot
// quietly satisfy the derivation test above.
func TestAtomTypeToDomain_DomainFlavours(t *testing.T) {
	for _, tc := range []struct {
		wire creationv1.AtomType
		want string
	}{
		{creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE, "mcq"},
		{creationv1.AtomType_ATOM_TYPE_ESSAY, "essay"},
		{creationv1.AtomType_ATOM_TYPE_OUTLINE, "outline"},
		{creationv1.AtomType_ATOM_TYPE_FLASHCARD, "flashcard"},
		{creationv1.AtomType_ATOM_TYPE_VIDEO, "video"},
	} {
		if got := atomTypeToDomain(tc.wire); got != tc.want {
			t.Errorf("%s decodes to %q, want %q", tc.wire, got, tc.want)
		}
	}
}

// TestAtomTypeToDomain_UnspecifiedStaysAbsent — UNSPECIFIED must yield "" so
// the caller omits the key entirely rather than writing a blank over a flavour
// an earlier event already established.
func TestAtomTypeToDomain_UnspecifiedStaysAbsent(t *testing.T) {
	if got := atomTypeToDomain(creationv1.AtomType_ATOM_TYPE_UNSPECIFIED); got != "" {
		t.Errorf("ATOM_TYPE_UNSPECIFIED decoded to %q — it must leave question_type absent", got)
	}
}
