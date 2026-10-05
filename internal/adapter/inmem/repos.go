// Package inmem provides in-memory repository adapters for the Content
// Consumption domain skeleton. Production repos (PostgreSQL via pgx + RLS)
// land in M12 — see database-postgresql + multi-tenant-rls skills.
//
// All repos are thread-safe via per-store mutex. Soft-deleted entities are
// retained but excluded from default reads (per ddd-enforcement #6).
//
// S4.2: PathRepo now wraps `domain/learning_path` and SessionRepo wraps
// `domain/atom_attempt`. The legacy `domain/session` + `domain/path`
// packages have been removed (see audit-content-fillgaps.md §3.2 for the
// duplicate-aggregate rationale).
package inmem

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ErrNotFound is returned when a repo cannot find the requested entity.
var ErrNotFound = errors.New("not found")

// PathRepo is an in-memory store for LearningPath. Backs the legacy
// /api/learning-paths router. Internal alias to learning_path.LearningPath
// — uses the SAME aggregate type as the new /v1/me/learning-paths
// endpoints. Per audit-content-fillgaps.md §3.2 the duplicate
// `domain/path` package has been removed in S4.2.
type PathRepo struct {
	mu    sync.RWMutex
	store map[string]*learning_path.LearningPath
}

// NewPathRepo constructs an empty PathRepo.
func NewPathRepo() *PathRepo {
	return &PathRepo{store: make(map[string]*learning_path.LearningPath)}
}

// Save inserts or updates a LearningPath by its PathID.
func (r *PathRepo) Save(p *learning_path.LearningPath) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[p.PathID] = p
}

// Get returns a LearningPath by ID. Excludes soft-deleted records.
func (r *PathRepo) Get(id string) (*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.store[id]
	if !ok || p.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// List returns all non-deleted LearningPaths for a tenant.
func (r *PathRepo) List(tenantID string, limit int) []*learning_path.LearningPath {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*learning_path.LearningPath, 0)
	for _, p := range r.store {
		if p.DeletedAt != nil {
			continue
		}
		if tenantID != "" && p.TenantID != tenantID {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// SessionRepo is an in-memory store for AtomAttempt. Backs the legacy
// /api/sessions router. Internal alias to atom_attempt.AtomAttempt.
// Per audit-content-fillgaps.md §3.2 the duplicate `domain/session`
// package has been removed in S4.2.
type SessionRepo struct {
	mu    sync.RWMutex
	store map[string]*atom_attempt.AtomAttempt
}

// NewSessionRepo constructs an empty SessionRepo.
func NewSessionRepo() *SessionRepo {
	return &SessionRepo{store: make(map[string]*atom_attempt.AtomAttempt)}
}

// Save inserts or updates an AtomAttempt by its SessionID. Implements
// atom_attempt.Repository (CHO-2029) - the in-memory default for unit
// servers; cmd/server swaps the pg adapter at boot.
func (r *SessionRepo) Save(_ context.Context, s *atom_attempt.AtomAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[s.SessionID] = s
	return nil
}

// Get returns a session by ID, excluding soft-deleted.
func (r *SessionRepo) Get(_ context.Context, id string) (*atom_attempt.AtomAttempt, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.store[id]
	if !ok || s.DeletedAt != nil {
		return nil, atom_attempt.ErrNotFound
	}
	return s, nil
}

var _ atom_attempt.Repository = (*SessionRepo)(nil)

// CompanionRepo is an in-memory store keyed by owner GCID + tenant_id (1:1
// per learner per tenant per current rules).
type CompanionRepo struct {
	mu    sync.RWMutex
	store map[string]*companion.Companion // key: tenant_id|gcid
}

// NewCompanionRepo constructs an empty CompanionRepo.
func NewCompanionRepo() *CompanionRepo {
	return &CompanionRepo{store: make(map[string]*companion.Companion)}
}

func companionKey(tenantID, gcid string) string {
	return tenantID + "|" + gcid
}

// Save inserts or updates a Companion.
func (r *CompanionRepo) Save(f *companion.Companion) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[companionKey(f.TenantID, f.OwnerGCID)] = f
}

// GetByGCID returns the Companion belonging to a (tenant, gcid) pair.
func (r *CompanionRepo) GetByGCID(tenantID, gcid string) (*companion.Companion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.store[companionKey(tenantID, gcid)]
	if !ok || f.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return f, nil
}

// AtomCatalogue is the in-memory mock atom seed used by Daily Dose
// composition. 20 atoms across 5 topics per Comic Ch6 P14 spec invariant.
//
// Production (M12+) replaces this with a Pub/Sub-projected AtomProjection
// cache populated from chora.creation.atom.published.v1 (cross-DB-forbidden
// rule: never query chora_creation directly).
type AtomCatalogue struct {
	seeds []companion.AtomSeed
}

// NewAtomCatalogue returns the canonical 20-atom × 5-topic catalogue.
func NewAtomCatalogue() *AtomCatalogue {
	topics := []string{"agile", "scrum", "kanban", "estimation", "retrospective"}
	letters := []string{"a", "b", "c", "d"}
	seeds := make([]companion.AtomSeed, 0, 20)
	for i, topic := range topics {
		for j, letter := range letters {
			seeds = append(seeds, companion.AtomSeed{
				AtomID: "01970000-0000-7000-a000-0000000000" + hex1(i) + hex1(j),
				Topic:  topic,
				Title:  topic + " atom " + letter,
			})
		}
	}
	return &AtomCatalogue{seeds: seeds}
}

func hex1(n int) string {
	const h = "0123456789abcdef"
	if n < 0 || n > 15 {
		return "0"
	}
	return string(h[n])
}

// Seeds returns a defensive copy of the seed list.
func (c *AtomCatalogue) Seeds() []companion.AtomSeed {
	out := make([]companion.AtomSeed, len(c.seeds))
	copy(out, c.seeds)
	return out
}

// Lookup returns the seed for an atom_id, or false.
func (c *AtomCatalogue) Lookup(atomID string) (companion.AtomSeed, bool) {
	for _, s := range c.seeds {
		if s.AtomID == atomID {
			return s, true
		}
	}
	return companion.AtomSeed{}, false
}

// SM2Repo is an in-memory store of per-learner SM-2 state keyed by
// (tenant_id, gcid, atom_id). Plus per-learner seen-topics set.
type SM2Repo struct {
	mu     sync.RWMutex
	states map[string]companion.SM2State // key: tenant|gcid|atom
	seen   map[string]map[string]bool    // key: tenant|gcid → topic-set
}

// NewSM2Repo constructs an empty SM2Repo.
func NewSM2Repo() *SM2Repo {
	return &SM2Repo{
		states: make(map[string]companion.SM2State),
		seen:   make(map[string]map[string]bool),
	}
}

func sm2Key(tenantID, gcid, atomID string) string {
	return tenantID + "|" + gcid + "|" + atomID
}

func learnerKey(tenantID, gcid string) string {
	return tenantID + "|" + gcid
}

// Compile-time check: in-memory adapter satisfies the domain port
// (companion.SM2Store) — same contract as the pg adapter.
var _ companion.SM2Store = (*SM2Repo)(nil)

// SaveState records the SM-2 state for a (tenant, gcid, atom_id).
func (r *SM2Repo) SaveState(_ context.Context, tenantID, gcid string, s companion.SM2State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[sm2Key(tenantID, gcid, s.AtomID)] = s
	return nil
}

// GetState returns the state for a (tenant, gcid, atom_id) or false.
func (r *SM2Repo) GetState(_ context.Context, tenantID, gcid, atomID string) (companion.SM2State, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.states[sm2Key(tenantID, gcid, atomID)]
	return s, ok, nil
}

// AllStatesForLearner returns map[atom_id]SM2State for one learner.
func (r *SM2Repo) AllStatesForLearner(_ context.Context, tenantID, gcid string) (map[string]companion.SM2State, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]companion.SM2State{}
	prefix := learnerKey(tenantID, gcid) + "|"
	for k, v := range r.states {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out[v.AtomID] = v
		}
	}
	return out, nil
}

// MarkTopicSeen records that the learner has engaged with a topic
// (drives curiosity-pick eligibility in Daily Dose composition). Empty
// topics are ignored (real atoms may carry no topic tag).
func (r *SM2Repo) MarkTopicSeen(_ context.Context, tenantID, gcid, topic string) error {
	if strings.TrimSpace(topic) == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := learnerKey(tenantID, gcid)
	if r.seen[k] == nil {
		r.seen[k] = map[string]bool{}
	}
	r.seen[k][topic] = true
	return nil
}

// SeenTopics returns the topic-set for a learner.
func (r *SM2Repo) SeenTopics(_ context.Context, tenantID, gcid string) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.seen[learnerKey(tenantID, gcid)]
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out, nil
}

// ExamPrepGoalRepo is an in-memory store for the per-learner active
// exam-prep goal that drives Daily Dose enrichment (BE-EP1).
//
// MVP only — production (M12) replaces this with a Pub/Sub-projected
// cache populated from chora_delivery's ExamRegistration aggregate
// (chora.delivery.exam_registration.activated.v1). The cross-DB-forbidden
// rule means consumption never reads chora_delivery directly.
type ExamPrepGoalRepo struct {
	mu    sync.RWMutex
	store map[string]*companion.ExamPrepGoal
}

// NewExamPrepGoalRepo constructs an empty ExamPrepGoalRepo.
func NewExamPrepGoalRepo() *ExamPrepGoalRepo {
	return &ExamPrepGoalRepo{store: make(map[string]*companion.ExamPrepGoal)}
}

// Save records (or replaces) the active goal for one learner. A nil goal
// is silently ignored — explicit deletion uses (TODO M12) Delete.
func (r *ExamPrepGoalRepo) Save(tenantID, gcid string, goal *companion.ExamPrepGoal) {
	if goal == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[learnerKey(tenantID, gcid)] = goal
}

// Get returns the active goal for a learner, or (nil, false) when absent.
// (Goal absence is the common case — no inline default.)
func (r *ExamPrepGoalRepo) Get(tenantID, gcid string) (*companion.ExamPrepGoal, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.store[learnerKey(tenantID, gcid)]
	return g, ok
}
