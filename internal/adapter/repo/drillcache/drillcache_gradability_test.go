// drillcache_gradability_test.go — CHO-1895 gradability invariant: the W4
// drill-atom cache must contain ONLY gradable+answerable atoms (atom_index IsMCQ
// AND topic-tagged). A non-gradable atom (no answer key / no topic) renders no
// options in the player and the DerivedWeaknessProjector skips it (IsMCQ()==false
// short-circuits HandleAtomSessionCompleted) — so caching it as a drill is a dead
// end that can never close the upload→practice→grow loop. The decorator resolves
// each searched candidate through a GradabilityFilter and caches only the gradable
// ones (capped at DrillCacheTopN), widening the search so filtering never starves.
//
// TDD strict (RED → GREEN): these assert the filtered behaviour before the filter
// exists — they fail to compile (Wrap is 2-arg; GradabilityFilter undefined) until
// the port + filtering land.
package drillcache

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// atomGetFake is a minimal atomGradableGetter for the AtomIndexGradability adapter.
type atomGetFake struct {
	atoms map[string]*atom_index.AtomIndex
	err   error
}

func (f *atomGetFake) Get(_ context.Context, atomID string) (*atom_index.AtomIndex, error) {
	if f.err != nil {
		return nil, f.err
	}
	if a, ok := f.atoms[atomID]; ok {
		return a, nil
	}
	return nil, atom_index.ErrNotFound
}

func TestAtomIndexGradability_Decision(t *testing.T) {
	getter := &atomGetFake{atoms: map[string]*atom_index.AtomIndex{
		"gradable": {AtomID: "gradable", AtomType: "mcq", CorrectOptionID: "b", TopicTags: []string{"algorithms"}},
		"no-key":   {AtomID: "no-key", AtomType: "mcq", TopicTags: []string{"algorithms"}}, // IsMCQ false (no answer key)
		"not-mcq":  {AtomID: "not-mcq", AtomType: "text", CorrectOptionID: "b", TopicTags: []string{"algorithms"}},
		"no-topic": {AtomID: "no-topic", AtomType: "mcq", CorrectOptionID: "b"}, // PrimaryTopic ""
	}}
	f := AtomIndexGradability(getter)

	cases := []struct {
		atomID string
		want   bool
	}{
		{"gradable", true},
		{"no-key", false},
		{"not-mcq", false},
		{"no-topic", false},
		{"unprojected", false}, // ErrNotFound → (false, nil)
	}
	for _, c := range cases {
		got, err := f.Gradable(context.Background(), c.atomID)
		if err != nil {
			t.Errorf("Gradable(%s) err = %v, want nil", c.atomID, err)
		}
		if got != c.want {
			t.Errorf("Gradable(%s) = %v, want %v", c.atomID, got, c.want)
		}
	}
}

func TestAtomIndexGradability_StorageErrorSurfaces(t *testing.T) {
	f := AtomIndexGradability(&atomGetFake{err: errors.New("pg down")})
	if _, err := f.Gradable(context.Background(), "x"); err == nil {
		t.Fatal("a real storage error must surface (so the decorator soft-fails loudly)")
	}
}

func TestAtomIndexGradability_NilRepoDisablesFilter(t *testing.T) {
	if f := AtomIndexGradability(nil); f != nil {
		t.Fatalf("AtomIndexGradability(nil) = %v, want nil (filtering disabled)", f)
	}
}

// gradFake is a settable GradabilityFilter: gradable[id]==true ⇒ gradable.
type gradFake struct {
	gradable map[string]bool
	err      error
	calls    []string
}

func (g *gradFake) Gradable(_ context.Context, atomID string) (bool, error) {
	g.calls = append(g.calls, atomID)
	if g.err != nil {
		return false, g.err
	}
	return g.gradable[atomID], nil
}

func TestUpsert_FiltersNonGradableDrillAtoms(t *testing.T) {
	inner := &innerFake{}
	search := &searcherFake{atomIDs: []string{"a-1", "a-2", "a-3", "a-4"}}
	filter := &gradFake{gradable: map[string]bool{"a-1": true, "a-3": true}} // a-2,a-4 non-gradable
	repo := Wrap(inner, search, filter)

	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Search widened beyond the cache size so gradability filtering has headroom.
	if search.gotLimit != DrillCacheCandidateN {
		t.Fatalf("search limit = %d, want DrillCacheCandidateN=%d (widened for filtering)", search.gotLimit, DrillCacheCandidateN)
	}
	if len(inner.cacheCalls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(inner.cacheCalls))
	}
	got := inner.cacheCalls[0].AtomIDs
	if len(got) != 2 || got[0] != "a-1" || got[1] != "a-3" {
		t.Fatalf("cached atoms = %v, want [a-1 a-3] (only the gradable candidates, in order)", got)
	}
}

func TestUpsert_AllNonGradable_SkipsCache(t *testing.T) {
	inner := &innerFake{}
	search := &searcherFake{atomIDs: []string{"a-1", "a-2"}}
	filter := &gradFake{gradable: map[string]bool{}} // none gradable
	repo := Wrap(inner, search, filter)

	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(inner.cacheCalls) != 0 {
		t.Fatalf("no gradable atom must NOT clobber the cache; got %+v", inner.cacheCalls)
	}
}

func TestUpsert_FilterErrorSoftFails(t *testing.T) {
	inner := &innerFake{}
	search := &searcherFake{atomIDs: []string{"a-1"}}
	filter := &gradFake{err: errors.New("boom")}
	repo := Wrap(inner, search, filter)

	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("a gradability-filter failure must not fail the upsert: %v", err)
	}
	if len(inner.cacheCalls) != 0 {
		t.Fatal("no cache write on a filter error (soft-fail, keep existing cache)")
	}
}

func TestUpsert_CapsGradableAtTopN(t *testing.T) {
	inner := &innerFake{}
	ids := []string{"a-1", "a-2", "a-3", "a-4", "a-5", "a-6", "a-7"}
	grad := map[string]bool{}
	for _, id := range ids {
		grad[id] = true
	}
	search := &searcherFake{atomIDs: ids}
	repo := Wrap(inner, search, &gradFake{gradable: grad})

	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(inner.cacheCalls) != 1 || len(inner.cacheCalls[0].AtomIDs) != DrillCacheTopN {
		t.Fatalf("cached count = %v, want exactly DrillCacheTopN=%d", inner.cacheCalls, DrillCacheTopN)
	}
}

func TestUpsert_NilFilterCachesAllSearched(t *testing.T) {
	inner := &innerFake{}
	search := &searcherFake{atomIDs: []string{"a-1", "a-2"}}
	repo := Wrap(inner, search, nil) // nil filter ⇒ legacy: cache all searched

	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if search.gotLimit != DrillCacheTopN {
		t.Fatalf("nil-filter search limit = %d, want DrillCacheTopN=%d (legacy, unwidened)", search.gotLimit, DrillCacheTopN)
	}
	if len(inner.cacheCalls) != 1 || len(inner.cacheCalls[0].AtomIDs) != 2 {
		t.Fatalf("nil-filter cache = %+v, want all 2 searched atoms", inner.cacheCalls)
	}
}
