// runs.go — CHO-2040 (owner ruling R8-1): the first-run ledger port. The FIRST
// ceremony edge-scout run per (tenant, goal, learner) is FREE
// (companion_ceremony_edge_scout_first, cost 0); re-runs pay the locked 25
// (companion_ceremony_edge_scout). The ledger is the scoping mechanism: no row
// ⇒ first run (stamp the zero-cost code, record the row as part of the SAME
// successful flow); row exists ⇒ the unchanged paid path.
//
// Semantics locked with the runner:
//   - HasRun errors fail the request LOUD — guessing "free" is un-metered
//     abuse, guessing "paid" silently overrides the ruling.
//   - RecordRun happens ONLY after a successful engine turn (parse+reconcile
//     done). The virgin no-turn fallback NEVER records — no LLM turn ran, so
//     the learner's first real scout stays free.
//   - RecordRun must be IDEMPOTENT on the (tenant, goal, learner) triple (the
//     pg impl absorbs the concurrent double-tap via ON CONFLICT DO NOTHING —
//     both racers ran a free turn, but the freebie is burnt exactly once).
//   - A RecordRun failure is a LOUD error, never a silent repeat-freebie.
package edgescout

import (
	"context"
	"time"
)

// RunStore is the R8-1 first-run ledger over ceremony_edge_scout_runs
// (chora_consumption migration 0067). pg-backed in production
// (pg.CeremonyEdgeScoutRunRepo); in-memory in tests/dev
// (inmem.CeremonyEdgeScoutRunRepo).
type RunStore interface {
	// HasRun reports whether the learner has already burnt the free first
	// run for this goal.
	HasRun(ctx context.Context, tenantID, goalID, learnerGCID string) (bool, error)

	// RecordRun burns the freebie. Idempotent on the triple: recording an
	// already-recorded run is a no-op success.
	RecordRun(ctx context.Context, tenantID, goalID, learnerGCID string, at time.Time) error
}
