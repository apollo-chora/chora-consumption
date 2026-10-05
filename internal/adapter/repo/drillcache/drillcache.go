// Package drillcache — W4 (Epic-1b): a lw.Repository decorator that resolves
// semantic drill atoms for every Growth-Edge Upsert and caches them on the
// edge (cached_drill_atom_ids), so the Daily Dose (W6) and batch targeting
// (W9) can drill instantly without a per-read gRPC round-trip.
//
// The searcher is chora-creation's ContentRetrieval.SearchEmbeddings — the
// edge's precomputed concept embedding is the query (same text-embedding-004
// space creation indexes atoms in). Per ddd-enforcement the sync gRPC is
// sanctioned: deterministic, no LLM, idempotent, side-effect-free.
//
// GRADABILITY INVARIANT (CHO-1895): only gradable+answerable atoms (atom_index
// IsMCQ AND topic-tagged) are cached. A non-gradable atom (no answer key / no
// topic) renders no options in the player and the DerivedWeaknessProjector skips
// it (IsMCQ()==false short-circuits HandleAtomSessionCompleted), so caching it as
// a drill is a dead end that can never close the upload→practice→grow loop. When
// a GradabilityFilter is wired the searched candidate pool is widened
// (DrillCacheCandidateN) so filtering to the gradable subset rarely starves the
// cache; the gradable matches are capped back to DrillCacheTopN. A nil filter
// disables filtering (legacy behaviour — every searched atom is cached).
//
// Soft-fail by design: the upsert is the load-bearing write; a search, filter,
// or cache-write failure logs + leaves the edge's existing cache untouched (W6
// falls back to topic matching).
package drillcache

import (
	"context"
	"errors"
	"log"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// DrillCacheTopN bounds how many drill atoms cache per edge.
const DrillCacheTopN = 5

// DrillCacheCandidateN is the widened candidate pool fetched when a
// GradabilityFilter is wired: gradable atoms are sparse relative to all
// published atoms, so the nearest DrillCacheTopN alone often contain none. A
// 5× pool gives the filter headroom to find DrillCacheTopN gradable matches
// without an unbounded scan. (Without a filter the search stays at
// DrillCacheTopN — legacy behaviour.)
const DrillCacheCandidateN = 25

// AtomSearcher resolves the nearest published atoms for a concept embedding.
type AtomSearcher interface {
	SearchByEmbedding(ctx context.Context, tenantID string, query []float32, limit int) ([]string, error)
}

// GradabilityFilter reports whether a candidate drill atom is gradable +
// answerable (atom_index IsMCQ AND topic-tagged). A non-gradable atom must never
// be cached as a drill. Optional: a nil filter disables filtering.
type GradabilityFilter interface {
	Gradable(ctx context.Context, atomID string) (bool, error)
}

// Repository decorates a lw.Repository with drill-atom caching on Upsert.
// Every other method passes through.
type Repository struct {
	lw.Repository
	search AtomSearcher
	filter GradabilityFilter // optional; nil ⇒ no gradability filtering
}

// Wrap returns the decorated repository; a nil searcher returns the inner
// repo unwrapped (no-op decoration). filter is optional — nil disables the
// gradability filter (every searched atom is cached, legacy behaviour).
func Wrap(inner lw.Repository, search AtomSearcher, filter GradabilityFilter) lw.Repository {
	if search == nil {
		return inner
	}
	return &Repository{Repository: inner, search: search, filter: filter}
}

// Upsert performs the inner upsert, then best-effort resolves + caches gradable
// drill atoms for the resulting edge.
func (r *Repository) Upsert(ctx context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	res, err := r.Repository.Upsert(ctx, in)
	if err != nil {
		return res, err
	}
	limit := DrillCacheTopN
	if r.filter != nil {
		limit = DrillCacheCandidateN // widen so gradability filtering doesn't starve the cache
	}
	candidates, serr := r.search.SearchByEmbedding(ctx, in.TenantID, in.Embedding, limit)
	if serr != nil {
		log.Printf("drillcache: search for edge %s failed (non-fatal): %v", res.ID, serr)
		return res, nil
	}
	atomIDs, ferr := r.gradableOnly(ctx, candidates)
	if ferr != nil {
		log.Printf("drillcache: gradability filter for edge %s failed (non-fatal): %v", res.ID, ferr)
		return res, nil
	}
	if len(atomIDs) == 0 {
		return res, nil // nothing gradable matched — keep any existing cache
	}
	if cerr := r.Repository.SetCachedDrillAtoms(ctx, in.LearnerGCID, res.ID, atomIDs, in.Now); cerr != nil {
		log.Printf("drillcache: cache write for edge %s failed (non-fatal): %v", res.ID, cerr)
	}
	return res, nil
}

// gradableOnly returns the gradable subset of candidates (in candidate order),
// capped at DrillCacheTopN. A nil filter returns the first DrillCacheTopN
// candidates unchanged (legacy). A filter error aborts (caller soft-fails).
func (r *Repository) gradableOnly(ctx context.Context, candidates []string) ([]string, error) {
	if r.filter == nil {
		if len(candidates) > DrillCacheTopN {
			return candidates[:DrillCacheTopN], nil
		}
		return candidates, nil
	}
	out := make([]string, 0, DrillCacheTopN)
	for _, id := range candidates {
		if len(out) >= DrillCacheTopN {
			break
		}
		ok, err := r.filter.Gradable(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// atomGradableGetter is the minimal atom_index read the gradability filter needs.
type atomGradableGetter interface {
	Get(ctx context.Context, atomID string) (*atom_index.AtomIndex, error)
}

// atomIndexGradability adapts an atom_index reader to the GradabilityFilter port.
type atomIndexGradability struct{ repo atomGradableGetter }

// AtomIndexGradability adapts an atom_index reader to the GradabilityFilter port.
// A nil repo returns a nil filter (filtering disabled), so callers can wire it
// unconditionally.
func AtomIndexGradability(repo atomGradableGetter) GradabilityFilter {
	if repo == nil {
		return nil
	}
	return &atomIndexGradability{repo: repo}
}

// Gradable reports whether the atom is a gradable+answerable drill: an MCQ with
// an answer key (IsMCQ) AND a primary topic (so the projector can attribute the
// completion). An unprojected atom (ErrNotFound) is not (yet) gradable — no
// error. A real storage error surfaces so the caller can soft-fail loudly.
func (g *atomIndexGradability) Gradable(ctx context.Context, atomID string) (bool, error) {
	a, err := g.repo.Get(ctx, atomID)
	if errors.Is(err, atom_index.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a == nil {
		return false, nil
	}
	return a.IsMCQ() && a.PrimaryTopic() != "", nil
}
