// goal_knowledge_payload.go — the TWO payload contracts for the CHO-2118
// goal-memory synthesis lane.
//
// ⚠ THERE IS NO PROTO. THESE STRUCTS *ARE* THE CONTRACT.
//
// The wire SHAPE mirrors concept_suggestion.requested.v1 (JSON body, envelope on
// the Pub/Sub attributes, snake_case fields) — but that lane has a proto in
// chora-contracts and this one does NOT. Neither goal_knowledge topic has an
// AsyncAPI file, a proto, or a Pub/Sub Schema Registry schema (audited
// 2026-07-14). Nothing machine-checks the Go↔Python boundary here.
//
// So the ONLY definitions of this wire are these structs and the fog
// orchestrator's publisher (services/chora-fog-orchestrator/.../goal_knowledge/
// publisher.py::build_synthesized_body). A field added on one side and forgotten
// on the other fails SILENTLY — the consumer just sees a zero value. Change them
// together, or author the proto.
//
// (The upside of being schema-registry-free: an additive field needs no schema
// revision, so it cannot dead-letter the topic the way an additive proto field
// silently killed submission.graded.v1 for ~3 weeks. That freedom is exactly why
// the absence of a machine-checked contract is easy to miss.)
//
// Flow (agent dispatch is Pub/Sub-orchestrated, ADR-155's locked rule — the
// caller never RPCs the reasoning engine directly):
//
//	consumption  --synthesis_requested.v1-->  fog orchestrator (ns ai-kernel)
//	                                            └─ chora-model-gateway Invoke
//	consumption  <--synthesized.v1------------  fog orchestrator
//
// Mana: the synthesis is EXEMPT at the metering seam — the orchestrator selects
// an un-catalogued action_code for this lane, exactly as campaign_free_reveal
// does (ADR-227 D2). Cloud Model Armor and the gateway chokepoint stay FULLY in
// force. Exempt from metering is not a bypass of the guardrail.
package events

import "time"

const (
	// TopicGoalKnowledgeSynthesisRequested is the JSON topic the fog orchestrator
	// pulls to synthesise one (companion, goal) reflection.
	TopicGoalKnowledgeSynthesisRequested = "chora.consumption.goal_knowledge.synthesis_requested.v1"
	// TopicGoalKnowledgeSynthesized is the completion topic consumption pulls to
	// write the cache row.
	TopicGoalKnowledgeSynthesized = "chora.consumption.goal_knowledge.synthesized.v1"
)

// GoalKnowledgeRequestSource marks why a synthesis was asked for. Carried for
// audit; the lane is mana-exempt regardless.
const GoalKnowledgeSourceLazyRegen = "goal_knowledge_lazy_regen"

// Bounds on what rides one synthesis request. The reflection is 2-3 sentences —
// it does not need the learner's whole history, and an unbounded event is a
// Pub/Sub size hazard.
const (
	MaxShakyConceptsInGoalKnowledgeRequest = 12
	MaxMemoriesInGoalKnowledgeRequest      = 20
)

// GoalKnowledgeShakyConcept is one concept the learner is shaky on WITHIN the
// goal (goal-subtree ∩ active weaknesses).
type GoalKnowledgeShakyConcept struct {
	ConceptKey   string
	ConceptLabel string
	Strength     float64
}

// GoalKnowledgeMemory is one episodic recall, projected at learner depth — the
// content and its recency only. No embeddings, no cosine distances, no model
// internals ever ride this wire (ADR-215 D5).
type GoalKnowledgeMemory struct {
	MemoryID  string
	Content   string
	CreatedAt time.Time
}

// GoalKnowledgeRequestInput is the assembled tier-1 view, ready for the wire.
// It is built FROM companionmind.GoalScopedView — the same structure the fail-soft
// renders — which is what makes ContentHash an honest description of what the
// model actually saw.
type GoalKnowledgeRequestInput struct {
	TenantID      string
	LearnerGCID   string
	CompanionID   string
	CompanionName string
	GoalID        string
	GoalTitle     string
	RootConceptID string

	ConceptsTotal    int
	ConceptsMastered int
	ShakyConcepts    []GoalKnowledgeShakyConcept
	Memories         []GoalKnowledgeMemory

	ContentHash   string
	PromptVersion string
	RequestedAt   time.Time
	RequestSource string
}

// GoalKnowledgeRequestPayload shapes synthesis_requested.v1. Slices are bounded
// here rather than at the caller so no call site can accidentally publish an
// oversize event.
func GoalKnowledgeRequestPayload(in GoalKnowledgeRequestInput) map[string]any {
	shaky := make([]map[string]any, 0, len(in.ShakyConcepts))
	for _, c := range in.ShakyConcepts {
		if len(shaky) >= MaxShakyConceptsInGoalKnowledgeRequest {
			break
		}
		shaky = append(shaky, map[string]any{
			"concept_key":   c.ConceptKey,
			"concept_label": c.ConceptLabel,
			"strength":      c.Strength,
		})
	}
	memories := make([]map[string]any, 0, len(in.Memories))
	for _, m := range in.Memories {
		if len(memories) >= MaxMemoriesInGoalKnowledgeRequest {
			break
		}
		memories = append(memories, map[string]any{
			"memory_id":  m.MemoryID,
			"content":    m.Content,
			"created_at": m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	source := in.RequestSource
	if source == "" {
		source = GoalKnowledgeSourceLazyRegen
	}
	return map[string]any{
		"tenant_id":    in.TenantID,
		"learner_gcid": in.LearnerGCID,
		// PRE-RENAME WIRE KEYS on purpose (ADR-254 D6/D9): the kennel's
		// companion_reflection fold lane decodes this JSON body BY NAME
		// (fold_lanes.py _REFLECTION_REQUIRED: familiar_id; familiar_name).
		// Renaming these keys is a coordinated cut with WP-K, not this one.
		"familiar_id":       in.CompanionID,
		"familiar_name":     in.CompanionName,
		"goal_id":           in.GoalID,
		"goal_title":        in.GoalTitle,
		"root_concept_id":   in.RootConceptID,
		"concepts_total":    in.ConceptsTotal,
		"concepts_mastered": in.ConceptsMastered,
		"shaky_concepts":    shaky,
		"memories":          memories,
		"content_hash":      in.ContentHash,
		"prompt_version":    in.PromptVersion,
		"requested_at":      in.RequestedAt.UTC().Format(time.RFC3339),
		"request_source":    source,
	}
}

// GoalKnowledgeSynthesizedPayload is the fog orchestrator's completion body,
// decoded at consumption's push handler.
//
// ContentHash and RootConceptID are ECHOED BACK deliberately. A synthesis takes
// seconds-to-tens-of-seconds; in that window the goal can re-root (ADR-214) or
// the inputs can move. Consumption re-checks both before writing, so a
// reflection about a world that no longer exists is refused rather than cached
// as current. Without the echo there is no way to tell.
type GoalKnowledgeSynthesizedPayload struct {
	TenantID    string `json:"tenant_id"`
	LearnerGCID string `json:"learner_gcid"`
	// PRE-RENAME WIRE KEY: the kennel's reflection lane EMITS familiar_id
	// (fold_lanes.py result body); decoded by name here. Coordinated cut later.
	CompanionID string `json:"familiar_id"`
	GoalID      string `json:"goal_id"`

	SynthesisText      string `json:"synthesis_text"`
	GeneratedByRunID   string `json:"generated_by_run_id"`
	GeneratedByModelID string `json:"generated_by_model_id"`
	PromptVersion      string `json:"prompt_version"`

	// NothingToSay is the model's HONEST DECLINE, carried back explicitly
	// (CHO-2180). The crew looked, found nothing about this goal worth saying, and
	// returned NO_MEMORY_YET; SynthesisText is empty and MUST stay empty — the
	// point is never to invent one.
	//
	// It exists because the crew used to express this by publishing NOTHING at
	// all, which was indistinguishable from a synthesis that crashed or was lost.
	// Consumption therefore never learned the run had CONCLUDED, left the row
	// claimed-but-never-generated, and served "reflecting…" forever while re-buying
	// the same doomed call on every read past the claim window. A decline is a
	// result, so it has to travel like one.
	//
	// Both goal_knowledge topics are schema-registry-FREE (verified 2026-07-14), so
	// this field is additive without a Pub/Sub schema revision. Do NOT assume that
	// of other topics — see the submission.graded.v1 dead-letter incident.
	NothingToSay bool `json:"nothing_to_say"`

	ContentHash   string `json:"content_hash"`
	RootConceptID string `json:"root_concept_id"`
	GeneratedAt   string `json:"generated_at"`
}
