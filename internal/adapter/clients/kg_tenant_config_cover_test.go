// kg_tenant_config_cover_test.go — supplemental coverage for the
// per-tenant override KG config reader constructor + SetOverride bounds
// branches the original suite did not exercise.
package clients

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// TestNewPerTenantOverrideKGConfigReader_RejectsBadDefaults — the
// constructor bounds-checks BOTH the default max-clusters and the default
// fog-grace. The original suite only covered the happy path.
func TestNewPerTenantOverrideKGConfigReader_RejectsBadDefaults(t *testing.T) {
	cases := []struct {
		name             string
		defMax, defGrace int
	}{
		{"max too small", 0, 300},
		{"max too large", 11, 300},
		{"grace too small", 3, 30},
		{"grace too large", 3, 4000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPerTenantOverrideKGConfigReader(tc.defMax, tc.defGrace)
			if !errors.Is(err, ErrKGConfigInvalidBound) {
				t.Errorf("err = %v; want ErrKGConfigInvalidBound", err)
			}
		})
	}
}

// TestPerTenantOverride_SetOverride_RejectsBadGrace — SetOverride
// bounds-checks the fog-grace dial as well as the cluster cap. The original
// suite only covered the bad-max case.
func TestPerTenantOverride_SetOverride_RejectsBadGrace(t *testing.T) {
	r, err := NewPerTenantOverrideKGConfigReader(3, 300)
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	cases := []struct {
		name string
		cfg  userknowledgegraph.KnowledgeGraphConfig
	}{
		{"grace too small", userknowledgegraph.KnowledgeGraphConfig{MaxConcurrentClustersPerUser: 3, FogInvalidationGraceSeconds: 30}},
		{"grace too large", userknowledgegraph.KnowledgeGraphConfig{MaxConcurrentClustersPerUser: 3, FogInvalidationGraceSeconds: 4000}},
		{"max too small", userknowledgegraph.KnowledgeGraphConfig{MaxConcurrentClustersPerUser: 0, FogInvalidationGraceSeconds: 300}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.SetOverride("tenant-z", tc.cfg); !errors.Is(err, ErrKGConfigInvalidBound) {
				t.Errorf("err = %v; want ErrKGConfigInvalidBound", err)
			}
		})
	}
}

// TestPerTenantOverride_SetOverride_TrimsTenantID — a tenant id with
// surrounding whitespace is trimmed before being keyed, so a trimmed read
// hits the same override.
func TestPerTenantOverride_SetOverride_TrimsTenantID(t *testing.T) {
	r, _ := NewPerTenantOverrideKGConfigReader(3, 300)
	if err := r.SetOverride("  tenant-trim  ", userknowledgegraph.KnowledgeGraphConfig{
		MaxConcurrentClustersPerUser: 7,
		FogInvalidationGraceSeconds:  600,
	}); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cfg, err := r.ReadKnowledgeGraphConfig(t.Context(), "tenant-trim")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.MaxConcurrentClustersPerUser != 7 {
		t.Errorf("max = %d; want 7 (override applied after trim)", cfg.MaxConcurrentClustersPerUser)
	}
}
