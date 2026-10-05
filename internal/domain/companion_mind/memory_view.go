// Package companionmind is the pure-domain read model for the ADR-215 WS-1
// tier-(a) learner-facing Companion "mind" view: what the designated Companion
// remembers (episodic recalls) + its persona/rules/focus + the visible KG
// neighbours, projected at LEARNER-appropriate depth (ADR-215 D5).
//
// Substrate: ADR-173's per-Companion pgvector RAG store (companion_memory_recall).
// The agent-facing Recall path does a cosine top-K search (needs a query
// embedding); this read model instead serves the PASSIVE learner panel — recent
// recalls by recency, with NO raw vectors, distances, model ids, or prompt
// internals ever surfaced (D5). "No memory yet" is returned honestly on an empty
// recall set (fail-loud, never fabricated — ADR-207 "Unknown is a state").
//
// Hexagonal: this package declares the MemoryReader port + pure assembly/
// resolution helpers only. It imports NO infrastructure (pgvector / SDK / HTTP)
// and builds no URLs. The concrete pg adapter + the resolving HTTP handler live
// in the adapter layer.
package companionmind

import (
	"context"
	"strings"
	"time"
)

// Bounds on how much of the Companion's memory + context rides one learner view.
const (
	// DefaultMemoryLimit is the recall count when the caller requests none.
	DefaultMemoryLimit = 20
	// MaxMemoryLimit caps a caller-requested recall count (learner panel, not a
	// bulk export).
	MaxMemoryLimit = 100
	// MaxNeighbors caps the visible KG neighbours in one view (learner-sized).
	MaxNeighbors = 12
	// MaxCitationsPerNeighbor caps resolved atom citations per neighbour concept.
	MaxCitationsPerNeighbor = 8
)

// MemoryTypeResearch is the memory_type of a note written by a Seeker skill's
// web_research sink — the ONLY memory_type that carries grounded provenance.
const MemoryTypeResearch = "research"

// ProvenanceCitation is one DURABLE source behind a grounded note (ADR-231 D4).
//
// ⚠ It has NO url, and that is the design. The live citation's uri is a Google
// grounding-api-redirect that EXPIRES (~30 days), so it was never persisted —
// re-serving one from a months-old note would hand the learner a dead link that
// still LOOKS like a working source, which is worse than no link at all. The
// domain (and the title, when it is a real headline rather than the site name)
// never expire, so they are the record.
type ProvenanceCitation struct {
	Domain  string
	Title   string
	Snippet string
}

// Provenance is where a grounded note came from: the web-search queries the
// model actually issued, plus the durable citations. Read back from the
// source_metadata JSONB (mig 0094).
//
// nil ⇒ genuinely unrecorded (every note written before mig 0094). That is an
// honest state, not a failure — and it is NEVER fabricated into an empty husk.
type Provenance struct {
	WebSearchQueries []string
	Citations        []ProvenanceCitation
}

// IsEmpty reports whether there is nothing to attribute — nil-safe, and TOTAL
// over the contract: a nil Provenance and an empty husk are the same fact.
//
// The adapters lean on this to keep one wire rule: provenance is emitted IF AND
// ONLY IF it exists. Shipping an empty husk would render as an attribution block
// with nothing in it, implying a web search that never happened.
func (p *Provenance) IsEmpty() bool {
	return p == nil || (len(p.WebSearchQueries) == 0 && len(p.Citations) == 0)
}

// EpisodicMemory is one recalled per-Companion memory, projected for the LEARNER
// audience (ADR-215 D5): a plain-language content string + recency only. The
// substrate's embedding, cosine distance, model id, and prompt internals are
// DELIBERATELY absent from this shape.
type EpisodicMemory struct {
	ID         string
	MemoryType string
	Content    string
	CreatedAt  time.Time
	// Provenance — grounded notes only (MemoryTypeResearch). nil for every other
	// memory_type, and for a research note written before mig 0094.
	Provenance *Provenance
}

// Citation resolves an atom UUID to a learner-readable name + topic path — the
// FE Citation{atomId, atomTitle, topicNodePath} shape (companion.model.ts). An
// unresolved atom (absent from the local atom_index projection) yields an empty
// AtomTitle, shown honestly as "unknown" and NEVER fabricated (D5).
type Citation struct {
	AtomID        string
	AtomTitle     string // "" = unresolved / unknown (honest, never fabricated)
	TopicNodePath string
}

// NeighborConcept is one visible KG neighbour in the Companion's context: the
// learner-owned concept node (its title is learner-authored, already stored) +
// the atom citations resolved from its atom refs.
type NeighborConcept struct {
	ConceptID    string
	ConceptTitle string
	Citations    []Citation
}

// InstanceContext carries the persona/rules/focus fields lifted from the
// Companion aggregate, decoupling this read model from the companion domain
// package's mutable entity.
type InstanceContext struct {
	CompanionID   string
	Name          string
	Focus         string // specialization
	Persona       string // persona_summary
	Rules         map[string]string
	EvolutionTier string
	Skills        []string
}

// MemoryView is the tier-(a) learner-facing Companion "mind" view (ADR-215 D2a).
type MemoryView struct {
	CompanionID      string
	Name             string
	Focus            string
	Persona          string
	Rules            map[string]string
	EvolutionTier    string
	Skills           []string
	HasMemory        bool // false + empty Memories = honest "no memory yet"
	Memories         []EpisodicMemory
	VisibleNeighbors []NeighborConcept
	// ResearchNotes are the Companion's grounded notes (MemoryTypeResearch), each
	// with the provenance behind it (CHO-2185). They are read as their OWN
	// memory_type-filtered list rather than sieved out of Memories: the recency
	// window is shared with chat_turns, so a chatty learner's research notes would
	// otherwise fall out of it and silently vanish from the panel.
	ResearchNotes []EpisodicMemory
}

// Query scopes a memory-view read to (tenant, learner, companion).
type Query struct {
	TenantID    string
	LearnerGCID string
	CompanionID string
	MemoryLimit int
	// MemoryTypes filters the read to these memory_types. Empty = every type
	// (the panel's recency read); []string{MemoryTypeResearch} = grounded notes.
	MemoryTypes []string
}

// MemoryReader returns the Companion's recent episodic recalls in RECENCY order
// (newest first), scoped by (tenant, companion) with tenant RLS + soft-delete +
// TTL applied by the adapter. This is the tier-(a) client-readable read path —
// distinct from the agent-facing cosine Recall (ADR-173) which needs a query
// embedding. Learner-appropriate projection only (no vectors / distances).
type MemoryReader interface {
	RecentMemories(ctx context.Context, q Query) ([]EpisodicMemory, error)
}

// ClampMemoryLimit normalises a requested recall count into [1, MaxMemoryLimit],
// defaulting a non-positive request to DefaultMemoryLimit.
func ClampMemoryLimit(n int) int {
	if n <= 0 {
		return DefaultMemoryLimit
	}
	if n > MaxMemoryLimit {
		return MaxMemoryLimit
	}
	return n
}

// TopicNodePath renders atom topic tags as a learner-readable path
// ("cs / recursion / base-case"). Blank/whitespace-only tags are dropped; an
// empty input yields "".
func TopicNodePath(tags []string) string {
	cleaned := make([]string, 0, len(tags))
	for _, t := range tags {
		if s := strings.TrimSpace(t); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	return strings.Join(cleaned, " / ")
}

// NewCitation builds a Citation from an atom id + a (possibly empty) resolved
// title + topic tags. An empty title is preserved as-is (unknown, never
// fabricated — D5).
func NewCitation(atomID, atomTitle string, topicTags []string) Citation {
	return Citation{
		AtomID:        atomID,
		AtomTitle:     atomTitle,
		TopicNodePath: TopicNodePath(topicTags),
	}
}

// AssembleView composes the learner-facing view from its resolved parts.
// HasMemory is derived (len(memories) > 0) so the FE renders an honest
// "no memory yet" empty-state (D5) without a fabricated placeholder. Nil
// collections/maps are normalised to non-nil empties for a stable wire shape.
//
// researchNotes carry their own provenance through untouched (CHO-2185) — a
// note whose provenance is nil stays nil, because a pre-0094 note's sources are
// genuinely unrecoverable and inventing them would be a lie.
func AssembleView(ic InstanceContext, memories []EpisodicMemory, neighbors []NeighborConcept, researchNotes []EpisodicMemory) MemoryView {
	if memories == nil {
		memories = []EpisodicMemory{}
	}
	if neighbors == nil {
		neighbors = []NeighborConcept{}
	}
	if researchNotes == nil {
		researchNotes = []EpisodicMemory{}
	}
	rules := ic.Rules
	if rules == nil {
		rules = map[string]string{}
	}
	skills := ic.Skills
	if skills == nil {
		skills = []string{}
	}
	return MemoryView{
		CompanionID:      ic.CompanionID,
		Name:             ic.Name,
		Focus:            ic.Focus,
		Persona:          ic.Persona,
		Rules:            rules,
		EvolutionTier:    ic.EvolutionTier,
		Skills:           skills,
		HasMemory:        len(memories) > 0,
		Memories:         memories,
		VisibleNeighbors: neighbors,
		ResearchNotes:    researchNotes,
	}
}
