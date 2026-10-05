// companion_chat_grounding.go — CHO-2192 (ADR-231 D4/D5, ADR-225, IMDA D2):
// attribute a recalled research note in the chat turn that actually uses it.
//
// THE GAP THIS CLOSES. CHO-2179 persisted a grounded note's durable provenance;
// CHO-2185 gave a learner who re-opens that note a reader for it. Neither touched
// the moment the note is USED: the Companion vector-recalls it as RAG context,
// restates the web-researched claim inside it, and the learner reads a bare
// assertion with no source and no way to judge it. The sources were on disk the
// whole time.
//
// THE TRANSPORT, and why it is a HANDLER frame rather than an engine one. The
// handler already holds the recalled rows at step 2c — BEFORE it opens the engine
// stream. So the disclosure needs no engine change, no extra round-trip, no new
// route (the gateway's ChatStream is a byte-for-byte SSE pass-through), and it can
// be emitted ahead of the first token: the learner sees where the answer came from
// before they read the answer.
//
// ⚠ WE ATTRIBUTE WHAT WE INJECTED, NOT WHAT THE MODEL CLAIMS. This frame says
// "these grounded notes were in this turn's context", which is a fact we hold. It
// does not say "the model used them" — we cannot know that, and asserting it would
// be the same unfounded confidence the ticket exists to remove.
//
// ⚠ snake_case, unlike CHO-2185's camelCase read DTO — and that is deliberate, not
// drift. The gateway's CompanionBridge runs SnakeToCamelJSON over JSON *bodies*, so
// the read endpoint pre-camelises to survive it. The SSE path is copied verbatim
// ("copy frames byte-for-byte", companionbridge.go), so this wire keeps the
// snake_case of its five sibling frames. Copying the read DTO here would have
// silently mismatched the FE.
package http

import (
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// chatFrameGrounding is the SSE event type for the per-turn source disclosure.
// It joins session_open / tool_call / token / turn_complete / error.
const chatFrameGrounding = "grounding"

// Learner-sized render caps. Top-K recall is 5 notes and each may cite several
// sources, so the deduped union can get long enough to bury the answer it is
// meant to support. Dedup runs FIRST and the rows arrive nearest-first, so what
// survives a cap is the most relevant, not an arbitrary slice.
const (
	maxGroundingCitations = 8
	maxGroundingQueries   = 8
)

// groundingCitationDTO is one durable source on the wire.
//
// ⚠ There is NO url field, and that is the design (ADR-231 D4). The live citation
// uri is a Google grounding-api-redirect that expires in ~30 days and was never
// persisted; re-serving one from a months-old note would hand the learner a dead
// link that still LOOKS live — worse than no link, because it looks like it works.
// The absence is structural: companion.MemoryCitation has no url to leak.
type groundingCitationDTO struct {
	Domain  string `json:"domain,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// groundingFrameDTO is the `grounding` frame payload.
//
// citations + web_search_queries are ALWAYS arrays (never null) so the FE has one
// stable shape to read. UnrecordedNotes is the honest count of recalled research
// notes whose sources predate mig 0094.
type groundingFrameDTO struct {
	Citations        []groundingCitationDTO `json:"citations"`
	WebSearchQueries []string               `json:"web_search_queries"`
	UnrecordedNotes  int                    `json:"unrecorded_notes"`
}

// buildGroundingFrame folds the recalled memories into one per-turn disclosure.
//
// EMIT RULE — the frame is built IF AND ONLY IF at least one recalled note is
// GROUNDED (memory_type = research). Two consequences, both deliberate:
//
//  1. A turn that recalled only chat_turn / recap / ceremony notes emits NOTHING.
//     An empty attribution block would imply a web search that never happened.
//
//  2. A turn that recalled a research note written BEFORE mig 0094 emits a frame
//     with NO citations and unrecorded_notes >= 1. We do not fabricate sources —
//     and we do not stay silent either. Silence is indistinguishable from "no web
//     search happened", which is exactly the untraceable assertion this closes.
//     Grounded-but-unrecorded is a state we must be able to SAY out loud.
//
// Sources and queries are merged across the recalled set and deduped — several
// notes routinely cite the same domain, and the learner should see it once.
// Nearest-first recall order is preserved through the dedup.
func buildGroundingFrame(rows []companion.MemoryRow) (groundingFrameDTO, bool) {
	frame := groundingFrameDTO{
		Citations:        make([]groundingCitationDTO, 0, maxGroundingCitations),
		WebSearchQueries: make([]string, 0, maxGroundingQueries),
	}

	grounded := false
	seenCitation := make(map[string]bool)
	seenQuery := make(map[string]bool)

	for _, row := range rows {
		if !row.IsGrounded() {
			continue // never web-grounded ⇒ owes no source
		}
		grounded = true

		if row.Provenance.IsEmpty() {
			// A research note whose sources were never recorded (pre-0094), or an
			// empty husk the adapter already collapsed. Count it; invent nothing.
			frame.UnrecordedNotes++
			continue
		}

		for _, c := range row.Provenance.Citations {
			domain := strings.TrimSpace(c.Domain)
			title := strings.TrimSpace(c.Title)
			if domain == "" && title == "" {
				continue // nothing to name the source by — not a citation
			}
			key := strings.ToLower(domain) + "\x00" + strings.ToLower(title)
			if seenCitation[key] {
				continue
			}
			seenCitation[key] = true
			if len(frame.Citations) < maxGroundingCitations {
				frame.Citations = append(frame.Citations, groundingCitationDTO{
					Domain:  domain,
					Title:   title,
					Snippet: strings.TrimSpace(c.Snippet),
				})
			}
		}

		for _, q := range row.Provenance.WebSearchQueries {
			query := strings.TrimSpace(q)
			if query == "" {
				continue
			}
			if seenQuery[query] {
				continue
			}
			seenQuery[query] = true
			if len(frame.WebSearchQueries) < maxGroundingQueries {
				frame.WebSearchQueries = append(frame.WebSearchQueries, query)
			}
		}
	}

	return frame, grounded
}
