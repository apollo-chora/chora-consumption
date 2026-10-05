// catalog.go — the platform capability catalogue (ADR-218 D4 rebase).
//
// `companion_skill_catalog` is platform-scoped reference data: one row per
// Skill a learner can equip into earned slots (skill_kind=active) or own as
// a designer-grammar passive (skill_kind=craft, R2-5 amendment). The v2
// columns (min_growth_stage / slot_cost / family / policy_class /
// output_sink / price_key / active …) replace the retired per-user
// xp_unlock_threshold axis (column retained, read path removed — ADR-218
// C2/D8). The seed source of truth is docs/FAMILIAR-SKILL-SPECS-2026-07-03.md;
// the Go mirror lives in the seedspec package. (The spec file keeps its
// pre-ADR-254 filename: the rename cut renamed identifiers and schema objects,
// not the historical design docs.)
package companion

import (
	"context"
	"strings"
)

// Port types (ADR-257 D2) — the closed vocabulary a Skill may consume from, and
// produce for, the next step. Deliberately a SUPERSET of the five SkillParamKind
// values in skill_params.go so the composer has ONE type system: the editor
// already renders a control per param kind and refuses to render an
// unrecognised one rather than guessing.
//
// RESERVED. Nothing reads these yet; chaining ships with the ADR-257 §5
// tool-allowlist enforcement gate. The asymmetry that matters when it does:
// every reference type reconciles against a deterministic pool at each boundary
// (the edgescout.Reconcile mechanism), so it may cross freely; PortText
// reconciles against nothing, so it crosses ONCE and is marked unverified
// (owner ruling R23).
const (
	PortNone          = "none"
	PortText          = "text"
	PortConceptRef    = "concept_ref"
	PortGrowthEdgeRef = "growth_edge_ref"
	PortAtomRef       = "atom_ref"
	PortQuestionRef   = "question_ref"
	PortSourceRef     = "source_ref"
)

// EffectivePort reconciles the pg column default ('none') with the Go zero
// value (""), so a row read before the ADR-257 columns were populated and a
// zero-value CatalogEntry answer the same way.
func EffectivePort(p string) string {
	if strings.TrimSpace(p) == "" {
		return PortNone
	}
	return p
}

// IsKnownPort reports closed-set membership. An empty string is NOT a port:
// callers normalise through EffectivePort first, so that an unset value is
// never silently treated as a valid declaration.
func IsKnownPort(p string) bool {
	switch p {
	case PortNone, PortText, PortConceptRef, PortGrowthEdgeRef,
		PortAtomRef, PortQuestionRef, PortSourceRef:
		return true
	}
	return false
}

// SkillKind splits the catalogue per the ADR-218 R2-5 amendment.
type SkillKind string

const (
	// SkillKindActive — occupies slots, binds tools, runs in chat/Rituals.
	SkillKindActive SkillKind = "active"
	// SkillKindCraft — slot-free passive expanding the Grimoire designer
	// grammar; owned = in effect, never unequippable.
	SkillKindCraft SkillKind = "craft"
)

// Catalogue families (ADR-218 D4 + spec pack §2/§3).
const (
	FamilyScholar   = "scholar"
	FamilySight     = "sight"
	FamilyWeaver    = "weaver"
	FamilySeeker    = "seeker"
	FamilyCompanion = "companion"
	FamilyHabit     = "habit"
	FamilyCraft     = "craft"
)

// Policy classes (ADR-218 D10 — the tenant allow/deny gate dimension).
const (
	PolicyStandard       = "standard"
	PolicyGenerative     = "generative"
	PolicyExternalEgress = "external_egress"
	PolicyAutonomy       = "autonomy"
)

// Closed output sinks (ADR-218 D9 + the Study-Calendar 6th-sink amendment).
// Craft Skills have no sink ("").
const (
	SinkChat             = "chat"
	SinkMemoryNote       = "memory_note"
	SinkSuggestionInbox  = "suggestion_inbox"
	SinkQuestionBank     = "question_bank"
	SinkNotification     = "notification"
	SinkCalendarArtifact = "calendar_artifact"
)

// CatalogEntry is one platform catalogue row (the Go projection of
// companion_skill_catalog v2).
type CatalogEntry struct {
	SkillKey       string
	Name           string
	SkillKind      SkillKind
	MinGrowthStage int
	SlotCost       int
	Family         string
	PolicyClass    string
	// OutputSink is the Skill's single primary sink (D9 closed list);
	// empty for craft Skills.
	OutputSink string
	// PriceKey is the mana action code (spec §7); empty for craft Skills
	// and equally for rows priced 0 the key still exists (equip is free,
	// use is metered by the gateway).
	PriceKey string
	// ToolHandlerRefs are the AI-Kernel tool registration names this Skill
	// binds (validated against the tool registry at release; several are
	// P2+ new tools — rows stay active=false until their eval gate passes).
	ToolHandlerRefs []string
	// Active is the ADR-174 release gate: seeded false; P2 flips the
	// Launch 7 after each Skill's eval suite passes. Equipping requires
	// active=true; Path unlocks accrue regardless (owned, dark).
	Active bool
	// Consumes / Produces are the ADR-257 D2 inter-step ports. RESERVED and
	// UNREAD: the columns exist so the projection can carry them, and both
	// default to PortNone, so nothing wires until the values are authored from
	// ToolHandlerRefs + OutputSink and the §5 gate closes. Normalise through
	// EffectivePort before comparing.
	Consumes string
	Produces string
}

// CatalogReader lists the platform catalogue (platform-scoped — no tenant
// axis). Implemented by the pg adapter over companion_skill_catalog.
type CatalogReader interface {
	// ListCatalogue returns every non-deleted catalogue row keyed by
	// skill_key (active AND inactive — callers gate on Active where it
	// matters).
	ListCatalogue(ctx context.Context) (map[string]CatalogEntry, error)
}
