// atom_index.go — in-memory store for the EXT-scope atom_index projection
// (hydrated from `chora.creation.atom.created.v1`).
//
// Per ddd-enforcement HARD RULE: chora-consumption never queries
// chora_creation directly. The atom_index is a SKINNY local projection
// owned by chora-consumption. Production (M12+) replaces this with
// PostgreSQL-backed pgx + RLS.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// searchDefaultLimit / searchMaxLimit mirror the pg adapter clamps so the
// in-memory and Postgres adapters agree on the same SearchForLearner contract.
const (
	searchDefaultLimit = 5
	searchMaxLimit     = 10
)

// AtomIndexRepo is an in-memory store for AtomIndex projections.
//
// Implements atom_index.Repo: methods take a context for parity with the
// pg-backed adapter (which uses it for the RLS tenant session); the in-memory
// store ignores ctx.
type AtomIndexRepo struct {
	mu    sync.RWMutex
	store map[string]*atom_index.AtomIndex
}

// NewAtomIndexRepo constructs an empty repo.
func NewAtomIndexRepo() *AtomIndexRepo {
	return &AtomIndexRepo{store: make(map[string]*atom_index.AtomIndex)}
}

// Compile-time check.
var _ atom_index.Repo = (*AtomIndexRepo)(nil)

// Save inserts or updates the projection by AtomID.
//
// status semantics mirror the pg upsert (CHO-1968): an empty Status normalises
// to draft (the column DEFAULT + domain New default), and the merge is
// NON-DOWNGRADING — once a row is PUBLISHED, a late/out-of-order atom.created
// (draft, EMPTY key) must never revert the status NOR clobber the authoritative
// answer key (which came from atom.published). When the existing row is already
// published we preserve status + correct_option_id + answer_count and take
// everything else (title / tags / difficulty / atom_type / published_at) from
// the incoming projection — byte-identical to the pg ON CONFLICT CASEs.
func (r *AtomIndexRepo) Save(_ context.Context, a *atom_index.AtomIndex) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.Status == "" {
		a.Status = atom_index.StatusDraft
	}
	if existing, ok := r.store[a.AtomID]; ok && existing.Status == atom_index.StatusPublished {
		a.Status = atom_index.StatusPublished        // non-downgrading status
		a.CorrectOptionID = existing.CorrectOptionID // preserve authoritative key
		a.AnswerCount = existing.AnswerCount
	}
	r.store[a.AtomID] = a
	return nil
}

// MarkPublished applies the atom.published flip: it sets ONLY status + the MCQ
// answer key (atom_type / correct_option_id / answer_count) for one atom,
// preserving title / topic_tags / course_id / difficulty. Identical semantics to
// the pg adapter (targeted set + non-downgrading status). A no-op when the atom
// is not yet projected (the atom.created event projects it first). CHO-1968.
func (r *AtomIndexRepo) MarkPublished(_ context.Context, atomID string, st atom_index.Status, atomType, correctOptionID string, answerCount int, cognitiveLevel string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.store[atomID]
	if !ok {
		return nil // not projected yet — targeted UPDATE matches 0 rows (no error)
	}
	if a.Status != atom_index.StatusPublished {
		a.Status = st // non-downgrading
	}
	a.AtomType = atomType
	a.CorrectOptionID = correctOptionID
	a.AnswerCount = answerCount
	if lvl := atom_index.NormalizeCognitiveLevel(cognitiveLevel); lvl != "" {
		a.CognitiveLevel = lvl // blank never clobbers a known level (WS-C3)
	}
	return nil
}

// Get returns the projection by AtomID, excluding soft-deleted. Not-found
// maps to the DOMAIN sentinel atom_index.ErrNotFound (same contract as the
// pg adapter) so callers can fail loud on real storage errors while treating
// unprojected atoms as an expected condition.
func (r *AtomIndexRepo) Get(_ context.Context, atomID string) (*atom_index.AtomIndex, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.store[atomID]
	if !ok || a.DeletedAt != nil {
		return nil, atom_index.ErrNotFound
	}
	return a, nil
}

// ListByCourse returns all atoms attached to a (tenant, course) pair,
// excluding soft-deleted. Order is non-deterministic — callers sort by
// PublishedAt or AtomID for stable ordering.
func (r *AtomIndexRepo) ListByCourse(_ context.Context, tenantID, courseID string) ([]*atom_index.AtomIndex, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*atom_index.AtomIndex, 0)
	for _, a := range r.store {
		if a.DeletedAt != nil {
			continue
		}
		if a.TenantID != tenantID {
			continue
		}
		if a.CourseID != courseID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// SearchForLearner returns up to `limit` live atoms for a tenant ordered
// most-recently-published first, biasing topic-matching atoms ahead of the
// rest when topicHint is set (then topping up with the next most-recent
// non-matching atoms). Mirrors the pg adapter contract.
func (r *AtomIndexRepo) SearchForLearner(_ context.Context, tenantID, topicHint string, limit int) ([]*atom_index.AtomIndex, error) {
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}

	r.mu.RLock()
	live := make([]*atom_index.AtomIndex, 0, len(r.store))
	for _, a := range r.store {
		if a.DeletedAt != nil || a.TenantID != tenantID {
			continue
		}
		live = append(live, a)
	}
	r.mu.RUnlock()

	// Stable recency ordering (newest first); atom_id ascending as a
	// deterministic tiebreaker so equal-timestamp results never flap.
	sort.Slice(live, func(i, j int) bool {
		if live[i].PublishedAt.Equal(live[j].PublishedAt) {
			return live[i].AtomID < live[j].AtomID
		}
		return live[i].PublishedAt.After(live[j].PublishedAt)
	})

	hint := strings.ToLower(strings.TrimSpace(topicHint))
	if hint != "" {
		// Partition: topic-matches (already in recency order) first, then the
		// rest (also in recency order) as top-up.
		matches := make([]*atom_index.AtomIndex, 0, len(live))
		rest := make([]*atom_index.AtomIndex, 0, len(live))
		for _, a := range live {
			if atomMatchesTopic(a, hint) {
				matches = append(matches, a)
			} else {
				rest = append(rest, a)
			}
		}
		live = append(matches, rest...)
	}

	if len(live) > limit {
		live = live[:limit]
	}
	out := make([]*atom_index.AtomIndex, len(live))
	copy(out, live)
	return out, nil
}

// atomMatchesTopic reports whether any of the atom's topic tags overlaps the
// (already lower-cased, trimmed) hint via case-insensitive substring match in
// either direction (so "scrum" matches a "scrum-basics" tag and vice versa).
func atomMatchesTopic(a *atom_index.AtomIndex, hint string) bool {
	for _, tag := range a.TopicTags {
		t := strings.ToLower(strings.TrimSpace(tag))
		if t == "" {
			continue
		}
		if strings.Contains(t, hint) || strings.Contains(hint, t) {
			return true
		}
	}
	return false
}
