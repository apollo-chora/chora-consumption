// grown_test.go — ADR-196 reward spine: the GrownEdge transition projection +
// the recovery_source contract values.
package learner_weakness

import "testing"

// The recovery_source string values are a WIRE CONTRACT — they populate the
// chora.consumption.weakness.grown.v1 recovery_source field and B3 reward
// consumers switch on them. Pin them so a rename can't silently break the
// contract (and the proto doc) without a failing test.
func TestRecoverySourceConsts_PinnedToContract(t *testing.T) {
	if RecoverySourceConceptKey != "concept_key" {
		t.Fatalf("RecoverySourceConceptKey = %q, want %q (proto recovery_source)", RecoverySourceConceptKey, "concept_key")
	}
	if RecoverySourceDrillAtom != "drill_atom" {
		t.Fatalf("RecoverySourceDrillAtom = %q, want %q (proto recovery_source)", RecoverySourceDrillAtom, "drill_atom")
	}
}
