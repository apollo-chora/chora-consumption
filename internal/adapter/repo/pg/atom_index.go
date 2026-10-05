// atom_index.go — Postgres adapter for the atom_index projection.
// Hydrated from chora.creation.atom.created.v1 events. Read path:
// MCQ grading + topic resolution. Write path: AtomCreatedSubscriber.
package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// AtomIndexRepo is the Postgres-backed projection repo.
type AtomIndexRepo struct {
	tx TxRunner
}

// NewAtomIndexRepo constructs an atom_index repo.
func NewAtomIndexRepo(tx TxRunner) *AtomIndexRepo {
	return &AtomIndexRepo{tx: tx}
}

// Compile-time check: satisfies the same port the inmem adapter implements.
var _ atom_index.Repo = (*AtomIndexRepo)(nil)

// Save upserts an atom_index entry.
//
// SQL:
//
//	INSERT INTO atom_index (atom_id, tenant_id, course_id, title,
//	    atom_type, difficulty, topic_tags, correct_option_id, answer_count,
//	    published_at)
//	VALUES ($1..$10)
//	ON CONFLICT (atom_id) DO UPDATE SET ...
func (r *AtomIndexRepo) Save(ctx context.Context, a *atom_index.AtomIndex) error {
	if a == nil {
		return atom_index.ErrInvalidAtom
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		// nullableUUID: standalone atoms carry CourseID == "" — binding the
		// empty string into the uuid column fails 22P02 (live 2026-06-10:
		// every standalone atom's upsert dead-ended, starving the fog
		// candidate catalogue). course_id is nullable per migration 0049.
		//
		// status: normalise an empty Status to draft (mirrors the domain New
		// default + the column DEFAULT 'draft') so an unflipped projection is
		// never persisted with a blank, never-playable status. CHO-1968.
		status := string(a.Status)
		if status == "" {
			status = string(atom_index.StatusDraft)
		}
		_, err := q.Exec(ctx, upsertAtomIndexSQL,
			a.AtomID, a.TenantID, nullableUUID(a.CourseID), a.Title, a.AtomType,
			a.Difficulty, a.TopicTags, a.CorrectOptionID, a.AnswerCount, a.PublishedAt, status,
			a.CognitiveLevel,
		)
		return err
	})
}

// Get returns the entry by atom_id, or nil + atom_index.ErrNotFound. The
// pgx no-rows condition maps to the DOMAIN sentinel (same contract as the
// inmem adapter) so callers can distinguish "unprojected" from a storage
// failure and fail loud on the latter.
//
// SQL: SELECT ... FROM atom_index WHERE atom_id = $1 AND deleted_at IS NULL;
func (r *AtomIndexRepo) Get(ctx context.Context, atomID string) (*atom_index.AtomIndex, error) {
	var out *atom_index.AtomIndex
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadAtomIndexSQL, atomID)
		if row == nil {
			return atom_index.ErrNotFound // stub Querier returns nil — not-found
		}
		var a atom_index.AtomIndex
		var status string
		if err := row.Scan(
			&a.AtomID, &a.TenantID, &a.CourseID, &a.Title, &a.AtomType,
			&a.Difficulty, &a.TopicTags, &a.CorrectOptionID, &a.AnswerCount, &a.PublishedAt, &status,
			&a.CognitiveLevel,
		); err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return atom_index.ErrNotFound
			}
			return err
		}
		a.Status = atom_index.Status(status)
		out = &a
		return nil
	})
	return out, err
}

// ListByCourse returns all live atoms for a (tenant, course) pair, excluding
// soft-deleted, ordered by published_at ASC.
//
// SQL: SELECT ... FROM atom_index WHERE tenant_id = $1 AND course_id = $2
//
//	AND deleted_at IS NULL ORDER BY published_at ASC;
func (r *AtomIndexRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*atom_index.AtomIndex, error) {
	out := make([]*atom_index.AtomIndex, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listAtomIndexByCourseSQL, tenantID, courseID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var a atom_index.AtomIndex
			var status string
			if err := rows.Scan(
				&a.AtomID, &a.TenantID, &a.CourseID, &a.Title, &a.AtomType,
				&a.Difficulty, &a.TopicTags, &a.CorrectOptionID, &a.AnswerCount, &a.PublishedAt, &status,
				&a.CognitiveLevel,
			); err != nil {
				return err
			}
			a.Status = atom_index.Status(status)
			out = append(out, &a)
		}
		return rows.Err()
	})
	return out, err
}

// searchForLearnerDefaultLimit / searchForLearnerMaxLimit clamp the
// SearchForLearner result size; they mirror the inmem adapter constants so both
// adapters honour the same Repo.SearchForLearner contract.
const (
	searchForLearnerDefaultLimit = 5
	searchForLearnerMaxLimit     = 10
)

// SearchForLearner returns up to `limit` live atoms for a tenant ordered
// most-recently-published first, biasing topic-matching atoms ahead of the
// rest when topicHint is set. Reads ONLY the local atom_index projection
// (cross-DB FORBIDDEN). RLS-scoped. This is the read surface behind the
// Consumption gRPC RecommendAtomsForLearner RPC the AI Kernel recommender crew
// calls for REAL RAG retrieval.
//
// The topic bias is computed in SQL: an atom whose topic_tags overlap the hint
// (case-insensitive, either-direction substring) sorts before non-matching
// atoms; within each partition, published_at DESC then atom_id ASC give a
// stable order. An empty topicHint degrades to pure recency. Binding order is
// (tenant_id, topic_hint, limit) — the limit is intentionally the LAST arg.
func (r *AtomIndexRepo) SearchForLearner(ctx context.Context, tenantID, topicHint string, limit int) ([]*atom_index.AtomIndex, error) {
	if limit <= 0 {
		limit = searchForLearnerDefaultLimit
	}
	if limit > searchForLearnerMaxLimit {
		limit = searchForLearnerMaxLimit
	}
	out := make([]*atom_index.AtomIndex, 0, limit)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, searchAtomIndexForLearnerSQL, tenantID, topicHint, limit)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var a atom_index.AtomIndex
			var status string
			if err := rows.Scan(
				&a.AtomID, &a.TenantID, &a.CourseID, &a.Title, &a.AtomType,
				&a.Difficulty, &a.TopicTags, &a.CorrectOptionID, &a.AnswerCount, &a.PublishedAt, &status,
				&a.CognitiveLevel,
			); err != nil {
				return err
			}
			a.Status = atom_index.Status(status)
			out = append(out, &a)
		}
		return rows.Err()
	})
	return out, err
}

// MarkPublished applies the atom.published flip: it sets ONLY status + the MCQ
// answer key (atom_type / correct_option_id / answer_count) for one atom,
// preserving title / topic_tags / course_id / difficulty (a targeted UPDATE —
// mirrors hexagon.go MarkInvalidatedByFocal). The answer key comes from the
// PUBLISHED event because the atom.created row's key is empty/unreliable — that
// is the whole point. status uses a non-downgrading CASE so a concurrent
// already-published row is never reverted. CHO-1968. RLS-scoped.
func (r *AtomIndexRepo) MarkPublished(ctx context.Context, atomID string, st atom_index.Status, atomType, correctOptionID string, answerCount int, cognitiveLevel string) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, markPublishedAtomIndexSQL, atomID, string(st), atomType, correctOptionID, answerCount, cognitiveLevel)
		return err
	})
}

// sqlLimit returns the LIMIT clause value for a SQL query. PostgreSQL treats
// LIMIT NULL = unbounded, so we send nil when the caller passes <= 0. Shared
// by sibling pg adapters (learning_path, topic_retention, atom_semantic_edge).
func sqlLimit(limit int) any {
	if limit <= 0 {
		return nil
	}
	return limit
}

const (
	upsertAtomIndexSQL = `
		INSERT INTO atom_index (
			atom_id, tenant_id, course_id, title, atom_type, difficulty,
			topic_tags, correct_option_id, answer_count, published_at, status,
			cognitive_level
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''))
		ON CONFLICT (atom_id) DO UPDATE SET
			title             = EXCLUDED.title,
			atom_type         = EXCLUDED.atom_type,
			difficulty        = EXCLUDED.difficulty,
			-- NON-BLANKING (18d, live 2026-07-09): the create step always emits an
			-- atom.created whose Event struct carries NO tags. If that tagless event
			-- is delivered (or redelivered) AFTER the tagged composer event
			-- (question POST/PATCH, batch accept), an unconditional overwrite blanked
			-- the projected topic_tags → PrimaryTopic() went "" → the answer-keyed MCQ
			-- silently dropped out of the answerable pick + dose (the guarded status +
			-- key survived, so only the topic vanished — a hard-to-spot half-blank).
			-- Overwrite topic_tags ONLY when the incoming (EXCLUDED) set is non-empty;
			-- else keep the existing tags. A tagged composer event still updates them.
			topic_tags        = CASE WHEN COALESCE(array_length(EXCLUDED.topic_tags, 1), 0) = 0
			                         THEN atom_index.topic_tags ELSE EXCLUDED.topic_tags END,
			published_at      = EXCLUDED.published_at,
			-- NON-DOWNGRADING (CHO-1968): a late/out-of-order atom.created (draft,
			-- EMPTY key) replaying AFTER the atom.published flip must NOT revert a
			-- published row's status NOR clobber its authoritative answer key (the
			-- key came from atom.published; clobbering it blanks correct_option_id
			-- → IsMCQ() goes false → the atom silently drops out of the dose).
			-- status + the answer key are preserved when the existing row is
			-- already published; everything else takes the incoming EXCLUDED value.
			status            = CASE WHEN atom_index.status = 'published'
			                         THEN atom_index.status ELSE EXCLUDED.status END,
			correct_option_id = CASE WHEN atom_index.status = 'published'
			                         THEN atom_index.correct_option_id ELSE EXCLUDED.correct_option_id END,
			answer_count      = CASE WHEN atom_index.status = 'published'
			                         THEN atom_index.answer_count ELSE EXCLUDED.answer_count END,
			-- A known level is never clobbered by a blank replay (WS-C3):
			-- levels only ever improve from unknown -> known.
			cognitive_level   = COALESCE(EXCLUDED.cognitive_level, atom_index.cognitive_level)`

	// markPublishedAtomIndexSQL is the atom.published flip: a TARGETED UPDATE of
	// status + the MCQ answer key only. status is non-downgrading (never reverts
	// an already-published row). Title / topic_tags / course_id are preserved.
	markPublishedAtomIndexSQL = `
		UPDATE atom_index SET
			status            = CASE WHEN status = 'published' THEN status ELSE $2 END,
			atom_type         = $3,
			correct_option_id = $4,
			answer_count      = $5,
			cognitive_level   = COALESCE(NULLIF($6, ''), cognitive_level)
		WHERE atom_id = $1`

	// Read SQLs project COALESCE(course_id::text, '') — course_id is NULLABLE
	// (standalone atoms have no owning course) and the domain field is a plain
	// string; a bare NULL scan would fail. status is projected last (scanned into
	// a string then converted to atom_index.Status — the pgx named-string idiom).
	loadAtomIndexSQL = `
		SELECT atom_id, tenant_id, COALESCE(course_id::text, ''), title, atom_type, difficulty,
			topic_tags, correct_option_id, answer_count, published_at, status,
			COALESCE(cognitive_level, '')
		FROM atom_index
		WHERE atom_id = $1 AND deleted_at IS NULL`

	listAtomIndexByCourseSQL = `
		SELECT atom_id, tenant_id, COALESCE(course_id::text, ''), title, atom_type, difficulty,
			topic_tags, correct_option_id, answer_count, published_at, status,
			COALESCE(cognitive_level, '')
		FROM atom_index
		WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
		ORDER BY published_at ASC`

	// searchAtomIndexForLearnerSQL ranks live tenant atoms: topic-hint matches
	// first (rank 1), then the rest (rank 0) — each partition by recency DESC,
	// atom_id ASC as a stable tiebreak. An empty $2 (no hint) collapses every
	// row to rank 0 ⇒ pure recency. Topic match = any topic tag overlaps the
	// hint case-insensitively in EITHER direction (tag contains hint OR hint
	// contains tag), so "scrum" matches a "scrum-basics" tag and vice versa.
	// $3 = LIMIT (clamped by the caller).
	//
	// CHO-2273 (SECURITY) — status = 'published' only. This is a LEARNER feed
	// (the RecommendAtomsForLearner recommender); an unpublished DRAFT is author
	// WIP and must never be surfaced or taken, matching the dose's Playable()
	// filter (daily_dose_seeds.go). Without this, drafts leaked into the learner
	// recommender the same way they were startable/gradeable pre-CHO-2273.
	searchAtomIndexForLearnerSQL = `
		SELECT atom_id, tenant_id, COALESCE(course_id::text, ''), title, atom_type, difficulty,
			topic_tags, correct_option_id, answer_count, published_at, status,
			COALESCE(cognitive_level, '')
		FROM atom_index
		WHERE tenant_id = $1 AND deleted_at IS NULL AND status = 'published'
		ORDER BY
			CASE WHEN $2 <> '' AND EXISTS (
				SELECT 1 FROM unnest(topic_tags) AS tag
				WHERE lower(tag) LIKE '%' || lower($2) || '%'
				   OR lower($2) LIKE '%' || lower(tag) || '%'
			) THEN 1 ELSE 0 END DESC,
			published_at DESC,
			atom_id ASC
		LIMIT $3`
)
