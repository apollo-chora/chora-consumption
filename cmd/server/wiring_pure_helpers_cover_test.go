// wiring_pure_helpers_cover_test.go — coverage for the small pure / env-parsing
// boot helpers in cmd/server that need NO external resource (no DB / PubSub /
// HTTP client). These functions have real parsing/branching logic and are
// unit-testable in isolation; the constructing bodies of the wire*/bootstrap*
// functions are intentionally NOT covered here.
//
// Scope: durationFromEnv, toCompanionUnlockInput, diagnoseConceptMatchTieBand,
// weaknessEnvelopeEnabled, blobShredTTL, weaknessCompanionRAGEnabled, the
// DARK-path guards of maybeWithBlobShredHook / maybeWithCompanionRAGHook,
// doseComposerV2Enabled, personaEnvOr, and retentionReaderAdapter.RetentionAt.
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// --- durationFromEnv -------------------------------------------------------

func TestDurationFromEnv(t *testing.T) {
	tests := []struct {
		name string
		set  string
		def  time.Duration
		want time.Duration
	}{
		{name: "empty -> default", set: "", def: 5 * time.Minute, want: 5 * time.Minute},
		{name: "whitespace -> default", set: "   ", def: 5 * time.Minute, want: 5 * time.Minute},
		{name: "non-duration -> default", set: "abc", def: 5 * time.Minute, want: 5 * time.Minute},
		{name: "zero -> default", set: "0s", def: 5 * time.Minute, want: 5 * time.Minute},
		{name: "negative -> default", set: "-3s", def: 5 * time.Minute, want: 5 * time.Minute},
		{name: "valid nanoseconds", set: "500ms", def: 5 * time.Minute, want: 500 * time.Millisecond},
		{name: "valid composite", set: "1h30m", def: 5 * time.Minute, want: 90 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CHORA_TEST_DURATION_ENV", tt.set)
			if got := durationFromEnv("CHORA_TEST_DURATION_ENV", tt.def); got != tt.want {
				t.Fatalf("durationFromEnv(%q, %v) = %v, want %v", tt.set, tt.def, got, tt.want)
			}
		})
	}
}

// --- toCompanionUnlockInput -------------------------------------------------

func TestToCompanionUnlockInput(t *testing.T) {
	in := growth.UnlockThroughStageInput{
		TenantID:    "tenant-9",
		CompanionID: "fam-11",
		OwnerGCID:   "gcid-4",
		Species:     "egg.standard.v1",
		Stage:       3,
		Traceparent: "00-trace",
		Tracestate:  "vendor=1",
	}
	got := toCompanionUnlockInput(in)
	want := companion.UnlockInput{
		TenantID:    in.TenantID,
		CompanionID: in.CompanionID,
		OwnerGCID:   in.OwnerGCID,
		Species:     in.Species,
		Stage:       in.Stage,
		Traceparent: in.Traceparent,
		Tracestate:  in.Tracestate,
	}
	if got != want {
		t.Fatalf("toCompanionUnlockInput() = %#v, want %#v", got, want)
	}
}

// --- diagnoseConceptMatchTieBand -------------------------------------------

func TestDiagnoseConceptMatchTieBand(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want float64
	}{
		{name: "unset -> default", set: "", want: 0.03},
		{name: "blank -> default", set: "  ", want: 0.03},
		{name: "invalid -> default", set: "abc", want: 0.03},
		{name: "negative -> default", set: "-1", want: 0.03},
		{name: "over one -> default", set: "2", want: 0.03},
		{name: "zero is kill-switch", set: "0", want: 0},
		{name: "valid low", set: "0.05", want: 0.05},
		{name: "valid one", set: "1", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CHORA_DIAGNOSE_CONCEPT_MATCH_TIE_BAND", tt.set)
			if got := diagnoseConceptMatchTieBand(); got != tt.want {
				t.Fatalf("diagnoseConceptMatchTieBand() with %q = %v, want %v", tt.set, got, tt.want)
			}
		})
	}
}

// --- weaknessEnvelopeEnabled -----------------------------------------------

func TestWeaknessEnvelopeEnabled(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want bool
	}{
		{name: "unset -> false", set: "", want: false},
		{name: "true", set: "true", want: true},
		{name: "True case-insensitive", set: "True", want: true},
		{name: "trimmed", set: "  true  ", want: true},
		{name: "false", set: "false", want: false},
		{name: "garbage", set: "yes", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WEAKNESS_BLOB_ENVELOPE_ENABLED", tt.set)
			if got := weaknessEnvelopeEnabled(); got != tt.want {
				t.Fatalf("weaknessEnvelopeEnabled() with %q = %v, want %v", tt.set, got, tt.want)
			}
		})
	}
}

// --- blobShredTTL ----------------------------------------------------------

func TestBlobShredTTL(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want time.Duration
	}{
		{name: "unset -> default 24h", set: "", want: 24 * time.Hour},
		{name: "non-numeric -> default", set: "abc", want: 24 * time.Hour},
		{name: "zero -> default", set: "0", want: 24 * time.Hour},
		{name: "negative -> default", set: "-2", want: 24 * time.Hour},
		{name: "valid hours", set: "3", want: 3 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WEAKNESS_BLOB_SHRED_TTL_HOURS", tt.set)
			if got := blobShredTTL(); got != tt.want {
				t.Fatalf("blobShredTTL() with %q = %v, want %v", tt.set, got, tt.want)
			}
		})
	}
}

// --- weaknessCompanionRAGEnabled --------------------------------------------

func TestWeaknessCompanionRAGEnabled(t *testing.T) {
	t.Setenv("WEAKNESS_COMPANION_RAG_ENABLED", "")
	if weaknessCompanionRAGEnabled() {
		t.Error("unset -> expected false")
	}
	t.Setenv("WEAKNESS_COMPANION_RAG_ENABLED", " FALSE ")
	if weaknessCompanionRAGEnabled() {
		t.Error("false -> expected false")
	}
	t.Setenv("WEAKNESS_COMPANION_RAG_ENABLED", "TRUE")
	if !weaknessCompanionRAGEnabled() {
		t.Error("TRUE -> expected true")
	}
}

// --- maybeWithBlobShredHook DARK path --------------------------------------

// The DARK path must return the subscriber unchanged (no shred hook attached)
// when the envelope is disabled or the pool is nil — and never touch the
// storage constructors.
func TestMaybeWithBlobShredHook_DarkReturnsSubUnchanged(t *testing.T) {
	t.Setenv("WEAKNESS_BLOB_ENVELOPE_ENABLED", "")
	if got := maybeWithBlobShredHook(nil, nil); got != nil {
		t.Fatalf("maybeWithBlobShredHook disabled = %#v, want nil (unchanged)", got)
	}
	// Enabled flag but nil pool also short-circuits before the storage side.
	t.Setenv("WEAKNESS_BLOB_ENVELOPE_ENABLED", "true")
	if got := maybeWithBlobShredHook(nil, nil); got != nil {
		t.Fatalf("maybeWithBlobShredHook with nil pool = %#v, want nil (unchanged)", got)
	}
}

// --- maybeWithCompanionRAGHook DARK path ------------------------------------

func TestMaybeWithCompanionRAGHook_DarkReturnsSubUnchanged(t *testing.T) {
	t.Setenv("WEAKNESS_COMPANION_RAG_ENABLED", "")
	if got := maybeWithCompanionRAGHook(nil, nil, nil); got != nil {
		t.Fatalf("maybeWithCompanionRAGHook disabled = %#v, want nil (unchanged)", got)
	}
}

// --- doseComposerV2Enabled -------------------------------------------------

func TestDoseComposerV2Enabled(t *testing.T) {
	t.Setenv("DOSE_COMPOSER_V2_ENABLED", "")
	if doseComposerV2Enabled() {
		t.Error("unset -> expected false")
	}
	t.Setenv("DOSE_COMPOSER_V2_ENABLED", " on ")
	if doseComposerV2Enabled() {
		t.Error("bad value -> expected false")
	}
	t.Setenv("DOSE_COMPOSER_V2_ENABLED", "True")
	if !doseComposerV2Enabled() {
		t.Error("True -> expected true")
	}
}

// --- sharedBreedDistributionProvider ---------------------------------------

// The memoised provider is safe offline when no tenancy client is configured
// (it falls back to the static breed table, same as
// TestBootstrapBreedDistributionProvider_FallsBackToStaticWhenNoBaseURL). The
// package-level sync.Once means bootstrap runs on first call; asserting a
// non-nil memoised result exercises the Do closure + return.
func TestSharedBreedDistributionProvider_MemoisedAndNonNil(t *testing.T) {
	t.Setenv("CHORA_TENANCY_GRPC_BASE_URL", "")
	t.Setenv("CHORA_TENANCY_BASE_URL", "")
	first := sharedBreedDistributionProvider()
	if first == nil {
		t.Fatal("sharedBreedDistributionProvider() = nil, want non-nil")
	}
	second := sharedBreedDistributionProvider()
	if second == nil {
		t.Fatal("second sharedBreedDistributionProvider() = nil")
	}
}

// --- personaEnvOr ----------------------------------------------------------

func TestPersonaEnvOr(t *testing.T) {
	t.Run("set returns env", func(t *testing.T) {
		t.Setenv("CHORA_TEST_PERSONA_OR", "  custom  ")
		if got := personaEnvOr("CHORA_TEST_PERSONA_OR", "fallback"); got != "custom" {
			t.Fatalf("personaEnvOr = %q, want trimmed env", got)
		}
	})
	t.Run("blank falls back", func(t *testing.T) {
		t.Setenv("CHORA_TEST_PERSONA_OR", "   ")
		if got := personaEnvOr("CHORA_TEST_PERSONA_OR", "fallback"); got != "fallback" {
			t.Fatalf("personaEnvOr = %q, want fallback", got)
		}
	})
	t.Run("unset falls back", func(t *testing.T) {
		t.Setenv("CHORA_TEST_PERSONA_OR_EMPTY", "")
		if got := personaEnvOr("CHORA_TEST_PERSONA_OR_EMPTY", "fallback"); got != "fallback" {
			t.Fatalf("personaEnvOr = %q, want fallback", got)
		}
	})
}

// --- retentionReaderAdapter.RetentionAt ------------------------------------

func TestRetentionReaderAdapter_NilRepoReturnsFalse(t *testing.T) {
	var r retentionReaderAdapter // repo nil
	_, ok := r.RetentionAt(context.Background(), "t", "g", "topic", time.Now())
	if ok {
		t.Fatal("RetentionAt with nil repo = (_, true), want false")
	}
}

func TestRetentionReaderAdapter_NotFoundReturnsFalse(t *testing.T) {
	r := retentionReaderAdapter{repo: inmem.NewTopicRetentionRepo()}
	_, ok := r.RetentionAt(context.Background(), "t", "g", "missing", time.Now())
	if ok {
		t.Fatal("RetentionAt with absent score = (_, true), want false")
	}
}

func TestRetentionReaderAdapter_GetErrorReturnsFalse(t *testing.T) {
	r := retentionReaderAdapter{repo: errRetentionRepo{}}
	_, ok := r.RetentionAt(context.Background(), "t", "g", "topic", time.Now())
	if ok {
		t.Fatal("RetentionAt with Get error = (_, true), want false")
	}
}

func TestRetentionReaderAdapter_PresentReturnsScore(t *testing.T) {
	repo := inmem.NewTopicRetentionRepo()
	now := time.Now().UTC()
	score := &topic_retention.TopicScore{
		TenantID: "t",
		GCID:     "g",
		TopicID:  "topic",
		Strength: 7,
	}
	if err := repo.Save(context.Background(), score); err != nil {
		t.Fatalf("Save: %v", err)
	}
	r := retentionReaderAdapter{repo: repo}
	got, ok := r.RetentionAt(context.Background(), "t", "g", "topic", now)
	if !ok {
		t.Fatal("RetentionAt with present score = (_, false), want true")
	}
	if want := score.RetentionAt(now); got != want {
		t.Fatalf("RetentionAt = %v, want %v", got, want)
	}
}

// errRetentionRepo is a minimal topic_retention.Repository that always errors
// on Get, used to exercise the error arm of retentionReaderAdapter.RetentionAt.
type errRetentionRepo struct{}

func (errRetentionRepo) Save(context.Context, *topic_retention.TopicScore) error { return nil }
func (errRetentionRepo) Get(context.Context, string, string, string) (*topic_retention.TopicScore, error) {
	return nil, errors.New("boom")
}
func (errRetentionRepo) ListByLearner(context.Context, string, string, int) ([]*topic_retention.TopicScore, error) {
	return nil, nil
}
