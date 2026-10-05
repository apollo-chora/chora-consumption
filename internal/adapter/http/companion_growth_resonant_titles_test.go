package http

// companion_growth_resonant_titles_test.go, D3 (CHO-2047): the growth read
// carries the learner-facing TITLE behind each resonant id.
//
// Why server-side. The profile used to have only the ids, so it could say a
// concept was picked but never which one. Resolving them in the browser would
// mean a second, learner-wide concept read from a screen that needs exactly
// two rows, which is a cross-map leak waiting to happen. The resolution
// therefore happens inside the SAME RLS-scoped read that produced the ids.
//
// The contract has three states and the third is the point:
//
//	resolved     → `resonant_concept_title` carries the title
//	unresolvable → the field is ABSENT, so the profile can say the concept is
//	               gone instead of inventing a name for it
//	unreadable   → the field is ABSENT too, and the response still succeeds,
//	               because a name lookup must never fail a growth read
//
// The middle and last cases look identical on the wire ON PURPOSE: a learner
// is owed the same honest copy either way, and the difference is an operator
// concern that belongs in the log, not in the payload.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

const (
	resonantConceptID = "0195d3f8-7c21-7a44-9e10-2b7f5c9a1d33"
	resonantAtomID    = "0195d3f8-7c21-7a44-9e10-4f21ab99c007"
)

// fakeTitles is a ResonanceTitleReader whose two arms are set per test.
type fakeTitles struct {
	conceptFn func(ctx context.Context, id string) (string, error)
	atomFn    func(ctx context.Context, id string) (string, error)
	sawCtx    context.Context
}

func (f *fakeTitles) ConceptTitle(ctx context.Context, id string) (string, error) {
	f.sawCtx = ctx
	if f.conceptFn != nil {
		return f.conceptFn(ctx, id)
	}
	return "", nil
}

func (f *fakeTitles) AtomTitle(ctx context.Context, id string) (string, error) {
	f.sawCtx = ctx
	if f.atomFn != nil {
		return f.atomFn(ctx, id)
	}
	return "", nil
}

/** A growth state carrying both resonant ids. */
func resonantState() *growth.State {
	return &growth.State{
		CompanionID:       "fam-1",
		GrowthStage:       2,
		StageName:         "fledgling",
		Species:           "owl",
		ResonantConceptID: resonantConceptID,
		ResonantAtomID:    resonantAtomID,
	}
}

func growthWithResonance() *fakeGrowth {
	return &fakeGrowth{
		getFn: func(_, _, _ string) (*growth.State, error) { return resonantState(), nil },
	}
}

// readTitles issues the growth GET and returns the two title fields plus
// whether each was present at all, which is the distinction under test.
func readTitles(t *testing.T, srv *Server) (concept, atom string, conceptSet, atomSet bool) {
	t.Helper()
	resp := doGrowthReq(t, srv, http.MethodGet, "/v1/me/companions/fam-1/growth", nil, "tenant-1", "gcid-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	c, cok := body["resonant_concept_title"]
	a, aok := body["resonant_atom_title"]
	cs, _ := c.(string)
	as, _ := a.(string)
	return cs, as, cok, aok
}

func TestGrowthRead_CarriesResonantTitles(t *testing.T) {
	srv := newServerWithGrowth(growthWithResonance())
	srv.ResonanceTitles = &fakeTitles{
		conceptFn: func(_ context.Context, id string) (string, error) {
			if id != resonantConceptID {
				t.Fatalf("concept id not threaded: %q", id)
			}
			return "Roundabout priority", nil
		},
		atomFn: func(_ context.Context, id string) (string, error) {
			if id != resonantAtomID {
				t.Fatalf("atom id not threaded: %q", id)
			}
			return "Who yields on entry", nil
		},
	}

	concept, atom, conceptSet, atomSet := readTitles(t, srv)
	if !conceptSet || concept != "Roundabout priority" {
		t.Fatalf("concept title = %q (set=%v), want %q", concept, conceptSet, "Roundabout priority")
	}
	if !atomSet || atom != "Who yields on entry" {
		t.Fatalf("atom title = %q (set=%v), want %q", atom, atomSet, "Who yields on entry")
	}
}

func TestGrowthRead_OmitsTitleWhenTheConceptNoLongerResolves(t *testing.T) {
	srv := newServerWithGrowth(growthWithResonance())
	srv.ResonanceTitles = &fakeTitles{
		// The concept left this learner's map; the id on the companion row is
		// now dangling. An empty title with no error is how the port says so.
		conceptFn: func(_ context.Context, _ string) (string, error) { return "", nil },
		atomFn:    func(_ context.Context, _ string) (string, error) { return "Who yields on entry", nil },
	}

	_, atom, conceptSet, atomSet := readTitles(t, srv)
	if conceptSet {
		t.Fatal("resonant_concept_title must be ABSENT when nothing resolves, " +
			"so the profile can say the concept is gone rather than invent one")
	}
	if !atomSet || atom != "Who yields on entry" {
		t.Fatalf("one unresolved id must not suppress the other: atom = %q (set=%v)", atom, atomSet)
	}
}

func TestGrowthRead_SurvivesAnUnreadableTitleStore(t *testing.T) {
	srv := newServerWithGrowth(growthWithResonance())
	srv.ResonanceTitles = &fakeTitles{
		conceptFn: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("concept store unreachable")
		},
		atomFn: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("atom index unreachable")
		},
	}

	// A name lookup must never fail a growth read: the companion's stage, EXP
	// and skills are what the screen is for, and they are all still true.
	_, _, conceptSet, atomSet := readTitles(t, srv)
	if conceptSet || atomSet {
		t.Fatal("a failed title read must omit the titles, not emit empty ones")
	}
}

func TestGrowthRead_WorksWithNoTitleReaderWired(t *testing.T) {
	srv := newServerWithGrowth(growthWithResonance())
	srv.ResonanceTitles = nil // unit servers and any deployment that has not wired it

	_, _, conceptSet, atomSet := readTitles(t, srv)
	if conceptSet || atomSet {
		t.Fatal("no reader wired must mean no titles, never empty strings")
	}
}

func TestGrowthRead_DoesNotResolveTitlesForAnAbsentID(t *testing.T) {
	var conceptCalls, atomCalls int
	srv := newServerWithGrowth(&fakeGrowth{
		getFn: func(_, _, _ string) (*growth.State, error) {
			st := resonantState()
			st.ResonantConceptID = "" // nothing picked yet
			st.ResonantAtomID = ""
			return st, nil
		},
	})
	srv.ResonanceTitles = &fakeTitles{
		conceptFn: func(_ context.Context, _ string) (string, error) { conceptCalls++; return "x", nil },
		atomFn:    func(_ context.Context, _ string) (string, error) { atomCalls++; return "x", nil },
	}

	_, _, conceptSet, atomSet := readTitles(t, srv)
	if conceptCalls != 0 || atomCalls != 0 {
		t.Fatalf("resolved a title for an empty id: concept=%d atom=%d", conceptCalls, atomCalls)
	}
	if conceptSet || atomSet {
		t.Fatal("no id means no title field")
	}
}

func TestGrowthRead_ResolvesInsideTheRLSScopedContext(t *testing.T) {
	// The whole reason this is server-side: the lookup must ride the SAME
	// tenant and owner scoping as the read that produced the ids. If it ran on
	// a bare context the repo's rls.ApplySession would have nothing to set.
	f := &fakeTitles{
		conceptFn: func(_ context.Context, _ string) (string, error) { return "Roundabout priority", nil },
	}
	srv := newServerWithGrowth(growthWithResonance())
	srv.ResonanceTitles = f

	_, _, _, _ = readTitles(t, srv)
	if f.sawCtx == nil {
		t.Fatal("title reader never called")
	}
	if got := tracing.TenantIDFromContext(f.sawCtx); got != "tenant-1" {
		t.Fatalf("title read ran outside the RLS-scoped context: tenant = %q", got)
	}
}
