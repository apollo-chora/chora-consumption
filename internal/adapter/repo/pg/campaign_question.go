// campaign_question.go — Postgres adapter for the campaign question-set
// aggregate (campaignquestion.Repository, WS-C3 CHO-2082). Schema:
// migrations/0079_campaign_question_bank.up.sql.
//
// Per multi-tenant-rls every read/write runs rls.ApplySession inside the
// transaction BEFORE the domain query; per-learner scoping is an explicit
// learner_gcid predicate. GetByAssistID is tenant-scoped only (the qgen
// terminal envelope carries tenant; unknown assist ids are (nil, nil) so the
// SHARED-topic subscriber ack-skips foreign jobs — the proofing idiom).
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
)

// CampaignQuestionSetRepo is the Postgres-backed campaignquestion.Repository.
type CampaignQuestionSetRepo struct {
	tx TxRunner
}

// NewCampaignQuestionSetRepo constructs the repo around a TxRunner.
func NewCampaignQuestionSetRepo(tx TxRunner) *CampaignQuestionSetRepo {
	return &CampaignQuestionSetRepo{tx: tx}
}

// Compile-time check.
var _ cq.Repository = (*CampaignQuestionSetRepo)(nil)

const (
	upsertCampaignQuestionSetSQL = `
		INSERT INTO campaign_question_sets (
			id, tenant_id, learner_gcid, concept_id, concept_key, rung,
			retrieved_atom_ids, retrieval_checked_at,
			generation_status, assist_id, requested_on, request_origin,
			questions_payload, failure_reason,
			created_at, updated_at, deleted_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (tenant_id, learner_gcid, concept_id, rung) WHERE deleted_at IS NULL
		DO UPDATE SET
			concept_key          = EXCLUDED.concept_key,
			retrieved_atom_ids   = EXCLUDED.retrieved_atom_ids,
			retrieval_checked_at = EXCLUDED.retrieval_checked_at,
			generation_status    = EXCLUDED.generation_status,
			assist_id            = EXCLUDED.assist_id,
			requested_on         = EXCLUDED.requested_on,
			request_origin       = EXCLUDED.request_origin,
			questions_payload    = EXCLUDED.questions_payload,
			failure_reason       = EXCLUDED.failure_reason,
			updated_at           = EXCLUDED.updated_at,
			deleted_at           = EXCLUDED.deleted_at`

	getCampaignQuestionSetSQL = `
		SELECT id, tenant_id, learner_gcid, concept_id, concept_key, rung,
		       retrieved_atom_ids, retrieval_checked_at,
		       generation_status, COALESCE(assist_id, ''), requested_on, request_origin,
		       questions_payload, COALESCE(failure_reason, ''),
		       created_at, updated_at, deleted_at
		  FROM campaign_question_sets
		 WHERE tenant_id = $1 AND learner_gcid = $2 AND concept_id = $3 AND rung = $4
		   AND deleted_at IS NULL`

	getCampaignQuestionSetByAssistSQL = `
		SELECT id, tenant_id, learner_gcid, concept_id, concept_key, rung,
		       retrieved_atom_ids, retrieval_checked_at,
		       generation_status, COALESCE(assist_id, ''), requested_on, request_origin,
		       questions_payload, COALESCE(failure_reason, ''),
		       created_at, updated_at, deleted_at
		  FROM campaign_question_sets
		 WHERE assist_id = $1 AND tenant_id = $2
		   AND deleted_at IS NULL`

	countCampaignQuestionRequestsSQL = `
		SELECT COUNT(*)
		  FROM campaign_question_sets
		 WHERE tenant_id = $1 AND learner_gcid = $2 AND requested_on = $3
			   AND request_origin = $4
			   AND deleted_at IS NULL`
)

// Save upserts on the live (tenant, learner, concept, rung) identity.
func (r *CampaignQuestionSetRepo) Save(ctx context.Context, s *cq.QuestionSet) error {
	if s == nil {
		return nil
	}
	// A REQUESTED set for a bare node carries no retrieved atoms; the column
	// is NOT NULL, so a nil slice must land as an empty array, never SQL NULL
	// (23502 killed the qgen leg for fresh goals — caught by the C8 walk).
	retrieved := s.RetrievedAtomIDs
	if retrieved == nil {
		retrieved = []string{}
	}
	// request_origin is NOT NULL with a CHECK (march|tap). A row minted via
	// cq.New always carries march; coerce a zero-value (an un-New'd literal)
	// to the conservative march default so a Save never trips the 23514 CHECK
	// (mirrors the nil-retrieved → empty-array coercion above).
	origin := s.RequestOrigin
	if origin == "" {
		origin = cq.OriginMarch
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertCampaignQuestionSetSQL,
			s.ID, s.TenantID, s.LearnerGCID, s.ConceptID, s.ConceptKey, s.Rung,
			retrieved, s.RetrievalCheckedAt,
			string(s.GenerationStatus), nullableText(s.AssistID), s.RequestedOn, string(origin),
			nullableBytes(s.QuestionsPayload), nullableText(s.FailureReason),
			s.CreatedAt, s.UpdatedAt, s.DeletedAt,
		)
		return err
	})
}

// GetByConceptRung returns the live set for one (learner, concept, rung).
func (r *CampaignQuestionSetRepo) GetByConceptRung(ctx context.Context, tenantID, learnerGCID, conceptID string, rung int) (*cq.QuestionSet, error) {
	return r.getOne(ctx, getCampaignQuestionSetSQL, tenantID, learnerGCID, conceptID, rung)
}

// GetByAssistID resolves the set a qgen terminal event keys on.
func (r *CampaignQuestionSetRepo) GetByAssistID(ctx context.Context, tenantID, assistID string) (*cq.QuestionSet, error) {
	return r.getOne(ctx, getCampaignQuestionSetByAssistSQL, assistID, tenantID)
}

// CountRequestedOn counts the learner's generation requests for one UTC day.
func (r *CampaignQuestionSetRepo) CountRequestedOn(ctx context.Context, tenantID, learnerGCID string, origin cq.RequestOrigin, day time.Time) (int, error) {
	n := 0
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, countCampaignQuestionRequestsSQL,
			tenantID, learnerGCID, doseclock.Bucket(day), string(origin))
		if row == nil {
			return nil // stub Querier — empty
		}
		if err := row.Scan(&n); err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		return nil
	})
	return n, err
}

func (r *CampaignQuestionSetRepo) getOne(ctx context.Context, sql string, args ...any) (*cq.QuestionSet, error) {
	var out *cq.QuestionSet
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, sql, args...)
		if row == nil {
			return nil // stub Querier — not found
		}
		var s cq.QuestionSet
		var status, origin string
		if err := row.Scan(
			&s.ID, &s.TenantID, &s.LearnerGCID, &s.ConceptID, &s.ConceptKey, &s.Rung,
			&s.RetrievedAtomIDs, &s.RetrievalCheckedAt,
			&status, &s.AssistID, &s.RequestedOn, &origin,
			&s.QuestionsPayload, &s.FailureReason,
			&s.CreatedAt, &s.UpdatedAt, &s.DeletedAt,
		); err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return nil // not found — the caller mints lazily / ack-skips
			}
			return err
		}
		s.GenerationStatus = cq.Status(status)
		s.RequestOrigin = cq.RequestOrigin(origin)
		out = &s
		return nil
	})
	return out, err
}
