// atom_refresh_subscriber_test.go - ADR-244 D5 standing refresh trigger:
// consuming atom.published / atom.updated proposes the atom to reachable
// (won focal hex, companion-attached goal) concepts as pending suggestion
// REQUESTS, once ever per (tenant, learner, atom, focal) via the
// claim-then-mark atom_refresh_ledger, capped per event and per learner-day.
package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

const (
	arTenant = "11111111-1111-1111-1111-111111111111"
	arGCID   = "01970000-0000-7000-a000-000000001999"
	arGoal   = "01970000-0000-7000-a000-000000009001"
	arRoot   = "01970000-0000-7000-a000-00000000c000"
	arFocal  = "01970000-0000-7000-a000-00000000c001"
	arAtom   = "01970000-0000-7000-a000-00000000a001"
	arEvent  = "01970000-0000-7000-a000-00000000e001"
)

// ---- fakes -------------------------------------------------------------------

type arLedger struct {
	rows      map[string]*atomrefresh.RefreshClaim // keyed tenant|gcid|atom|focal
	claimErr  error
	markErr   error
	countErr  error
	dailyUsed int
	marked    []string
}

func arKey(tenantID, gcid, atomID, focalID string) string {
	return strings.Join([]string{tenantID, gcid, atomID, focalID}, "|")
}

func (l *arLedger) Claim(_ context.Context, c atomrefresh.RefreshClaim) (bool, *atomrefresh.RefreshClaim, error) {
	if l.claimErr != nil {
		return false, nil, l.claimErr
	}
	k := arKey(c.TenantID, c.LearnerGCID, c.AtomID, c.FocalConceptID)
	if existing, ok := l.rows[k]; ok {
		cp := *existing
		return false, &cp, nil
	}
	if l.rows == nil {
		l.rows = map[string]*atomrefresh.RefreshClaim{}
	}
	cp := c
	l.rows[k] = &cp
	return true, &cp, nil
}

func (l *arLedger) MarkPublished(_ context.Context, tenantID, gcid, atomID, focalID, requestID string, at time.Time) error {
	if l.markErr != nil {
		return l.markErr
	}
	row, ok := l.rows[arKey(tenantID, gcid, atomID, focalID)]
	if !ok {
		return atomrefresh.ErrRefreshClaimNotFound
	}
	row.RequestID = &requestID
	row.PublishedAt = &at
	l.marked = append(l.marked, requestID)
	return nil
}

func (l *arLedger) CountClaimedSince(context.Context, string, string, time.Time) (int, error) {
	if l.countErr != nil {
		return 0, l.countErr
	}
	return l.dailyUsed, nil
}

type arGoals struct {
	goals []*goal.Goal
	err   error
}

func (s *arGoals) ListCompanionedByTenant(context.Context, string) ([]*goal.Goal, error) {
	return s.goals, s.err
}

type arConcepts struct{ nodes []*conceptgraph.ConceptNode }

func (s *arConcepts) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *arConcepts) GetByID(_ context.Context, _, _, id string) (*conceptgraph.ConceptNode, error) {
	for _, n := range s.nodes {
		if n.ConceptID == id && n.DeletedAt == nil {
			return n, nil
		}
	}
	return nil, nil
}
func (s *arConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return s.nodes, nil
}
func (s *arConcepts) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

type arEdges struct{ edges []*conceptgraph.Edge }

func (s *arEdges) Create(context.Context, *conceptgraph.Edge) error { return nil }
func (s *arEdges) GetByID(_ context.Context, _, _, id string) (*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *arEdges) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return s.edges, nil
}
func (s *arEdges) ListByConcept(context.Context, string, string, string) ([]*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *arEdges) Update(context.Context, *conceptgraph.Edge) error { return nil }
func (s *arEdges) SoftDeleteByConcept(context.Context, string, string, string, time.Time) (int64, error) {
	return 0, nil
}

type arProgress struct{ won map[string]bool } // conceptID -> won

func (s *arProgress) GetByConcept(_ context.Context, _, _, conceptID string) (*campaign.NodeProgress, error) {
	return nil, nil
}
func (s *arProgress) ListByLearner(context.Context, string, string) ([]*campaign.NodeProgress, error) {
	out := make([]*campaign.NodeProgress, 0, len(s.won))
	for id, won := range s.won {
		p := &campaign.NodeProgress{ConceptID: id}
		if won {
			t := time.Now().UTC()
			p.WonAt = &t
		}
		out = append(out, p)
	}
	return out, nil
}
func (s *arProgress) Save(context.Context, *campaign.NodeProgress) error { return nil }

type arAtoms struct {
	atom *atom_index.AtomIndex
	err  error
}

func (s *arAtoms) Get(context.Context, string) (*atom_index.AtomIndex, error) {
	return s.atom, s.err
}

type arCatalogue struct {
	atoms []events.CatalogueAtom
	calls int
}

func (s *arCatalogue) ResolveSuggestionCatalogue(_ context.Context, _, _, _, _ string, _ []string) []events.CatalogueAtom {
	s.calls++
	return s.atoms
}

type arPublisher struct {
	topics   []string
	envs     []events.Envelope
	payloads []map[string]any
	err      error
}

func (p *arPublisher) Publish(topic string, env events.Envelope, payload map[string]any) error {
	if p.err != nil {
		return p.err
	}
	p.topics = append(p.topics, topic)
	p.envs = append(p.envs, env)
	p.payloads = append(p.payloads, payload)
	return nil
}

// ---- fixture -----------------------------------------------------------------

func arServableAtom(title string, tags ...string) *atom_index.AtomIndex {
	return &atom_index.AtomIndex{
		AtomID:          arAtom,
		TenantID:        arTenant,
		Title:           title,
		AtomType:        "mcq",
		CorrectOptionID: "opt-a",
		TopicTags:       tags,
		Status:          atom_index.StatusPublished,
	}
}

func arNode(id, title string, refs ...string) *conceptgraph.ConceptNode {
	return &conceptgraph.ConceptNode{
		ConceptID:   id,
		TenantID:    arTenant,
		LearnerGCID: arGCID,
		Title:       title,
		AtomRefs:    refs,
	}
}

func arHierarchyEdge(source, target string) *conceptgraph.Edge {
	return &conceptgraph.Edge{
		EdgeID:          "edge-" + source + "-" + target,
		TenantID:        arTenant,
		LearnerGCID:     arGCID,
		SourceConceptID: source,
		TargetConceptID: target,
		Class:           conceptgraph.EdgeClassHierarchy,
	}
}

func arCompanionedGoal(id, learner, root string) *goal.Goal {
	fam := "companion-1"
	r := root
	return &goal.Goal{
		GoalID:              id,
		TenantID:            arTenant,
		LearnerGCID:         learner,
		NorthStarNote:       "note",
		AttachedCompanionID: &fam,
		RootConceptID:       &r,
	}
}

type arFixture struct {
	ledger    *arLedger
	goals     *arGoals
	concepts  *arConcepts
	edges     *arEdges
	progress  *arProgress
	atoms     *arAtoms
	catalogue *arCatalogue
	pub       *arPublisher
	sub       *AtomRefreshSubscriber
}

// arDefaultFixture: one companioned goal, root arRoot with won focal arFocal
// ("Refactoring Patterns") under it, atom entitled via the catalogue.
func arDefaultFixture() *arFixture {
	f := &arFixture{
		ledger:   &arLedger{},
		goals:    &arGoals{goals: []*goal.Goal{arCompanionedGoal(arGoal, arGCID, arRoot)}},
		concepts: &arConcepts{nodes: []*conceptgraph.ConceptNode{arNode(arRoot, "Software Design"), arNode(arFocal, "Refactoring Patterns")}},
		edges:    &arEdges{edges: []*conceptgraph.Edge{arHierarchyEdge(arRoot, arFocal)}},
		progress: &arProgress{won: map[string]bool{arFocal: true}},
		atoms:    &arAtoms{atom: arServableAtom("Refactoring legacy code", "refactoring")},
		catalogue: &arCatalogue{atoms: []events.CatalogueAtom{
			{AtomID: arAtom, Title: "Refactoring legacy code", AtomType: "mcq"},
		}},
		pub: &arPublisher{},
	}
	f.sub = NewAtomRefreshSubscriber(f.ledger, f.goals, f.concepts, f.edges, f.progress, f.atoms, f.pub).
		WithCatalogue(f.catalogue)
	return f
}

func arTrigger() AtomRefreshTrigger {
	return AtomRefreshTrigger{
		TenantID: arTenant,
		AtomID:   arAtom,
		EventID:  arEvent,
		Reason:   AtomRefreshReasonPublished,
	}
}

// ---- tests -------------------------------------------------------------------

func TestAtomRefresh_ProposesToWonCompanionedFocal(t *testing.T) {
	f := arDefaultFixture()
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.pub.topics) != 1 {
		t.Fatalf("expected exactly 1 suggestion request, got %d", len(f.pub.topics))
	}
	if f.pub.topics[0] != events.TopicConceptSuggestionRequested {
		t.Fatalf("topic = %s", f.pub.topics[0])
	}
	payload := f.pub.payloads[0]
	if payload["request_source"] != events.SuggestionSourceAtomRefresh {
		t.Errorf("request_source = %v, want atom_refresh", payload["request_source"])
	}
	if payload["focal_concept_id"] != arFocal {
		t.Errorf("focal = %v, want %s", payload["focal_concept_id"], arFocal)
	}
	if payload["map_theme"] != "Software Design" {
		t.Errorf("map_theme = %v, want the root concept title", payload["map_theme"])
	}
	if payload["familiar_id"] != "companion-1" {
		t.Errorf("companion_id = %v", payload["familiar_id"])
	}
	if got := f.pub.envs[0].IdempotencyKey; !strings.Contains(got, arEvent) || !strings.Contains(got, arFocal) {
		t.Errorf("idempotency key must be deterministic on (event, focal): %s", got)
	}
	if len(f.ledger.marked) != 1 {
		t.Errorf("ledger must be marked published once, got %v", f.ledger.marked)
	}
}

func TestAtomRefresh_DuplicateDeliveryAcksWithoutRepublish(t *testing.T) {
	f := arDefaultFixture()
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("redelivery must ack, got %v", err)
	}
	if len(f.pub.topics) != 1 {
		t.Fatalf("redelivery must not republish, got %d publishes", len(f.pub.topics))
	}
}

func TestAtomRefresh_CrashWindowRepublishes(t *testing.T) {
	f := arDefaultFixture()
	// A claimed-but-unpublished row is the publish crash-window.
	claim, err := atomrefresh.NewRefreshClaim(arTenant, arGCID, arAtom, arFocal, "older-event", time.Now().UTC())
	if err != nil {
		t.Fatalf("fixture claim: %v", err)
	}
	f.ledger.rows = map[string]*atomrefresh.RefreshClaim{
		arKey(arTenant, arGCID, arAtom, arFocal): &claim,
	}
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.pub.topics) != 1 {
		t.Fatalf("crash-window row must re-publish, got %d", len(f.pub.topics))
	}
}

func TestAtomRefresh_AtomNotInCatalogueSkipsWithAuditRow(t *testing.T) {
	f := arDefaultFixture()
	f.catalogue.atoms = []events.CatalogueAtom{{AtomID: "some-other-atom"}}
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("not-entitled must ack, got %v", err)
	}
	if len(f.pub.topics) != 0 {
		t.Fatalf("not-entitled atom must not publish, got %d", len(f.pub.topics))
	}
	if len(f.ledger.rows) != 1 {
		t.Fatalf("the claim stays as the audit trace of the skip, rows=%d", len(f.ledger.rows))
	}
}

func TestAtomRefresh_DailyCapSkipsLearner(t *testing.T) {
	f := arDefaultFixture()
	f.ledger.dailyUsed = atomrefresh.MaxProposalsPerLearnerPerDay
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("over-cap must ack, got %v", err)
	}
	if len(f.pub.topics) != 0 || len(f.ledger.rows) != 0 {
		t.Fatalf("over-cap learner must not claim or publish: publishes=%d rows=%d", len(f.pub.topics), len(f.ledger.rows))
	}
}

func TestAtomRefresh_PerEventCapBoundsFanOut(t *testing.T) {
	f := arDefaultFixture()
	nodes := []*conceptgraph.ConceptNode{arNode(arRoot, "Software Design")}
	edges := []*conceptgraph.Edge{}
	won := map[string]bool{}
	for _, suffix := range []string{"c001", "c002", "c003", "c004", "c005"} {
		id := "01970000-0000-7000-a000-00000000" + suffix
		nodes = append(nodes, arNode(id, "Refactoring "+suffix))
		edges = append(edges, arHierarchyEdge(arRoot, id))
		won[id] = true
	}
	f.concepts.nodes = nodes
	f.edges.edges = edges
	f.progress.won = won
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.pub.topics) != atomrefresh.MaxProposalsPerAtomEvent {
		t.Fatalf("per-event cap must bound fan-out: got %d, want %d",
			len(f.pub.topics), atomrefresh.MaxProposalsPerAtomEvent)
	}
}

func TestAtomRefresh_UnwonOrUncompanionedOrOffTopicNotTargeted(t *testing.T) {
	f := arDefaultFixture()
	f.progress.won = map[string]bool{} // focal not won
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.pub.topics) != 0 {
		t.Fatalf("unwon focal must not be targeted, got %d", len(f.pub.topics))
	}

	f2 := arDefaultFixture()
	f2.goals.goals = nil // no companioned goals at all
	if err := f2.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f2.pub.topics) != 0 {
		t.Fatalf("no companioned goal must mean no proposals, got %d", len(f2.pub.topics))
	}

	f3 := arDefaultFixture()
	f3.atoms.atom = arServableAtom("Sourdough starter hydration", "baking")
	if err := f3.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f3.pub.topics) != 0 {
		t.Fatalf("zero lexical affinity must not propose, got %d", len(f3.pub.topics))
	}
}

func TestAtomRefresh_AtomAlreadyOnFocalNotReproposed(t *testing.T) {
	f := arDefaultFixture()
	f.concepts.nodes = []*conceptgraph.ConceptNode{
		arNode(arRoot, "Software Design"),
		arNode(arFocal, "Refactoring Patterns", arAtom), // already attached
	}
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.pub.topics) != 0 {
		t.Fatalf("an atom already in focal atom_refs must not re-propose, got %d", len(f.pub.topics))
	}
}

func TestAtomRefresh_UnknownAtomAcksQuietly(t *testing.T) {
	f := arDefaultFixture()
	f.atoms.atom = nil
	f.atoms.err = atom_index.ErrNotFound
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("an atom the projection never saw must ack (honest omission), got %v", err)
	}
	if len(f.pub.topics) != 0 {
		t.Fatalf("unknown atom must not propose, got %d", len(f.pub.topics))
	}
}

func TestAtomRefresh_NotServableAcksQuietly(t *testing.T) {
	f := arDefaultFixture()
	f.atoms.atom.Status = atom_index.StatusDraft
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("non-servable atom must ack, got %v", err)
	}
	if len(f.pub.topics) != 0 {
		t.Fatalf("non-servable atom must not propose, got %d", len(f.pub.topics))
	}
}

func TestAtomRefresh_FailuresNack(t *testing.T) {
	boom := errors.New("boom")

	f := arDefaultFixture()
	f.atoms.err = boom
	if err := f.sub.Handle(context.Background(), arTrigger()); err == nil {
		t.Fatal("atom_index read failure must NACK")
	}

	f2 := arDefaultFixture()
	f2.goals.err = boom
	if err := f2.sub.Handle(context.Background(), arTrigger()); err == nil {
		t.Fatal("goal listing failure must NACK")
	}

	f3 := arDefaultFixture()
	f3.pub.err = boom
	if err := f3.sub.Handle(context.Background(), arTrigger()); err == nil {
		t.Fatal("publish failure must NACK")
	}

	f4 := arDefaultFixture()
	f4.ledger.claimErr = boom
	if err := f4.sub.Handle(context.Background(), arTrigger()); err == nil {
		t.Fatal("ledger claim failure must NACK")
	}

	f5 := arDefaultFixture()
	f5.ledger.markErr = boom
	if err := f5.sub.Handle(context.Background(), arTrigger()); err == nil {
		t.Fatal("mark-published failure must NACK")
	}
}

func TestAtomRefresh_MissingMandatoryFieldsError(t *testing.T) {
	f := arDefaultFixture()
	for _, tr := range []AtomRefreshTrigger{
		{AtomID: arAtom, EventID: arEvent},
		{TenantID: arTenant, EventID: arEvent},
		{TenantID: arTenant, AtomID: arAtom},
	} {
		if err := f.sub.Handle(context.Background(), tr); err == nil {
			t.Fatalf("missing mandatory field must error: %+v", tr)
		}
	}
}

func TestAtomRefresh_ExistingConceptsExcludeFocalAndCarryCatalogue(t *testing.T) {
	f := arDefaultFixture()
	if err := f.sub.Handle(context.Background(), arTrigger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	payload := f.pub.payloads[0]
	existing, ok := payload["existing_concepts"].([]map[string]string)
	if !ok {
		t.Fatalf("existing_concepts wrong shape: %T", payload["existing_concepts"])
	}
	for _, ec := range existing {
		if ec["concept_id"] == arFocal {
			t.Errorf("existing_concepts must exclude the focal")
		}
	}
	cat, ok := payload["atom_catalogue"].([]map[string]any)
	if !ok || len(cat) != 1 {
		t.Fatalf("atom_catalogue must carry the resolved catalogue: %v", payload["atom_catalogue"])
	}
	if f.catalogue.calls != 1 {
		t.Errorf("catalogue resolved once per emitted proposal, got %d", f.catalogue.calls)
	}
}
