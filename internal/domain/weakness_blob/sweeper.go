package weakness_blob

import (
	"context"
	"fmt"
	"time"
)

// defaultSweepLimit caps one sweep pass so an opportunistic call (piggybacked on
// a diagnose-complete event) stays bounded.
const defaultSweepLimit = 200

// Sweeper is the TTL backstop: it crypto-shreds any blob whose diagnosis never
// completed (the shred-on-diagnose path never fired) once it is older than ttl.
//
// RLS-correct + cross-tenant-safe: SweepExpired operates on the CURRENT tenant
// only (the store scopes by the ctx's chora.tenant_id), so it needs no
// cross-tenant scan and adds NO RLS-bypass surface. The primary shred is still
// the per-upload shred-on-diagnose; this is the safety net for stuck blobs.
type Sweeper struct {
	store    WrappedDEKStore
	shredder *Shredder
	ttl      time.Duration
	limit    int
	now      func() time.Time
}

// NewSweeper constructs the sweeper. ttl is the max age an un-shredded blob may
// reach before the backstop shreds it (the diagnose path should have shredded it
// long before).
func NewSweeper(store WrappedDEKStore, shredder *Shredder, ttl time.Duration) *Sweeper {
	return &Sweeper{store: store, shredder: shredder, ttl: ttl, limit: defaultSweepLimit, now: time.Now}
}

// SweepExpired shreds every un-shredded blob older than now-ttl for the current
// tenant. Returns the count shredded. Stops + returns on the first shred error
// (the caller logs; a later pass retries — shred is idempotent).
func (s *Sweeper) SweepExpired(ctx context.Context) (int, error) {
	if s == nil || s.store == nil || s.shredder == nil {
		return 0, fmt.Errorf("weakness_blob: sweeper not configured")
	}
	cutoff := s.now().UTC().Add(-s.ttl)
	expired, err := s.store.ListExpiredUnshredded(ctx, cutoff, s.limit)
	if err != nil {
		return 0, fmt.Errorf("weakness_blob: list expired: %w", err)
	}
	shredded := 0
	for _, rec := range expired {
		if err := s.shredder.Shred(ctx, rec.TenantID, rec.LearnerGCID, rec.UploadID); err != nil {
			return shredded, fmt.Errorf("weakness_blob: sweep shred (upload=%s): %w", rec.UploadID, err)
		}
		shredded++
	}
	return shredded, nil
}
