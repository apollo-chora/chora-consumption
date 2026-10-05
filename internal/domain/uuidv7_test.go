package domain

import (
	"regexp"
	"testing"
)

var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewUUIDv7_FormatAndVersion verifies the generator produces
// canonical hyphenated 36-char UUIDs with version=7 and variant=10xx.
func TestNewUUIDv7_FormatAndVersion(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewUUIDv7()
		if len(id) != 36 {
			t.Fatalf("len(id) = %d, want 36; got %q", len(id), id)
		}
		if !uuidRegex.MatchString(id) {
			t.Errorf("id %q does not match v7 regex", id)
		}
	}
}

// TestNewUUIDv7_Unique sanity-checks no collisions in a small batch.
func TestNewUUIDv7_Unique(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id := NewUUIDv7()
		if _, dup := seen[id]; dup {
			t.Fatalf("collision on iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}
