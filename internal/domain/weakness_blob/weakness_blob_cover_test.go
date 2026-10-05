package weakness_blob

import (
	"context"
	"errors"
	"testing"
	"time"
)

// failReader makes the DEK/nonce read fail (Seal rng error path).
type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("rng boom") }

// emptyWrapKEK returns an empty wrapped DEK — Seal must refuse it (unshreddable).
type emptyWrapKEK struct{}

func (emptyWrapKEK) WrapDEK(context.Context, []byte, []byte) ([]byte, string, error) {
	return nil, "v1", nil
}
func (emptyWrapKEK) UnwrapDEK(context.Context, []byte, []byte) ([]byte, error) { return nil, nil }

func TestEnvelope_NilGuards(t *testing.T) {
	var e *Envelope
	if _, err := e.Seal(context.Background(), nil, nil); err == nil {
		t.Fatal("nil envelope Seal must error")
	}
	if _, err := e.Open(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("nil envelope Open must error")
	}
	if _, err := (&Envelope{}).Seal(context.Background(), nil, nil); err == nil {
		t.Fatal("envelope with nil KEK Seal must error")
	}
}

func TestEnvelope_SealRNGError(t *testing.T) {
	e := &Envelope{kek: &fakeKEK{}, rng: failReader{}}
	if _, err := e.Seal(context.Background(), BlobAAD(tTenant, tGCID, tUpload), []byte("x")); err == nil {
		t.Fatal("Seal must fail loud when the rng fails")
	}
}

func TestEnvelope_SealRefusesEmptyWrap(t *testing.T) {
	e := NewEnvelope(emptyWrapKEK{})
	if _, err := e.Seal(context.Background(), BlobAAD(tTenant, tGCID, tUpload), []byte("x")); err == nil {
		t.Fatal("Seal must refuse an empty wrapped DEK")
	}
}

func TestEnvelope_OpenCiphertextTooShort(t *testing.T) {
	e := NewEnvelope(&fakeKEK{})
	if _, err := e.Open(context.Background(), BlobAAD(tTenant, tGCID, tUpload), []byte("w"), []byte{1, 2, 3}); err == nil {
		t.Fatal("Open must reject ciphertext shorter than the nonce")
	}
}

func TestShredder_NilConfig(t *testing.T) {
	var s *Shredder
	if err := s.Shred(context.Background(), tTenant, tGCID, tUpload); err == nil {
		t.Fatal("nil shredder must error")
	}
}

func TestShredder_GCSDeleteErrorPropagates(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	_ = st.Put(context.Background(), mkRec(tUpload, time.Now()))
	sh := NewShredder(st, &fakeBlobDeleter{fail: true})
	if err := sh.Shred(context.Background(), tTenant, tGCID, tUpload); err == nil {
		t.Fatal("a GCS delete failure must propagate (fail-loud → caller NACKs)")
	}
	// The DEK guarantee still landed before the GCS delete failed.
	got, _ := st.Get(context.Background(), tUpload)
	if got == nil || !got.Deleted {
		t.Fatalf("DEK must be tombstoned even when the GCS delete fails: %+v", got)
	}
}

func TestStore_ListExpiredRespectsLimit(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	old := time.Now().Add(-48 * time.Hour)
	for _, id := range []string{"a", "b", "c"} {
		_ = st.Put(context.Background(), mkRec(id, old))
	}
	got, err := st.ListExpiredUnshredded(context.Background(), time.Now().Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit not respected: got %d want 2", len(got))
	}
}

func TestStore_PutIdempotent(t *testing.T) {
	st := NewInMemoryWrappedDEKStore()
	r := mkRec(tUpload, time.Now())
	_ = st.Put(context.Background(), r)
	r.Wrapped = []byte("DIFFERENT") // a re-put must NOT overwrite the in-flight wrap
	_ = st.Put(context.Background(), r)
	got, _ := st.Get(context.Background(), tUpload)
	if string(got.Wrapped) == "DIFFERENT" {
		t.Fatal("Put must be idempotent (no overwrite of an existing wrap)")
	}
}

func TestSweeper_NilConfig(t *testing.T) {
	var s *Sweeper
	if _, err := s.SweepExpired(context.Background()); err == nil {
		t.Fatal("nil sweeper must error")
	}
}

// oneExpiredStore lists exactly one expired rec but its Shred always fails — to
// drive the sweep's in-loop shred-error path.
type oneExpiredStore struct{}

func (oneExpiredStore) Put(context.Context, WrappedDEK) error            { return nil }
func (oneExpiredStore) Get(context.Context, string) (*WrappedDEK, error) { return nil, nil }
func (oneExpiredStore) Shred(context.Context, string, time.Time) error {
	return errors.New("shred boom")
}
func (oneExpiredStore) ListExpiredUnshredded(context.Context, time.Time, int) ([]WrappedDEK, error) {
	return []WrappedDEK{{UploadID: tUpload, TenantID: tTenant, LearnerGCID: tGCID, CreatedAt: time.Now().Add(-72 * time.Hour)}}, nil
}

func TestSweeper_ShredErrorStops(t *testing.T) {
	sw := NewSweeper(oneExpiredStore{}, NewShredder(oneExpiredStore{}, nil), time.Hour)
	n, err := sw.SweepExpired(context.Background())
	if err == nil {
		t.Fatal("a shred failure mid-sweep must surface (fail-loud)")
	}
	if n != 0 {
		t.Fatalf("count must reflect 0 shredded before the failure; got %d", n)
	}
}

func TestSweeper_ListErrorSurfaces(t *testing.T) {
	sw := NewSweeper(listErrStore{NewInMemoryWrappedDEKStore()}, NewShredder(NewInMemoryWrappedDEKStore(), nil), time.Hour)
	if _, err := sw.SweepExpired(context.Background()); err == nil {
		t.Fatal("a list error must surface")
	}
}

type listErrStore struct{ *InMemoryWrappedDEKStore }

func (listErrStore) ListExpiredUnshredded(context.Context, time.Time, int) ([]WrappedDEK, error) {
	return nil, errors.New("list boom")
}
