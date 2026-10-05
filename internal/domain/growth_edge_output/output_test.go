package growth_edge_output

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validArgs() NewArgs {
	return NewArgs{
		OutputID:      "019f89c7-3439-74cc-892d-5fb7d2726ea6",
		TenantID:      "11111111-1111-7111-8111-111111111111",
		LearnerGCID:   "00000000-0000-7000-8000-000000001999",
		UploadID:      "019f89c7-3418-70c4-bc6a-1396c35166fc",
		Kind:          KindStudyAids,
		Content:       json.RawMessage(`"Revise fluvial vs pluvial."`),
		Metered:       true,
		SourceEventID: "019f8a00-0000-7000-e000-000000000001",
		GeneratedAt:   time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
	}
}

func TestNew_Valid(t *testing.T) {
	got, err := New(validArgs())
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if got.Kind != KindStudyAids {
		t.Errorf("Kind = %q, want %q", got.Kind, KindStudyAids)
	}
	if !got.Metered {
		t.Error("Metered = false, want true")
	}
}

func TestNew_RejectsMissingIdentity(t *testing.T) {
	// Every one of these makes the artifact unattributable: it could not be
	// scoped by RLS, correlated to its upload, or shown to its owner.
	cases := map[string]func(*NewArgs){
		"output id":    func(a *NewArgs) { a.OutputID = "" },
		"tenant id":    func(a *NewArgs) { a.TenantID = "  " },
		"learner gcid": func(a *NewArgs) { a.LearnerGCID = "" },
		"upload id":    func(a *NewArgs) { a.UploadID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			args := validArgs()
			mutate(&args)
			if _, err := New(args); err == nil {
				t.Fatalf("New() with empty %s: want error, got nil", name)
			}
		})
	}
}

func TestNew_RejectsUnknownKind(t *testing.T) {
	// The kind drives rendering AND the DB CHECK. An unknown kind from a
	// producer must fail loud here, not 23514 at the INSERT.
	args := validArgs()
	args.Kind = Kind("focused_dose") // a real selection, but NOT a generated artifact
	if _, err := New(args); err == nil {
		t.Fatal("New() with unknown kind: want error, got nil")
	}
}

func TestNew_RejectsEmptyContent(t *testing.T) {
	// An artifact with no content is the WS-7 gap wearing a row: it looks
	// delivered and shows the learner nothing.
	for name, content := range map[string]json.RawMessage{
		"nil":   nil,
		"empty": json.RawMessage(""),
		"blank": json.RawMessage("   "),
	} {
		t.Run(name, func(t *testing.T) {
			args := validArgs()
			args.Content = content
			if _, err := New(args); err == nil {
				t.Fatal("New() with empty content: want error, got nil")
			}
		})
	}
}

func TestNew_RejectsInvalidJSONContent(t *testing.T) {
	// content lands in a JSONB column; malformed JSON would 22P02 at the
	// INSERT, which surfaces as an opaque projection failure and a NACK loop.
	args := validArgs()
	args.Content = json.RawMessage(`{"unclosed":`)
	if _, err := New(args); err == nil {
		t.Fatal("New() with malformed JSON content: want error, got nil")
	}
}

func TestNew_AcceptsBothKindShapes(t *testing.T) {
	// study_aids is prose (a JSON string); practice_test is an object. One
	// column holds both, so both must validate.
	args := validArgs()
	args.Kind = KindPracticeTest
	args.Content = json.RawMessage(`{"questions":[{"stem":"2+2?","answer":"4"}]}`)
	if _, err := New(args); err != nil {
		t.Fatalf("New() practice_test: unexpected error: %v", err)
	}
}

func TestNew_DefaultsGeneratedAtToCreatedAt(t *testing.T) {
	// A missing generated_at must not write a zero timestamp: the column is NOT
	// NULL and a zero time renders as year 1 on the surface.
	args := validArgs()
	args.GeneratedAt = time.Time{}
	got, err := New(args)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if got.GeneratedAt.IsZero() {
		t.Error("GeneratedAt is zero; want a fallback stamp")
	}
}

func TestParseKind(t *testing.T) {
	for _, in := range []string{"study_aids", "practice_test"} {
		if _, err := ParseKind(in); err != nil {
			t.Errorf("ParseKind(%q) unexpected error: %v", in, err)
		}
	}
	for _, in := range []string{"", "focused_dose", "familiar_coaching", "STUDY_AIDS"} {
		if _, err := ParseKind(in); err == nil {
			t.Errorf("ParseKind(%q): want error, got nil", in)
		}
	}
}

func TestKindsAreTotalOverTheMeteredContract(t *testing.T) {
	// The producer's METERED_OUTPUT_KINDS is ("practice_test", "study_aids"),
	// plus the un-metered companion_voice (ADR-254 D4: the diagnosis crew's
	// Companion voice step). If a kind is added there and not here, the
	// projection silently drops the new artifact and the learner pays for
	// something invisible again.
	want := map[string]bool{"study_aids": true, "practice_test": true, "companion_voice": true}
	got := map[string]bool{}
	for _, k := range AllKinds() {
		got[string(k)] = true
	}
	if len(got) != len(want) {
		t.Fatalf("AllKinds() = %v, want exactly %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("AllKinds() missing %q", k)
		}
	}
}

func TestNew_TrimsIdentifiers(t *testing.T) {
	args := validArgs()
	args.UploadID = "  019f89c7-3418-70c4-bc6a-1396c35166fc  "
	got, err := New(args)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if strings.TrimSpace(got.UploadID) != got.UploadID {
		t.Errorf("UploadID = %q, want trimmed", got.UploadID)
	}
}
