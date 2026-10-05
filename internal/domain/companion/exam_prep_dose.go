// BE-EP1 — Exam Prep Coach enrichment of the Daily Dose.
//
// When a learner has an active exam-prep goal (see ExamPrepGoal), the
// Companion daily dose swaps its curiosity slot for atoms recommended by
// the chora-ai-kernel-orchestrator exam-prep-coach (PE-02). This file owns
// the pure-domain port + composition logic; the HTTP adapter is wired in
// internal/adapter/clients/exam_prep_dose_port.go.
//
// Hexagonal note: domain code never imports the adapter. The Server (in
// internal/adapter/http) injects an ExamPrepCoachPort at construction; if
// the adapter is nil OR the orchestrator is unreachable, ComposeDailyDose
// falls back to the vanilla 2-1-2 mix so the learner never sees a 5xx
// because of an upstream blip.
//
// Trace context: the caller's context.Context is forwarded to the port so
// W3C traceparent + tracestate propagate across the service boundary
// (CLAUDE.md "OTLP everywhere"). The adapter is responsible for
// converting the ctx-bound traceparent into HTTP headers.
package companion

import (
	"context"
	"sort"
	"time"
)

// DoseReasonExamPrepDrill tags an atom picked by the exam-prep-coach
// weakness-drill recommendation, replacing the curiosity slot when the
// learner has an active exam-prep goal.
const DoseReasonExamPrepDrill DoseReason = "exam_prep_drill"

// ExamPrepTopicRetention mirrors the orchestrator's TopicRetention wire
// shape (score in [0.0, 1.0]). Topic labels match the syllabus / knowledge
// graph as published by chora-creation.
type ExamPrepTopicRetention struct {
	Topic string
	Score float64
}

// ExamPrepGoal is a per-learner active exam-prep goal stored in chora_consumption.
//
// MVP: in-memory; no database. Real persistence lands in M12 with a
// Pub/Sub-projected goal cache populated from chora_delivery's
// ExamRegistration aggregate.
type ExamPrepGoal struct {
	LearnerGCID string
	ExamID      string
	ExamDate    time.Time
	Retention   []ExamPrepTopicRetention
	// WeaknessThreshold defaults to 0.50 (per UX flow VS05.WeaknessDrill)
	// when zero. Callers may override.
	WeaknessThreshold float64
}

// defaultWeaknessThreshold matches the orchestrator coach's UX flow
// VS05.WeaknessDrill default ("retention < 0.50 ± 0.10 warning band").
const defaultWeaknessThreshold = 0.50

// ExamPrepCoachRequest is the per-call payload sent to the port. The
// adapter translates this into an HTTP POST to
// /agents/exam-prep-coach/weakness-drills.
type ExamPrepCoachRequest struct {
	Ctx         context.Context
	LearnerGCID string
	ExamID      string
	Retention   []ExamPrepTopicRetention
	Threshold   float64
	// AtomCount is the upper bound on drill atoms requested. Domain hint —
	// the coach may return fewer.
	AtomCount int
}

// ExamPrepCoachRecommendation is the response from the port. Topics are
// ordered weakest-first (per the coach's pure-domain contract).
type ExamPrepCoachRecommendation struct {
	// Topics is the ordered list of weakness-drill topics. The dose
	// composer maps these to AtomSeed.Topic.
	Topics []string
}

// ExamPrepCoachPort is the hexagonal port the dose composer calls. The
// production implementation is the chora-ai-kernel-orchestrator HTTP
// client (internal/adapter/clients/exam_prep_dose_port.go). Tests inject
// a fake.
type ExamPrepCoachPort interface {
	RecommendDose(ctx context.Context, req ExamPrepCoachRequest) (*ExamPrepCoachRecommendation, error)
}

// ComposeDailyDoseWithMana is the BE-USR-2 entry point that adds a
// pre-flight mana debit on the LLM-backed coach path. Behaviour:
//
//   - goal == nil   → vanilla ComposeDailyDose (no LLM, no mana). Quoter untouched.
//   - coach == nil  → vanilla ComposeDailyDose (no LLM). Quoter untouched.
//   - quoter == nil → fail-open. Coach is still called per ADR-142 §8 backstop
//     policy (medium-tier action). Caller is responsible for the
//     mana_metering_skipped audit log entry.
//   - quoter.DeductMana → ErrInsufficientMana → returns the typed error,
//     coach NOT called (the dose composer never re-prompts on insufficient
//     funds; UI shows the upsell instead).
//   - quoter.DeductMana → success → ComposeDailyDoseWithCoach runs as before.
//   - quoter.DeductMana → other error → returned untouched (caller decides
//     whether to bubble up or fail-open).
//
// idempotencyKey is mandatory; chora-identity dedups on (gcid, key). The
// caller (HTTP handler) typically composes it from the request_id so a
// learner refresh re-uses the same key and never double-debits.
//
// tenantID is forwarded to the quoter so chora-identity can bias subsidy
// FIFO selection when the learner holds allocations from multiple tenants.
func ComposeDailyDoseWithMana(
	ctx context.Context,
	in DailyDoseInput,
	coach ExamPrepCoachPort,
	goal *ExamPrepGoal,
	quoter ManaQuoter,
	idempotencyKey string,
	tenantID string,
) (DailyDose, error) {
	// Vanilla path: no goal OR no coach → no LLM call, no mana, no error.
	if goal == nil || coach == nil {
		return ComposeDailyDose(in), nil
	}

	// Fail-open backstop when chora-identity unreachable on a medium-tier
	// action (per ADR-142 §8). The HTTP layer is expected to emit a
	// `mana_metering_skipped=true` audit event in this branch.
	if quoter != nil {
		err := quoter.DeductMana(ctx, DeductManaInput{
			GCID:           goal.LearnerGCID,
			ActionCode:     ActionDailyDoseCoach,
			Units:          0, // resolve from action_code on server
			IdempotencyKey: idempotencyKey,
			TenantID:       tenantID,
		})
		if err != nil {
			// Surface ErrInsufficientMana untouched so callers can produce 402.
			// Other errors bubble up — the caller decides fail-open vs fail-closed.
			return DailyDose{}, err
		}
	}

	dose := ComposeDailyDoseWithCoach(ctx, in, coach, goal)
	return dose, nil
}

// ComposeDailyDoseWithCoach picks 5 atoms per the canonical 2-1-2 rule but
// replaces the curiosity slot with exam-prep-coach drill recommendations
// when learner has an active goal AND the coach returns successfully.
//
//   - goal == nil  → vanilla ComposeDailyDose (no coach call).
//   - coach == nil → vanilla ComposeDailyDose (config-absent ≠ runtime crash;
//     per feedback_no_inline_config the boot wiring is the only env-aware
//     layer, and the dose composer must not assume one is configured).
//   - coach error  → log + fall back to vanilla; learner never sees a 5xx
//     because of an upstream blip.
//   - coach OK     → curiosity slot is replaced by up to DoseCuriosityCount
//     atoms whose Topic matches the coach-recommended topics, tagged
//     DoseReasonExamPrepDrill.
//
// Determinism: when the coach returns the same topics for the same input,
// the same atoms are picked (sorted by AtomID).
func ComposeDailyDoseWithCoach(
	ctx context.Context,
	in DailyDoseInput,
	coach ExamPrepCoachPort,
	goal *ExamPrepGoal,
) DailyDose {
	// Vanilla path: no goal OR no coach configured.
	if goal == nil || coach == nil {
		return ComposeDailyDose(in)
	}

	threshold := goal.WeaknessThreshold
	if threshold <= 0 {
		threshold = defaultWeaknessThreshold
	}

	rec, err := coach.RecommendDose(ctx, ExamPrepCoachRequest{
		Ctx:         ctx,
		LearnerGCID: goal.LearnerGCID,
		ExamID:      goal.ExamID,
		Retention:   goal.Retention,
		Threshold:   threshold,
		AtomCount:   DoseCuriosityCount,
	})
	if err != nil || rec == nil || len(rec.Topics) == 0 {
		// Graceful fallback — never surface a 5xx to the learner.
		return ComposeDailyDose(in)
	}

	// Compose vanilla first, then swap up to DoseCuriosityCount curiosity
	// entries with drill picks. Keep Ebbinghaus + Fresh untouched.
	dose := ComposeDailyDose(in)
	dose = swapCuriosityForDrills(dose, in.Seeds, rec.Topics)
	return dose
}

// swapCuriosityForDrills returns a new dose where the curiosity slot has
// been replaced with up to DoseCuriosityCount weakness-drill atoms drawn
// from coach-recommended topics. The total of (drills + remaining
// curiosity) entries is capped at DoseCuriosityCount — when a learner has
// an active exam-prep goal, drills displace curiosity from the same
// budget; they do not stack on top of it.
//
// Selection priority for the drill slots:
//
//  1. In-dose curiosity entries whose topic matches a recTopic are
//     retagged DoseReasonExamPrepDrill (preserves deterministic atom_id
//     selection from the vanilla composer).
//  2. If unfilled, additional atoms from recTopics not already in the
//     dose are appended (weakest topic first; within a topic, atom_id
//     ascending).
//
// Ebbinghaus + Fresh entries are NEVER swapped — they're orthogonal
// concerns to the exam-prep enrichment. Excess curiosity entries beyond
// the DoseCuriosityCount budget are dropped to preserve the canonical
// 2-1-2 mix when a goal is active.
func swapCuriosityForDrills(
	dose DailyDose,
	seeds []AtomSeed,
	recTopics []string,
) DailyDose {
	if len(recTopics) == 0 {
		return dose
	}

	recTopicSet := make(map[string]bool, len(recTopics))
	for _, t := range recTopics {
		recTopicSet[t] = true
	}

	// Pull non-curiosity / non-fresh entries through unchanged + collect
	// curiosity AND fresh entries for re-allocation. Per S5.1 the new
	// daily-dose composer may place atoms from coach-recommended topics
	// in the Fresh top-up slots when SeenTopics + ActivePathTopics are
	// empty — the swap must retag those too so the goal-active dose
	// surfaces drills consistently.
	out := DailyDose{
		GeneratedAt: dose.GeneratedAt,
		Message:     dose.Message,
		Entries:     make([]DailyDoseEntry, 0, len(dose.Entries)),
	}
	curiosityEntries := make([]DailyDoseEntry, 0, DoseCuriosityCount)
	freshEntries := make([]DailyDoseEntry, 0, DoseTargetSize)
	for _, e := range dose.Entries {
		switch e.DoseReason {
		case DoseReasonCuriosity:
			curiosityEntries = append(curiosityEntries, e)
		case DoseReasonFresh:
			freshEntries = append(freshEntries, e)
		default:
			out.Entries = append(out.Entries, e)
		}
	}

	// Track atoms already kept (or staged) to prevent duplicates when
	// pulling new drill picks from the seed catalogue.
	used := make(map[string]bool, len(dose.Entries))
	for _, e := range out.Entries {
		used[e.AtomID] = true
	}

	// Step 1a — retag in-place curiosity entries whose topic matches a
	// recTopic. Track the leftover curiosity entries (no topic match) so
	// they can fill any unused slot up to the budget.
	drills := make([]DailyDoseEntry, 0, DoseCuriosityCount)
	leftover := make([]DailyDoseEntry, 0, len(curiosityEntries))
	for _, e := range curiosityEntries {
		if recTopicSet[e.Topic] && len(drills) < DoseCuriosityCount {
			drills = append(drills, DailyDoseEntry{
				AtomID:     e.AtomID,
				Topic:      e.Topic,
				Title:      e.Title,
				DoseReason: DoseReasonExamPrepDrill,
			})
			used[e.AtomID] = true
			continue
		}
		leftover = append(leftover, e)
	}

	// Step 1b — retag in-place fresh entries whose topic matches a
	// recTopic (S5.1: fresh top-up may have grabbed coach-relevant atoms
	// when no curiosity-eligible topics were available). These are the
	// most natural drill candidates: they're already in the dose, just
	// need a label change. Leftover fresh entries pass through unchanged.
	freshLeftover := make([]DailyDoseEntry, 0, len(freshEntries))
	for _, e := range freshEntries {
		if recTopicSet[e.Topic] && len(drills) < DoseCuriosityCount {
			drills = append(drills, DailyDoseEntry{
				AtomID:     e.AtomID,
				Topic:      e.Topic,
				Title:      e.Title,
				DoseReason: DoseReasonExamPrepDrill,
			})
			used[e.AtomID] = true
			continue
		}
		freshLeftover = append(freshLeftover, e)
	}

	// Step 2 — if drill slots remain, pull fresh atoms from recTopics not
	// yet in the dose (deterministic by atom_id ascending within topic).
	if len(drills) < DoseCuriosityCount {
		ordered := make([]AtomSeed, len(seeds))
		copy(ordered, seeds)
		sort.SliceStable(ordered, func(i, j int) bool {
			return ordered[i].AtomID < ordered[j].AtomID
		})
		byTopic := make(map[string][]AtomSeed, len(ordered))
		for _, s := range ordered {
			byTopic[s.Topic] = append(byTopic[s.Topic], s)
		}
		for _, topic := range recTopics {
			if len(drills) >= DoseCuriosityCount {
				break
			}
			for _, s := range byTopic[topic] {
				if used[s.AtomID] {
					continue
				}
				drills = append(drills, DailyDoseEntry{
					AtomID:     s.AtomID,
					Topic:      s.Topic,
					Title:      s.Title,
					DoseReason: DoseReasonExamPrepDrill,
				})
				used[s.AtomID] = true
				if len(drills) >= DoseCuriosityCount {
					break
				}
			}
		}
	}

	// Step 3 — append drills, then pad with leftover curiosity up to the
	// shared (drills + curiosity) ≤ DoseCuriosityCount budget. Excess
	// curiosity entries from the vanilla composer are dropped here so the
	// goal-active dose never exceeds the canonical 40/30/30 mix.
	for _, d := range drills {
		out.Entries = append(out.Entries, d)
	}
	remaining := DoseCuriosityCount - len(drills)
	for i := 0; i < remaining && i < len(leftover); i++ {
		out.Entries = append(out.Entries, leftover[i])
	}

	// Step 4 — restore leftover Fresh entries up to the global
	// DoseTargetSize budget so the goal-active dose still meets the
	// 5-card invariant when fresh slots are needed for filler.
	for _, e := range freshLeftover {
		if len(out.Entries) >= DoseTargetSize {
			break
		}
		out.Entries = append(out.Entries, e)
	}
	// Recompute the nudge — the swap can change the entry count and the
	// message must reflect the FINAL composition (no-debt directive).
	out.Message = DoseMessageFor(len(out.Entries))
	return out
}
