// companion_mana_tier_config_test.go — Task 2(b) TDD spec.
//
// The per-tenant fallback mana tier was a hardcoded "basic" literal in
// resolveManaTier (an inline-config violation per feedback_no_inline_config).
// It is now config-driven: sourced from COMPANION_DEFAULT_MANA_TIER at boot
// (NewServer), validated, and exposed as Server.DefaultManaTier. resolveManaTier
// reads the Server field — NEVER the env directly.
package http

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// withEnv sets an env var for the duration of the test (auto-restored by
// t.Setenv). NewServer reads COMPANION_DEFAULT_MANA_TIER via the package
// `getenv` → `osGetenv` (= os.Getenv) indirection, so setting the real
// process env here drives the boot-time read. Cannot run in parallel with
// other env-mutating tests (t.Setenv enforces this).
func withEnv(t *testing.T, key, val string) {
	t.Helper()
	t.Setenv(key, val)
}

func TestNewServer_DefaultManaTier_UnsetUsesCanonicalDefault(t *testing.T) {
	// No env override → canonical growth.DefaultManaTier ("basic").
	withEnv(t, "COMPANION_DEFAULT_MANA_TIER", "")
	srv := NewServer()
	if srv.DefaultManaTier != growth.DefaultManaTier {
		t.Errorf("DefaultManaTier = %q, want %q (canonical default)", srv.DefaultManaTier, growth.DefaultManaTier)
	}
	if got := resolveManaTier(srv, testTenant, testGCID); got != growth.DefaultManaTier {
		t.Errorf("resolveManaTier = %q, want %q", got, growth.DefaultManaTier)
	}
}

func TestNewServer_DefaultManaTier_EnvOverrideApplied(t *testing.T) {
	withEnv(t, "COMPANION_DEFAULT_MANA_TIER", "standard")
	srv := NewServer()
	if srv.DefaultManaTier != "standard" {
		t.Errorf("DefaultManaTier = %q, want standard", srv.DefaultManaTier)
	}
	if got := resolveManaTier(srv, testTenant, testGCID); got != "standard" {
		t.Errorf("resolveManaTier = %q, want standard", got)
	}
}

func TestResolveManaTier_EmptyServerFieldFallsBackToCanonical(t *testing.T) {
	// Defensive: a Server with no DefaultManaTier seeded (e.g. zero-value
	// struct) still resolves to the canonical default rather than "".
	srv := &Server{}
	if got := resolveManaTier(srv, testTenant, testGCID); got != growth.DefaultManaTier {
		t.Errorf("resolveManaTier(zero-value Server) = %q, want %q", got, growth.DefaultManaTier)
	}
}

func TestGrowth_IsValidManaTier(t *testing.T) {
	for _, ok := range []string{"basic", "standard", "premium"} {
		if !growth.IsValidManaTier(ok) {
			t.Errorf("IsValidManaTier(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "BASIC", "gold", "free", "enterprise"} {
		if growth.IsValidManaTier(bad) {
			t.Errorf("IsValidManaTier(%q) = true, want false", bad)
		}
	}
}
