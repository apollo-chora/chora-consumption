// chat_session.go — ADR-154 conversational chat value object.
//
// ChatSession is an immutable value object representing one long-lived
// ADK session bound to (owner_gcid, companion_id). Per ADR-154 D2 there
// is AT MOST ONE open session per pair at any time (enforced at the
// pg index level: `UNIQUE (owner_gcid, companion_id) WHERE closed_at IS NULL`).
//
// RecordTurn returns a NEW value with TurnCount + ManaChargedTotal + LastUsedAt
// updated; the source value is never mutated. This keeps the domain free of
// hidden side effects + makes concurrent reads safe under PgBouncer
// transaction-pooling.
package companion

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrChatSessionInvalid is returned when NewChatSession is called with
// missing or malformed input. The caller-facing message names the offending
// field for ops triage.
var ErrChatSessionInvalid = errors.New("companion: chat_session invalid input")

// ChatSession is the immutable per-(gcid, companion_id) chat-session value.
//
// Persistence is handled by the CompanionChatSessionRepo port; this struct
// is the pure-domain shape and has no I/O or DB knowledge.
type ChatSession struct {
	ID               string
	TenantID         string
	OwnerGCID        string
	CompanionID      string
	EngineSessionID  string
	ManaChargedTotal int
	TurnCount        int
	CreatedAt        time.Time
	LastUsedAt       time.Time
	ClosedAt         *time.Time
}

// NewChatSessionInput carries the fields required to mint a fresh ChatSession.
// All non-time fields are required; Now defaults to time.Now().UTC() when zero.
type NewChatSessionInput struct {
	ID              string
	TenantID        string
	OwnerGCID       string
	CompanionID     string
	EngineSessionID string
	Now             time.Time
}

// NewChatSession constructs a fresh ChatSession at TurnCount=0,
// ManaChargedTotal=0, ClosedAt=nil.
//
// Returns ErrChatSessionInvalid wrapped with the offending field name when
// any required field is blank/whitespace-only.
func NewChatSession(in NewChatSessionInput) (ChatSession, error) {
	if strings.TrimSpace(in.ID) == "" {
		return ChatSession{}, fmt.Errorf("%w: id required", ErrChatSessionInvalid)
	}
	if strings.TrimSpace(in.TenantID) == "" {
		return ChatSession{}, fmt.Errorf("%w: tenant_id required", ErrChatSessionInvalid)
	}
	if strings.TrimSpace(in.OwnerGCID) == "" {
		return ChatSession{}, fmt.Errorf("%w: owner_gcid required", ErrChatSessionInvalid)
	}
	if strings.TrimSpace(in.CompanionID) == "" {
		return ChatSession{}, fmt.Errorf("%w: companion_id required", ErrChatSessionInvalid)
	}
	if strings.TrimSpace(in.EngineSessionID) == "" {
		return ChatSession{}, fmt.Errorf("%w: engine_session_id required", ErrChatSessionInvalid)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return ChatSession{
		ID:               in.ID,
		TenantID:         in.TenantID,
		OwnerGCID:        in.OwnerGCID,
		CompanionID:      in.CompanionID,
		EngineSessionID:  in.EngineSessionID,
		TurnCount:        0,
		ManaChargedTotal: 0,
		CreatedAt:        now,
		LastUsedAt:       now,
		ClosedAt:         nil,
	}, nil
}

// RecordTurn returns a new ChatSession value with TurnCount + ManaChargedTotal
// incremented and LastUsedAt stamped to `at`. The source value is unchanged
// (value-type immutability).
//
// Negative manaCharged is clamped to zero — the running total is monotonic
// (a malformed engine response or accounting bug MUST NOT produce a negative
// ledger). TurnCount still increments because the turn DID complete.
func (s ChatSession) RecordTurn(manaCharged int, at time.Time) ChatSession {
	if manaCharged < 0 {
		manaCharged = 0
	}
	next := s
	next.TurnCount = s.TurnCount + 1
	next.ManaChargedTotal = s.ManaChargedTotal + manaCharged
	next.LastUsedAt = at
	return next
}

// IsOpen returns true when the session is not yet closed.
func (s ChatSession) IsOpen() bool {
	return s.ClosedAt == nil
}

// Close returns a new ChatSession with ClosedAt set to `at`. The source
// value is unchanged.
func (s ChatSession) Close(at time.Time) ChatSession {
	next := s
	next.ClosedAt = &at
	return next
}
