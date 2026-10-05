// atom_attempt.go - in-memory store for the EXT-scope AtomAttempt
// state-machine aggregate. Implements atom_attempt.Repository (CHO-2029)
// as the unit-server default; cmd/server swaps the pg adapter at boot so
// sessions survive replica hops.
package inmem

import (
	"context"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

// AtomSessionRepo is an in-memory store for AtomAttempt.
type AtomSessionRepo struct {
	mu    sync.RWMutex
	store map[string]*atom_attempt.AtomAttempt
}

// NewAtomSessionRepo constructs an empty repo.
func NewAtomSessionRepo() *AtomSessionRepo {
	return &AtomSessionRepo{store: make(map[string]*atom_attempt.AtomAttempt)}
}

// Save inserts or updates the session.
func (r *AtomSessionRepo) Save(_ context.Context, s *atom_attempt.AtomAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[s.SessionID] = s
	return nil
}

// Get returns the session, excluding soft-deleted.
func (r *AtomSessionRepo) Get(_ context.Context, id string) (*atom_attempt.AtomAttempt, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.store[id]
	if !ok || s.DeletedAt != nil {
		return nil, atom_attempt.ErrNotFound
	}
	return s, nil
}

var _ atom_attempt.Repository = (*AtomSessionRepo)(nil)
