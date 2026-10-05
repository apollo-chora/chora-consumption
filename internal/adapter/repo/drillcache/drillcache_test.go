// drillcache_test.go — W4 (Epic-1b): the Upsert decorator that resolves +
// caches semantic drill atoms for every Growth-Edge write. Soft-fail: a
// search/cache miss never fails the underlying upsert.
package drillcache

import (
	"context"
	"errors"
	"testing"
	"time"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

type innerFake struct {
	upserts    []lw.UpsertInput
	upsertErr  error
	cacheCalls []cacheCall
	cacheErr   error
}

type cacheCall struct {
	GCID    string
	EdgeID  string
	AtomIDs []string
}

func (f *innerFake) Upsert(_ context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	f.upserts = append(f.upserts, in)
	if f.upsertErr != nil {
		return lw.UpsertResult{}, f.upsertErr
	}
	return lw.UpsertResult{ID: "edge-1"}, nil
}
func (f *innerFake) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	return lw.ListResult{}, nil
}
func (f *innerFake) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil
}
func (f *innerFake) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (f *innerFake) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (f *innerFake) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (f *innerFake) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (f *innerFake) SetCachedDrillAtoms(_ context.Context, gcid, id string, atomIDs []string, _ time.Time) error {
	f.cacheCalls = append(f.cacheCalls, cacheCall{GCID: gcid, EdgeID: id, AtomIDs: atomIDs})
	return f.cacheErr
}

type searcherFake struct {
	gotTenant string
	gotQuery  []float32
	gotLimit  int
	atomIDs   []string
	err       error
}

func (s *searcherFake) SearchByEmbedding(_ context.Context, tenantID string, query []float32, limit int) ([]string, error) {
	s.gotTenant = tenantID
	s.gotQuery = query
	s.gotLimit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.atomIDs, nil
}

func upsertIn() lw.UpsertInput {
	return lw.UpsertInput{
		TenantID:     "tnt-1",
		LearnerGCID:  "gcid-1",
		ConceptLabel: "Fractions",
		Embedding:    []float32{0.1, 0.2},
		Strength:     0.8,
		Source:       lw.SourceExplicit,
		Now:          time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestUpsert_CachesDrillAtoms(t *testing.T) {
	inner := &innerFake{}
	search := &searcherFake{atomIDs: []string{"a-1", "a-2"}}
	repo := Wrap(inner, search, nil)

	res, err := repo.Upsert(context.Background(), upsertIn())
	if err != nil || res.ID != "edge-1" {
		t.Fatalf("Upsert = %+v, %v", res, err)
	}
	if search.gotTenant != "tnt-1" || len(search.gotQuery) != 2 || search.gotLimit != DrillCacheTopN {
		t.Fatalf("search got tenant=%q query=%v limit=%d", search.gotTenant, search.gotQuery, search.gotLimit)
	}
	if len(inner.cacheCalls) != 1 {
		t.Fatalf("cache calls = %+v", inner.cacheCalls)
	}
	cc := inner.cacheCalls[0]
	if cc.EdgeID != "edge-1" || cc.GCID != "gcid-1" || len(cc.AtomIDs) != 2 {
		t.Fatalf("cache call = %+v", cc)
	}
}

func TestUpsert_SearchFailureSoftFails(t *testing.T) {
	inner := &innerFake{}
	repo := Wrap(inner, &searcherFake{err: errors.New("boom")}, nil)
	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("search failure must not fail the upsert: %v", err)
	}
	if len(inner.cacheCalls) != 0 {
		t.Fatal("no cache call on search failure")
	}
}

func TestUpsert_EmptyMatchesSkipsCache(t *testing.T) {
	inner := &innerFake{}
	repo := Wrap(inner, &searcherFake{}, nil)
	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(inner.cacheCalls) != 0 {
		t.Fatal("empty matches must not clobber an existing cache")
	}
}

func TestUpsert_CacheWriteFailureSoftFails(t *testing.T) {
	inner := &innerFake{cacheErr: errors.New("boom")}
	repo := Wrap(inner, &searcherFake{atomIDs: []string{"a-1"}}, nil)
	if _, err := repo.Upsert(context.Background(), upsertIn()); err != nil {
		t.Fatalf("cache-write failure must not fail the upsert: %v", err)
	}
}

func TestUpsert_InnerErrorPropagates(t *testing.T) {
	inner := &innerFake{upsertErr: errors.New("boom")}
	search := &searcherFake{atomIDs: []string{"a-1"}}
	repo := Wrap(inner, search, nil)
	if _, err := repo.Upsert(context.Background(), upsertIn()); err == nil {
		t.Fatal("inner upsert error must propagate")
	}
	if len(inner.cacheCalls) != 0 || search.gotTenant != "" {
		t.Fatal("no search/cache after a failed upsert")
	}
}

func TestWrap_NilSearcherReturnsInner(t *testing.T) {
	inner := &innerFake{}
	if got := Wrap(inner, nil, nil); got != lw.Repository(inner) {
		t.Fatal("nil searcher must return the inner repo unwrapped")
	}
}
