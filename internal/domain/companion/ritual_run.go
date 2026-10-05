// ritual_run.go — the stamped run record + the ADR-215 role-scoped
// projection. ONE capture (RitualRun, with a per-step decision stamp), TWO
// audiences: O+/auditor reads the full record; the learner reads a
// plain-language RitualRunStory that REDACTS model internals (prompt
// version/hash, raw tool ids). Extends the ADR-197 decision-stamp substrate to
// Ritual runs.
package companion

import (
	"fmt"
	"strings"
	"time"
)

// RunStatus is the terminal (or in-flight) state of a Ritual run.
type RunStatus string

const (
	RunStatusRunning       RunStatus = "running"
	RunStatusCompleted     RunStatus = "completed"
	RunStatusFailed        RunStatus = "failed"
	RunStatusSkippedBudget RunStatus = "skipped_budget"
	RunStatusBlocked       RunStatus = "blocked"
	// RunStatusCompanionSuspended is the ADR-252 containment denial (ADR-257
	// D7). Distinct from `blocked`, which means an Armor content block, and from
	// `failed`, which means the product went wrong. This one means an operator
	// deliberately paused a companion, so it takes the vocabulary
	// `skipped_budget` already established for a designed pause. Its wire value
	// matches the kennel's REJECTED error_code (KennelErrCodeCompanionSuspended).
	RunStatusCompanionSuspended RunStatus = "companion_suspended"
)

// StepStamp is the per-step decision stamp (ADR-197 substrate): the prompt
// version + content-hash, the tools actually invoked, and citations. The
// prompt version/hash + tool ids are AUDITOR-only (redacted from the learner
// projection); citations are learner-safe.
type StepStamp struct {
	StepIndex     int
	SkillKey      string
	PromptVersion string
	// PromptSource is "registry" or "embedded_fallback": WHICH prompt shaped
	// the turn. Auditor-only alongside the version, and worth as much: a
	// version cannot answer "was my override actually in effect", which is the
	// first question an operator asks when an answer surprises them.
	//
	// ADDITIVE on the decision_stamp JSONB. Like its siblings this column is
	// stored under GO FIELD NAMES, so a stamp written before this field simply
	// unmarshals it as "". Do NOT retag the existing fields to snake_case while
	// adding one: that would silently empty every stamp already written.
	PromptSource string
	PromptHash   string
	ToolsInvoked []string
	Citations    []string
}

// RitualRun is one execution of a Ritual revision (companion_ritual_runs row).
// Stamps is the decision_stamp JSONB — the O+ full record. RitualName +
// ReservationID are denormalised for the learner projection and the
// stale-running refund respectively.
type RitualRun struct {
	RunID         string
	TenantID      string
	CompanionID   string
	OwnerGCID     string
	RitualID      string
	RitualName    string
	RevisionNo    int
	TriggerSource string
	Status        RunStatus
	ManaCharged   int
	ReservationID string
	Stamps        []StepStamp
	// StepOutputs is the S8 per-step output record (step_outputs JSONB, ADR-257
	// section 6). Populated on EVERY terminal, so a failed or blocked run keeps
	// the outputs of the steps that did complete: a run that says "stopped
	// early" and shows nothing is indistinguishable from one that never started.
	StepOutputs []StepOutput
	// PausedCompanionID names the companion an operator suspended, set only on a
	// RunStatusCompanionSuspended terminal. Under a peer step it may not be the
	// companion the learner is looking at, so the run has to carry it rather
	// than let the reader assume.
	PausedCompanionID string
	SinkRef           string
	Error             string
	StartedAt         time.Time
	CompletedAt       *time.Time

	// traceparent/tracestate are TRANSIENT W3C trace-context carriers from
	// RunInput → the terminal ritual_run_completed envelope (unexported — the
	// runs table does not persist them). publishCompleted previously hardcoded
	// "" here, which failed outbox envelope validation and killed EVERY
	// terminal publish (CHO-2136 walk-found).
	traceparent string
	tracestate  string
}

// RitualRunStory is the LEARNER projection (ADR-215 D5 — plain language, no
// model internals). Marshalled to JSON for the Grimoire run-story view.
type RitualRunStory struct {
	Title    string            `json:"title"`
	Status   string            `json:"status"`
	Steps    []RitualStepStory `json:"steps"`
	ManaCost int               `json:"manaCost"`
	// PausedCompanionID is set only on a companion_suspended terminal, so the
	// client can name the paused companion from its own roster. An id, not a
	// name: this projection cannot resolve one and must not invent one.
	PausedCompanionID string `json:"pausedCompanionId,omitempty"`
}

// RitualStepStory is one step in learner language: the Skill's DISPLAY name, a
// plain sentence, and concrete sources (citations) — never the prompt
// hash/version or raw tool ids.
type RitualStepStory struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Sources []string `json:"sources,omitempty"`
	// Truncated says the summary is shorter than what the step produced, so a
	// cut-off answer is never read as the whole answer.
	Truncated bool `json:"truncated,omitempty"`
}

// CitationNeedsTitle reports whether a citation is an opaque identifier that
// must be resolved before a learner sees it.
//
// Exported so the ADAPTER collects exactly the ids this projection will filter.
// If the adapter used its own predicate the two would drift, and the drift is
// silent in the worse direction: the adapter would skip resolving something the
// projection then drops, and a real source would vanish with no error anywhere.
func CitationNeedsTitle(c string) bool { return isBareID(c) }

// isBareID reports whether a citation is an opaque identifier rather than
// something a learner can read.
//
// Deliberately narrow: it matches the canonical 8-4-4-4-12 UUID shape and
// nothing else, so a real source title is never mistaken for an id. A broader
// heuristic (say, "no spaces") would silently swallow one-word titles, and a
// dropped real source is a worse failure than a shown one.
func isBareID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// friendlyRunStatus maps a RunStatus to learner-facing copy.
func friendlyRunStatus(s RunStatus) string {
	switch s {
	case RunStatusCompleted:
		return "done"
	case RunStatusFailed:
		return "stopped early"
	case RunStatusBlocked:
		return "stopped for safety"
	case RunStatusSkippedBudget:
		return "paused, top up mana"
	case RunStatusCompanionSuspended:
		// A pause, not a fault. The companion is NOT named here: this projection
		// has no roster, and inventing a display name would be a fabrication.
		// The client renders the name from RitualRunStory.PausedCompanionID.
		return "paused, a companion is suspended"
	case RunStatusRunning:
		return "running"
	default:
		return string(s)
	}
}

// LearnerRunStory projects a RitualRun into the learner audience: Skill display
// names (from the catalogue), a plain sentence per step, and citations only.
// It NEVER surfaces StepStamp.PromptVersion / PromptHash / ToolsInvoked — the
// ADR-215 D5 learner-appropriate-depth invariant.
func LearnerRunStory(run *RitualRun, catalogue map[string]CatalogEntry) RitualRunStory {
	return LearnerRunStoryWithTitles(run, catalogue, nil)
}

// LearnerRunStoryWithTitles is LearnerRunStory plus the resolved source titles
// (D2 tail a). `titles` maps an atom id to the name a learner would recognise,
// resolved by the ADAPTER through the same reader D3's character sheet uses, so
// there is exactly one atom-title resolver in this service.
//
// A nil or incomplete map is the ordinary case, not an error: the resolve is
// best-effort and never fails a run read. An id nothing could name is OMITTED
// rather than shown, because a UUID is not a source a learner can act on, and
// the step is still honest without it: it says which skill ran.
func LearnerRunStoryWithTitles(run *RitualRun, catalogue map[string]CatalogEntry, titles map[string]string) RitualRunStory {
	outputs := make(map[int]StepOutput, len(run.StepOutputs))
	for _, o := range run.StepOutputs {
		outputs[o.StepIndex] = o
	}
	steps := make([]RitualStepStory, 0, len(run.Stamps))
	for _, st := range run.Stamps {
		name := st.SkillKey
		if e, ok := catalogue[st.SkillKey]; ok && e.Name != "" {
			name = e.Name
		}
		// The stamp keeps every citation (the O+ record is provenance and must
		// not be filtered); the LEARNER sees a source only when it has a NAME.
		//
		// The agent's cite_atom refs carry an atom_id and a revision_id and no
		// title, so the honest citation to RECORD is the id. `titles` is the
		// adapter's best-effort resolve of those ids, through the same reader
		// D3's character sheet uses. An id nothing could name is dropped rather
		// than printed: a UUID is not a source a learner can act on, and
		// ADR-215 D5 keeps machine internals out of this projection.
		//
		// Resolution is not rewriting: run.Stamps is untouched, so provenance
		// keeps every id whether or not anything could name it.
		var sources []string
		for _, c := range st.Citations {
			if !isBareID(c) {
				sources = append(sources, c)
				continue
			}
			// A blank resolved title is not a name. Rendering it would put an
			// empty bullet on the page, which reads as a source with no title
			// rather than as no source.
			if title := strings.TrimSpace(titles[c]); title != "" {
				sources = append(sources, title)
			}
		}
		// S8 clause 4: render what the step actually produced. A run recorded
		// before S8, or a step whose output was never captured, keeps the old
		// fixed sentence: an EMPTY summary would read as a step that did nothing.
		// The redaction is unchanged either way (ADR-215 D5 does not relax
		// because the data got richer).
		summary := fmt.Sprintf("Your companion used %s.", name)
		truncated := false
		if o, ok := outputs[st.StepIndex]; ok && o.Text != "" {
			summary = o.Text
			truncated = o.Truncated
		}
		steps = append(steps, RitualStepStory{
			Title:     name,
			Summary:   summary,
			Sources:   sources,
			Truncated: truncated,
		})
	}
	return RitualRunStory{
		Title:             run.RitualName,
		Status:            friendlyRunStatus(run.Status),
		Steps:             steps,
		ManaCost:          run.ManaCharged,
		PausedCompanionID: run.PausedCompanionID,
	}
}
