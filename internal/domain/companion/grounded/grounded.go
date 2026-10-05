// Package grounded holds the gateway grounded-search RESULT value types +
// the CITATION MANDATE primitives shared by the Seeker family (ADR-220 —
// Far Sight / fact_check / source_reader).
//
// ADR-220 D1 makes grounded search a GATEWAY surface, not an agent tool: the
// only web egress is chora-model-gateway (Armor + mana metering central), and
// the response carries STRUCTURED CITATIONS (url + title + snippet). This
// package models that result at the consumption domain boundary so the Seeker
// builders can enforce the mandate deterministically, independent of how the
// gateway surface is wired (the real GroundedSearchPort adapter is the P5
// gateway-vertical checkpoint).
//
// ADR-220 D3 (IMDA D2) — CITATION MANDATE: every grounded result renders its
// sources to the learner; a result whose citations are empty is DISCARDED
// fail-loud, never presented as companion knowledge. `HasCitations` /
// `CitedHits` are that mandate as pure predicates: a "hit" only counts as a
// citation when it carries BOTH a url and a title (you cannot render a source
// you cannot name or link).
package grounded

import (
	"fmt"
	"strings"
)

// Hit is one grounded-search result the gateway surface returns. URL + Title
// are the citation surface (rendered to the learner); Snippet is the grounded
// evidence a Seeker turn is fenced to reason over.
//
// Domain is the publisher domain (e.g. "nature.com"). Per ADR-231 D4 the URL is
// a Google grounding-api-redirect link that EXPIRES (~30 days), so Domain is the
// DURABLE citation surface: the web_research memory-note sink persists
// domain+title+snippet (never the ephemeral redirect uri), and the learner sees
// "domain — title". The citation mandate itself (IsCitation) still hinges on
// url+title — Domain never rescues an unnameable source.
type Hit struct {
	URL     string
	Title   string
	Snippet string
	Domain  string
}

// IsCitation reports whether the hit is a renderable citation — it must carry
// BOTH a non-blank url AND a non-blank title (a source you cannot name or link
// is not a citation, and must never back a companion claim).
func (h Hit) IsCitation() bool {
	return strings.TrimSpace(h.URL) != "" && strings.TrimSpace(h.Title) != ""
}

// trimmed returns a field-trimmed copy (the wire hit may carry padded fields).
func (h Hit) trimmed() Hit {
	return Hit{
		URL:     strings.TrimSpace(h.URL),
		Title:   strings.TrimSpace(h.Title),
		Snippet: strings.TrimSpace(h.Snippet),
		Domain:  strings.TrimSpace(h.Domain),
	}
}

// Result is a grounded-search response: the ordered hits the gateway surface
// returned for one directive.
type Result struct {
	Hits []Hit
	// SearchEntryPointHTML is the Google-mandated Search-Suggestions chip HTML
	// (searchEntryPoint.renderedContent) the gateway surface returns. The Far
	// Sight FE MUST render it verbatim when non-empty — the Google ToS display
	// obligation (ADR-231 D5). Empty for a non-grounded or zero-citation result
	// (nothing grounded was shown ⇒ no chip to attribute).
	SearchEntryPointHTML string
	// WebSearchQueries are the queries the model actually issued to Google
	// (Vertex webSearchQueries). They are the DURABLE half of grounded
	// transparency: the chip's links ride the same grounding-redirect
	// infrastructure that EXPIRES (~30 days, ADR-231 D4), but these are plain
	// strings that never expire — so they are what lets a persisted research note
	// still explain itself long after every chip link is dead.
	//
	// ⚠ TRANSPARENCY METADATA ONLY. The contract is explicit that they are
	// "never presented to the learner as knowledge": surface them as
	// "what I searched", NEVER as answer content (IMDA D2 / ADR-225).
	WebSearchQueries []string
}

// SearchQueries returns the issued web-search queries — trimmed, blanks dropped,
// de-duplicated (first-seen wins, order preserved). It is the durable
// "what I searched" surface, and the mirror of CitedHits: the wire value is
// vendor text, so it is normalised HERE once rather than in each adapter.
//
// It NEVER invents a query. An empty return is the honest "the vendor issued
// none" state — the same no-fabrication rule the chip already obeys.
func (r Result) SearchQueries() []string {
	seen := make(map[string]struct{}, len(r.WebSearchQueries))
	out := make([]string, 0, len(r.WebSearchQueries))
	for _, q := range r.WebSearchQueries {
		t := strings.TrimSpace(q)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// CitedHits returns the renderable citations in the result: only url+title
// hits survive, deduplicated by trimmed URL (first-seen wins), fields trimmed.
// The order of first appearance is preserved so the citations render in the
// gateway's relevance order.
func (r Result) CitedHits() []Hit {
	seen := make(map[string]struct{}, len(r.Hits))
	out := make([]Hit, 0, len(r.Hits))
	for _, h := range r.Hits {
		if !h.IsCitation() {
			continue
		}
		t := h.trimmed()
		if _, dup := seen[t.URL]; dup {
			continue
		}
		seen[t.URL] = struct{}{}
		out = append(out, t)
	}
	return out
}

// HasCitations is the citation mandate: true only when at least one renderable
// citation survives. A false here is the DISCARD signal (ADR-220 D3) — nothing
// grounded to present, so a Seeker must never assert a verdict/note as fact.
func (r Result) HasCitations() bool {
	return len(r.CitedHits()) > 0
}

// Query is the grounded-search request the Seeker builder hands the gateway
// surface: the (screened) directive + a bounded hit count. TenantID/GCID ride
// so the gateway meters + attributes the egress per tenant.
//
// ActionCode is the per-skill mana action the gateway debits for THIS grounded
// egress (ADR-231 D6, the sole meter per ADR-177): each Seeker carries its own
// price — fact_check `companion_skill_fact_check` (40) vs web_research
// `companion_skill_web_research` (80) — so the grounded egress preserves the
// spec-§2.4 differentiation rather than a flat rate. The gateway prices it from
// chora_identity.mana_action_pricing (mig 0031); an empty code is a fail-loud
// un-priced egress (rejected by the adapter and the gateway alike).
type Query struct {
	TenantID   string
	GCID       string
	Directive  string
	MaxHits    int
	ActionCode string
}

// Validate rejects a directionless or unbounded egress call fail-loud (a
// grounded search must always point somewhere and be capped).
func (q Query) Validate() error {
	if strings.TrimSpace(q.Directive) == "" {
		return fmt.Errorf("grounded: query directive is empty")
	}
	if q.MaxHits <= 0 {
		return fmt.Errorf("grounded: query MaxHits must be > 0, got %d", q.MaxHits)
	}
	return nil
}

// DenyReason classifies WHY the governed egress refused a grounded search.
//
// A deny is a DELIBERATE, CORRECT decision by the chokepoint (ADR-231 D6) — it
// is NOT an upstream failure, and the two must never be conflated. Reporting a
// deny as 5xx does three concrete harms, all observed live on 2026-07-14:
//   - it tells the learner to "try again" against a gate that is intentionally
//     shut, inviting an unbounded retry loop;
//   - chora-gateway normalises EVERY upstream 5xx to GATEWAY_UPSTREAM_5XX (2xx +
//     4xx pass through verbatim, by its documented contract), so the reason is
//     ERASED before A+ can render it — the surface cannot explain itself;
//   - an engaged O+ kill-switch then reads as a platform outage on the 5xx
//     dashboards, so the emergency stop is indistinguishable from a crash.
//
// Hence every deny below is a 4xx at the HTTP edge. Only a genuine upstream
// failure (vendor down, transport error, timeout) stays 5xx.
type DenyReason string

const (
	// DenyEgressOff — the tenant is not entitled to web egress (ADR-220 D4:
	// franchise default-deny is the ABSENCE of a policy row) or its budget is
	// exhausted. Actionable by the tenant admin (H+ → Web Access).
	DenyEgressOff DenyReason = "egress_off"
	// DenyKillSwitch — the platform-wide O+ kill-switch is engaged, which BEATS
	// tenant opt-in. Kept DISTINCT from DenyEgressOff on purpose: telling a
	// learner "ask your admin to enable it" when the admin already HAS, and the
	// platform paused it globally, is precisely the misdirection this type
	// exists to prevent (and would generate false support tickets).
	DenyKillSwitch DenyReason = "kill_switch"
	// DenyCeilingReached — the tenant's daily grounded-call ceiling is spent, or
	// the post-screen refused the web answer. Retryable LATER, not now.
	DenyCeilingReached DenyReason = "ceiling_reached"
	// DenyQueryBlocked — the Model Armor pre-screen refused the query itself.
	DenyQueryBlocked DenyReason = "query_blocked"
	// DenyInvalidQuery — the gateway rejected the query as malformed.
	DenyInvalidQuery DenyReason = "invalid_query"
)

// DeniedError is a GOVERNANCE refusal of the grounded egress, carried across the
// GroundedSearchPort as a typed domain error so the HTTP edge can map it to an
// honest status WITHOUT importing the gateway's transport (hexagonal: the client
// adapter owns the gRPC→domain translation; the HTTP adapter never sees a code).
//
// The gateway already audits the precise machine token (ADR-231 D6 audit event),
// so Detail is for logs/traces — never for the learner.
type DeniedError struct {
	Reason DenyReason
	Detail string
}

func (e *DeniedError) Error() string {
	if e == nil {
		return "grounded: egress denied"
	}
	if strings.TrimSpace(e.Detail) == "" {
		return fmt.Sprintf("grounded: egress denied (%s)", e.Reason)
	}
	return fmt.Sprintf("grounded: egress denied (%s): %s", e.Reason, e.Detail)
}
