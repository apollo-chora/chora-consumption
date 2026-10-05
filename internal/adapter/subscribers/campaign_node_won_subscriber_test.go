// campaign_node_won_subscriber_test.go - WS-C4 (CHO-2083, ADR-227 D2)
// free-on-win reveal: consuming campaign.node_won.v1 publishes exactly ONE
// mana-exempt concept_suggestion.requested.v1 with the won node as focal,
// idempotent on the node_won event id via the claim-then-mark RevealLedger.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

const (
	nwTenant = "11111111-1111-1111-1111-111111111111"
	nwGCID   = "01970000-0000-7000-a000-000000001999"
	nwGoal   = "01970000-0000-7000-a000-00000000g001"
	nwEvent  = "01970000-0000-7000-a000-00000000e001"
)

// ---- fakes -------------------------------------------------------------------

type nwLedger struct {
	rows     map[string]*campaign.RevealClaim // keyed tenant|gcid|goal|concept
	claimErr error
	markErr  error
	marked   []string // request ids passed to MarkPublished
}

func nwKey(tenantID, gcid, goalID, conceptID string) string {
	return strings.Join([]string{tenantID, gcid, goalID, conceptID}, "|")
}

func (l *nwLedger) Claim(_ context.Context, c campaign.RevealClaim) (bool, *campaign.RevealClaim, error) {
	if l.claimErr != nil {
		return false, nil, l.claimErr
	}
	k := nwKey(c.TenantID, c.LearnerGCID, c.GoalID, c.ConceptID)
	if existing, ok := l.rows[k]; ok {
		cp := *existing
		return false, &cp, nil
	}
	if l.rows == nil {
		l.rows = map[string]*campaign.RevealClaim{}
	}
	cp := c
	l.rows[k] = &cp
	return true, &cp, nil
}

func (l *nwLedger) MarkPublished(_ context.Context, tenantID, gcid, goalID, conceptID, requestID string, at time.Time) error {
	if l.markErr != nil {
		return l.markErr
	}
	row, ok := l.rows[nwKey(tenantID, gcid, goalID, conceptID)]
	if !ok {
		return campaign.ErrRevealClaimNotFound
	}
	row.RequestID = &requestID
	row.PublishedAt = &at
	l.marked = append(l.marked, requestID)
	return nil
}

type nwGoals struct{ byID map[string]*goal.Goal }

func (s *nwGoals) Create(context.Context, *goal.Goal) error { return nil }
func (s *nwGoals) GetByID(_ context.Context, _, _, id string) (*goal.Goal, error) {
	return s.byID[id], nil
}
func (s *nwGoals) ListByLearner(context.Context, string, string) ([]*goal.Goal, error) {
	out := make([]*goal.Goal, 0, len(s.byID))
	for _, g := range s.byID {
		out = append(out, g)
	}
	return out, nil
}
func (s *nwGoals) Update(context.Context, *goal.Goal) error { return nil }

type nwConcepts struct{ nodes []*conceptgraph.ConceptNode }

func (s *nwConcepts) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *nwConcepts) GetByID(_ context.Context, _, _, id string) (*conceptgraph.ConceptNode, error) {
	for _, n := range s.nodes {
		if n.ConceptID == id && n.DeletedAt == nil {
			return n, nil
		}
	}
	return nil, nil
}
func (s *nwConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return s.nodes, nil
}
func (s *nwConcepts) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

type nwFailingPublisher struct{ err error }

func (p *nwFailingPublisher) Publish(string, events.Envelope, map[string]any) error { return p.err }

// nwRevealEmitter captures node_revealed.v1 emissions (and can fail).
type nwRevealEmitter struct {
	calls []events.CampaignNodeRevealedInput
	err   error
}

func (e *nwRevealEmitter) NodeRevealed(_ context.Context, in events.CampaignNodeRevealedInput) error {
	if e.err != nil {
		return e.err
	}
	e.calls = append(e.calls, in)
	return nil
}

// ---- fixture -----------------------------------------------------------------

func nwNode(id, title string) *conceptgraph.ConceptNode {
	return &conceptgraph.ConceptNode{ConceptID: id, TenantID: nwTenant, LearnerGCID: nwGCID, Title: title}
}

func nwPayload(conceptID string) CampaignNodeWonPayload {
	return CampaignNodeWonPayload{
		TenantID:    nwTenant,
		LearnerGCID: nwGCID,
		GoalID:      nwGoal,
		ConceptID:   conceptID,
		ConceptKey:  "factorisation",
		EventID:     nwEvent,
		Traceparent: "00-11111111111111111111111111111111-2222222222222222-01",
	}
}

// nwSubscriber wires a goal (root c-root, attached companion) + a 2-node roster
// over a discard node_revealed emitter.
func nwSubscriber(ledger *nwLedger, pub events.Publisher) *CampaignNodeWonSubscriber {
	return nwSubscriberEmit(ledger, pub, &nwRevealEmitter{})
}

// nwSubscriberEmit is nwSubscriber with an explicit node_revealed emitter.
func nwSubscriberEmit(ledger *nwLedger, pub events.Publisher, emitter CampaignRevealEmitter) *CampaignNodeWonSubscriber {
	fid := "01970000-0000-7000-a000-0000000000f1"
	root := "c-root"
	g := &goal.Goal{
		GoalID: nwGoal, TenantID: nwTenant, LearnerGCID: nwGCID,
		RootConceptID: &root, AttachedCompanionID: &fid,
		NorthStarNote: "Own algebra",
	}
	return NewCampaignNodeWonSubscriber(
		ledger,
		&nwGoals{byID: map[string]*goal.Goal{nwGoal: g}},
		&nwConcepts{nodes: []*conceptgraph.ConceptNode{
			nwNode("c-root", "Algebra"),
			nwNode("c-child", "Factorisation"),
		}},
		pub,
		emitter,
	)
}

// ---- tests ---------------------------------------------------------------------

// A fresh node win publishes ONE free reveal: focal = the won node,
// request_source = campaign_free_reveal, companion + theme resolved from the
// goal, idempotency key deterministic on the node_won event id, and the
// ledger row marked published.
func TestCampaignNodeWonSubscriber_FreshWin_PublishesFreeReveal(t *testing.T) {
	ledger := &nwLedger{}
	pub := events.NewInMemoryPublisher()
	em := &nwRevealEmitter{}
	sub := nwSubscriberEmit(ledger, pub, em)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	// The win mirrors as exactly ONE node_revealed.v1, anchored on the won
	// event id (idempotency) + carrying the goal id + concept key.
	if len(em.calls) != 1 {
		t.Fatalf("node_revealed emissions = %d; want 1", len(em.calls))
	}
	nr := em.calls[0]
	if nr.WonEventID != nwEvent || nr.GoalID != nwGoal || nr.ConceptID != "c-child" || nr.ConceptKey != "factorisation" {
		t.Errorf("node_revealed input = %+v", nr)
	}
	ev := evs[0]
	if ev.Topic != events.TopicConceptSuggestionRequested {
		t.Errorf("topic = %s; want %s", ev.Topic, events.TopicConceptSuggestionRequested)
	}
	if got := ev.Payload["request_source"]; got != events.SuggestionSourceCampaignFreeReveal {
		t.Errorf("request_source = %v; want %s (mana-exempt marker)", got, events.SuggestionSourceCampaignFreeReveal)
	}
	if got := ev.Payload["focal_concept_id"]; got != "c-child" {
		t.Errorf("focal_concept_id = %v; want c-child", got)
	}
	if got := ev.Payload["focal_title"]; got != "Factorisation" {
		t.Errorf("focal_title = %v; want Factorisation", got)
	}
	if got := ev.Payload["familiar_id"]; got != "01970000-0000-7000-a000-0000000000f1" {
		t.Errorf("companion_id = %v; want the goal's attached companion", got)
	}
	if got := ev.Payload["map_theme"]; got != "Algebra" {
		t.Errorf("map_theme = %v; want Algebra (root concept title)", got)
	}
	wantIdem := "concept_suggestion.requested.campaign." + nwEvent
	if ev.Envelope.IdempotencyKey != wantIdem {
		t.Errorf("idempotency_key = %s; want %s (deterministic on node_won event id)", ev.Envelope.IdempotencyKey, wantIdem)
	}
	// existing_concepts excludes the focal.
	existing, _ := ev.Payload["existing_concepts"].([]map[string]string)
	for _, c := range existing {
		if c["concept_id"] == "c-child" {
			t.Errorf("existing_concepts contains the focal - must be excluded")
		}
	}
	if len(ledger.marked) != 1 {
		t.Fatalf("MarkPublished calls = %d; want 1", len(ledger.marked))
	}
}

// Redelivery of an already-published win acks silently - no second reveal.
func TestCampaignNodeWonSubscriber_Redelivery_AcksWithoutRepublish(t *testing.T) {
	published := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	rid := "r-1"
	ledger := &nwLedger{rows: map[string]*campaign.RevealClaim{
		nwKey(nwTenant, nwGCID, nwGoal, "c-child"): {
			TenantID: nwTenant, LearnerGCID: nwGCID, GoalID: nwGoal, ConceptID: "c-child",
			NodeWonEventID: nwEvent, RequestID: &rid, PublishedAt: &published,
		},
	}}
	pub := events.NewInMemoryPublisher()
	sub := nwSubscriber(ledger, pub)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v (redelivery must ack)", err)
	}
	if n := len(pub.Events()); n != 0 {
		t.Errorf("published %d events on redelivery; want 0", n)
	}
}

// A claim that exists but was never marked published (crash between claim and
// publish) is re-published and marked on redelivery.
func TestCampaignNodeWonSubscriber_CrashWindow_RepublishesUnpublishedClaim(t *testing.T) {
	ledger := &nwLedger{rows: map[string]*campaign.RevealClaim{
		nwKey(nwTenant, nwGCID, nwGoal, "c-child"): {
			TenantID: nwTenant, LearnerGCID: nwGCID, GoalID: nwGoal, ConceptID: "c-child",
			NodeWonEventID: nwEvent,
		},
	}}
	pub := events.NewInMemoryPublisher()
	sub := nwSubscriber(ledger, pub)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(pub.Events()); n != 1 {
		t.Fatalf("published %d events; want 1 (crash-window recovery)", n)
	}
	if len(ledger.marked) != 1 {
		t.Errorf("MarkPublished calls = %d; want 1", len(ledger.marked))
	}
}

// A publish failure surfaces (NACK → Pub/Sub retries); the claim stays
// unpublished so the retry path re-publishes.
func TestCampaignNodeWonSubscriber_PublishError_ErrsForRedelivery(t *testing.T) {
	ledger := &nwLedger{}
	sub := nwSubscriber(ledger, &nwFailingPublisher{err: errors.New("outbox down")})

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err == nil {
		t.Fatal("Handle = nil; want error (publish failed must NACK)")
	}
	if len(ledger.marked) != 0 {
		t.Errorf("MarkPublished calls = %d; want 0 (never mark an unpublished reveal)", len(ledger.marked))
	}
}

// A node_revealed emit failure NACKs (the reveal is not marked, so the
// crash-window re-publishes both the suggestion + node_revealed on redelivery,
// deduped by their deterministic idempotency keys).
func TestCampaignNodeWonSubscriber_NodeRevealedEmitFails_Nacks(t *testing.T) {
	ledger := &nwLedger{}
	pub := events.NewInMemoryPublisher()
	em := &nwRevealEmitter{err: errors.New("outbox down")}
	sub := nwSubscriberEmit(ledger, pub, em)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err == nil {
		t.Fatal("node_revealed emit failure must NACK")
	}
	if len(ledger.marked) != 0 {
		t.Errorf("MarkPublished calls = %d; want 0 (never mark when node_revealed failed)", len(ledger.marked))
	}
}

// A won node that no longer exists (soft-deleted post-win) has nothing to
// reveal - ack loudly without publishing; the claim stays as the audit trace.
func TestCampaignNodeWonSubscriber_WonNodeGone_AcksWithoutReveal(t *testing.T) {
	ledger := &nwLedger{}
	pub := events.NewInMemoryPublisher()
	sub := nwSubscriber(ledger, pub)

	if err := sub.Handle(context.Background(), nwPayload("c-vanished")); err != nil {
		t.Fatalf("Handle: %v (a vanished node must ack, not DLQ-loop)", err)
	}
	if n := len(pub.Events()); n != 0 {
		t.Errorf("published %d events; want 0", n)
	}
}

// Mandatory fields missing → fail-loud error (NACK → DLQ), never a fabricated
// reveal.
func TestCampaignNodeWonSubscriber_MissingFields_FailLoud(t *testing.T) {
	sub := nwSubscriber(&nwLedger{}, events.NewInMemoryPublisher())
	p := nwPayload("c-child")
	p.EventID = ""
	if err := sub.Handle(context.Background(), p); err == nil {
		t.Fatal("Handle = nil; want error for missing event id")
	}
}

// The existing-concepts roster is capped at the shared bound.
func TestCampaignNodeWonSubscriber_ExistingRosterCapped(t *testing.T) {
	ledger := &nwLedger{}
	pub := events.NewInMemoryPublisher()
	fid := "01970000-0000-7000-a000-0000000000f1"
	root := "c-root"
	g := &goal.Goal{
		GoalID: nwGoal, TenantID: nwTenant, LearnerGCID: nwGCID,
		RootConceptID: &root, AttachedCompanionID: &fid,
	}
	nodes := []*conceptgraph.ConceptNode{nwNode("c-root", "Algebra"), nwNode("c-child", "Factorisation")}
	for i := 0; i < events.MaxExistingConceptsInSuggestionRequest+10; i++ {
		nodes = append(nodes, nwNode(fmt.Sprintf("c-%03d", i), fmt.Sprintf("T%03d", i)))
	}
	sub := NewCampaignNodeWonSubscriber(
		ledger,
		&nwGoals{byID: map[string]*goal.Goal{nwGoal: g}},
		&nwConcepts{nodes: nodes},
		pub,
		&nwRevealEmitter{},
	)
	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	existing, ok := evs[0].Payload["existing_concepts"].([]map[string]string)
	if !ok {
		t.Fatalf("existing_concepts has unexpected type %T", evs[0].Payload["existing_concepts"])
	}
	if len(existing) > events.MaxExistingConceptsInSuggestionRequest {
		t.Errorf("existing_concepts len = %d; want ≤ %d", len(existing), events.MaxExistingConceptsInSuggestionRequest)
	}
}

// ---- ADR-244 D2 (CHO-2303): the free-on-win reveal must cite entitled atoms ----
//
// Found by the UI walk: the Suggestions tab is gated behind WINNING the hex, so
// THIS producer, not the learner door, is how suggestions are actually
// requested here. Wiring only the learner door left the real path shipping an
// empty catalogue, which looks exactly like the pre-ADR-244 behaviour.

type stubCatalogueResolver struct {
	calls int
	out   []events.CatalogueAtom
	// ADR-245: recorded, not ignored. The theme args are the whole point of the
	// widened port, and a stub that discards them would let the subscriber pass
	// two empty strings forever while every test stayed green.
	gotTheme string
	gotFocal string
	gotRefs  []string
}

func (s *stubCatalogueResolver) ResolveSuggestionCatalogue(_ context.Context, _, _, mapTheme, focalTitle string, focalAtomRefs []string) []events.CatalogueAtom {
	s.calls++
	s.gotTheme, s.gotFocal, s.gotRefs = mapTheme, focalTitle, focalAtomRefs
	return s.out
}

// ADR-245: the free-on-win reveal must scope its catalogue with the SAME topic
// identity the learner door uses. The two producers were already required to
// share one entitlement resolution; sharing the entitlement but not the theme
// would give the Companion a themed menu at the door and a cross-domain one on a
// win, which is a worse failure than being consistently unscoped.
//
// The fixture's goal roots on "Algebra" and the won node is "Factorisation", so
// both values must arrive non-empty and correct.
func TestCampaignNodeWon_FreeRevealScopesCatalogueByGoalTheme(t *testing.T) {
	sub := nwSubscriberEmit(&nwLedger{}, events.NewInMemoryPublisher(), &nwRevealEmitter{})
	res := &stubCatalogueResolver{}
	sub.WithCatalogue(res)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.calls == 0 {
		t.Fatal("catalogue resolver never called (positive control)")
	}
	if res.gotTheme != "Algebra" {
		t.Errorf("mapTheme = %q, want %q: an unscoped catalogue cites cross-domain atoms", res.gotTheme, "Algebra")
	}
	if res.gotFocal != "Factorisation" {
		t.Errorf("focalTitle = %q, want %q: the focal concept sharpens the theme", res.gotFocal, "Factorisation")
	}
	// ADR-245 D1.2: the focal concept's attached atoms are the theme's taxonomy
	// anchor, so this producer must hand them over exactly as the learner door
	// does. The fixture's node carries none, and nil is the correct value to
	// pass for it - what must not happen is the argument being dropped.
	if res.gotRefs != nil && len(res.gotRefs) != 0 {
		t.Errorf("focalAtomRefs = %v, want the fixture node's (empty) refs", res.gotRefs)
	}
}

// The taxonomy anchor must actually TRAVEL from this producer. The test above
// cannot distinguish "passed the node's empty refs" from "passed nothing", so
// this one gives the focal node real refs and asserts they arrive.
func TestCampaignNodeWon_FreeRevealCarriesFocalAtomRefsAsTaxonomyAnchor(t *testing.T) {
	sub := nwSubscriberEmit(&nwLedger{}, events.NewInMemoryPublisher(), &nwRevealEmitter{})
	res := &stubCatalogueResolver{}
	sub.WithCatalogue(res)

	// Give the won concept two attached atoms.
	want := []string{"01980000-0000-7000-9000-0000000000a1", "01980000-0000-7000-9000-0000000000a2"}
	sub.concepts = &nwConcepts{nodes: []*conceptgraph.ConceptNode{
		nwNode("c-root", "Algebra"),
		{ConceptID: "c-child", TenantID: nwTenant, LearnerGCID: nwGCID,
			Title: "Factorisation", AtomRefs: want},
	}}

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(res.gotRefs) != len(want) {
		t.Fatalf("focalAtomRefs = %v, want %v: the taxonomy anchor never left this producer",
			res.gotRefs, want)
	}
	for i := range want {
		if res.gotRefs[i] != want[i] {
			t.Errorf("focalAtomRefs[%d] = %q, want %q", i, res.gotRefs[i], want[i])
		}
	}
}

func TestCampaignNodeWon_FreeRevealCarriesAtomCatalogue(t *testing.T) {
	ledger := &nwLedger{}
	pub := events.NewInMemoryPublisher()
	sub := nwSubscriberEmit(ledger, pub, &nwRevealEmitter{})
	res := &stubCatalogueResolver{out: []events.CatalogueAtom{
		{AtomID: "a1", Title: "Adding unlike fractions", AtomType: "mcq", TopicTags: []string{"fractions"}},
	}}
	sub.WithCatalogue(res)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.calls == 0 {
		t.Fatal("catalogue resolver never called: the free reveal ships an empty catalogue")
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	cat, ok := evs[0].Payload["atom_catalogue"].([]map[string]any)
	if !ok || len(cat) != 1 {
		t.Fatalf("atom_catalogue = %#v; want 1 entry", evs[0].Payload["atom_catalogue"])
	}
	if cat[0]["title"] != "Adding unlike fractions" {
		t.Errorf("title = %v", cat[0]["title"])
	}
}

// An unwired resolver must degrade to an empty catalogue, never fail the
// learner's reveal.
func TestCampaignNodeWon_FreeRevealSurvivesUnwiredCatalogue(t *testing.T) {
	ledger := &nwLedger{}
	pub := events.NewInMemoryPublisher()
	sub := nwSubscriberEmit(ledger, pub, &nwRevealEmitter{})

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("an unwired catalogue must not fail the reveal: %v", err)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	if cat, ok := evs[0].Payload["atom_catalogue"].([]map[string]any); !ok || len(cat) != 0 {
		t.Fatalf("want an empty catalogue, got %#v", evs[0].Payload["atom_catalogue"])
	}
}
