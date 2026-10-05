// memory_view_test.go — pure-domain unit tests for the ADR-215 WS-1 tier-(a)
// learner-facing Companion "mind" view assembly + UUID→title resolution helpers.
//
// These exercise the LEARNER-APPROPRIATE-DEPTH invariants (ADR-215 D5): no raw
// vectors / distances / model internals leak into the view; an empty recall set
// yields an honest HasMemory=false ("no memory yet", never fabricated); and an
// unresolved atom title stays blank (unknown) rather than being invented.
package companionmind

import (
	"testing"
	"time"
)

func TestClampMemoryLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, DefaultMemoryLimit},
		{-5, DefaultMemoryLimit},
		{1, 1},
		{DefaultMemoryLimit, DefaultMemoryLimit},
		{MaxMemoryLimit, MaxMemoryLimit},
		{MaxMemoryLimit + 1, MaxMemoryLimit},
		{10_000, MaxMemoryLimit},
	}
	for _, c := range cases {
		if got := ClampMemoryLimit(c.in); got != c.want {
			t.Errorf("ClampMemoryLimit(%d) = %d; want %d", c.in, got, c.want)
		}
	}
}

func TestTopicNodePath(t *testing.T) {
	if got := TopicNodePath([]string{"cs", "recursion", "base-case"}); got != "cs / recursion / base-case" {
		t.Errorf("TopicNodePath = %q; want joined path", got)
	}
	// Blank + whitespace-only tags are dropped (never fabricate structure).
	if got := TopicNodePath([]string{" cs ", "", "  ", "loops"}); got != "cs / loops" {
		t.Errorf("TopicNodePath trimmed = %q; want 'cs / loops'", got)
	}
	if got := TopicNodePath(nil); got != "" {
		t.Errorf("TopicNodePath(nil) = %q; want empty", got)
	}
	if got := TopicNodePath([]string{}); got != "" {
		t.Errorf("TopicNodePath(empty) = %q; want empty", got)
	}
}

func TestNewCitation_ResolvedTitle(t *testing.T) {
	c := NewCitation("atom-1", "What is a base case?", []string{"cs", "recursion"})
	if c.AtomID != "atom-1" {
		t.Errorf("AtomID = %q", c.AtomID)
	}
	if c.AtomTitle != "What is a base case?" {
		t.Errorf("AtomTitle = %q", c.AtomTitle)
	}
	if c.TopicNodePath != "cs / recursion" {
		t.Errorf("TopicNodePath = %q; want 'cs / recursion'", c.TopicNodePath)
	}
}

func TestNewCitation_UnknownTitleStaysBlank(t *testing.T) {
	// D5: an unresolved atom (no local title) must surface as blank, NEVER a
	// fabricated name. The atom id is still carried so the FE can render
	// "unknown".
	c := NewCitation("atom-unresolved", "", nil)
	if c.AtomID != "atom-unresolved" {
		t.Errorf("AtomID = %q; want carried through even when unresolved", c.AtomID)
	}
	if c.AtomTitle != "" {
		t.Errorf("AtomTitle = %q; want blank (honest unknown, never fabricated)", c.AtomTitle)
	}
	if c.TopicNodePath != "" {
		t.Errorf("TopicNodePath = %q; want blank when no tags", c.TopicNodePath)
	}
}

func TestAssembleView_HasMemoryTrueWhenPresent(t *testing.T) {
	ic := InstanceContext{
		CompanionID:   "fam-1",
		Name:          "Byte",
		Focus:         "coding",
		Persona:       "a patient mentor",
		Rules:         map[string]string{"tone": "warm"},
		EvolutionTier: "adept",
		Skills:        []string{"debugging"},
	}
	mems := []EpisodicMemory{{ID: "m1", MemoryType: "chat_turn", Content: "we discussed loops", CreatedAt: time.Now()}}
	neighbors := []NeighborConcept{{ConceptID: "c1", ConceptTitle: "Recursion"}}

	v := AssembleView(ic, mems, neighbors, nil)
	if !v.HasMemory {
		t.Error("HasMemory = false; want true when memories present")
	}
	if v.CompanionID != "fam-1" || v.Name != "Byte" || v.Focus != "coding" {
		t.Errorf("identity fields not carried: %+v", v)
	}
	if v.Persona != "a patient mentor" || v.EvolutionTier != "adept" {
		t.Errorf("context fields not carried: %+v", v)
	}
	if v.Rules["tone"] != "warm" {
		t.Errorf("rules not carried: %+v", v.Rules)
	}
	if len(v.Skills) != 1 || v.Skills[0] != "debugging" {
		t.Errorf("skills not carried: %+v", v.Skills)
	}
	if len(v.Memories) != 1 || len(v.VisibleNeighbors) != 1 {
		t.Errorf("collections not carried: %+v", v)
	}
}

func TestAssembleView_EmptyMemoriesIsHonestNoMemoryYet(t *testing.T) {
	// The empty-state contract: no recalls ⇒ HasMemory=false + a non-nil (empty)
	// slice, so the FE renders an honest "no memory yet" rather than a fabricated
	// placeholder memory (ADR-215 D5, ADR-207 "Unknown is a state").
	v := AssembleView(InstanceContext{CompanionID: "fam-1"}, nil, nil, nil)
	if v.HasMemory {
		t.Error("HasMemory = true; want false on empty recall set")
	}
	if v.Memories == nil {
		t.Error("Memories = nil; want non-nil empty slice for a stable wire shape")
	}
	if len(v.Memories) != 0 {
		t.Errorf("Memories = %v; want empty", v.Memories)
	}
	if v.VisibleNeighbors == nil {
		t.Error("VisibleNeighbors = nil; want non-nil empty slice")
	}
	if v.Rules == nil {
		t.Error("Rules = nil; want non-nil empty map for a stable wire shape")
	}
	if v.Skills == nil {
		t.Error("Skills = nil; want non-nil empty slice")
	}
}
