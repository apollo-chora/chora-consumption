package clients

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

func TestStaticKGConfigReader_DefaultDials(t *testing.T) {
	r, err := NewStaticKGConfigReader(KGDefaultMaxClustersPerUser, KGDefaultFogInvalidationGraceS)
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	cfg, err := r.ReadKnowledgeGraphConfig(context.Background(), "01970000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.MaxConcurrentClustersPerUser != KGDefaultMaxClustersPerUser {
		t.Errorf("max = %d, want %d", cfg.MaxConcurrentClustersPerUser, KGDefaultMaxClustersPerUser)
	}
	if cfg.FogInvalidationGraceSeconds != KGDefaultFogInvalidationGraceS {
		t.Errorf("grace = %d, want %d", cfg.FogInvalidationGraceSeconds, KGDefaultFogInvalidationGraceS)
	}
}

func TestStaticKGConfigReader_RejectsEmptyTenant(t *testing.T) {
	r, _ := NewStaticKGConfigReader(KGDefaultMaxClustersPerUser, KGDefaultFogInvalidationGraceS)
	if _, err := r.ReadKnowledgeGraphConfig(context.Background(), ""); err == nil {
		t.Error("expected error on empty tenant")
	}
}

func TestStaticKGConfigReader_RejectsBadBounds(t *testing.T) {
	cases := []struct {
		max, grace int
	}{
		{0, 300},  // max too small
		{11, 300}, // max too large
		{3, 30},   // grace too small
		{3, 4000}, // grace too large
	}
	for _, c := range cases {
		if _, err := NewStaticKGConfigReader(c.max, c.grace); err == nil {
			t.Errorf("expected error for max=%d grace=%d", c.max, c.grace)
		}
	}
}

func TestPerTenantOverride_FallbackToDefault(t *testing.T) {
	r, err := NewPerTenantOverrideKGConfigReader(3, 300)
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	cfg, err := r.ReadKnowledgeGraphConfig(context.Background(), "tenant-without-override")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.MaxConcurrentClustersPerUser != 3 {
		t.Errorf("got %d, want default 3", cfg.MaxConcurrentClustersPerUser)
	}
}

func TestPerTenantOverride_AppliesOverride(t *testing.T) {
	r, _ := NewPerTenantOverrideKGConfigReader(3, 300)
	if err := r.SetOverride("tenant-with-override", userknowledgegraph.KnowledgeGraphConfig{
		MaxConcurrentClustersPerUser: 5,
		FogInvalidationGraceSeconds:  600,
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	cfg, err := r.ReadKnowledgeGraphConfig(context.Background(), "tenant-with-override")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.MaxConcurrentClustersPerUser != 5 {
		t.Errorf("max = %d, want override 5", cfg.MaxConcurrentClustersPerUser)
	}
	if cfg.FogInvalidationGraceSeconds != 600 {
		t.Errorf("grace = %d, want 600", cfg.FogInvalidationGraceSeconds)
	}
}

func TestPerTenantOverride_RejectsBadBounds(t *testing.T) {
	r, _ := NewPerTenantOverrideKGConfigReader(3, 300)
	if err := r.SetOverride("t", userknowledgegraph.KnowledgeGraphConfig{
		MaxConcurrentClustersPerUser: 99,
		FogInvalidationGraceSeconds:  300,
	}); err == nil {
		t.Error("expected error: max out of bounds")
	}
}

func TestPerTenantOverride_RejectsEmptyTenantOnSet(t *testing.T) {
	r, _ := NewPerTenantOverrideKGConfigReader(3, 300)
	if err := r.SetOverride("", userknowledgegraph.KnowledgeGraphConfig{MaxConcurrentClustersPerUser: 3, FogInvalidationGraceSeconds: 300}); err == nil {
		t.Error("expected error on empty tenant")
	}
}

func TestPerTenantOverride_RejectsEmptyTenantOnRead(t *testing.T) {
	r, _ := NewPerTenantOverrideKGConfigReader(3, 300)
	if _, err := r.ReadKnowledgeGraphConfig(context.Background(), ""); err == nil {
		t.Error("expected error on empty tenant")
	}
}
