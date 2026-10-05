package weakness_blob

import (
	"context"
	"sync"
	"time"
)

// WrappedDEK is one persisted wrapped-DEK record, keyed by upload_id (1:1 with a
// Growth-Edge upload). Wrapped is the KEK-wrapped per-blob DEK ciphertext; the
// plaintext DEK is NEVER stored. Deleted=true + empty Wrapped is the crypto-shred
// tombstone (soft-delete + scrub — NEVER a hard row delete, per ddd-enforcement).
type WrappedDEK struct {
	UploadID    string
	TenantID    string
	LearnerGCID string
	Wrapped     []byte
	KEKVersion  string
	CreatedAt   time.Time
	Deleted     bool
}

// WrappedDEKStore persists + tombstones per-blob wrapped DEKs. Production wires
// the pg adapter (chora_consumption.weakness_blob_dek_wrap, RLS-scoped); tests
// use the in-memory impl. Every method is tenant-scoped via RLS in the adapter
// (tenant rides the ctx) — cross-DB queries forbidden, chora_consumption only.
type WrappedDEKStore interface {
	// Put persists a freshly wrapped DEK. Idempotent — a re-put for the same
	// upload_id is a no-op (protects an in-flight blob from a double-seal).
	Put(ctx context.Context, rec WrappedDEK) error
	// Get returns the record (alive OR tombstoned) or (nil, nil) when absent.
	Get(ctx context.Context, uploadID string) (*WrappedDEK, error)
	// Shred tombstones the row (deleted_at = now) + scrubs the wrapped bytes —
	// crypto-shred. Idempotent: absent / already-shredded is a no-op (never errors).
	Shred(ctx context.Context, uploadID string, now time.Time) error
	// ListExpiredUnshredded returns live (un-shredded) records created before
	// olderThan, capped at limit — the TTL-sweep candidates for the CURRENT tenant
	// (the adapter scopes by RLS; no cross-tenant scan, no RLS-bypass).
	ListExpiredUnshredded(ctx context.Context, olderThan time.Time, limit int) ([]WrappedDEK, error)
}

// InMemoryWrappedDEKStore is the test / local-dev store. NOT durable — production
// wires the pg adapter.
type InMemoryWrappedDEKStore struct {
	mu sync.Mutex
	m  map[string]*WrappedDEK
}

// NewInMemoryWrappedDEKStore constructs an empty in-memory store.
func NewInMemoryWrappedDEKStore() *InMemoryWrappedDEKStore {
	return &InMemoryWrappedDEKStore{m: map[string]*WrappedDEK{}}
}

func (s *InMemoryWrappedDEKStore) Put(_ context.Context, rec WrappedDEK) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[rec.UploadID]; ok {
		return nil // idempotent
	}
	cp := rec
	cp.Wrapped = append([]byte(nil), rec.Wrapped...)
	s.m[rec.UploadID] = &cp
	return nil
}

func (s *InMemoryWrappedDEKStore) Get(_ context.Context, uploadID string) (*WrappedDEK, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.m[uploadID]
	if !ok {
		return nil, nil
	}
	cp := *rec
	cp.Wrapped = append([]byte(nil), rec.Wrapped...)
	return &cp, nil
}

func (s *InMemoryWrappedDEKStore) Shred(_ context.Context, uploadID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.m[uploadID]
	if !ok || rec.Deleted {
		return nil // idempotent
	}
	rec.Wrapped = nil
	rec.Deleted = true
	return nil
}

func (s *InMemoryWrappedDEKStore) ListExpiredUnshredded(_ context.Context, olderThan time.Time, limit int) ([]WrappedDEK, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WrappedDEK, 0)
	for _, rec := range s.m {
		if rec.Deleted || !rec.CreatedAt.Before(olderThan) {
			continue
		}
		cp := *rec
		cp.Wrapped = append([]byte(nil), rec.Wrapped...)
		out = append(out, cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

var _ WrappedDEKStore = (*InMemoryWrappedDEKStore)(nil)
