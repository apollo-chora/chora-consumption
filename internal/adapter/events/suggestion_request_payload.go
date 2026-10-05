// suggestion_request_payload.go - the ONE payload contract for
// chora.consumption.concept_suggestion.requested.v1 (JSON topic; the proto
// contract defines the schema, the wire is JSON - mirrors weakness_doc.uploaded).
//
// Two producers share it (WS-C4, CHO-2083): the learner-facing generate door
// (request_source=learner_request) and the campaign free-on-win reveal consumer
// (request_source=campaign_free_reveal, ADR-227 D2). request_source is additive
// - the fog orchestrator uses it to select the mana action_code (the free
// reveal is exempt at the model-gateway metering seam, never by bypassing the
// gateway).
package events

import "time"

// TopicConceptSuggestionRequested is the JSON topic the concept-shaped fog
// orchestrator (ns ai-kernel) pulls.
const TopicConceptSuggestionRequested = "chora.consumption.concept_suggestion.requested.v1"

// Request-source markers for concept_suggestion.requested.v1.
const (
	// SuggestionSourceLearnerRequest - the learner pressed the door
	// (POST /v1/me/concept-graph/suggestions/generate); metered normally.
	SuggestionSourceLearnerRequest = "learner_request"
	// SuggestionSourceCampaignFreeReveal - the free-on-win reveal
	// (ADR-227 D2): bounded by a verified node win, mana-exempt.
	SuggestionSourceCampaignFreeReveal = "campaign_free_reveal"
	// SuggestionSourceAtomRefresh - the ADR-244 D5 standing refresh trigger
	// (atom.published / atom.updated): system-initiated, bounded by the
	// atom_refresh_ledger caps, mana-exempt via the fog orchestrator's
	// dedicated un-catalogued action_code (mirror constant in
	// chora-fog-orchestrator concept_suggestion/handler.py; deploy the fog
	// orchestrator BEFORE this producer or the gateway FallbackMap meters the
	// request as knowledge_graph_traverse at 100 mana).
	SuggestionSourceAtomRefresh = "atom_refresh"
)

// MaxExistingConceptsInSuggestionRequest bounds the event size (the fog only
// needs the learner's existing concept titles/ids to avoid re-proposing + to
// link edges).
const MaxExistingConceptsInSuggestionRequest = 50

// MaxAtomCatalogueInSuggestionRequest bounds the ADR-244 D2 catalogue. The
// event has to stay well inside Pub/Sub's 10MB cap, and an over-long catalogue
// also degrades the model's selection quality. Truncation is LOGGED by the
// caller, never silent: a silently-clipped catalogue would look like the
// Companion declining to propose atoms it was simply never shown.
const MaxAtomCatalogueInSuggestionRequest = 60

// CatalogueAtom is one ADR-243-entitled candidate the Companion may cite in
// atom_refs. Bare UUIDs are useless to a model, so each carries its metadata.
type CatalogueAtom struct {
	AtomID    string
	Title     string
	AtomType  string
	TopicTags []string
	// Relevance is the ADR-245 affinity band of this atom against the goal
	// theme: on_theme | related | off_theme, or "" when no topic identity was
	// resolvable and nothing was compared.
	//
	// It is ADVISORY. It orders the catalogue and tells the model which atoms
	// suit the goal, and it is deliberately NOT a gate: entitlement (ADR-244
	// D3) remains the only thing that may drop an atom_ref. An empty string
	// means "unknown", never "irrelevant", and the fog renders the pre-ADR-245
	// prompt when the whole catalogue is unbanded.
	Relevance string
}

// ExistingConcept is one (id, title) pair of the learner's current roster.
type ExistingConcept struct {
	ConceptID string
	Title     string
}

// WeaknessPayload is the ADR-247 Capability B (CHO-2328) learner-weakness
// projection carried on the request: the diagnosed weakness for the focal
// node's concept_key (F2). The producer sends it ONLY when a weakness was
// diagnosed; an absent weakness marshals to JSON null (see SuggestionRequestPayload),
// which is the fog's "nothing diagnosed" signal. Misconceptions/Evidence are the
// distilled learner_weakness.WeaknessContext lists (may be empty, never nil on the
// wire).
type WeaknessPayload struct {
	Descriptor     string
	Misconceptions []string
	Evidence       []string
}

// SuggestionRequestInput carries everything the payload needs; callers resolve
// repos, the builder only shapes the wire map.
type SuggestionRequestInput struct {
	TenantID       string
	LearnerGCID    string
	CompanionID    string
	MapTheme       string
	FocalConceptID string
	FocalTitle     string
	FocalAtomRefs  []string
	Existing       []ExistingConcept
	AtomCatalogue  []CatalogueAtom
	RequestedAt    time.Time
	RequestSource  string

	// ADR-247 Capability B (CHO-2328): the learner-aware fog inputs, all additive
	// and backward-compatible. The two producer sites (learner door + free-on-win
	// reveal) resolve them best-effort; an unresolved piece degrades to its empty
	// form and the fog keeps today's curiosity/theme prompt. Keys match the fog
	// consumer verbatim (concept_suggestion/handler.py).
	SubGoal   string           // the focal node's objective (F1)
	GoalTitle string           // the goal (root concept) title (F3)
	Ancestors []string         // ancestor concept titles, nearest-parent-first (F3)
	Weakness  *WeaknessPayload // the diagnosed weakness (F2); nil ⇒ JSON null on the wire
}

// SuggestionRequestPayload shapes the requested.v1 JSON body. Field names are
// the wire contract - keep in lockstep with the fog orchestrator's
// concept_suggestion handler (snake_case mirrors the proto).
func SuggestionRequestPayload(in SuggestionRequestInput) map[string]any {
	existing := make([]map[string]string, 0, len(in.Existing))
	for _, c := range in.Existing {
		existing = append(existing, map[string]string{"concept_id": c.ConceptID, "title": c.Title})
	}
	catalogue := make([]map[string]any, 0, len(in.AtomCatalogue))
	for _, a := range in.AtomCatalogue {
		tags := a.TopicTags
		if tags == nil {
			tags = []string{}
		}
		catalogue = append(catalogue, map[string]any{
			"atom_id":    a.AtomID,
			"title":      a.Title,
			"atom_type":  a.AtomType,
			"topic_tags": tags,
			// ADR-245: always present, "" when unscored. A stable key keeps the
			// fog's render branch keyed on the VALUE rather than on presence.
			"relevance": a.Relevance,
		})
	}
	// ADR-247 Capability B (CHO-2328): ancestors is ALWAYS a JSON array (fog reads
	// req.ancestors and iterates it), so an unresolved lineage marshals to [], not
	// null.
	ancestors := in.Ancestors
	if ancestors == nil {
		ancestors = []string{}
	}
	// weakness marshals to JSON null when nothing was diagnosed (a nil map[string]any
	// value marshals to null), which is the fog's "no diagnosis" signal. When present
	// the object carries the exact keys the fog reads; its two lists are never nil on
	// the wire so an empty diagnosis is [] rather than null.
	var weakness map[string]any
	if in.Weakness != nil {
		misconceptions := in.Weakness.Misconceptions
		if misconceptions == nil {
			misconceptions = []string{}
		}
		evidence := in.Weakness.Evidence
		if evidence == nil {
			evidence = []string{}
		}
		weakness = map[string]any{
			"descriptor":     in.Weakness.Descriptor,
			"misconceptions": misconceptions,
			"evidence":       evidence,
		}
	}
	return map[string]any{
		"atom_catalogue": catalogue,
		"tenant_id":      in.TenantID,
		"learner_gcid":   in.LearnerGCID,
		// PRE-RENAME WIRE KEY on purpose (ADR-254 D6/D9): the kennel's
		// kg_exploration fold lane decodes this JSON body BY NAME
		// (fold_lanes.py: body.get("familiar_id")) and the committed wire
		// fixture pins it. Renaming is a coordinated cut with WP-K.
		"familiar_id":       in.CompanionID,
		"map_theme":         in.MapTheme,
		"focal_concept_id":  in.FocalConceptID,
		"focal_title":       in.FocalTitle,
		"focal_atom_refs":   in.FocalAtomRefs,
		"existing_concepts": existing,
		"requested_at":      in.RequestedAt.UTC().Format(time.RFC3339),
		"request_source":    in.RequestSource,
		// ADR-247 Capability B (CHO-2328): keys match the fog consumer verbatim
		// (concept_suggestion/handler.py). All additive + backward-compatible.
		"sub_goal":   in.SubGoal,
		"goal_title": in.GoalTitle,
		"ancestors":  ancestors,
		"weakness":   weakness,
	}
}
