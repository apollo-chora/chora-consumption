package domain

// ADR-246 D1/D2 naming guard (CHO-2311). D1: the single-atom grading
// aggregate is AtomAttempt in pkg internal/domain/atom_attempt; no Go file
// in this module may still name the old package or type. D2 freeze: the
// wire event chora.consumption.atom_session.completed.v1, the
// chora_consumption.atomic_sessions table, and the /api/sessions +
// /v1/me/atom-sessions routes keep their legacy names on purpose; this
// test fails if a future rename touches them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot resolves services/chora-consumption from this test's dir
// (internal/domain).
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s has no go.mod: %v", root, err)
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestADR246AtomAttemptPackageExists(t *testing.T) {
	root := moduleRoot(t)
	sm := filepath.Join(root, "internal", "domain", "atom_attempt", "state_machine.go")
	src := readFile(t, sm)
	if !strings.Contains(src, "package atom_attempt") {
		t.Fatalf("%s does not declare package atom_attempt", sm)
	}
	if !strings.Contains(src, "type AtomAttempt struct") {
		t.Fatalf("%s does not declare type AtomAttempt", sm)
	}
}

// TestADR246RenameTotality walks every .go file in the module and fails on
// any reference to the retired package path or type token. The forbidden
// tokens are built by concatenation so this file never matches itself.
func TestADR246RenameTotality(t *testing.T) {
	root := moduleRoot(t)
	oldImport := "internal/domain/atom_" + "session\""
	oldType := "Atomic" + "Session"
	self := "atom_attempt_naming_test.go"

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, self) {
			return nil
		}
		src := readFile(t, path)
		if strings.Contains(src, oldImport) {
			t.Errorf("%s still imports the retired %s path", path, "atom_"+"session")
		}
		if strings.Contains(src, oldType) {
			t.Errorf("%s still references the retired %s type", path, oldType)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestADR246FrozenContracts pins the D2 legacy names: renaming any of these
// is a contract change and needs its own ADR, not a refactor.
func TestADR246FrozenContracts(t *testing.T) {
	root := moduleRoot(t)

	topicSeen := false
	eventsDir := filepath.Join(root, "internal", "adapter", "events")
	err := filepath.WalkDir(eventsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(readFile(t, path), "chora.consumption.atom_session.completed.v1") {
			topicSeen = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk events: %v", err)
	}
	if !topicSeen {
		t.Errorf("wire event chora.consumption.atom_session.completed.v1 not found under internal/adapter/events (D2 freeze)")
	}

	pgRepo := filepath.Join(root, "internal", "adapter", "repo", "pg", "atom_attempt.go")
	if !strings.Contains(readFile(t, pgRepo), "atomic_sessions") {
		t.Errorf("%s no longer targets the frozen atomic_sessions table (D2 freeze)", pgRepo)
	}

	ext := readFile(t, filepath.Join(root, "internal", "adapter", "http", "ext_server.go"))
	if !strings.Contains(ext, "/v1/me/atom-sessions") {
		t.Errorf("ext_server.go lost the frozen /v1/me/atom-sessions route (D2 freeze)")
	}
	router := readFile(t, filepath.Join(root, "internal", "adapter", "http", "router.go"))
	if !strings.Contains(router, "\"/api/sessions\"") {
		t.Errorf("router.go lost the frozen /api/sessions route (D2 freeze)")
	}

	// The outbox aggregate_type is persisted in outbox_events rows and rides
	// the publish path as metadata: a legacy VALUE, frozen like the topic.
	pub := readFile(t, filepath.Join(root, "internal", "adapter", "outbox", "publisher.go"))
	if !strings.Contains(pub, "cfg.AggregateType = \"atom_session\"") {
		t.Errorf("outbox publisher.go lost the frozen aggregate_type default \"atom_session\" (D2 freeze)")
	}
}
