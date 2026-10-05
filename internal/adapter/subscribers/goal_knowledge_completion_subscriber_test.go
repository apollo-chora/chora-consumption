package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// goal_knowledge_completion_subscriber_test.go — CHO-2118 sub-phase B, the
// completion half.
//
// The DRIFT GUARD is the whole point. A synthesis takes seconds-to-tens-of-
// seconds; in that window the goal can re-root or the inputs can move, so the
// completion echoes back root_concept_id + content_hash and we re-check BOTH
// against the world as it stands NOW. A reflection about a world that no longer
// exists is refused rather than cached as current.

const (
	gkcGoal    = "goal-c1"
	gkcRootOld = "concept-root-old"
	gkcRootNew = "concept-root-new"
)

// --- fakes ---

// gkcStore is the narrow write-side store (FindByGoal + Upsert).
type gkcStore struct {
	row       *fgk.GoalKnowledge
	upserts   int
	findErr   error
	upsertErr error
}

func (s *gkcStore) FindByGoal(_ context.Context, _, _, companionID, goalID string) (*fgk.GoalKnowledge, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	if s.row == nil || s.row.CompanionID != companionID || s.row.GoalID != goalID {
		return nil, nil
	}
	return s.row, nil
}

func (s *gkcStore) Upsert(_ context.Context, k *fgk.GoalKnowledge) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts++
	s.row = k
	return nil
}

// gkcAssembler re-assembles the tier-1 view as it stands NOW — the comparand for
// the content-hash drift guard.
type gkcAssembler struct {
	view companionmind.GoalScopedView
	err  error
}

func (a *gkcAssembler) AssembleGoalScopedView(context.Context, string, string, string, string) (companionmind.GoalScopedView, error) {
	if a.err != nil {
		return companionmind.GoalScopedView{}, a.err
	}
	return a.view, nil
}

// --- helpers ---

// gkcView is the tier-1 view the synthesis was generated from. Its ContentHash is
// what the request carried and the completion echoes back.
func gkcView(shaky ...string) companionmind.GoalScopedView {
	sc := make([]companionmind.ShakyConcept, 0, len(shaky))
	for i, k := range shaky {
		sc = append(sc, companionmind.ShakyConcept{ConceptKey: k, Strength: 0.9 - float64(i)*0.1})
	}
	return companionmind.GoalScopedView{
		GoalID: gkcGoal, CompanionID: gkiCompanion,
		ConceptsTotal: 4, ConceptsMastered: 1,
		ShakyConcepts: sc,
	}
}

// gkcRequestedRow is a row with a synthesis IN FLIGHT (the read path claimed it
// via MarkRequested before publishing) — the realistic state a completion lands on.
func gkcRequestedRow(t *testing.T, root string) *fgk.GoalKnowledge {
	t.Helper()
	var rootPtr *string
	if root != "" {
		rootPtr = &root
	}
	k, err := fgk.NewGoalKnowledge(gkiTenant, gkiLearner, gkiCompanion, gkcGoal, rootPtr)
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	k.MarkRequested(gkiNow)
	return k
}

func gkcGoalRepo(root string) *gkiGoals {
	g := &goal.Goal{
		GoalID: gkcGoal, TenantID: gkiTenant, LearnerGCID: gkiLearner,
		Status: goal.StatusActive, ConceptSet: []string{"fractions"},
	}
	if root != "" {
		r := root
		g.RootConceptID = &r
	}
	return &gkiGoals{goals: []*goal.Goal{g}}
}

func gkcPayload(hash, root string) events.GoalKnowledgeSynthesizedPayload {
	return events.GoalKnowledgeSynthesizedPayload{
		TenantID:           gkiTenant,
		LearnerGCID:        gkiLearner,
		CompanionID:        gkiCompanion,
		GoalID:             gkcGoal,
		SynthesisText:      "You have been steady on fractions, and long division is still shaky.",
		GeneratedByRunID:   "run-9",
		GeneratedByModelID: "gemini-2.5",
		PromptVersion:      "goal-knowledge-v1",
		ContentHash:        hash,
		RootConceptID:      root,
		GeneratedAt:        gkiNow.Format(time.RFC3339),
	}
}

// gkcEnv builds an envelope with an explicit idempotency_key — the completion
// dedupes on THAT (derived from the request), not on event_id.
func gkcEnv(eventID, idemKey string) events.Envelope {
	env := gkiEnv(eventID)
	env.IdempotencyKey = idemKey
	return env
}

func gkcSub(store *gkcStore, goals *gkiGoals, asm *gkcAssembler) *GoalKnowledgeCompletionSubscriber {
	return NewGoalKnowledgeCompletionSubscriber(store, goals, asm)
}

// --- happy path ---

func TestGoalKnowledgeCompletion_RecordsSynthesisWhenNothingDrifted(t *testing.T) {
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

	p := gkcPayload(view.ContentHash(), gkcRootOld)
	if err := sub.Handle(context.Background(), gkcEnv("evt-c1", "idem-c1"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", store.upserts)
	}
	got := store.row
	if got.Status != fgk.StatusFresh {
		t.Errorf("status = %q, want fresh", got.Status)
	}
	if got.SynthesisText != p.SynthesisText {
		t.Errorf("text not stored: %q", got.SynthesisText)
	}
	if got.GeneratedByRunID != "run-9" || got.GeneratedByModelID != "gemini-2.5" || got.PromptVersion != "goal-knowledge-v1" {
		t.Errorf("decision stamp not recorded: run=%q model=%q prompt=%q",
			got.GeneratedByRunID, got.GeneratedByModelID, got.PromptVersion)
	}
	if got.ContentHash != view.ContentHash() {
		t.Errorf("content hash = %q, want %q", got.ContentHash, view.ContentHash())
	}
	if got.RequestedAt != nil {
		t.Error("in-flight claim not released — a later read would think a synthesis is still pending")
	}
	if !got.IsCacheFresh() {
		t.Error("row is not a genuine cache hit after a successful synthesis")
	}
}

// A rootless goal is legitimate (the domain models the root as nullable), so ""
// must pass the root guard rather than being read as a mismatch.
func TestGoalKnowledgeCompletion_RootlessGoalIsAccepted(t *testing.T) {
	view := gkcView()
	store := &gkcStore{row: gkcRequestedRow(t, "")}
	sub := gkcSub(store, gkcGoalRepo(""), &gkcAssembler{view: view})

	if err := sub.Handle(context.Background(), gkcEnv("evt-c2", "idem-c2"), gkcPayload(view.ContentHash(), "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.upserts != 1 {
		t.Fatalf("a rootless goal was refused: upserts = %d, want 1", store.upserts)
	}
}

// REGRESSION (infinite-regen loop): RecordSynthesis does not advance the row's
// RootConceptID, but the read path gates serving on RootMatches(currentRoot). If
// the completion leaves the OLD root on the row, a re-rooted goal is re-requested
// on every single read — an LLM call per render, and the learner never sees a
// reflection. The completion must stamp the root it was generated against.
func TestGoalKnowledgeCompletion_AdvancesRowRootSoTheReadPathCanServeIt(t *testing.T) {
	view := gkcView("long-division")
	// The row was created back when the goal was rooted at OLD; the goal has since
	// re-rooted to NEW, and this synthesis was generated against NEW.
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootNew), &gkcAssembler{view: view})

	if err := sub.Handle(context.Background(), gkcEnv("evt-c3", "idem-c3"), gkcPayload(view.ContentHash(), gkcRootNew)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := store.row
	if got.RootConceptID == nil || *got.RootConceptID != gkcRootNew {
		t.Fatalf("row root = %v, want %q — the read path's RootMatches gate would refuse this row forever and re-request on every read",
			got.RootConceptID, gkcRootNew)
	}
	newRoot := gkcRootNew
	if !got.RootMatches(&newRoot) {
		t.Error("RootMatches(current) is false right after a successful synthesis — infinite regen loop")
	}
}

// --- the drift guards ---

// The goal re-rooted while the model was writing: the reflection is about a
// DIFFERENT subtree. Refuse the write; ack (nothing is broken, the work was
// superseded). The row stays stale/pending so the next read re-requests.
func TestGoalKnowledgeCompletion_RootDrift_RefusesTheWriteAndAcks(t *testing.T) {
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	// Synthesis was generated against OLD; the goal is NOW rooted at NEW.
	sub := gkcSub(store, gkcGoalRepo(gkcRootNew), &gkcAssembler{view: view})

	err := sub.Handle(context.Background(), gkcEnv("evt-c4", "idem-c4"), gkcPayload(view.ContentHash(), gkcRootOld))
	if err != nil {
		t.Fatalf("a superseded synthesis is not an error — it must ack: %v", err)
	}
	if store.upserts != 0 {
		t.Errorf("upserts = %d, want 0 — a reflection about an abandoned subtree was cached as current", store.upserts)
	}
	if store.row.Status == fgk.StatusFresh {
		t.Error("row marked fresh off a re-rooted synthesis")
	}
}

// The facts moved under the synthesis (a weakness grew, a memory landed): the
// echoed content_hash no longer describes the world. Refuse; ack.
func TestGoalKnowledgeCompletion_ContentHashDrift_RefusesTheWriteAndAcks(t *testing.T) {
	requestView := gkcView("long-division")
	currentView := gkcView("long-division", "place-value") // a weakness appeared mid-synthesis
	if requestView.ContentHash() == currentView.ContentHash() {
		t.Fatal("fixture broken: the two views must hash differently")
	}
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: currentView})

	err := sub.Handle(context.Background(), gkcEnv("evt-c5", "idem-c5"), gkcPayload(requestView.ContentHash(), gkcRootOld))
	if err != nil {
		t.Fatalf("a superseded synthesis is not an error — it must ack: %v", err)
	}
	if store.upserts != 0 {
		t.Errorf("upserts = %d, want 0 — a reflection about stale facts was cached as current", store.upserts)
	}
}

// The goal is gone (soft-deleted while the model was writing). There is nothing
// to reflect on — refuse, ack, and never resurrect a row for a deleted goal.
func TestGoalKnowledgeCompletion_GoalGone_RefusesAndAcks(t *testing.T) {
	view := gkcView()
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, &gkiGoals{}, &gkcAssembler{view: view}) // no goals at all

	if err := sub.Handle(context.Background(), gkcEnv("evt-c6", "idem-c6"), gkcPayload(view.ContentHash(), gkcRootOld)); err != nil {
		t.Fatalf("a vanished goal must ack, not error: %v", err)
	}
	if store.upserts != 0 {
		t.Errorf("upserts = %d, want 0", store.upserts)
	}
}

// No cache row (the goal or learner was soft-deleted): no-op ack. Creating one
// here would resurrect data for a row that was deliberately removed.
func TestGoalKnowledgeCompletion_NoCacheRow_IsNoOpAck(t *testing.T) {
	view := gkcView()
	store := &gkcStore{} // no row
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

	if err := sub.Handle(context.Background(), gkcEnv("evt-c7", "idem-c7"), gkcPayload(view.ContentHash(), gkcRootOld)); err != nil {
		t.Fatalf("a missing cache row must ack: %v", err)
	}
	if store.upserts != 0 {
		t.Errorf("upserts = %d, want 0 — a row was resurrected for a deleted goal", store.upserts)
	}
}

// --- the domain's own refusals (empty / oversize / unexplainable) ---

// The domain refuses a blank or oversize reflection (a blank one is a fabricated
// success; an oversize one means the model ignored the output contract). Let it
// refuse, log loudly, and ACK — redelivering identical bytes can never succeed,
// and the row stays claimable so the next read re-requests a fresh synthesis.
func TestGoalKnowledgeCompletion_DomainRefusalsAckWithoutCaching(t *testing.T) {
	view := gkcView()
	cases := map[string]func(events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload{
		"empty text": func(p events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload {
			p.SynthesisText = "   "
			return p
		},
		"oversize text": func(p events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload {
			p.SynthesisText = strings.Repeat("x", fgk.MaxSynthesisChars+1)
			return p
		},
		"missing decision stamp": func(p events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload {
			p.GeneratedByModelID = ""
			return p
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
			sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

			p := mutate(gkcPayload(view.ContentHash(), gkcRootOld))
			if err := sub.Handle(context.Background(), gkcEnv("evt-c8-"+name, "idem-c8-"+name), p); err != nil {
				t.Fatalf("a refused synthesis must ack (redelivery cannot fix it): %v", err)
			}
			if store.upserts != 0 {
				t.Errorf("upserts = %d, want 0 — a refused synthesis was cached anyway", store.upserts)
			}
		})
	}
}

// --- idempotency: dedupe on the REQUEST-derived key, not event_id ---

// The fog orchestrator derives the completion's idempotency_key from the REQUEST.
// A redelivered request therefore yields a second completion with a NEW event_id
// but the SAME key — which is the only way the inbox can recognise it as the same
// work. Dedupe must key on that, or the same synthesis is written twice.
func TestGoalKnowledgeCompletion_DedupesOnRequestKeyNotEventID(t *testing.T) {
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})
	p := gkcPayload(view.ContentHash(), gkcRootOld)

	if err := sub.Handle(context.Background(), gkcEnv("evt-first", "idem-shared"), p); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same work, re-published off a redelivered request: DIFFERENT event_id, SAME key.
	if err := sub.Handle(context.Background(), gkcEnv("evt-second", "idem-shared"), p); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if store.upserts != 1 {
		t.Errorf("upserts = %d, want 1 — the duplicate completion was not deduped on the request key", store.upserts)
	}
}

// A GENUINE regen (a new request ⇒ a new key) must NOT be deduped away.
func TestGoalKnowledgeCompletion_GenuineRegenIsNotDeduped(t *testing.T) {
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})
	p := gkcPayload(view.ContentHash(), gkcRootOld)

	if err := sub.Handle(context.Background(), gkcEnv("evt-1", "idem-req-1"), p); err != nil {
		t.Fatalf("first: %v", err)
	}
	// A later read re-requested; a new synthesis of the same (still-current) view.
	store.row.MarkRequested(gkiNow)
	if err := sub.Handle(context.Background(), gkcEnv("evt-2", "idem-req-2"), p); err != nil {
		t.Fatalf("regen: %v", err)
	}
	if store.upserts != 2 {
		t.Errorf("upserts = %d, want 2 — a genuine regen was swallowed as a duplicate", store.upserts)
	}
}

// --- fail-loud ---

func TestGoalKnowledgeCompletion_BadEnvelopeNACKs(t *testing.T) {
	view := gkcView()
	sub := gkcSub(&gkcStore{}, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})
	if err := sub.Handle(context.Background(), events.Envelope{}, gkcPayload(view.ContentHash(), gkcRootOld)); err == nil {
		t.Fatal("an incomplete envelope must NACK")
	}
}

func TestGoalKnowledgeCompletion_MissingIdentityNACKs(t *testing.T) {
	view := gkcView()
	sub := gkcSub(&gkcStore{}, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

	for name, mutate := range map[string]func(events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload{
		"no companion_id": func(p events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload {
			p.CompanionID = ""
			return p
		},
		"no goal_id": func(p events.GoalKnowledgeSynthesizedPayload) events.GoalKnowledgeSynthesizedPayload {
			p.GoalID = ""
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := mutate(gkcPayload(view.ContentHash(), gkcRootOld))
			if err := sub.Handle(context.Background(), gkcEnv("evt-x-"+name, "idem-x-"+name), p); err == nil {
				t.Error("expected a NACK")
			}
		})
	}
}

// Infrastructure failures NACK for redelivery — never a silent ack that loses a
// completed synthesis, and never a dedupe key burned before the write landed.
func TestGoalKnowledgeCompletion_RepoErrorsNACKAndDoNotBurnTheKey(t *testing.T) {
	view := gkcView("long-division")

	t.Run("assembler error", func(t *testing.T) {
		store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
		sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{err: errors.New("pg down")})
		if err := sub.Handle(context.Background(), gkcEnv("e1", "k1"), gkcPayload(view.ContentHash(), gkcRootOld)); err == nil {
			t.Fatal("cannot verify drift ⇒ must NACK, never write blind")
		}
	})

	t.Run("find error", func(t *testing.T) {
		store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld), findErr: errors.New("pg down")}
		sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})
		if err := sub.Handle(context.Background(), gkcEnv("e2", "k2"), gkcPayload(view.ContentHash(), gkcRootOld)); err == nil {
			t.Fatal("a cache read failure must NACK")
		}
	})

	t.Run("goal read error", func(t *testing.T) {
		store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
		sub := gkcSub(store, &gkiGoals{getErr: errors.New("pg down")}, &gkcAssembler{view: view})
		if err := sub.Handle(context.Background(), gkcEnv("e3", "k3"), gkcPayload(view.ContentHash(), gkcRootOld)); err == nil {
			t.Fatal("a goal read failure must NACK — the root guard cannot be skipped")
		}
	})

	t.Run("upsert error does not burn the dedupe key", func(t *testing.T) {
		store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld), upsertErr: errors.New("transient")}
		sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})
		env := gkcEnv("e4", "k4")
		p := gkcPayload(view.ContentHash(), gkcRootOld)

		if err := sub.Handle(context.Background(), env, p); err == nil {
			t.Fatal("an upsert failure must NACK")
		}
		// Pub/Sub redelivers the same completion; the recovered repo must apply it.
		store.upsertErr = nil
		if err := sub.Handle(context.Background(), env, p); err != nil {
			t.Fatalf("redelivery after recovery: %v", err)
		}
		if store.upserts != 1 {
			t.Errorf("upserts = %d, want 1 — the redelivery was ack-dropped as a duplicate and the synthesis was lost", store.upserts)
		}
	})
}

// generated_at feeds the TTL floor, so it must be a real time. A missing or
// malformed stamp falls back to the envelope's occurred_at — the event's own,
// honest time — rather than a fabricated one or a zero value (a zero GeneratedAt
// would make the row look infinitely old and regenerate on every read).
func TestGoalKnowledgeCompletion_MalformedGeneratedAtFallsBackToEnvelopeTime(t *testing.T) {
	view := gkcView("long-division")
	for name, raw := range map[string]string{
		"malformed": "not-a-timestamp",
		"empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
			sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

			p := gkcPayload(view.ContentHash(), gkcRootOld)
			p.GeneratedAt = raw
			env := gkcEnv("evt-ts-"+name, "idem-ts-"+name)
			if err := sub.Handle(context.Background(), env, p); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if store.upserts != 1 {
				t.Fatalf("upserts = %d, want 1", store.upserts)
			}
			got := store.row.GeneratedAt
			if got == nil {
				t.Fatal("GeneratedAt nil — the row would look never-generated")
			}
			if !got.Equal(env.OccurredAt.UTC()) {
				t.Errorf("GeneratedAt = %v, want the envelope occurred_at %v", *got, env.OccurredAt.UTC())
			}
		})
	}
}

func TestNewGoalKnowledgeCompletionSubscriber_PanicsOnNilDeps(t *testing.T) {
	view := gkcView()
	for name, build := range map[string]func(){
		"nil store":     func() { NewGoalKnowledgeCompletionSubscriber(nil, gkcGoalRepo(""), &gkcAssembler{view: view}) },
		"nil goals":     func() { NewGoalKnowledgeCompletionSubscriber(&gkcStore{}, nil, &gkcAssembler{view: view}) },
		"nil assembler": func() { NewGoalKnowledgeCompletionSubscriber(&gkcStore{}, gkcGoalRepo(""), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("a nil dependency is a wiring bug — panic at construction, never fail open into an unguarded write")
				}
			}()
			build()
		})
	}
}

// ── CHO-2180: the Companion's honest DECLINE must be recorded as a conclusion ──
//
// The crew is allowed to say "I have nothing true to say about this goal yet"
// (NO_MEMORY_YET) — that instinct is right, and it must never be talked into
// inventing a memory. What was WRONG is that it expressed the decline by
// publishing nothing at all, which is indistinguishable here from a synthesis
// that crashed. The row stayed claimed-but-never-generated, so the read path
// served "reflecting…" forever and re-bought the same doomed LLM call on every
// read past the claim window.

// gkcDecline is the crew's decline: nothing_to_say, no text, full ADR-197 stamp.
func gkcDecline(hash, root string) events.GoalKnowledgeSynthesizedPayload {
	p := gkcPayload(hash, root)
	p.SynthesisText = ""
	p.NothingToSay = true
	return p
}

func TestGoalKnowledgeCompletion_DeclineIsRecordedAsATerminalConclusion(t *testing.T) {
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

	if err := sub.Handle(context.Background(), gkcEnv("evt-d1", "idem-d1"), gkcDecline(view.ContentHash(), gkcRootOld)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if store.upserts != 1 {
		t.Fatalf("the decline was not persisted: upserts = %d, want 1 — the row would stay stranded (CHO-2180)", store.upserts)
	}
	got := store.row
	if !got.ConcludedSilent() {
		t.Fatal("the decline did not conclude the row — the read path will keep saying 'reflecting…' forever")
	}
	if got.Status != fgk.StatusSilent {
		t.Errorf("status = %q, want %q", got.Status, fgk.StatusSilent)
	}
	if got.GeneratedAt == nil {
		t.Error("GeneratedAt not stamped — the row still reads as never-generated and is retried unconditionally")
	}
	if got.RequestedAt != nil {
		t.Error("in-flight claim not released — the synthesis is over, it just said nothing")
	}
	if got.SynthesisText != "" {
		t.Errorf("invented prose on a decline: %q", got.SynthesisText)
	}
	// Silence is a MODEL DECISION and carries the same ADR-197 stamp as prose.
	if got.GeneratedByRunID != "run-9" || got.GeneratedByModelID != "gemini-2.5" || got.PromptVersion != "goal-knowledge-v1" {
		t.Errorf("decision stamp not recorded on the decline: run=%q model=%q prompt=%q",
			got.GeneratedByRunID, got.GeneratedByModelID, got.PromptVersion)
	}
	// And the row's root advances, exactly as it does for a real synthesis —
	// otherwise RootMatches stays false and the read path re-requests every render.
	if got.RootConceptID == nil || *got.RootConceptID != gkcRootOld {
		t.Error("the decline did not advance the row root — the read path would re-request on every render")
	}
}

func TestGoalKnowledgeCompletion_DeclineAboutAReRootedGoalIsStillRefused(t *testing.T) {
	// The drift guards apply to a decline exactly as they do to prose: silence
	// about a world that no longer exists is not worth caching either.
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo("root-NEW"), &gkcAssembler{view: view})

	if err := sub.Handle(context.Background(), gkcEnv("evt-d2", "idem-d2"), gkcDecline(view.ContentHash(), gkcRootOld)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.upserts != 0 {
		t.Fatalf("cached a decline about a subtree the learner has abandoned: upserts = %d, want 0", store.upserts)
	}
}

func TestGoalKnowledgeCompletion_DeclineWithInputsMovedIsStillRefused(t *testing.T) {
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

	if err := sub.Handle(context.Background(), gkcEnv("evt-d3", "idem-d3"), gkcDecline("hash-from-a-world-that-moved", gkcRootOld)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.upserts != 0 {
		t.Fatalf("cached a decline the inputs had already outrun: upserts = %d, want 0", store.upserts)
	}
}

func TestGoalKnowledgeCompletion_UnattributableDeclineIsRefusedNotCached(t *testing.T) {
	// A verdict that cannot name the model that reached it is unexplainable
	// (ADR-197) — true for silence as much as for prose.
	view := gkcView("long-division")
	store := &gkcStore{row: gkcRequestedRow(t, gkcRootOld)}
	sub := gkcSub(store, gkcGoalRepo(gkcRootOld), &gkcAssembler{view: view})

	p := gkcDecline(view.ContentHash(), gkcRootOld)
	p.GeneratedByModelID = ""

	if err := sub.Handle(context.Background(), gkcEnv("evt-d4", "idem-d4"), p); err != nil {
		t.Fatalf("Handle should ACK an unsatisfiable payload rather than burn retries: %v", err)
	}
	if store.upserts != 0 {
		t.Fatalf("cached an unattributable decline: upserts = %d, want 0", store.upserts)
	}
}
