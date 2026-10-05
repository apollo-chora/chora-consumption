// proofingtest.go — Postgres adapter for the ProofingTest aggregate
// (proofingtest.Repository, CHO-2040 R8-6). Schema mirrors
// migrations/00XX_proofing_tests.up.sql (renumbered at merge; see the
// migration header).
//
// Per multi-tenant-rls every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the
// domain query; per-learner scoping is an explicit learner_gcid predicate and
// writes are scoped by id AND tenant AND learner as defence-in-depth.
// GetByAssistID is tenant-scoped only (the qgen terminal envelope carries
// tenant + gcid; the row's own learner_gcid is re-checked by the subscriber's
// refund path which reads it FROM the row). Cross-DB queries are forbidden —
// the ProofingTest lives wholly in chora_consumption; the ai_assist batch is
// referenced by opaque UUID only (#3).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/rls"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// ProofingTestRepo is the Postgres-backed proofingtest.Repository.
type ProofingTestRepo struct {
	tx TxRunner
}

// NewProofingTestRepo constructs the repo around a TxRunner.
func NewProofingTestRepo(tx TxRunner) *ProofingTestRepo {
	return &ProofingTestRepo{tx: tx}
}

// Compile-time check.
var _ pt.Repository = (*ProofingTestRepo)(nil)

// uqProofingInflightSignature is the migration-0069 partial-unique index that
// backstops the concurrent-double-submit race: at most one in-flight
// (requested|composing) proofing test per (tenant, learner, goal, signature).
const uqProofingInflightSignature = "uq_proofing_tests_inflight_signature"

const (
	insertProofingTestSQL = `
		INSERT INTO proofing_tests (
			id, tenant_id, learner_gcid, goal_id, companion_id, assist_id,
			status, target_edges, testset_ref, testset_payload,
			failure_reason, mana_reserved, reservation_id,
			created_at, updated_at, edge_signature
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	updateProofingTestSQL = `
		UPDATE proofing_tests SET
			status          = $4,
			testset_ref     = $5,
			testset_payload = $6,
			failure_reason  = $7,
			mana_reserved   = $8,
			reservation_id  = $9,
			updated_at      = $10,
			deleted_at      = $11
		WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3`

	getProofingTestSQL = `
		SELECT id, tenant_id, learner_gcid, goal_id, companion_id, assist_id,
		       status, target_edges, testset_ref, testset_payload,
		       failure_reason, mana_reserved, reservation_id,
		       created_at, updated_at, deleted_at
		  FROM proofing_tests
		 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3
		   AND deleted_at IS NULL`

	getProofingTestByAssistSQL = `
		SELECT id, tenant_id, learner_gcid, goal_id, companion_id, assist_id,
		       status, target_edges, testset_ref, testset_payload,
		       failure_reason, mana_reserved, reservation_id,
		       created_at, updated_at, deleted_at
		  FROM proofing_tests
		 WHERE assist_id = $1 AND tenant_id = $2
		   AND deleted_at IS NULL`

	listProofingTestsSQL = `
		SELECT id, tenant_id, learner_gcid, goal_id, companion_id, assist_id,
		       status, target_edges, testset_ref, testset_payload,
		       failure_reason, mana_reserved, reservation_id,
		       created_at, updated_at, deleted_at
		  FROM proofing_tests
		 WHERE tenant_id = $1 AND learner_gcid = $2
		   AND ($3::uuid IS NULL OR goal_id = $3::uuid)
		   AND deleted_at IS NULL
		 ORDER BY created_at DESC, id DESC`
)

// Create persists a new ProofingTest.
func (r *ProofingTestRepo) Create(ctx context.Context, p *pt.ProofingTest) error {
	if p == nil {
		return nil
	}
	edgesJSON, err := json.Marshal(p.TargetEdges)
	if err != nil {
		return fmt.Errorf("pg proofing: marshal target_edges: %w", err)
	}
	// The idempotency signature is derived (goal + normalized deduped titles);
	// it is stored so the migration-0069 partial-unique index can enforce
	// one-in-flight-per-request as the concurrency backstop behind the
	// handler's read-probe.
	sig := pt.EdgeSignature(p.GoalID, p.TargetEdges)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertProofingTestSQL,
			p.ID, p.TenantID, p.LearnerGCID, p.GoalID, p.CompanionID, p.AssistID,
			string(p.Status), edgesJSON, p.TestSetRef, nullableBytes(p.TestSetPayload),
			nullableText(p.FailureReason), p.ManaReserved, nullableText(p.ReservationID),
			p.CreatedAt, p.UpdatedAt, sig,
		)
		if isUniqueViolation(err, uqProofingInflightSignature) {
			// A concurrent request beat us to the in-flight slot — surface the
			// typed sentinel so the handler returns the winner + refunds.
			return pt.ErrDuplicateInFlight
		}
		return err
	})
}

// isUniqueViolation reports whether err is a Postgres unique_violation (23505),
// optionally scoped to a named constraint/index (constraint == "" matches any).
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}

// Update persists the mutable fields, scoped to (id, tenant, learner).
// Identity + target_edges are immutable after mint (a new request is a new
// row).
func (r *ProofingTestRepo) Update(ctx context.Context, p *pt.ProofingTest) error {
	if p == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, updateProofingTestSQL,
			p.ID, p.TenantID, p.LearnerGCID,
			string(p.Status), p.TestSetRef, nullableBytes(p.TestSetPayload),
			nullableText(p.FailureReason), p.ManaReserved, nullableText(p.ReservationID),
			p.UpdatedAt, p.DeletedAt,
		)
		return err
	})
}

// GetByID returns one live row for the learner, or (nil, nil).
func (r *ProofingTestRepo) GetByID(ctx context.Context, tenantID, learnerGCID, id string) (*pt.ProofingTest, error) {
	var out *pt.ProofingTest
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getProofingTestSQL, id, tenantID, learnerGCID)
		if row == nil {
			return nil // stub Querier — not found
		}
		p, err := scanProofingTest(row)
		if err != nil {
			if err == ErrNoRows {
				return nil
			}
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// GetByAssistID resolves the row a qgen terminal event keys on, or (nil, nil).
func (r *ProofingTestRepo) GetByAssistID(ctx context.Context, tenantID, assistID string) (*pt.ProofingTest, error) {
	var out *pt.ProofingTest
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getProofingTestByAssistSQL, assistID, tenantID)
		if row == nil {
			return nil
		}
		p, err := scanProofingTest(row)
		if err != nil {
			if err == ErrNoRows {
				return nil
			}
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// ListByLearner returns the learner's live rows, newest first; goalID == ""
// lists across goals. Always a non-nil slice.
func (r *ProofingTestRepo) ListByLearner(ctx context.Context, tenantID, learnerGCID, goalID string) ([]*pt.ProofingTest, error) {
	out := make([]*pt.ProofingTest, 0)
	var goalFilter *string
	if goalID != "" {
		goalFilter = &goalID
	}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listProofingTestsSQL, tenantID, learnerGCID, goalFilter)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProofingTest(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// rowScanner is the shared Scan seam (pgx.Row + pgx.Rows both satisfy it via
// the package's Querier surfaces).
type proofingScanner interface{ Scan(dest ...any) error }

// scanProofingTest maps one row onto the aggregate.
func scanProofingTest(row proofingScanner) (*pt.ProofingTest, error) {
	var (
		p          pt.ProofingTest
		status     string
		edgesJSON  []byte
		testsetRef *string
		payload    []byte
		failure    *string
		resID      *string
	)
	if err := row.Scan(
		&p.ID, &p.TenantID, &p.LearnerGCID, &p.GoalID, &p.CompanionID, &p.AssistID,
		&status, &edgesJSON, &testsetRef, &payload,
		&failure, &p.ManaReserved, &resID,
		&p.CreatedAt, &p.UpdatedAt, &p.DeletedAt,
	); err != nil {
		// The pgxRow bridge already maps pgx.ErrNoRows → ErrNoRows.
		return nil, err
	}
	p.Status = pt.Status(status)
	if len(edgesJSON) > 0 {
		if err := json.Unmarshal(edgesJSON, &p.TargetEdges); err != nil {
			return nil, fmt.Errorf("pg proofing: unmarshal target_edges: %w", err)
		}
	}
	if p.TargetEdges == nil {
		p.TargetEdges = []pt.TargetEdge{}
	}
	p.TestSetRef = testsetRef
	p.TestSetPayload = payload
	if failure != nil {
		p.FailureReason = *failure
	}
	if resID != nil {
		p.ReservationID = *resID
	}
	return &p, nil
}

// nullableBytes maps empty → NULL (JSONB column). nullableText is shared
// from learner_weakness.go.
func nullableBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
