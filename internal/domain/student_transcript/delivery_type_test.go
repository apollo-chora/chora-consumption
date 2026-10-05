// delivery_type_test — StudentTranscript mode attribution (CHO-2224, §10.6
// capstone criterion 1).
//
// The transcript projects a graduate assessment and a short-course grade as the
// SAME kind ('assessment'). Without a mode they are indistinguishable, so §10.6
// ("one learner's unified transcript rolls up graded components from >=2 MODES")
// can only ever demonstrate 2 KINDS. DeliveryType is the axis that fixes that,
// and it is ORTHOGONAL to Kind: a certification issued off a graduate offering
// is both kind='certification' and mode='graduate'.
//
// ⚠ The central invariant here is that an unrecognised mode NORMALISES rather
// than rejects. delivery_type is minted in ANOTHER domain (chora_delivery), so
// consumption does not control its value set. Rejecting an unknown value would
// error the projection, NACK the event and DEAD-LETTER a learner's grade over a
// metadata nicety. Normalising to "" keeps the outcome and loses only the label.
package student_transcript_test

import (
	"testing"
	"time"

	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

func TestDeliveryType_Valid(t *testing.T) {
	valid := []st.DeliveryType{st.DeliveryTypeGraduate, st.DeliveryTypeShort, st.DeliveryTypeAsync}
	for _, dt := range valid {
		if !dt.Valid() {
			t.Errorf("DeliveryType(%q).Valid() = false; want true", dt)
		}
	}
}

// TestDeliveryType_InvalidValues — "" is NOT valid (it is the absence of a mode,
// not a mode), and neither is any unrecognised string. Both must normalise to ""
// at construction rather than be stored.
func TestDeliveryType_InvalidValues(t *testing.T) {
	invalid := []st.DeliveryType{"", "   ", "wat", "GRADUATE", "Graduate", "exam", "franchise"}
	for _, dt := range invalid {
		if dt.Valid() {
			t.Errorf("DeliveryType(%q).Valid() = true; want false", dt)
		}
	}
}

// TestDeliveryType_MirrorsTheContract pins the wire vocabulary. These three
// strings are chora_delivery's offerings.delivery_type values (ADR-190 D1); if
// delivery ever renames one, this test is the tripwire.
func TestDeliveryType_MirrorsTheContract(t *testing.T) {
	if string(st.DeliveryTypeGraduate) != "graduate" {
		t.Errorf("DeliveryTypeGraduate = %q; want %q", st.DeliveryTypeGraduate, "graduate")
	}
	if string(st.DeliveryTypeShort) != "short" {
		t.Errorf("DeliveryTypeShort = %q; want %q", st.DeliveryTypeShort, "short")
	}
	if string(st.DeliveryTypeAsync) != "async" {
		t.Errorf("DeliveryTypeAsync = %q; want %q", st.DeliveryTypeAsync, "async")
	}
}

func newGradedInput(dt st.DeliveryType) st.NewEntryInput {
	return st.NewEntryInput{
		TenantID:       "tenant-1",
		GCID:           "gcid-1",
		Kind:           st.KindAssessment,
		SourceRef:      "ass-1",
		DeliveryType:   dt,
		OccurredAt:     time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		IdempotencyKey: "submission:sub-1:graded",
		Now:            time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
}

func TestNew_PersistsGraduateDeliveryType(t *testing.T) {
	e, err := st.New(newGradedInput(st.DeliveryTypeGraduate))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.DeliveryType != st.DeliveryTypeGraduate {
		t.Errorf("DeliveryType = %q; want %q", e.DeliveryType, st.DeliveryTypeGraduate)
	}
}

// The other half of the ">=2 modes" proof: a constructor that pinned one value
// would satisfy the test above and still leave the capstone unprovable.
func TestNew_PersistsShortDeliveryType(t *testing.T) {
	e, err := st.New(newGradedInput(st.DeliveryTypeShort))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.DeliveryType != st.DeliveryTypeShort {
		t.Errorf("DeliveryType = %q; want %q", e.DeliveryType, st.DeliveryTypeShort)
	}
}

func TestNew_PersistsAsyncDeliveryType(t *testing.T) {
	e, err := st.New(newGradedInput(st.DeliveryTypeAsync))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.DeliveryType != st.DeliveryTypeAsync {
		t.Errorf("DeliveryType = %q; want %q", e.DeliveryType, st.DeliveryTypeAsync)
	}
}

// TestNew_AbsentDeliveryTypeIsUnattributed — a freestanding assessment (no
// offering, legal per chora_delivery migration 0033) carries no mode. The entry
// must still construct: the grade is the payload, the mode is metadata.
func TestNew_AbsentDeliveryTypeIsUnattributed(t *testing.T) {
	e, err := st.New(newGradedInput(""))
	if err != nil {
		t.Fatalf("New must not reject an entry with no delivery_type: %v", err)
	}
	if e.DeliveryType != "" {
		t.Errorf("DeliveryType = %q; want \"\" (unattributed)", e.DeliveryType)
	}
}

// TestNew_UnknownDeliveryTypeNormalisesNeverRejects — THE load-bearing
// invariant. chora_delivery owns this value set and offerings.delivery_type has
// no DB CHECK, so an unknown value is genuinely reachable (a 4th mode, a typo).
// Rejecting would NACK and dead-letter the learner's grade; storing the garbage
// would let a typo render as a "mode" in the UI. Normalise to "", keep the row.
func TestNew_UnknownDeliveryTypeNormalisesNeverRejects(t *testing.T) {
	for _, bogus := range []st.DeliveryType{"wat", "GRADUATE", "exam", "franchise", "   "} {
		e, err := st.New(newGradedInput(bogus))
		if err != nil {
			t.Fatalf("New(%q) must NOT reject: an unknown mode may never cost a learner their grade; got %v", bogus, err)
		}
		if e.DeliveryType != "" {
			t.Errorf("New(%q).DeliveryType = %q; want \"\" (normalised, not propagated)", bogus, e.DeliveryType)
		}
	}
}

// TestNew_DeliveryTypeIsOrthogonalToKind — a certification can also carry a
// mode. The two axes must not be collapsed into one column.
func TestNew_DeliveryTypeIsOrthogonalToKind(t *testing.T) {
	in := newGradedInput(st.DeliveryTypeGraduate)
	in.Kind = st.KindCertification
	in.SourceRef = "cert-1"
	in.IdempotencyKey = "cert:cert-1:issued"

	e, err := st.New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.Kind != st.KindCertification {
		t.Errorf("Kind = %q; want %q", e.Kind, st.KindCertification)
	}
	if e.DeliveryType != st.DeliveryTypeGraduate {
		t.Errorf("DeliveryType = %q; want %q (mode and kind are orthogonal axes)", e.DeliveryType, st.DeliveryTypeGraduate)
	}
}
