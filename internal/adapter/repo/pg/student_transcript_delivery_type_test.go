// student_transcript_delivery_type_test.go — SQL-shape verification for the
// delivery_type column (CHO-2224, §10.6 capstone criterion 1).
//
// A column the write path never persists, or the read path never selects, is a
// column that does not exist as far as the transcript is concerned. These assert
// the mode survives BOTH directions, including through a re-grade (the ON
// CONFLICT arm), and that the argument actually reaches the driver rather than
// merely appearing in the SQL text.
package pg

import (
	"testing"
	"time"

	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

func ttEntryWithMode(t *testing.T, dt st.DeliveryType) *st.TranscriptEntry {
	t.Helper()
	e, err := st.New(st.NewEntryInput{
		TenantID:       pgTenantID,
		GCID:           pgUserGCID,
		Kind:           st.KindAssessment,
		SourceRef:      "assessment-1",
		Title:          "Algebra Quiz",
		DeliveryType:   dt,
		OccurredAt:     time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
		IdempotencyKey: "submission:sub-1:graded",
		Now:            time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ttEntryWithMode: %v", err)
	}
	return e
}

// TestPGTranscriptRepo_UpsertPersistsDeliveryType — the INSERT must name the
// column, and the ON CONFLICT arm must update it, or a re-grade would leave a
// stale mode behind on the converged row.
func TestPGTranscriptRepo_UpsertPersistsDeliveryType(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	if err := repo.Upsert(withCtx(), ttEntryWithMode(t, st.DeliveryTypeGraduate)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	calls := tx.q.execCalls
	last := calls[len(calls)-1]
	if !contains(last, "delivery_type") {
		t.Errorf("upsert SQL does not name delivery_type: %q", last)
	}
	if !contains(last, "delivery_type  = EXCLUDED.delivery_type") &&
		!contains(last, "delivery_type = EXCLUDED.delivery_type") {
		t.Errorf("ON CONFLICT arm must update delivery_type, or a re-grade keeps a stale mode: %q", last)
	}
}

// TestPGTranscriptRepo_UpsertBindsDeliveryTypeArg — the value must reach the
// driver. SQL text naming the column proves nothing if the arg never ships.
func TestPGTranscriptRepo_UpsertBindsDeliveryTypeArg(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	if err := repo.Upsert(withCtx(), ttEntryWithMode(t, st.DeliveryTypeShort)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	found := false
	for _, a := range args {
		if s, ok := a.(*string); ok && s != nil && *s == "short" {
			found = true
			break
		}
		if s, ok := a.(string); ok && s == "short" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("delivery_type value 'short' never reached the driver args: %#v", args)
	}
}

// TestPGTranscriptRepo_UpsertBindsNullForUnattributedMode — "" must bind as SQL
// NULL, not an empty string. NULL reads as "not attributable"; ” would be a
// distinct, meaningless value that breaks IS NULL filtering.
func TestPGTranscriptRepo_UpsertBindsNullForUnattributedMode(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	if err := repo.Upsert(withCtx(), ttEntryWithMode(t, "")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	for _, a := range args {
		if s, ok := a.(string); ok && s == "" {
			t.Errorf("an unattributed mode must bind as NULL, not an empty string: %#v", args)
		}
	}
}

// TestPGTranscriptRepo_ReadsSelectDeliveryType — both read paths must select the
// column, or the learner-facing transcript and the R+ gradebook join would
// silently render every entry mode-less however well the write path worked.
func TestPGTranscriptRepo_ReadsSelectDeliveryType(t *testing.T) {
	for name, sql := range map[string]string{
		"listTranscriptByGCIDSQL":          listTranscriptByGCIDSQL,
		"listTranscriptByAssessmentIDsSQL": listTranscriptByAssessmentIDsSQL,
	} {
		if !contains(sql, "delivery_type") {
			t.Errorf("%s does not select delivery_type: %q", name, sql)
		}
	}
}
