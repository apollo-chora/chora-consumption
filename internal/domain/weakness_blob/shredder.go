package weakness_blob

import (
	"context"
	"fmt"
	"time"
)

// BlobDeleter deletes the GCS ciphertext object for one blob. Deleting an absent
// object MUST be a no-op (idempotent). This is defence-in-depth — the DEK
// tombstone (below) is the crypto-shred guarantee; the GCS delete just removes
// the (now-undecryptable) ciphertext bytes too.
type BlobDeleter interface {
	Delete(ctx context.Context, tenantID, uploadID string) error
}

// Shredder crypto-shreds one blob: (1) tombstone the wrapped DEK — the GUARANTEE,
// the plaintext was never stored so every ciphertext copy is now undecryptable —
// then (2) best-effort delete the GCS ciphertext object. Used by both the
// shred-on-diagnose path (subscriber) and the TTL sweeper.
type Shredder struct {
	store WrappedDEKStore
	blobs BlobDeleter      // optional — nil skips the GCS delete (DEK tombstone still shreds)
	now   func() time.Time // overridable in tests
}

// NewShredder constructs the shredder. blobs may be nil (the DEK tombstone is the
// shred guarantee; the GCS delete is defence-in-depth).
func NewShredder(store WrappedDEKStore, blobs BlobDeleter) *Shredder {
	return &Shredder{store: store, blobs: blobs, now: time.Now}
}

// Shred crypto-shreds the blob for uploadID. Fail-loud: a DEK-tombstone failure
// (the guarantee) returns an error; the GCS delete runs after and its error is
// also returned (the caller can NACK to retry — both steps are idempotent). The
// LearnerWeakness aggregate row is NEVER touched here (soft-delete + pseudonymise
// invariant) — only the raw-blob bytes are shredded.
func (s *Shredder) Shred(ctx context.Context, tenantID, gcid, uploadID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("weakness_blob: shredder not configured")
	}
	// 1. Tombstone the wrapped DEK — the crypto-shred guarantee.
	if err := s.store.Shred(ctx, uploadID, s.now().UTC()); err != nil {
		return fmt.Errorf("weakness_blob: shred dek (upload=%s): %w", uploadID, err)
	}
	// 2. Defence-in-depth: delete the GCS ciphertext object (idempotent).
	if s.blobs != nil {
		if err := s.blobs.Delete(ctx, tenantID, uploadID); err != nil {
			return fmt.Errorf("weakness_blob: delete ciphertext object (upload=%s): %w", uploadID, err)
		}
	}
	return nil
}
