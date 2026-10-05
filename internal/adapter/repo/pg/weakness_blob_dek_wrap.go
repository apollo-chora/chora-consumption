// weakness_blob_dek_wrap.go — pg adapter for the per-blob wrapped-DEK store
// (ADR-205 WS-5 / ADR-186 / CHO-1957). Persists ONLY the master-KEK-wrapped
// per-blob DEK (never plaintext key material); crypto-shred = scrub the wrapped
// bytes + tombstone (deleted_at). Mirrors weakness_upload.go: every read/write
// runs rls.ApplySession (SET LOCAL chora.tenant_id) first, then the query.
// chora_consumption only — no cross-DB.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
)

const insertBlobDEKWrapSQL = `
INSERT INTO weakness_blob_dek_wrap
    (upload_id, tenant_id, learner_gcid, wrapped_dek, kek_version, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (upload_id) DO NOTHING`

// COALESCE so a shredded (NULL) wrapped_dek scans into an empty []byte, matching
// the domain's "empty Wrapped == shredded" semantic.
const getBlobDEKWrapSQL = `
SELECT upload_id, tenant_id, learner_gcid, COALESCE(wrapped_dek, ''::bytea),
       kek_version, created_at, (deleted_at IS NOT NULL)
FROM weakness_blob_dek_wrap
WHERE upload_id = $1`

// deleted_at IS NULL makes a re-delivered shred a no-op (idempotent crypto-shred).
const shredBlobDEKWrapSQL = `
UPDATE weakness_blob_dek_wrap
SET wrapped_dek = NULL, deleted_at = $2
WHERE upload_id = $1 AND deleted_at IS NULL`

// The TTL backstop: live (un-shredded) rows older than the cutoff, for the
// current tenant (RLS-scoped — no cross-tenant scan). wrapped_dek is intentionally
// NOT selected (the sweeper only needs ids to shred).
const listExpiredBlobDEKWrapSQL = `
SELECT upload_id, tenant_id, learner_gcid, kek_version, created_at
FROM weakness_blob_dek_wrap
WHERE deleted_at IS NULL AND created_at < $1
ORDER BY created_at
LIMIT $2`

// WeaknessBlobDEKWrapRepo is the weakness_blob.WrappedDEKStore backed by a TxRunner.
type WeaknessBlobDEKWrapRepo struct {
	tx TxRunner
}

// NewWeaknessBlobDEKWrapRepo constructs the repo around a TxRunner.
func NewWeaknessBlobDEKWrapRepo(tx TxRunner) *WeaknessBlobDEKWrapRepo {
	return &WeaknessBlobDEKWrapRepo{tx: tx}
}

var _ wb.WrappedDEKStore = (*WeaknessBlobDEKWrapRepo)(nil)

func (r *WeaknessBlobDEKWrapRepo) Put(ctx context.Context, rec wb.WrappedDEK) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		created := rec.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		if _, err := q.Exec(ctx, insertBlobDEKWrapSQL,
			rec.UploadID, rec.TenantID, rec.LearnerGCID, rec.Wrapped, rec.KEKVersion, created,
		); err != nil {
			return fmt.Errorf("pg: insert weakness_blob_dek_wrap: %w", err)
		}
		return nil
	})
}

func (r *WeaknessBlobDEKWrapRepo) Get(ctx context.Context, uploadID string) (*wb.WrappedDEK, error) {
	var out *wb.WrappedDEK
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var rec wb.WrappedDEK
		err := q.QueryRow(ctx, getBlobDEKWrapSQL, uploadID).Scan(
			&rec.UploadID, &rec.TenantID, &rec.LearnerGCID, &rec.Wrapped,
			&rec.KEKVersion, &rec.CreatedAt, &rec.Deleted,
		)
		if errors.Is(err, ErrNoRows) {
			return nil // absent → out stays nil
		}
		if err != nil {
			return fmt.Errorf("pg: get weakness_blob_dek_wrap: %w", err)
		}
		out = &rec
		return nil
	})
	return out, err
}

func (r *WeaknessBlobDEKWrapRepo) Shred(ctx context.Context, uploadID string, now time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, shredBlobDEKWrapSQL, uploadID, now.UTC()); err != nil {
			return fmt.Errorf("pg: shred weakness_blob_dek_wrap: %w", err)
		}
		return nil
	})
}

func (r *WeaknessBlobDEKWrapRepo) ListExpiredUnshredded(ctx context.Context, olderThan time.Time, limit int) ([]wb.WrappedDEK, error) {
	if limit <= 0 {
		limit = 200
	}
	var out []wb.WrappedDEK
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listExpiredBlobDEKWrapSQL, olderThan.UTC(), limit)
		if err != nil {
			return fmt.Errorf("pg: list expired weakness_blob_dek_wrap: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var rec wb.WrappedDEK
			if err := rows.Scan(&rec.UploadID, &rec.TenantID, &rec.LearnerGCID, &rec.KEKVersion, &rec.CreatedAt); err != nil {
				return fmt.Errorf("pg: scan expired weakness_blob_dek_wrap: %w", err)
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	return out, err
}
