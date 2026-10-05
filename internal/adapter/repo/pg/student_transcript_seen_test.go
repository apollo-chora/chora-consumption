package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
)

// --- stub Querier recording the exec ------------------------------------

type seenExec struct {
	sql  string
	args []any
}

type seenQuerier struct {
	execs    []seenExec
	applied  bool
	execErr  error
	affected int64
}

func (q *seenQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	if strings.Contains(sql, "chora.tenant_id") {
		q.applied = true
		return rls.CommandTag{RowsAffected: 1}, nil
	}
	q.execs = append(q.execs, seenExec{sql, args})
	if q.execErr != nil {
		return rls.CommandTag{}, q.execErr
	}
	return rls.CommandTag{RowsAffected: q.affected}, nil
}
func (q *seenQuerier) QueryRow(context.Context, string, ...any) Row { return nil }
func (q *seenQuerier) Query(context.Context, string, ...any) (Rows, error) {
	return nil, nil
}
func (q *seenQuerier) find(marker string) (seenExec, bool) {
	for _, e := range q.execs {
		if strings.Contains(e.sql, marker) {
			return e, true
		}
	}
	return seenExec{}, false
}

type seenTx struct{ q *seenQuerier }

func (t *seenTx) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if t.q == nil {
		t.q = &seenQuerier{affected: 1}
	}
	return fn(ctx, t.q)
}

// seenCtx carries the tenant rls.ApplySession requires. A bare
// context.Background() makes ApplySession fail loudly, which is the correct
// behaviour and not what these tests are exercising.
func seenCtx() context.Context {
	return tracing.WithGCID(tracing.WithTenantID(context.Background(), "tnt-1"), "gcid-1")
}

// --- tests ---------------------------------------------------------------

func TestMarkSeen_AppliesRLSBeforeWriting(t *testing.T) {
	q := &seenQuerier{affected: 1}
	if err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), "tnt-1", "gcid-1", "e-1", time.Now().UTC()); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if !q.applied {
		t.Fatal("MarkSeen must run rls.ApplySession before the UPDATE")
	}
}

func TestMarkSeen_CarriesBothTheTenantAndTheLearnerPredicate(t *testing.T) {
	// The positive control that matters most here. This is a WRITE, and RLS
	// only scopes the tenant: without an explicit gcid predicate one learner
	// in a tenant could mark another learner's result as read.
	q := &seenQuerier{affected: 1}
	if err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), "tnt-1", "gcid-9", "e-1", time.Now().UTC()); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	e, ok := q.find("student_transcript_entries")
	if !ok {
		t.Fatal("no UPDATE against student_transcript_entries was issued")
	}
	flat := strings.Join(strings.Fields(e.sql), " ")
	for _, want := range []string{"UPDATE student_transcript_entries", "tenant_id = $1", "gcid = $2", "entry_id = $3"} {
		if !strings.Contains(flat, want) {
			t.Errorf("UPDATE must contain %q, got %q", want, flat)
		}
	}
}

func TestMarkSeen_IsGuardedOneWay(t *testing.T) {
	// The domain refuses to move an existing stamp; the SQL must agree, or a
	// second open would resurface an old result by re-stamping it.
	q := &seenQuerier{affected: 1}
	if err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), "tnt-1", "gcid-1", "e-1", time.Now().UTC()); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	e, _ := q.find("student_transcript_entries")
	if !strings.Contains(strings.Join(strings.Fields(e.sql), " "), "seen_at IS NULL") {
		t.Errorf("the UPDATE must be guarded AND seen_at IS NULL, got %q", e.sql)
	}
}

func TestMarkSeen_AlreadySeenIsNotAnError(t *testing.T) {
	// Zero rows affected means the learner had already opened it. That is the
	// normal second tap, not a failure, and surfacing it as one would make the
	// result screen show an error for doing nothing wrong.
	q := &seenQuerier{affected: 0}
	if err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), "tnt-1", "gcid-1", "e-1", time.Now().UTC()); err != nil {
		t.Fatalf("an already-seen entry must not error, got %v", err)
	}
}

func TestMarkSeen_ZeroTimeIsRefusedBeforeAnyWrite(t *testing.T) {
	// Mirrors the domain guard. Refused at the adapter too, so a caller bug
	// cannot reach the database and stamp year 1.
	q := &seenQuerier{affected: 1}
	err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), "tnt-1", "gcid-1", "e-1", time.Time{})
	if err == nil {
		t.Fatal("a zero timestamp must be refused")
	}
	if _, issued := q.find("student_transcript_entries"); issued {
		t.Error("a refused MarkSeen must not issue an UPDATE")
	}
}

func TestMarkSeen_RequiresTenantLearnerAndEntry(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct{ name, tenant, gcid, entry string }{
		{"no tenant", "", "gcid-1", "e-1"},
		{"no learner", "tnt-1", "", "e-1"},
		{"no entry", "tnt-1", "gcid-1", ""},
	} {
		q := &seenQuerier{affected: 1}
		if err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), tc.tenant, tc.gcid, tc.entry, now); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
		if _, issued := q.find("student_transcript_entries"); issued {
			t.Errorf("%s: must not issue an UPDATE", tc.name)
		}
	}
}

func TestMarkSeen_ExecErrorIsReturned(t *testing.T) {
	q := &seenQuerier{affected: 1, execErr: errors.New("deadlock detected")}
	err := NewTranscriptRepo(&seenTx{q: q}).MarkSeen(seenCtx(), "tnt-1", "gcid-1", "e-1", time.Now().UTC())
	if err == nil {
		t.Fatal("expected the exec error to surface")
	}
	if !strings.Contains(err.Error(), "deadlock detected") {
		t.Errorf("error must carry the cause, got %q", err)
	}
}

func TestListTranscriptByGCIDSQL_SelectsSeenAt(t *testing.T) {
	// The list feeds the home card, so it has to carry the flag. Asserting the
	// projection here catches the case where the column was added to the table
	// and the read was never widened, which would render every result unseen
	// forever.
	flat := strings.Join(strings.Fields(listTranscriptByGCIDSQL), " ")
	if !strings.Contains(flat, "seen_at") {
		t.Errorf("the learner list must select seen_at, got %q", flat)
	}
}

func TestListTranscriptByAssessmentIDsSQL_DoesNotLeakSeenAt(t *testing.T) {
	// Whether a learner has OPENED their own result is a learner-private fact
	// about their reading, not a fact about the assessment. The instructor
	// gradebook read spans every learner in the tenant, so carrying seen_at
	// there would disclose to an instructor which of their students have
	// looked at their grades. Nobody asked for that, and it is the kind of
	// disclosure that is easy to add by accident when a shared scan helper is
	// widened for one caller.
	flat := strings.Join(strings.Fields(listTranscriptByAssessmentIDsSQL), " ")
	if strings.Contains(flat, "seen_at") {
		t.Errorf("the cross-learner gradebook read must NOT select seen_at, got %q", flat)
	}
}

// seenScanRows is a Rows over one scripted row, used to exercise the scan
// helper directly. A scan whose destination list does not match its SELECT
// fails only at RUNTIME, so leaving this function untested would leave the
// column-order bug it is most likely to have completely undetected.
type seenScanRows struct {
	vals []any
	i    int
}

func (r *seenScanRows) Next() bool { r.i++; return r.i == 1 }
func (r *seenScanRows) Scan(dest ...any) error {
	if len(dest) != len(r.vals) {
		return errors.New("column count mismatch: the SELECT and the scan disagree")
	}
	for i, d := range dest {
		switch dst := d.(type) {
		case *string:
			*dst = r.vals[i].(string)
		case **string:
			if r.vals[i] == nil {
				*dst = nil
			} else {
				v := r.vals[i].(string)
				*dst = &v
			}
		case **float64:
			*dst = nil
		case **bool:
			*dst = nil
		case *time.Time:
			*dst = r.vals[i].(time.Time)
		case **time.Time:
			switch v := r.vals[i].(type) {
			case nil:
				*dst = nil
			case time.Time:
				t := v
				*dst = &t
			}
		default:
			return errors.New("unsupported destination")
		}
	}
	return nil
}
func (r *seenScanRows) Close() error { return nil }
func (r *seenScanRows) Err() error   { return nil }

func TestScanTranscriptEntryRowWithSeen_ColumnOrderMatchesTheSelect(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seen := now.Add(time.Hour)
	rows := &seenScanRows{vals: []any{
		"e-1", "tnt-1", "gcid-1", "assessment", "as-1", "Algebra", "graduate",
		nil, nil, nil, nil,
		"course-1", now, "idem-1", now, now, seen,
	}}
	rows.Next()

	e, err := scanTranscriptEntryRowWithSeen(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if e.EntryID != "e-1" || e.Kind != "assessment" || e.Title != "Algebra" {
		t.Errorf("scalar columns landed wrong: %+v", e)
	}
	if string(e.DeliveryType) != "graduate" {
		t.Errorf("DeliveryType = %q, want graduate", e.DeliveryType)
	}
	if e.CourseID != "course-1" {
		t.Errorf("CourseID = %q, want course-1", e.CourseID)
	}
	if e.SeenAt == nil || !e.SeenAt.Equal(seen) {
		t.Errorf("SeenAt = %v, want %v (it is the LAST column of the learner SELECT)", e.SeenAt, seen)
	}
	if e.Unseen() {
		t.Error("a stamped row must not report Unseen")
	}
}

func TestScanTranscriptEntryRowWithSeen_NullSeenAtMeansNeverOpened(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	rows := &seenScanRows{vals: []any{
		"e-2", "tnt-1", "gcid-1", "assessment", "as-2", "Geometry", nil,
		nil, nil, nil, nil,
		nil, now, "idem-2", now, now, nil,
	}}
	rows.Next()

	e, err := scanTranscriptEntryRowWithSeen(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if e.SeenAt != nil {
		t.Errorf("SeenAt = %v, want nil", e.SeenAt)
	}
	if !e.Unseen() {
		t.Error("a NULL seen_at must report Unseen")
	}
	if e.DeliveryType != "" || e.CourseID != "" {
		t.Errorf("NULL delivery_type and course_id must stay zero, got %+v", e)
	}
}
