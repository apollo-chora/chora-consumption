// weakness_blob_delete_test.go — the delete-on-analyse / crypto-shred
// defence-in-depth path (ADR-205 WS-5 / CHO-1957). The DEK tombstone is the
// crypto-shred guarantee; deleting the ciphertext object removes the (now
// undecryptable) bytes too. Deleting an absent object is a no-op (idempotent).
package storage

import (
	"context"
	"errors"
	"testing"

	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
)

func storeWithFakeDeleter(del func(ctx context.Context, key string) error) *WeaknessBlobStore {
	return &WeaknessBlobStore{bucket: "b", delete: del}
}

func TestWeaknessBlobStore_DeleteUsesCanonicalKey(t *testing.T) {
	var gotKey string
	s := storeWithFakeDeleter(func(_ context.Context, key string) error { gotKey = key; return nil })
	if err := s.Delete(context.Background(), "tnt-1", "up-9"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if want := WeaknessBlobKey("tnt-1", "up-9"); gotKey != want {
		t.Fatalf("Delete key = %q want %q", gotKey, want)
	}
}

func TestWeaknessBlobStore_DeleteAbsentIsNoOp(t *testing.T) {
	// S3 deletion is idempotent: deleting a missing key succeeds.
	s := storeWithFakeDeleter(func(context.Context, string) error { return nil })
	if err := s.Delete(context.Background(), "tnt-1", "up-9"); err != nil {
		t.Fatalf("deleting an absent object must be a no-op; got %v", err)
	}
}

func TestWeaknessBlobStore_DeleteRealErrorPropagates(t *testing.T) {
	s := storeWithFakeDeleter(func(context.Context, string) error { return errors.New("s3 500") })
	if err := s.Delete(context.Background(), "tnt-1", "up-9"); err == nil {
		t.Fatalf("a real delete error must propagate (fail-loud)")
	}
}

func TestWeaknessBlobStore_DeleteValidatesInput(t *testing.T) {
	s := storeWithFakeDeleter(func(context.Context, string) error { return nil })
	if err := s.Delete(context.Background(), "", "up"); err == nil {
		t.Fatalf("empty tenant must error")
	}
	if err := s.Delete(context.Background(), "tnt", ""); err == nil {
		t.Fatalf("empty upload must error")
	}
}

// The objectstore store satisfies the domain BlobDeleter port.
var _ wb.BlobDeleter = (*WeaknessBlobStore)(nil)
