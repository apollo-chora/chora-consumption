// weakness_blob_test.go — RED→GREEN for the per-blob envelope crypto-shred
// domain (ADR-205 D8 / ADR-186 / CHO-1957 WS-5).
//
// The Growth-Edge raw upload (marked papers / notes / scribbles / a grounding
// textbook) is the highest-PII/IP input in the learning loop. ADR-205 D8 +
// ADR-186 mandate: persist only the distilled Descriptor, never the raw blob;
// envelope-encrypt the blob with a RANDOM per-blob DEK wrapped by a master KEK;
// crypto-shred on diagnosis-complete by DELETING the wrapped DEK (the plaintext
// DEK never persists, so the ciphertext becomes permanently undecryptable); a
// TTL backstop shreds any blob whose diagnosis never completes.
//
// This file pins the PURE domain (no cloud SDK): the AES-256-GCM envelope (Seal/Open),
// the AAD binding, the WrappedDEKStore (in-mem), the Shredder, and the per-tenant
// TTL Sweeper. The KEKClient + pg store + S3 deleter are adapters
// tested separately.
package weakness_blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

const (
	tTenant = "01970000-0000-7000-8000-0000000000aa"
	tGCID   = "01970000-0000-7000-9000-0000000000bb"
	tUpload = "01970000-0000-7000-a000-0000000000cc"
)

// ---------------------------------------------------------------------------
// fakeKEK — a deterministic, AAD-enforcing KEKClient stand-in. The wrapped form
// is sha256(aad)||dek, so Unwrap with the wrong AAD fails (mirrors Cloud KMS
// returning INVALID_ARGUMENT on an AAD mismatch). The real AES-256-GCM envelope
// runs for real on top of this; only the KEK wrap/unwrap is faked.
// ---------------------------------------------------------------------------
type fakeKEK struct {
	failWrap   bool
	failUnwrap bool
	wraps      int
}

func (f *fakeKEK) WrapDEK(_ context.Context, dek, aad []byte) ([]byte, string, error) {
	if f.failWrap {
		return nil, "", errors.New("kek: wrap boom")
	}
	f.wraps++
	h := sha256.Sum256(aad)
	return append(append([]byte(nil), h[:]...), dek...), "fake-kek/v1", nil
}

func (f *fakeKEK) UnwrapDEK(_ context.Context, wrapped, aad []byte) ([]byte, error) {
	if f.failUnwrap {
		return nil, errors.New("kek: unwrap boom")
	}
	h := sha256.Sum256(aad)
	if len(wrapped) < sha256.Size || !bytes.Equal(wrapped[:sha256.Size], h[:]) {
		return nil, errors.New("kek: aad mismatch")
	}
	return append([]byte(nil), wrapped[sha256.Size:]...), nil
}

// ---------------------------------------------------------------------------
// Envelope — Seal/Open round-trip, AAD binding, fail-loud.
// ---------------------------------------------------------------------------

func TestEnvelope_SealOpenRoundTrip(t *testing.T) {
	env := NewEnvelope(&fakeKEK{})
	aad := BlobAAD(tTenant, tGCID, tUpload)
	plain := []byte("a marked physics past paper — momentum questions all wrong")

	sealed, err := env.Seal(context.Background(), aad, plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(sealed.WrappedDEK) == 0 || sealed.KEKVersion == "" || len(sealed.Ciphertext) == 0 {
		t.Fatalf("Seal returned an empty field: %+v", sealed)
	}
	// The ciphertext must NOT contain the plaintext (it is actually encrypted).
	if bytes.Contains(sealed.Ciphertext, plain) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := env.Open(context.Background(), aad, sealed.WrappedDEK, sealed.Ciphertext)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestEnvelope_OpenWrongAADFails(t *testing.T) {
	env := NewEnvelope(&fakeKEK{})
	aad := BlobAAD(tTenant, tGCID, tUpload)
	sealed, err := env.Seal(context.Background(), aad, []byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// A different upload context must not unwrap (cross-blob isolation).
	wrongAAD := BlobAAD(tTenant, tGCID, "01970000-0000-7000-a000-0000000000ff")
	if _, err := env.Open(context.Background(), wrongAAD, sealed.WrappedDEK, sealed.Ciphertext); err == nil {
		t.Fatalf("expected Open to fail under a mismatched AAD")
	}
}

func TestEnvelope_SealFailLoudOnKEKError(t *testing.T) {
	env := NewEnvelope(&fakeKEK{failWrap: true})
	if _, err := env.Seal(context.Background(), BlobAAD(tTenant, tGCID, tUpload), []byte("x")); err == nil {
		t.Fatalf("expected Seal to fail loud when the KEK wrap fails (never store unencrypted)")
	}
}

func TestEnvelope_OpenFailLoudOnKEKError(t *testing.T) {
	env := NewEnvelope(&fakeKEK{})
	sealed, _ := env.Seal(context.Background(), BlobAAD(tTenant, tGCID, tUpload), []byte("x"))
	env2 := NewEnvelope(&fakeKEK{failUnwrap: true})
	if _, err := env2.Open(context.Background(), BlobAAD(tTenant, tGCID, tUpload), sealed.WrappedDEK, sealed.Ciphertext); err == nil {
		t.Fatalf("expected Open to fail loud when the KEK unwrap fails")
	}
}

func TestEnvelope_DistinctDEKPerSeal(t *testing.T) {
	kek := &fakeKEK{}
	env := NewEnvelope(kek)
	aad := BlobAAD(tTenant, tGCID, tUpload)
	s1, _ := env.Seal(context.Background(), aad, []byte("same"))
	s2, _ := env.Seal(context.Background(), aad, []byte("same"))
	if bytes.Equal(s1.WrappedDEK, s2.WrappedDEK) {
		t.Fatalf("each Seal must mint a fresh random DEK (wrapped forms must differ)")
	}
	if bytes.Equal(s1.Ciphertext, s2.Ciphertext) {
		t.Fatalf("same plaintext under fresh DEK/nonce must produce different ciphertext")
	}
}

// ---------------------------------------------------------------------------
// InMemoryWrappedDEKStore — Put/Get/Shred/ListExpiredUnshredded.
// ---------------------------------------------------------------------------

func mkRec(uploadID string, created time.Time) WrappedDEK {
	return WrappedDEK{
		UploadID:    uploadID,
		TenantID:    tTenant,
		LearnerGCID: tGCID,
		Wrapped:     []byte("wrapped-" + uploadID),
		KEKVersion:  "fake-kek/v1",
		CreatedAt:   created,
	}
}

func TestStore_PutGet(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	rec := mkRec(tUpload, time.Now())
	if err := st.Put(context.Background(), rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := st.Get(context.Background(), tUpload)
	if err != nil || got == nil {
		t.Fatalf("Get: %v rec=%v", err, got)
	}
	if !bytes.Equal(got.Wrapped, rec.Wrapped) || got.Deleted {
		t.Fatalf("Get round-trip wrong: %+v", got)
	}
	// Absent → (nil,nil), not an error.
	miss, err := st.Get(context.Background(), "missing")
	if err != nil || miss != nil {
		t.Fatalf("absent Get = (%v,%v); want (nil,nil)", miss, err)
	}
}

func TestStore_ShredTombstonesAndScrubs(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	_ = st.Put(context.Background(), mkRec(tUpload, time.Now()))
	now := time.Now()
	if err := st.Shred(context.Background(), tUpload, now); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	got, _ := st.Get(context.Background(), tUpload)
	if got == nil || !got.Deleted {
		t.Fatalf("after Shred the row must be tombstoned: %+v", got)
	}
	if len(got.Wrapped) != 0 {
		t.Fatalf("after Shred the wrapped DEK bytes MUST be scrubbed (crypto-shred); got %d bytes", len(got.Wrapped))
	}
	// Idempotent — a second Shred (and shredding an absent id) must not error.
	if err := st.Shred(context.Background(), tUpload, now); err != nil {
		t.Fatalf("second Shred not idempotent: %v", err)
	}
	if err := st.Shred(context.Background(), "never-existed", now); err != nil {
		t.Fatalf("Shred of absent id must be a no-op: %v", err)
	}
}

func TestStore_ListExpiredUnshredded(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	_ = st.Put(context.Background(), mkRec("01970000-0000-7000-a000-000000000001", old))   // expired
	_ = st.Put(context.Background(), mkRec("01970000-0000-7000-a000-000000000002", fresh)) // fresh
	expiredShredded := mkRec("01970000-0000-7000-a000-000000000003", old)
	_ = st.Put(context.Background(), expiredShredded)
	_ = st.Shred(context.Background(), expiredShredded.UploadID, time.Now()) // already shredded → excluded

	cutoff := time.Now().Add(-24 * time.Hour)
	got, err := st.ListExpiredUnshredded(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("ListExpiredUnshredded: %v", err)
	}
	if len(got) != 1 || got[0].UploadID != "01970000-0000-7000-a000-000000000001" {
		t.Fatalf("want exactly the one expired+unshredded row; got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// fakeBlobDeleter — records GCS object deletes.
// ---------------------------------------------------------------------------
type fakeBlobDeleter struct {
	deleted []string
	fail    bool
}

func (f *fakeBlobDeleter) Delete(_ context.Context, tenantID, uploadID string) error {
	if f.fail {
		return errors.New("gcs: delete boom")
	}
	f.deleted = append(f.deleted, tenantID+"/"+uploadID)
	return nil
}

// ---------------------------------------------------------------------------
// Shredder — tombstone the DEK (the guarantee) + best-effort delete the GCS
// ciphertext (defence in depth).
// ---------------------------------------------------------------------------

func TestShredder_ShredTombstonesDEKAndDeletesBlob(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	_ = st.Put(context.Background(), mkRec(tUpload, time.Now()))
	del := &fakeBlobDeleter{}
	sh := NewShredder(st, del)
	sh.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

	if err := sh.Shred(context.Background(), tTenant, tGCID, tUpload); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	got, _ := st.Get(context.Background(), tUpload)
	if got == nil || !got.Deleted || len(got.Wrapped) != 0 {
		t.Fatalf("DEK not crypto-shredded: %+v", got)
	}
	if len(del.deleted) != 1 || del.deleted[0] != tTenant+"/"+tUpload {
		t.Fatalf("GCS ciphertext not deleted: %v", del.deleted)
	}
}

func TestShredder_FailLoudWhenDEKTombstoneFails(t *testing.T) {
	sh := NewShredder(failingStore{}, &fakeBlobDeleter{})
	if err := sh.Shred(context.Background(), tTenant, tGCID, tUpload); err == nil {
		t.Fatalf("expected fail-loud when the DEK tombstone fails (the shred guarantee)")
	}
}

func TestShredder_NilDeleterStillShredsDEK(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	_ = st.Put(context.Background(), mkRec(tUpload, time.Now()))
	sh := NewShredder(st, nil) // no GCS deleter wired — the DEK tombstone is still the guarantee
	if err := sh.Shred(context.Background(), tTenant, tGCID, tUpload); err != nil {
		t.Fatalf("Shred with nil deleter: %v", err)
	}
	got, _ := st.Get(context.Background(), tUpload)
	if got == nil || !got.Deleted {
		t.Fatalf("DEK must still be shredded with a nil deleter: %+v", got)
	}
}

// failingStore makes Shred error (DEK tombstone failure path).
type failingStore struct{}

func (failingStore) Put(context.Context, WrappedDEK) error            { return nil }
func (failingStore) Get(context.Context, string) (*WrappedDEK, error) { return nil, nil }
func (failingStore) Shred(context.Context, string, time.Time) error {
	return errors.New("pg: shred boom")
}
func (failingStore) ListExpiredUnshredded(context.Context, time.Time, int) ([]WrappedDEK, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Sweeper — per-tenant TTL backstop (RLS-correct; no cross-tenant scan).
// ---------------------------------------------------------------------------

func TestSweeper_ShredsExpiredUnshredded(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	old := time.Now().Add(-72 * time.Hour)
	_ = st.Put(context.Background(), mkRec("01970000-0000-7000-a000-000000000001", old))
	_ = st.Put(context.Background(), mkRec("01970000-0000-7000-a000-000000000002", old))
	_ = st.Put(context.Background(), mkRec("01970000-0000-7000-a000-000000000003", time.Now())) // fresh

	del := &fakeBlobDeleter{}
	sw := NewSweeper(st, NewShredder(st, del), 24*time.Hour)
	n, err := sw.SweepExpired(context.Background())
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 expired blobs shredded; got %d", n)
	}
	// The two expired rows are now tombstoned; the fresh one survives.
	fresh, _ := st.Get(context.Background(), "01970000-0000-7000-a000-000000000003")
	if fresh == nil || fresh.Deleted {
		t.Fatalf("fresh blob must NOT be swept: %+v", fresh)
	}
	// Re-sweep is a no-op (nothing left expired+unshredded).
	if n2, _ := sw.SweepExpired(context.Background()); n2 != 0 {
		t.Fatalf("re-sweep should shred nothing; got %d", n2)
	}
}
