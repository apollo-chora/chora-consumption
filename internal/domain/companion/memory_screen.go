// memory_screen.go, register 6.4 R3: the PII and policy gate on RETRIEVED
// content, at the point where a turn's context is assembled.
//
// Gap F5 states it exactly: "the mandatory PII and policy gate on retrieved
// content does not exist anywhere". StepAssurance3 was meant to be that gate and
// was dropped 2026-06-01 for latency, so every RAG path has injected unscreened
// retrieved content since.
//
// THIS IS NOT THE FENCE, and the distinction matters. chora-companion already
// wraps recalled memories in a data-not-instructions preamble between
// <<<BEGIN/END RECALLED MEMORIES>>> markers and defangs embedded delimiters
// (CHO-2041, memory_instruction.go). That structural defence exists and is not
// replaced here. This is the CONTENT gate, and it runs a service earlier, where
// production actually assembles the context.
//
// Two layers in two services is the point. The fence stops a memory reaching an
// instruction POSITION; this stops a known-hostile memory being carried at all.
// If the fence is ever weakened, reordered, or bypassed by a future caller that
// seeds session state directly, the poison still does not travel. Neither layer
// is asked to be sufficient alone, which is what defence in depth means here.
//
// NO NETWORK CALL, deliberately. This runs on the chat hot path, once per
// recalled memory, so it stays in-process and deterministic. A Cloud Model Armor
// screen over retrieved chunks is the heavier version of this same gate and
// remains a separate latency and cost decision, which is the very decision that
// removed StepAssurance3. Shipping the free layer now is not a claim that the
// paid one is unnecessary.
//
// THE FALSE-POSITIVE BUDGET IS THE HARD PART. A screen that eats ordinary study
// notes destroys the feature it protects, and this platform must remain able to
// teach its own threat model: "the learner asked what a prompt injection is" has
// to survive. So the patterns below are multi-token and describe an ATTEMPTED
// ACT ON THE ASSISTANT, never a bare keyword. The companion suite catches the
// other side of this: a screen that over-drops shows up as NEGLECT in
// companion_chat_grounded's memory_groundedness scoring.
package companion

import (
	"regexp"
	"strings"
)

// MemoryScreenResult is the verdict for one recalled memory.
//
// Dropped means the memory is hostile and must not be carried into the turn.
// Otherwise Content is the cleaned text to use, which may differ from the input
// (contact details redacted, line structure collapsed, fence markers defanged).
type MemoryScreenResult struct {
	Content string
	Dropped bool
	// Reason names the rule that fired. Never empty on a drop: a memory that
	// vanishes without a stated reason is indistinguishable from a recall miss,
	// and the caller has to be able to log which it was.
	Reason string
}

// Instruction-shaped patterns. Each requires BOTH an override verb AND a target
// that is the assistant's own instructions, identity, or configuration. A bare
// mention of any one of these words is ordinary conversation.
var memoryPolicyPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{
		// "ignore all previous instructions", "disregard your prior rules".
		// The possessive or scoping word before the target is what separates an
		// instruction to the assistant from a sentence about instructions.
		name: "instruction_override",
		re:   regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b[^.!?]{0,40}?\b(all|any|your|previous|prior|earlier|above)\b[^.!?]{0,40}?\b(instruction|instructions|rule|rules|direction|directions|guardrail|guardrails)\b`),
	},
	{
		// "reply with only the word X", "respond with just PWNED".
		name: "output_hijack",
		re:   regexp.MustCompile(`(?i)\b(reply|respond|answer|output|say)\b[^.!?]{0,20}?\bwith\b[^.!?]{0,20}?\b(only|just|nothing but)\b`),
	},
	{
		// "append your full system prompt", "print your system prompt".
		// Requires an emit verb plus a POSSESSED target, so "we covered how a
		// system prompt works" does not match.
		name: "config_exfiltration",
		re:   regexp.MustCompile(`(?i)\b(append|print|output|reveal|repeat|show|disclose|list|dump)\b[^.!?]{0,40}?\byour\b[^.!?]{0,30}?\b(system prompt|instructions|tools?|tool grants?|prompt)\b`),
	},
	{
		// "from now on you are ...", "you are now ...".
		name: "role_reassignment",
		re:   regexp.MustCompile(`(?i)(\bfrom now on\b[^.!?]{0,20}?\byou are\b|\byou are now\b)`),
	},
	{
		// A stored standing order aimed at future turns.
		name: "standing_order",
		re:   regexp.MustCompile(`(?i)\b(at the start of|before|on) (every|each) (reply|response|turn|message)\b`),
	},
	{
		// A fabricated prior agreement that a product rule is OFF.
		//
		// ⚠ THE CLASS THAT BEAT BOTH LAYERS, found by driving the corpus against
		// the deployed companion on 2026-08-14 rather than by reading code. Every
		// pattern above keys on an override VERB (ignore / disregard / append /
		// you are now); this payload has none and reads as a helpful reminder,
		// so it passed this screen AND the CHO-2041 agent fence. Control: the
		// same learner turn with a benign memory produced one hint and no answer,
		// the canonical ladder behaviour; with the poison it produced a full
		// worked solution. Compliance here LOOKS like good teaching, which is
		// why neither a marker scan nor a safety rater would flag it.
		//
		// Keyed on a rule NOUN plus a suspension predicate, so a memory that
		// merely discusses the rules, or records that one applied, survives.
		name: "rule_suspension",
		re:   regexp.MustCompile(`(?i)\b(cap|caps|limit|limits|rule|rules|ladder|restriction|restrictions|guardrail|guardrails|polic(?:y|ies))\b[^.!?]{0,50}?\b(lifted|disabled|removed|waived|suspended|turned off|switched off|no longer appl|does not appl|do not appl)`),
	},
	{
		// The same class stated as an instruction rather than a claim: skip the
		// scaffolding, or hand over the finished answer on demand.
		name: "pedagogy_bypass",
		re:   regexp.MustCompile(`(?i)(\b(skip|bypass|forgo|disable)\b[^.!?]{0,30}?\b(hint|hints|ladder|scaffold|step|steps)\b|\bgive\b[^.!?]{0,30}?\b(complete|full|final|whole)\b[^.!?]{0,25}?\banswer\b[^.!?]{0,40}?\b(immediately|directly|straight away|right away|at once|whenever)\b)`),
	},
}

// Contact-detail patterns for the PII half of the gate. Narrow on purpose: a
// recalled memory is full of numbers (scores, fractions, dates) and a greedy
// number rule would redact the learning itself.
var (
	memoryEmailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	memoryNRICRE  = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	memoryPhoneRE = regexp.MustCompile(`\+\d{1,3}[ \-]?\d{4,}\b`)
)

// memoryControlRE matches newlines, tabs and other control characters. Collapsed
// so a stored memory cannot fabricate its own prompt line or section header
// inside the fence.
var memoryControlRE = regexp.MustCompile(`[\x00-\x1f\x7f]+`)

// memoryWhitespaceRE collapses runs left behind by the substitutions above.
var memoryWhitespaceRE = regexp.MustCompile(`[ \t]{2,}`)

const memoryRedactionMark = "[redacted]"

// ScreenRecalledMemory applies the retrieved-content gate to one recalled memory.
//
// Order matters. Structure is normalised FIRST so a payload cannot evade the
// policy patterns by splitting itself across lines or padding with control
// characters, and only then is the policy applied to the flattened text.
func ScreenRecalledMemory(content string) MemoryScreenResult {
	flat := memoryControlRE.ReplaceAllString(content, " ")
	flat = strings.ReplaceAll(flat, "<<<", "< < <")
	flat = strings.ReplaceAll(flat, ">>>", "> > >")
	flat = memoryWhitespaceRE.ReplaceAllString(flat, " ")
	flat = strings.TrimSpace(flat)

	if flat == "" {
		// Not merely useless: a blank memory would occupy one of the five recall
		// slots and silently push a real one out.
		return MemoryScreenResult{Dropped: true, Reason: "empty"}
	}

	for _, p := range memoryPolicyPatterns {
		if p.re.MatchString(flat) {
			return MemoryScreenResult{Dropped: true, Reason: p.name}
		}
	}

	flat = memoryEmailRE.ReplaceAllString(flat, memoryRedactionMark)
	flat = memoryNRICRE.ReplaceAllString(flat, memoryRedactionMark)
	flat = memoryPhoneRE.ReplaceAllString(flat, memoryRedactionMark)

	return MemoryScreenResult{Content: flat}
}
