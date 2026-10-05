// build_prompt_v2.go - ADR-247 Capability A: the learner-aware campaign
// question prompt. BuildPromptV2 prepends goal/ancestor framing, the node
// sub-goal objective, and the diagnosed weakness onto the v1 rung/MCQ/single-
// correct instruction, so the generated questions target what the learner is
// trying to master and the gaps a diagnosis found, instead of the node title
// alone. It DEGRADES byte-for-byte to BuildPrompt when no enrichment is present
// (a node with no sub-goal, no weakness, and no lineage keeps today's prompt).
//
// The AI-kernel qgen crew is UNCHANGED: it consumes state["prompt"] verbatim,
// so the enrichment rides the prompt string only (no new wire fields, no RAG
// branch on the campaign path).
package campaignquestion

import (
	"fmt"
	"strings"
)

// WeaknessInput is the diagnosed-weakness framing for BuildPromptV2. Plain
// domain-level fields (the http/subscriber wiring maps lw.WeaknessContext into
// it) so the campaignquestion domain never imports the pg adapter.
type WeaknessInput struct {
	Descriptor     string
	Misconceptions []string
	Evidence       []string
}

// hasSignal reports whether the weakness carries anything worth injecting (a
// blank pointer degrades like no weakness).
func (w *WeaknessInput) hasSignal() bool {
	if w == nil {
		return false
	}
	return strings.TrimSpace(w.Descriptor) != "" || len(trimNonEmpty(w.Misconceptions)) > 0 || len(trimNonEmpty(w.Evidence)) > 0
}

// PromptContext is the learner-aware framing composed into BuildPromptV2: the
// goal + ancestor lineage (D3), the node's sub-goal objective (D1), and the
// diagnosed weakness (D2/F2). Every field is optional; an empty PromptContext
// yields the v1 prompt.
type PromptContext struct {
	GoalTitle string
	Ancestors []string // nearest-parent-first, excluding the focal node and the root goal
	SubGoal   string
	Weakness  *WeaknessInput
}

// BuildPromptV2 composes the enriched campaign generation prompt. Empty context
// returns exactly BuildPrompt(...).
func BuildPromptV2(nodeTitle, conceptKey, rungLabel string, count int, pc PromptContext) string {
	base := BuildPrompt(nodeTitle, conceptKey, rungLabel, count)
	framing := pc.framing()
	if framing == "" {
		return base
	}
	return framing + base
}

// framing renders the optional context block prepended to the base instruction.
// Returns "" when nothing is present (the degrade path).
func (pc PromptContext) framing() string {
	var b strings.Builder

	goal := strings.TrimSpace(pc.GoalTitle)
	anc := trimNonEmpty(pc.Ancestors)
	if goal != "" || len(anc) > 0 {
		b.WriteString("Frame these questions in service of the learner's goal")
		if goal != "" {
			fmt.Fprintf(&b, " %q", goal)
		}
		if len(anc) > 0 {
			fmt.Fprintf(&b, ", under %s", strings.Join(quoteEach(anc), " > "))
		}
		b.WriteString(".\n")
	}

	if sg := strings.TrimSpace(pc.SubGoal); sg != "" {
		fmt.Fprintf(&b, "Mastery of this node means: %s. Target the questions at that objective.\n", sg)
	}

	if pc.Weakness.hasSignal() {
		w := pc.Weakness
		parts := make([]string, 0, 3)
		if desc := strings.TrimSpace(w.Descriptor); desc != "" {
			parts = append(parts, "the gap: "+desc)
		}
		if mis := trimNonEmpty(w.Misconceptions); len(mis) > 0 {
			parts = append(parts, "misconceptions ("+strings.Join(mis, "; ")+")")
		}
		if ev := trimNonEmpty(w.Evidence); len(ev) > 0 {
			parts = append(parts, "evidence ("+strings.Join(ev, "; ")+")")
		}
		fmt.Fprintf(&b, "A diagnosis of this node found %s. Prioritise questions that expose and close these gaps.\n", strings.Join(parts, "; "))
	}

	return b.String()
}

// trimNonEmpty returns the trimmed, non-blank members of in (order preserved).
func trimNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// quoteEach %q-quotes each member (for the ancestor chain render).
func quoteEach(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}
