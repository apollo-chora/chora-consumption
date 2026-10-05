// companion_loadout_test.go — CHO-2012 pg loadout adapter seams: RLS session
// application on tenant-scoped grants ops, the not-owned / not-in-catalogue
// error mappings, and the tool_handler_ref decoding.
package pg

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func loadoutCtx() context.Context {
	return withCtx()
}

// loadoutStubQuerier wraps instanceStubQuerier with per-statement
// RowsAffected control (the shared stub always reports 1).
type loadoutStubQuerier struct {
	instanceStubQuerier
	zeroAffectedFor string
}

func (s *loadoutStubQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	tag, err := s.instanceStubQuerier.Exec(ctx, sql, args...)
	if s.zeroAffectedFor != "" && contains(sql, s.zeroAffectedFor) {
		tag.RowsAffected = 0
	}
	return tag, err
}

type loadoutStubTxRunner struct {
	q *loadoutStubQuerier
}

func (r *loadoutStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &loadoutStubQuerier{}
	}
	return fn(ctx, r.q)
}

func TestPGLoadout_SetEquipped_NotOwnedMapsError(t *testing.T) {
	tx := &loadoutStubTxRunner{q: &loadoutStubQuerier{zeroAffectedFor: "UPDATE companion_skill_grants"}}
	repo := NewCompanionLoadoutRepo(tx)
	err := repo.SetEquipped(loadoutCtx(), pgTenantID, "fam-1", "explain_anew", true)
	if !errors.Is(err, companion.ErrSkillNotOwned) {
		t.Fatalf("err = %v, want ErrSkillNotOwned on 0 rows affected", err)
	}
	// RLS session applied before the UPDATE.
	if len(tx.q.execCalls) < 3 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS session not applied first: %v", tx.q.execCalls)
	}
}

func TestPGLoadout_MintGrants_UnknownKeyFailsLoud(t *testing.T) {
	// resolveSkillID misses → ErrSkillNotInCatalog, never a silent skip.
	tx := &loadoutStubTxRunner{q: &loadoutStubQuerier{}} // QueryRow → ErrNoRows
	repo := NewCompanionLoadoutRepo(tx)
	_, err := repo.MintGrants(loadoutCtx(), pgTenantID, "fam-1", []companion.GrantMint{
		{SkillKey: "skill_tree_hack", UnlockedVia: "species_path", UnlockedAtStage: 2},
	})
	if !errors.Is(err, companion.ErrSkillNotInCatalog) {
		t.Fatalf("err = %v, want ErrSkillNotInCatalog", err)
	}
}

func TestPGLoadout_MintGrants_InsertedKeysReturned(t *testing.T) {
	q := &instanceStubQuerier{
		queryRowSeq: []*instanceStubRow{
			{values: []any{"0197-skill-id-1"}}, // resolve skill_id
		},
	}
	tx := &loadoutStubTxRunner{q: &loadoutStubQuerier{instanceStubQuerier: *q}}
	repo := NewCompanionLoadoutRepo(tx)
	inserted, err := repo.MintGrants(loadoutCtx(), pgTenantID, "fam-1", []companion.GrantMint{
		{SkillKey: "explain_anew", UnlockedVia: "species_path", UnlockedAtStage: 2},
	})
	if err != nil {
		t.Fatalf("MintGrants: %v", err)
	}
	if !reflect.DeepEqual(inserted, []string{"explain_anew"}) {
		t.Fatalf("inserted = %v, want [explain_anew] (stub reports 1 row affected)", inserted)
	}
	foundInsert := false
	for _, c := range tx.q.execCalls {
		if contains(c, "INSERT INTO companion_skill_grants") && contains(c, "ON CONFLICT (companion_id, skill_id) DO NOTHING") {
			foundInsert = true
		}
	}
	if !foundInsert {
		t.Fatalf("idempotent grant INSERT not issued: %v", tx.q.execCalls)
	}
}

func TestParseToolHandlerRefs(t *testing.T) {
	cases := map[string][]string{
		`["atom.search","atom.cite"]`: {"atom.search", "atom.cite"},
		`[]`:                          {},
		``:                            {},
		`legacy.single_ref`:           {"legacy.single_ref"},
	}
	for raw, want := range cases {
		if got := parseToolHandlerRefs(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("parseToolHandlerRefs(%q) = %v, want %v", raw, got, want)
		}
	}
}
