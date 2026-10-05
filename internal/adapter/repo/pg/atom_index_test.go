// atom_index_test.go — RLS contract verification for the atom_index
// pgx adapter. Stub Querier captures SQL strings.
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

func TestPGAtomIndexRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewAtomIndexRepo(tx)
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          "01970000-0000-7000-a000-000000000001",
		TenantID:        pgTenantID,
		CourseID:        "01970000-0000-7000-b000-000000000001",
		AtomType:        "mcq",
		TopicTags:       []string{"agile"},
		CorrectOptionID: "opt-a",
		AnswerCount:     4,
		PublishedAt:     time.Now().UTC(),
	})
	if err := repo.Save(withCtx(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO atom_index") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGAtomIndexRepo_SaveRejectsNil(t *testing.T) {
	repo := NewAtomIndexRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); err != atom_index.ErrInvalidAtom {
		t.Errorf("err = %v; want ErrInvalidAtom", err)
	}
}

func TestPGAtomIndexRepo_GetAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewAtomIndexRepo(tx)
	_, _ = repo.Get(withCtx(), "01970000-0000-7000-a000-000000000001")
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGAtomIndexRepo_ListByCourseAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewAtomIndexRepo(tx)
	_, _ = repo.ListByCourse(withCtx(), pgTenantID, "01970000-0000-7000-b000-000000000001")
	// Stub Querier.Query() does NOT record into execCalls (it returns
	// nil rows); we only assert the SET LOCAL pair landed FIRST.
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

func TestPGAtomIndexRepo_SearchForLearnerAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewAtomIndexRepo(tx)
	_, _ = repo.SearchForLearner(withCtx(), pgTenantID, "agile", 5)
	if len(tx.q.execCalls) < 2 {
		t.Fatalf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

func TestPGAtomIndexRepo_SearchForLearnerClampsLimit(t *testing.T) {
	// limit 0 → default; over-large → clamped. Verified via the captured
	// query args on a recording querier.
	rq := &recordingQuerier{}
	repo := NewAtomIndexRepo(&recordingTxRunner{q: rq})
	if _, err := repo.SearchForLearner(withCtx(), pgTenantID, "", 0); err != nil {
		t.Fatalf("SearchForLearner(limit=0): %v", err)
	}
	if rq.lastLimit != 5 {
		t.Errorf("limit 0 → %d; want default 5", rq.lastLimit)
	}
	if _, err := repo.SearchForLearner(withCtx(), pgTenantID, "", 99); err != nil {
		t.Fatalf("SearchForLearner(limit=99): %v", err)
	}
	if rq.lastLimit != 10 {
		t.Errorf("limit 99 → %d; want clamp 10", rq.lastLimit)
	}
}

// recordingQuerier captures the Query() args so the clamp test can assert the
// bound limit, while still satisfying the Querier interface (Exec for RLS).
// Exec SQL + args are captured too so Save tests can assert bound values
// (e.g. the NULL course_id bind for standalone atoms).
type recordingQuerier struct {
	lastLimit int
	execSQL   []string
	execArgs  [][]any
}

func (q *recordingQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	q.execSQL = append(q.execSQL, sql)
	q.execArgs = append(q.execArgs, args)
	return rls.CommandTag{}, nil
}
func (q *recordingQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row { return nil }
func (q *recordingQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	// SearchForLearner binds (tenant, [topic,] limit). The limit is the LAST arg.
	if n := len(args); n > 0 {
		if v, ok := args[n-1].(int); ok {
			q.lastLimit = v
		}
	}
	return nil, nil
}

type recordingTxRunner struct{ q *recordingQuerier }

func (r *recordingTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	return fn(ctx, r.q)
}

// Regression (18d, live 2026-07-09): a LATE tagless atom.created.v1 must NOT
// blank a published atom's topic_tags. The create step always emits an
// atom.created event whose Event struct carries NO tags; if it is delivered
// (or redelivered) AFTER the tagged question-attach event, the upsert's
// unconditional `topic_tags = EXCLUDED.topic_tags` overwrote the projected
// topic with {} — the guarded status + correct_option_id survived (published
// guard), so a real, answer-keyed MCQ silently became un-pickable (empty
// PrimaryTopic → dropped by the answerable pick). The upsert must PRESERVE the
// existing topic_tags when the incoming EXCLUDED set is empty, exactly as it
// already preserves status + the answer key once published.
func TestPGAtomIndexRepo_UpsertGuardsTopicTagsAgainstEmptyIncoming(t *testing.T) {
	rq := &recordingQuerier{}
	repo := NewAtomIndexRepo(&recordingTxRunner{q: rq})
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:    "01970000-0000-7000-a000-000000000003",
		TenantID:  pgTenantID,
		AtomType:  "mcq",
		TopicTags: []string{"recursion"},
		Status:    atom_index.StatusPublished,
	})
	if err := repo.Save(withCtx(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var upsert string
	for _, s := range rq.execSQL {
		if strings.Contains(s, "INSERT INTO atom_index") {
			upsert = s
		}
	}
	if upsert == "" {
		t.Fatal("no atom_index upsert SQL captured")
	}
	norm := strings.Join(strings.Fields(upsert), " ") // collapse whitespace
	if strings.Contains(norm, "topic_tags = EXCLUDED.topic_tags") {
		t.Error("topic_tags is unconditionally overwritten with EXCLUDED.topic_tags — a late " +
			"tagless atom.created blanks a published atom's topic (18d clobber). Guard it like status/key.")
	}
	if !strings.Contains(norm, "atom_index.topic_tags") {
		t.Error("topic_tags DO UPDATE SET must reference atom_index.topic_tags (the preservation branch)")
	}
}

// Regression (live 2026-06-10): the pgx_runtime Row wrapper returns the
// PACKAGE ErrNoRows sentinel — Get must map it to atom_index.ErrNotFound so
// unprojected atoms read as expected-absent, not as a storage failure.
func TestPGAtomIndexRepo_RuntimeNoRowsMapsToDomainNotFound(t *testing.T) {
	repo := NewAtomIndexRepo(&stubTxRunnerWith{q: &noRowsQuerier{}})
	_, err := repo.Get(withCtx(), "01970000-0000-7000-a000-000000000001")
	if !errors.Is(err, atom_index.ErrNotFound) {
		t.Fatalf("err = %v, want atom_index.ErrNotFound", err)
	}
}

// Regression (live 2026-06-10, L4 KG-fog): standalone atoms (no course) emit
// atom.created with course_id = "". Binding the empty STRING into the uuid
// course_id column fails with 22P02 `invalid input syntax for type uuid: ""`
// — every standalone atom's projection upsert dead-ends, starving atom_index
// (the fog candidate catalogue + MCQ grading key read it). Save must bind SQL
// NULL for the empty CourseID instead.
func TestPGAtomIndexRepo_SaveBindsNullCourseIDWhenEmpty(t *testing.T) {
	rq := &recordingQuerier{}
	repo := NewAtomIndexRepo(&recordingTxRunner{q: rq})
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:      "01970000-0000-7000-a000-000000000002",
		TenantID:    pgTenantID,
		CourseID:    "", // standalone atom — no owning course
		AtomType:    "mcq",
		PublishedAt: time.Now().UTC(),
	})
	if err := repo.Save(withCtx(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(rq.execSQL) == 0 {
		t.Fatal("expected the INSERT exec to be recorded")
	}
	last := len(rq.execSQL) - 1
	if !contains(rq.execSQL[last], "INSERT INTO atom_index") {
		t.Fatalf("last exec = %q; want atom_index INSERT", rq.execSQL[last])
	}
	args := rq.execArgs[last]
	if len(args) != 12 {
		t.Fatalf("INSERT args = %d; want 12 (status CHO-1968 + cognitive_level WS-C3)", len(args))
	}
	if args[2] != nil {
		t.Errorf("course_id bind ($3) = %#v; want nil (SQL NULL) for standalone atom", args[2])
	}
}

// MarkPublished issues a TARGETED UPDATE (status + answer key only, preserving
// title/tags/course) under the RLS session. CHO-1968.
func TestPGAtomIndexRepo_MarkPublishedAppliesRLSAndUpdates(t *testing.T) {
	rq := &recordingQuerier{}
	repo := NewAtomIndexRepo(&recordingTxRunner{q: rq})
	if err := repo.MarkPublished(withCtx(), "01970000-0000-7000-a000-000000000009",
		atom_index.StatusPublished, "mcq", "opt-b", 4, "application"); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if len(rq.execSQL) < 3 {
		t.Fatalf("expected SET LOCAL pair + UPDATE; got %d execs", len(rq.execSQL))
	}
	if !contains(rq.execSQL[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; want SET LOCAL tenant", rq.execSQL[0])
	}
	last := rq.execSQL[len(rq.execSQL)-1]
	if !contains(last, "UPDATE atom_index") || !contains(last, "status") {
		t.Errorf("last exec = %q; want a targeted UPDATE atom_index ... status", last)
	}
	// Preserves title/tags/course: the UPDATE must NOT touch them.
	if contains(last, "title") || contains(last, "topic_tags") || contains(last, "course_id") {
		t.Errorf("MarkPublished UPDATE touched a preserved column: %q", last)
	}
}

// The upsert must NOT downgrade an already-published row when a late atom.created
// (draft) replays — status uses a non-downgrading CASE. CHO-1968.
func TestPGAtomIndexRepo_UpsertStatusNonDowngrading(t *testing.T) {
	if !contains(upsertAtomIndexSQL, "CASE WHEN atom_index.status = 'published'") {
		t.Errorf("upsertAtomIndexSQL missing non-downgrading status CASE:\n%s", upsertAtomIndexSQL)
	}
}

// CHO-1968 hardening: a published row's answer key is authoritative (it came from
// atom.published). A late atom.created (empty key) upserting AFTER the flip must
// NOT clobber it — correct_option_id + answer_count use the SAME preserve-when-
// published CASE as status. Without this the key blanks → IsMCQ() goes false →
// the atom silently drops out of the dose.
func TestPGAtomIndexRepo_UpsertAnswerKeyNonDowngrading(t *testing.T) {
	for _, want := range []string{
		"THEN atom_index.correct_option_id ELSE EXCLUDED.correct_option_id END",
		"THEN atom_index.answer_count ELSE EXCLUDED.answer_count END",
	} {
		if !contains(upsertAtomIndexSQL, want) {
			t.Errorf("upsertAtomIndexSQL missing answer-key preserve CASE %q:\n%s", want, upsertAtomIndexSQL)
		}
	}
}

// Companion: a course-bound atom must keep binding the course uuid string.
func TestPGAtomIndexRepo_SaveBindsCourseIDWhenSet(t *testing.T) {
	rq := &recordingQuerier{}
	repo := NewAtomIndexRepo(&recordingTxRunner{q: rq})
	const course = "01970000-0000-7000-b000-000000000001"
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:      "01970000-0000-7000-a000-000000000003",
		TenantID:    pgTenantID,
		CourseID:    course,
		AtomType:    "mcq",
		PublishedAt: time.Now().UTC(),
	})
	if err := repo.Save(withCtx(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	args := rq.execArgs[len(rq.execArgs)-1]
	if got, ok := args[2].(string); !ok || got != course {
		t.Errorf("course_id bind ($3) = %#v; want %q", args[2], course)
	}
}

// The read SQLs must tolerate NULL course_id rows (standalone atoms): a bare
// `course_id` scan into the domain's string field fails on NULL, so every
// SELECT projects COALESCE(course_id::text, ”). Asserted on the SQL consts
// directly (same package), consistent with the harness's SQL-string style.
func TestPGAtomIndexRepo_ReadSQLCoalescesNullCourseID(t *testing.T) {
	const want = "COALESCE(course_id::text, '')"
	for name, sql := range map[string]string{
		"loadAtomIndexSQL":             loadAtomIndexSQL,
		"listAtomIndexByCourseSQL":     listAtomIndexByCourseSQL,
		"searchAtomIndexForLearnerSQL": searchAtomIndexForLearnerSQL,
	} {
		if !contains(sql, want) {
			t.Errorf("%s does not COALESCE course_id — NULL rows would fail the string scan", name)
		}
	}
}
