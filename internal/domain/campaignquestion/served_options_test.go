// served_options_test.go — CHO-2244 (SECURITY, amends WS-C7 Slice F): the
// pre-grade serve must carry ZERO answer signal — not in the option ids, and
// not in the option ORDER.
//
// Slice F stripped answer-revealing FIELDS but shipped ids + order verbatim.
// Measured on the live bank 2026-07-17 (20 ready sets / 40 questions / 160
// options), that left two leaks:
//
//   - IDS: a qgen batch (concept "Increment") minted opt_1_correct /
//     opt_1_distractor1..3. Devtools reads the key pre-submit.
//   - ORDER: the is_correct option sits at stored index 0 in 38/40 questions
//     (95%). The FE renders in payload order and assigns A/B/C/D positionally
//     and never shuffles — so "always pick A" clears rungs. This leak DOMINATES
//     the id leak, and it is why the ids the v1.0 story called neutral
//     (opt_1, opt_1a) are not neutral at all: they encode position.
//
// The fix derives every served option id from DISPLAY CONTENT the learner
// already sees (question index + label + text, hashed) and orders options by
// that hash. Nothing secret is involved: the id is a function of bytes already
// on screen, so it adds no information — while the stored option_id and the
// stored position, which DO correlate with the answer, become unrecoverable.
//
// These tests drive the serve door through its real exported surface, so they
// fail against the pre-fix implementation for the RIGHT reason (the leak),
// not merely because a symbol is missing.
package campaignquestion

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// --- helpers ---------------------------------------------------------------

// servedOption / servedQuestion decode the SERVED payload the way a browser
// does: an opaque option_id + the display text. No answer key — the serve door
// strips it.
type servedOption struct {
	OptionID string `json:"option_id"`
	Label    string `json:"label"`
	Text     string `json:"text"`
}

type servedQuestion struct {
	Stem       string         `json:"stem"`
	Options    []servedOption `json:"options"`
	MCQPayload *struct {
		Options []servedOption `json:"options"`
	} `json:"mcq_payload"`
}

func (q servedQuestion) opts() []servedOption {
	if len(q.Options) > 0 {
		return q.Options
	}
	if q.MCQPayload != nil {
		return q.MCQPayload.Options
	}
	return nil
}

// parseServed decodes a sanitised serve into its questions, tolerating the
// three BatchCandidatePayload wire shapes the normalizer emits.
func parseServed(t *testing.T, served []byte) []servedQuestion {
	t.Helper()
	trimmed := strings.TrimSpace(string(served))
	switch {
	case strings.HasPrefix(trimmed, "["):
		var arr []servedQuestion
		if err := json.Unmarshal(served, &arr); err != nil {
			t.Fatalf("parseServed array: %v", err)
		}
		return arr
	default:
		var env struct {
			Candidates []servedQuestion `json:"candidates"`
		}
		if err := json.Unmarshal(served, &env); err != nil {
			t.Fatalf("parseServed object: %v", err)
		}
		if env.Candidates != nil {
			return env.Candidates
		}
		var single servedQuestion
		if err := json.Unmarshal(served, &single); err != nil {
			t.Fatalf("parseServed single: %v", err)
		}
		return []servedQuestion{single}
	}
}

// findServedIDByLabel returns the served (opaque) option id of the option whose
// display label matches — exactly what a learner does when they click a choice.
func findServedIDByLabel(t *testing.T, q servedQuestion, label string) string {
	t.Helper()
	for _, o := range q.opts() {
		if o.Label == label {
			return o.OptionID
		}
	}
	t.Fatalf("no served option with label %q (served: %+v)", label, q.opts())
	return ""
}

// leakyIDPayload is the LIVE-observed shape (concept "Increment", 2026-07-17):
// option ids that name the key, correct option stored FIRST in every question.
const leakyIDPayload = `{
  "candidates": [
    {"stem":"What is an increment?","question_type":"mcq","draft_id":"d0","intent":"new_question","options":[
      {"option_id":"opt_1_correct","label":"A distinct usable piece of work","text":"A distinct usable piece of work","is_correct":true,"explainer":"yes"},
      {"option_id":"opt_1_distractor1","label":"A comprehensive final report","text":"A comprehensive final report","is_correct":false,"explainer":"no"},
      {"option_id":"opt_1_distractor2","label":"The initial planning phase","text":"The initial planning phase","is_correct":false,"explainer":"no"},
      {"option_id":"opt_1_distractor3","label":"A running list of tasks","text":"A running list of tasks","is_correct":false,"explainer":"no"}
    ]},
    {"stem":"What must an increment be?","question_type":"mcq","draft_id":"d1","intent":"new_question","options":[
      {"option_id":"opt_2_correct","label":"Fully functional and integrated","text":"Fully functional and integrated","is_correct":true,"explainer":"yes"},
      {"option_id":"opt_2_distractor1","label":"A complete overhaul","text":"A complete overhaul","is_correct":false,"explainer":"no"},
      {"option_id":"opt_2_distractor2","label":"Kept strictly confidential","text":"Kept strictly confidential","is_correct":false,"explainer":"no"},
      {"option_id":"opt_2_distractor3","label":"The entire scope of work","text":"The entire scope of work","is_correct":false,"explainer":"no"}
    ]}
  ],
  "proposed_test_set":{"title":"Increment","order":["d0","d1"],"points":{"d0":10}}
}`

// --- AC: Serve-shape guard -------------------------------------------------

// The story's guard, plus the strictly stronger positive assertion. A denylist
// of bad words is exactly the "trust the naming" reflex that caused this bug —
// so the real gate is an ALLOW-list on the served id shape.
//
// Scope matters: the denylist applies to option IDS only, never to the payload
// as a whole. Real questions legitimately SAY these words — live labels include
// "The decision is partially correct…" (on a WRONG option) and stems ask "which
// of the following correctly describes…". That text is the question; the learner
// reads it. A blob-wide word scan would fire on 4 of the 20 live sets and teach
// whoever hit it to weaken this test.
func TestSanitizeServedPayload_ServedOptionIDsCarryNoAnswerSignal(t *testing.T) {
	served, err := SanitizeServedPayload([]byte(leakyIDPayload))
	if err != nil {
		t.Fatalf("SanitizeServedPayload: %v", err)
	}
	banned := regexp.MustCompile(`(?i)correct|distractor|answer`)
	opaque := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := 0
	for qi, q := range parseServed(t, served) {
		for _, o := range q.opts() {
			seen++
			if banned.MatchString(o.OptionID) {
				t.Errorf("q%d: served option id %q names the answer key", qi, o.OptionID)
			}
			if !opaque.MatchString(o.OptionID) {
				t.Errorf("q%d: served option id %q is not an opaque [0-9a-f]{16} token", qi, o.OptionID)
			}
		}
	}
	// Guard the guard: a parse that silently yielded no options would make the
	// loop above vacuously green.
	if seen != 8 {
		t.Fatalf("checked %d served options; want 8 — the guard is not seeing the payload", seen)
	}
}

// --- AC: Id derivation is stored-id-blind ----------------------------------

// The sharpest statement of "the sanitiser must not trust id naming": two
// payloads that differ ONLY in their stored option_ids must serve BYTE-
// IDENTICAL bytes. If any stored id reaches the wire — verbatim, prefixed,
// hashed, or merely used to order the options — this fails.
func TestSanitizeServedPayload_IsBlindToStoredOptionIDs(t *testing.T) {
	// Same questions, same labels, same answer — only the stored ids differ.
	neutral := strings.NewReplacer(
		"opt_1_correct", "zq9", "opt_1_distractor1", "zq3", "opt_1_distractor2", "zq7", "opt_1_distractor3", "zq1",
		"opt_2_correct", "wk4", "opt_2_distractor1", "wk8", "opt_2_distractor2", "wk2", "opt_2_distractor3", "wk6",
	).Replace(leakyIDPayload)

	leakyServed, err := SanitizeServedPayload([]byte(leakyIDPayload))
	if err != nil {
		t.Fatalf("SanitizeServedPayload leaky: %v", err)
	}
	neutralServed, err := SanitizeServedPayload([]byte(neutral))
	if err != nil {
		t.Fatalf("SanitizeServedPayload neutral: %v", err)
	}
	if string(leakyServed) != string(neutralServed) {
		t.Errorf("served bytes depend on the STORED option ids — the sanitiser is trusting id naming\nleaky:   %s\nneutral: %s",
			leakyServed, neutralServed)
	}
}

// --- AC: Order leak closed -------------------------------------------------

// correctFirstPayload builds n questions each with the is_correct option stored
// FIRST — the live 95% shape. Labels are distinct per question so each option
// derives its own served id.
func correctFirstPayload(n int) string {
	var b strings.Builder
	b.WriteString(`{"candidates":[`)
	for q := 0; q < n; q++ {
		if q > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"stem":"q%d","question_type":"mcq","options":[`, q)
		for o := 0; o < 4; o++ {
			if o > 0 {
				b.WriteString(",")
			}
			// Stored index 0 is ALWAYS the answer.
			fmt.Fprintf(&b, `{"option_id":"q%d_opt%d","label":"q%d choice %d","text":"q%d choice %d","is_correct":%t,"explainer":"e"}`,
				q, o, q, o, q, o, o == 0)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// With the answer stored first in EVERY question, the served position of the
// correct option must be decorrelated from that stored index. Pre-fix it lands
// first 100% of the time ("always pick A" wins); post-fix the hash order should
// scatter it (~1 in 4).
func TestSanitizeServedPayload_DecorrelatesCorrectOptionPosition(t *testing.T) {
	const n = 12
	stored := correctFirstPayload(n)
	served, err := SanitizeServedPayload([]byte(stored))
	if err != nil {
		t.Fatalf("SanitizeServedPayload: %v", err)
	}
	qs := parseServed(t, served)
	if len(qs) != n {
		t.Fatalf("served %d questions; want %d", len(qs), n)
	}
	firstCount := 0
	positions := map[int]bool{}
	for q, sq := range qs {
		// The answer's label — its identity survives the re-key; its POSITION
		// must not.
		want := fmt.Sprintf("q%d choice 0", q)
		pos := -1
		for i, o := range sq.opts() {
			if o.Label == want {
				pos = i
				break
			}
		}
		if pos < 0 {
			t.Fatalf("q%d: correct option missing from the serve", q)
		}
		positions[pos] = true
		if pos == 0 {
			firstCount++
		}
	}
	if firstCount == n {
		t.Errorf("the correct option is served FIRST in all %d questions — stored order leaks the answer verbatim", n)
	}
	if firstCount > n/2 {
		t.Errorf("the correct option is served first in %d/%d questions — served position still correlates with the stored index", firstCount, n)
	}
	if len(positions) < 2 {
		t.Errorf("the correct option only ever lands at position(s) %v — the serve is not decorrelating order", positions)
	}
}

// --- AC: Grade round-trip (every wire shape) -------------------------------

// The doors must agree on the id-space across ALL three BatchCandidatePayload
// shapes and both option placements: whatever the serve renders, the answers
// door must grade — otherwise a shape-discrimination mismatch would silently
// mark honest answers wrong.
func TestServeGradeRoundTrip_AllWireShapes(t *testing.T) {
	cases := map[string]struct {
		payload      string
		qIdx         int
		correctLabel string
		explainer    string
	}{
		"object/flat options":    {leakyIDPayload, 0, "A distinct usable piece of work", "yes"},
		"object/second question": {leakyIDPayload, 1, "Fully functional and integrated", "yes"},
		"bare array": {`[{"stem":"q","question_type":"mcq","options":[
			{"option_id":"x","label":"wrong one","text":"wrong one","is_correct":false,"explainer":"no"},
			{"option_id":"y","label":"right one","text":"right one","is_correct":true,"explainer":"yes"}]}]`, 0, "right one", "yes"},
		"nested mcq_payload": {`{"candidates":[{"stem":"q","mcq_payload":{"options":[
			{"option_id":"1","label":"the answer","text":"the answer","is_correct":true,"explainer":"one"},
			{"option_id":"2","label":"not it","text":"not it","is_correct":false,"explainer":"two"}]}}]}`, 0, "the answer", "one"},
		"single candidate object": {`{"stem":"q","question_type":"mcq","options":[
			{"option_id":"p","label":"nope","text":"nope","is_correct":false,"explainer":"no"},
			{"option_id":"q","label":"yep","text":"yep","is_correct":true,"explainer":"yes"}]}`, 0, "yep", "yes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			served, err := SanitizeServedPayload([]byte(tc.payload))
			if err != nil {
				t.Fatalf("SanitizeServedPayload: %v", err)
			}
			qs := parseServed(t, served)
			if tc.qIdx >= len(qs) {
				t.Fatalf("serve produced %d questions; want index %d", len(qs), tc.qIdx)
			}
			// The learner clicks the right choice; the browser posts back the
			// opaque id it was served.
			picked := findServedIDByLabel(t, qs[tc.qIdx], tc.correctLabel)

			got, err := GradeAnswer([]byte(tc.payload), tc.qIdx, picked)
			if err != nil {
				t.Fatalf("GradeAnswer(served id %q): %v", picked, err)
			}
			if !got.Correct {
				t.Errorf("the served id of the correct option graded INCORRECT — serve and grade disagree on the id-space")
			}
			if got.CorrectOptionID != picked {
				t.Errorf("CorrectOptionID = %q; want the served id %q — the FE matches this against what it rendered",
					got.CorrectOptionID, picked)
			}
			if got.Explainer != tc.explainer {
				t.Errorf("Explainer = %q; want %q", got.Explainer, tc.explainer)
			}

			// A wrong pick still resolves the key, in the served id-space.
			for _, o := range qs[tc.qIdx].opts() {
				if o.OptionID == picked {
					continue
				}
				wrong, err := GradeAnswer([]byte(tc.payload), tc.qIdx, o.OptionID)
				if err != nil {
					t.Fatalf("GradeAnswer wrong pick: %v", err)
				}
				if wrong.Correct {
					t.Errorf("distractor %q graded correct", o.OptionID)
				}
				if wrong.CorrectOptionID != picked {
					t.Errorf("CorrectOptionID = %q on a wrong answer; want %q", wrong.CorrectOptionID, picked)
				}
				break
			}
		})
	}
}

// --- AC: Legacy ids rejected -----------------------------------------------

// The served ids are server-minted, so a stored-shape id can only come from a
// client that never saw it served. Accepting one would let an attacker blind-
// POST "opt_1_correct" and score correct without ever reading the payload.
//
// CHO-2253 sharpened the refusal from "grade it incorrect" to "fail loud": a
// stored id is not a wrong answer, it is a submission the server never issued.
func TestGradeAnswer_LegacyStoredOptionID_FailsLoud(t *testing.T) {
	for _, legacy := range []string{"opt_1_correct", "opt_1_distractor1"} {
		got, err := GradeAnswer([]byte(leakyIDPayload), 0, legacy)
		if got.Correct {
			t.Fatalf("a blind POST of the stored id %q graded CORRECT — legacy ids must never be accepted", legacy)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("a blind POST of the stored id %q must fail loud (ErrInvalid), got err=%v", legacy, err)
		}
	}
}

// --- AC: Ambiguous payload fails loud --------------------------------------

// Two options with identical display text are indistinguishable to the learner
// AND collide in the id-space. Never serve a silently ambiguous map, and never
// grade one — fail loud (never a fabricated correct).
func TestSanitizeServedPayload_DuplicateDisplayText_FailsLoud(t *testing.T) {
	dup := `{"candidates":[{"stem":"q","options":[
		{"option_id":"a","label":"same text","text":"same text","is_correct":true,"explainer":"yes"},
		{"option_id":"b","label":"same text","text":"same text","is_correct":false,"explainer":"no"}]}]}`
	if _, err := SanitizeServedPayload([]byte(dup)); !errors.Is(err, ErrUngradable) {
		t.Fatalf("duplicate display text must be ErrUngradable at serve, got %v", err)
	}
	if _, err := GradeAnswer([]byte(dup), 0, "whatever"); !errors.Is(err, ErrUngradable) {
		t.Fatalf("duplicate display text must be ErrUngradable at grade, got %v", err)
	}
}

// An option with NO display content cannot be rendered, distinguished, or
// keyed from what the learner sees. OpenAPI MCQOption makes label/text
// required; a payload without them is malformed — fail loud rather than fall
// back to the stored id or the stored index (both of which leak).
func TestSanitizeServedPayload_OptionsWithoutDisplayContent_FailLoud(t *testing.T) {
	blank := `{"candidates":[{"stem":"q","options":[
		{"option_id":"a","is_correct":true,"explainer":"yes"},
		{"option_id":"b","is_correct":false,"explainer":"no"}]}]}`
	if _, err := SanitizeServedPayload([]byte(blank)); !errors.Is(err, ErrUngradable) {
		t.Fatalf("options with no display content must be ErrUngradable at serve, got %v", err)
	}
}
