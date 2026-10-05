// companion_suspension_projection.go: in-memory companion.SuspensionProjection
// (ADR-254 D11 advisory projection) for tests and the no-pool boot path.
//
// Same semantics as the pg adapter (internal/adapter/repo/pg
// companion_suspension_projection_repo.go): one row per observability
// suspension id, applied only when the incoming event Supersedes the held
// row (monotonic source_version, then changed_at), released rows retained but
// never pausing. NOT durable across restart: production wires the pg adapter
// whenever a pool is present, exactly like the other in-memory fallbacks here.
package inmem

import (
	"context"
	"sort"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// CompanionSuspensionProjection is the in-memory projection store.
type CompanionSuspensionProjection struct {
	mu   sync.RWMutex
	rows map[string]companion.SuspensionRecord // suspension_id -> latest state
}

// NewCompanionSuspensionProjection constructs an empty projection.
func NewCompanionSuspensionProjection() *CompanionSuspensionProjection {
	return &CompanionSuspensionProjection{rows: make(map[string]companion.SuspensionRecord)}
}

// Apply projects one event. Returns (false, nil) for a stale or duplicate
// event (the caller ACKs it), (false, err) for an invalid one (the caller
// NACKs it).
func (p *CompanionSuspensionProjection) Apply(_ context.Context, ev companion.SuspensionChanged) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	rec := ev.Record()
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.rows[rec.SuspensionID]; ok && !rec.Supersedes(existing) {
		return false, nil
	}
	p.rows[rec.SuspensionID] = rec
	return true, nil
}

// Status answers the advisory question for (tenantID, actionCode) over the
// engaged rows visible to that tenant (platform rows + its own).
func (p *CompanionSuspensionProjection) Status(_ context.Context, tenantID, actionCode string) (companion.SuspensionStatus, error) {
	p.mu.RLock()
	visible := make([]companion.SuspensionRecord, 0, len(p.rows))
	for _, r := range p.rows {
		if !r.Engaged {
			continue
		}
		if r.Scope == companion.SuspensionScopeTenant && r.TenantID != tenantID {
			continue
		}
		visible = append(visible, r)
	}
	p.mu.RUnlock()
	return companion.DecideSuspension(visible, tenantID, actionCode), nil
}

// Records returns a copy of every projected row (released ones included),
// ordered by suspension id. Test / debug helper.
func (p *CompanionSuspensionProjection) Records() []companion.SuspensionRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]companion.SuspensionRecord, 0, len(p.rows))
	for _, r := range p.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SuspensionID < out[j].SuspensionID })
	return out
}

// Compile-time check.
var _ companion.SuspensionProjection = (*CompanionSuspensionProjection)(nil)
