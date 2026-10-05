// grade_test.go — WS-C7 (CHO-2086, ADR-227 D13 addendum #3) RED tests for the
// server-side MCQ grader over the stored campaign question payload. The
// answers door grades EVERY submission here, by option IDENTITY (CHO-1627
// posture — never positional, never client-graded), against the verbatim
// BatchCandidatePayload the qgen terminal landed. The three wire placements
// mirror the shared candidate normalizer (candidate_normalizer.go): a
// {candidates:[...]} object, a bare [...] array, and a single candidate
// object; options sit flat (candidate.options) or nested
// (candidate.mcq_payload.options). Every option carries a stable option_id +
// an is_correct answer key (OpenAPI MCQOption required fields).
//
// CHO-2244: identity is now the SERVED id-space — the learner posts back the
// opaque servedOptionID the serve door minted, never the stored option_id (some
// qgen batches name the key in it). These tests therefore grade with
// servedOptionID(qIdx, label, text); served_options_test.go covers the honest
// end-to-end round-trip through the serve door itself.
package campaignquestion

import (
	"errors"
	"strings"
	"testing"
)

// A realistic BatchCandidatePayload OBJECT with two questions: q0 correct = "b",
// q1 correct = "c". Options carry option_id + is_correct + explainer, exactly
// as the qgen crew emits (OpenAPI AiAssistCandidate.mcq_payload / flat options).
const gradeObjectPayload = `{
  "candidates": [
    {"stem":"2+2?","question_type":"mcq","options":[
      {"option_id":"a","label":"A","text":"3","is_correct":false,"explainer":"too low"},
      {"option_id":"b","label":"B","text":"4","is_correct":true,"explainer":"exactly four"},
      {"option_id":"c","label":"C","text":"5","is_correct":false,"explainer":"too high"}
    ]},
    {"stem":"3+3?","question_type":"mcq","options":[
      {"option_id":"a","label":"A","text":"5","is_correct":false,"explainer":"no"},
      {"option_id":"b","label":"B","text":"7","is_correct":false,"explainer":"no"},
      {"option_id":"c","label":"C","text":"6","is_correct":true,"explainer":"six"}
    ]}
  ],
  "proposed_test_set": {"title":"Sums","order":["a","b"]}
}`

func TestGradeAnswer_ObjectPayload_IdentityGrade(t *testing.T) {
	// q0, correct option = the one displaying "4" (stored id "b").
	want := servedOptionID(0, "B", "4")
	got, err := GradeAnswer([]byte(gradeObjectPayload), 0, want)
	if err != nil {
		t.Fatalf("GradeAnswer: %v", err)
	}
	if !got.Correct {
		t.Errorf("selected the is_correct option → want correct")
	}
	if got.CorrectOptionID != want {
		t.Errorf("CorrectOptionID = %q; want the served id %q", got.CorrectOptionID, want)
	}
	if got.Explainer != "exactly four" {
		t.Errorf("Explainer = %q; want the correct option's explainer", got.Explainer)
	}
	// A wrong option for the SAME question → incorrect, key still resolved.
	wrong, err := GradeAnswer([]byte(gradeObjectPayload), 0, servedOptionID(0, "A", "3"))
	if err != nil {
		t.Fatalf("GradeAnswer wrong: %v", err)
	}
	if wrong.Correct {
		t.Errorf("selected a distractor → want incorrect")
	}
	if wrong.CorrectOptionID != want {
		t.Errorf("CorrectOptionID = %q; want %q even on a wrong answer", wrong.CorrectOptionID, want)
	}
}

func TestGradeAnswer_SecondQuestion_Indexed(t *testing.T) {
	// The question index is part of the derivation, so q1's ids are its own.
	want := servedOptionID(1, "C", "6")
	got, err := GradeAnswer([]byte(gradeObjectPayload), 1, want)
	if err != nil {
		t.Fatalf("GradeAnswer: %v", err)
	}
	if !got.Correct || got.CorrectOptionID != want {
		t.Errorf("q1 correct is the option displaying 6: got %+v", got)
	}
	// q0's served id for the same display text must NOT grade q1 (domain
	// separation by question index).
	if cross, _ := GradeAnswer([]byte(gradeObjectPayload), 1, servedOptionID(0, "C", "6")); cross.Correct {
		t.Errorf("a q0-scoped served id graded correct on q1 — question index is not separating the id-space")
	}
}

func TestGradeAnswer_BareArrayShape(t *testing.T) {
	payload := `[{"stem":"q","question_type":"mcq","options":[
		{"option_id":"x","label":"nope","text":"nope","is_correct":false,"explainer":"no"},
		{"option_id":"y","label":"yep","text":"yep","is_correct":true,"explainer":"yes"}]}]`
	want := servedOptionID(0, "yep", "yep")
	got, err := GradeAnswer([]byte(payload), 0, want)
	if err != nil {
		t.Fatalf("GradeAnswer bare array: %v", err)
	}
	if !got.Correct || got.CorrectOptionID != want {
		t.Errorf("bare array grade = %+v", got)
	}
}

func TestGradeAnswer_NestedMCQPayloadShape(t *testing.T) {
	payload := `{"candidates":[{"stem":"q","mcq_payload":{"options":[
		{"option_id":"1","label":"one","text":"one","is_correct":true,"explainer":"one"},
		{"option_id":"2","label":"two","text":"two","is_correct":false,"explainer":"two"}]}}]}`
	want := servedOptionID(0, "one", "one")
	got, err := GradeAnswer([]byte(payload), 0, want)
	if err != nil {
		t.Fatalf("GradeAnswer nested mcq_payload: %v", err)
	}
	if !got.Correct || got.CorrectOptionID != want {
		t.Errorf("nested mcq_payload grade = %+v", got)
	}
}

func TestGradeAnswer_SingleCandidateObject(t *testing.T) {
	// No `candidates` key — a single candidate object (1-element batch), the
	// pre-1c orchestrator emission the shared normalizer tolerates.
	payload := `{"stem":"q","question_type":"mcq","options":[
		{"option_id":"p","label":"nope","text":"nope","is_correct":false,"explainer":"no"},
		{"option_id":"q","label":"yep","text":"yep","is_correct":true,"explainer":"yes"}]}`
	want := servedOptionID(0, "yep", "yep")
	got, err := GradeAnswer([]byte(payload), 0, want)
	if err != nil {
		t.Fatalf("GradeAnswer single candidate: %v", err)
	}
	if !got.Correct || got.CorrectOptionID != want {
		t.Errorf("single candidate grade = %+v", got)
	}
}

// CHO-2253: an id matching no SERVED option for this question cannot come from
// honest play — the ids are server-minted, deterministic and question-scoped
// (CHO-2244), so the valid set is closed and server-known. Such an id means
// tampering, a stale pre-CHO-2244 bundle, or a client/server disagreement about
// which question this is. Grading it "incorrect" charges every one of those to
// the learner as a wrong answer, silently. Refuse instead.
func TestGradeAnswer_UnservedOptionID_FailsLoud(t *testing.T) {
	for _, id := range []string{
		"zzz",                       // junk
		"b",                         // a STORED id from this very payload
		servedOptionID(1, "C", "6"), // a real served id, but for the WRONG question
	} {
		_, err := GradeAnswer([]byte(gradeObjectPayload), 0, id)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("selected_option_id %q was never served for q0 — want ErrInvalid, got %v", id, err)
		}
	}
}

// Attribution: a payload the SERVER cannot grade is the server's defect and must
// win over the client's submission — otherwise a missing answer key reports as
// "your option id is invalid" and points the next reader at the browser.
func TestGradeAnswer_PayloadDefectBeatsUnservedID(t *testing.T) {
	noKey := `{"candidates":[{"stem":"q","options":[
		{"option_id":"a","label":"one","is_correct":false},
		{"option_id":"b","label":"two","is_correct":false}]}]}`
	// "a" is also an unserved id — the payload defect must still be the verdict.
	_, err := GradeAnswer([]byte(noKey), 0, "a")
	if !errors.Is(err, ErrUngradable) {
		t.Fatalf("a payload with no answer key must be ErrUngradable even when the id is unserved, got %v", err)
	}
}

func TestGradeAnswer_QuestionIndexOutOfRange_ErrInvalid(t *testing.T) {
	if _, err := GradeAnswer([]byte(gradeObjectPayload), 9, "b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("index 9 (2 questions) must be ErrInvalid, got %v", err)
	}
	if _, err := GradeAnswer([]byte(gradeObjectPayload), -1, "b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative index must be ErrInvalid, got %v", err)
	}
}

func TestGradeAnswer_EmptySelectedOption_ErrInvalid(t *testing.T) {
	if _, err := GradeAnswer([]byte(gradeObjectPayload), 0, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank selected_option_id must be ErrInvalid, got %v", err)
	}
}

func TestGradeAnswer_NoAnswerKey_ErrUngradable(t *testing.T) {
	// A served question whose options carry NO is_correct flag has no server
	// answer key — fail loud (never client-grade, never fabricate a correct).
	// Labels are present so this fails for the MISSING-KEY reason and not for
	// CHO-2244's missing-display-content reason.
	payload := `{"candidates":[{"stem":"q","options":[
		{"option_id":"a","label":"one","is_correct":false},
		{"option_id":"b","label":"two","is_correct":false}]}]}`
	_, err := GradeAnswer([]byte(payload), 0, "a")
	if !errors.Is(err, ErrUngradable) {
		t.Fatalf("no is_correct option must be ErrUngradable, got %v", err)
	}
	if !strings.Contains(err.Error(), "is_correct") {
		t.Fatalf("ErrUngradable fired for the wrong reason: %v", err)
	}
}

func TestGradeAnswer_NoOptions_ErrUngradable(t *testing.T) {
	payload := `{"candidates":[{"stem":"open ended","question_type":"oe","model_answer":"prose"}]}`
	if _, err := GradeAnswer([]byte(payload), 0, "a"); !errors.Is(err, ErrUngradable) {
		t.Fatalf("a candidate with no MCQ options must be ErrUngradable, got %v", err)
	}
}

func TestGradeAnswer_CorrectOptionMissingID_ErrUngradable(t *testing.T) {
	// The is_correct option carries no stored option_id — the payload is
	// malformed against the OpenAPI contract, so it is refused rather than
	// graded. Labels are present so this fails for the BLANK-ID reason and not
	// for CHO-2244's missing-display-content reason.
	payload := `{"candidates":[{"options":[
		{"option_id":"","label":"blank id","is_correct":true,"explainer":"blank id"},
		{"option_id":"b","label":"other","is_correct":false}]}]}`
	_, err := GradeAnswer([]byte(payload), 0, "b")
	if !errors.Is(err, ErrUngradable) {
		t.Fatalf("blank correct option id must be ErrUngradable, got %v", err)
	}
	if !strings.Contains(err.Error(), "usable option_id") {
		t.Fatalf("ErrUngradable fired for the wrong reason: %v", err)
	}
}

func TestGradeAnswer_MalformedAndEmpty_ErrUngradable(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":     ``,
		"blank":     `   `,
		"not-json":  `not json`,
		"bad-array": `[{`,
		"scalar":    `42`,
	} {
		if _, err := GradeAnswer([]byte(payload), 0, "a"); !errors.Is(err, ErrUngradable) {
			t.Errorf("%s payload must be ErrUngradable, got %v", name, err)
		}
	}
}
