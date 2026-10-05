// ceremony_edge_scout_runs.go — in-memory edgescout.RunStore (CHO-2040, owner
// ruling R8-1). Hermetic fixture for tests + pool-less dev boots; production
// wires pg.CeremonyEdgeScoutRunRepo over migration 0067. Mirrors the pg
// semantics: HasRun on the full (tenant, goal, learner) triple; RecordRun
// idempotent (the double-tap is a no-op success, like ON CONFLICT DO NOTHING).
package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
)

// CeremonyEdgeScoutRunRepo is the in-memory first-run ledger.
type CeremonyEdgeScoutRunRepo struct {
	mu   sync.Mutex
	runs map[string]time.Time // triple key → first_run_at
}

// NewCeremonyEdgeScoutRunRepo constructs an empty ledger.
func NewCeremonyEdgeScoutRunRepo() *CeremonyEdgeScoutRunRepo {
	return &CeremonyEdgeScoutRunRepo{runs: make(map[string]time.Time)}
}

func ceremonyRunKey(tenantID, goalID, learnerGCID string) string {
	return tenantID + "|" + goalID + "|" + learnerGCID
}

// HasRun satisfies edgescout.RunStore.
func (r *CeremonyEdgeScoutRunRepo) HasRun(_ context.Context, tenantID, goalID, learnerGCID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[ceremonyRunKey(tenantID, goalID, learnerGCID)]
	return ok, nil
}

// RecordRun satisfies edgescout.RunStore. Idempotent: an existing row wins
// (the first first_run_at is kept, mirroring ON CONFLICT DO NOTHING).
func (r *CeremonyEdgeScoutRunRepo) RecordRun(_ context.Context, tenantID, goalID, learnerGCID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ceremonyRunKey(tenantID, goalID, learnerGCID)
	if _, ok := r.runs[key]; ok {
		return nil
	}
	r.runs[key] = at
	return nil
}

// Compile-time port assertion.
var _ edgescout.RunStore = (*CeremonyEdgeScoutRunRepo)(nil)
