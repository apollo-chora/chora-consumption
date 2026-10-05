package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	geo "github.com/apollo-chora/chora-consumption/internal/domain/growth_edge_output"
)

type geoFakeRepo struct {
	batches [][]geo.Output
	err     error
	newRows int
}

func (r *geoFakeRepo) CreateBatch(_ context.Context, outs []geo.Output) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.batches = append(r.batches, outs)
	return r.newRows, nil
}

func (r *geoFakeRepo) ListForUpload(context.Context, string, string, string) ([]geo.Output, error) {
	return nil, nil
}

func geoEnv() events.Envelope {
	return events.Envelope{
		EventID:        "019f8a00-0000-7000-e000-000000000001",
		IdempotencyKey: "weakness.outputs_generated.upl-1",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		GCID:           "00000000-0000-7000-8000-000000001999",
		OccurredAt:     time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		SourceProject:  "chora-489812",
		SourceService:  "chora-ai-kernel-orchestrator",
		// Mandatory per the event-envelope contract; the shared validator
		// rejects without it, so a producer that omits it NACKs 100%.
		Traceparent:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SchemaVersion: 1,
	}
}

func geoPayload(outs ...GeneratedOutputPayload) WeaknessOutputsGeneratedPayload {
	return WeaknessOutputsGeneratedPayload{
		UploadID:    "019f89c7-3418-70c4-bc6a-1396c35166fc",
		TenantID:    "11111111-1111-7111-8111-111111111111",
		LearnerGCID: "00000000-0000-7000-8000-000000001999",
		Outputs:     outs,
		GeneratedAt: time.Date(2026, 7, 23, 12, 0, 5, 0, time.UTC),
	}
}

func studyAids() GeneratedOutputPayload {
	return GeneratedOutputPayload{Kind: "study_aids", ContentJSON: `"Revise fluvial vs pluvial."`, Metered: true}
}

func practiceTest() GeneratedOutputPayload {
	return GeneratedOutputPayload{Kind: "practice_test", ContentJSON: `{"questions":[]}`, Metered: true}
}

func TestHandle_ProjectsBothKindsInOneBatch(t *testing.T) {
	repo := &geoFakeRepo{newRows: 2}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	if err := s.Handle(context.Background(), geoEnv(), geoPayload(studyAids(), practiceTest())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.batches) != 1 {
		t.Fatalf("want 1 all-or-nothing batch, got %d", len(repo.batches))
	}
	if len(repo.batches[0]) != 2 {
		t.Fatalf("want 2 artifacts, got %d", len(repo.batches[0]))
	}
}

func TestHandle_CarriesCorrelationAndProvenance(t *testing.T) {
	repo := &geoFakeRepo{newRows: 1}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	if err := s.Handle(context.Background(), geoEnv(), geoPayload(studyAids())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := repo.batches[0][0]
	if got.UploadID != "019f89c7-3418-70c4-bc6a-1396c35166fc" {
		t.Errorf("UploadID = %q", got.UploadID)
	}
	if got.SourceEventID != geoEnv().EventID {
		t.Errorf("SourceEventID = %q, want the event id so a duplicate is diagnosable", got.SourceEventID)
	}
	if got.OutputID == "" {
		t.Error("OutputID must be minted")
	}
	if !got.Metered {
		t.Error("Metered must be carried so the surface is honest about what was paid for")
	}
}

func TestHandle_EmptyOutputsIsAValidAck(t *testing.T) {
	// "Generated nothing" is a real outcome the producer deliberately emits.
	// NACKing it would DLQ a correct event forever.
	repo := &geoFakeRepo{}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	if err := s.Handle(context.Background(), geoEnv(), geoPayload()); err != nil {
		t.Fatalf("empty outputs must ack, got %v", err)
	}
	if len(repo.batches) != 0 {
		t.Errorf("empty outputs must not write, got %d batches", len(repo.batches))
	}
}

func TestHandle_UnknownKindFailsLoud(t *testing.T) {
	// Dropping it would recreate the WS-7 gap one artifact at a time.
	repo := &geoFakeRepo{}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	err := s.Handle(context.Background(), geoEnv(),
		geoPayload(GeneratedOutputPayload{Kind: "focused_dose", ContentJSON: `"x"`}))
	if err == nil {
		t.Fatal("want error (NACK) on an unknown kind, got nil")
	}
	if len(repo.batches) != 0 {
		t.Error("must not persist a partial batch")
	}
}

func TestHandle_MalformedContentFailsLoud(t *testing.T) {
	repo := &geoFakeRepo{}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	err := s.Handle(context.Background(), geoEnv(),
		geoPayload(GeneratedOutputPayload{Kind: "study_aids", ContentJSON: `{"unclosed":`}))
	if err == nil {
		t.Fatal("want error on malformed content JSON, got nil")
	}
}

func TestHandle_RejectsMissingIdentity(t *testing.T) {
	s := NewWeaknessOutputsGeneratedSubscriber(&geoFakeRepo{})
	for name, mutate := range map[string]func(*WeaknessOutputsGeneratedPayload){
		"tenant":  func(p *WeaknessOutputsGeneratedPayload) { p.TenantID = "" },
		"learner": func(p *WeaknessOutputsGeneratedPayload) { p.LearnerGCID = " " },
		"upload":  func(p *WeaknessOutputsGeneratedPayload) { p.UploadID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := geoPayload(studyAids())
			mutate(&p)
			if err := s.Handle(context.Background(), geoEnv(), p); err == nil {
				t.Fatalf("missing %s: want error, got nil", name)
			}
		})
	}
}

func TestHandle_RedeliveryIsSkippedByTheInbox(t *testing.T) {
	repo := &geoFakeRepo{newRows: 1}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	env, p := geoEnv(), geoPayload(studyAids())
	for i := 0; i < 3; i++ {
		if err := s.Handle(context.Background(), env, p); err != nil {
			t.Fatalf("Handle #%d: %v", i, err)
		}
	}
	if len(repo.batches) != 1 {
		t.Errorf("redelivery must not duplicate a paid-for artifact: %d batches", len(repo.batches))
	}
}

func TestHandle_RepoFailureLeavesTheKeyUnclaimed(t *testing.T) {
	// A transient pg failure must redeliver and reprocess, never ack into a
	// black hole: the learner already paid for these artifacts.
	repo := &geoFakeRepo{err: errors.New("pg down")}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	env, p := geoEnv(), geoPayload(studyAids())
	if err := s.Handle(context.Background(), env, p); err == nil {
		t.Fatal("want error so Pub/Sub NACKs")
	}
	repo.err = nil
	repo.newRows = 1
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if len(repo.batches) != 1 {
		t.Errorf("retry must reprocess, got %d batches", len(repo.batches))
	}
}

func TestHandle_FallsBackToEnvelopeTimeWhenGeneratedAtMissing(t *testing.T) {
	repo := &geoFakeRepo{newRows: 1}
	s := NewWeaknessOutputsGeneratedSubscriber(repo)
	p := geoPayload(studyAids())
	p.GeneratedAt = time.Time{}
	if err := s.Handle(context.Background(), geoEnv(), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := repo.batches[0][0].GeneratedAt; !got.Equal(geoEnv().OccurredAt) {
		t.Errorf("GeneratedAt = %v, want the envelope occurred_at fallback", got)
	}
}

func TestHandle_RejectsInvalidEnvelope(t *testing.T) {
	s := NewWeaknessOutputsGeneratedSubscriber(&geoFakeRepo{})
	env := geoEnv()
	env.EventID = ""
	if err := s.Handle(context.Background(), env, geoPayload(studyAids())); err == nil {
		t.Fatal("want error on an envelope missing event_id")
	} else if !strings.Contains(strings.ToLower(err.Error()), "event") {
		t.Logf("envelope validation error: %v", err)
	}
}
