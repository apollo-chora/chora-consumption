// repository.go - the AtomAttempt persistence port (CHO-2029).
//
// Sessions were in-memory-per-pod from S4.2 until 2026-07-03: with
// consumption at 2 replicas, start/submit landing on different pods
// produced intermittent SESSION_NOT_FOUND for real learners (~40%% of
// submits in the P1.D Ember walk). The port lets cmd/server swap the
// pg adapter (atomic_sessions, RLS tenant-scoped) over the in-memory
// default used by unit servers.
package atom_attempt

import (
	"context"
	"errors"
)

// ErrNotFound is the canonical not-found sentinel for Repository.Get.
// Both adapters (inmem + pg) return it so handlers can 404 on absence
// and 500 on real read failures instead of masking outages as 404s.
var ErrNotFound = errors.New("atom_session: not found")

// Repository persists AtomAttempt aggregates.
type Repository interface {
	// Save upserts the session by SessionID. Fail-loud: an error means
	// the state did NOT persist and the handler must surface it.
	Save(ctx context.Context, s *AtomAttempt) error
	// Get returns the session by id (soft-deleted excluded), or
	// ErrNotFound.
	Get(ctx context.Context, id string) (*AtomAttempt, error)
}
