// grounded_search_gateway_client.go — P5 Far Sight (CHO-2017, ADR-231 D7): the
// REAL consumption→chora-model-gateway GroundedSearch client that replaces the
// P5 nil-seam (503 SKILL_INVOKE_NOT_WIRED).
//
// GroundedSearchGatewayClient implements the http.GroundedSearchPort seam onto
// chora-model-gateway's grounded-search surface (ADR-220 D1 / ADR-231 D2: the
// ONLY controlled web egress; Cloud Model Armor screens + mana meters CENTRALLY
// at the gateway per ADR-177/152). Given a screened directive it returns
// STRUCTURED CITATIONS (uri + title + domain + snippet). The citation mandate,
// honest-hedge, and untrusted-source fence stay caller-side (the Seeker router)
// against the returned Result — this adapter is purely the wire hop.
//
// This is a NEW client, DISTINCT from CompanionEngineClient (which dials the GKE
// Companion agent). Grounding is a GATEWAY capability, so consumption dials the
// gateway mesh Service DIRECTLY (mTLS injected by the Cloud Service Mesh sidecar;
// the app dials plaintext, :9090). The dial shape mirrors mana_client.go /
// content_retrieval_client.go: insecure transport credentials + a mesh-DNS
// target from env CHORA_MODEL_GATEWAY_GRPC_URL (e.g.
// "chora-model-gateway.ai-kernel.svc.cluster.local:9090"). A new gRPC METHOD
// (GroundedSearch) needs a mesh AuthorizationPolicy allow-list entry for the
// consumption principal + a caller restart to take effect.
//
// Metering (ADR-231 D6): the gateway is the SOLE meter (ADR-177). This client
// stamps the caller-supplied per-skill external_egress action_code
// (grounded.Query.ActionCode — fact_check `companion_skill_fact_check` 40,
// web_research `companion_skill_web_research` 80; the grounded egress keeps each
// Seeker's own price) and a per-call UUIDv7 invocation_id so a retried Seeker turn
// collapses to ONE ledger row + ONE debit at the gateway. An empty action_code is
// a fail-loud un-priced egress (rejected here, mirroring the gateway).
//
// Per feedback_no_inline_config the gRPC target MUST be supplied at construction
// (read in router.go boot wiring). An empty target fails loud.
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/grounded"
)

// ErrGroundedSearchEmptyTarget is returned by the constructor when the gRPC
// target is empty, and by SearchGround when the client is unconfigured. Per
// feedback_no_inline_config, missing config MUST fail loudly rather than degrade
// to a no-op (which would brick every Seeker use as an ungrounded turn).
var ErrGroundedSearchEmptyTarget = errors.New("grounded_search_gateway_client: target empty (set CHORA_MODEL_GATEWAY_GRPC_URL)")

const (
	// groundedSearchDefaultTimeout bounds one grounded-search round-trip. It is
	// generous: the gateway runs a Vertex "Grounding with Google Search" call
	// (real-time web search + a synthesised, Armor-screened completion), slower
	// than a plain RPC.
	groundedSearchDefaultTimeout = 25 * time.Second

	// groundedSearchAgentID / groundedSearchCrewKind — the calling Seeker identity
	// stamped on the egress for attribution + the audit trail (ADR-231 D7).
	groundedSearchAgentID  = "companion_seeker"
	groundedSearchCrewKind = "companion"
)

// groundedSearchGRPCClient is the minimal slice of
// mgv1.ModelGatewayServiceClient the adapter calls (GroundedSearch only). The
// generated client satisfies it; tests inject a fake.
type groundedSearchGRPCClient interface {
	GroundedSearch(ctx context.Context, in *mgv1.GroundedSearchRequest, opts ...grpc.CallOption) (*mgv1.GroundedSearchResponse, error)
}

// GroundedSearchGatewayClient calls chora-model-gateway's GroundedSearch RPC.
// Its public surface implements http.GroundedSearchPort (the seam is asserted at
// the router.go wiring site — the http package imports clients, so this package
// cannot import it back without a cycle).
type GroundedSearchGatewayClient struct {
	client  groundedSearchGRPCClient
	timeout time.Duration
}

// NewGroundedSearchGatewayClient dials the gateway ModelGatewayService at the
// mesh target and returns a GroundedSearchPort. An empty target fails loud
// (ErrGroundedSearchEmptyTarget); a non-positive timeout falls back to
// groundedSearchDefaultTimeout. grpc.NewClient is lazy (no eager connection) so a
// healthy return does not guarantee reachability — the first RPC surfaces an
// Unavailable error, which the Seeker router turns into a loud search-failure.
func NewGroundedSearchGatewayClient(target string, timeout time.Duration) (*GroundedSearchGatewayClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrGroundedSearchEmptyTarget
	}
	if timeout <= 0 {
		timeout = groundedSearchDefaultTimeout
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("grounded_search_gateway_client: dial %q: %w", target, err)
	}
	return &GroundedSearchGatewayClient{client: mgv1.NewModelGatewayServiceClient(conn), timeout: timeout}, nil
}

// NewGroundedSearchGatewayClientFromStub injects a fake gRPC client (tests). A
// non-positive timeout falls back to groundedSearchDefaultTimeout.
func NewGroundedSearchGatewayClientFromStub(stub groundedSearchGRPCClient, timeout time.Duration) *GroundedSearchGatewayClient {
	if timeout <= 0 {
		timeout = groundedSearchDefaultTimeout
	}
	return &GroundedSearchGatewayClient{client: stub, timeout: timeout}
}

// SearchGround implements http.GroundedSearchPort. It maps the domain
// grounded.Query onto the gateway GroundedSearchRequest (caller identity, the
// external_egress action_code, a per-call UUIDv7 invocation_id for idempotency,
// and the W3C traceparent from ctx), invokes the RPC, and maps the structured
// citations back to grounded.Hit (uri→URL, title→Title, snippet→Snippet,
// domain→Domain per ADR-231 D4). The query is validated first so a directionless
// or unbounded egress call fails loud BEFORE the web hop.
func (c *GroundedSearchGatewayClient) SearchGround(ctx context.Context, q grounded.Query) (grounded.Result, error) {
	if c == nil || c.client == nil {
		return grounded.Result{}, ErrGroundedSearchEmptyTarget
	}
	if err := q.Validate(); err != nil {
		return grounded.Result{}, fmt.Errorf("grounded_search_gateway_client: %w", err)
	}
	// The gateway rejects an un-priced egress (action_code required); enforce it
	// here too so a mis-wired Seeker fails loud BEFORE the web hop rather than as an
	// opaque gateway InvalidArgument.
	if strings.TrimSpace(q.ActionCode) == "" {
		return grounded.Result{}, fmt.Errorf("grounded_search_gateway_client: query ActionCode is empty (un-priced egress not permitted)")
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &mgv1.GroundedSearchRequest{
		InvocationId: domain.NewUUIDv7(),
		TenantId:     q.TenantID,
		Gcid:         q.GCID,
		AgentId:      groundedSearchAgentID,
		CrewKind:     groundedSearchCrewKind,
		Directive:    q.Directive,
		MaxResults:   int32(q.MaxHits),
		// ADR-231 D6: the SOLE meter. The gateway debits this per-skill
		// external_egress action ONCE per grounded call (each Seeker keeps its own
		// spec-§2.4 price); the fenced verify/research turn is un-metered so a Seeker
		// use is not charged twice.
		ActionCode:  q.ActionCode,
		Traceparent: TraceparentFromContext(ctx),
	}

	resp, err := c.client.GroundedSearch(callCtx, req)
	if err != nil {
		// A GOVERNANCE deny is not an upstream failure — type it so the HTTP edge
		// can answer 4xx (see groundedDenyFromRPC).
		if deny := groundedDenyFromRPC(err); deny != nil {
			return grounded.Result{}, deny
		}
		return grounded.Result{}, fmt.Errorf("grounded_search_gateway_client: GroundedSearch rpc: %w", err)
	}
	if resp == nil {
		return grounded.Result{}, fmt.Errorf("grounded_search_gateway_client: nil GroundedSearch response")
	}

	cits := resp.GetCitations()
	hits := make([]grounded.Hit, 0, len(cits))
	for _, cit := range cits {
		if cit == nil {
			continue
		}
		hits = append(hits, grounded.Hit{
			URL:     cit.GetUri(),
			Title:   cit.GetTitle(),
			Snippet: cit.GetSnippet(),
			Domain:  cit.GetDomain(),
		})
	}
	// Both transparency channels ride through, and they are NOT interchangeable
	// (ADR-231 D4/D5):
	//   - SearchEntryPointHTML — Google's Search-Suggestions chip, rendered
	//     verbatim wherever a LIVE grounded result is shown (the ToS display
	//     obligation). Its links expire (~30 days), so it is EPHEMERAL.
	//   - WebSearchQueries — the queries the model actually issued. Plain strings
	//     that never expire, so they are the DURABLE record a persisted research
	//     note carries to explain itself once the chip's links are dead.
	// Dropping the queries here (as this adapter did until CHO-2179) silently
	// costs the platform its only durable "what I searched" evidence.
	return grounded.Result{
		Hits:                 hits,
		SearchEntryPointHTML: resp.GetSearchEntryPointHtml(),
		WebSearchQueries:     resp.GetWebSearchQueries(),
	}, nil
}

// groundedDenyFromRPC translates chora-model-gateway's documented GroundedSearch
// status contract (its mapGroundedError) into a TYPED domain deny, or nil when
// the error is a genuine upstream failure.
//
// This adapter is the only place that translation may live: the HTTP edge maps
// the deny to a status WITHOUT importing grpc/codes (hexagonal — the domain and
// the inbound adapter never learn an outbound transport).
//
// Why it matters (live-caught 2026-07-14, CHO-2148): every grounded error used
// to collapse into 502. chora-gateway normalises EVERY upstream 5xx to
// GATEWAY_UPSTREAM_5XX (2xx + 4xx pass through verbatim, by its documented
// contract), so the deny reason was ERASED before A+ could render it — an
// engaged kill-switch reached the learner as "please try again", and looked like
// a platform outage on the 5xx SLOs. A deny is a correct decision, so it is 4xx.
//
// The gateway carries the machine token in the status message
// ("grounded_search <reason>: <detail>" — GroundedSearchError.Error()). Both
// kill_switch_engaged and external_egress_disabled arrive as FailedPrecondition,
// so the token is what separates "the platform paused this" from "your admin has
// not enabled it" — two DIFFERENT things to tell a learner.
func groundedDenyFromRPC(err error) *grounded.DeniedError {
	st, ok := status.FromError(err)
	if !ok {
		return nil // not a gateway status → a transport/plain error, i.e. a real failure
	}
	msg := st.Message()
	switch st.Code() {
	case codes.FailedPrecondition:
		// kill-switch (platform stop, beats tenant opt-in) vs not-entitled/budget.
		// An unparseable token is contract drift: fall back to the tenant-actionable
		// reason and keep the raw message in Detail for the trace.
		if groundedReasonToken(msg) == gatewayReasonKillSwitch {
			return &grounded.DeniedError{Reason: grounded.DenyKillSwitch, Detail: msg}
		}
		return &grounded.DeniedError{Reason: grounded.DenyEgressOff, Detail: msg}
	case codes.ResourceExhausted:
		return &grounded.DeniedError{Reason: grounded.DenyCeilingReached, Detail: msg}
	case codes.PermissionDenied:
		return &grounded.DeniedError{Reason: grounded.DenyQueryBlocked, Detail: msg}
	case codes.InvalidArgument:
		return &grounded.DeniedError{Reason: grounded.DenyInvalidQuery, Detail: msg}
	default:
		// Unavailable (vendor down) / Internal (gateway misconfigured) / Unknown —
		// a genuine upstream failure. Stays 5xx; never dressed up as a decision.
		return nil
	}
}

// gatewayReasonKillSwitch is chora-model-gateway's EgressDenyKillSwitch machine
// token (domain/grounded.go). Matched as a token, not a substring of prose.
const gatewayReasonKillSwitch = "kill_switch_engaged"

// groundedReasonToken pulls the machine token out of a gateway status message of
// the form "grounded_search <reason>: <detail>". Returns "" when absent.
func groundedReasonToken(msg string) string {
	const prefix = "grounded_search "
	rest, ok := strings.CutPrefix(strings.TrimSpace(msg), prefix)
	if !ok {
		return ""
	}
	token, _, _ := strings.Cut(rest, ":")
	return strings.TrimSpace(token)
}
