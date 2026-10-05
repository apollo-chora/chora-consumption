// turn_id_test.go: ValidateTurnID, the guard between a client-minted turn id
// and the `turn_id UUID PRIMARY KEY` column migration 0111 declares.
//
// It lives in the domain package because the rule is the domain's, not the
// handler's: any future caller that accepts a client-minted id owes the same
// check, and a rule tested only through one HTTP route is a rule the next
// caller will not know it has.
package companion

import (
	"strings"
	"testing"
)

// The validator itself, exercised directly: it is the thing the handler and
// any future caller share, so it is pinned independently of the HTTP shape.
func TestValidateTurnID(t *testing.T) {
	good := []string{
		"01957c8c-2222-7000-aaaa-222222222222",
		"01957C8C-2222-7000-AAAA-222222222222",     // case-insensitive
		"  01957c8c-2222-7000-aaaa-222222222222  ", // surrounding space is the client's, not an error
	}
	for _, id := range good {
		if got, err := ValidateTurnID(id); err != nil {
			t.Errorf("ValidateTurnID(%q) errored: %v", id, err)
		} else if strings.TrimSpace(got) == "" {
			t.Errorf("ValidateTurnID(%q) returned empty", id)
		}
	}
	bad := []string{"hello", "01957c8c-2222-7000-aaaa", "", "   ", "01957c8c22227000aaaa222222222222x"}
	for _, id := range bad {
		if _, err := ValidateTurnID(id); err == nil {
			t.Errorf("ValidateTurnID(%q) accepted a value the uuid column would reject", id)
		}
	}
}

// The normalised id is what the store and the wire must both see, so the
// validator returns it rather than leaving the caller to trim again.
func TestValidateTurnID_ReturnsTheTrimmedID(t *testing.T) {
	const want = "01957c8c-2222-7000-aaaa-222222222222"
	got, err := ValidateTurnID("  " + want + " ")
	if err != nil {
		t.Fatalf("ValidateTurnID: %v", err)
	}
	if got != want {
		t.Errorf("ValidateTurnID returned %q; want the trimmed %q", got, want)
	}
}
