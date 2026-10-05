// Package companion — Mana-metered LLM greeting hook (S5.1).
//
// The Companion daily-dose surface optionally renders an LLM-personalised
// greeting line ("Hi Maya, you're crushing your Scrum series today!").
// Per ADR-142 §4 this is a medium-tier mana action costing 50 mana per
// call, action_code = "companion_greeting".
//
// Hexagonal contract:
//
//	GreetingPort       = port (interface) — the production adapter is
//	                     a Model Broker Gateway HTTP client wired in
//	                     internal/adapter/clients (S5.1 scaffold; real
//	                     wiring lands later).
//	GreetingRequest    = port DTO (in)
//	PersonalisedGreeting = pure-domain composer that orchestrates
//	                     mana check → port call → fallback.
//
// CRITICAL: per memory `feedback_companion_vs_agent` the Companion entity
// is a domain construct — NO LLM imports. This file imports ONLY stdlib
// + the domain ManaQuoter port. The Gateway adapter that satisfies
// GreetingPort lives outside the domain package.
//
// Behaviour matrix:
//
//	port == nil                   → DefaultGreeting, no mana spent
//	port != nil, quoter == nil    → call port directly (fail-open)
//	port != nil, quoter sufficient → debit mana → call port
//	port + quoter, insufficient   → DefaultGreeting (greeting is
//	                                non-critical — gracefully degrade,
//	                                NOT an error)
//	port + quoter, port error     → DefaultGreeting (fail-soft)
//
// Determinism: the fallback string is built deterministically from the
// companion name + level so tests can assert on output without time noise.
package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ActionCompanionGreeting is the mana action_code for the LLM-personalised
// greeting per ADR-142 §4.
const ActionCompanionGreeting = "companion_greeting"

// GreetingInput is the surface-supplied context the composer needs to
// build a request.
type GreetingInput struct {
	TenantID        string
	LearnerGCID     string
	CompanionName   string
	Level           int
	ActivePathTitle string // optional — empty if learner has no active path
}

// GreetingRequest is the port DTO sent to the LLM Gateway. Mirrors the
// shape Gateway expects without leaking HTTP / proto types into the
// domain package.
type GreetingRequest struct {
	TenantID        string
	LearnerGCID     string
	CompanionName   string
	Level           int
	ActivePathTitle string
}

// GreetingPort is the hexagonal port the composer calls to fetch a
// personalised greeting from the Model Broker Gateway. Production wiring
// is the chora-ai-kernel-orchestrator HTTP client; tests inject a fake.
type GreetingPort interface {
	Greet(ctx context.Context, req GreetingRequest) (string, error)
}

// PersonalisedGreeting orchestrates mana metering + greeting fetch.
// Returns the greeting text (LLM or deterministic fallback) plus an
// error ONLY for unrecoverable issues (validation). Mana shortage and
// port errors are handled by falling back to DefaultGreeting (greeting
// is a non-critical surface).
func PersonalisedGreeting(
	ctx context.Context,
	in GreetingInput,
	port GreetingPort,
	quoter ManaQuoter,
	idempotencyKey string,
) (string, error) {
	if strings.TrimSpace(in.CompanionName) == "" {
		// Defensive — surface should always pass a name. If empty,
		// substitute a generic word so output is still graceful.
		in.CompanionName = "Companion"
	}

	// No port wired → deterministic fallback. No mana spent.
	if port == nil {
		return DefaultGreeting(in), nil
	}

	// Port wired + quoter wired → pre-flight mana debit.
	if quoter != nil {
		err := quoter.DeductMana(ctx, DeductManaInput{
			GCID:           in.LearnerGCID,
			TenantID:       in.TenantID,
			ActionCode:     ActionCompanionGreeting,
			Units:          0, // server-resolved per action_code
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			// Insufficient mana — gracefully degrade. Greeting is non-critical.
			if errors.Is(err, ErrInsufficientManaSentinel) {
				return DefaultGreeting(in), nil
			}
			// Unknown mana error — fail-open to default (don't 5xx the page
			// on a metering glitch; surface logs the audit elsewhere).
			return DefaultGreeting(in), nil
		}
	}

	greeting, err := port.Greet(ctx, GreetingRequest{
		TenantID:        in.TenantID,
		LearnerGCID:     in.LearnerGCID,
		CompanionName:   in.CompanionName,
		Level:           in.Level,
		ActivePathTitle: in.ActivePathTitle,
	})
	if err != nil || strings.TrimSpace(greeting) == "" {
		return DefaultGreeting(in), nil
	}
	return greeting, nil
}

// DefaultGreeting returns the deterministic fallback greeting. Built
// from companion name + level so the same inputs always produce the same
// string (no time-based randomness). Used as the LLM-disabled fallback
// AND when mana metering / port calls fail.
func DefaultGreeting(in GreetingInput) string {
	if strings.TrimSpace(in.CompanionName) == "" {
		in.CompanionName = "Companion"
	}
	if in.Level <= 0 {
		return fmt.Sprintf("Hi! %s is here. Ready for today's Daily Dose?", in.CompanionName)
	}
	return fmt.Sprintf("Hi! %s (level %d) is here. Ready for today's Daily Dose?",
		in.CompanionName, in.Level)
}
