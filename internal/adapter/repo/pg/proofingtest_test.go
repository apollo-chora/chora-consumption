// proofingtest_test.go — CHO-2040: the ProofingTest pg adapter's unique-
// violation mapping (the concurrent-double-submit backstop). Pure unit test —
// no DB (the INSERT/index behaviour is covered by the prepare-smoke +
// migration 0069 at roll time).
package pg

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolation(t *testing.T) {
	idx := uqProofingInflightSignature
	dup := &pgconn.PgError{Code: "23505", ConstraintName: idx}

	if !isUniqueViolation(dup, idx) {
		t.Fatalf("23505 on %s must be a unique violation", idx)
	}
	if !isUniqueViolation(dup, "") {
		t.Fatalf("empty constraint filter must match any 23505")
	}
	// A %w-wrapped 23505 must still be detected (errors.As walks the chain).
	if !isUniqueViolation(fmt.Errorf("pg proofing insert: %w", dup), idx) {
		t.Fatalf("a %%w-wrapped 23505 must still be detected")
	}
	// Different constraint ⇒ not our violation.
	if isUniqueViolation(&pgconn.PgError{Code: "23505", ConstraintName: "other"}, idx) {
		t.Fatalf("23505 on a different constraint must not match the named index")
	}
	// Different SQLSTATE ⇒ not a unique violation.
	if isUniqueViolation(&pgconn.PgError{Code: "23503"}, "") {
		t.Fatalf("23503 (FK violation) must not be a unique violation")
	}
	// Non-pg + nil ⇒ false.
	if isUniqueViolation(errors.New("boom"), idx) {
		t.Fatalf("a plain error must not be a unique violation")
	}
	if isUniqueViolation(nil, idx) {
		t.Fatalf("nil must not be a unique violation")
	}
}
