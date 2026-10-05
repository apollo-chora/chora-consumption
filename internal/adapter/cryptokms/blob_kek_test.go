// Tests for the production local-KEK BlobKEK adapter (ADR-205 D8 / CHO-1957
// WS-5): the per-blob DEK wrapping layer that satisfies the
// weakness_blob.KEKClient port.
//
// Uses an internal test package so the unexported newBlobKEKWithKEK
// constructor seam is accessible without exporting implementation details.
package cryptokms

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func testKEK(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func newTestBlobKEK(t *testing.T) *BlobKEK {
	t.Helper()
	kek, err := newBlobKEKWithKEK(testKEK(7))
	if err != nil {
		t.Fatalf("newBlobKEKWithKEK: %v", err)
	}
	return kek
}

// TestWrapUnwrap_Roundtrip verifies that a DEK wrapped then unwrapped with the
// same AAD is identical to the original plaintext DEK.
func TestWrapUnwrap_Roundtrip(t *testing.T) {
	kek := newTestBlobKEK(t)
	ctx := context.Background()

	dek := []byte("32-byte-test-dek-AAAAAAAAAAAAAAAA")
	aad := []byte("tenant|gcid|upload-001")

	wrapped, kekVersion, err := kek.WrapDEK(ctx, dek, aad)
	if err != nil {
		t.Fatalf("WrapDEK error: %v", err)
	}
	if len(wrapped) == 0 {
		t.Fatal("expected non-empty wrapped DEK")
	}
	if kekVersion == "" {
		t.Fatal("expected non-empty kekVersion")
	}

	got, err := kek.UnwrapDEK(ctx, wrapped, aad)
	if err != nil {
		t.Fatalf("UnwrapDEK error: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", got, dek)
	}
}

// TestWrapDEK_ReturnsKEKVersion verifies that the kekVersion returned by WrapDEK
// is the local KEK's version label.
func TestWrapDEK_ReturnsKEKVersion(t *testing.T) {
	kek := newTestBlobKEK(t)
	ctx := context.Background()

	_, kekVersion, err := kek.WrapDEK(ctx, []byte("dek"), []byte("aad"))
	if err != nil {
		t.Fatalf("WrapDEK error: %v", err)
	}
	if kekVersion != kekVersion {
		t.Fatalf("kekVersion %q, want %q", kekVersion, kekVersion)
	}
}

// TestUnwrapDEK_AADMismatch_Errors verifies that unwrapping with a different AAD
// returns an error (AES-GCM authentication failure).
func TestUnwrapDEK_AADMismatch_Errors(t *testing.T) {
	kek := newTestBlobKEK(t)
	ctx := context.Background()

	dek := []byte("plaintext-dek")
	aadWrap := []byte("tenant|gcid|upload-A")
	aadOpen := []byte("tenant|gcid|upload-B") // different — mismatch

	wrapped, _, err := kek.WrapDEK(ctx, dek, aadWrap)
	if err != nil {
		t.Fatalf("WrapDEK error: %v", err)
	}

	_, err = kek.UnwrapDEK(ctx, wrapped, aadOpen)
	if err == nil {
		t.Fatal("expected an error on AAD mismatch, got nil")
	}
	// error must propagate with context
	if !strings.Contains(err.Error(), "UnwrapDEK") {
		t.Fatalf("error %q missing 'UnwrapDEK' context", err.Error())
	}
}

// TestUnwrapDEK_UnknownCiphertext_Errors verifies fail-loud behaviour on
// decrypt of a wrapped form this KEK did not produce.
func TestUnwrapDEK_UnknownCiphertext_Errors(t *testing.T) {
	kek := newTestBlobKEK(t)
	ctx := context.Background()

	_, err := kek.UnwrapDEK(ctx, []byte("unknown-ciphertext"), []byte("aad"))
	if err == nil {
		t.Fatal("expected error from decrypt failure, got nil")
	}
	if !strings.Contains(err.Error(), "UnwrapDEK") {
		t.Fatalf("error %q missing 'UnwrapDEK' context", err.Error())
	}
}

// TestNewBlobKEK_MissingKEK verifies that a missing env KEK is a hard error
// (fail-loud on boot — never a silent plaintext path).
func TestNewBlobKEK_MissingKEK(t *testing.T) {
	t.Setenv("CHORA_BLOB_KEK_TEST_MISSING", "")
	_, err := NewBlobKEK(context.Background(), "CHORA_BLOB_KEK_TEST_MISSING")
	if err == nil {
		t.Fatal("expected error from missing KEK, got nil")
	}
	if !strings.Contains(err.Error(), "CHORA_BLOB_KEK_TEST_MISSING") {
		t.Fatalf("error %q missing the env var name", err.Error())
	}
}

// TestNewBlobKEK_MalformedKEK verifies that a non-base64 / wrong-length KEK is
// a hard error.
func TestNewBlobKEK_MalformedKEK(t *testing.T) {
	t.Setenv("CHORA_BLOB_KEK_TEST_BAD", "not-base64!!")
	if _, err := NewBlobKEK(context.Background(), "CHORA_BLOB_KEK_TEST_BAD"); err == nil {
		t.Fatal("expected error from malformed KEK, got nil")
	}
	t.Setenv("CHORA_BLOB_KEK_TEST_SHORT", base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := NewBlobKEK(context.Background(), "CHORA_BLOB_KEK_TEST_SHORT"); err == nil {
		t.Fatal("expected error from short KEK, got nil")
	}
}

// TestNewBlobKEK_FromEnv builds a real BlobKEK from the environment and
// verifies wrap works.
func TestNewBlobKEK_FromEnv(t *testing.T) {
	t.Setenv("CHORA_BLOB_KEK_TEST_GOOD", base64.StdEncoding.EncodeToString(testKEK(9)))
	kek, err := NewBlobKEK(context.Background(), "CHORA_BLOB_KEK_TEST_GOOD")
	if err != nil {
		t.Fatalf("NewBlobKEK error: %v", err)
	}
	wrapped, _, err := kek.WrapDEK(context.Background(), []byte("dek"), []byte("aad"))
	if err != nil || len(wrapped) == 0 {
		t.Fatalf("WrapDEK after NewBlobKEK: wrapped=%v err=%v", wrapped, err)
	}
}

// TestWrapUnwrap_MultipleBlobs verifies per-blob isolation: different AADs
// produce independent wrapped DEKs, and cross-unwrapping fails.
func TestWrapUnwrap_MultipleBlobs(t *testing.T) {
	kek := newTestBlobKEK(t)
	ctx := context.Background()

	dekA := []byte("dek-for-blob-A")
	dekB := []byte("dek-for-blob-B")
	aadA := []byte("tenant|gcid|upload-A")
	aadB := []byte("tenant|gcid|upload-B")

	wrappedA, _, err := kek.WrapDEK(ctx, dekA, aadA)
	if err != nil {
		t.Fatalf("WrapDEK A error: %v", err)
	}
	wrappedB, _, err := kek.WrapDEK(ctx, dekB, aadB)
	if err != nil {
		t.Fatalf("WrapDEK B error: %v", err)
	}

	// Correct roundtrip for each.
	gotA, err := kek.UnwrapDEK(ctx, wrappedA, aadA)
	if err != nil || string(gotA) != string(dekA) {
		t.Fatalf("blob A roundtrip: got=%q err=%v", gotA, err)
	}
	gotB, err := kek.UnwrapDEK(ctx, wrappedB, aadB)
	if err != nil || string(gotB) != string(dekB) {
		t.Fatalf("blob B roundtrip: got=%q err=%v", gotB, err)
	}

	// Cross-unwrap must fail (AAD isolation).
	if _, err := kek.UnwrapDEK(ctx, wrappedA, aadB); err == nil {
		t.Fatal("expected error cross-unwrapping A with B's AAD")
	}
}
