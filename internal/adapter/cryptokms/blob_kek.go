// Package cryptokms implements the production KEKClient adapter for
// the Growth-Edge per-blob envelope crypto-shred (ADR-205 D8 / ADR-186 /
// CHO-1957 WS-5).
//
// Each raw upload (weakness blob) is encrypted with a fresh random AES-256 DEK.
// This adapter wraps / unwraps that DEK under a master KEK so the ONLY persistent
// form is the wrapped ciphertext — crypto-shred = delete the wrapped row.
// Mirrors the pattern in chora-identity/internal/adapter/cryptolocal but keyed
// PER BLOB rather than per user (deletion granularity = one diagnosis-complete
// event vs account closure).
//
// The master KEK is provided by the environment (base64 of a 32-byte key) —
// the cloud-neutral replacement for Cloud KMS. Envelope encryption is NEVER
// silently disabled: a missing or malformed KEK is a hard error at construction
// time, and every wrap/unwrap runs against the real AEAD.
//
// No cloud types cross the package boundary: the domain (weakness_blob) depends
// only on its KEKClient port.
//
// Hexagonal: this adapter knows about the domain (imports KEKClient for the
// compile-time check) but the domain never imports this adapter.
package cryptokms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
)

// kekEnv is the default environment variable carrying the base64-encoded
// 32-byte master KEK. The constructor's resource argument names the env var
// to read; when empty, kekEnv is used.
const kekEnv = "CHORA_BLOB_KEK"

// kekVersion is the local KEK's version label, returned alongside every wrap
// for audit + future rotation. There is no KMS key-version counter; the label
// is a constant so the persisted form never lies about its provenance.
const kekVersion = "local-kek-v1"

// kekBytes is the required master-KEK length (AES-256).
const kekBytes = 32

// BlobKEK wraps / unwraps per-blob DEKs via AES-256-GCM under the env-provided
// master KEK. It satisfies weakness_blob.KEKClient; no cloud types leak
// through the interface.
type BlobKEK struct {
	kek cipher.AEAD
}

// NewBlobKEK builds a BlobKEK from the base64 32-byte master KEK carried by
// the env var named by resource (default CHORA_BLOB_KEK). A missing or
// malformed KEK is a hard error (fail-loud — never a silent plaintext path).
func NewBlobKEK(_ context.Context, resource string) (*BlobKEK, error) {
	name := strings.TrimSpace(resource)
	if name == "" {
		name = kekEnv
	}
	kekB64 := strings.TrimSpace(os.Getenv(name))
	if kekB64 == "" {
		return nil, fmt.Errorf("cryptokms: %s is required (base64 of a 32-byte AES-256 KEK); refusing to run without envelope encryption", name)
	}
	raw, err := base64.StdEncoding.DecodeString(kekB64)
	if err != nil {
		return nil, fmt.Errorf("cryptokms: %s is not valid base64: %w", name, err)
	}
	return newBlobKEKWithKEK(raw)
}

// newBlobKEKWithKEK constructs a BlobKEK over the raw 32-byte KEK. Used by
// tests to inject a key without the environment.
func newBlobKEKWithKEK(raw []byte) (*BlobKEK, error) {
	if len(raw) != kekBytes {
		return nil, fmt.Errorf("cryptokms: KEK must be %d bytes, got %d", kekBytes, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("cryptokms: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptokms: cipher.NewGCM: %w", err)
	}
	return &BlobKEK{kek: gcm}, nil
}

// WrapDEK encrypts plaintextDEK under the master KEK (AAD-bound). Returns the
// wrapped DEK bytes (nonce || ciphertext || tag) and the KEK version label.
// Fails LOUD on any error — never silently stores an un-wrapped DEK.
func (b *BlobKEK) WrapDEK(_ context.Context, plaintextDEK, aad []byte) ([]byte, string, error) {
	if b == nil || b.kek == nil {
		return nil, "", errors.New("cryptokms: WrapDEK: KEK not initialised")
	}
	nonce := make([]byte, 12) // AES-GCM standard nonce
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("cryptokms: WrapDEK: nonce: %w", err)
	}
	wrapped := b.kek.Seal(nonce, nonce, plaintextDEK, aad)
	return wrapped, kekVersion, nil
}

// UnwrapDEK decrypts the wrapped DEK. The AAD must exactly match the value used
// at wrap time; AES-GCM returns an authentication error otherwise. Fails LOUD on
// any error.
func (b *BlobKEK) UnwrapDEK(_ context.Context, wrapped, aad []byte) ([]byte, error) {
	if b == nil || b.kek == nil {
		return nil, errors.New("cryptokms: UnwrapDEK: KEK not initialised")
	}
	if len(wrapped) < 12 {
		return nil, errors.New("cryptokms: UnwrapDEK: wrapped DEK shorter than the nonce")
	}
	nonce, ct := wrapped[:12], wrapped[12:]
	plain, err := b.kek.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("cryptokms: UnwrapDEK: %w", err)
	}
	return plain, nil
}

// compile-time check: BlobKEK satisfies the KEKClient port.
var _ wb.KEKClient = (*BlobKEK)(nil)
