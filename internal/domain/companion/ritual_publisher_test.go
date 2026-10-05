// ritual_publisher_test.go — CHO-2016 G5: the publish/enable orchestration
// (compose capability → validate+price → persist → emit), RED-first. Domain
// service in the PathUnlocker style (injected ports; fake-tested).
package companion

import (
	"context"
	"errors"
	"testing"
)

// ---- fakes ----------------------------------------------------------------

type fakeRitualRepo struct {
	created   []*Ritual
	revisions []struct {
		ritualID string
		rev      RitualRevision
		price    int
	}
	enabled  map[string]bool
	getByID  map[string]*Ritual
	deleted  []string
	countRet int
}

func newFakeRitualRepo() *fakeRitualRepo {
	return &fakeRitualRepo{enabled: map[string]bool{}, getByID: map[string]*Ritual{}}
}

func (f *fakeRitualRepo) Create(_ context.Context, r *Ritual) error {
	f.created = append(f.created, r)
	f.getByID[r.RitualID] = r
	return nil
}
func (f *fakeRitualRepo) GetByID(_ context.Context, _, ritualID string) (*Ritual, error) {
	return f.getByID[ritualID], nil
}
func (f *fakeRitualRepo) ListByCompanion(_ context.Context, _, _ string) ([]*Ritual, error) {
	return nil, nil
}
func (f *fakeRitualRepo) AppendRevision(_ context.Context, _, ritualID string, rev RitualRevision, price int) error {
	f.revisions = append(f.revisions, struct {
		ritualID string
		rev      RitualRevision
		price    int
	}{ritualID, rev, price})
	return nil
}
func (f *fakeRitualRepo) SetEnabled(_ context.Context, _, ritualID string, enabled bool) error {
	f.enabled[ritualID] = enabled
	return nil
}
func (f *fakeRitualRepo) CountEnabledByCompanion(_ context.Context, _, _, _ string) (int, error) {
	return f.countRet, nil
}
func (f *fakeRitualRepo) SoftDelete(_ context.Context, _, ritualID string) error {
	f.deleted = append(f.deleted, ritualID)
	return nil
}

type fakeCapsResolver struct {
	caps RitualCapabilityContext
	err  error
}

func (f *fakeCapsResolver) ResolveCapability(_ context.Context, _, _, _, _ string) (RitualCapabilityContext, error) {
	return f.caps, f.err
}

type fakeRitualOutbox struct{ topics []string }

func (f *fakeRitualOutbox) PublishRitualEvent(_ context.Context, topic string, _ map[string]any, _ LoadoutEnvelope) error {
	f.topics = append(f.topics, topic)
	return nil
}

func newPublisher(t *testing.T, repo RitualRepository, caps RitualCapabilityResolver, obx RitualOutbox) *RitualPublisher {
	t.Helper()
	p, err := NewRitualPublisher(RitualPublisherConfig{
		Repo: repo, Caps: caps, Outbox: obx,
		// Every sink wired: these tests are about publish, enable and quota
		// semantics, not about what this deployment can write (N5 owns that).
		Sinks: allSinksWired(),
		Clock: fixedClock(), NewID: func() string { return "evt-1" },
	})
	if err != nil {
		t.Fatalf("NewRitualPublisher: %v", err)
	}
	return p
}

func publishInput() PublishRitualInput {
	return PublishRitualInput{
		TenantID: "t-1", CompanionID: "fam-1", OwnerGCID: "gcid-1",
		Name: "Morning Review", Trigger: TriggerManual, Sink: SinkQuestionBank,
		Steps: steps("explain_anew", "flashcard_forge"), ArmorVerdict: "allow",
	}
}

// ---- create draft ---------------------------------------------------------

func TestPublisher_CreateDraft(t *testing.T) {
	repo := newFakeRitualRepo()
	p := newPublisher(t, repo, &fakeCapsResolver{caps: ritualTestCaps()}, &fakeRitualOutbox{})
	r, err := p.CreateDraft(context.Background(), "t-1", "fam-1", "Morning Review", TriggerManual, SinkChat)
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if r.RitualID == "" || r.CurrentRevision != 0 || r.Enabled {
		t.Errorf("draft wrong: %+v", r)
	}
	if len(repo.created) != 1 {
		t.Errorf("draft must be persisted once, got %d", len(repo.created))
	}
}

func TestPublisher_CreateDraftValidatesFields(t *testing.T) {
	repo := newFakeRitualRepo()
	p := newPublisher(t, repo, &fakeCapsResolver{caps: ritualTestCaps()}, &fakeRitualOutbox{})
	if _, err := p.CreateDraft(context.Background(), "t-1", "fam-1", "R", TriggerManual, "webhook"); !errors.Is(err, ErrRitualUnknownSink) {
		t.Errorf("bad sink = %v, want ErrRitualUnknownSink", err)
	}
}

// ---- create + publish -----------------------------------------------------

func TestPublisher_CreatesPublishesEmits(t *testing.T) {
	repo := newFakeRitualRepo()
	caps := &fakeCapsResolver{caps: ritualTestCaps()}
	obx := &fakeRitualOutbox{}
	p := newPublisher(t, repo, caps, obx)

	r, rev, err := p.Publish(context.Background(), publishInput())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("a new ritual must be Created once, got %d", len(repo.created))
	}
	if len(repo.revisions) != 1 || repo.revisions[0].rev.RevisionNo != 1 {
		t.Fatalf("one revision must be appended, got %+v", repo.revisions)
	}
	if repo.revisions[0].price != RitualBasePriceUnits+RitualGenerativeUpliftUnits {
		t.Errorf("appended price = %d, want 35 (frozen composed)", repo.revisions[0].price)
	}
	if rev.RevisionNo != 1 || r.PublishedPriceUnits != 35 {
		t.Errorf("returned ritual/rev wrong: price=%d rev=%d", r.PublishedPriceUnits, rev.RevisionNo)
	}
	if len(obx.topics) != 1 || obx.topics[0] != TopicCompanionRitualPublished {
		t.Errorf("must emit ritual_published.v1, got %v", obx.topics)
	}
}

func TestPublisher_RepublishExistingAppendsRev2(t *testing.T) {
	repo := newFakeRitualRepo()
	caps := &fakeCapsResolver{caps: ritualTestCaps()}
	p := newPublisher(t, repo, caps, &fakeRitualOutbox{})

	// Seed an already-published ritual (rev 1).
	existing, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	_, _ = existing.Publish(steps("explain_anew"), "allow", ritualTestCaps(), fixedClock())
	repo.getByID[existing.RitualID] = existing

	in := publishInput()
	in.RitualID = existing.RitualID
	_, rev, err := p.Publish(context.Background(), in)
	if err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	if rev.RevisionNo != 2 {
		t.Errorf("re-publish must append rev 2, got %d", rev.RevisionNo)
	}
	if len(repo.created) != 0 {
		t.Errorf("re-publish must NOT Create a new ritual row")
	}
}

func TestPublisher_RepublishUnknownFailsLoud(t *testing.T) {
	repo := newFakeRitualRepo()
	p := newPublisher(t, repo, &fakeCapsResolver{caps: ritualTestCaps()}, &fakeRitualOutbox{})
	in := publishInput()
	in.RitualID = "does-not-exist"
	if _, _, err := p.Publish(context.Background(), in); !errors.Is(err, ErrRitualNotFound) {
		t.Errorf("re-publishing an unknown ritual = %v, want ErrRitualNotFound", err)
	}
}

func TestPublisher_StageGateRefusesBeforeStructural(t *testing.T) {
	caps := ritualTestCaps()
	caps.Stage = 3
	repo := newFakeRitualRepo()
	p := newPublisher(t, repo, &fakeCapsResolver{caps: caps}, &fakeRitualOutbox{})
	if _, _, err := p.Publish(context.Background(), publishInput()); !errors.Is(err, ErrRitualUnlockStage) {
		t.Errorf("publish at st3 = %v, want ErrRitualUnlockStage", err)
	}
	// Nothing persisted / emitted on a rejected publish.
	if len(repo.created) != 0 || len(repo.revisions) != 0 {
		t.Error("a rejected publish must not persist")
	}
}

func TestPublisher_InvalidStepsFailLoud(t *testing.T) {
	repo := newFakeRitualRepo()
	p := newPublisher(t, repo, &fakeCapsResolver{caps: ritualTestCaps()}, &fakeRitualOutbox{})
	in := publishInput()
	in.Steps = steps("explain_anew", "atom_forge") // atom_forge not equipped
	if _, _, err := p.Publish(context.Background(), in); !errors.Is(err, ErrRitualStepSkillNotEquipped) {
		t.Errorf("invalid step = %v, want ErrRitualStepSkillNotEquipped", err)
	}
}

// ---- enable / disable -----------------------------------------------------

func TestPublisher_EnableUsesResolvedQuota(t *testing.T) {
	repo := newFakeRitualRepo()
	existing, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	_, _ = existing.Publish(steps("explain_anew"), "allow", ritualTestCaps(), fixedClock())
	repo.getByID[existing.RitualID] = existing

	// The resolver reports OtherEnabledCount (excluding self) — 2 exhausts the
	// base quota 2.
	over := ritualTestCaps()
	over.OtherEnabledCount = 2
	p := newPublisher(t, repo, &fakeCapsResolver{caps: over}, &fakeRitualOutbox{})
	if err := p.SetEnabled(context.Background(), "t-1", "fam-1", "gcid-1", existing.RitualID, true); !errors.Is(err, ErrRitualEnabledQuotaReached) {
		t.Errorf("enable over quota = %v, want ErrRitualEnabledQuotaReached", err)
	}

	// Under quota → persists enabled.
	under := ritualTestCaps()
	under.OtherEnabledCount = 1
	p2 := newPublisher(t, repo, &fakeCapsResolver{caps: under}, &fakeRitualOutbox{})
	if err := p2.SetEnabled(context.Background(), "t-1", "fam-1", "gcid-1", existing.RitualID, true); err != nil {
		t.Fatalf("enable within quota: %v", err)
	}
	if !repo.enabled[existing.RitualID] {
		t.Error("enable must persist via SetEnabled")
	}
}

func TestPublisher_DisablePersists(t *testing.T) {
	repo := newFakeRitualRepo()
	existing, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	_, _ = existing.Publish(steps("explain_anew"), "allow", ritualTestCaps(), fixedClock())
	existing.Enabled = true
	repo.getByID[existing.RitualID] = existing
	repo.enabled[existing.RitualID] = true

	p := newPublisher(t, repo, &fakeCapsResolver{caps: ritualTestCaps()}, &fakeRitualOutbox{})
	if err := p.SetEnabled(context.Background(), "t-1", "fam-1", "gcid-1", existing.RitualID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if repo.enabled[existing.RitualID] {
		t.Error("disable must persist enabled=false")
	}
}

func TestPublisher_NilDepsFailLoud(t *testing.T) {
	if _, err := NewRitualPublisher(RitualPublisherConfig{}); err == nil {
		t.Error("nil deps must fail loud")
	}
}
