package http

// companion_ritual_source_titles_test.go: a grounded ritual step shows its
// sources by NAME (UX Track U, D2 tail a).
//
// The citations landing in N9 are atom ids, because the agent's cite_atom refs
// carry an atom_id and a revision_id and no title. The learner projection
// dropped them rather than print a UUID, which was right and left a grounded
// step showing no sources at all. This resolves them at read time through the
// SAME ResonanceTitleReader D3's character sheet uses, so this service has
// exactly one atom-title resolver.
//
// Best-effort throughout, mirroring learnerCatalogue: a name is a nicety and
// the run is the payload, so an unwired or failing reader omits the name and
// never fails the read.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const (
	citedAtomA = "01920000-0000-7000-8000-00000000000a"
	citedAtomB = "01920000-0000-7000-8000-00000000000b"
)

// titleReader is a ResonanceTitleReader whose atom arm is scripted per id.
type titleReader struct {
	titles map[string]string
	err    error
	calls  []string
}

func (r *titleReader) ConceptTitle(context.Context, string) (string, error) { return "", nil }
func (r *titleReader) AtomTitle(_ context.Context, atomID string) (string, error) {
	r.calls = append(r.calls, atomID)
	if r.err != nil {
		return "", r.err
	}
	return r.titles[atomID], nil
}

func runWithCitations(t *testing.T, s *Server, ritualID string, citations []string) {
	t.Helper()
	completed := time.Now()
	repo := s.RitualRuns.(*runrepo)
	repo.runs = append(repo.runs, &companion.RitualRun{
		RunID: "run-1", TenantID: "t-1", CompanionID: "fam-1", OwnerGCID: "gcid-1",
		RitualID: ritualID, RitualName: "R", RevisionNo: 1,
		Status: companion.RunStatusCompleted, ManaCharged: 35,
		Stamps: []companion.StepStamp{{
			StepIndex: 0, SkillKey: "explain_anew", Citations: citations,
		}},
		StartedAt: completed.Add(-time.Minute), CompletedAt: &completed,
	})
}

func sourcesOf(t *testing.T, s *Server, ritualID string) []string {
	t.Helper()
	w := do(t, s, http.MethodGet, "/v1/me/companions/fam-1/rituals/"+ritualID+"/runs/run-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", w.Code, w.Body.String())
	}
	var got struct {
		Story struct {
			Steps []struct {
				Sources []string `json:"sources"`
			} `json:"steps"`
		} `json:"story"`
	}
	if err := decodeJSON(w, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Story.Steps) != 1 {
		t.Fatalf("steps = %d; want 1", len(got.Story.Steps))
	}
	return got.Story.Steps[0].Sources
}

func TestRitualRunStory_ResolvesCitedAtomIDsToTitles(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	runWithCitations(t, s, rt.RitualID, []string{citedAtomA})
	s.AtomTitles = &titleReader{titles: map[string]string{citedAtomA: "Dividing Fractions"}}

	if got := sourcesOf(t, s, rt.RitualID); len(got) != 1 || got[0] != "Dividing Fractions" {
		t.Errorf("sources = %v; want the resolved title", got)
	}
}

// An id the projection cannot name is omitted, never rendered raw.
func TestRitualRunStory_UnresolvableCitationIsOmittedNeverShownRaw(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	runWithCitations(t, s, rt.RitualID, []string{citedAtomA})
	s.AtomTitles = &titleReader{titles: map[string]string{}}

	got := sourcesOf(t, s, rt.RitualID)
	if len(got) != 0 {
		t.Errorf("sources = %v; want none", got)
	}
	for _, src := range got {
		if src == citedAtomA {
			t.Error("a raw atom id reached the learner")
		}
	}
}

// A reader that FAILS omits the name and does not fail the read. A run the
// learner can see without source names beats a 500 they cannot see at all.
func TestRitualRunStory_TitleReaderFailureDoesNotFailTheRead(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	runWithCitations(t, s, rt.RitualID, []string{citedAtomA})
	s.AtomTitles = &titleReader{err: errors.New("connection refused")}

	if got := sourcesOf(t, s, rt.RitualID); len(got) != 0 {
		t.Errorf("sources = %v; want none when the reader failed", got)
	}
}

// No reader wired at all: same outcome, same reason.
func TestRitualRunStory_UnwiredTitleReaderStillServesTheRun(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	runWithCitations(t, s, rt.RitualID, []string{citedAtomA})
	s.AtomTitles = nil

	if got := sourcesOf(t, s, rt.RitualID); len(got) != 0 {
		t.Errorf("sources = %v; want none when no reader is wired", got)
	}
}

// One lookup per DISTINCT id, however many steps cite it. A run of ten steps
// citing the same atom must not make ten reads of the same row.
func TestRitualRunStory_ResolvesEachDistinctIDOnce(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	runWithCitations(t, s, rt.RitualID, []string{citedAtomA, citedAtomB, citedAtomA})
	reader := &titleReader{titles: map[string]string{citedAtomA: "Fractions", citedAtomB: "Reciprocals"}}
	s.AtomTitles = reader

	if got := sourcesOf(t, s, rt.RitualID); len(got) != 3 {
		t.Errorf("sources = %v; want three entries, the repeat included", got)
	}
	if len(reader.calls) != 2 {
		t.Errorf("AtomTitle called %d times (%v); want one per DISTINCT id", len(reader.calls), reader.calls)
	}
}

// A citation that is already a name is never sent to the resolver: it is not an
// id, and looking it up would be a read that cannot succeed.
func TestRitualRunStory_AlreadyNamedSourceIsNotLookedUp(t *testing.T) {
	s := ritualServer(t, &manaR{})
	rt := seedPublishedRitual(t, s)
	runWithCitations(t, s, rt.RitualID, []string{"Fractions basics"})
	reader := &titleReader{titles: map[string]string{}}
	s.AtomTitles = reader

	if got := sourcesOf(t, s, rt.RitualID); len(got) != 1 || got[0] != "Fractions basics" {
		t.Errorf("sources = %v; want the already-named source", got)
	}
	if len(reader.calls) != 0 {
		t.Errorf("AtomTitle called for a non-id: %v", reader.calls)
	}
}
