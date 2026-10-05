// Package goalscopedview composes the tier-1 goal-scoped Companion view
// (CHO-2118 / CHO-2116) from the repositories that own its parts, and hands the
// result to the pure domain assembler companionmind.AssembleGoalScopedView.
//
// It exists as its own adapter package (the kgmapread precedent) because it has
// TWO consumers that must never drift:
//
//   - the READ path (GET /v1/me/goals/{id}/knowledge) — serves this view as the
//     deterministic tier-1 block, and feeds the SAME assembled view to the
//     synthesis request, which is what makes the cached content_hash an honest
//     description of what the model actually saw;
//   - the COMPLETION path (goal_knowledge.synthesized.v1) — re-assembles the view
//     as it stands NOW and compares its ContentHash against the one echoed back,
//     refusing a synthesis whose inputs have moved.
//
// If those two produced views by different routes, the hashes would disagree and
// EVERY synthesis would be refused forever — a failure indistinguishable from
// "the model never answers". Hence one assembly, two entry points, and
// adapter/subscribers can depend on this without importing adapter/http.
//
// # The concept key-space (the subtle one)
//
// Goal.ConceptSet is trimmed + deduplicated by the Goal aggregate but NEVER
// slugified, while a Growth Edge's ConceptKey IS a normalised slug. The domain
// assembler intersects the two on exact trimmed strings, so BOTH sides are put
// into the slug space here via lw.NormalizeConceptKey — the same normalisation
// the goal-graduation subscriber and the A+ progress %-ring already apply
// (lw.CountMastered). Skip it and the intersection is empty: no shaky concepts,
// no progress, no signal, and the learner is told "no memory yet" forever.
//
// # Error policy (deliberate, and asymmetric)
//
// Weaknesses and memories are LOAD-BEARING: they are the ContentHash inputs, so
// a read failure FAILS LOUD. A fail-soft empty set here would hash as "a real
// but empty learner" — and if the read path degraded while the completion path
// did not (or vice versa), the two hashes would disagree and every synthesis
// would be refused. The Companion name and the goal title are ENRICHMENT: they
// are excluded from ContentHash by construction, so they degrade to blank
// (never fabricated) rather than blanking the learner's tab.
package goalscopedview

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// GoalReader loads one learner-owned Goal. goal.Repository satisfies it.
type GoalReader interface {
	GetByID(ctx context.Context, tenantID, learnerGCID, goalID string) (*goal.Goal, error)
}

// WeaknessLister is the learner's whole Growth-Edge set — ONE list that yields
// BOTH inputs: a "grown" edge is a mastered concept, an "active" edge is a shaky
// one. lw.Repository satisfies it (it is lw.GrownEdgeLister).
type WeaknessLister interface {
	ListAll(ctx context.Context, q lw.ListQuery) ([]lw.LearnerWeakness, error)
}

// MemoryReader is the Companion's recent episodic recalls.
type MemoryReader interface {
	RecentMemories(ctx context.Context, q companionmind.Query) ([]companionmind.EpisodicMemory, error)
}

// CompanionReader resolves the Companion's name. Enrichment (fail-soft).
type CompanionReader interface {
	Get(ctx context.Context, companionID string) (*companion.Instance, error)
}

// ConceptReader resolves the goal's root concept title. Enrichment (fail-soft).
type ConceptReader interface {
	GetByID(ctx context.Context, tenantID, learnerGCID, conceptID string) (*conceptgraph.ConceptNode, error)
}

// Assembler composes the tier-1 view. Goals/Weaknesses/Memories are mandatory;
// Companions/Concepts are enrichment and may be nil.
type Assembler struct {
	Goals      GoalReader
	Weaknesses WeaknessLister
	Memories   MemoryReader
	Companions CompanionReader
	Concepts   ConceptReader
}

// AssembleGoalScopedView loads the goal and assembles its view. This is the port
// the completion subscriber holds (subscribers.GoalScopedViewAssembler); the read
// path has already loaded the goal and calls AssembleForGoal directly rather than
// paying for a second read.
func (a *Assembler) AssembleGoalScopedView(ctx context.Context, tenantID, learnerGCID, companionID, goalID string) (companionmind.GoalScopedView, error) {
	if a == nil || a.Goals == nil {
		return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: goal reader not wired")
	}
	g, err := a.Goals.GetByID(ctx, tenantID, learnerGCID, goalID)
	if err != nil {
		return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: load goal %s: %w", goalID, err)
	}
	if g == nil {
		// An absent goal has no view. Returning an empty one would hash as a real
		// learner with nothing on this goal, and the completion path would compare
		// a synthesis against it.
		return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: goal %s not found", goalID)
	}
	return a.AssembleForGoal(ctx, tenantID, learnerGCID, companionID, g)
}

// AssembleForGoal composes the view for an already-loaded goal.
//
// companionID may be "" — no Companion is bound to this goal. There are then no
// per-Companion memories by definition, so neither the memory nor the instance
// repo is asked (their queries are scoped by that id; a blank one is not a
// question worth asking). The deterministic goal facts still assemble.
func (a *Assembler) AssembleForGoal(ctx context.Context, tenantID, learnerGCID, companionID string, g *goal.Goal) (companionmind.GoalScopedView, error) {
	if a == nil || a.Weaknesses == nil || a.Memories == nil {
		return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: assembler not wired (weaknesses + memories are mandatory)")
	}
	if g == nil {
		return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: nil goal")
	}
	companionID = strings.TrimSpace(companionID)

	// ONE list, BOTH inputs. Mastered edges are soft-archived to "grown", so they
	// only appear with IncludeGrown — without it, progress reads 0 forever.
	edges, err := a.Weaknesses.ListAll(ctx, lw.ListQuery{
		TenantID:     tenantID,
		LearnerGCID:  learnerGCID,
		IncludeGrown: true,
	})
	if err != nil {
		return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: list growth edges (learner=%s): %w", learnerGCID, err)
	}

	mastered := make([]string, 0, len(edges))
	signals := make([]companionmind.WeaknessSignal, 0, len(edges))
	for _, e := range edges {
		key := lw.NormalizeConceptKey(e.ConceptKey)
		if key == "" {
			continue
		}
		if e.IsGrown() {
			mastered = append(mastered, key)
		}
		// The domain assembler filters shaky = in-goal AND active; hand it every
		// edge with an honest Active flag and let it decide.
		signals = append(signals, companionmind.WeaknessSignal{
			ConceptKey:   key,
			ConceptLabel: e.ConceptLabel,
			Strength:     e.Strength,
			Active:       !e.IsGrown(),
		})
	}

	var memories []companionmind.EpisodicMemory
	if companionID != "" {
		memories, err = a.Memories.RecentMemories(ctx, companionmind.Query{
			TenantID:    tenantID,
			LearnerGCID: learnerGCID,
			CompanionID: companionID,
			MemoryLimit: companionmind.DefaultMemoryLimit,
		})
		if err != nil {
			return companionmind.GoalScopedView{}, fmt.Errorf("goalscopedview: recall memories (companion=%s): %w", companionID, err)
		}
	}

	return companionmind.AssembleGoalScopedView(companionmind.GoalScopedInput{
		GoalID:              g.GoalID,
		GoalTitle:           a.goalTitle(ctx, tenantID, learnerGCID, g),
		CompanionID:         companionID,
		CompanionName:       a.companionName(ctx, tenantID, learnerGCID, companionID),
		GoalConceptKeys:     normaliseKeys(g.ConceptSet),
		MasteredConceptKeys: mastered,
		Weaknesses:          signals,
		Memories:            memories,
	}), nil
}

// normaliseKeys puts the goal's concept set into the SAME slug space the Growth
// Edges live in. Without this the intersection is empty and the whole view reads
// as an empty learner (see the package header).
func normaliseKeys(in []string) []string {
	out := make([]string, 0, len(in))
	for _, k := range in {
		if n := lw.NormalizeConceptKey(k); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// companionName resolves the Companion's name. ENRICHMENT: a nil repo, a read
// error, or a Companion owned by anyone else yields "" — never a fabricated or
// another learner's name. Excluded from ContentHash, so degrading is hash-safe.
func (a *Assembler) companionName(ctx context.Context, tenantID, learnerGCID, companionID string) string {
	if a.Companions == nil || companionID == "" {
		return ""
	}
	inst, err := a.Companions.Get(ctx, companionID)
	if err != nil {
		log.Printf("consumption: goal_knowledge view could not resolve the Companion name (companion=%s): %v — serving the view unnamed", companionID, err)
		return ""
	}
	if inst == nil {
		return ""
	}
	if inst.OwnerGCID != learnerGCID || inst.TenantID != tenantID {
		// A goal bound to a Companion the learner does not own is an anomaly, not a
		// naming problem. Say so loudly; never render the name.
		log.Printf("consumption: goal_knowledge view REFUSED to name a foreign Companion (companion=%s owner=%s learner=%s)", companionID, inst.OwnerGCID, learnerGCID)
		return ""
	}
	return strings.TrimSpace(inst.Name)
}

// goalTitle names the goal for the learner (and for the synthesis prompt): the
// root concept's title, else the north-star note, else BLANK.
//
// Blank is a legitimate answer (ADR-207 — unknown is a state); a placeholder is
// not. This deliberately does NOT reuse companion_acquire's resolveMapTheme, which
// answers a different question (which THEME to derive a specialization from) and
// falls back to the literal "discovery" — a fine theme, but a lie as a title.
func (a *Assembler) goalTitle(ctx context.Context, tenantID, learnerGCID string, g *goal.Goal) string {
	if a.Concepts != nil && g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" {
		c, err := a.Concepts.GetByID(ctx, tenantID, learnerGCID, strings.TrimSpace(*g.RootConceptID))
		if err != nil {
			log.Printf("consumption: goal_knowledge view could not resolve the root concept title (goal=%s): %v — falling back", g.GoalID, err)
		} else if c != nil {
			if t := strings.TrimSpace(c.Title); t != "" {
				return t
			}
		}
	}
	return strings.TrimSpace(g.NorthStarNote)
}
