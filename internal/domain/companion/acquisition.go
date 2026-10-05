// acquisition.go — how a learner acquired the Companion for a map (the egg/hatch
// ceremony). Extracted from the retired CompanionMapBinding aggregate (WS-A3, My
// Knowledge unification — the Goal is now the single source of truth for the
// map↔Companion link, ADR-214 D1). The mode is request/response-level only (it is
// no longer persisted on a binding row); it distinguishes the paid ceremony from
// the free/dev path for the acquire API.
package companion

// AcquisitionMode records HOW a map's Companion was acquired.
type AcquisitionMode string

const (
	// AcquisitionHatched — the full egg/hatch ceremony (Stripe-gated in prod).
	AcquisitionHatched AcquisitionMode = "hatched"
	// AcquisitionDevHatched — the free/dev hatch path: a Companion acquired for a
	// map WITHOUT the live Stripe ceremony, so the FE has realistic test data (and
	// free onboarding is honest, not a stub).
	AcquisitionDevHatched AcquisitionMode = "dev_hatched"
)

// Valid reports whether m is a recognised acquisition mode.
func (m AcquisitionMode) Valid() bool {
	switch m {
	case AcquisitionHatched, AcquisitionDevHatched:
		return true
	}
	return false
}
