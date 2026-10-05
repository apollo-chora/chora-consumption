// attachable_atoms_handler.go - the ENROLMENT-entitled candidate source for the
// A+ concept attach picker.
//
//	GET /v1/me/concept-graph/attachable-atoms
//
// WHY THIS EXISTS. The picker previously offered exactly one candidate source:
// chora-creation's reuse search, whose entitlement clause is
// `mine UNION tenant-visible UNION granted`. A plain learner authors nothing and
// holds no grants, so that reduces to `reuse_visibility='tenant' AND published`.
// Measured live 2026-07-20: 2 of 367 live atoms qualify, so the fully-built
// attach path showed a learner two candidates, 46 of 48 concept nodes carried no
// atom_refs, and a goal-scoped daily dose had nothing to serve.
//
// The entitlement question was simply the wrong one. `reuse_visibility`
// (ADR-229) asks "may this learner REUSE this atom in shareable work?", an
// author-to-author consent question about attribution, grants and licensing.
// Attaching an atom to the learner's OWN private concept map is not that:
// chora_consumption is one learner's private traversal, the map is
// learner-sovereign (ADR-212), and nothing is republished or licensed. The right
// question is "does this learner already have access?", which ENROLMENT answers.
//
// ADR-229 is NOT overridden, only scoped: it still governs reuse. Bulk-flipping
// reuse_visibility to 'tenant' was rejected, because `private` is a deliberate
// default and mutation is author-only, so a bulk flip would override author
// consent on 124 atoms.
//
// This endpoint deliberately mirrors doseAtomUniverse (daily_dose_seeds.go): the
// same LearningPaths -> atom_index resolution, the same servability predicate,
// and the same FAIL-LOUD posture. A learner can therefore only ever attach an
// atom they could actually sit down and answer.
//
// Routed UNDER /v1/me/concept-graph on purpose: the gateway subtree proxy and
// the Istio authz wildcard already admit that prefix, so this needs no new edge
// wiring, no new mesh rule and no new RLS surface.
package http

import (
	"context"
	"errors"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// attachSourceEnrolled labels an atom offered because the learner is enrolled in
// a path containing it. The picker mixes TWO entitlement models (this one and
// chora-creation's tenant reuse), and unlabelled mixing is exactly the kind of
// thing that later reads as a bug, so every item states WHY it is available.
const attachSourceEnrolled = "enrolled"

// attachableAtomDTO is one candidate for attachment to a ConceptNode.
type attachableAtomDTO struct {
	AtomID   string   `json:"atomId"`
	Title    string   `json:"title"`
	AtomType string   `json:"atomType,omitempty"`
	Topic    string   `json:"topic,omitempty"`
	Topics   []string `json:"topics,omitempty"`
	CourseID string   `json:"courseId,omitempty"`
	// PublishedAt is the only real timestamp the atom_index projection carries.
	// The picker renders a relative date, so this is sent rather than letting the
	// FE invent one: for an enrolled atom "when it became available" is the
	// meaningful date anyway. RFC3339, empty when the projection has no publish
	// time (never fabricated).
	PublishedAt string `json:"publishedAt,omitempty"`
	// Source is the ENTITLEMENT REASON, not a category: it tells the learner why
	// this atom is theirs to attach. Never omitempty - an unlabelled item is the
	// failure mode this field exists to prevent.
	Source string `json:"source"`
}

type attachableAtomsResp struct {
	Items []attachableAtomDTO `json:"items"`
}

// publishedAtRFC3339 renders the projection's publish time, or "" when it has
// none. Never substitutes "now": a fabricated timestamp would read as a real
// fact in the picker's relative-date column.
func publishedAtRFC3339(a *atom_index.AtomIndex) string {
	if a == nil || a.PublishedAt.IsZero() {
		return ""
	}
	return a.PublishedAt.UTC().Format(time.RFC3339)
}

// handleMeAttachableAtoms - GET /v1/me/concept-graph/attachable-atoms.
//
// Returns every atom on the learner's non-deleted LearningPaths that is
// Playable() AND IsAnswerable(), deduplicated across paths, each labelled with
// its entitlement source. Empty is a legitimate answer (no enrolments yet); it
// NEVER falls back to a synthetic catalogue, which would offer atoms the learner
// cannot actually study.
func (s *ExtServer) handleMeAttachableAtoms(w http.ResponseWriter, r *http.Request) {
	if s.Paths == nil || s.AtomIndex == nil {
		extWriteError(w, http.StatusServiceUnavailable, "ATTACHABLE_ATOMS_UNAVAILABLE",
			"attachable-atom lookup not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := repoCtx(r, tenantID, gcid)

	paths, err := s.Paths.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		// FAIL-LOUD (mirrors doseAtomUniverse): silently serving a short list on
		// a repo error is indistinguishable, to the learner, from "you have no
		// atoms" - and it hides the outage that caused it.
		extWriteError(w, http.StatusInternalServerError, "ATTACHABLE_ATOMS_READ_FAILED", err.Error())
		return
	}

	items, err := s.resolveEntitledAtoms(ctx, paths)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "ATTACHABLE_ATOMS_READ_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, attachableAtomsResp{Items: items})
}

// resolveEntitledAtoms is the ONE ADR-243 entitlement resolution, shared by the
// picker (what the learner may attach BY HAND) and the ADR-244 D2 catalogue
// (what the Companion may PROPOSE). Keeping them on one function is the point:
// if they drifted, the Companion could propose an atom the learner is then
// refused when they accept it, which reads as a broken accept button rather
// than as an entitlement mismatch.
func (s *ExtServer) resolveEntitledAtoms(ctx context.Context, paths []*learning_path.LearningPath) ([]attachableAtomDTO, error) {
	items := make([]attachableAtomDTO, 0)
	seen := make(map[string]bool)
	for _, p := range paths {
		if p == nil {
			continue
		}
		for _, atomID := range p.AtomIDs {
			if atomID == "" || seen[atomID] {
				continue
			}
			seen[atomID] = true
			a, gerr := s.AtomIndex.Get(ctx, atomID)
			if errors.Is(gerr, atom_index.ErrNotFound) || (gerr == nil && a == nil) {
				continue // enrolled but unprojected: honest omission, not an error
			}
			if gerr != nil {
				// FAIL-LOUD (mirrors doseAtomUniverse): silently serving a short
				// list on a repo error is indistinguishable, to the learner,
				// from "you have no atoms" - and it hides the outage.
				return nil, gerr
			}
			// ADR-243 D4: the SAME predicate the dose serves at, not a second
			// copy of it, never offer an un-gradable dead end, because an atom
			// that cannot be answered produces no signal, so the concept it is
			// attached to could never light up and the learner would have no way
			// to tell why.
			if !a.Servable() {
				continue
			}
			items = append(items, attachableAtomDTO{
				AtomID:      a.AtomID,
				Title:       a.Title,
				AtomType:    a.AtomType,
				Topic:       a.PrimaryTopic(),
				Topics:      a.TopicTags,
				CourseID:    a.CourseID,
				PublishedAt: publishedAtRFC3339(a),
				Source:      attachSourceEnrolled,
			})
		}
	}
	return items, nil
}

// mapThemeSentinel mirrors resolveMapTheme's last-resort return
// (companion_acquire_handler.go). A goal with neither a root concept title nor a
// NorthStarNote yields this literal, and it is a SENTINEL, not a topic.
//
// Measured before this guard existed: theme "discovery" scored an unrelated atom
// titled "Discovery of penicillin" at 0.600 / on_theme. That is not starvation,
// it is worse in one respect - a confidently WRONG ordering that the prompt then
// tells the model to prefer.
//
// The cost is a false negative on a goal genuinely named "Discovery": it
// degrades to unscored, which is today's behaviour and the safe direction. The
// alternative (a confident wrong answer) is not. Handled here rather than in
// resolveMapTheme, which belongs to another owner.
const mapThemeSentinel = "discovery"

// scopedMapTheme drops the sentinel so it never becomes topic identity.
func scopedMapTheme(mapTheme string) string {
	if strings.EqualFold(strings.TrimSpace(mapTheme), mapThemeSentinel) {
		return ""
	}
	return mapTheme
}

// maxFocalAtomsForTaxonomy bounds the anchor resolution. A focal concept holds a
// handful of atoms at most (ADR-244 §5's census records 9 bindings across a
// whole tenant), so this is a defensive ceiling, not a working limit.
const maxFocalAtomsForTaxonomy = 24

// resolveFocalTaxonomy derives the THEME's taxonomy anchor from the atoms the
// learner already attached to the focal concept (ADR-245 D1.2).
//
// Sovereignty: ADR-212 D1 makes a concept's TITLE learner-authored free text and
// rules nothing about the atoms the learner CHOSE to attach, so reading those
// attachments derives an anchor from curation rather than imposing a tenant
// taxonomy on a private map.
//
// ⚠ DORMANT UNTIL CHO-2149, and the reason is precise. Wire field 7 IS
// populated today, but with freeform content tags rather than taxonomy ids:
// chora-creation falls back to the composer's "tags" when no explicit
// topic_node_ids is set (protomarshal.go:291-293), and nothing sets that key.
// So this returns no UUID-shaped anchor against live data and the lexical tier
// carries every request. That is the designed fallback, not a failure: an
// absent anchor never shortens anything.
//
// Non-fatal throughout: a read error yields no anchor and a loud log, never a
// failed suggestion.
func (s *ExtServer) resolveFocalTaxonomy(ctx context.Context, tenantID string, focalAtomRefs []string) []string {
	if s.AtomIndex == nil || len(focalAtomRefs) == 0 {
		return nil
	}
	refs := focalAtomRefs
	if len(refs) > maxFocalAtomsForTaxonomy {
		refs = refs[:maxFocalAtomsForTaxonomy]
	}
	seen := make(map[string]bool)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		a, err := s.AtomIndex.Get(ctx, ref)
		if errors.Is(err, atom_index.ErrNotFound) || (err == nil && a == nil) {
			continue // attached but unprojected: honest omission, never a phantom anchor
		}
		if err != nil {
			log.Printf("consumption: focal taxonomy anchor read FAILED atom=%s tenant=%s: %v", ref, tenantID, err)
			continue
		}
		for _, tag := range a.TopicTags {
			if tag != "" && !seen[tag] {
				seen[tag] = true
				out = append(out, tag)
			}
		}
	}
	return out
}

// resolveSuggestionCatalogue builds the ADR-244 D2 atom catalogue for a
// concept-suggestion request, from the SAME ADR-243 entitled set the manual
// picker offers (resolveEntitledAtoms), ordered by ADR-245 topic affinity to
// the goal theme.
//
// ADR-245: `mapTheme` (the goal-map root concept title, or its NorthStarNote)
// and `focalTitle` (the concept the suggestion hangs off) are the only topic
// identity the graph holds. Together they form the ThemeSignature every
// candidate is scored against.
//
// WHAT RANKING DOES AND DOES NOT DO. It REORDERS and BANDS. It never filters:
// the returned length is exactly what it was before ADR-245, so a theme that
// matches nothing still yields the whole entitled set. That is deliberate and
// load-bearing. A hard theme filter was measured on live data (2026-07-20) and
// left 0 of 16 entitled atoms on a Fractions goal, because 86% of the corpus
// carries no topic_tags; the resulting empty catalogue would hit the ADR-244 D3
// fail-closed gate and drop every atom_ref, reintroducing the bug ADR-244 fixed.
// Relevance therefore shapes ORDER and the model's INSTRUCTIONS, and entitlement
// remains the only gate.
//
// With no resolvable theme every atom bands "" and the order is untouched, so
// this degrades exactly to its pre-ADR-245 behaviour.
//
// Deliberately NON-fatal: an unwired repo or a read error yields an EMPTY
// catalogue and a loud log, never a failed suggestion. The fog then proposes
// concepts without citing atoms, which is the pre-ADR-244 behaviour and
// strictly better than refusing the learner any suggestions at all. It is
// logged because an always-empty catalogue is otherwise indistinguishable from
// a Companion that simply chose not to cite anything.
func (s *ExtServer) ResolveSuggestionCatalogue(ctx context.Context, tenantID, gcid, mapTheme, focalTitle string, focalAtomRefs []string) []events.CatalogueAtom {
	if s.Paths == nil || s.AtomIndex == nil {
		log.Printf("consumption: concept-suggestion catalogue SKIPPED (paths/atom_index not wired) tenant=%s", tenantID)
		return nil
	}
	paths, err := s.Paths.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		log.Printf("consumption: concept-suggestion catalogue read FAILED tenant=%s: %v", tenantID, err)
		return nil
	}
	items, err := s.resolveEntitledAtoms(ctx, paths)
	if err != nil {
		log.Printf("consumption: concept-suggestion catalogue resolve FAILED tenant=%s: %v", tenantID, err)
		return nil
	}
	// ADR-245: score every candidate against the goal theme, then order by
	// affinity. RankByAffinity returns one RankedAtom per input atom, so this
	// step cannot change how many candidates survive - only which of them
	// truncation below reaches.
	theme := conceptgraph.NewThemeSignature(scopedMapTheme(mapTheme), focalTitle).
		WithTaxonomy(s.resolveFocalTaxonomy(ctx, tenantID, focalAtomRefs))
	sigs := make([]conceptgraph.AtomSignature, 0, len(items))
	for _, it := range items {
		sigs = append(sigs, conceptgraph.NewAtomSignature(it.Title, it.Topics))
	}
	ranked := conceptgraph.RankByAffinity(theme, sigs)
	if len(ranked) != len(items) {
		// Unreachable by RankByAffinity's contract. Asserted anyway because a
		// silent shrink here is precisely the starvation this design forbids,
		// and it would otherwise surface as "the Companion stopped citing atoms".
		log.Printf("consumption: BUG concept-suggestion ranking changed catalogue size %d -> %d tenant=%s",
			len(items), len(ranked), tenantID)
	}
	out := make([]events.CatalogueAtom, 0, len(ranked))
	for _, r := range ranked {
		if r.Index < 0 || r.Index >= len(items) {
			continue // unreachable: RankByAffinity indexes the slice it was given
		}
		it := items[r.Index]
		band := r.Band
		if band == conceptgraph.AffinityUnscored {
			// No topic identity was resolvable, so nothing was compared.
			// Reported as "" rather than as a band, so the fog renders the
			// pre-ADR-245 prompt instead of claiming a verdict it does not have.
			band = ""
		}
		out = append(out, events.CatalogueAtom{
			AtomID: it.AtomID, Title: it.Title, AtomType: it.AtomType, TopicTags: it.Topics,
			Relevance: band,
		})
	}
	if len(out) > events.MaxAtomCatalogueInSuggestionRequest {
		// Loud, never silent: a clipped catalogue looks exactly like a Companion
		// declining to cite atoms it was never shown. Post-ADR-245 the tail this
		// drops is the LEAST relevant rather than whichever happened to come
		// last in path order.
		log.Printf("consumption: concept-suggestion catalogue TRUNCATED %d -> %d tenant=%s",
			len(out), events.MaxAtomCatalogueInSuggestionRequest, tenantID)
		out = out[:events.MaxAtomCatalogueInSuggestionRequest]
	}
	return out
}
