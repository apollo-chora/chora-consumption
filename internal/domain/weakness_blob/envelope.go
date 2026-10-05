// Package weakness_blob is the per-blob envelope crypto-shred domain for the
// Growth-Edge raw upload (ADR-205 D8 / ADR-186 / CHO-1957 WS-5).
//
// The raw upload (marked papers / notes / scribbles / a grounding textbook) is
// the highest-PII/IP input in the learning loop. The aggregate persists ONLY the
// distilled Descriptor; the raw blob is transient. To make "transient" a real
// crypto guarantee (GDPR Art.17 / PDPA "destroyed") rather than a best-effort
// GCS delete, each blob is envelope-encrypted:
//
//   - a RANDOM per-blob DEK (AES-256-GCM) encrypts the blob bytes (→ ciphertext
//     stored in GCS),
//   - the DEK is WRAPPED by a Cloud KMS master KEK and ONLY the wrapped form is
//     persisted (in chora_consumption, NOT in GCS),
//   - the plaintext DEK is held in memory for one operation and then zeroed.
//
// Crypto-shred = DELETE the wrapped DEK. The plaintext DEK was never stored, so
// every ciphertext copy under that DEK (including GCS backups) becomes
// permanently undecryptable. Mirrors chora-identity's cryptokms.CloudKMSKeyManager
// (ADR-186) but keyed PER BLOB (deletable on a single diagnosis-complete event),
// not per user — the per-user DEK is the account-closure granularity.
//
// This file is pure Go (std-lib crypto only). The Cloud KMS KEKClient is an
// adapter (internal/adapter/cryptokms); the wrapped-DEK store + GCS deleter are
// adapters too. Hexagonal: the domain depends only on the ports below.
package weakness_blob

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const (
	dekBytes   = 32 // AES-256 DEK
	nonceBytes = 12 // AES-GCM standard nonce
)

// KEKClient wraps/unwraps a per-blob DEK with the master KEK. The AAD binds each
// wrap to its blob context (tenant|gcid|upload), so a wrapped DEK can only be
// unwrapped under the matching context — the KEK returns an authentication
// error otherwise, isolating blobs even on a shared KEK. No cloud-SDK types
// leak through this port; the adapter hides them (so the domain + envelope
// stay cloud-neutral + testable).
type KEKClient interface {
	// WrapDEK encrypts the plaintext DEK under the master KEK (AAD-bound). The
	// returned kekVersion is the KMS key-version name (for re-wrap on rotation).
	WrapDEK(ctx context.Context, plaintextDEK, aad []byte) (wrapped []byte, kekVersion string, err error)
	// UnwrapDEK reverses WrapDEK. Returns an error when the AAD does not match the
	// wrap context or the KEK is unavailable.
	UnwrapDEK(ctx context.Context, wrapped, aad []byte) (plaintextDEK []byte, err error)
}

// SealedBlob is the envelope-encrypted result of sealing one raw blob: the
// wrapped per-blob DEK (persisted in pg — deleting it crypto-shreds the blob),
// the KEK version, and the AES-256-GCM ciphertext (stored in GCS).
type SealedBlob struct {
	WrappedDEK []byte
	KEKVersion string
	Ciphertext []byte
}

// Envelope seals/opens raw blob bytes with a fresh random per-blob DEK wrapped by
// the KEKClient. The plaintext DEK never persists.
type Envelope struct {
	kek KEKClient
	rng io.Reader // overridable in tests; defaults to crypto/rand
}

// NewEnvelope constructs the envelope cipher over a KEKClient.
func NewEnvelope(kek KEKClient) *Envelope {
	return &Envelope{kek: kek, rng: rand.Reader}
}

// Seal encrypts plaintext with a fresh random DEK and wraps the DEK with the KEK.
// Layout of Ciphertext: nonce(12) || aesgcm(ciphertext||tag). Fails LOUD on any
// rng / cipher / KEK error — the caller MUST NOT store an unencrypted blob.
func (e *Envelope) Seal(ctx context.Context, aad, plaintext []byte) (SealedBlob, error) {
	if e == nil || e.kek == nil {
		return SealedBlob{}, errors.New("weakness_blob: envelope not configured (nil KEK)")
	}
	dek := make([]byte, dekBytes)
	if _, err := io.ReadFull(e.rng, dek); err != nil {
		return SealedBlob{}, fmt.Errorf("weakness_blob: dek gen: %w", err)
	}
	defer zero(dek)

	gcm, err := newGCM(dek)
	if err != nil {
		return SealedBlob{}, err
	}
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(e.rng, nonce); err != nil {
		return SealedBlob{}, fmt.Errorf("weakness_blob: nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, aad)

	wrapped, kekVersion, err := e.kek.WrapDEK(ctx, dek, aad)
	if err != nil {
		return SealedBlob{}, fmt.Errorf("weakness_blob: kek wrap: %w", err)
	}
	if len(wrapped) == 0 {
		return SealedBlob{}, errors.New("weakness_blob: kek returned an empty wrapped DEK (refusing — would be unrecoverable + unshreddable)")
	}
	return SealedBlob{WrappedDEK: wrapped, KEKVersion: kekVersion, Ciphertext: ciphertext}, nil
}

// Open reverses Seal: unwrap the DEK (AAD-bound), then AES-256-GCM open. Fails
// LOUD on any KEK / cipher / tag-verification error.
func (e *Envelope) Open(ctx context.Context, aad, wrapped, ciphertext []byte) ([]byte, error) {
	if e == nil || e.kek == nil {
		return nil, errors.New("weakness_blob: envelope not configured (nil KEK)")
	}
	if len(ciphertext) < nonceBytes {
		return nil, errors.New("weakness_blob: ciphertext shorter than nonce")
	}
	dek, err := e.kek.UnwrapDEK(ctx, wrapped, aad)
	if err != nil {
		return nil, fmt.Errorf("weakness_blob: kek unwrap: %w", err)
	}
	defer zero(dek)

	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce, body := ciphertext[:nonceBytes], ciphertext[nonceBytes:]
	plain, err := gcm.Open(nil, nonce, body, aad)
	if err != nil {
		return nil, fmt.Errorf("weakness_blob: gcm open: %w", err)
	}
	return plain, nil
}

func newGCM(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("weakness_blob: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("weakness_blob: gcm: %w", err)
	}
	return gcm, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
