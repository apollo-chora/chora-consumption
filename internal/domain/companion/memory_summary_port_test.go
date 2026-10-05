// memory_summary_port_test.go — documents the nil-safe contract of the F5
// MemorySummaryResolver port. This batch ships ONLY the port (no Vertex AI
// Memory Bank adapter — F4 is designed separately), so the test asserts the
// interface shape + that a trivial fake satisfies it.
package companion

import (
	"context"
	"testing"
)

// staticMemoryResolver is a trivial in-test implementation proving the port
// is satisfiable and exercising the ("", nil) "no memory yet" contract.
type staticMemoryResolver struct {
	summary string
	err     error
}

func (s staticMemoryResolver) ResolveMemorySummary(_ context.Context, _, _, _ string) (string, error) {
	return s.summary, s.err
}

// Compile-time assertion that staticMemoryResolver satisfies the port.
var _ MemorySummaryResolver = staticMemoryResolver{}

func TestMemorySummaryResolver_SatisfiesPort(t *testing.T) {
	var r MemorySummaryResolver = staticMemoryResolver{summary: "loves calculus"}
	got, err := r.ResolveMemorySummary(context.Background(), "t", "f", "g")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "loves calculus" {
		t.Errorf("summary = %q, want loves calculus", got)
	}
}

func TestMemorySummaryResolver_EmptyIsNoMemory(t *testing.T) {
	var r MemorySummaryResolver = staticMemoryResolver{summary: ""}
	got, err := r.ResolveMemorySummary(context.Background(), "t", "f", "g")
	if err != nil || got != "" {
		t.Fatalf("want ('', nil) for no-memory contract, got (%q, %v)", got, err)
	}
}
