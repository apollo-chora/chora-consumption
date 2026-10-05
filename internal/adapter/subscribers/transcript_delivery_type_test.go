// transcript_delivery_type_test — the TranscriptSubscriber must carry the
// graded event's delivery_type onto the projected entry (CHO-2224, §10.6
// capstone criterion 1), and must NEVER let an unrecognised mode cost a learner
// their grade.
//
// Reuses fakeTranscriptRepo from transcript_subscriber_test.go, whose Upsert
// mirrors the pg adapter's ON CONFLICT (tenant_id, idempotency_key) convergence
// — so the re-grade case below exercises real upsert semantics, not a slice
// append.
package subscribers

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// captureLog redirects the stdlib logger for the duration of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func dtGradedPayload(deliveryType string) TranscriptSubmissionGradedPayload {
	return TranscriptSubmissionGradedPayload{
		SubmissionID: "sub-1",
		AssessmentID: "ass-1",
		LearnerGCID:  ttLearner,
		DeliveryType: deliveryType,
		OccurredAt:   time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
}

func TestTranscript_SubmissionGraded_PersistsGraduateMode(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	env := ttEnv("01970000-0000-7000-e000-000000000201")
	if err := sub.HandleSubmissionGraded(env, dtGradedPayload("graduate")); err != nil {
		t.Fatalf("HandleSubmissionGraded: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(repo.entries))
	}
	if repo.entries[0].DeliveryType != st.DeliveryTypeGraduate {
		t.Errorf("DeliveryType = %q; want %q", repo.entries[0].DeliveryType, st.DeliveryTypeGraduate)
	}
	if repo.entries[0].Kind != st.KindAssessment {
		t.Errorf("Kind = %q; want %q", repo.entries[0].Kind, st.KindAssessment)
	}
}

// TestTranscript_SubmissionGraded_TwoModesOneLearner — §10.6 criterion 1 in
// miniature, and the single assertion this whole story exists to make possible:
// ONE learner, TWO entries of the SAME kind, bearing DISTINCT modes. Before this
// change both rows read kind='assessment' with nothing to tell them apart, so
// the capstone could evidence 2 KINDS and never 2 MODES.
func TestTranscript_SubmissionGraded_TwoModesOneLearner(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	if err := sub.HandleSubmissionGraded(
		ttEnv("01970000-0000-7000-e000-000000000202"), dtGradedPayload("graduate")); err != nil {
		t.Fatalf("graduate: %v", err)
	}
	short := dtGradedPayload("short")
	short.SubmissionID = "sub-2"
	short.AssessmentID = "ass-2"
	if err := sub.HandleSubmissionGraded(
		ttEnv("01970000-0000-7000-e000-000000000203"), short); err != nil {
		t.Fatalf("short: %v", err)
	}

	if len(repo.entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(repo.entries))
	}
	if repo.entries[0].Kind != repo.entries[1].Kind {
		t.Fatalf("precondition: both entries must share kind='assessment'; got %q and %q",
			repo.entries[0].Kind, repo.entries[1].Kind)
	}
	if repo.entries[0].DeliveryType == repo.entries[1].DeliveryType {
		t.Fatalf("§10.6 criterion 1 UNMET: two same-kind entries carry the same mode %q; the transcript still cannot attribute >=2 modes",
			repo.entries[0].DeliveryType)
	}
	if repo.entries[0].DeliveryType != st.DeliveryTypeGraduate || repo.entries[1].DeliveryType != st.DeliveryTypeShort {
		t.Errorf("modes = %q, %q; want graduate, short",
			repo.entries[0].DeliveryType, repo.entries[1].DeliveryType)
	}
	if repo.entries[0].GCID != repo.entries[1].GCID {
		t.Errorf("entries must belong to ONE learner; got %q and %q", repo.entries[0].GCID, repo.entries[1].GCID)
	}
}

// TestTranscript_SubmissionGraded_NoMode — a freestanding assessment (no
// Offering, legal per chora_delivery migration 0033), or an event predating
// field 14. The entry must land, unattributed.
func TestTranscript_SubmissionGraded_NoMode(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	if err := sub.HandleSubmissionGraded(
		ttEnv("01970000-0000-7000-e000-000000000204"), dtGradedPayload("")); err != nil {
		t.Fatalf("an entry with no mode must still project: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(repo.entries))
	}
	if repo.entries[0].DeliveryType != "" {
		t.Errorf("DeliveryType = %q; want \"\"", repo.entries[0].DeliveryType)
	}
}

// TestTranscript_SubmissionGraded_UnknownModeKeepsTheGrade — THE invariant.
// chora_delivery owns this vocabulary and offerings.delivery_type has no DB
// CHECK, so an unknown value is genuinely reachable (a 4th mode ships, a typo
// lands). The projection must NOT error: an error NACKs, redelivers and
// dead-letters, and the learner's GRADE is gone over a label.
func TestTranscript_SubmissionGraded_UnknownModeKeepsTheGrade(t *testing.T) {
	for _, bogus := range []string{"wat", "GRADUATE", "franchise", "exam"} {
		repo := &fakeTranscriptRepo{}
		sub := NewTranscriptSubscriber(repo)

		err := sub.HandleSubmissionGraded(
			ttEnv("01970000-0000-7000-e000-000000000205"), dtGradedPayload(bogus))
		if err != nil {
			t.Fatalf("HandleSubmissionGraded(%q) must NOT error: an unrecognised mode may never dead-letter a grade; got %v", bogus, err)
		}
		if len(repo.entries) != 1 {
			t.Fatalf("%q: the grade must still land; want 1 entry, got %d", bogus, len(repo.entries))
		}
		if repo.entries[0].DeliveryType != "" {
			t.Errorf("%q: DeliveryType = %q; want \"\" (normalised, never propagated)", bogus, repo.entries[0].DeliveryType)
		}
		if repo.entries[0].SourceRef != "ass-1" {
			t.Errorf("%q: SourceRef = %q; want ass-1 (dropping the mode must not disturb the outcome)", bogus, repo.entries[0].SourceRef)
		}
	}
}

// TestTranscript_SubmissionGraded_UnknownModeWarnsLoudly — the fail-LOUD half of
// the invariant above, and the ONLY behaviour deliveryTypeOrWarn uniquely owns.
//
// ⚠ This test exists because mutation testing caught its absence: replacing
// deliveryTypeOrWarn with a bare cast killed nothing, since the domain's New()
// normalises too. The value-normalisation is defence in depth; the WARN is the
// unique contribution. Without this assertion an unrecognised mode would drop
// SILENTLY, which is precisely the class of defect this project keeps finding —
// a projection quietly degrading while every test stays green.
func TestTranscript_SubmissionGraded_UnknownModeWarnsLoudly(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	out := captureLog(t, func() {
		if err := sub.HandleSubmissionGraded(
			ttEnv("01970000-0000-7000-e000-000000000209"), dtGradedPayload("franchise")); err != nil {
			t.Fatalf("HandleSubmissionGraded: %v", err)
		}
	})
	if !strings.Contains(out, "UNRECOGNISED delivery_type") {
		t.Errorf("an unrecognised mode must be LOGGED LOUDLY, not dropped silently; log was: %q", out)
	}
	// The offending value must be named, or the log cannot be acted on.
	if !strings.Contains(out, "franchise") {
		t.Errorf("the warn must name the offending value; log was: %q", out)
	}
	// The submission must be identifiable, or the operator cannot find the row.
	if !strings.Contains(out, "sub-1") {
		t.Errorf("the warn must name the submission; log was: %q", out)
	}
}

// TestTranscript_SubmissionGraded_KnownModeIsQuiet — the warn must be a signal,
// not noise: a recognised mode logs NOTHING. A warn on every graded event would
// be scrolled past, which is how a real one gets missed.
func TestTranscript_SubmissionGraded_KnownModeIsQuiet(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	out := captureLog(t, func() {
		if err := sub.HandleSubmissionGraded(
			ttEnv("01970000-0000-7000-e000-00000000020a"), dtGradedPayload("graduate")); err != nil {
			t.Fatalf("HandleSubmissionGraded: %v", err)
		}
	})
	if strings.Contains(out, "UNRECOGNISED") {
		t.Errorf("a recognised mode must not warn; log was: %q", out)
	}
}

// TestTranscript_SubmissionGraded_AbsentModeIsQuiet — an unattributed entry is
// the NORMAL case for a freestanding assessment and for every pre-field-14
// event. Warning on it would drown the real signal above.
func TestTranscript_SubmissionGraded_AbsentModeIsQuiet(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	out := captureLog(t, func() {
		if err := sub.HandleSubmissionGraded(
			ttEnv("01970000-0000-7000-e000-00000000020b"), dtGradedPayload("")); err != nil {
			t.Fatalf("HandleSubmissionGraded: %v", err)
		}
	})
	if strings.Contains(out, "UNRECOGNISED") {
		t.Errorf("an absent mode is normal and must not warn; log was: %q", out)
	}
}

// TestTranscript_SubmissionGraded_RegradeUpdatesMode — a legitimate re-grade
// converges on the SAME row (deterministic natural key), and the mode updates
// with it rather than duplicating the entry.
func TestTranscript_SubmissionGraded_RegradeUpdatesMode(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	if err := sub.HandleSubmissionGraded(
		ttEnv("01970000-0000-7000-e000-000000000206"), dtGradedPayload("")); err != nil {
		t.Fatalf("first grade: %v", err)
	}
	// Re-grade of the SAME submission: fresh event id, mode now resolvable.
	if err := sub.HandleSubmissionGraded(
		ttEnv("01970000-0000-7000-e000-000000000207"), dtGradedPayload("short")); err != nil {
		t.Fatalf("re-grade: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("a re-grade must converge on ONE row, got %d", len(repo.entries))
	}
	if repo.entries[0].DeliveryType != st.DeliveryTypeShort {
		t.Errorf("DeliveryType = %q; want %q (the re-grade must update the mode in place)",
			repo.entries[0].DeliveryType, st.DeliveryTypeShort)
	}
}

// TestTranscript_CertIssued_HasNoModeYet — pins the KNOWN, DELIBERATE gap.
// chora.delivery.certification.issued.v1 carries course_id + cert_type only, and
// cert_type {COMPLETION|COMPETENCY|ACCREDITED|MICRO_CREDENTIAL} is a CREDENTIAL
// type, orthogonal to a delivery mode. §10.6 needs >=2 modes and the two
// assessment lanes supply them, so attributing the cert lane would cost a SECOND
// live schema revision for no additional criterion. If this test ever fails, the
// cert lane gained a mode and this comment is stale.
func TestTranscript_CertIssued_HasNoModeYet(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)

	err := sub.HandleCertIssued(ttEnv("01970000-0000-7000-e000-000000000208"), TranscriptCertIssuedPayload{
		CertID:      "cert-1",
		CourseID:    "course-1",
		LearnerGCID: ttLearner,
		IssuedAt:    time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("HandleCertIssued: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(repo.entries))
	}
	if repo.entries[0].Kind != st.KindCertification {
		t.Errorf("Kind = %q; want %q", repo.entries[0].Kind, st.KindCertification)
	}
	if repo.entries[0].DeliveryType != "" {
		t.Errorf("DeliveryType = %q; want \"\": the cert lane carries no mode on the wire yet", repo.entries[0].DeliveryType)
	}
}
