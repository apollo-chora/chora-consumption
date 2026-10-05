// Package config_test holds the RED-phase TDD specs for the PII Closure Map
// loader.
package config_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/config"
)

func TestLoadPIIClosureMap_FromBytes_ParsesYAML(t *testing.T) {
	t.Parallel()
	yaml := `
domain: chora_consumption
version: "1.0"
fields_to_tokenize:
  - table: t1
    columns:
      - column: c1
        strategy: tombstone_string
        value: "Former member"
retention_days_by_jurisdiction:
  EU: 2557
  default: 2557
on_creator_closure:
  strategy: tokenise_authorship_keep_atom
  show_authorship_as: "Former member"
`
	m, err := config.LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.Domain != "chora_consumption" {
		t.Fatalf("domain: %q", m.Domain)
	}
	if m.Version != "1.0" {
		t.Fatalf("version: %q", m.Version)
	}
	if len(m.FieldsToTokenize) != 1 {
		t.Fatalf("expected 1 table; got %d", len(m.FieldsToTokenize))
	}
	if m.RetentionDaysByJurisdiction["EU"] != 2557 {
		t.Fatalf("EU retention: %d", m.RetentionDaysByJurisdiction["EU"])
	}
	if m.OnCreatorClosure.Strategy != "tokenise_authorship_keep_atom" {
		t.Fatalf("creator closure strategy: %q", m.OnCreatorClosure.Strategy)
	}
}

func TestLoadPIIClosureMap_FromFile_DomainManifest(t *testing.T) {
	t.Parallel()
	m, err := config.LoadFromFile("../../config/PII_Closure_Map.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.Domain != "chora_consumption" {
		t.Fatalf("expected domain chora_consumption, got %q", m.Domain)
	}
	if len(m.FieldsToTokenize) == 0 {
		t.Fatalf("expected at least one tokenize entry")
	}
}

func TestLoadPIIClosureMap_RejectsEmptyDomain(t *testing.T) {
	t.Parallel()
	yaml := `
version: "1.0"
fields_to_tokenize: []
`
	_, err := config.LoadFromBytes([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("expected domain-required error; got %v", err)
	}
}

func TestLoadPIIClosureMap_RejectsBadStrategy(t *testing.T) {
	t.Parallel()
	yaml := `
domain: chora_consumption
version: "1.0"
fields_to_tokenize:
  - table: x
    columns:
      - column: y
        strategy: zap_to_void
        value: ""
`
	_, err := config.LoadFromBytes([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "strategy") {
		t.Fatalf("expected strategy validation error; got %v", err)
	}
}

func TestLoadPIIClosureMap_AllowedStrategies(t *testing.T) {
	t.Parallel()
	allowed := []string{
		"tombstone_string", "tombstone_email", "drop", "hash",
		"preserve", "tokenize", "encrypt", "cascade_delete",
	}
	for _, s := range allowed {
		if !config.IsAllowedStrategy(s) {
			t.Errorf("strategy %q should be allowed", s)
		}
	}
	if config.IsAllowedStrategy("zap_to_void") {
		t.Errorf("strategy zap_to_void should NOT be allowed")
	}
}

func TestLoadPIIClosureMap_AGIDApplicable(t *testing.T) {
	t.Parallel()
	yaml := `
domain: chora_consumption
version: "1.0"
fields_to_tokenize: []
`
	m, err := config.LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	expectAGID := (m.Domain == "chora_a2a")
	if m.AGIDApplicable != expectAGID {
		t.Fatalf("AGID applicability mismatch: domain=%s applicable=%v expect=%v", m.Domain, m.AGIDApplicable, expectAGID)
	}
}

// TestShippedMapNamesConversationMemory pins register 6.4 R6's third part.
//
// ⚠ AND STATES ITS LIMIT. This asserts the DECLARATION, which is all the map can
// carry. It does NOT assert that closure erases conversation memory, because it
// does not: ClosureRepository.Pseudonymise runs no per-table UPDATE and reports
// rows_touched as a declared-intent column count (see the "What this adapter
// does NOT do" note in adapter/repo/pg/closure_repository.go; CHO-2198 found the
// same no-op in all 9 closure-saga services). A test claiming otherwise here
// would be the exact green-test-over-a-gap this codebase forbids. When the
// executor lands, the test that proves erasure belongs beside IT.
func TestShippedMapNamesConversationMemory(t *testing.T) {
	m, err := config.LoadFromFile("../../config/PII_Closure_Map.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var spec *config.TableSpec
	for i := range m.FieldsToTokenize {
		if m.FieldsToTokenize[i].Table == "companion_memory_recall" {
			spec = &m.FieldsToTokenize[i]
			break
		}
	}
	if spec == nil {
		t.Fatal("companion_memory_recall is not declared; closure would leave the " +
			"learner's conversation untouched even once the executor exists")
	}
	got := map[string]string{}
	for _, c := range spec.Columns {
		got[c.Column] = c.Strategy
	}
	// The text AND the vector of it. Dropping only content_text would leave a
	// cosine-matchable, invertible representation of the same conversation.
	for _, col := range []string{"content_text", "embedding", "source_metadata"} {
		if got[col] != "drop" {
			t.Errorf("column %q strategy = %q, want drop", col, got[col])
		}
	}
}
