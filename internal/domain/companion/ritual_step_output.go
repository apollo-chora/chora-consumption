// ritual_step_output.go: S8 step-output persistence (ADR-257 section 6).
//
// Until now a Ritual run recorded what ran, in what order, whether it finished
// and what it cost, and nothing about what any step PRODUCED: `outputs` was an
// in-memory buffer handed once to the sink and dropped with the stack frame
// (ritual_runner.go). So "what did step 2 say" was unanswerable the moment a run
// ended, and a run that stopped early could not show the learner where.
//
// The outputs live in a `step_outputs JSONB` column on companion_ritual_runs,
// parallel to `decision_stamp`. One row per run keeps the audit fact atomic,
// leaves the `tenant_isolation` RLS policy untouched and leaves closure's
// pseudonymisation key (`owner_gcid`) unchanged. A child table was rejected in
// the ADR: the runner writes the whole array once, at terminate, so it would buy
// nothing and add a second surface to the closure map and the RLS estate.
package companion

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Bounds on what a run may store. Explicit constants per ADR-257 section 6
// clause 3: a model can emit an arbitrarily long turn, and an unbounded JSONB
// column on an audit table is how one verbose run degrades every later read of
// that ritual's history.
const (
	// StepOutputRuneCap bounds ONE step's stored text.
	StepOutputRuneCap = 2000
	// RunOutputRuneCap bounds the whole run's stored text. A five-step run can
	// therefore not store five full caps; the later steps are marked instead.
	RunOutputRuneCap = 8000
	// StepOutputTruncationMark is appended to a cut string so the truncation is
	// visible in the text itself, not only in the flag. Matches the in-repo
	// rune-safe precedent (edgescout.capExcerpt).
	StepOutputTruncationMark = "…"
)

// StepOutput is one step's persisted output.
//
// ⚠ WIRE SHAPE. The sibling `steps` JSONB on companion_ritual_revisions keys
// SkillKey and Params by their GO FIELD NAMES, and retagging that would silently
// empty every stored revision, so it is pinned as-is. `step_outputs` is a NEW
// column with no legacy rows, so it uses the snake_case the rest of the wire
// uses. The two adjacent JSONB columns therefore differ ON PURPOSE, and
// TestStepOutput_WireShapeIsSnakeCaseAndPinned exists so a future reader cannot
// "harmonise" either one by accident.
type StepOutput struct {
	StepIndex int `json:"step_index"`
	// StepID is the step's stable identity. Empty for a legacy revision, which
	// carries none; such a step is keyed positionally and NEVER given a
	// fabricated id (ADR-257 section 7.4).
	StepID   string `json:"step_id,omitempty"`
	SkillKey string `json:"skill_key"`
	Text     string `json:"text"`
	// Truncated is set when the stored text is shorter than what the step
	// produced, whether the per-step cap or the per-run budget cut it. Never a
	// default: a short output is not a truncated one.
	Truncated bool `json:"truncated,omitempty"`
}

// StepOutputRecorder accumulates bounded, marker-neutralised step outputs across
// one run. The runner holds one per run and hands its Outputs() to terminate on
// EVERY path, which is what makes ADR-257 clause 2 (a failed or blocked run keeps
// the outputs of the steps that did complete) true by construction rather than
// by remembering to do it in three places.
type StepOutputRecorder struct {
	outputs   []StepOutput
	remaining int
}

// NewStepOutputRecorder starts a recorder with the full per-run budget.
func NewStepOutputRecorder() *StepOutputRecorder {
	return &StepOutputRecorder{outputs: []StepOutput{}, remaining: RunOutputRuneCap}
}

// Record captures one completed step's output. It is called only after a step
// SUCCEEDS: a failed step has no result and a blocked step carries no output, so
// neither contributes an entry and neither should.
func (r *StepOutputRecorder) Record(stepIndex int, step RitualStep, raw any) {
	if r == nil {
		return
	}
	text, truncated := r.fit(neutraliseForStorage(renderOutput(raw)))
	r.outputs = append(r.outputs, StepOutput{
		StepIndex: stepIndex,
		StepID:    step.StepID,
		SkillKey:  step.SkillKey,
		Text:      text,
		Truncated: truncated,
	})
}

// Outputs returns the recorded entries. Never nil: a run that completed nothing
// stores an empty array rather than a JSON null, so a reader never has to
// distinguish "no outputs" from "column absent".
func (r *StepOutputRecorder) Outputs() []StepOutput {
	if r == nil || r.outputs == nil {
		return []StepOutput{}
	}
	return r.outputs
}

// fit applies the per-step cap and the remaining per-run budget, whichever binds
// first, and reports whether anything was cut. A step that arrives with no budget
// left is still RECORDED, with empty text and the flag set: dropping it would
// silently shorten the run story and hide that a step ever ran.
func (r *StepOutputRecorder) fit(s string) (string, bool) {
	allowance := StepOutputRuneCap
	if r.remaining < allowance {
		allowance = r.remaining
	}
	if allowance < 0 {
		allowance = 0
	}
	runes := []rune(s)
	if len(runes) <= allowance {
		r.remaining -= len(runes)
		return s, false
	}
	if allowance == 0 {
		return "", true
	}
	r.remaining -= allowance
	return string(runes[:allowance]) + StepOutputTruncationMark, true
}

// renderOutput turns the port's `any` output into text FAITHFULLY. The
// production executor already hands over a string; a structured output is
// rendered as JSON rather than through a formatting verb that would invent prose
// the model never produced.
func renderOutput(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// neutraliseForStorage defangs the fence markers and control bytes in a stored
// output (ADR-257 section 6 clause 5). A stored output is model-authored text
// that is re-displayed and, under chaining, re-injected into a later prompt, so
// a crafted `<<<` sequence could otherwise close the frame it is placed in.
//
// It DEFANGS rather than drops, mirroring ScreenRecalledMemory's marker
// handling: the learner must still be able to read what their companion said,
// and an auditor must be able to see the attempt. Unlike that screen it performs
// no PII redaction, because a step output is the learner's own answer coming
// back to them and redacting it would destroy the content they asked for.
func neutraliseForStorage(s string) string {
	flat := memoryControlRE.ReplaceAllString(s, " ")
	flat = strings.ReplaceAll(flat, "<<<", "< < <")
	flat = strings.ReplaceAll(flat, ">>>", "> > >")
	flat = memoryWhitespaceRE.ReplaceAllString(flat, " ")
	return strings.TrimSpace(flat)
}
