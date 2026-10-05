// companion_goal_knowledge_test.go — RLS-contract + SQL-shape + null-round-trip
// gates for the CHO-2118 goal-knowledge cache adapter (sub-phase B). Mirrors
// campaign_repo_test.go / goal_test.go: the stub Querier captures Exec SQL+args
// (so the SET LOCAL pair provably lands BEFORE the mutation, and NULL-vs-empty
// arg coercions are asserted), and goalFakeQuerier returns canned rows so the
// scan→domain mapping is exercised.
//
// The load-bearing tests here are the ones that pin what a cache row MEANS:
//   - a never-generated row must write SQL NULL (not "") for its stamps, and
//     must scan NULL back as nil (not a zero time) — see the two null tests;
//   - a stale row must keep its synthesis_text through the write, or
//     serve-stale-while-regen degrades to a blank card on every invalidation.
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	companiongoalknowledge "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
)

const (
	gkCompanionID = "01971b00-cccc-7000-8000-000000000001"
	gkGoalID      = "01971b00-dddd-7000-8000-000000000002"
	gkRootID      = "01971b00-eeee-7000-8000-000000000003"
	gkRunID       = "01971b00-ffff-7000-8000-000000000004"
)

// Upsert bind positions (1-based $N → 0-based arg index). Named so an off-by-one
// in the INSERT column list fails with a readable message instead of a puzzle.
const (
	gkArgSynthesisText = 5
	gkArgStatus        = 6
	gkArgRunID         = 7
	gkArgModelID       = 8
	gkArgPromptVersion = 9
	gkArgContentHash   = 10
	gkArgGeneratedAt   = 11
	gkArgRootConceptID = 12
	gkArgRequestedAt   = 13
	gkArgInvalidatedAt = 14
	gkArgReason        = 15
)

func gkFixture(t *testing.T) *companiongoalknowledge.GoalKnowledge {
	t.Helper()
	root := gkRootID
	k, err := companiongoalknowledge.NewGoalKnowledge(pgTenantID, pgUserGCID, gkCompanionID, gkGoalID, &root)
	if err != nil {
		t.Fatalf("gkFixture: %v", err)
	}
	return k
}

func TestPGGoalKnowledgeRepo_UpsertAppliesRLSAndTargetsLiveKey(t *testing.T) {
	tx := &stubTxRunner{}
	if err := NewGoalKnowledgeRepo(tx).Upsert(withCtx(), gkFixture(t)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 {
		t.Fatalf("want RLS pair + upsert; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS tenant must land before the write: %q", calls[0])
	}
	if !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("RLS gcid must land before the write: %q", calls[1])
	}
	up := calls[len(calls)-1]
	// The conflict target IS the live partial unique index from migration 0092
	// (uq_companion_goal_knowledge_live). Miss the WHERE and Postgres cannot infer
	// the arbiter (42P10) — the class of defect the prepare-smoke exists for.
	for _, must := range []string{
		"INSERT INTO companion_goal_knowledge",
		"ON CONFLICT (tenant_id, learner_gcid, companion_id, goal_id)",
		"WHERE deleted_at IS NULL",
		"DO UPDATE",
		"::companion_goal_knowledge_status",
		"::companion_goal_knowledge_invalidation_reason",
	} {
		if !contains(up, must) {
			t.Errorf("upsert SQL missing %q:\n%s", must, up)
		}
	}
}

// A never-generated row has no synthesis and no decision stamp. Those columns
// must go down as SQL NULL, never as "": generated_by_run_id and root_concept_id
// are UUID columns, and an empty string is not a uuid — Postgres rejects it with 22P02, so an
// empty-string bind would make the very first Upsert of every new (companion,
// goal) pair fail in production while the stubbed unit suite stayed green.
func TestPGGoalKnowledgeRepo_UpsertWritesNullNotEmptyStamps(t *testing.T) {
	tx := &stubTxRunner{}
	k := gkFixture(t)
	k.RootConceptID = nil // a goal with no root — the other NULL-able uuid
	if err := NewGoalKnowledgeRepo(tx).Upsert(withCtx(), k); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	for _, c := range []struct {
		name string
		idx  int
	}{
		{"synthesis_text", gkArgSynthesisText},
		{"generated_by_run_id", gkArgRunID},
		{"generated_by_model_id", gkArgModelID},
		{"prompt_version", gkArgPromptVersion},
		{"content_hash", gkArgContentHash},
		{"root_concept_id", gkArgRootConceptID},
		{"generated_at", gkArgGeneratedAt},
		{"requested_at", gkArgRequestedAt},
		{"invalidated_at", gkArgInvalidatedAt},
	} {
		if !isNilArg(args[c.idx]) {
			t.Errorf("%s = %#v; want SQL NULL on a never-generated row", c.name, args[c.idx])
		}
	}
	// The reason enum is NOT NULL (default 'never_generated') — it must carry the
	// pseudo-state, not an empty string ('' is not a valid enum label → 22P02).
	if got := args[gkArgReason]; got != string(companiongoalknowledge.ReasonNeverGenerated) {
		t.Errorf("invalidation_reason = %#v; want %q", got, companiongoalknowledge.ReasonNeverGenerated)
	}
	if got := args[gkArgStatus]; got != string(companiongoalknowledge.StatusPending) {
		t.Errorf("status = %#v; want %q", got, companiongoalknowledge.StatusPending)
	}
}

// The whole lifecycle through the write path: pending → requested → fresh →
// stale. The row that matters is the last one — an INVALIDATED row still carries
// its last good synthesis_text. Drop it here and serve-stale-while-regen becomes
// serve-blank-while-regen: every weakness change would blank the learner's
// reflection until the LLM came back.
func TestPGGoalKnowledgeRepo_UpsertRoundTripsLifecycleKeepingText(t *testing.T) {
	const text = "You have been steady on fractions, but decimals still trip you up."
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	tx := &stubTxRunner{}
	repo := NewGoalKnowledgeRepo(tx)
	k := gkFixture(t)

	// pending → in-flight claim. Losing requested_at here would let N concurrent
	// tab reads each fire their own LLM call (the claim is the cost bound).
	k.MarkRequested(now)
	if err := repo.Upsert(withCtx(), k); err != nil {
		t.Fatalf("Upsert(pending): %v", err)
	}
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	if isNilArg(args[gkArgRequestedAt]) {
		t.Error("requested_at = NULL after MarkRequested; the in-flight claim was lost")
	}
	if got := args[gkArgStatus]; got != string(companiongoalknowledge.StatusPending) {
		t.Errorf("status = %#v; want pending", got)
	}

	// → fresh.
	if err := k.RecordSynthesis(text, gkRunID, "gemini-3-flash", "goal_reflection@v1", "sha256:abc", now); err != nil {
		t.Fatalf("RecordSynthesis: %v", err)
	}
	if err := repo.Upsert(withCtx(), k); err != nil {
		t.Fatalf("Upsert(fresh): %v", err)
	}
	args = tx.q.execArgs[len(tx.q.execArgs)-1]
	if args[gkArgSynthesisText] != text {
		t.Errorf("synthesis_text = %#v; want the reflection", args[gkArgSynthesisText])
	}
	if got := args[gkArgStatus]; got != string(companiongoalknowledge.StatusFresh) {
		t.Errorf("status = %#v; want fresh", got)
	}
	if isNilArg(args[gkArgGeneratedAt]) {
		t.Error("generated_at = NULL on a fresh row")
	}
	if args[gkArgRunID] != gkRunID || args[gkArgPromptVersion] != "goal_reflection@v1" {
		t.Errorf("decision stamp not persisted: run=%#v prompt=%#v", args[gkArgRunID], args[gkArgPromptVersion])
	}
	if !isNilArg(args[gkArgRequestedAt]) {
		t.Error("requested_at must clear once the synthesis lands")
	}

	// → stale. The text SURVIVES.
	k.Invalidate(companiongoalknowledge.ReasonWeaknessGrown)
	if err := repo.Upsert(withCtx(), k); err != nil {
		t.Fatalf("Upsert(stale): %v", err)
	}
	args = tx.q.execArgs[len(tx.q.execArgs)-1]
	if args[gkArgSynthesisText] != text {
		t.Errorf("stale row lost its text (%#v) — serve-stale-while-regen is broken", args[gkArgSynthesisText])
	}
	if got := args[gkArgStatus]; got != string(companiongoalknowledge.StatusStale) {
		t.Errorf("status = %#v; want stale", got)
	}
	if isNilArg(args[gkArgInvalidatedAt]) {
		t.Error("invalidated_at = NULL on a stale row")
	}
	if got := args[gkArgReason]; got != string(companiongoalknowledge.ReasonWeaknessGrown) {
		t.Errorf("invalidation_reason = %#v; want weakness_grown", got)
	}
}

// A nil aggregate reaching the repo is a caller bug, not an empty write. Fail
// loud (the hexagon.go sibling cache does the same) rather than no-op silently.
func TestPGGoalKnowledgeRepo_UpsertNilFailsLoud(t *testing.T) {
	tx := &stubTxRunner{}
	err := NewGoalKnowledgeRepo(tx).Upsert(withCtx(), nil)
	if err == nil {
		t.Fatal("nil upsert must fail loud, not silently succeed")
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil upsert must not touch the DB: %v", tx.q.execCalls)
	}
}

// Upsert cannot express a soft-delete, and must say so instead of emitting SQL
// that dies at the far end. The ON CONFLICT arbiter is the LIVE partial unique
// index (uq_companion_goal_knowledge_live), which contains no soft-deleted rows —
// so a proposed row carrying deleted_at matches nothing there, Postgres falls
// through to a plain INSERT, and it collides on the aggregate's own id with a
// 23505 pkey violation. The live round-trip caught precisely that; this is its
// unit-level tripwire. Soft-delete belongs in a dedicated scoped UPDATE behind a
// port method that does not exist yet.
func TestPGGoalKnowledgeRepo_UpsertRefusesSoftDeletedRow(t *testing.T) {
	tx := &stubTxRunner{}
	k := gkFixture(t)
	deletedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	k.DeletedAt = &deletedAt

	err := NewGoalKnowledgeRepo(tx).Upsert(withCtx(), k)
	if !errors.Is(err, ErrGoalKnowledgeDeleted) {
		t.Fatalf("Upsert(soft-deleted) = %v; want ErrGoalKnowledgeDeleted", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("the refusal must happen before any SQL is issued: %v", tx.q.execCalls)
	}
}

// The upsert must not carry deleted_at at all — a `deleted_at = EXCLUDED...`
// line can only ever write NULL here (see above) while reading as though
// soft-delete were supported. Live rows only; the column defaults to NULL.
func TestPGGoalKnowledgeRepo_UpsertWritesLiveRowsOnly(t *testing.T) {
	if contains(upsertGoalKnowledgeSQL, "deleted_at        =") ||
		contains(upsertGoalKnowledgeSQL, "deleted_at = EXCLUDED") {
		t.Error("upsert SQL assigns deleted_at — it cannot; the arbiter excludes soft-deleted rows")
	}
	// It still names deleted_at exactly once: in the ON CONFLICT arbiter predicate.
	if !contains(upsertGoalKnowledgeSQL, "ON CONFLICT (tenant_id, learner_gcid, companion_id, goal_id) WHERE deleted_at IS NULL") {
		t.Error("upsert SQL must arbitrate on the live partial unique index")
	}
}

// THE null round-trip. A NULL generated_at means "never generated" — that is the
// whole basis of the regen decision. Coerce it to a zero time on scan and the
// row stops being never-generated: Invalidate() then flips it to STALE, which
// the schema defines as "invalidated; last good text still servable" — a row
// that has no text would be claiming it has one. Assert nil, and assert the
// domain still reads the row as never-generated.
func TestPGGoalKnowledgeRepo_FindByGoalScansNullsAsNilNotZero(t *testing.T) {
	created := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: []any{
		"01971b00-aaaa-7000-8000-000000000009", pgTenantID, pgUserGCID, gkCompanionID, gkGoalID,
		nil, "pending", // synthesis_text, status
		nil, nil, nil, nil, nil, // run_id, model_id, prompt_version, content_hash, generated_at
		nil,      // root_concept_id
		nil, nil, // requested_at, invalidated_at
		"never_generated",
		created, created, nil,
	}}}
	k, err := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: fake}).FindByGoal(withCtx(), pgTenantID, pgUserGCID, gkCompanionID, gkGoalID)
	if err != nil {
		t.Fatalf("FindByGoal: %v", err)
	}
	if k == nil {
		t.Fatal("FindByGoal returned nil for an existing row")
	}
	if k.GeneratedAt != nil {
		t.Errorf("GeneratedAt = %v; want nil — a NULL must never scan back as a zero time", k.GeneratedAt)
	}
	if k.RequestedAt != nil || k.InvalidatedAt != nil || k.RootConceptID != nil || k.DeletedAt != nil {
		t.Errorf("NULLs coerced to non-nil: req=%v inv=%v root=%v del=%v",
			k.RequestedAt, k.InvalidatedAt, k.RootConceptID, k.DeletedAt)
	}
	if k.SynthesisText != "" || k.GeneratedByRunID != "" || k.PromptVersion != "" {
		t.Errorf("NULL text columns must scan as empty: %#v / %#v / %#v",
			k.SynthesisText, k.GeneratedByRunID, k.PromptVersion)
	}
	if k.Status != companiongoalknowledge.StatusPending || k.InvalidationReason != companiongoalknowledge.ReasonNeverGenerated {
		t.Errorf("enum mapping broken: status=%q reason=%q", k.Status, k.InvalidationReason)
	}
	// Behavioural consequence of the nil: the row is still never-generated.
	if !k.NeedsSynthesis(created.Add(time.Minute), companiongoalknowledge.DefaultRegenFloor,
		companiongoalknowledge.DefaultMaxAge, companiongoalknowledge.DefaultPendingTTL) {
		t.Error("a never-generated row must need synthesis")
	}
	k.Invalidate(companiongoalknowledge.ReasonChatTurnCompleted)
	if k.Status != companiongoalknowledge.StatusPending {
		t.Errorf("Status = %q after invalidating a never-generated row; want pending "+
			"(a zero-time GeneratedAt would have made this 'stale' with no text to serve)", k.Status)
	}
}

func TestPGGoalKnowledgeRepo_FindByGoalScansFullRow(t *testing.T) {
	const text = "We keep circling back to place value."
	created := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 7, 14, 8, 30, 0, 0, time.UTC)
	inv := time.Date(2026, 7, 14, 9, 15, 0, 0, time.UTC)
	req := time.Date(2026, 7, 14, 9, 20, 0, 0, time.UTC)
	txt, run, model, prompt, hash, root := text, gkRunID, "gemini-3-flash", "goal_reflection@v1", "sha256:abc", gkRootID

	fake := &goalFakeQuerier{row: &goalFakeRow{cols: []any{
		"01971b00-aaaa-7000-8000-000000000009", pgTenantID, pgUserGCID, gkCompanionID, gkGoalID,
		&txt, "stale",
		&run, &model, &prompt, &hash, &gen,
		&root,
		&req, &inv,
		"weakness_grown",
		created, created, nil,
	}}}
	repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: fake})

	k, err := repo.FindByGoal(withCtx(), pgTenantID, pgUserGCID, gkCompanionID, gkGoalID)
	if err != nil {
		t.Fatalf("FindByGoal: %v", err)
	}
	if k.SynthesisText != text || k.Status != companiongoalknowledge.StatusStale {
		t.Errorf("text/status = %q / %q", k.SynthesisText, k.Status)
	}
	if k.GeneratedByRunID != gkRunID || k.GeneratedByModelID != "gemini-3-flash" ||
		k.PromptVersion != "goal_reflection@v1" || k.ContentHash != "sha256:abc" {
		t.Errorf("decision stamp lost: %+v", k)
	}
	if k.GeneratedAt == nil || !k.GeneratedAt.Equal(gen) {
		t.Errorf("GeneratedAt = %v; want %v", k.GeneratedAt, gen)
	}
	if k.InvalidatedAt == nil || !k.InvalidatedAt.Equal(inv) || k.RequestedAt == nil || !k.RequestedAt.Equal(req) {
		t.Errorf("stamps lost: inv=%v req=%v", k.InvalidatedAt, k.RequestedAt)
	}
	if k.RootConceptID == nil || *k.RootConceptID != gkRootID {
		t.Errorf("RootConceptID = %v; want %q (the ADR-214 re-root guard)", k.RootConceptID, gkRootID)
	}
	if k.InvalidationReason != companiongoalknowledge.ReasonWeaknessGrown {
		t.Errorf("reason = %q", k.InvalidationReason)
	}
	// A stale row still has text → the learner sees the last good reflection.
	if !k.Servable() || k.IsCacheFresh() {
		t.Errorf("stale row must be servable but not fresh: servable=%v fresh=%v", k.Servable(), k.IsCacheFresh())
	}
	// The read is scoped, live-only, and RLS-guarded.
	q := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{"FROM companion_goal_knowledge", "tenant_id = $1", "learner_gcid = $2",
		"companion_id = $3", "goal_id = $4", "deleted_at IS NULL"} {
		if !contains(q, must) {
			t.Errorf("find SQL missing %q:\n%s", must, q)
		}
	}
	if len(fake.sqls) < 3 || !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS must land before the read: %v", fake.sqls)
	}
}

func TestPGGoalKnowledgeRepo_FindByGoalNotFound(t *testing.T) {
	// nil-row stub (no row) → (nil, nil), per the port contract.
	k, err := NewGoalKnowledgeRepo(&stubTxRunner{}).FindByGoal(withCtx(), pgTenantID, pgUserGCID, gkCompanionID, gkGoalID)
	if err != nil {
		t.Fatalf("FindByGoal: %v", err)
	}
	if k != nil {
		t.Errorf("missing row must yield nil; got %+v", k)
	}
	// ErrNoRows → also (nil, nil): a cache miss is normal, not an error.
	fake := &goalFakeQuerier{row: &goalFakeRow{err: ErrNoRows}}
	k2, err := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: fake}).FindByGoal(withCtx(), pgTenantID, pgUserGCID, gkCompanionID, gkGoalID)
	if err != nil {
		t.Fatalf("ErrNoRows must map to a cache miss: %v", err)
	}
	if k2 != nil {
		t.Errorf("ErrNoRows must yield nil; got %+v", k2)
	}
}

func gkRow(id string, status string) *goalFakeRow {
	created := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	return &goalFakeRow{cols: []any{
		id, pgTenantID, pgUserGCID, gkCompanionID, gkGoalID,
		nil, status,
		nil, nil, nil, nil, nil,
		nil,
		nil, nil,
		"never_generated",
		created, created, nil,
	}}
}

// ListByGoal is the goal-scoped invalidation fan-out (a weakness grows → stale
// that goal's reflection for whichever Companion is bound).
func TestPGGoalKnowledgeRepo_ListByGoalScopedAndLive(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		gkRow("01971b00-aaaa-7000-8000-000000000001", "fresh"),
		gkRow("01971b00-aaaa-7000-8000-000000000002", "pending"),
	}}}
	out, err := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: fake}).ListByGoal(withCtx(), pgTenantID, pgUserGCID, gkGoalID)
	if err != nil {
		t.Fatalf("ListByGoal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows; want 2", len(out))
	}
	q := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{"FROM companion_goal_knowledge", "tenant_id = $1", "learner_gcid = $2",
		"goal_id = $3", "deleted_at IS NULL"} {
		if !contains(q, must) {
			t.Errorf("list-by-goal SQL missing %q:\n%s", must, q)
		}
	}
}

// ListByCompanion is the memory-scoped fan-out (a chat turn or eviction changes
// what the Companion remembers → every goal it reflects on).
func TestPGGoalKnowledgeRepo_ListByCompanionScopedAndLive(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		gkRow("01971b00-aaaa-7000-8000-000000000001", "fresh"),
	}}}
	out, err := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: fake}).ListByCompanion(withCtx(), pgTenantID, pgUserGCID, gkCompanionID)
	if err != nil {
		t.Fatalf("ListByCompanion: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d rows; want 1", len(out))
	}
	q := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{"FROM companion_goal_knowledge", "tenant_id = $1", "learner_gcid = $2",
		"companion_id = $3", "deleted_at IS NULL"} {
		if !contains(q, must) {
			t.Errorf("list-by-companion SQL missing %q:\n%s", must, q)
		}
	}
}

func TestPGGoalKnowledgeRepo_ListsEmptyOnNilStub(t *testing.T) {
	repo := NewGoalKnowledgeRepo(&stubTxRunner{})
	byGoal, err := repo.ListByGoal(withCtx(), pgTenantID, pgUserGCID, gkGoalID)
	if err != nil {
		t.Fatalf("ListByGoal: %v", err)
	}
	byFam, err := repo.ListByCompanion(withCtx(), pgTenantID, pgUserGCID, gkCompanionID)
	if err != nil {
		t.Fatalf("ListByCompanion: %v", err)
	}
	if len(byGoal) != 0 || len(byFam) != 0 {
		t.Errorf("nil-stub lists must be empty; got %d / %d", len(byGoal), len(byFam))
	}
}

// Fail-loud: a DB error never becomes a silent cache miss. Swallowing a query
// error here would look exactly like "no cached reflection" and would re-request
// a synthesis on every read — an invisible, unbounded LLM spend.
func TestPGGoalKnowledgeRepo_ErrorsBubble(t *testing.T) {
	boom := context.DeadlineExceeded
	t.Run("upsert exec error", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: boom}})
		if err := repo.Upsert(withCtx(), gkFixture(t)); err == nil {
			t.Fatal("want upsert error to bubble")
		}
	})
	t.Run("find scan error", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: &goalFakeQuerier{row: &goalFakeRow{err: boom}}})
		if _, err := repo.FindByGoal(withCtx(), pgTenantID, pgUserGCID, gkCompanionID, gkGoalID); err == nil {
			t.Fatal("want scan error to bubble, not a silent cache miss")
		}
	})
	t.Run("list by goal query error", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: &goalFakeQuerier{queryErr: boom}})
		if _, err := repo.ListByGoal(withCtx(), pgTenantID, pgUserGCID, gkGoalID); err == nil {
			t.Fatal("want query error to bubble")
		}
	})
	t.Run("list by companion query error", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: &goalFakeQuerier{queryErr: boom}})
		if _, err := repo.ListByCompanion(withCtx(), pgTenantID, pgUserGCID, gkCompanionID); err == nil {
			t.Fatal("want query error to bubble")
		}
	})
	// A row that fails to scan mid-list must abort the list, not truncate it: a
	// short fan-out silently skips the goals whose reflections needed staling.
	t.Run("list scan error", func(t *testing.T) {
		fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{{err: boom}}}}
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: fake})
		if _, err := repo.ListByGoal(withCtx(), pgTenantID, pgUserGCID, gkGoalID); err == nil {
			t.Fatal("want scan error to abort the fan-out, not truncate it")
		}
	})
	// Fail CLOSED with no tenant context — on reads as well as writes. A read that
	// proceeded without SET LOCAL chora.tenant_id would be relying on the explicit
	// predicate alone, with RLS silently inert.
	for _, tc := range []struct {
		name string
		call func(r *GoalKnowledgeRepo) error
	}{
		{"upsert", func(r *GoalKnowledgeRepo) error { return r.Upsert(context.Background(), gkFixture(t)) }},
		{"find", func(r *GoalKnowledgeRepo) error {
			_, err := r.FindByGoal(context.Background(), pgTenantID, pgUserGCID, gkCompanionID, gkGoalID)
			return err
		}},
		{"list by goal", func(r *GoalKnowledgeRepo) error {
			_, err := r.ListByGoal(context.Background(), pgTenantID, pgUserGCID, gkGoalID)
			return err
		}},
		{"list by companion", func(r *GoalKnowledgeRepo) error {
			_, err := r.ListByCompanion(context.Background(), pgTenantID, pgUserGCID, gkCompanionID)
			return err
		}},
	} {
		t.Run("no tenant context: "+tc.name, func(t *testing.T) {
			if err := tc.call(NewGoalKnowledgeRepo(&stubTxRunner{})); err == nil {
				t.Fatal("want rls.ApplySession to refuse a context with no tenant")
			}
		})
	}
}

// The NOT NULL invalidation_reason enum has no empty-string label. The domain's zero value
// IS the empty string, so a hand-built aggregate that never went through the
// constructor would bind an empty string and die at 22P02 — bind the never_generated
// pseudo-state instead (it is the column default and means the same thing).
func TestGKReason_EmptyBindsNeverGenerated(t *testing.T) {
	if got := gkReason(""); got != string(companiongoalknowledge.ReasonNeverGenerated) {
		t.Errorf("gkReason(\"\") = %q; want %q", got, companiongoalknowledge.ReasonNeverGenerated)
	}
	if got := gkReason(companiongoalknowledge.ReasonGoalRerooted); got != "goal_rerooted" {
		t.Errorf("gkReason passes real reasons through unchanged; got %q", got)
	}
}

// The port is deliberately List → Invalidate → Upsert so the reason-priority
// table lives ONLY in the domain (goal_knowledge.go reasonPriority). A bulk
// UPDATE that ranked reasons in SQL would give that invariant a second home,
// free to drift. This test is the tripwire on that design decision.
func TestPGGoalKnowledgeRepo_NoInvalidationPriorityInSQL(t *testing.T) {
	for name, sql := range gkAllStatements() {
		upper := strings.ToUpper(sql)
		if strings.Contains(upper, "CASE ") || strings.Contains(upper, "PRIORITY") {
			t.Errorf("%s ranks invalidation reasons in SQL — the priority table belongs to "+
				"the domain only (List → Invalidate → Upsert):\n%s", name, sql)
		}
	}
}

// Soft-delete is a project invariant: a repo that forgets the filter resurrects
// closed learners' rows.
func TestPGGoalKnowledgeRepo_EveryStatementIsLiveOnly(t *testing.T) {
	for name, sql := range gkAllStatements() {
		if !contains(sql, "deleted_at IS NULL") {
			t.Errorf("%s does not filter soft-deleted rows:\n%s", name, sql)
		}
	}
}

func gkAllStatements() map[string]string {
	return map[string]string{
		"upsertGoalKnowledgeSQL":                upsertGoalKnowledgeSQL,
		"findGoalKnowledgeByGoalSQL":            findGoalKnowledgeByGoalSQL,
		"listGoalKnowledgeByGoalSQL":            listGoalKnowledgeByGoalSQL,
		"listGoalKnowledgeByCompanionSQL":       listGoalKnowledgeByCompanionSQL,
		"softDeleteGoalKnowledgeByCompanionSQL": softDeleteGoalKnowledgeByCompanionSQL,
		"softDeleteGoalKnowledgeByLearnerSQL":   softDeleteGoalKnowledgeByLearnerSQL,
	}
}

// --- closure / retirement soft-delete (ClosureRepository port) ---------------

// gkExecQuerier returns a CONFIGURABLE RowsAffected. The stock stubs always
// report 0, which would let a soft-delete that purges nothing look identical to
// one that purges everything — and on this table that difference is the
// difference between honouring account closure and silently retaining LLM prose
// about the learner. The count must be asserted, so it must be settable.
type gkExecQuerier struct {
	sqls []string
	args [][]any
	rows int64
	err  error
}

func (q *gkExecQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	q.sqls = append(q.sqls, sql)
	q.args = append(q.args, args)
	if contains(sql, "SET LOCAL") {
		return rls.CommandTag{}, nil // let the RLS pair through
	}
	if q.err != nil {
		return rls.CommandTag{}, q.err
	}
	return rls.CommandTag{RowsAffected: q.rows}, nil
}
func (q *gkExecQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row { return nil }
func (q *gkExecQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return nil, nil
}

// Companion retirement (CHO-2096 shape): soft-delete every live reflection this
// Companion holds for the learner.
//
// rls.ApplySession is NOT optional here, and this is the test that says why. The
// table is FORCE RLS with tenant_isolation on current_setting('chora.tenant_id').
// An UPDATE issued without that GUC matches ZERO rows — no error, no warning,
// just a purge that silently purges nothing and returns (0, nil), which reads as
// "this Companion remembered nothing". A retirement that retains the learner's
// memories while reporting success is the worst possible failure mode.
func TestPGGoalKnowledgeRepo_SoftDeleteByCompanionAppliesRLSAndScopes(t *testing.T) {
	q := &gkExecQuerier{rows: 3}
	n, err := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: q}).
		SoftDeleteByCompanion(withCtx(), pgTenantID, pgUserGCID, gkCompanionID)
	if err != nil {
		t.Fatalf("SoftDeleteByCompanion: %v", err)
	}
	if n != 3 {
		t.Errorf("rows purged = %d; want 3 — the count must reach the caller, not be swallowed", n)
	}
	if len(q.sqls) < 3 || !contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS must land before the purge, or FORCE RLS silently matches 0 rows: %v", q.sqls)
	}
	up := q.sqls[len(q.sqls)-1]
	for _, must := range []string{
		"UPDATE companion_goal_knowledge",
		"SET deleted_at = now()",
		"tenant_id = $1", "learner_gcid = $2", "companion_id = $3",
		"deleted_at IS NULL", // idempotent: a second retirement purges nothing
	} {
		if !contains(up, must) {
			t.Errorf("soft-delete-by-companion SQL missing %q:\n%s", must, up)
		}
	}
}

// Account closure: sweep every reflection the learner holds, across every goal
// and every Companion.
func TestPGGoalKnowledgeRepo_SoftDeleteByLearnerAppliesRLSAndScopes(t *testing.T) {
	q := &gkExecQuerier{rows: 7}
	n, err := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: q}).
		SoftDeleteByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("SoftDeleteByLearner: %v", err)
	}
	if n != 7 {
		t.Errorf("rows purged = %d; want 7", n)
	}
	if len(q.sqls) < 3 || !contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS must land before the purge: %v", q.sqls)
	}
	up := q.sqls[len(q.sqls)-1]
	for _, must := range []string{
		"UPDATE companion_goal_knowledge",
		"SET deleted_at = now()",
		"tenant_id = $1", "learner_gcid = $2", "deleted_at IS NULL",
	} {
		if !contains(up, must) {
			t.Errorf("soft-delete-by-learner SQL missing %q:\n%s", must, up)
		}
	}
	// It must NOT narrow to one Companion — closure sweeps them all.
	if contains(up, "companion_id") {
		t.Errorf("learner-scoped closure must not filter by companion_id:\n%s", up)
	}
}

// NEVER hard-delete (ddd-enforcement #4). Closure pseudonymises and tombstones;
// a DELETE would destroy the audit trail the closure saga itself relies on.
func TestPGGoalKnowledgeRepo_NeverHardDeletes(t *testing.T) {
	for name, sql := range gkAllStatements() {
		upper := strings.ToUpper(sql)
		if strings.Contains(upper, "DELETE FROM") || strings.Contains(upper, "TRUNCATE") {
			t.Errorf("%s hard-deletes — soft-delete + pseudonymise is a hard invariant:\n%s", name, sql)
		}
	}
}

// A purge that fails must fail LOUD. Swallowing the error would report a
// successful closure over rows that still hold the learner's reflections.
func TestPGGoalKnowledgeRepo_SoftDeleteErrorsBubble(t *testing.T) {
	boom := context.DeadlineExceeded
	t.Run("by companion", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: &gkExecQuerier{err: boom}})
		if _, err := repo.SoftDeleteByCompanion(withCtx(), pgTenantID, pgUserGCID, gkCompanionID); err == nil {
			t.Fatal("want purge error to bubble")
		}
	})
	t.Run("by learner", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunnerWith{q: &gkExecQuerier{err: boom}})
		if _, err := repo.SoftDeleteByLearner(withCtx(), pgTenantID, pgUserGCID); err == nil {
			t.Fatal("want purge error to bubble")
		}
	})
	t.Run("no tenant context by companion", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunner{})
		if _, err := repo.SoftDeleteByCompanion(context.Background(), pgTenantID, pgUserGCID, gkCompanionID); err == nil {
			t.Fatal("a purge with no tenant context must refuse, not run unscoped")
		}
	})
	t.Run("no tenant context by learner", func(t *testing.T) {
		repo := NewGoalKnowledgeRepo(&stubTxRunner{})
		if _, err := repo.SoftDeleteByLearner(context.Background(), pgTenantID, pgUserGCID); err == nil {
			t.Fatal("a purge with no tenant context must refuse, not run unscoped")
		}
	})
}

// isNilArg reports a bind that will land as SQL NULL — either an untyped nil or
// a typed nil pointer (pgx encodes both as NULL).
func isNilArg(v any) bool {
	switch p := v.(type) {
	case nil:
		return true
	case *string:
		return p == nil
	case *time.Time:
		return p == nil
	default:
		return false
	}
}
