// grade.go — WS-C7 (CHO-2086, ADR-227 D13 addendum #3) server-side MCQ grader
// over a stored campaign question set. The answers door grades EVERY
// submission through GradeAnswer, by option IDENTITY (CHO-1627 posture, mirrors
// atom_index.Grade) against the verbatim BatchCandidatePayload the qgen
// terminal landed in QuestionSet.QuestionsPayload. Pure + deterministic: no
// I/O, no LLM, no client-supplied answer key.
//
// The payload wire shapes mirror the shared candidate normalizer
// (services/chora-creation .../candidate_normalizer.go): a {candidates:[...]}
// object (optionally with proposed_test_set), a bare [...] array, or a single
// candidate object (a 1-element batch, the pre-1c orchestrator emission).
// Options sit flat (candidate.options) OR nested (candidate.mcq_payload.options)
// — both placements the normalizer tolerates. Every option carries a stable
// option_id + an is_correct answer key (OpenAPI MCQOption required fields:
// option_id / label / text / is_correct / explainer).
package campaignquestion

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUngradable marks a served payload that carries no usable server answer
// key (malformed, no MCQ options, or no is_correct option id). Fail-loud: the
// answers door refuses rather than client-grading or fabricating a correct.
var ErrUngradable = errors.New("campaignquestion: question set not gradable")

// GradeResult is the outcome of grading one MCQ answer by option identity.
type GradeResult struct {
	// Correct — the selected option id equals the answer-key option id.
	Correct bool
	// CorrectOptionID — the answer-key option id (resolved even on a wrong
	// answer; the FE reveals it post-grade).
	CorrectOptionID string
	// Explainer — the correct option's explainer, when the payload carries one.
	Explainer string
}

// gradeOption is the minimal per-option decode: the stored identity key + the
// answer flag + the post-grade explainer + the display fields the served id is
// derived from (CHO-2244 — see served_id.go).
type gradeOption struct {
	OptionID  string `json:"option_id"`
	IsCorrect bool   `json:"is_correct"`
	Explainer string `json:"explainer"`
	Label     string `json:"label"`
	Text      string `json:"text"`
}

// gradeCandidate decodes one AiAssistCandidate's MCQ options from either wire
// placement (flat options OR mcq_payload.options).
type gradeCandidate struct {
	Options    []gradeOption `json:"options"`
	MCQPayload *struct {
		Options []gradeOption `json:"options"`
	} `json:"mcq_payload"`
}

// options resolves the candidate's MCQ options from whichever placement the
// crew emitted (flat wins; then mcq_payload).
func (c gradeCandidate) options() []gradeOption {
	if len(c.Options) > 0 {
		return c.Options
	}
	if c.MCQPayload != nil {
		return c.MCQPayload.Options
	}
	return nil
}

// GradeAnswer parses the stored BatchCandidatePayload and grades the answer at
// questionIndex by option IDENTITY. Deterministic, pure, no I/O.
//
// Identity here is the SERVED id-space (CHO-2244): the learner posts back an
// opaque servedOptionID minted by the serve door, so grading re-derives each
// stored option's served id and compares against that. The stored option_id is
// never matched on — it names the key in some qgen batches (opt_1_correct), so
// honouring it would let a client blind-POST "opt_1_correct" and score correct
// without ever fetching the question. CorrectOptionID likewise returns in the
// served id-space, because that is what the FE rendered and matches against.
//
// Errors (fail-loud — never a fabricated correct):
//   - ErrUngradable: malformed/empty payload, the indexed candidate has no MCQ
//     options, an option carries no display content or duplicates another's
//     (an ambiguous id-space), or no option carries an is_correct answer key
//     with a usable stored id.
//   - ErrInvalid:    questionIndex out of range, a blank selectedOptionID, or a
//     selectedOptionID that was never SERVED for this question (CHO-2253).
//
// Payload defects (ErrUngradable) are checked BEFORE the submission (ErrInvalid)
// deliberately: a payload the server cannot grade is the SERVER's defect, and
// reporting it as "your option id is invalid" would point the next reader at the
// browser.
//
// CHO-2253 — an unserved selectedOptionID fails loud rather than grading
// incorrect. This DIVERGES from atom_index's tolerance, on purpose: that lane
// serves stored option ids a client might plausibly mangle, so an unknown id
// there is just a wrong answer. Here CHO-2244 made the ids server-minted,
// deterministic and question-scoped, so the valid set is closed and known — an
// id outside it cannot come from honest play. It means tampering, a stale
// pre-CHO-2244 bundle, or a client/server disagreement about which question this
// is (e.g. a drifted question_index). Grading those "incorrect" would charge
// every one of them to the learner, silently.
func GradeAnswer(payload []byte, questionIndex int, selectedOptionID string) (GradeResult, error) {
	selectedOptionID = strings.TrimSpace(selectedOptionID)
	if selectedOptionID == "" {
		return GradeResult{}, fmt.Errorf("%w: selected_option_id required", ErrInvalid)
	}
	if questionIndex < 0 {
		return GradeResult{}, fmt.Errorf("%w: question_index %d is negative", ErrInvalid, questionIndex)
	}
	candidates, err := decodeCandidates(payload)
	if err != nil {
		return GradeResult{}, err
	}
	if questionIndex >= len(candidates) {
		return GradeResult{}, fmt.Errorf("%w: question_index %d out of range (%d question(s))", ErrInvalid, questionIndex, len(candidates))
	}
	opts := candidates[questionIndex].options()
	if len(opts) == 0 {
		return GradeResult{}, fmt.Errorf("%w: question %d carries no MCQ options", ErrUngradable, questionIndex)
	}
	// Re-derive the served id-space this question was shipped under. Ambiguity
	// here means the serve door refused too — the two doors share the rule.
	served := make([]string, len(opts))
	seen := make(map[string]bool, len(opts))
	for i, o := range opts {
		if !hasDisplayContent(o.Label, o.Text) {
			return GradeResult{}, fmt.Errorf("%w: question %d has an option with no display content — it cannot be keyed from what the learner sees",
				ErrUngradable, questionIndex)
		}
		served[i] = servedOptionID(questionIndex, o.Label, o.Text)
		if seen[served[i]] {
			return GradeResult{}, fmt.Errorf("%w: question %d has options with identical display text — the served id-space would be ambiguous",
				ErrUngradable, questionIndex)
		}
		seen[served[i]] = true
	}
	// Resolve the answer key first — a payload with none is the server's defect
	// and outranks whatever the client sent.
	key := -1
	for i, o := range opts {
		if o.IsCorrect && strings.TrimSpace(o.OptionID) != "" {
			key = i
			break
		}
	}
	if key < 0 {
		return GradeResult{}, fmt.Errorf("%w: question %d has no is_correct option with a usable option_id", ErrUngradable, questionIndex)
	}
	// CHO-2253: the served id-space is closed and server-known, so an id outside
	// it was never issued for this question — refuse rather than charge the
	// learner a wrong answer for tampering, a stale bundle, or an index drift.
	if !seen[selectedOptionID] {
		return GradeResult{}, fmt.Errorf("%w: selected_option_id %q was never served for question %d",
			ErrInvalid, selectedOptionID, questionIndex)
	}
	return GradeResult{
		Correct:         selectedOptionID == served[key],
		CorrectOptionID: served[key],
		Explainer:       opts[key].Explainer,
	}, nil
}

// decodeCandidates discriminates the three BatchCandidatePayload wire shapes by
// the first non-space char (the same rule as the shared candidate normalizer):
// `[` bare array, `{` object with a candidates[] key (else a single bare
// candidate). A malformed/empty/scalar payload is ErrUngradable.
func decodeCandidates(payload []byte) ([]gradeCandidate, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty payload", ErrUngradable)
	}
	switch trimmed[0] {
	case '[':
		var arr []gradeCandidate
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, fmt.Errorf("%w: candidate array decode: %v", ErrUngradable, err)
		}
		return arr, nil
	case '{':
		var envelope struct {
			Candidates []gradeCandidate `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			return nil, fmt.Errorf("%w: candidate object decode: %v", ErrUngradable, err)
		}
		if envelope.Candidates != nil {
			return envelope.Candidates, nil
		}
		// No `candidates` key — tolerate a single bare candidate object as a
		// 1-element batch (pre-1c orchestrator emission).
		var single gradeCandidate
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("%w: single candidate decode: %v", ErrUngradable, err)
		}
		return []gradeCandidate{single}, nil
	default:
		return nil, fmt.Errorf("%w: payload is neither array nor object", ErrUngradable)
	}
}
