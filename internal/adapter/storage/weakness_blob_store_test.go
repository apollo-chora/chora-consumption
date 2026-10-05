package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/objectstore"
)

// fakePutCloser captures streamed bytes + simulates a put error.
type fakePutCloser struct {
	buf     bytes.Buffer
	putErr  error
	closed  bool
}

func (f *fakePutCloser) Write(p []byte) (int, error) {
	return f.buf.Write(p)
}

func (f *fakePutCloser) Close() error { f.closed = true; return nil }

// storeWithFakePut builds a store whose put seam returns the supplied fake —
// exercising the streaming path without a live bucket.
func storeWithFakePut(fp *fakePutCloser) *WeaknessBlobStore {
	return &WeaknessBlobStore{
		bucket: "b",
		put: func(_ context.Context, _, _ string, r io.Reader, _ int64) error {
			if fp.putErr != nil {
				return fp.putErr
			}
			_, err := io.Copy(&fp.buf, r)
			return err
		},
	}
}

func TestWeaknessBlobKey(t *testing.T) {
	got := WeaknessBlobKey("tnt-1", "up-9")
	want := "tenants/tnt-1/weakness-uploads/up-9/source"
	if got != want {
		t.Errorf("WeaknessBlobKey = %q want %q", got, want)
	}
}

func TestNewWeaknessBlobStore_EmptyBucket(t *testing.T) {
	_, err := NewWeaknessBlobStore(WeaknessBlobStoreConfig{Bucket: "  "})
	if !errors.Is(err, ErrBlobStoreNotWired) {
		t.Fatalf("err = %v want ErrBlobStoreNotWired", err)
	}
}

func TestNewWeaknessBlobStore_InjectedStore(t *testing.T) {
	// Inject a store so the constructor doesn't need S3 env. objectstore.New
	// performs no network I/O, so a dummy endpoint is fine.
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
	if s.bucket != "b" {
		t.Errorf("bucket=%q", s.bucket)
	}
}

// Upload's input guards run BEFORE any store call, so they're unit-testable
// with a nil store (the streaming path itself is integration-verified vs real
// MinIO, mirroring the objectstore package's own integration tests).
func TestWeaknessBlobStore_Upload_Guards(t *testing.T) {
	s := &WeaknessBlobStore{bucket: "b"}
	ctx := context.Background()
	cases := map[string]UploadReq{
		"missing tenant": {UploadID: "u", Body: strings.NewReader("x")},
		"missing upload": {TenantID: "t", Body: strings.NewReader("x")},
		"nil body":       {TenantID: "t", UploadID: "u"},
		"oversize":       {TenantID: "t", UploadID: "u", Body: strings.NewReader("x"), Size: MaxWeaknessBlobBytes + 1},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Upload(ctx, req); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestWeaknessBlobStore_Upload_StreamsAndReturnsURI(t *testing.T) {
	fp := &fakePutCloser{}
	s := storeWithFakePut(fp)
	uri, err := s.Upload(context.Background(), UploadReq{
		TenantID: "tnt-1", UploadID: "up-9", MIME: "application/pdf",
		Filename: "t.pdf", Body: strings.NewReader("hello-bytes"),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if uri != "s3://b/tenants/tnt-1/weakness-uploads/up-9/source" {
		t.Errorf("uri = %q", uri)
	}
	if fp.buf.String() != "hello-bytes" {
		t.Errorf("streamed=%q", fp.buf.String())
	}
}

func TestWeaknessBlobStore_Upload_PutError(t *testing.T) {
	s := storeWithFakePut(&fakePutCloser{putErr: errors.New("put boom")})
	_, err := s.Upload(context.Background(), UploadReq{
		TenantID: "t", UploadID: "u", Body: strings.NewReader("x"),
	})
	if err == nil {
		t.Fatal("expected put error to surface")
	}
}
