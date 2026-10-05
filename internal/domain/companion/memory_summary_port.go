// memory_summary_port.go — OPTIONAL port for the F5 profile's
// `memory_summary` field.
//
// F4 (Vertex AI Memory Bank, per the multi-Companion two-tier RAG design —
// domain-content-consumption SKILL §"RAG memory (two-tier)") is being
// designed SEPARATELY by a peer. This batch ships ONLY the nil-safe port
// definition so the F5 profile endpoint can carry an optional
// `memory_summary` field that gracefully omits when Memory Bank is not wired.
//
// IMPORTANT (hexagonal + feedback_companion_vs_agent): this package is the
// PURE domain layer. It declares the port (interface) the profile handler
// depends on — it MUST NOT import any Vertex AI / Memory Bank SDK, mint any
// app_name, or carry prompt strings. The concrete adapter (a Vertex AI
// Memory Bank client scoped `app_name="companion:{companion_id}"`) lands with
// F4 in an adapter package; until then the handler leaves the port nil and
// omits the field.
package companion

import "context"

// MemorySummaryResolver resolves a short, human-readable conversational
// memory summary for a single Companion instance (per-Companion Memory Bank
// scope `companion:{companion_id}` + `user_id=owner_gcid`).
//
// Contract for callers:
//   - A nil resolver means Memory Bank is not wired → the profile OMITS the
//     `memory_summary` field entirely (it is optional).
//   - A non-nil resolver that returns ("", nil) means "no memory yet" → the
//     profile OMITS the field (empty summary is not rendered).
//   - A resolver error (Memory Bank unreachable / timeout) MUST be treated as
//     non-fatal by callers — memory is supplementary, never load-bearing for
//     the profile. Callers omit the field and continue (never 5xx).
type MemorySummaryResolver interface {
	// ResolveMemorySummary returns a short memory summary for the Companion.
	// Returning ("", nil) signals "no memory available yet".
	ResolveMemorySummary(ctx context.Context, tenantID, companionID, ownerGCID string) (string, error)
}
