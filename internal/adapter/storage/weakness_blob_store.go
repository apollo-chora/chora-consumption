// Package storage holds chora-consumption's blob-storage adapters.
//
// weakness_blob_store.go — S3-compatible (MinIO) uploader for the Growth-Edge
// weakness-doc upload path (Epic-1b W8b). The cloud-neutral replacement for
// the retired GCS adapter: static credentials from the environment
// (S3_ENDPOINT / S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY via
// chora-common/objectstore), 32 MiB cap, streaming copy, s3:// URI return. The
// blob is transient — the ai-kernel analyser downloads it once, then it is
// cold-archived / discarded; only the distilled Growth Edge persists.
//
// The BlobUploader port + UploadReq live here (not in the http package) so the
// adapter never imports http — http imports storage for the port type, cmd/server
// wires the concrete adapter.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/apollo-chora/chora-common/objectstore"
)

// MaxWeaknessBlobBytes is the 32 MiB upload cap (matches the OpenAPI 413).
const MaxWeaknessBlobBytes int64 = 32 * 1024 * 1024

// ErrBlobStoreNotWired is returned when the bucket env is unset so cmd/server
// can log "NOT wired" + the handler returns 503 (fail-loud, no stub).
var ErrBlobStoreNotWired = errors.New("weakness blob store not wired (bucket unset)")

// UploadReq is one weakness-doc upload to store.
type UploadReq struct {
	TenantID string
	UploadID string
	MIME     string
	Filename string
	Size     int64
	Body     io.Reader
}

// BlobUploader stores a weakness-doc blob and returns its s3:// URI. The http
// handler depends on this port; the objectstore adapter below satisfies it.
type BlobUploader interface {
	Upload(ctx context.Context, req UploadReq) (s3URI string, err error)
}

// WeaknessBlobStore implements BlobUploader against an S3-compatible object
// store (MinIO in the local stack).
type WeaknessBlobStore struct {
	bucket string
	store  *objectstore.Store
	// put is the seam that writes the object. Defaults to the objectstore
	// Store.Put; tests inject a fake to exercise the streaming path without a
	// live bucket.
	put func(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	// delete is the seam that deletes an object by key. Defaults to the
	// objectstore Store.Delete; tests inject a fake.
	delete func(ctx context.Context, key string) error
}

// WeaknessBlobStoreConfig is the constructor input.
type WeaknessBlobStoreConfig struct {
	// Bucket is the object-store bucket name (no s3:// prefix), from env
	// WEAKNESS_UPLOADS_BUCKET via main.go. Empty ⇒ ErrBlobStoreNotWired.
	Bucket string
	// Store is injectable for tests; nil ⇒ NewWeaknessBlobStore builds one
	// from the S3_* environment via chora-common/objectstore.
	Store *objectstore.Store
}

// NewWeaknessBlobStore constructs an S3-compatible uploader. Returns
// ErrBlobStoreNotWired when the bucket is empty.
func NewWeaknessBlobStore(cfg WeaknessBlobStoreConfig) (*WeaknessBlobStore, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, ErrBlobStoreNotWired
	}
	store := cfg.Store
	if store == nil {
		s3cfg := objectstore.ConfigFromEnv()
		s3cfg.Bucket = strings.TrimSpace(cfg.Bucket)
		s, err := objectstore.New(s3cfg)
		if err != nil {
			return nil, fmt.Errorf("objectstore.New: %w", err)
		}
		store = s
	}
	s := &WeaknessBlobStore{bucket: strings.TrimSpace(cfg.Bucket), store: store}
	s.put = s.objectStorePut
	s.delete = s.objectStoreDelete
	return s, nil
}

// objectStorePut is the production write path — the objectstore Put with the
// content-type set.
func (s *WeaknessBlobStore) objectStorePut(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	return s.store.Put(ctx, key, r, size, contentType)
}

// objectStoreDelete is the production delete path. S3 deletion is idempotent,
// so an absent object is a no-op.
func (s *WeaknessBlobStore) objectStoreDelete(ctx context.Context, key string) error {
	return s.store.Delete(ctx, key)
}

// Delete removes the stored blob object for (tenantID, uploadID). An absent
// object is a no-op (idempotent) — this backs both delete-on-analyse and the
// crypto-shred defence-in-depth path (ADR-205 WS-5). The DEK tombstone is the
// real crypto-shred guarantee; this just removes the (now undecryptable)
// ciphertext bytes. A real transport error fails loud (the caller NACKs/retries).
func (s *WeaknessBlobStore) Delete(ctx context.Context, tenantID, uploadID string) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(uploadID) == "" {
		return errors.New("weakness blob: tenant_id + upload_id required")
	}
	if s.delete == nil {
		return errors.New("weakness blob: deleter not wired")
	}
	if err := s.delete(ctx, WeaknessBlobKey(tenantID, uploadID)); err != nil {
		return fmt.Errorf("weakness blob: delete: %w", err)
	}
	return nil
}

var _ BlobUploader = (*WeaknessBlobStore)(nil)

// Upload streams the body to s3://{bucket}/{WeaknessBlobKey} and returns the URI.
func (s *WeaknessBlobStore) Upload(ctx context.Context, req UploadReq) (string, error) {
	if strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.UploadID) == "" {
		return "", errors.New("weakness blob: tenant_id + upload_id required")
	}
	if req.Body == nil {
		return "", errors.New("weakness blob: body nil")
	}
	if req.Size > MaxWeaknessBlobBytes {
		return "", fmt.Errorf("weakness blob: size %d > max %d", req.Size, MaxWeaknessBlobBytes)
	}
	key := WeaknessBlobKey(req.TenantID, req.UploadID)
	limit := io.LimitReader(req.Body, MaxWeaknessBlobBytes+1)
	counter := &countingReader{r: limit}
	if err := s.put(ctx, key, req.MIME, counter, req.Size); err != nil {
		return "", fmt.Errorf("weakness blob: put: %w", err)
	}
	if counter.n > MaxWeaknessBlobBytes {
		return "", fmt.Errorf("weakness blob: size %d > max %d (streamed)", counter.n, MaxWeaknessBlobBytes)
	}
	return weaknessBlobURI(s.bucket, key), nil
}

// countingReader counts the bytes pulled from a reader so Upload can enforce
// the streamed-size cap even when the declared Size is small.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// WeaknessBlobKey is the canonical object key for a weakness upload.
func WeaknessBlobKey(tenantID, uploadID string) string {
	return "tenants/" + tenantID + "/weakness-uploads/" + uploadID + "/source"
}

func weaknessBlobURI(bucket, key string) string { return "s3://" + bucket + "/" + key }
