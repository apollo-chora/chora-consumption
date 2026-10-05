// grounded_search_gateway_client_test.go — P5 Far Sight (CHO-2017, ADR-231 D7):
// the REAL consumption→gateway GroundedSearch adapter that replaces the P5
// nil-seam. These pin the wire mapping the Seeker builders depend on: the
// domain grounded.Query → mgv1.GroundedSearchRequest (caller identity, the
// external_egress action_code, a per-call UUIDv7 idempotency key, the W3C
// traceparent) and the mgv1.GroundedSearchResponse citations → grounded.Hit
// (uri/title/snippet/domain, ADR-231 D4). The citation mandate/hedge/fence stay
// caller-side (the Seeker router) against the returned Result — untested here.
package clients

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/grounded"
)

// testGroundedActionCode is a per-skill grounded-egress action_code (fact_check's
// own price, companion_skill_fact_check = 40); the Seeker builder supplies it on
// grounded.Query.ActionCode and the adapter forwards it verbatim (ADR-231 D6).
const testGroundedActionCode = "companion_skill_fact_check"

// fakeGroundedGRPC captures the outgoing request + returns a canned response.
type fakeGroundedGRPC struct {
	resp  *mgv1.GroundedSearchResponse
	err   error
	got   *mgv1.GroundedSearchRequest
	calls int
}

func (f *fakeGroundedGRPC) GroundedSearch(_ context.Context, in *mgv1.GroundedSearchRequest, _ ...grpc.CallOption) (*mgv1.GroundedSearchResponse, error) {
	f.calls++
	f.got = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func twoCitationResp() *mgv1.GroundedSearchResponse {
	return &mgv1.GroundedSearchResponse{
		Citations: []*mgv1.GroundedCitation{
			{Uri: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/aaa", Title: "Earth is an oblate spheroid", Domain: "nasa.gov", Snippet: "Measurements confirm a near-spherical planet."},
			{Uri: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/bbb", Title: "Shape of the Earth", Domain: "noaa.gov", Snippet: "Satellite geodesy."},
		},
	}
}

func okQuery() grounded.Query {
	return grounded.Query{TenantID: "tenant-x", GCID: "gcid-y", Directive: "The Earth is round", MaxHits: 5, ActionCode: testGroundedActionCode}
}

func TestGroundedSearch_MapsCitationsToHitsWithDomain(t *testing.T) {
	fake := &fakeGroundedGRPC{resp: twoCitationResp()}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	res, err := c.SearchGround(context.Background(), okQuery())
	if err != nil {
		t.Fatalf("SearchGround err = %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("Hits = %d, want 2", len(res.Hits))
	}
	h := res.Hits[0]
	if h.URL != "https://vertexaisearch.cloud.google.com/grounding-api-redirect/aaa" ||
		h.Title != "Earth is an oblate spheroid" ||
		h.Snippet != "Measurements confirm a near-spherical planet." ||
		h.Domain != "nasa.gov" { // ADR-231 D4 — the DURABLE citation surface
		t.Errorf("first hit mismapped: %+v", h)
	}
	if res.Hits[1].Domain != "noaa.gov" {
		t.Errorf("second hit domain = %q, want noaa.gov", res.Hits[1].Domain)
	}
	// The mandate primitive still holds over the mapped result.
	if !res.HasCitations() {
		t.Error("a two-citation response must satisfy the citation mandate")
	}
}

func TestGroundedSearch_StampsRequestIdentityAndActionCode(t *testing.T) {
	fake := &fakeGroundedGRPC{resp: twoCitationResp()}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	if _, err := c.SearchGround(context.Background(), okQuery()); err != nil {
		t.Fatalf("SearchGround err = %v", err)
	}
	got := fake.got
	if got == nil {
		t.Fatal("no request captured")
	}
	if got.TenantId != "tenant-x" || got.Gcid != "gcid-y" {
		t.Errorf("tenant/gcid mismapped: tenant=%q gcid=%q", got.TenantId, got.Gcid)
	}
	if got.Directive != "The Earth is round" {
		t.Errorf("directive = %q, want the screened directive", got.Directive)
	}
	if got.MaxResults != 5 {
		t.Errorf("max_results = %d, want 5", got.MaxResults)
	}
	if got.AgentId != "companion_seeker" {
		t.Errorf("agent_id = %q, want companion_seeker (ADR-231 D7)", got.AgentId)
	}
	if got.CrewKind != "companion" {
		t.Errorf("crew_kind = %q, want companion", got.CrewKind)
	}
	// ADR-231 D6: the gateway is the SOLE meter; the adapter forwards the caller's
	// per-skill external_egress action_code verbatim so the grounded egress debits
	// that skill's own price ONCE.
	if got.ActionCode != testGroundedActionCode {
		t.Errorf("action_code = %q, want the caller-supplied %q", got.ActionCode, testGroundedActionCode)
	}
	// Idempotency: a per-call UUIDv7 invocation_id (36 chars) so a retried Seeker
	// turn collapses to one ledger row + one debit.
	if len(got.InvocationId) != 36 {
		t.Errorf("invocation_id = %q, want a 36-char UUIDv7", got.InvocationId)
	}
}

func TestGroundedSearch_MintsFreshInvocationIdPerCall(t *testing.T) {
	fake := &fakeGroundedGRPC{resp: twoCitationResp()}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	_, _ = c.SearchGround(context.Background(), okQuery())
	first := fake.got.InvocationId
	_, _ = c.SearchGround(context.Background(), okQuery())
	second := fake.got.InvocationId
	if first == "" || second == "" || first == second {
		t.Errorf("each call must mint a fresh invocation_id: first=%q second=%q", first, second)
	}
}

func TestGroundedSearch_ForwardsTraceparentFromContext(t *testing.T) {
	fake := &fakeGroundedGRPC{resp: twoCitationResp()}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx := WithTraceparent(context.Background(), tp)
	if _, err := c.SearchGround(ctx, okQuery()); err != nil {
		t.Fatalf("SearchGround err = %v", err)
	}
	if fake.got.Traceparent != tp {
		t.Errorf("traceparent = %q, want the W3C header from ctx", fake.got.Traceparent)
	}
}

func TestGroundedSearch_InvalidQueryDoesNotEgress(t *testing.T) {
	fake := &fakeGroundedGRPC{resp: twoCitationResp()}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	// A directionless / unbounded query is a fail-loud local reject — the gateway
	// egress must NEVER fire on it.
	_, err := c.SearchGround(context.Background(), grounded.Query{TenantID: "t", GCID: "g", Directive: "  ", MaxHits: 5})
	if err == nil {
		t.Fatal("blank directive must error before egress")
	}
	if fake.calls != 0 {
		t.Errorf("gateway called %d times on an invalid query; want 0", fake.calls)
	}
}

func TestGroundedSearch_EmptyActionCodeDoesNotEgress(t *testing.T) {
	fake := &fakeGroundedGRPC{resp: twoCitationResp()}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	// An un-priced egress (empty action_code) is fail-loud BEFORE the web hop —
	// the gateway rejects it too, but a mis-wired Seeker must never reach egress.
	q := okQuery()
	q.ActionCode = "  "
	_, err := c.SearchGround(context.Background(), q)
	if err == nil {
		t.Fatal("empty action_code must error before egress")
	}
	if fake.calls != 0 {
		t.Errorf("gateway called %d times on an un-priced query; want 0", fake.calls)
	}
}

func TestGroundedSearch_RPCErrorPropagatesLoud(t *testing.T) {
	sentinel := errors.New("gateway unavailable")
	fake := &fakeGroundedGRPC{err: sentinel}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	res, err := c.SearchGround(context.Background(), okQuery())
	if err == nil {
		t.Fatal("an RPC failure must surface loud (never a silent empty result)")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error must wrap the transport failure: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("no hits on failure; got %d", len(res.Hits))
	}
}

func TestGroundedSearch_EmptyCitationsIsEmptyResult(t *testing.T) {
	// A zero-citation grounded search (the honest empty state) maps to an empty
	// Result — the caller's citation mandate then hedges (never a nil-panic).
	fake := &fakeGroundedGRPC{resp: &mgv1.GroundedSearchResponse{Citations: nil}}
	c := NewGroundedSearchGatewayClientFromStub(fake, time.Second)

	res, err := c.SearchGround(context.Background(), okQuery())
	if err != nil {
		t.Fatalf("SearchGround err = %v", err)
	}
	if res.HasCitations() {
		t.Error("empty citations must not satisfy the mandate")
	}
}

func TestNewGroundedSearchGatewayClient_EmptyTargetFailsLoud(t *testing.T) {
	// feedback_no_inline_config: a missing gateway target fails loud rather than
	// silently degrading to a no-op (which would brick every Seeker use).
	if _, err := NewGroundedSearchGatewayClient("   ", time.Second); !errors.Is(err, ErrGroundedSearchEmptyTarget) {
		t.Errorf("empty target err = %v, want ErrGroundedSearchEmptyTarget", err)
	}
	// A real target dials lazily (grpc.NewClient does not eagerly connect).
	if _, err := NewGroundedSearchGatewayClient("chora-model-gateway.ai-kernel.svc.cluster.local:9090", 0); err != nil {
		t.Errorf("dial with a valid target errored: %v", err)
	}
}

// P5 Far Sight FE (CHO-2113 step 3): the Google-mandated Search-Suggestions chip
// HTML (searchEntryPoint.renderedContent) must survive the gateway→consumption
// hop so the Far Sight FE can render it (Google ToS display obligation, ADR-231 D5).
func TestGroundedSearch_MapsSearchEntryPointHTML(t *testing.T) {
	resp := twoCitationResp()
	resp.SearchEntryPointHtml = `<div class="container">Search suggestions chip</div>`
	c := NewGroundedSearchGatewayClientFromStub(&fakeGroundedGRPC{resp: resp}, time.Second)
	res, err := c.SearchGround(context.Background(), okQuery())
	if err != nil {
		t.Fatalf("SearchGround: %v", err)
	}
	if res.SearchEntryPointHTML != `<div class="container">Search suggestions chip</div>` {
		t.Fatalf("SearchEntryPointHTML = %q, want the Google chip HTML mapped through", res.SearchEntryPointHTML)
	}
}
