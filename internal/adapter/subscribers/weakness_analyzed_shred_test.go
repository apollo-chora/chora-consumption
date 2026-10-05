// weakness_analyzed_shred_test.go — WS-5 (ADR-205 D8 / CHO-1957): a successful
// analysis crypto-shreds the raw upload blob via the BlobShredHook.
package subscribers

import (
	"context"
	"errors"
	"testing"
)

func TestWeaknessAnalyzed_CallsBlobShredHookOnSuccess(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1, -0.2, 0.3}}
	var gotTenant, gotGCID, gotUpload string
	calls := 0
	sub := newLWSub(repo, emb).WithBlobShredHook(func(_ context.Context, tenant, gcid, upload string) error {
		calls++
		gotTenant, gotGCID, gotUpload = tenant, gcid, upload
		return nil
	})
	if err := sub.Handle(context.Background(), lwEnv("evt-shred-1"), lwPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if calls != 1 {
		t.Fatalf("shred hook called %d times; want exactly 1 (once per analysis)", calls)
	}
	if gotUpload != "upl-1" || gotTenant != "01970000-0000-7000-8000-000000000001" || gotGCID != "01970000-0000-7000-9000-000000000001" {
		t.Fatalf("shred hook args = (%q,%q,%q)", gotTenant, gotGCID, gotUpload)
	}
}

func TestWeaknessAnalyzed_ShredHookErrorNACKs(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1, -0.2, 0.3}}
	sub := newLWSub(repo, emb).WithBlobShredHook(func(context.Context, string, string, string) error {
		return errors.New("kms unwrap unavailable")
	})
	err := sub.Handle(context.Background(), lwEnv("evt-shred-2"), lwPayload())
	if err == nil {
		t.Fatalf("a shred failure MUST fail loud (NACK → redelivery), never swallow")
	}
}

func TestWeaknessAnalyzed_NilHookIsDarkNoShred(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1, -0.2, 0.3}}
	// No WithBlobShredHook → envelope path DARK → analysis still succeeds.
	if err := newLWSub(repo, emb).Handle(context.Background(), lwEnv("evt-shred-3"), lwPayload()); err != nil {
		t.Fatalf("Handle with no shred hook (DARK): %v", err)
	}
}
