// Package learner_weakness models the per-GCID "Growth Edge" aggregate — the
// learner's persistent map of where they are shaky. It is fed by two source
// classes (explicit uploads analysed by the ai-kernel weakness-analyser, and
// derived performance signals) which are source-tagged and folded together.
//
// LEARNER-FACING term is "Growth Edge" (curiosity-first); the internal /
// instructor / auditor term is "weakness" (3-audience explainability). The data
// is neutral — the framing is per-surface. See
// chora-contracts/proto/events/consumption/weakness.proto +
// openapi/consumption-growth-edges.yaml (both FROZEN, W0 2026-06-09).
//
// Granularity is FLAT (no parent/child hierarchy): concepts are de-duplicated by
// embedding cosine similarity (>= 0.9 cos ⟺ <= DedupCosineDistanceMax distance →
// fold into the matched row via Merge; else a sibling row) and rolled up at read
// time by category / tag / topic.
//
// Hexagonal purity: NO infra imports, NO time.Now() leaks — every clock-dependent
// call takes a `now`. Mirrors internal/domain/topic_retention. Per
// ddd-enforcement #5 closure is soft-delete; #7 new rows use UUIDv7; #9 gcid +
// topic_id are opaque UUIDs without FK constraints.
package learner_weakness

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Tuning constants.
const (
	// DedupCosineDistanceMax is the pgvector cosine DISTANCE (`<=>`) ceiling for
	// folding an incoming concept into an existing Growth Edge. Cosine
	// similarity >= 0.9 ⟺ cosine distance <= 0.1.
	DedupCosineDistanceMax = 0.1

	// MasteredStrengthThreshold: at or below this shakiness an edge is "grown"
	// (mastered) and soft-archived from active surfaces (kept for KG history).
	MasteredStrengthThreshold = 0.1

	// CeremonySeedStrength is the shakiness a ceremony-minted (unevidenced)
	// Growth Edge starts at (CHO-2043). It sits clearly above the mastered
	// floor so the edge reads active/shaky, but it is a declared-not-evidenced
	// seed — a real diagnosis (weakness-analyser) overwrites strength +
	// descriptor later. The `ceremony` source tag + empty descriptor + absent
	// cached drills keep it out of the daily-dose math until then.
	CeremonySeedStrength = 0.5

	// Evidence-array caps bound per-row growth (the descriptor is distilled
	// metadata, never the raw upload).
	MaxMisconceptions     = 12
	MaxSuggestedAngles    = 8
	MaxSampleWrong        = 8
	MaxPerItemCorrectness = 200
)

// Status is the Growth-Edge lifecycle state.
type Status string

const (
	// StatusActive — a live frontier surfaced to the learner.
	StatusActive Status = "active"
	// StatusGrown — mastered (strength ~0); soft-archived, kept for KG history.
	StatusGrown Status = "grown"
)

// Source tags which signal class contributed to an edge (merged, source-tagged).
type Source string

const (
	SourceExplicit  Source = "explicit"  // analyser-extracted from an upload
	SourceDerived   Source = "derived"   // topic_accuracy + Ebbinghaus retention
	SourceClassroom Source = "classroom" // live-quiz score_awarded.v1 wrong answers
	SourceCeremony  Source = "ceremony"  // learner-ticked remediate learning-edge (CHO-2043; unevidenced seed)
)

// ValidSource reports whether s is one of the three known source tags.
func ValidSource(s Source) bool {
	switch s {
	case SourceExplicit, SourceDerived, SourceClassroom, SourceCeremony:
		return true
	}
	return false
}

// SampleWrong is a redacted exemplar of a wrong answer for the gap.
type SampleWrong struct {
	Prompt   string `json:"prompt"`
	Chosen   string `json:"chosen,omitempty"`
	WhyWrong string `json:"why_wrong"`
}

// ItemCorrectness is marked-test provenance: which test item was right/wrong.
type ItemCorrectness struct {
	Item    string `json:"item"`
	Correct bool   `json:"correct"`
}

// Descriptor is the distilled, persisted metadata for a Growth Edge — NEVER the
// raw uploaded document. Stable keys mirror the frozen proto's descriptor_json.
type Descriptor struct {
	Summary            string            `json:"summary,omitempty"`
	Misconceptions     []string          `json:"misconceptions,omitempty"`
	SuggestedAngles    []string          `json:"suggested_angles,omitempty"`
	SampleWrong        []SampleWrong     `json:"sample_wrong,omitempty"`
	PerItemCorrectness []ItemCorrectness `json:"per_item_correctness,omitempty"`
}

// LearnerWeakness is the per-GCID Growth-Edge aggregate root.
type LearnerWeakness struct {
	ID                 string
	TenantID           string
	LearnerGCID        string
	ConceptKey         string    // normalised slug
	ConceptLabel       string    // Companion-facing natural phrase
	Embedding          []float32 // 768-d text-embedding-004
	TopicID            string    // "" = unresolved to a TopicNode
	TargetConceptID    string    // ADR-238: resolved on-map ConceptNode id; "" = UNMATCHED / pre-0098
	Category           string    // "" = none
	Tags               []string
	Strength           float64 // 0..1 shakiness (1 = very weak)
	Sources            []Source
	Descriptor         Descriptor
	CachedDrillAtomIDs []string // populated by W4
	Status             Status
	FirstSeenAt        time.Time
	LastEvidencedAt    time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// ErrInvalid is the sentinel returned by New for any validation failure.
var ErrInvalid = errors.New("learner_weakness: invalid")

// New constructs a fresh Growth Edge from one piece of evidence. It validates
// required fields, derives + normalises the concept_key (from concept_label when
// not supplied), clamps strength into [0,1], seeds the single source, caps the
// descriptor evidence arrays, and sets the mastered/active status.
func New(in UpsertInput) (*LearnerWeakness, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	if strings.TrimSpace(in.ConceptLabel) == "" {
		return nil, fmt.Errorf("%w: concept_label required", ErrInvalid)
	}
	if len(in.Embedding) == 0 {
		return nil, fmt.Errorf("%w: concept_embedding required", ErrInvalid)
	}
	if !ValidSource(in.Source) {
		return nil, fmt.Errorf("%w: source must be explicit|derived|classroom", ErrInvalid)
	}

	key := in.ConceptKey
	if strings.TrimSpace(key) == "" {
		key = in.ConceptLabel
	}
	key = NormalizeConceptKey(key)
	if key == "" {
		return nil, fmt.Errorf("%w: concept_label yields an empty concept_key", ErrInvalid)
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	emb := make([]float32, len(in.Embedding))
	copy(emb, in.Embedding)

	strength := ClampStrength(in.Strength)
	w := &LearnerWeakness{
		ID:              domain.NewUUIDv7(),
		TenantID:        in.TenantID,
		LearnerGCID:     in.LearnerGCID,
		ConceptKey:      key,
		ConceptLabel:    strings.TrimSpace(in.ConceptLabel),
		Embedding:       emb,
		TopicID:         strings.TrimSpace(in.TopicID),
		TargetConceptID: strings.TrimSpace(in.TargetConceptID),
		Category:        strings.TrimSpace(in.Category),
		Tags:            cleanTags(in.Tags),
		Strength:        strength,
		Sources:         []Source{in.Source},
		Descriptor:      capDescriptor(in.Descriptor),
		Status:          statusFor(strength),
		FirstSeenAt:     now,
		LastEvidencedAt: now,
		UpdatedAt:       now,
	}
	return w, nil
}

// Merge folds a new piece of evidence into an existing Growth Edge (the dedup
// path: the incoming embedding was within DedupCosineDistanceMax of this row).
// Strength is taken as the max (new evidence can only confirm/raise shakiness;
// recovery flows through the separate Ebbinghaus decay path). Sources + tags are
// unioned; an empty incoming category/topic_id never clobbers an existing value;
// the descriptor is appended (deduped + capped). first_seen_at is preserved;
// last_evidenced_at + updated_at advance to the merge time; status is recomputed
// (so a re-evidenced "grown" edge reactivates).
func (w *LearnerWeakness) Merge(in UpsertInput) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if s := ClampStrength(in.Strength); s > w.Strength {
		w.Strength = s
	}
	w.Sources = unionSources(w.Sources, in.Source)
	w.Tags = unionTags(w.Tags, cleanTags(in.Tags))
	if w.Category == "" {
		w.Category = strings.TrimSpace(in.Category)
	}
	if w.TopicID == "" {
		w.TopicID = strings.TrimSpace(in.TopicID)
	}
	if w.TargetConceptID == "" {
		w.TargetConceptID = strings.TrimSpace(in.TargetConceptID)
	}
	mergeDescriptor(&w.Descriptor, in.Descriptor)

	w.LastEvidencedAt = now
	w.UpdatedAt = now
	w.applyStatus()
}

// IsGrown reports whether the edge is mastered + soft-archived.
func (w *LearnerWeakness) IsGrown() bool { return w.Status == StatusGrown }

// SoftDelete dismisses the edge without removing it (hard delete forbidden per
// ddd-enforcement #5).
func (w *LearnerWeakness) SoftDelete(now time.Time) {
	w.DeletedAt = &now
	w.UpdatedAt = now
}

func (w *LearnerWeakness) applyStatus() { w.Status = statusFor(w.Strength) }

// statusFor maps a strength to its lifecycle status.
func statusFor(strength float64) Status {
	if strength <= MasteredStrengthThreshold {
		return StatusGrown
	}
	return StatusActive
}

// NormalizeConceptKey slugifies a free-form label into a stable concept_key:
// lower-cased, every run of non-alphanumeric collapsed to a single hyphen,
// leading/trailing hyphens trimmed. Returns "" for an all-punctuation input.
func NormalizeConceptKey(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// ClampStrength bounds a shakiness score into [0,1].
func ClampStrength(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// --- internal fold helpers ---

func cleanTags(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func unionTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, t := range a {
		seen[t] = true
	}
	for _, t := range b {
		if !seen[t] {
			seen[t] = true
			a = append(a, t)
		}
	}
	return a
}

func unionSources(a []Source, s Source) []Source {
	if !ValidSource(s) {
		return a
	}
	for _, x := range a {
		if x == s {
			return a
		}
	}
	return append(a, s)
}

func capDescriptor(d Descriptor) Descriptor {
	return Descriptor{
		Summary:            strings.TrimSpace(d.Summary),
		Misconceptions:     capStrings(d.Misconceptions, MaxMisconceptions),
		SuggestedAngles:    capStrings(d.SuggestedAngles, MaxSuggestedAngles),
		SampleWrong:        capSampleWrong(d.SampleWrong, MaxSampleWrong),
		PerItemCorrectness: capPerItem(d.PerItemCorrectness, MaxPerItemCorrectness),
	}
}

func mergeDescriptor(dst *Descriptor, src Descriptor) {
	if dst.Summary == "" {
		dst.Summary = strings.TrimSpace(src.Summary)
	}
	dst.Misconceptions = capStrings(append(dst.Misconceptions, src.Misconceptions...), MaxMisconceptions)
	dst.SuggestedAngles = capStrings(append(dst.SuggestedAngles, src.SuggestedAngles...), MaxSuggestedAngles)
	dst.SampleWrong = capSampleWrong(append(dst.SampleWrong, src.SampleWrong...), MaxSampleWrong)
	dst.PerItemCorrectness = capPerItem(append(dst.PerItemCorrectness, src.PerItemCorrectness...), MaxPerItemCorrectness)
}

func capStrings(in []string, max int) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, min(len(in), max))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

func capSampleWrong(in []SampleWrong, max int) []SampleWrong {
	seen := make(map[string]bool, len(in))
	out := make([]SampleWrong, 0, min(len(in), max))
	for _, s := range in {
		k := s.Prompt + "\x00" + s.WhyWrong
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

func capPerItem(in []ItemCorrectness, max int) []ItemCorrectness {
	seen := make(map[string]bool, len(in))
	out := make([]ItemCorrectness, 0, min(len(in), max))
	for _, it := range in {
		if seen[it.Item] {
			continue
		}
		seen[it.Item] = true
		out = append(out, it)
		if len(out) >= max {
			break
		}
	}
	return out
}
