// learner_weakness.go — Postgres (pgvector) adapter for the Growth-Edge
// (learner_weakness.Repository) port. Schema mirrors
// migrations/0046_learner_weakness.up.sql.
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the domain
// query; per-learner scoping is an explicit learner_gcid predicate. The 768-d
// concept_embedding is bound as a Postgres text literal `[a,b,...]` cast with
// `$N::vector` (no pgx binary codec wired) — identical to the F4 companion memory
// + chora-creation atom_embeddings paths. The descriptor JSONB is marshalled to
// a string and cast `$N::jsonb`.
//
// Upsert applies embedding dedup IN-TX: find the nearest non-deleted edge for the
// learner; if its cosine distance <= DedupCosineDistanceMax, load + domain-Merge
// + UPDATE; otherwise New + INSERT a sibling. The Merge decision lives in the
// pure domain (learner_weakness.Merge) — this adapter only orchestrates.
package pg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const (
	defaultListPageSize = 20
	maxListPageSize     = 100
)

// LearnerWeaknessRepo is the pgvector-backed learner_weakness.Repository.
type LearnerWeaknessRepo struct {
	tx TxRunner
}

// NewLearnerWeaknessRepo constructs the repo around a TxRunner.
func NewLearnerWeaknessRepo(tx TxRunner) *LearnerWeaknessRepo {
	return &LearnerWeaknessRepo{tx: tx}
}

var (
	_ lw.Repository            = (*LearnerWeaknessRepo)(nil)
	_ lw.WeaknessContextLoader = (*LearnerWeaknessRepo)(nil) // ADR-247 F2 narrow read port
)

// ---------------------------------------------------------------------------
// Upsert — dedup-fold (find-nearest -> merge | insert)
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) Upsert(ctx context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	// Validate + normalise up-front (pure, fails fast); reused on the insert path.
	fresh, err := lw.New(in)
	if err != nil {
		return lw.UpsertResult{}, err
	}
	embLit := vectorLiteral(fresh.Embedding)

	var res lw.UpsertResult
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}

		// 1. Nearest non-deleted edge for this learner.
		var nearestID string
		var distance float64
		found := false
		rows, err := q.Query(ctx, nearestActiveLearnerWeaknessSQL, in.LearnerGCID, embLit)
		if err != nil {
			return err
		}
		if rows != nil {
			if rows.Next() {
				if err := rows.Scan(&nearestID, &distance); err != nil {
					rows.Close()
					return fmt.Errorf("pg: scan nearest learner_weakness: %w", err)
				}
				found = true
			}
			rows.Close()
		}

		// 2. Within the dedup radius -> load + domain-merge + UPDATE.
		if found && distance <= lw.DedupCosineDistanceMax {
			existing, err := scanLearnerWeakness(q.QueryRow(ctx, getLearnerWeaknessSQL, nearestID, in.LearnerGCID))
			if err != nil {
				return fmt.Errorf("pg: load nearest learner_weakness: %w", err)
			}
			existing.LearnerGCID = in.LearnerGCID
			existing.Merge(in)
			if _, err := q.Exec(ctx, updateLearnerWeaknessSQL,
				existing.ID,
				existing.Strength,
				sourcesToStrings(existing.Sources),
				existing.Tags,
				nullableText(existing.Category),
				nullableUUID(existing.TopicID),
				marshalDescriptor(existing.Descriptor),
				string(existing.Status),
				existing.LastEvidencedAt,
				existing.UpdatedAt,
				existing.TargetConceptID,
			); err != nil {
				return fmt.Errorf("pg: update learner_weakness: %w", err)
			}
			res = lw.UpsertResult{ID: existing.ID, Merged: true}
			return nil
		}

		// 3. New sibling edge.
		if _, err := q.Exec(ctx, insertLearnerWeaknessSQL,
			fresh.ID,
			fresh.TenantID,
			fresh.LearnerGCID,
			fresh.ConceptKey,
			fresh.ConceptLabel,
			embLit,
			nullableUUID(fresh.TopicID),
			nullableText(fresh.Category),
			fresh.Tags,
			fresh.Strength,
			sourcesToStrings(fresh.Sources),
			marshalDescriptor(fresh.Descriptor),
			string(fresh.Status),
			fresh.FirstSeenAt,
			fresh.LastEvidencedAt,
			fresh.UpdatedAt,
			fresh.TargetConceptID,
		); err != nil {
			return fmt.Errorf("pg: insert learner_weakness: %w", err)
		}
		res = lw.UpsertResult{ID: fresh.ID, Merged: false}
		return nil
	})
	if err != nil {
		return lw.UpsertResult{}, err
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// List — filter + sort + paginate
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) List(ctx context.Context, query lw.ListQuery) (lw.ListResult, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = defaultListPageSize
	}
	if pageSize > maxListPageSize {
		pageSize = maxListPageSize
	}
	offset := decodeOffsetToken(query.PageToken)

	where, args := buildLearnerWeaknessWhere(query)
	var sb strings.Builder
	sb.WriteString("SELECT")
	sb.WriteString(selectLearnerWeaknessColumns)
	sb.WriteString(" FROM learner_weakness ")
	sb.WriteString(where)
	sb.WriteString(" ORDER BY ")
	sb.WriteString(orderByClause(query.Sort))
	args = append(args, pageSize+1)
	limIdx := len(args)
	args = append(args, offset)
	offIdx := len(args)
	fmt.Fprintf(&sb, " LIMIT $%d OFFSET $%d", limIdx, offIdx)
	sql := sb.String()

	var out []lw.LearnerWeakness
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		out, err = scanLearnerWeaknessRows(rows, query.TenantID, query.LearnerGCID)
		return err
	})
	if err != nil {
		return lw.ListResult{}, err
	}

	next := ""
	if len(out) > pageSize {
		out = out[:pageSize]
		next = encodeOffsetToken(offset + pageSize)
	}
	return lw.ListResult{Items: out, NextPageToken: next}, nil
}

// ---------------------------------------------------------------------------
// ListAll — full filtered set, sorted, NO pagination (rollup feed)
// ---------------------------------------------------------------------------

// listAllSafetyCap bounds the unpaginated rollup feed. Per-learner Growth-Edge
// counts are small (dozens); this cap only guards against a pathological row
// explosion — it is far above any realistic learner edge count, so it never
// truncates a real result set.
const listAllSafetyCap = 1000

func (r *LearnerWeaknessRepo) ListAll(ctx context.Context, query lw.ListQuery) ([]lw.LearnerWeakness, error) {
	where, args := buildLearnerWeaknessWhere(query)
	var sb strings.Builder
	sb.WriteString("SELECT")
	sb.WriteString(selectLearnerWeaknessColumns)
	sb.WriteString(" FROM learner_weakness ")
	sb.WriteString(where)
	sb.WriteString(" ORDER BY ")
	sb.WriteString(orderByClause(query.Sort))
	args = append(args, listAllSafetyCap)
	fmt.Fprintf(&sb, " LIMIT $%d", len(args)) // no OFFSET — the caller (RollupPage) paginates
	sql := sb.String()

	var out []lw.LearnerWeakness
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		out, err = scanLearnerWeaknessRows(rows, query.TenantID, query.LearnerGCID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// buildLearnerWeaknessWhere builds the shared "WHERE ... [filters]" clause + its
// positional args ($1 = learner_gcid) for both List + ListAll. Per multi-tenant-
// rls the tenant predicate is enforced by RLS; the learner predicate is explicit.
func buildLearnerWeaknessWhere(query lw.ListQuery) (string, []any) {
	args := []any{query.LearnerGCID}
	var sb strings.Builder
	sb.WriteString("WHERE learner_gcid = $1 AND deleted_at IS NULL")
	if !query.IncludeGrown {
		sb.WriteString(" AND status <> 'grown'")
	}
	if c := strings.TrimSpace(query.Category); c != "" {
		args = append(args, c)
		fmt.Fprintf(&sb, " AND category = $%d", len(args))
	}
	if tid := strings.TrimSpace(query.TopicID); tid != "" {
		args = append(args, tid)
		fmt.Fprintf(&sb, " AND topic_id = $%d::uuid", len(args))
	}
	if query.MinStrength > 0 {
		args = append(args, query.MinStrength)
		fmt.Fprintf(&sb, " AND strength >= $%d", len(args))
	}
	if tags := cleanQueryTags(query.Tags); len(tags) > 0 {
		args = append(args, tags)
		fmt.Fprintf(&sb, " AND tags && $%d", len(args))
	}
	return sb.String(), args
}

// scanLearnerWeaknessRows drains a List/ListAll cursor into domain aggregates,
// stamping the (RLS-implicit) tenant + learner scope onto each row.
func scanLearnerWeaknessRows(rows Rows, tenantID, learnerGCID string) ([]lw.LearnerWeakness, error) {
	out := make([]lw.LearnerWeakness, 0)
	if rows == nil {
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		w, err := scanLearnerWeakness(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: scan learner_weakness: %w", err)
		}
		w.TenantID = tenantID
		w.LearnerGCID = learnerGCID
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) Get(ctx context.Context, learnerGCID, id string) (*lw.LearnerWeakness, error) {
	var out *lw.LearnerWeakness
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		w, err := scanLearnerWeakness(q.QueryRow(ctx, getLearnerWeaknessSQL, id, learnerGCID))
		if errors.Is(err, ErrNoRows) {
			return nil // not found -> nil, nil
		}
		if err != nil {
			return fmt.Errorf("pg: get learner_weakness: %w", err)
		}
		w.LearnerGCID = learnerGCID
		out = &w
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// LoadWeaknessContextByConceptKey - ADR-247 (F2) weakness-load read helper
// ---------------------------------------------------------------------------

// WeaknessContext is the domain read-projection (lw.WeaknessContext, ADR-247
// F2); this adapter maps a loaded Growth Edge into it. The type lives in the
// learner_weakness domain so the Repository port can expose the loader to the
// KG-generation consumers without an adapter dependency.

// LoadWeaknessContextByConceptKey returns the learner's explicit diagnosed
// weakness for one concept_key, or (nil, nil) when there is none. Only edges
// carrying the `explicit` source (the marked-test / upload Diagnose lane,
// ADR-238 D4) match; derived / classroom / ceremony-only seeds are inferred or
// declared, not diagnosed, so they never surface here. Tenant isolation is
// RLS-enforced from ctx (the tenantID param mirrors the sibling read methods and
// is not a second filter); the explicit learner_gcid predicate + deleted_at IS
// NULL soft-delete filter scope the read exactly as the other methods do.
// (nil, nil) on no-match lets callers degrade cleanly.
func (r *LearnerWeaknessRepo) LoadWeaknessContextByConceptKey(ctx context.Context, tenantID, learnerGCID, conceptKey string) (*lw.WeaknessContext, error) {
	key := lw.NormalizeConceptKey(conceptKey)
	if key == "" {
		return nil, nil // no resolvable concept -> no weakness
	}
	var out *lw.WeaknessContext
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		w, err := scanLearnerWeakness(q.QueryRow(ctx, getExplicitWeaknessByConceptKeySQL, learnerGCID, key))
		if errors.Is(err, ErrNoRows) {
			return nil // no explicit weakness -> nil, nil
		}
		if err != nil {
			return fmt.Errorf("pg: load explicit learner_weakness by concept_key: %w", err)
		}
		out = weaknessContextFrom(w)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// weaknessContextFrom distils a loaded Growth Edge into the weakness-load
// context. Slices are defensively copied/rebuilt so the caller never aliases the
// aggregate's backing arrays.
func weaknessContextFrom(w lw.LearnerWeakness) *lw.WeaknessContext {
	return &lw.WeaknessContext{
		Descriptor:     w.Descriptor.Summary,
		Misconceptions: append([]string(nil), w.Descriptor.Misconceptions...),
		Evidence:       sampleWrongEvidence(w.Descriptor.SampleWrong),
	}
}

// sampleWrongEvidence flattens the redacted wrong-answer exemplars into compact
// evidence lines ("prompt: why_wrong"), skipping any that carry no text. Mirrors
// the FE diagnosis drawer, which renders prompt + why_wrong (the `chosen` option
// is dropped).
func sampleWrongEvidence(samples []lw.SampleWrong) []string {
	out := make([]string, 0, len(samples))
	for _, s := range samples {
		prompt := strings.TrimSpace(s.Prompt)
		why := strings.TrimSpace(s.WhyWrong)
		switch {
		case prompt != "" && why != "":
			out = append(out, prompt+": "+why)
		case why != "":
			out = append(out, why)
		case prompt != "":
			out = append(out, prompt)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// SoftDelete
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) SoftDelete(ctx context.Context, learnerGCID, id string, now time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, softDeleteLearnerWeaknessSQL, id, learnerGCID, now); err != nil {
			return fmt.Errorf("pg: soft-delete learner_weakness: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// RecoverByConceptKey — W3-derived Ebbinghaus recovery (only-lower)
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) RecoverByConceptKey(ctx context.Context, learnerGCID, conceptKey string, strength float64, now time.Time) ([]lw.GrownEdge, error) {
	key := lw.NormalizeConceptKey(conceptKey)
	if key == "" {
		return nil, fmt.Errorf("pg: recover learner_weakness: concept_key required")
	}
	var grown []lw.GrownEdge
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		grown = grown[:0] // reset on tx retry so transitions are never double-reported
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		w, err := scanLearnerWeakness(q.QueryRow(ctx, getLearnerWeaknessByConceptKeySQL, learnerGCID, key))
		if errors.Is(err, ErrNoRows) {
			return nil // nothing to recover
		}
		if err != nil {
			return fmt.Errorf("pg: load learner_weakness by concept_key: %w", err)
		}
		w.LearnerGCID = learnerGCID
		wasActive := w.Status == lw.StatusActive
		if !w.Recover(strength, now) {
			return nil // only-lower semantics — a would-be raise is a no-op
		}
		if _, err := q.Exec(ctx, recoverLearnerWeaknessSQL, w.ID, w.Strength, string(w.Status), now); err != nil {
			return fmt.Errorf("pg: recover learner_weakness: %w", err)
		}
		if wasActive && w.IsGrown() {
			grown = append(grown, grownEdgeFrom(&w, now))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return grown, nil
}

// ---------------------------------------------------------------------------
// RecoverByDrillAtomID — W4 drill-completion recovery (only-lower, multi-edge)
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) RecoverByDrillAtomID(ctx context.Context, learnerGCID, atomID string, strength float64, now time.Time) ([]lw.GrownEdge, error) {
	atomID = strings.TrimSpace(atomID)
	if atomID == "" {
		return nil, fmt.Errorf("pg: recover by drill atom: atom_id required")
	}
	var grown []lw.GrownEdge
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		grown = grown[:0] // reset on tx retry so transitions are never double-reported
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, getLearnerWeaknessByDrillAtomSQL, learnerGCID, atomID)
		if err != nil {
			return err
		}
		// Read all matching edges first, then close the cursor BEFORE any write —
		// the recovery UPDATEs must not race the open read on the same tx conn.
		var edges []lw.LearnerWeakness
		if rows != nil {
			for rows.Next() {
				w, err := scanLearnerWeakness(rows)
				if err != nil {
					rows.Close()
					return fmt.Errorf("pg: scan drill-atom learner_weakness: %w", err)
				}
				w.LearnerGCID = learnerGCID
				edges = append(edges, w)
			}
			rerr := rows.Err()
			rows.Close()
			if rerr != nil {
				return fmt.Errorf("pg: iterate drill-atom learner_weakness: %w", rerr)
			}
		}
		for i := range edges {
			w := &edges[i]
			wasActive := w.Status == lw.StatusActive
			if !w.Recover(strength, now) {
				continue // only-lower semantics — a would-be raise is a no-op
			}
			if _, err := q.Exec(ctx, recoverLearnerWeaknessSQL, w.ID, w.Strength, string(w.Status), now); err != nil {
				return fmt.Errorf("pg: recover drill-atom learner_weakness: %w", err)
			}
			if wasActive && w.IsGrown() {
				grown = append(grown, grownEdgeFrom(w, now))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return grown, nil
}

// grownEdgeFrom projects a just-recovered edge that crossed active→grown into the
// domain GrownEdge the recover methods report (ADR-196). now is the recovery
// timestamp persisted on the row (== w.UpdatedAt after Recover).
func grownEdgeFrom(w *lw.LearnerWeakness, now time.Time) lw.GrownEdge {
	tags := append([]string(nil), w.Tags...) // defensive copy — caller owns it
	return lw.GrownEdge{
		ID:            w.ID,
		ConceptKey:    w.ConceptKey,
		ConceptLabel:  w.ConceptLabel,
		FinalStrength: w.Strength,
		Tags:          tags,
		GrownAt:       now,
	}
}

// ---------------------------------------------------------------------------
// SetCachedDrillAtoms — W4 drill-atom cache refresh
// ---------------------------------------------------------------------------

func (r *LearnerWeaknessRepo) SetCachedDrillAtoms(ctx context.Context, learnerGCID, id string, atomIDs []string, now time.Time) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("pg: set cached drill atoms: id required")
	}
	if atomIDs == nil {
		atomIDs = []string{}
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, setCachedDrillAtomsSQL, id, learnerGCID, atomIDs, now); err != nil {
			return fmt.Errorf("pg: set cached drill atoms: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// scan + marshalling helpers
// ---------------------------------------------------------------------------

// scanner is satisfied by both Row and Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanLearnerWeakness reads selectLearnerWeaknessColumns into a domain aggregate.
// category/topic_id/target_concept_id come COALESCE'd to ” (NULL -> empty string);
// tags/sources/cached_drill_atom_ids are text[]/uuid[] -> []string; descriptor is
// jsonb::text.
func scanLearnerWeakness(sc scanner) (lw.LearnerWeakness, error) {
	var (
		w              lw.LearnerWeakness
		category       string
		topicID        string
		descriptorJSON string
		status         string
		tags           []string
		sources        []string
		cached         []string
	)
	if err := sc.Scan(
		&w.ID, &w.ConceptKey, &w.ConceptLabel, &category, &topicID,
		&tags, &w.Strength, &sources, &descriptorJSON, &cached, &status,
		&w.FirstSeenAt, &w.LastEvidencedAt, &w.TargetConceptID,
	); err != nil {
		return lw.LearnerWeakness{}, err
	}
	w.Category = category
	w.TopicID = topicID
	w.Tags = tags
	w.Sources = stringsToSources(sources)
	w.CachedDrillAtomIDs = cached
	w.Descriptor = unmarshalDescriptor(descriptorJSON)
	w.Status = lw.Status(status)
	return w, nil
}

func sourcesToStrings(ss []lw.Source) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}

func stringsToSources(ss []string) []lw.Source {
	if len(ss) == 0 {
		return nil
	}
	out := make([]lw.Source, len(ss))
	for i, s := range ss {
		out[i] = lw.Source(s)
	}
	return out
}

func marshalDescriptor(d lw.Descriptor) string {
	b, err := json.Marshal(d)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalDescriptor(s string) lw.Descriptor {
	var d lw.Descriptor
	if strings.TrimSpace(s) == "" {
		return d
	}
	_ = json.Unmarshal([]byte(s), &d)
	return d
}

// nullableText returns nil for a blank string so a NULL is stored (not ”).
func nullableText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func cleanQueryTags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func orderByClause(s lw.ListSort) string {
	switch s {
	case lw.SortLastEvidencedDesc:
		return "last_evidenced_at DESC, id DESC"
	case lw.SortFirstSeenDesc:
		return "first_seen_at DESC, id DESC"
	default: // SortStrengthDesc + unset
		return "strength DESC, id DESC"
	}
}

// encodeOffsetToken / decodeOffsetToken — opaque offset cursor. Per-learner edge
// counts are small (dozens), so offset paging is correct + sufficient; a keyset
// cursor is a future optimisation if counts grow.
func encodeOffsetToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset)))
}

func decodeOffsetToken(tok string) int {
	if tok == "" {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return 0
	}
	s := string(b)
	if !strings.HasPrefix(s, "o:") {
		return 0
	}
	n, err := strconv.Atoi(s[2:])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// SQL templates (review-via-test)
// ---------------------------------------------------------------------------

// selectLearnerWeaknessColumns is the shared projection for Get/List/full-load.
// category + topic_id + target_concept_id (ADR-238) are COALESCE'd to ” so a
// *string scan is safe.
const selectLearnerWeaknessColumns = ` id, concept_key, concept_label, COALESCE(category, ''), COALESCE(topic_id::text, ''),
        tags, strength, sources, descriptor::text, cached_drill_atom_ids, status,
        first_seen_at, last_evidenced_at, COALESCE(target_concept_id::text, '')`

// nearestActiveLearnerWeaknessSQL finds the single nearest non-deleted edge for a
// learner by cosine distance ($2 query embedding bound as a ::vector literal).
const nearestActiveLearnerWeaknessSQL = `
SELECT id, (concept_embedding <=> $2::vector) AS distance
  FROM learner_weakness
 WHERE learner_gcid = $1 AND deleted_at IS NULL
 ORDER BY concept_embedding <=> $2::vector
 LIMIT 1`

const getLearnerWeaknessSQL = `SELECT` + selectLearnerWeaknessColumns + `
  FROM learner_weakness
 WHERE id = $1 AND learner_gcid = $2 AND deleted_at IS NULL`

const insertLearnerWeaknessSQL = `
INSERT INTO learner_weakness
       (id, tenant_id, learner_gcid, concept_key, concept_label, concept_embedding,
        topic_id, category, tags, strength, sources, descriptor, status,
        first_seen_at, last_evidenced_at, updated_at, target_concept_id)
VALUES ($1, $2, $3, $4, $5, $6::vector,
        $7, $8, $9, $10, $11, $12::jsonb, $13,
        $14, $15, $16, NULLIF($17,'')::uuid)`

const updateLearnerWeaknessSQL = `
UPDATE learner_weakness
   SET strength = $2, sources = $3, tags = $4, category = $5, topic_id = $6,
       descriptor = $7::jsonb, status = $8, last_evidenced_at = $9, updated_at = $10,
       target_concept_id = NULLIF($11,'')::uuid
 WHERE id = $1`

// getLearnerWeaknessByConceptKeySQL loads the live edge for the (learner,
// concept_key) pair — the exact-key lookup the recovery path uses (no
// embedding round-trip). idx_learner_weakness_concept_key serves it.
const getLearnerWeaknessByConceptKeySQL = `SELECT` + selectLearnerWeaknessColumns + `
  FROM learner_weakness
 WHERE learner_gcid = $1 AND concept_key = $2 AND deleted_at IS NULL
 LIMIT 1`

// getExplicitWeaknessByConceptKeySQL loads the live, EXPLICITLY-diagnosed edge
// for the (learner, concept_key) pair: the marked-test / upload Diagnose lane
// (ADR-238 D4). `'explicit' = ANY(sources)` excludes derived / classroom /
// ceremony-only seeds; deleted_at IS NULL respects soft-delete; the tenant is
// RLS-scoped. idx_learner_weakness_concept_key serves the (learner_gcid,
// concept_key) lookup.
const getExplicitWeaknessByConceptKeySQL = `SELECT` + selectLearnerWeaknessColumns + `
  FROM learner_weakness
 WHERE learner_gcid = $1 AND concept_key = $2
       AND 'explicit' = ANY(sources) AND deleted_at IS NULL
 LIMIT 1`

// getLearnerWeaknessByDrillAtomSQL loads every live edge for a learner whose W4
// drill cache contains the completed atom — the drill-completion recovery lookup
// that lets practising a suggested atom move the edge that suggested it.
const getLearnerWeaknessByDrillAtomSQL = `SELECT` + selectLearnerWeaknessColumns + `
  FROM learner_weakness
 WHERE learner_gcid = $1 AND cached_drill_atom_ids @> ARRAY[$2]::uuid[] AND deleted_at IS NULL`

// recoverLearnerWeaknessSQL persists a domain Recover: strength only ever
// drops, status may flip to grown, both evidence timestamps advance.
const recoverLearnerWeaknessSQL = `
UPDATE learner_weakness
   SET strength = $2, status = $3, last_evidenced_at = $4, updated_at = $4
 WHERE id = $1`

// setCachedDrillAtomsSQL replaces the W4 drill cache on a live edge.
const setCachedDrillAtomsSQL = `
UPDATE learner_weakness
   SET cached_drill_atom_ids = $3::uuid[], updated_at = $4
 WHERE id = $1 AND learner_gcid = $2 AND deleted_at IS NULL`

const softDeleteLearnerWeaknessSQL = `
UPDATE learner_weakness
   SET deleted_at = $3, updated_at = $3
 WHERE id = $1 AND learner_gcid = $2 AND deleted_at IS NULL`
