// topic_affinity.go - ADR-245: two-tier topic affinity for the ADR-244 D2
// atom catalogue.
//
// ⚠ AN EARLIER VERSION OF THIS COMMENT CLAIMED "there is no topic model in this
// system". THAT WAS WRONG, and it is corrected here rather than deleted because
// the mistake is instructive. chora-creation ships a real tenant taxonomy
// (`topic_nodes`, CHO-2275/2276, with a `name` column), propagated on atom
// events as `topic_node_ids` and already decoded by this service at
// protodecode.go:730. What is genuinely absent is a topic reference on
// ConceptNode or Goal, and generalising that absence into "no topic model
// exists" is what produced the wrong claim.
//
// THE DEFECT THIS CLOSES. The catalogue is entitlement-scoped but not
// theme-scoped, so a Fractions goal hands the model a cross-domain atom menu and
// the ADR-244 D3 gate keeps whatever it cites, because that gate asks about
// ENTITLEMENT, not relevance.
//
// TIER 1, TAXONOMY (D1.2). An exact `topic_node_id` overlap is scoping by
// construction. The move that makes it reachable is that you never need the
// taxonomy NAME: names live in chora_creation and cross-DB reads are forbidden,
// but comparing id to id needs no name. The THEME side gets its ids from the
// atoms already attached to the focal concept, which the learner curated, so the
// anchor respects ADR-212 D1 sovereignty by construction (D1 rules on a
// concept's TITLE being learner-authored free text; it says nothing about the
// atoms the learner chose to attach).
//
// TIER 2, LEXICAL (D1/D2), and why it is the primary path in practice. The
// taxonomy bridge needs atoms ALREADY attached, and ADR-244 §5's census reports
// only 5 of 50 concept nodes carry any atom_refs - so for ~90% of concepts there
// is no anchor to derive, and those are exactly the concepts the proposal engine
// exists to fill. On top of that, wire field 7 carries FREEFORM CONTENT TAGS
// today, not taxonomy ids: chora-creation's encoder falls back to the composer's
// "tags" when no explicit topic_node_ids is set (protomarshal.go:291-293), and
// nothing repo-wide sets that key. So the lexical tier is what actually runs,
// and the taxonomy tier is a promotion when the evidence exists, never a
// precondition.
//
// ⚠ THE FIELD IS NOT EMPTY, IT CARRIES A DIFFERENT POPULATION. An earlier
// version of this comment said "the producer never populates it", inferred from
// a grep for the identifier TopicNodeIds returning nothing. The grep was true
// and the inference was false: the encoder works with MAP KEYS, so no grep for
// the Go field name could have found it. Corrected here because the same wrong
// conclusion was reached independently twice.
//
// WHY DERIVED AND NOT A NEW STORED FIELD. A topic column on concept_nodes was
// rejected (ADR-245 §4.1): it needs a writer, a backfill and an owner, it would
// sit empty on every existing row exactly as topic_tags does today, and it would
// be a THIRD topic concept beside the taxonomy and the tags. The concept side is
// not short of signal, it is short of a COMPARABLE FORM. This file is that form.
//
// WHY IT WORKS ON A SPARSELY-TAGGED CORPUS. Measured live 2026-07-20: only 30
// of 221 atom_index rows carry topic_tags at all (13.6%), and on the walk
// learner's Fractions goal a hard filter on topic_tags leaves 0 of 16 entitled
// atoms. The most on-theme atom in the corpus, "Comparing Fractions: 2/3 vs
// 3/5", is UNTAGGED. So tags are treated as a HIGH-WEIGHT signal that is
// usually absent, and titles as a lower-weight signal that is always present.
// Coverage of 13.6% is therefore a floor rather than a dependency, and tagging
// an atom measurably promotes it (TestAffinity_TagOutranksTitleOnly).
//
// WHY IT CANNOT STARVE A CATALOGUE. Nothing here filters. RankByAffinity
// REORDERS and returns every atom it was given, for every theme, including a
// theme that matches nothing and a theme that is empty. An empty theme yields
// AffinityUnscored on every atom and the input order untouched, so a caller
// can detect "no topic identity" from the result alone and degrade to exactly
// its pre-ADR-245 behaviour. Relevance shapes what the model SEES and how it is
// INSTRUCTED; it never gates. The entitlement gate (ADR-244 D3) is untouched
// and remains the only thing that drops an atom_ref.
//
// HEXAGONAL purity: stdlib-only, no infra imports, no clock (mirrors the rest
// of this package).
package conceptgraph

import (
	"sort"
	"strings"
)

// Affinity bands. These are a PRESENTATION and ORDERING vocabulary, not a
// filter: no band causes an atom to be withheld from a catalogue.
const (
	// AffinityOnTheme - the atom's topic identity clearly overlaps the theme.
	AffinityOnTheme = "on_theme"
	// AffinityRelated - some overlap, too weak to call on-theme.
	AffinityRelated = "related"
	// AffinityOffTheme - no overlap found. NOT a verdict that the atom is
	// irrelevant, only that this corpus gave no evidence either way.
	AffinityOffTheme = "off_theme"
	// AffinityUnscored - no topic identity was resolvable for the theme, so
	// nothing was compared. Distinct from off_theme ON PURPOSE: reporting "no
	// evidence" as "not relevant" is what would let a caller build a filter
	// that empties the catalogue for every untagged tenant.
	AffinityUnscored = "unscored"
)

// Signals name WHICH tier produced a verdict. This is the observability seam
// that makes an inert taxonomy tier visible: "the Companion proposed nothing
// useful" and "the taxonomy branch never fired" are otherwise identical.
const (
	// SignalTaxonomy - decided by an exact topic_node_id overlap (ADR-245 D1.2).
	SignalTaxonomy = "taxonomy"
	// SignalLexical - decided by normalised term overlap (the D1/D2 fallback).
	SignalLexical = "lexical"
	// SignalNone - no evidence of either kind.
	SignalNone = "none"
)

// maxLexicalScore is the ceiling of the lexical tier (weightTag 1.0 at full
// coverage). Taxonomy scores start strictly above it, so the two tiers occupy
// disjoint ranges and a score alone identifies its tier.
const maxLexicalScore = 1.0

// taxonomyScoreBase puts every taxonomy hit above every lexical hit. Within the
// tier, overlap coverage breaks ties, so broader classification ranks higher.
const taxonomyScoreBase = 2.0

// onThemeThreshold is the score at or above which an atom bands on-theme. It
// is calibrated so that a TITLE-ONLY match on a single-term theme clears it
// (0.6 * 1.0 = 0.6), because the live corpus's best candidate is untagged. A
// higher bar would band "Comparing Fractions: 2/3 vs 3/5" as merely related on
// a Fractions goal, which is the failure this design exists to avoid.
const onThemeThreshold = 0.5

// Signal weights. A topic_tag is a CURATED assertion about what an atom is
// about; a title word is incidental evidence. Tags therefore outrank titles,
// which is what makes tagging coverage improve ordering rather than merely
// preserve it.
const (
	weightTag   = 1.0
	weightTitle = 0.6
)

// topicStopwords carry no topic identity. Without them a NorthStarNote such as
// "I want to be good at this" would score every atom containing "at".
var topicStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "for": true, "from": true, "how": true,
	"in": true, "into": true, "is": true, "it": true, "its": true, "of": true,
	"on": true, "or": true, "the": true, "to": true, "up": true, "vs": true,
	"was": true, "what": true, "when": true, "why": true, "with": true,
	"i": true, "my": true, "me": true, "want": true, "learn": true, "learning": true,
	"understand": true, "get": true, "good": true, "better": true, "this": true,
	"that": true, "intro": true, "introduction": true, "basics": true, "basic": true,
}

// normaliseTopicTerm folds one raw token to its comparison form, or "" when the
// token carries no topic identity.
//
// The plural fold is deliberately crude (trailing "s", protecting "ss"/"is"/
// "us"). It is applied IDENTICALLY to both sides, so an imperfect fold such as
// "ceremonies" -> "ceremonie" still matches itself; the cost is readability of
// Terms(), never a missed match between a theme and an atom.
func normaliseTopicTerm(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return ""
	}
	// Digits alone are never a topic ("2/3 vs 3/5" contributes nothing).
	allDigits := true
	for _, r := range t {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return ""
	}
	if len(t) < 3 || topicStopwords[t] {
		return ""
	}
	if strings.HasSuffix(t, "s") &&
		!strings.HasSuffix(t, "ss") && !strings.HasSuffix(t, "is") && !strings.HasSuffix(t, "us") &&
		len(t) > 3 {
		t = t[:len(t)-1]
	}
	if topicStopwords[t] {
		return ""
	}
	return t
}

// splitTopicTerms tokenises free text on any non-alphanumeric boundary, then
// normalises each token. Titles, tags, goal names and note prose all go
// through this one path, which is what makes the two sides comparable.
func splitTopicTerms(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		return !isAlnum
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := normaliseTopicTerm(f); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ThemeSignature is the topic identity of a goal map, derived from the signals
// the graph already holds: the map theme (root concept title, or the goal's
// NorthStarNote) plus the focal concept the suggestion hangs off.
type ThemeSignature struct {
	terms    map[string]bool
	taxonomy map[string]bool
}

// NewThemeSignature derives topic identity from a map theme and the focal
// concept title. Either may be blank; both blank yields an EMPTY signature,
// which scores nothing and bands every atom AffinityUnscored.
func NewThemeSignature(mapTheme, focalTitle string) ThemeSignature {
	terms := make(map[string]bool)
	for _, src := range []string{mapTheme, focalTitle} {
		for _, t := range splitTopicTerms(src) {
			terms[t] = true
		}
	}
	return ThemeSignature{terms: terms}
}

// WithTaxonomy returns a COPY of the theme anchored to a set of taxonomy ids.
//
// The ids come from the atoms already attached to the focal concept, so the
// anchor is derived from the learner's own curation rather than imposed on
// their map. ADR-212 D1 makes a concept's TITLE learner-authored free text; it
// rules on naming, not on the atoms the learner chose to attach, so reading
// those attachments is sovereignty-respecting by construction.
//
// Non-ids are ignored: only canonical topic_node_ids anchor anything. Returns a
// copy because this package's value objects are never aliased.
func (t ThemeSignature) WithTaxonomy(ids []string) ThemeSignature {
	tax := make(map[string]bool, len(ids))
	for _, id := range ids {
		v := strings.TrimSpace(id)
		if isOpaqueTopicID(v) {
			tax[strings.ToLower(v)] = true
		}
	}
	terms := make(map[string]bool, len(t.terms))
	for k := range t.terms {
		terms[k] = true
	}
	return ThemeSignature{terms: terms, taxonomy: tax}
}

// HasTaxonomy reports whether this theme carries a taxonomy anchor.
func (t ThemeSignature) HasTaxonomy() bool { return len(t.taxonomy) > 0 }

// IsEmpty reports that no topic identity was resolvable. Callers MUST branch on
// this rather than on a zero score: it is the difference between "nothing is
// relevant" and "relevance is unknown here".
func (t ThemeSignature) IsEmpty() bool { return len(t.terms) == 0 && len(t.taxonomy) == 0 }

// Has reports whether a normalised term is part of this theme.
func (t ThemeSignature) Has(term string) bool { return t.terms[term] }

// Terms returns the theme's normalised terms, sorted for deterministic logs
// and test output.
func (t ThemeSignature) Terms() []string {
	out := make([]string, 0, len(t.terms))
	for k := range t.terms {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isOpaqueTopicID reports a canonical 8-4-4-4-12 UUID.
//
// THIS IS A LIVE SWITCH, NOT A FUTURE GUARD. Wire field 7 is OVERLOADED: it
// carries either freeform content tags or taxonomy node ids, and
// chora-creation's encoder picks between them (protomarshal.go:291-293 - explicit
// `topic_node_ids` wins when non-empty, otherwise the composer's "tags"). The
// decoder lands whichever arrived in the same place
// (protodecode.go:730 - `out["topic_tags"] = ids`).
//
// Today nothing sets the explicit key, so the field carries WORDS and this
// discriminator routes 100% of tags to the lexical tier. After CHO-2149 sets it,
// the field carries UUIDs and this routes 100% to the taxonomy tier. It handles
// a SWAP between two populations, not a mixture, and it is required either way:
// without it, post-CHO-2149 every id would enter the lexical signature as
// hex-fragment junk at full tag weight, matching nothing.
//
// Those ids are UNRESOLVABLE in this domain: topic_nodes.name lives in
// chora_creation, cross-DB reads are forbidden, and no topic_node projection
// lane exists into chora_consumption. Left unguarded a UUID would enter the
// signature as hex-fragment junk ("a000", "f1") holding the top tag weight
// while matching nothing, so the tag signal would look populated and
// contribute zero. Dropping it keeps the scorer honest and makes taxonomy
// integration an explicit future step rather than a silent no-op.
//
// Deliberately narrow: only a full canonical UUID. A hyphenated real tag such
// as "version-control" keeps its full weight.
func isOpaqueTopicID(s string) bool {
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

// AtomSignature is one candidate atom's topic identity, keyed by normalised
// term to the weight of the STRONGEST signal that produced it (a term carried
// by both a tag and the title weighs as a tag).
type AtomSignature struct {
	title    string
	terms    map[string]float64
	taxonomy map[string]bool
}

// NewAtomSignature derives topic identity from an atom's title and topic tags.
// The title is retained verbatim for rendering and ordering diagnostics.
func NewAtomSignature(title string, topicTags []string) AtomSignature {
	terms := make(map[string]float64)
	tax := make(map[string]bool)
	for _, tag := range topicTags {
		if v := strings.TrimSpace(tag); isOpaqueTopicID(v) {
			// ADR-245 D1.2 refines D1.1: an opaque id is still barred from the
			// LEXICAL signature (it carries no words), but it is retained as
			// taxonomy identity rather than discarded. Comparing id to id needs
			// no name, which is what makes the taxonomy usable in this domain.
			tax[strings.ToLower(v)] = true
			continue
		}
		for _, t := range splitTopicTerms(tag) {
			if terms[t] < weightTag {
				terms[t] = weightTag
			}
		}
	}
	for _, t := range splitTopicTerms(title) {
		if terms[t] < weightTitle {
			terms[t] = weightTitle
		}
	}
	return AtomSignature{title: title, terms: terms, taxonomy: tax}
}

// HasTaxonomy reports whether this atom carries any topic_node_id.
func (a AtomSignature) HasTaxonomy() bool { return len(a.taxonomy) > 0 }

// Title returns the atom's verbatim title.
func (a AtomSignature) Title() string { return a.title }

// taxonomyOverlap counts theme taxonomy ids the atom also carries. Zero when
// either side is unclassified, which is the common case today.
func (t ThemeSignature) taxonomyOverlap(atom AtomSignature) int {
	n := 0
	for id := range t.taxonomy {
		if atom.taxonomy[id] {
			n++
		}
	}
	return n
}

// Affinity scores one atom against one theme and bands the result.
//
// The score is `strongest * (0.75 + 0.25 * coverage)`, where `strongest` is the
// weight of the best-evidenced matching term and `coverage` is the fraction of
// theme terms the atom matched. Strongest-signal-first is deliberate: a
// multi-word theme such as "Introduction to Fractions" must not demote an atom
// that squarely matches "fractions" merely because it says nothing about
// "introduction". Coverage then breaks ties, so breadth still ranks higher.
//
// An EMPTY theme returns (0, AffinityUnscored) and compares nothing.
func Affinity(theme ThemeSignature, atom AtomSignature) (float64, string) {
	if theme.IsEmpty() {
		return 0, AffinityUnscored
	}
	// TIER 1 - taxonomy. An exact topic_node_id overlap is a curated tenant
	// classification, which is stronger evidence than any string similarity, so
	// it outranks the entire lexical range.
	//
	// PROMOTE, NEVER DEMOTE. A taxonomy MISS deliberately falls through to the
	// lexical tier instead of returning off-theme. Taxonomy coverage is partial
	// and will stay partial (CHO-2149 populates going forward, and an atom may
	// carry one id of several topics it touches), so treating a non-overlap as a
	// negative verdict would let incomplete curation demote atoms the evidence
	// says are relevant. That would hand this tier the power to reshape the
	// catalogue downward, which is the starvation shape ADR-245 forbids arriving
	// through a different door.
	if overlap := theme.taxonomyOverlap(atom); overlap > 0 {
		coverage := float64(overlap) / float64(len(theme.taxonomy))
		return taxonomyScoreBase + coverage, AffinityOnTheme
	}
	strongest, matched := 0.0, 0
	for term := range theme.terms {
		w, ok := atom.terms[term]
		if !ok {
			continue
		}
		matched++
		if w > strongest {
			strongest = w
		}
	}
	if matched == 0 || len(theme.terms) == 0 {
		// len(terms)==0 is the taxonomy-only theme that missed: no lexical
		// evidence exists to fall back ON, so this is honestly off-theme rather
		// than a division by zero.
		return 0, AffinityOffTheme
	}
	coverage := float64(matched) / float64(len(theme.terms))
	score := strongest * (0.75 + 0.25*coverage)
	if score >= onThemeThreshold {
		return score, AffinityOnTheme
	}
	return score, AffinityRelated
}

// RankedAtom is one atom with its affinity verdict attached.
type RankedAtom struct {
	// Index is this atom's position in the slice passed to RankByAffinity.
	// Callers rejoin to their own richer record through it, which is exact
	// where a title-keyed rejoin would collide on two atoms sharing a title.
	Index     int
	Signature AtomSignature
	Score     float64
	Band      string
	// Signal names the tier that decided this verdict (taxonomy | lexical |
	// none). Derivable from Score because the tiers occupy disjoint ranges, but
	// materialised here so callers log and test it without re-deriving a
	// threshold.
	Signal string
}

// signalFor names the tier a score came from. The tiers are disjoint by
// construction (lexical caps at maxLexicalScore, taxonomy starts above it), so
// the score alone is a sound discriminator.
func signalFor(score float64) string {
	switch {
	case score > maxLexicalScore:
		return SignalTaxonomy
	case score > 0:
		return SignalLexical
	default:
		return SignalNone
	}
}

// RankByAffinity orders atoms by descending affinity to the theme.
//
// IT NEVER DROPS AN ATOM. len(output) == len(input), for every theme, including
// one that matches nothing and one that is empty. This is the structural reason
// this design cannot reintroduce the empty-catalogue failure that the ADR-244
// D3 fail-closed gate turns into "every atom_ref dropped": ordering has no
// mechanism to remove.
//
// The sort is STABLE, so equally-scoring atoms keep their input order and two
// identical requests produce an identical catalogue. With an empty theme every
// atom scores 0 and bands AffinityUnscored, making this an order-preserving
// pass-through.
func RankByAffinity(theme ThemeSignature, atoms []AtomSignature) []RankedAtom {
	out := make([]RankedAtom, 0, len(atoms))
	for i, a := range atoms {
		score, band := Affinity(theme, a)
		out = append(out, RankedAtom{
			Index: i, Signature: a, Score: score, Band: band, Signal: signalFor(score),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
