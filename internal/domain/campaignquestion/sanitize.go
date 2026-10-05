// sanitize.go — WS-C7 Slice F (CHO-2086) + CHO-2244 (SECURITY): strip the answer
// key from the SERVED question payload. Security & Privacy is the top conflict-
// resolution principle and the campaign ladder mechanic depends on honest
// climbs — so the pre-submit serve door must never ship a payload from which a
// learner (or a devtools/script read) can recover the answer.
//
// The stored QuestionSet.QuestionsPayload is the verbatim BatchCandidatePayload
// the qgen terminal landed; it carries per-option is_correct + the post-grade
// explainer (OpenAPI MCQOption: "Sent to learner only post-grade") + OE
// model_answer / rubric / grader_tier. SanitizeServedPayload works on a COPY;
// the stored row is never mutated, so the answers door still grades from the
// intact server-side key (GradeAnswer).
//
// Sanitising takes THREE passes, because the answer leaks through three
// channels — and Slice F originally closed only the first:
//
//  1. FIELDS — deep-strip is_correct / explainer / model_answer / rubric / …
//  2. IDS — re-key every option to a servedOptionID. qgen minted ids that name
//     the key (opt_1_correct / opt_1_distractor1..3, live 2026-07-17), so an id
//     is answer-bearing data, not an identifier. It is re-keyed unconditionally:
//     the sanitiser must never decide by inspecting the naming, because the
//     naming is exactly what it cannot trust.
//  3. ORDER — reorder options by that id. Measured over the live bank
//     2026-07-17, the is_correct option sits at stored index 0 in 38 of 40
//     questions (95%); the FE renders in payload order with positional A/B/C/D
//     markers and never shuffles, so stored order leaks the answer harder than
//     the ids ever did.
//
// All three run in ONE function on purpose: a strip without a re-key is a leak,
// so there is no call path that can perform half of it.
package campaignquestion

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// servedAnswerRevealingKeys are the payload keys deep-stripped before serving —
// answer keys, post-grade-only fields, OE model answers/rubrics, and crew-
// internal scoring/critique. Field names are the verbatim wire keys from the
// shared candidate shape (candidate_normalizer.go + OpenAPI AiAssistCandidate).
// Display fields (stem, question_type, option_id, label, text, images for the
// stem, citations, proposed_test_set order/points) are deliberately NOT here.
var servedAnswerRevealingKeys = map[string]bool{
	"is_correct":           true, // MCQ answer key — the pre-submit leak
	"explainer":            true, // OpenAPI: "Sent to learner only post-grade"
	"model_answer":         true, // OE model answer
	"oe_payload":           true, // OE answer envelope (model_answer + rubric + grader_tier)
	"rubric":               true, // OE grading criteria
	"grader_tier":          true, // OE grader metadata
	"critic_notes":         true, // qgen_critic critique — may name the answer
	"composite":            true, // qgen internal quality score
	"answer_image_url":     true, // MODEL-ANSWER illustration URL
	"answer_image_gcs_uri": true, // MODEL-ANSWER illustration object path
}

// SanitizeServedPayload returns a copy of the BatchCandidatePayload safe to put
// in front of a learner pre-submit: every answer-/grading-revealing field deep-
// stripped (at any nesting depth), every option re-keyed to an opaque
// servedOptionID, and every option list reordered by that id — across the
// object / bare-array / single-candidate shapes and both option placements.
//
// The input bytes are NEVER mutated: the stored row stays the verbatim qgen
// emission, so the answers door keeps grading from the intact server-side key.
//
// Fail-loud: a payload that cannot be parsed, or whose options cannot be keyed
// unambiguously from their display content, returns ErrUngradable — the serve
// door then refuses rather than shipping something it could not fully sanitise.
func SanitizeServedPayload(payload []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty payload", ErrUngradable)
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return nil, fmt.Errorf("%w: sanitize decode: %v", ErrUngradable, err)
	}
	served := stripAnswerRevealingKeys(v)
	if err := rekeyServedOptions(served); err != nil {
		return nil, err
	}
	out, err := json.Marshal(served)
	if err != nil {
		return nil, fmt.Errorf("%w: sanitize re-encode: %v", ErrUngradable, err)
	}
	return out, nil
}

// rekeyServedOptions walks the decoded payload to each candidate's option list
// and re-keys + reorders it in place.
//
// Shape discrimination mirrors decodeCandidates byte-for-byte (bare array /
// {candidates:[…]} object / single bare candidate). It has to: the question
// index feeds the id derivation, so if the two doors ever disagreed about which
// candidate is index N, the serve would mint ids the grader could not resolve
// and honest answers would silently grade wrong.
func rekeyServedOptions(v any) error {
	switch t := v.(type) {
	case []any:
		for i, c := range t {
			if err := rekeyCandidateOptions(c, i); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		raw, present := t["candidates"]
		if !present || raw == nil {
			// No candidates key — a single bare candidate (1-element batch),
			// the pre-1c orchestrator emission decodeCandidates tolerates.
			return rekeyCandidateOptions(t, 0)
		}
		arr, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%w: candidates is not an array", ErrUngradable)
		}
		for i, c := range arr {
			if err := rekeyCandidateOptions(c, i); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: payload is neither array nor object", ErrUngradable)
	}
}

// rekeyCandidateOptions replaces every option_id in one candidate with its
// servedOptionID and sorts the options by it. A candidate with no MCQ options
// (an OE question) is left alone — it carries no option key to leak.
func rekeyCandidateOptions(candidate any, questionIndex int) error {
	m, ok := candidate.(map[string]any)
	if !ok {
		return nil
	}
	opts := candidateOptionsTree(m)
	if len(opts) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(opts))
	for _, o := range opts {
		om, ok := o.(map[string]any)
		if !ok {
			continue
		}
		label, _ := om["label"].(string)
		text, _ := om["text"].(string)
		if !hasDisplayContent(label, text) {
			return fmt.Errorf("%w: question %d has an option with no display content — it cannot be keyed from what the learner sees",
				ErrUngradable, questionIndex)
		}
		id := servedOptionID(questionIndex, label, text)
		if seen[id] {
			return fmt.Errorf("%w: question %d has options with identical display text — the served id-space would be ambiguous",
				ErrUngradable, questionIndex)
		}
		seen[id] = true
		om["option_id"] = id
	}
	// Order by the opaque id: uniform over display content, and display content
	// is independent of correctness — so the served position carries no signal.
	sort.SliceStable(opts, func(i, j int) bool {
		return servedIDOf(opts[i]) < servedIDOf(opts[j])
	})
	return nil
}

// candidateOptionsTree resolves a candidate's option list from whichever
// placement the crew emitted — flat wins, then mcq_payload. Mirrors
// gradeCandidate.options so both doors see the same list.
func candidateOptionsTree(m map[string]any) []any {
	if a, ok := m["options"].([]any); ok && len(a) > 0 {
		return a
	}
	if mp, ok := m["mcq_payload"].(map[string]any); ok {
		if a, ok := mp["options"].([]any); ok {
			return a
		}
	}
	return nil
}

func servedIDOf(o any) string {
	if om, ok := o.(map[string]any); ok {
		if id, ok := om["option_id"].(string); ok {
			return id
		}
	}
	return ""
}

// stripAnswerRevealingKeys walks the decoded JSON, deleting every key in
// servedAnswerRevealingKeys wherever it appears and recursing into the rest.
// Operates on the freshly-unmarshalled tree (a copy), so the caller's bytes are
// untouched.
func stripAnswerRevealingKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if servedAnswerRevealingKeys[k] {
				delete(t, k)
				continue
			}
			t[k] = stripAnswerRevealingKeys(t[k])
		}
		return t
	case []any:
		for i := range t {
			t[i] = stripAnswerRevealingKeys(t[i])
		}
		return t
	default:
		return v
	}
}
