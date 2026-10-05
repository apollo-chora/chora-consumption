// exp_rules.go — ADR-218 D6: one EXP economy, resolved through identity's
// ExpRuleResolver (ADR-201, chora-identity migration 0027/0030 schema).
//
// The award path resolves (tenant, source) → {value, daily cap, enabled}
// via the ExpRuler port (production adapter: gRPC client to chora-identity
// ExpRuleService, TTL-cached). The historical in-code map in curve.go is
// DEMOTED to the documented fallback used ONLY when
//
//	(a) no resolver is wired (build-out / tests), or
//	(b) the resolver errors at award time — in which case the
//	    OnExpRuleFallback hook fires so the degradation is LOUD (logged +
//	    traced by the caller), never silent.
//
// Behaviour neutrality is guaranteed by the parity contract: identity
// migration 0030 seeds exp_source_def with EXACTLY the curve.go values for
// the live vocabulary, and both sides pin the shared literals in their own
// test suites (exp_rules_test.go here; the 0030 migration-content test in
// chora-identity). The verified-EXP invariant (L16/ADR-203) is untouched:
// sources remain non-configurable; only value/cap/enabled are tunable.
package growth

import (
	"context"
	"fmt"
)

// ExpRule is the resolved EXP rule for one occurrence of a verified source.
type ExpRule struct {
	// Value is the EXP awarded for one occurrence (used when the caller
	// does not supply an explicit delta).
	Value int
	// DailyCap is the max EXP per source per calendar day; 0 = uncapped.
	DailyCap int
	// Enabled=false means the source currently awards nothing — the award
	// is skipped (a successful resolution, not an error).
	Enabled bool
}

// ExpRuler resolves the EXP rule for a verified source. Implemented by the
// identity ExpRuleService gRPC adapter; nil when not wired.
type ExpRuler interface {
	ResolveExpRule(ctx context.Context, tenantID, source string) (ExpRule, error)
}

// FallbackExpRule returns the documented in-code fallback rule for a
// canonical source (the ADR-218 D6 parity values from the historical
// curve.go map). Unknown sources fail loud — the fallback can never mint a
// source (L16).
func FallbackExpRule(source string) (ExpRule, error) {
	if !IsValidSource(source) {
		return ExpRule{}, fmt.Errorf("%w: source %q", ErrInvalidSource, source)
	}
	return ExpRule{
		Value:    expPerEventBySource[source],
		DailyCap: dailyCapsBySource[source],
		Enabled:  true,
	}, nil
}

// resolveExpRuleOrFallback runs ExpRule resolution with the documented
// fallback semantics. source MUST already be validated canonical (the
// caller checks IsValidSource first, so the FallbackExpRule error path is
// structurally unreachable and swallowing it here is safe).
//
//   - ruler nil  → fallback silently (resolver simply not wired yet).
//   - ruler errs → fallback + fire onFallback (loud, ADR-218 D6).
//   - otherwise  → the resolver's rule.
func resolveExpRuleOrFallback(ctx context.Context, ruler ExpRuler, tenantID, source string, onFallback func(source string, err error)) ExpRule {
	fallback := func() ExpRule {
		rule, err := FallbackExpRule(source)
		if err != nil {
			// Unreachable by contract (source pre-validated); return a
			// disabled rule so a contract breach awards nothing rather
			// than something wrong.
			return ExpRule{Enabled: false}
		}
		return rule
	}
	if ruler == nil {
		return fallback()
	}
	rule, err := ruler.ResolveExpRule(ctx, tenantID, source)
	if err != nil {
		if onFallback != nil {
			onFallback(source, err)
		}
		return fallback()
	}
	return rule
}

// ClampDeltaWithCap applies an explicit resolved daily cap to a requested
// delta given the running count already awarded today. Returns
// (clampedDelta, capHit). cap <= 0 → no clamp (uncapped source). This is
// the resolver-era replacement for ClampDelta (which reads the in-code
// fallback map and remains only for documented-fallback callers).
func ClampDeltaWithCap(cap, alreadyAwardedToday, requestedDelta int) (int, bool) {
	if cap <= 0 {
		return requestedDelta, false
	}
	if alreadyAwardedToday >= cap {
		return 0, true
	}
	remaining := cap - alreadyAwardedToday
	if requestedDelta <= remaining {
		return requestedDelta, requestedDelta >= remaining
	}
	return remaining, true
}
