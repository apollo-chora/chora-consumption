package pg

import (
	"context"
	"testing"
	"time"

	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

func TestWeaknessUploadRepo_Insert_CarriesConsentArg(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	consent := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	u, _ := wu.New(wu.NewInput{
		UploadID: "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee", TenantID: "tnt-1", LearnerGCID: "gcid-1",
		UploadKind: wu.KindSourceMaterial, SourceMIME: "application/pdf", SourceBlobURI: "gs://b/o.pdf",
		Now: consent, UploadRightsConsentAt: &consent,
	})
	if err := repo.Insert(wuCtx(), u); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	e, ok := tx.q.find("INSERT INTO weakness_doc_uploads")
	if !ok {
		t.Fatal("no INSERT exec")
	}
	// args: [0]upload_id .. [6]status [7]created_at [8]upload_rights_consent_at
	if len(e.args) < 9 {
		t.Fatalf("insert must carry the consent arg; got %d args", len(e.args))
	}
	got, ok := e.args[8].(*time.Time)
	if !ok || got == nil || !got.Equal(consent) {
		t.Fatalf("consent arg = %v want %v", e.args[8], consent)
	}
}

func TestWeaknessUploadRepo_MarkBlobShredded(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	if err := repo.MarkBlobShredded(wuCtx(), "gcid-1", "up-1", now); err != nil {
		t.Fatalf("MarkBlobShredded: %v", err)
	}
	e, ok := tx.q.find("blob_deleted_at = $3")
	if !ok {
		t.Fatal("no blob_deleted_at update exec")
	}
	if e.args[0] != "up-1" || e.args[1] != "gcid-1" {
		t.Fatalf("MarkBlobShredded args = %v", e.args)
	}
	// RLS applied first.
	if len(tx.q.execs) < 3 || tx.q.execs[0].sql == "" {
		t.Fatalf("expected SET LOCAL pair before the update")
	}
}

func TestWeaknessUploadRepo_MarkBlobShredded_RLSError(t *testing.T) {
	repo := NewWeaknessUploadRepo(&wuTx{})
	if err := repo.MarkBlobShredded(context.Background(), "gcid-1", "up-1", time.Now()); err == nil {
		t.Fatal("MarkBlobShredded: want RLS error on no-tenant ctx")
	}
}
