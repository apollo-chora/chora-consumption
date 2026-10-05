// awakening.go — Phase 3 (CHO-2235, ADR-228 Amendment A1): the species→
// awakening-class taxonomy from docs/design/companion-art-brief.md §2. The
// class changes the reveal METAPHOR, never the ceremony steps (owner
// decision 2026-07-16 #1): avian/mythic/reptile HATCH, mammals WAKE, and
// machines POWER-ON — the style anchor forbids egg imagery for mammals and
// machines. The growth domain owns the map (beside the species roll
// authority in breed.go, which the sovereign lane now derives from too) and
// the reveal
// response carries it, so the FE flavours copy + art without ever
// hardcoding the taxonomy. Code identifiers elsewhere stay hatch/egg per
// the companion-lingo rule — this file names the *reveal classes*, which
// are themselves domain vocabulary.
package growth

// Awakening classes — the three reveal metaphors (art brief §4).
const (
	AwakeningHatch   = "hatch"    // avian / mythic / reptile: the pod parts like a shell
	AwakeningWake    = "wake"     // mammal: the pod blooms open, first breath
	AwakeningPowerOn = "power_on" // machine: the capsule slides open, systems online
)

// AwakeningClassBySpecies is the art brief §2 roster. It must stay TOTAL
// over every species the platform can roll (CanonicalSpecies) or store
// (migration 0046 CHECK) — awakening_test.go fails loud on drift. robot is
// mapped ahead of being storable (the brief's flagship "Cog"; the 0046 DB
// CHECK and CanonicalSpecies still exclude it, see the tracker's open item) so the
// class exists the moment storage lands.
var AwakeningClassBySpecies = map[string]string{
	"owl":     AwakeningHatch, // avian
	"raven":   AwakeningHatch, // avian
	"penguin": AwakeningHatch, // avian
	"dragon":  AwakeningHatch, // mythic
	"phoenix": AwakeningHatch, // mythic
	"turtle":  AwakeningHatch, // reptile
	"fox":     AwakeningWake,  // mammal
	"cat":     AwakeningWake,  // mammal
	"wolf":    AwakeningWake,  // mammal
	"robot":   AwakeningPowerOn,
}

// AwakeningClass resolves a species to its reveal class. Unknown or empty
// species read as the universal HATCH metaphor — a documented presentation
// fallback, not a data path: the totality test guarantees every storable
// species resolves explicitly, so this arm only fires for a species that
// predates its own class mapping.
func AwakeningClass(species string) string {
	if class, ok := AwakeningClassBySpecies[species]; ok {
		return class
	}
	return AwakeningHatch
}
