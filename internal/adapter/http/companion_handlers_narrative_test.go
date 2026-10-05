package http

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestProseNarrative guards the Recommender-narrative sanitiser: the daily-dose
// AI endpoint must never leak a raw JSON envelope (the recommender's structured
// bail-out payload, sometimes fenced) into the learner's Companion panel as
// prose. proseNarrative returns "" for any non-prose input so callers fall back
// to the deterministic narrative.
func TestProseNarrative(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "fenced json envelope -> empty",
			in:   "```json\n{\"input_tokens\":1386,\"reason\":\"Unable to generate recommendations\",\"recommendations\":[],\"tokens_consumed_total\":1422}\n```",
			want: "",
		},
		{
			name: "unfenced json object -> empty",
			in:   `{"reason":"Unable to generate recommendations","recommendations":[]}`,
			want: "",
		},
		{
			name: "json array -> empty",
			in:   `[{"atom_id":"a1"},{"atom_id":"a2"}]`,
			want: "",
		},
		{
			name: "normal prose -> unchanged",
			in:   "I picked 2 review atoms to keep your streak alive.",
			want: "I picked 2 review atoms to keep your streak alive.",
		},
		{
			name: "prose with surrounding whitespace -> trimmed prose",
			in:   "  I picked 2 review atoms to keep your streak alive.  ",
			want: "I picked 2 review atoms to keep your streak alive.",
		},
		{
			name: "fenced prose -> unwrapped prose",
			in:   "```\nI picked 2 review atoms to keep your streak alive.\n```",
			want: "I picked 2 review atoms to keep your streak alive.",
		},
		{
			name: "empty -> empty",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only -> empty",
			in:   "   \n\t ",
			want: "",
		},
		{
			name: "empty fenced block -> empty",
			in:   "```json\n\n```",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := proseNarrative(tc.in)
			if got != tc.want {
				t.Fatalf("proseNarrative(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Shared cross-language vectors (DEBT-REGISTER 1.7).
//
// proseNarrative (Go, here) is the ORIGINAL and is what renders to a learner.
// asProse (TypeScript, chora-web) and the kennel's Python port re-implement the
// same predicate. Three implementations of one predicate is three things that
// can drift, and they ALREADY disagree: the Go original rejects bare scalars
// (42, true, null, a quoted string) because json.Valid accepts them, and the FE
// does not (DEBT-REGISTER 1.1 residue).
//
// chora-contracts/testdata/prose_narrative_vectors.json is the anti-drift
// mechanism: ONE file, read by every side, so adding a vector turns RED on
// whichever implementation does not handle it. Until this test existed the file
// was read by Python alone, which meant a change to the Go original turned
// nothing red and the "shared vectors" claim was false. Do not replace this
// with an inline copy: an inline copy is exactly the drift the file prevents.
//
// The inline corpus above stays. It is the observed-live regression set (real
// recommender bail payloads); this is the cross-language contract. They answer
// different questions.
// ---------------------------------------------------------------------------

type proseVector struct {
	Name string `json:"name"`
	In   string `json:"in"`
	Out  string `json:"out"`
}

type proseVectorDoc struct {
	Vectors []proseVector `json:"vectors"`
}

// loadProseVectors walks up from this file's directory to the monorepo root and
// loads the shared vectors. It fails loud rather than skipping: a missing
// contract must never pass as green.
func loadProseVectors(t *testing.T) proseVectorDoc {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("prose vectors: runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		candidate := filepath.Join(dir, "chora-contracts", "testdata", "prose_narrative_vectors.json")
		if raw, err := os.ReadFile(candidate); err == nil {
			var doc proseVectorDoc
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("prose vectors %s: malformed JSON: %v", candidate, err)
			}
			return doc
		}
		// Stop AT the monorepo root (the directory holding go.work). Without
		// this the walk climbs past a git worktree into whatever checkout
		// happens to contain it and reads THAT tree's vectors, which reports
		// green against a file nobody edited.
		if _, gerr := os.Stat(filepath.Join(dir, "go.work")); gerr == nil {
			t.Fatalf("chora-contracts/testdata/prose_narrative_vectors.json not found at the monorepo root %s: the shared prose contract is missing", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("chora-contracts/testdata/prose_narrative_vectors.json not found walking up from the test dir: the shared prose contract is missing")
		}
		dir = parent
	}
}

// TestProseNarrative_SharedVectors runs the Go original against every vector in
// the shared file. Adding a vector there now fails this test if Go disagrees,
// which is what makes the file a control rather than a claim.
func TestProseNarrative_SharedVectors(t *testing.T) {
	doc := loadProseVectors(t)

	// An empty or absent vector list would make every assertion below vacuous
	// and the suite would still report ok. Refuse that explicitly.
	if len(doc.Vectors) == 0 {
		t.Fatal("prose vectors: zero vectors loaded; every case below would pass vacuously")
	}

	seen := make(map[string]bool, len(doc.Vectors))
	for _, v := range doc.Vectors {
		if v.Name == "" {
			t.Fatalf("prose vectors: a vector has no name: %+v", v)
		}
		if seen[v.Name] {
			t.Fatalf("prose vectors: duplicate vector name %q; one would silently shadow the other", v.Name)
		}
		seen[v.Name] = true

		t.Run(v.Name, func(t *testing.T) {
			if got := proseNarrative(v.In); got != v.Out {
				t.Fatalf("proseNarrative(%q) = %q, want %q (shared vector %q)", v.In, got, v.Out, v.Name)
			}
		})
	}

	// The bare-scalar cases are the live Go-versus-TypeScript disagreement
	// (DEBT-REGISTER 1.1). If someone removes them the file stops pinning the
	// thing it exists to pin, so require them by name.
	for _, required := range []string{
		"bare_scalar_number",
		"bare_scalar_true",
		"bare_scalar_null",
		"quoted_string_is_still_json",
	} {
		if !seen[required] {
			t.Fatalf("prose vectors: required vector %q is missing; it pins the Go/TS disagreement on bare scalars", required)
		}
	}
}
