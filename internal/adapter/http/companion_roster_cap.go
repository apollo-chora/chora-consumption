package http

// companion_roster_cap.go: ONE roster cap, projected and enforced from the same
// call (UX Track U, C1a follow-up).
//
// C3's roster renders "2 of N". A wrong N is worse than no N: it either tells a
// learner they cannot hold a companion they have paid for, or offers one the
// create handler will refuse with a 409. So the number the list SERVES and the
// number the create handler ENFORCES come from one function, the shape B6's
// BudgetFor established for the practice budget.
//
// ⚠ WHAT cap_source CAN HONESTLY SAY TODAY.
//
// Nothing in this service reads tenant_entitlements. The create handler
// hardcoded companion.DefaultMaxCompanionsPerUser behind a comment naming an
// M12.3 follow-up, and a search for max_companions_per_user across the service
// finds only prose describing the intent. So the only source that exists is the
// default, and "default" is the only value emitted.
//
// capSourceEntitlement is NAMED so the wire shape does not change on the day
// that read lands, and deliberately has no branch that could produce it. A
// branch over a store nobody wired would be speculative wiring, and worse, a
// cap_source of "entitlement" nothing verified is a provenance claim an O+
// reader could not tell from a real one.

import "github.com/apollo-chora/chora-consumption/internal/domain/companion"

// Roster cap provenance, served beside the cap so the number can be explained.
const (
	// capSourceDefault: the free-tier fallback constant.
	capSourceDefault = "default"
	// capSourceEntitlement: a tenant entitlement override. RESERVED. Nothing
	// emits this yet, because nothing reads tenant_entitlements. Named here so
	// the contract and the frontend do not change when M12.3 lands.
	capSourceEntitlement = "entitlement"
)

// rosterCap is the cap to SERVE, with its provenance.
//
// It calls enforcedRosterCap rather than restating the value, so the served and
// enforced numbers are the same number and not two that happen to agree today.
func (s *Server) rosterCap() (int, string) {
	return s.enforcedRosterCap(), capSourceDefault
}

// enforcedRosterCap is the cap the create handler PASSES to the repository,
// which refuses the insert past it with ErrRosterCapReached.
//
// The single definition. Everything that states a roster cap anywhere in this
// service goes through here: the create enforcement, the list projection and
// the profile's roster_context. Three call sites used to state the constant
// independently, which is three chances for a later edit to move one and leave
// the learner reading a number nothing enforces.
func (s *Server) enforcedRosterCap() int {
	// When the M12.3 entitlement read lands it belongs HERE, so the enforcement
	// and the projection gain it in the same breath and cannot disagree even
	// for one commit.
	return companion.DefaultMaxCompanionsPerUser
}
