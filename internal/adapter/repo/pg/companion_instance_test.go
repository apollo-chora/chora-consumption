// companion_instance_test.go — verifies the Postgres adapter for the
// multi-Companion (1:N) repo.
//
// Test surface:
//
//  1. RLS pair (SET LOCAL chora.tenant_id + chora.user_gcid) lands BEFORE
//     any user query on EVERY operation (per multi-tenant-rls SKILL).
//  2. Get + ListByOwner pgx Scan paths produce fully-populated
//     companion.Instance values from a canned column fixture (M14.1 paydown
//     — closes the demo-blocking empty-items[] defect surfaced by seed
//     agent #32).
//  3. Sentinel errors: ErrNoRows → companion.ErrInstanceNotFound on Get;
//     ListByOwner returns empty slice on ErrNoRows (NOT an error).
//  4. Defence-in-depth: bare context (no tenant_id) returns
//     rls.ErrNoTenantContext from every operation.
//
// Companion pattern: companion_chat_session_test.go (canned chatStubRow) +
// growth_scan_test.go (stubRows for multi-row paths).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---------------------------------------------------------------------------
// instanceStubRow / instanceStubQuerier — canned row fixture that lets tests
// pre-seed the loadCompanionInstanceSQL Scan result.
//
// Mirrors chatStubRow/chatStubQuerier — kept in-file (not in user_kg_test.go)
// so the two stub families stay independent + readable.
// ---------------------------------------------------------------------------

type instanceStubRow struct {
	values []any
	err    error
}

func (r *instanceStubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("instanceStubRow.Scan: argc mismatch")
	}
	for i, d := range dest {
		assignScanDest(d, r.values[i])
	}
	return nil
}

type instanceStubRows struct {
	values [][]any
	i      int
	err    error
}

func (r *instanceStubRows) Next() bool {
	return r.err == nil && r.i < len(r.values)
}
func (r *instanceStubRows) Scan(dest ...any) error {
	if r.i >= len(r.values) {
		return ErrNoRows
	}
	row := r.values[r.i]
	r.i++
	if len(dest) != len(row) {
		return errors.New("instanceStubRows.Scan: argc mismatch")
	}
	for j, d := range dest {
		assignScanDest(d, row[j])
	}
	return nil
}
func (r *instanceStubRows) Close() error { return nil }
func (r *instanceStubRows) Err() error   { return r.err }

// assignScanDest is the local scan-target adapter used by both
// instanceStubRow + instanceStubRows. Supports the pointer types projected
// by loadCompanionInstanceSQL (base 0006 columns: string, int, time.Time,
// *time.Time, []byte for JSONB) AND the additive 0032 growth-axis columns
// (bool, **string for nullable TEXT/UUID, **time.Time for nullable
// TIMESTAMPTZ).
//
// Per debt #44: listCompanionRosterByOwnerSQL projects nullable columns
// (species, species_rarity, resonant_atom_id, effective_llm_tier_cached,
// hatched_at, aha_moment_active_until, last_stage_up_at) — pgx scans
// them into pointer-to-pointer destinations so the production scan path
// matches.
func assignScanDest(dest any, value any) {
	switch tgt := dest.(type) {
	case *string:
		if v, ok := value.(string); ok {
			*tgt = v
		}
	case *int:
		if v, ok := value.(int); ok {
			*tgt = v
		}
	case *bool:
		if v, ok := value.(bool); ok {
			*tgt = v
		}
	case *time.Time:
		if v, ok := value.(time.Time); ok {
			*tgt = v
		}
	case **time.Time:
		if v, ok := value.(*time.Time); ok {
			*tgt = v
		}
	case **string:
		if v, ok := value.(*string); ok {
			*tgt = v
		}
	case *[]byte:
		if v, ok := value.([]byte); ok {
			*tgt = v
		}
	}
}

// instanceStubQuerier captures Exec calls + serves a scripted Row / Rows.
type instanceStubQuerier struct {
	execCalls    []string
	queryRowSQLs []string
	queryRowArgs [][]any
	queryRowSeq  []*instanceStubRow // FIFO — each QueryRow consumes one
	nextRow      *instanceStubRow   // fallback when queryRowSeq is empty
	nextRows     *instanceStubRows
	queryErr     error
}

func (s *instanceStubQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	return rls.CommandTag{RowsAffected: 1}, nil
}

func (s *instanceStubQuerier) QueryRow(_ context.Context, sql string, args ...any) Row {
	s.queryRowSQLs = append(s.queryRowSQLs, sql)
	s.queryRowArgs = append(s.queryRowArgs, args)
	if len(s.queryRowSeq) > 0 {
		row := s.queryRowSeq[0]
		s.queryRowSeq = s.queryRowSeq[1:]
		return row
	}
	if s.nextRow != nil {
		return s.nextRow
	}
	return &instanceStubRow{err: ErrNoRows}
}

func (s *instanceStubQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	if s.nextRows != nil {
		return s.nextRows, nil
	}
	return &instanceStubRows{}, nil
}

type instanceStubTxRunner struct {
	q *instanceStubQuerier
}

func (r *instanceStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &instanceStubQuerier{}
	}
	return fn(ctx, r.q)
}

// canonicalEiraValues mirrors the 0036_phyllis_eira_seed.up.sql payload at
// the 13-column loadCompanionInstanceSQL projection shape.
//
// Column order MUST mirror loadCompanionInstanceSQL:
//
//	companion_id, tenant_id, owner_gcid, name, specialization,
//	evolution_tier, skill_slots_unlocked, memory_context_capacity,
//	persona_summary, configured_rules (JSONB), guidance_note, persona_version,
//	created_at, updated_at, deleted_at
func canonicalEiraValues() []any {
	created := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	rules, _ := json.Marshal(map[string]any{
		"max_hint_count":      3,
		"max_session_minutes": 25,
	})
	return []any{
		"00000000-0000-7000-8000-00000000e1a0", // companion_id
		"11111111-1111-7111-8111-111111111111", // tenant_id
		"00000000-0000-7000-8000-000000001999", // owner_gcid
		"Eira",                                 // name
		"cspo",                                 // specialization
		"apprentice",                           // evolution_tier
		int(1),                                 // skill_slots_unlocked
		int(1000),                              // memory_context_capacity
		"Dragon Companion bonded to Phyllis at the Fledgling stage. Specialised on Certified Scrum Product Owner content. Curious, encouraging, cite-first.", // persona_summary
		rules,             // configured_rules (JSONB)
		"",                // guidance_note
		int(0),            // persona_version
		created,           // created_at
		created,           // updated_at
		(*time.Time)(nil), // deleted_at
	}
}

// ---------------------------------------------------------------------------
// Create — RLS + cap-check
// ---------------------------------------------------------------------------

func TestPGCompanionInstanceRepo_Create_AppliesRLS_AndCountsCap(t *testing.T) {
	// First QueryRow (cap-check) returns count=0 → Create can proceed.
	stub := &instanceStubQuerier{
		queryRowSeq: []*instanceStubRow{
			{values: []any{int(0)}},
		},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)

	inst, err := companion.NewInstance(pgTenantID, pgUserGCID, "Newton", "math")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := repo.Create(withCtx(), inst, 3); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(stub.execCalls) < 3 {
		t.Fatalf("expected ≥ 3 exec calls (rls pair + INSERT), got %d: %v", len(stub.execCalls), stub.execCalls)
	}
	if !contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q (missing tenant SET LOCAL)", stub.execCalls[0])
	}
	if !contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", stub.execCalls[1])
	}
	if !contains(stub.execCalls[2], "INSERT INTO companion_instances") {
		t.Errorf("call[2] = %q (missing INSERT)", stub.execCalls[2])
	}
}

// THE DB-DEFAULT HOLE (owner ruling 2026-08-07; ADR-248 D6 and §5).
//
// companion_instances.species carries a migration-0046 column DEFAULT that picks
// a random hero at INSERT. Omitting the column does NOT leave the row without a
// species: it mints one nobody rolled and nobody saw. ownerSpeciesSetSQL then
// reads that decoy as a species the learner OWNS, so it is excluded from the
// odds of their next ceremony and an explicit pick of it is 409'd, against a
// breed they have never been shown.
//
// The NULLIF(species,”) guard does NOT cover this: the DEFAULT writes a real
// non-blank value like 'dragon', which passes NULLIF and enters the owned set.
// The column must be NAMED. This is the same fix provisionEggInsertSQL already
// carries for the egg lane (CHO-2227); this INSERT is the other reachable path,
// serving BOTH POST /v1/me/companions and the sovereign acquire lane.
//
// LIMIT OF THIS TEST: it reads SQL TEXT. A stubbed Exec cannot watch a column
// DEFAULT fire, so this pins the fix but does not prove it at the database.
// TestIntegration_PodMystery_LegacyCreatePathLeavesNoSpecies is the real proof.
func TestInsertCompanionInstanceSQL_NamesSpeciesExplicitly(t *testing.T) {
	cols, vals, ok := strings.Cut(insertCompanionInstanceSQL, "VALUES")
	if !ok {
		t.Fatalf("insertCompanionInstanceSQL has no VALUES clause:\n%s", insertCompanionInstanceSQL)
	}
	if !strings.Contains(cols, "species") {
		t.Errorf("insertCompanionInstanceSQL does not name the `species` column, so the migration-0046 "+
			"random-hero DEFAULT fires and mints a species nobody rolled. That decoy enters the "+
			"learner's owned-species set and narrows their next ceremony's odds. Column list:\n%s", cols)
	}
	if !strings.Contains(vals, "NULL") {
		t.Errorf("insertCompanionInstanceSQL binds no NULL for `species`. A companion.Instance carries no "+
			"species at create time (the growth axis is owned by growth.go), so the column must be "+
			"written as an explicit NULL. VALUES clause:\n%s", vals)
	}
}

func TestPGCompanionInstanceRepo_Create_EnforcesRosterCap(t *testing.T) {
	// Cap-check returns count=3 → equal to maxPerUser → roster full.
	stub := &instanceStubQuerier{
		queryRowSeq: []*instanceStubRow{
			{values: []any{int(3)}},
		},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)
	inst, _ := companion.NewInstance(pgTenantID, pgUserGCID, "Newton", "math")

	err := repo.Create(withCtx(), inst, 3)
	if !errors.Is(err, companion.ErrRosterCapReached) {
		t.Fatalf("err = %v; want ErrRosterCapReached", err)
	}
	// INSERT must NOT have been emitted after cap miss.
	for _, c := range stub.execCalls {
		if contains(c, "INSERT INTO companion_instances") {
			t.Errorf("INSERT emitted after cap miss: %q", c)
		}
	}
}

// ---------------------------------------------------------------------------
// Get — Scan path (closes M14.1 stub gap)
// ---------------------------------------------------------------------------

func TestPGCompanionInstanceRepo_Get_ReturnsPopulatedInstance(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRow: &instanceStubRow{values: canonicalEiraValues()},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)

	got, err := repo.Get(withCtx(), "00000000-0000-7000-8000-00000000e1a0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil instance on hit")
	}
	if got.CompanionID != "00000000-0000-7000-8000-00000000e1a0" {
		t.Errorf("CompanionID = %q; want canonical Eira", got.CompanionID)
	}
	if got.TenantID != "11111111-1111-7111-8111-111111111111" {
		t.Errorf("TenantID = %q", got.TenantID)
	}
	if got.OwnerGCID != "00000000-0000-7000-8000-000000001999" {
		t.Errorf("OwnerGCID = %q", got.OwnerGCID)
	}
	if got.Name != "Eira" {
		t.Errorf("Name = %q; want Eira", got.Name)
	}
	if got.Specialization != "cspo" {
		t.Errorf("Specialization = %q; want cspo", got.Specialization)
	}
	if got.EvolutionTier != companion.TierApprentice {
		t.Errorf("EvolutionTier = %q; want apprentice", got.EvolutionTier)
	}
	if got.SkillSlotsUnlocked != 1 {
		t.Errorf("SkillSlotsUnlocked = %d; want 1", got.SkillSlotsUnlocked)
	}
	if got.MemoryContextCapacity != 1000 {
		t.Errorf("MemoryContextCapacity = %d; want 1000", got.MemoryContextCapacity)
	}
	if got.PersonaSummary == "" {
		t.Error("PersonaSummary empty")
	}
	if got.ConfiguredRules == nil {
		t.Fatal("ConfiguredRules nil — JSONB scan failed")
	}
	if got.ConfiguredRules["max_hint_count"] != "3" {
		t.Errorf("ConfiguredRules[max_hint_count] = %q; want 3", got.ConfiguredRules["max_hint_count"])
	}
	if got.ConfiguredRules["max_session_minutes"] != "25" {
		t.Errorf("ConfiguredRules[max_session_minutes] = %q; want 25", got.ConfiguredRules["max_session_minutes"])
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt zero")
	}
	if got.DeletedAt != nil {
		t.Errorf("DeletedAt = %v; want nil for non-deleted row", *got.DeletedAt)
	}
}

func TestPGCompanionInstanceRepo_Get_AppliesRLS(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRow: &instanceStubRow{values: canonicalEiraValues()},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)

	if _, err := repo.Get(withCtx(), "00000000-0000-7000-8000-00000000e1a0"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stub.execCalls) < 2 {
		t.Fatalf("expected ≥ 2 SET LOCAL calls, got %d", len(stub.execCalls))
	}
	if !contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", stub.execCalls[0])
	}
	if !contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", stub.execCalls[1])
	}
}

func TestPGCompanionInstanceRepo_Get_NotFound_MapsToTypedError(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRow: &instanceStubRow{err: ErrNoRows},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)
	_, err := repo.Get(withCtx(), "00000000-0000-7000-8000-00000000e1a0")
	if !errors.Is(err, companion.ErrInstanceNotFound) {
		t.Fatalf("err = %v; want ErrInstanceNotFound", err)
	}
}

func TestPGCompanionInstanceRepo_Get_FailsLoud_OnMissingTenantContext(t *testing.T) {
	tx := &instanceStubTxRunner{}
	repo := NewCompanionInstanceRepo(tx)
	// Bare ctx — no tenant_id.
	_, err := repo.Get(context.Background(), "00000000-0000-7000-8000-00000000e1a0")
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// TenantIsolation: when a row from a different tenant somehow surfaces
// (which RLS would prevent in real Postgres), the typed-error path is the
// closest stub-side behaviour test we can offer. The real defence is RLS at
// the DB layer; the helper-layer test for that is
// TestPGCompanionInstanceRepo_Get_FailsLoud_OnMissingTenantContext above.
//
// Per multi-tenant-rls SKILL: defence-in-depth — handler-layer + RLS GUC +
// per-DB role grants. The middle layer (rls.ApplySession) is exercised by
// TestPGCompanionInstanceRepo_Get_AppliesRLS.

// ---------------------------------------------------------------------------
// ListByOwner — Scan loop (closes M14.1 stub gap)
// ---------------------------------------------------------------------------

func TestPGCompanionInstanceRepo_ListByOwner_ReturnsAllInstances(t *testing.T) {
	created := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	rules, _ := json.Marshal(map[string]any{})
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{
			values: [][]any{
				// Row 1 — Pythagoras (owl S1)
				{
					"00000000-0000-7000-8000-00000000ab01", pgTenantID, pgUserGCID,
					"Pythagoras", "math", "apprentice", int(1), int(1000),
					"Wise owl Companion", rules, "", int(0), created, created, (*time.Time)(nil),
				},
				// Row 2 — Kepler (fox S3)
				{
					"00000000-0000-7000-8000-00000000ab02", pgTenantID, pgUserGCID,
					"Kepler", "astronomy", "adept", int(3), int(5000),
					"Curious fox Companion", rules, "", int(0), created, created, (*time.Time)(nil),
				},
				// Row 3 — Galileo (dragon S5)
				{
					"00000000-0000-7000-8000-00000000ab03", pgTenantID, pgUserGCID,
					"Galileo", "physics", "master", int(5), int(15000),
					"Bold dragon Companion", rules, "", int(0), created, created, (*time.Time)(nil),
				},
				// Row 4 — Eira (dragon S2) — the canonical Phyllis demo row
				{
					"00000000-0000-7000-8000-00000000e1a0", pgTenantID, pgUserGCID,
					"Eira", "cspo", "apprentice", int(1), int(1000),
					"Dragon Companion bonded to Phyllis", rules, "", int(0), created, created, (*time.Time)(nil),
				},
			},
		},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)

	out, err := repo.ListByOwner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("len(out) = %d; want 4", len(out))
	}
	names := make([]string, 0, len(out))
	for _, inst := range out {
		names = append(names, inst.Name)
	}
	for _, want := range []string{"Pythagoras", "Kepler", "Galileo", "Eira"} {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in returned set %v", want, names)
		}
	}
	// Verify Eira specifically (the demo-blocking row).
	var eira *companion.Instance
	for _, inst := range out {
		if inst.CompanionID == "00000000-0000-7000-8000-00000000e1a0" {
			eira = inst
			break
		}
	}
	if eira == nil {
		t.Fatal("Eira not in returned set")
	}
	if eira.Specialization != "cspo" {
		t.Errorf("Eira.Specialization = %q; want cspo", eira.Specialization)
	}
}

func TestPGCompanionInstanceRepo_ListByOwner_AppliesRLS(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{values: nil},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)
	if _, err := repo.ListByOwner(withCtx(), pgTenantID, pgUserGCID); err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(stub.execCalls) < 2 {
		t.Fatalf("expected ≥ 2 SET LOCAL calls, got %d", len(stub.execCalls))
	}
	if !contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", stub.execCalls[0])
	}
	if !contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", stub.execCalls[1])
	}
}

func TestPGCompanionInstanceRepo_ListByOwner_EmptyResult_IsNotAnError(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{values: nil},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)
	out, err := repo.ListByOwner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len(out) = %d; want 0 for empty result", len(out))
	}
}

func TestPGCompanionInstanceRepo_ListByOwner_FailsLoud_OnMissingTenantContext(t *testing.T) {
	tx := &instanceStubTxRunner{}
	repo := NewCompanionInstanceRepo(tx)
	_, err := repo.ListByOwner(context.Background(), pgTenantID, pgUserGCID)
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// ---------------------------------------------------------------------------
// Update — RLS (unchanged from M14.1 placeholder; kept to preserve coverage)
// ---------------------------------------------------------------------------

func TestPGCompanionInstanceRepo_Update_AppliesRLS(t *testing.T) {
	tx := &instanceStubTxRunner{}
	repo := NewCompanionInstanceRepo(tx)
	inst, _ := companion.NewInstance(pgTenantID, pgUserGCID, "Newton", "math")
	inst.EvolutionTier = companion.TierAdept // plain field mutation (ADR-218 D8)
	if err := repo.Update(withCtx(), inst); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatal("RLS pair + UPDATE not invoked")
	}
}

// ---------------------------------------------------------------------------
// SoftDelete — RLS (unchanged from M14.1 placeholder; kept to preserve cov)
// ---------------------------------------------------------------------------

func TestPGCompanionInstanceRepo_SoftDelete_AppliesRLS(t *testing.T) {
	tx := &instanceStubTxRunner{}
	repo := NewCompanionInstanceRepo(tx)
	if err := repo.SoftDelete(withCtx(), "01970000-0000-7000-8000-aaaaaaaaaaaa"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		count := 0
		if tx.q != nil {
			count = len(tx.q.execCalls)
		}
		t.Fatalf("expected ≥ 3 calls (rls pair + UPDATE), got %d", count)
	}
	if !contains(tx.q.execCalls[2], "UPDATE companion_instances") {
		t.Errorf("call[2] = %q (want UPDATE companion_instances)", tx.q.execCalls[2])
	}
}

// ---------------------------------------------------------------------------
// unmarshalConfiguredRules + stringifyJSONValue — coercion edge cases.
//
// The configured_rules JSONB seed in 0036 inserts integers (`{"max_hint_count":
// 3}`); other rows may carry booleans / nested values. The domain map is
// map[string]string — these helpers coerce without dropping data.
// ---------------------------------------------------------------------------

func TestUnmarshalConfiguredRules_EmptyBytes_ReturnsEmptyMap(t *testing.T) {
	got, err := unmarshalConfiguredRules(nil)
	if err != nil {
		t.Fatalf("unmarshalConfiguredRules(nil): %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestUnmarshalConfiguredRules_StringifiesMixedTypes(t *testing.T) {
	raw := []byte(`{"tone":"encouraging","max_hint_count":3,"strict":true,"ratio":1.5,"missing":null,"nested":{"a":1}}`)
	got, err := unmarshalConfiguredRules(raw)
	if err != nil {
		t.Fatalf("unmarshalConfiguredRules: %v", err)
	}
	want := map[string]string{
		"tone":           "encouraging",
		"max_hint_count": "3",
		"strict":         "true",
		"ratio":          "1.5",
		"missing":        "",
		"nested":         `{"a":1}`,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %q; want %q", k, got[k], v)
		}
	}
}

func TestUnmarshalConfiguredRules_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := unmarshalConfiguredRules([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// SQL strings — review-via-test (preserved from M14.1 placeholder).
// ---------------------------------------------------------------------------

func TestPGCompanionInstanceRepo_SQLStringsAreReviewed(t *testing.T) {
	for name, sql := range map[string]string{
		"insert":       insertCompanionInstanceSQL,
		"upsert":       upsertCompanionInstanceSQL,
		"load":         loadCompanionInstanceSQL,
		"listByOwner":  listCompanionInstancesByOwnerSQL,
		"countByOwner": countCompanionInstancesByOwnerSQL,
		"softDelete":   softDeleteCompanionInstanceSQL,
	} {
		if len(sql) < 20 {
			t.Errorf("%s SQL too short to be real: %q", name, sql)
		}
		if !contains(sql, "companion_instances") {
			t.Errorf("%s SQL missing companion_instances table reference", name)
		}
	}
}
