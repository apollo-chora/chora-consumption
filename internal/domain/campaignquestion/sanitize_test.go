// sanitize_test.go — WS-C7 Slice F (CHO-2086, SECURITY): the serve door must
// NEVER ship the answer key to the browser pre-submit. The stored payload is
// the verbatim BatchCandidatePayload — it carries per-option is_correct + the
// post-grade explainer (OpenAPI: "Sent to learner only post-grade") + OE
// model_answer/rubric. SanitizeServedPayload deep-strips every answer- or
// grading-revealing field from the SERVED copy (the stored row stays verbatim
// so the answers door still grades from it). Honest climbs depend on this.
package campaignquestion

import (
	"errors"
	"strings"
	"testing"
)

// A rich payload exercising every strip target across both option placements +
// an OE candidate + the (answer-free) proposed_test_set.
const sanitizeRichPayload = `{
  "candidates": [
    {"stem":"2+2?","question_type":"mcq","critic_notes":"B is the answer","composite":0.91,
     "answer_image_url":"https://cdn/ans.png","answer_image_gcs_uri":"gs://b/ans.png",
     "options":[
       {"option_id":"a","label":"A","text":"3","is_correct":false,"explainer":"too low"},
       {"option_id":"b","label":"B","text":"4","is_correct":true,"explainer":"exactly four"}
     ]},
    {"stem":"Explain gravity","question_type":"mcq",
     "mcq_payload":{"options":[
       {"option_id":"x","label":"X","text":"pull","is_correct":true,"explainer":"yes"}
     ]}},
    {"stem":"Open one","question_type":"oe","model_answer":"a full prose answer",
     "oe_payload":{"model_answer":"a full prose answer","grader_tier":"tier2",
       "rubric":[{"criterion_id":"c1","weight":0.5,"description":"clarity"}]}}
  ],
  "proposed_test_set":{"title":"Sums","order":["a","b"],"points":{"a":5}}
}`

func TestSanitizeServedPayload_StripsEveryAnswerRevealingField(t *testing.T) {
	out, err := SanitizeServedPayload([]byte(sanitizeRichPayload))
	if err != nil {
		t.Fatalf("SanitizeServedPayload: %v", err)
	}
	s := string(out)
	// Not a single byte of any answer-/grading-revealing key survives.
	for _, forbidden := range []string{
		"is_correct", "explainer", "model_answer", "oe_payload", "rubric",
		"grader_tier", "critic_notes", "composite", "answer_image_url", "answer_image_gcs_uri",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("served payload still leaks %q:\n%s", forbidden, s)
		}
	}
	// The learner-facing display fields DO survive (question is still answerable).
	// NB (CHO-2244): option_id VALUES are deliberately re-keyed, so the stored
	// ids ("x", …) must NOT be looked for here — only the key itself survives.
	// "a"/"b" below are proposed_test_set references, not option ids.
	for _, keep := range []string{
		"option_id", "\"label\"", "\"text\"", "stem", "2+2?", "question_type",
		"proposed_test_set", "order", "\"a\"", "\"b\"",
	} {
		if !strings.Contains(s, keep) {
			t.Errorf("served payload dropped display field %q:\n%s", keep, s)
		}
	}
	// The result must still parse + grade-shape into candidates (structurally sane).
	if _, derr := decodeCandidates(out); derr != nil {
		t.Fatalf("sanitized output must still decode as candidates: %v", derr)
	}
}

func TestSanitizeServedPayload_BareArrayAndSingle(t *testing.T) {
	arr := `[{"stem":"q","options":[{"option_id":"y","text":"t","is_correct":true,"explainer":"e"}]}]`
	out, err := SanitizeServedPayload([]byte(arr))
	if err != nil {
		t.Fatalf("array: %v", err)
	}
	if strings.Contains(string(out), "is_correct") || strings.Contains(string(out), "explainer") {
		t.Errorf("bare array not sanitized: %s", out)
	}
	if !strings.Contains(string(out), "option_id") {
		t.Errorf("bare array dropped option_id: %s", out)
	}

	// Display content is mandatory (CHO-2244): the served id is derived from it,
	// so an option without any is unservable rather than silently keyed off the
	// stored id.
	single := `{"stem":"q","options":[{"option_id":"z","label":"the only choice","is_correct":true,"explainer":"e"}]}`
	out2, err := SanitizeServedPayload([]byte(single))
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if strings.Contains(string(out2), "is_correct") || strings.Contains(string(out2), "explainer") {
		t.Errorf("single candidate not sanitized: %s", out2)
	}
}

func TestSanitizeServedPayload_MalformedFailsLoud(t *testing.T) {
	for name, bad := range map[string]string{
		"empty":     ``,
		"blank":     `   `,
		"not-json":  `not json`,
		"truncated": `{"candidates":[{`,
	} {
		if _, err := SanitizeServedPayload([]byte(bad)); !errors.Is(err, ErrUngradable) {
			t.Errorf("%s payload must fail loud (ErrUngradable), got %v", name, err)
		}
	}
}

// Sanitisation must not weaken grading: the STORED payload is unchanged, so
// GradeAnswer still resolves the key from it after a serve.
func TestSanitizeServedPayload_DoesNotMutateGradingSource(t *testing.T) {
	stored := []byte(sanitizeRichPayload)
	before := string(stored)
	if _, err := SanitizeServedPayload(stored); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if string(stored) != before {
		t.Fatal("SanitizeServedPayload mutated its input — the stored grading source must stay verbatim")
	}
	// The stored copy still grades (answer key intact server-side). The pick is
	// the SERVED id of the correct option (label "B" / text "4") — CHO-2244:
	// the learner never sees the stored id "b".
	got, err := GradeAnswer(stored, 0, servedOptionID(0, "B", "4"))
	if err != nil || !got.Correct {
		t.Fatalf("stored payload must still grade the correct option: %+v err=%v", got, err)
	}
}
