package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- a Querier that records the query and returns scripted rows -----------

type pendingRows struct {
	rows [][]any
	idx  int
	err  error
}

func (m *pendingRows) Next() bool {
	if m.idx >= len(m.rows) {
		return false
	}
	m.idx++
	return true
}

func (m *pendingRows) Scan(dest ...any) error {
	src := m.rows[m.idx-1]
	if len(src) != len(dest) {
		return errors.New("pendingRows.Scan: column count mismatch")
	}
	for i, d := range dest {
		switch dst := d.(type) {
		case *string:
			*dst = src[i].(string)
		case *int:
			*dst = src[i].(int)
		case *[]string:
			*dst = src[i].([]string)
		case *time.Time:
			*dst = src[i].(time.Time)
		case **time.Time:
			// A typed nil (*time.Time)(nil) inside an interface is NOT equal
			// to nil, so the pointer case must be matched explicitly or the
			// type assertion below panics on the analyzed_at column.
			switch v := src[i].(type) {
			case nil:
				*dst = nil
			case *time.Time:
				*dst = v
			case time.Time:
				t := v
				*dst = &t
			default:
				return errors.New("pendingRows.Scan: bad analyzed_at fixture")
			}
		case *[]byte:
			if src[i] == nil {
				*dst = nil
			} else {
				*dst = src[i].([]byte)
			}
		default:
			return errors.New("pendingRows.Scan: unsupported destination")
		}
	}
	return nil
}
func (m *pendingRows) Close() error { return nil }
func (m *pendingRows) Err() error   { return m.err }

type pendingQuerier struct {
	lastSQL  string
	lastArgs []any
	rows     *pendingRows
	queryErr error
	// applied records whether rls.ApplySession ran before the read. The whole
	// point of the assertion is that a read without it returns zero rows with
	// NO error, which is indistinguishable from an empty list.
	applied bool
}

func (q *pendingQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	if strings.Contains(sql, "chora.tenant_id") {
		q.applied = true
	}
	return rls.CommandTag{RowsAffected: 1}, nil
}
func (q *pendingQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return wuRow{scan: func(...any) error { return ErrNoRows }}
}
func (q *pendingQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	q.lastSQL, q.lastArgs = sql, args
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	if q.rows == nil {
		return &pendingRows{}, nil
	}
	return q.rows, nil
}

type pendingTx struct{ q *pendingQuerier }

func (t *pendingTx) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if t.q == nil {
		t.q = &pendingQuerier{}
	}
	return fn(ctx, t.q)
}

func pendingCtx() context.Context {
	return tracing.WithGCID(tracing.WithTenantID(context.Background(), "tnt-1"), "gcid-1")
}

func awaitingRow(id, kind string, created time.Time, panel []byte) []any {
	return []any{id, "gcid-1", kind, "image/png", "gs://b/x", "AWAITING_REVIEW",
		[]string{}, 0, "", created, (*time.Time)(nil), panel, "goal-1", "concept-1"}
}

// --- tests ---------------------------------------------------------------

func TestListAwaitingReview_ReturnsOnlyParkedJobs(t *testing.T) {
	now := time.Now().UTC()
	q := &pendingQuerier{rows: &pendingRows{rows: [][]any{
		awaitingRow("u-2", "marked_test", now, []byte(`{"proposed_edges":[{"proposed_edge_id":"e-2"}]}`)),
		awaitingRow("u-1", "notes", now.Add(-time.Hour), nil),
	}}}
	repo := NewWeaknessUploadRepo(&pendingTx{q: q})

	out, err := repo.ListAwaitingReview(pendingCtx(), "gcid-1", 10)
	if err != nil {
		t.Fatalf("ListAwaitingReview: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d uploads, want 2", len(out))
	}
	for _, u := range out {
		if u.Status != wu.StatusAwaitingReview {
			t.Errorf("upload %s has status %q, want AWAITING_REVIEW", u.UploadID, u.Status)
		}
	}
	if out[0].UploadID != "u-2" {
		t.Errorf("first row = %q, want the newest (u-2)", out[0].UploadID)
	}
}

func TestListAwaitingReview_AppliesRLSBeforeReading(t *testing.T) {
	// A read that skips ApplySession returns zero rows with no error under a
	// NOBYPASSRLS role, which reads exactly like "nothing pending". This
	// assertion is the difference between an empty list and a silent leak of
	// the wrong tenant's rows.
	q := &pendingQuerier{rows: &pendingRows{}}
	if _, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", 10); err != nil {
		t.Fatalf("ListAwaitingReview: %v", err)
	}
	if !q.applied {
		t.Fatal("ListAwaitingReview must run rls.ApplySession before the query")
	}
}

func TestListAwaitingReview_ScopesToTheLearnerAndTheParkedStatus(t *testing.T) {
	// The positive control for the predicate: the query must carry BOTH the
	// learner predicate and the AWAITING_REVIEW filter, and must exclude
	// soft-deleted rows. A list that leaves any of the three out looks correct
	// on a single-learner test database and is wrong in production.
	q := &pendingQuerier{rows: &pendingRows{}}
	if _, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-7", 5); err != nil {
		t.Fatalf("ListAwaitingReview: %v", err)
	}
	flat := strings.Join(strings.Fields(q.lastSQL), " ")
	for _, want := range []string{"learner_gcid = $1", "status = 'AWAITING_REVIEW'", "deleted_at IS NULL", "FROM weakness_doc_uploads"} {
		if !strings.Contains(flat, want) {
			t.Errorf("query must contain %q, got %q", want, flat)
		}
	}
	if len(q.lastArgs) < 1 || q.lastArgs[0] != "gcid-7" {
		t.Errorf("first arg = %v, want the learner gcid", q.lastArgs)
	}
}

func TestListAwaitingReview_HydratesTheReviewPanel(t *testing.T) {
	// The panel is the whole payload of the card: without it the FE knows a
	// review is pending and cannot render it.
	now := time.Now().UTC()
	q := &pendingQuerier{rows: &pendingRows{rows: [][]any{
		awaitingRow("u-1", "marked_test", now, []byte(`{"proposed_edges":[{"proposed_edge_id":"edge-9","concept_label":"Recursion"}]}`)),
	}}}
	out, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", 10)
	if err != nil {
		t.Fatalf("ListAwaitingReview: %v", err)
	}
	if out[0].Review == nil {
		t.Fatal("review panel must be hydrated for a parked job")
	}
	if len(out[0].Review.ProposedEdges) != 1 {
		t.Fatalf("got %d proposed edges, want 1", len(out[0].Review.ProposedEdges))
	}
	if out[0].Review.ProposedEdges[0].ProposedEdgeID != "edge-9" {
		t.Errorf("ProposedEdgeID = %q, want edge-9", out[0].Review.ProposedEdges[0].ProposedEdgeID)
	}
}

func TestListAwaitingReview_CorruptPanelFailsLoud(t *testing.T) {
	// Serving an empty panel for a row whose JSONB will not parse would show
	// the learner a review with nothing in it and no way to tell why.
	now := time.Now().UTC()
	q := &pendingQuerier{rows: &pendingRows{rows: [][]any{
		awaitingRow("u-1", "notes", now, []byte(`{not json`)),
	}}}
	if _, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", 10); err == nil {
		t.Fatal("a corrupt review_payload must fail loud, not serve an empty panel")
	}
}

func TestListAwaitingReview_QueryErrorIsReturned(t *testing.T) {
	q := &pendingQuerier{queryErr: errors.New("connection refused")}
	_, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", 10)
	if err == nil {
		t.Fatal("expected the query error to surface")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error must carry the cause, got %q", err)
	}
}

func TestListAwaitingReview_RowsErrIsReturned(t *testing.T) {
	// An iteration that stops half way must not look like a short list.
	now := time.Now().UTC()
	q := &pendingQuerier{rows: &pendingRows{
		rows: [][]any{awaitingRow("u-1", "notes", now, nil)},
		err:  errors.New("iteration aborted"),
	}}
	if _, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", 10); err == nil {
		t.Fatal("expected the rows error to surface")
	}
}

func TestListAwaitingReview_EmptyIsAnEmptySliceNotNil(t *testing.T) {
	// The handler serialises this straight to JSON; nil would render `null`
	// where the contract says `[]`.
	q := &pendingQuerier{rows: &pendingRows{}}
	out, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", 10)
	if err != nil {
		t.Fatalf("ListAwaitingReview: %v", err)
	}
	if out == nil {
		t.Fatal("empty result must be an empty slice, not nil")
	}
	if len(out) != 0 {
		t.Fatalf("got %d rows, want 0", len(out))
	}
}

func TestListAwaitingReview_LimitIsBoundedAndDefaulted(t *testing.T) {
	// An unbounded list is a denial-of-service on the learner's own home page.
	// Zero or negative means "use the default"; anything above the cap clamps.
	for _, tc := range []struct{ in, want int }{{0, defaultPendingReviewLimit}, {-5, defaultPendingReviewLimit}, {3, 3}, {9999, maxPendingReviewLimit}} {
		q := &pendingQuerier{rows: &pendingRows{}}
		if _, err := NewWeaknessUploadRepo(&pendingTx{q: q}).ListAwaitingReview(pendingCtx(), "gcid-1", tc.in); err != nil {
			t.Fatalf("ListAwaitingReview(%d): %v", tc.in, err)
		}
		got, ok := q.lastArgs[len(q.lastArgs)-1].(int)
		if !ok {
			t.Fatalf("last arg is not the limit: %v", q.lastArgs)
		}
		if got != tc.want {
			t.Errorf("limit %d became %d, want %d", tc.in, got, tc.want)
		}
	}
}
