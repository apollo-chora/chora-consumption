package grpc

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ADR-249 A1a ladder-agreement invariant: every innate ladder name the
// growth curve grants must either resolve (via innateToolNameMap) to a
// registry key the agent actually ships, or be explicitly declared
// unshipped in growth.UnshippedLadderTools. A name that does neither
// vanishes into the agent's narrowing-safe WARN, which is a silent
// capability loss (F1's second half: ebbinghaus_state and
// score_atom_for_learner before this invariant existed).
func TestInnateLadderNamesResolveOrAreDeclaredUnshipped(t *testing.T) {
	// The agent's availableTools registry keys (cmd/companion/main.go).
	// After ADR-249 A1a the agent ships exactly one tool: atom.cite.
	shippedRegistryKeys := map[string]bool{
		"atom.cite": true,
	}
	for stage := 0; stage <= 6; stage++ {
		for _, ladder := range growth.UnlockedToolsForStage(stage) {
			if growth.UnshippedLadderTools[ladder] {
				continue
			}
			mapped, ok := innateToolNameMap[ladder]
			if !ok {
				t.Errorf("stage %d ladder name %q neither maps to a shipped agent tool nor is declared unshipped",
					stage, ladder)
				continue
			}
			if !shippedRegistryKeys[mapped] {
				t.Errorf("stage %d ladder name %q resolves to %q, which the agent does not ship",
					stage, ladder, mapped)
			}
		}
	}
}

// A stale unshipped declaration (naming a tool no stage grants, or a tool
// that actually ships) would misreport coverage; both directions fail here.
func TestUnshippedDeclarationsAreRealLadderNames(t *testing.T) {
	st6 := map[string]bool{}
	for _, name := range growth.UnlockedToolsForStage(6) {
		st6[name] = true
	}
	for name := range growth.UnshippedLadderTools {
		if !st6[name] {
			t.Errorf("UnshippedLadderTools names %q, which no stage grants (stale declaration)", name)
		}
		if _, mapped := innateToolNameMap[name]; mapped {
			t.Errorf("%q is declared unshipped AND mapped to a shipped tool; pick one", name)
		}
	}
}
