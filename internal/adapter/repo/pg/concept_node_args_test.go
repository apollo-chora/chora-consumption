// concept_node_args_test.go: regression guard for the shared concept_nodes
// SQL constants. `sub_goal` + `sub_goal_provenance` were added to
// insertConceptNodeSQL / updateConceptNodeSQL but only ConceptNodeRepo's own
// call sites were updated; the other six passed the old arity and every write
// failed at runtime with "expected 12 parameters but got 10" (a 500 on
// POST /v1/me/goals/{id}/learning-edges, concept accept, cluster projection
// and merge/split).
//
// The arg lists now come from conceptNodeInsertArgs / conceptNodeUpdateArgs, so
// arity can only drift in ONE place: and these tests fail loudly when it does.
package pg

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

var placeholderRe = regexp.MustCompile(`\$(\d+)`)

// maxPlaceholder returns the highest $N referenced by the statement, which is
// the exact number of arguments pgx must be given.
func maxPlaceholder(sql string) int {
	highest := 0
	for _, m := range placeholderRe.FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest
}

func sampleConceptNode() *conceptgraph.ConceptNode {
	now := time.Now().UTC()
	return &conceptgraph.ConceptNode{
		ConceptID:   "019fda60-0000-7000-8000-00000000000a",
		TenantID:    "11111111-1111-7111-8111-111111111111",
		LearnerGCID: "00000000-0000-7000-8000-000000001999",
		Title:       "Cycling Safety Protocols",
		AtomRefs:    []string{},
		ConceptKey:  "cycling-safety-protocols",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestConceptNodeInsertArgsMatchesPlaceholderCount(t *testing.T) {
	want := maxPlaceholder(insertConceptNodeSQL)
	got := len(conceptNodeInsertArgs(sampleConceptNode()))
	if got != want {
		t.Fatalf("insertConceptNodeSQL takes %d parameters but conceptNodeInsertArgs supplies %d", want, got)
	}
}

func TestConceptNodeUpdateArgsMatchesPlaceholderCount(t *testing.T) {
	want := maxPlaceholder(updateConceptNodeSQL)
	got := len(conceptNodeUpdateArgs(sampleConceptNode()))
	if got != want {
		t.Fatalf("updateConceptNodeSQL takes %d parameters but conceptNodeUpdateArgs supplies %d", want, got)
	}
}

// The trailing sub-goal columns are the ones that drifted; assert they are
// actually carried rather than silently defaulted.
func TestConceptNodeArgsCarrySubGoalColumns(t *testing.T) {
	n := sampleConceptNode()
	n.SubGoal = "keep strictly to the left"
	n.SubGoalProvenance = conceptgraph.Provenance("learner_authored")

	for name, args := range map[string][]any{
		"insert": conceptNodeInsertArgs(n),
		"update": conceptNodeUpdateArgs(n),
	} {
		last := args[len(args)-1]
		penultimate := args[len(args)-2]
		if penultimate != "keep strictly to the left" {
			t.Errorf("%s: sub_goal not carried, got %v", name, penultimate)
		}
		if last != "learner_authored" {
			t.Errorf("%s: sub_goal_provenance not carried, got %v", name, last)
		}
	}
}
