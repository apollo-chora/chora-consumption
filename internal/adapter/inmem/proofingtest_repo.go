// proofingtest_repo.go — CHO-2040: in-memory proofingtest.Repository for unit
// servers + handler/subscriber suites (production swaps the pg adapter at
// cmd/server boot, mirroring every other repo pair in this package).
//
// Thread-safe; defensive copies on every boundary (a caller mutating its copy
// never leaks into the store). Soft-deleted rows are invisible to reads.
package inmem

import (
	"context"
	"errors"
	"sort"
	"sync"

	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// ProofingTestRepo is a map-backed proofingtest.Repository.
type ProofingTestRepo struct {
	mu        sync.RWMutex
	rows      map[string]*pt.ProofingTest // id → row
	createErr error
}

// NewProofingTestRepo constructs an empty repo.
func NewProofingTestRepo() *ProofingTestRepo {
	return &ProofingTestRepo{rows: make(map[string]*pt.ProofingTest)}
}

// Compile-time check.
var _ pt.Repository = (*ProofingTestRepo)(nil)

// FailCreate arms a one-shot-style injected Create failure (tests).
func (r *ProofingTestRepo) FailCreate(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createErr = err
}

func copyRow(p *pt.ProofingTest) *pt.ProofingTest {
	if p == nil {
		return nil
	}
	cp := *p
	cp.TargetEdges = append([]pt.TargetEdge(nil), p.TargetEdges...)
	cp.TestSetPayload = append([]byte(nil), p.TestSetPayload...)
	if p.TestSetRef != nil {
		v := *p.TestSetRef
		cp.TestSetRef = &v
	}
	if p.DeletedAt != nil {
		v := *p.DeletedAt
		cp.DeletedAt = &v
	}
	return &cp
}

// Create persists a new row. Duplicate ids fail loud.
func (r *ProofingTestRepo) Create(_ context.Context, p *pt.ProofingTest) error {
	if p == nil {
		return errors.New("inmem proofing: nil aggregate")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	if _, dup := r.rows[p.ID]; dup {
		return errors.New("inmem proofing: duplicate id " + p.ID)
	}
	r.rows[p.ID] = copyRow(p)
	return nil
}

// GetByID returns one live row or (nil, nil).
func (r *ProofingTestRepo) GetByID(_ context.Context, tenantID, learnerGCID, id string) (*pt.ProofingTest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.rows[id]
	if !ok || p.DeletedAt != nil || p.TenantID != tenantID || p.LearnerGCID != learnerGCID {
		return nil, nil
	}
	return copyRow(p), nil
}

// GetByAssistID resolves the row the qgen terminal events key on, or (nil, nil).
func (r *ProofingTestRepo) GetByAssistID(_ context.Context, tenantID, assistID string) (*pt.ProofingTest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.rows {
		if p.DeletedAt == nil && p.TenantID == tenantID && p.AssistID == assistID {
			return copyRow(p), nil
		}
	}
	return nil, nil
}

// ListByLearner returns the learner's live rows, newest first; goalID filters
// when non-empty. Always a non-nil slice.
func (r *ProofingTestRepo) ListByLearner(_ context.Context, tenantID, learnerGCID, goalID string) ([]*pt.ProofingTest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*pt.ProofingTest, 0)
	for _, p := range r.rows {
		if p.DeletedAt != nil || p.TenantID != tenantID || p.LearnerGCID != learnerGCID {
			continue
		}
		if goalID != "" && p.GoalID != goalID {
			continue
		}
		out = append(out, copyRow(p))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID // deterministic tie-break
	})
	return out, nil
}

// Update overwrites an existing row (scoped by id + tenant + learner). A
// never-created row fails loud — Update is a transition, not an upsert.
func (r *ProofingTestRepo) Update(_ context.Context, p *pt.ProofingTest) error {
	if p == nil {
		return errors.New("inmem proofing: nil aggregate")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.rows[p.ID]
	if !ok || existing.TenantID != p.TenantID || existing.LearnerGCID != p.LearnerGCID {
		return errors.New("inmem proofing: update of unknown row " + p.ID)
	}
	r.rows[p.ID] = copyRow(p)
	return nil
}
