// growth_edge_output.go - pg adapter for the WS-7 generated-artifact aggregate
// (CHO-2348). Mirrors weakness_upload.go: every read/write runs
// rls.ApplySession (SET LOCAL chora.tenant_id) FIRST, then the user query with an
// explicit learner_gcid predicate. chora_consumption only - no cross-DB.
//
// The ApplySession call is not ceremony: growth_edge_outputs is FORCE ROW LEVEL
// SECURITY, and a query that skips the session GUC does not error, it silently
// matches nothing. A missing ApplySession therefore reads as "the learner has no
// artifacts" rather than as a failure.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	geo "github.com/apollo-chora/chora-consumption/internal/domain/growth_edge_output"
)

// insertGrowthEdgeOutputSQL projects one artifact. IDEMPOTENT: Pub/Sub is
// at-least-once, and the partial unique index (tenant_id, upload_id, kind)
// WHERE deleted_at IS NULL makes a redelivery a no-op instead of a second copy
// on the learner's surface.
const insertGrowthEdgeOutputSQL = `
INSERT INTO growth_edge_outputs
    (output_id, tenant_id, learner_gcid, upload_id, kind, content, metered,
     source_event_id, generated_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, NULLIF($8,'')::uuid, $9, $10)
ON CONFLICT (tenant_id, upload_id, kind) WHERE deleted_at IS NULL DO NOTHING`

// listGrowthEdgeOutputsSQL is the learner read path. Ordered by kind so the
// surface renders deterministically rather than in insertion race order.
const listGrowthEdgeOutputsSQL = `
SELECT output_id, tenant_id, learner_gcid, upload_id, kind, content, metered,
       COALESCE(source_event_id::text, ''), generated_at, created_at
FROM growth_edge_outputs
WHERE tenant_id = $1 AND learner_gcid = $2 AND upload_id = $3 AND deleted_at IS NULL
ORDER BY kind`

// GrowthEdgeOutputRepo is the growth_edge_output.Repository backed by a TxRunner.
type GrowthEdgeOutputRepo struct {
	tx TxRunner
}

// NewGrowthEdgeOutputRepo constructs the repo around a TxRunner.
func NewGrowthEdgeOutputRepo(tx TxRunner) *GrowthEdgeOutputRepo {
	return &GrowthEdgeOutputRepo{tx: tx}
}

// CreateBatch projects one delivery's artifacts in ONE transaction, so a
// partial failure cannot leave the learner holding half a delivery. Returns the
// number of rows newly inserted; 0 means every artifact was already present
// (a pure redelivery), which the subscriber logs rather than mistaking for work.
func (r *GrowthEdgeOutputRepo) CreateBatch(ctx context.Context, outs []geo.Output) (int, error) {
	if len(outs) == 0 {
		return 0, nil
	}
	inserted := 0
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		inserted = 0 // reset: RunInTx may retry the closure
		for _, o := range outs {
			tag, err := q.Exec(ctx, insertGrowthEdgeOutputSQL,
				o.OutputID, o.TenantID, o.LearnerGCID, o.UploadID, string(o.Kind),
				string(o.Content), o.Metered, o.SourceEventID, o.GeneratedAt, o.CreatedAt,
			)
			if err != nil {
				return fmt.Errorf("pg: insert growth_edge_output %s/%s: %w",
					o.UploadID, o.Kind, err)
			}
			inserted += int(tag.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// ListForUpload returns the learner's live artifacts for one upload.
func (r *GrowthEdgeOutputRepo) ListForUpload(
	ctx context.Context, tenantID, learnerGCID, uploadID string,
) ([]geo.Output, error) {
	out := make([]geo.Output, 0, len(geo.AllKinds()))
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		out = out[:0] // reset: RunInTx may retry the closure
		rows, err := q.Query(ctx, listGrowthEdgeOutputsSQL, tenantID, learnerGCID, uploadID)
		if err != nil {
			return fmt.Errorf("pg: list growth_edge_outputs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				o       geo.Output
				kind    string
				content []byte
			)
			if err := rows.Scan(
				&o.OutputID, &o.TenantID, &o.LearnerGCID, &o.UploadID, &kind,
				&content, &o.Metered, &o.SourceEventID, &o.GeneratedAt, &o.CreatedAt,
			); err != nil {
				return fmt.Errorf("pg: scan growth_edge_output: %w", err)
			}
			// Fail loud on a kind the domain does not know: the DB CHECK and the
			// domain must agree, and a silently dropped row is a paid-for
			// artifact vanishing from the learner's surface again.
			parsed, err := geo.ParseKind(kind)
			if err != nil {
				return fmt.Errorf("pg: growth_edge_output %s: %w", o.OutputID, err)
			}
			o.Kind = parsed
			o.Content = json.RawMessage(content)
			out = append(out, o)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg: iterate growth_edge_outputs: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
