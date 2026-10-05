package storage

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/objectstore"
)

// TestWeaknessBlobStore_ObjectStoreEntryHook covers the production seam itself:
// NewWeaknessBlobStore wires s.put/s.delete to the objectstore-backed closures.
func TestWeaknessBlobStore_ObjectStoreEntryHook(t *testing.T) {
	st, err := objectstore.New(objectstore.Config{
		Endpoint: "http://localhost:9000", Bucket: "b",
		AccessKey: "x", SecretKey: "y", UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("objectstore.New: %v", err)
	}
	s, err := NewWeaknessBlobStore(WeaknessBlobStoreConfig{Bucket: "b", Store: st})
	if err != nil {
		t.Fatalf("NewWeaknessBlobStore: %v", err)
	}
	if s.put == nil || s.delete == nil {
		t.Fatal("constructor must wire both objectstore seams")
	}
}

// TestWeaknessBlobStore_DeleteNilDeleter — Delete with a nil delete seam
// fails loud rather than panic.
func TestWeaknessBlobStore_DeleteNilDeleter(t *testing.T) {
	s := &WeaknessBlobStore{bucket: "b"}
	if err := s.Delete(context.Background(), "tnt", "up"); err == nil {
		t.Fatal("expected error when deleter seam is nil")
	}
}

// TestWeaknessBlobStore_Upload_StreamedOversize — a body that streams past the
// cap (even when the declared Size is small) must fail loud.
func TestWeaknessBlobStore_Upload_StreamedOversize(t *testing.T) {
	var closed bool
	fp := &fakePutCloser{}
	s := &WeaknessBlobStore{
		bucket: "b",
		put: func(_ context.Context, _, _ string, r io.Reader, _ int64) error {
			_, err := io.Copy(&fp.buf, r)
			return err
		},
	}
	body := strings.NewReader(strings.Repeat("x", int(MaxWeaknessBlobBytes+1)))
	_, err := s.Upload(context.Background(), UploadReq{
		TenantID: "t", UploadID: "u", Body: body,
	})
	if err == nil {
		t.Fatal("expected streamed-oversize error")
	}
	_ = closed
}
