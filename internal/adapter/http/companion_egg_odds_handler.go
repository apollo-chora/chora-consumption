// companion_egg_odds_handler.go: the EFFECTIVE per-learner egg odds surface.
//
//	GET /v1/me/companions/{id}/egg-odds
//
// Owner ruling 2026-08-07 inverted the one-species directive: a reveal must not
// hand back a companion type the learner already owns while an unseen one
// remains. growth.ExcludeOwnedSpecies subtracts the owned species from the
// SKU's declared table and renormalises the survivors to 100, and RevealBreed
// rolls THAT pool.
//
// That left the published odds lying. chora-tenancy's PreviewEggOdds returns
// the DECLARED SKU table, which for a learner who already owns fox still
// advertises fox at its declared weight when fox is impossible for them.
// Declared stopped equalling observed, which is precisely the quantity the
// chora-observability chi-square fairness audit reads (IMDA D2).
//
// WHY THIS LIVES HERE AND NOT IN TENANCY. The owned set is in
// chora_consumption.companion_instances and cross-DB queries are forbidden, so
// chora-tenancy structurally cannot compute it: PreviewEggOdds keeps returning
// the declared table and is not wrong to. chora-consumption already fetches
// that table over the BreedDistributionProvider gRPC seam and reads the owned
// set locally, so it is the only place both halves exist.
//
// ONE implementation, not two. The effective table is produced by the SAME
// growth.ExcludeOwnedSpecies call the reveal rolls, so declared equals observed
// by construction rather than by two copies of the arithmetic agreeing. There
// is deliberately no renormalisation in this file.
//
// BOTH numbers are published. Silently swapping the table for a different one
// is worse than either alone: the response carries the declared odds, the
// effective odds, and the names of the species excluded, so the learner can see
// what changed and why.
//
// Learner-scoped: tenant + GCID come from the validated session headers, never
// from the body or query, and the pod's owner is checked before anything is
// read. Another learner's pod is reported not-found, never forbidden: the same
// answer RevealBreed gives, leaking no existence.
package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

const (
	eggOddsPathPrefix = "/v1/me/companions/"
	eggOddsPathSuffix = "/egg-odds"
)

// EggOddsGrowthReader is the narrow read the odds surface needs. Satisfied in
// production by growth.Repository (both the pg and in-memory implementations);
// the handler has no business with the rest of that port.
type EggOddsGrowthReader interface {
	// GetGrowthRow returns the pod's growth snapshot, carrying the egg SKU and
	// the owner GCID. Returns growth.ErrCompanionNotFound on miss.
	GetGrowthRow(ctx context.Context, tenantID, companionID string) (*growth.CompanionGrowthRow, error)

	// OwnerSpeciesSet returns the species of the learner's other active
	// Companions. excludeCompanionID drops the pod being previewed, exactly as
	// RevealBreed scopes it.
	OwnerSpeciesSet(ctx context.Context, tenantID, ownerGCID, excludeCompanionID string) (map[string]bool, error)
}

// CompanionEggOddsHandler serves GET /v1/me/companions/{id}/egg-odds.
//
// Both collaborators are ESSENTIAL. A nil one yields 503, never a partial
// answer: publishing the declared table alone under an "effective" name is the
// exact inconsistency this endpoint exists to close.
type CompanionEggOddsHandler struct {
	Growth EggOddsGrowthReader
	Dist   growth.BreedDistributionProvider
}

// NewCompanionEggOddsHandler constructs the handler from its collaborators.
func NewCompanionEggOddsHandler(g EggOddsGrowthReader, dist growth.BreedDistributionProvider) *CompanionEggOddsHandler {
	return &CompanionEggOddsHandler{Growth: g, Dist: dist}
}

var _ http.Handler = (*CompanionEggOddsHandler)(nil)

// eggOddsWeightDTO is one row of a probability table.
//
// snake_case, matching the reveal response on this same subtree
// (rolled_probability / already_revealed). The effective table is literally the
// pre-image of that roll, so the two should read as one pair on the wire.
type eggOddsWeightDTO struct {
	Species     string  `json:"species"`
	Probability float64 `json:"probability"`
	Rarity      string  `json:"rarity"`
}

type eggOddsResp struct {
	CompanionID string `json:"companion_id"`
	EggSKU      string `json:"egg_sku"`

	// DeclaredOdds is the SKU table exactly as chora-tenancy publishes it,
	// untouched. This is what every learner shares.
	DeclaredOdds []eggOddsWeightDTO `json:"declared_odds"`

	// EffectiveOdds is the pool THIS learner's next reveal actually rolls.
	EffectiveOdds []eggOddsWeightDTO `json:"effective_odds"`

	// ExcludedSpecies names the declared species removed because the learner
	// already owns them, in declared order. Empty when nothing was removed,
	// including on wrap.
	ExcludedSpecies []string `json:"excluded_species"`

	// AllSpeciesOwned is the wrap signal: every species this SKU offers is
	// already on the roster, so nothing can be excluded and the effective odds
	// are the declared odds. It explains an empty ExcludedSpecies that would
	// otherwise look identical to "you own nothing".
	AllSpeciesOwned bool `json:"all_species_owned"`
}

func (h *CompanionEggOddsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	if h == nil || h.Growth == nil || h.Dist == nil {
		writeError(w, http.StatusServiceUnavailable, "EGG_ODDS_NOT_WIRED",
			"effective egg odds unavailable: growth repository or breed-distribution provider not wired")
		return
	}
	companionID := eggOddsCompanionIDFromRequest(r)
	if companionID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ID", "companion_id required")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := rlsCtx(r, tenantID, gcid)

	row, err := h.Growth.GetGrowthRow(ctx, tenantID, companionID)
	if err != nil {
		if errors.Is(err, growth.ErrCompanionNotFound) {
			writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "GROWTH_READ_FAILED", err.Error())
		return
	}
	// Owner guard before any odds are computed. Mirrors RevealBreed: an owner
	// mismatch is ErrCompanionNotFound, not a 403.
	if row == nil || row.OwnerGCID != gcid {
		writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
		return
	}

	declared, err := h.Dist.Lookup(tenantID, row.EggSku)
	if err != nil {
		if errors.Is(err, growth.ErrSkuNotFound) {
			writeError(w, http.StatusNotFound, "EGG_SKU_NOT_FOUND", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "EGG_SKU_LOOKUP_FAILED", err.Error())
		return
	}
	// Validate the DECLARED table first, before any exclusion. Renormalisation
	// rescales survivors to 100, so a malformed SKU (a sum-20 table) would be
	// laundered into a healthy-looking sum-100 one and the misconfiguration
	// would reach the learner as a confident set of odds. RevealBreed refuses
	// such a SKU; refusing identically here keeps the two surfaces honest about
	// the same input.
	if err := growth.ValidateBreedDistribution(declared); err != nil {
		writeError(w, http.StatusInternalServerError, "EGG_SKU_DISTRIBUTION_INVALID",
			"declared distribution for "+row.EggSku+" is unrollable: "+err.Error())
		return
	}

	owned, err := h.Growth.OwnerSpeciesSet(ctx, tenantID, gcid, companionID)
	if err != nil {
		// Fail loud. Degrading to the declared table would publish odds the
		// reveal will not roll, which is the very gap being closed.
		writeError(w, http.StatusInternalServerError, "OWNED_SPECIES_READ_FAILED", err.Error())
		return
	}

	// The one and only exclusion, shared with the roll.
	effective := growth.ExcludeOwnedSpecies(declared, owned)

	writeJSON(w, http.StatusOK, eggOddsResp{
		CompanionID:     companionID,
		EggSKU:          row.EggSku,
		DeclaredOdds:    toEggOddsDTO(declared),
		EffectiveOdds:   toEggOddsDTO(effective),
		ExcludedSpecies: excludedSpecies(declared, effective),
		AllSpeciesOwned: allDeclaredOwned(declared, owned),
	})
}

// excludedSpecies is DERIVED from what ExcludeOwnedSpecies actually returned,
// never recomputed from the owned set. That distinction carries the wrap case
// for free: on wrap the function hands back the declared table unchanged, every
// species is still present, and this correctly reports nothing excluded. It
// also covers the degenerate all-zero-survivors fallback, where the same thing
// happens for a different reason.
func excludedSpecies(declared, effective []growth.BreedWeight) []string {
	surviving := make(map[string]bool, len(effective))
	for _, w := range effective {
		surviving[w.Species] = true
	}
	out := []string{}
	for _, w := range declared {
		if !surviving[w.Species] {
			out = append(out, w.Species)
		}
	}
	return out
}

// allDeclaredOwned reports whether every species THIS SKU offers is on the
// learner's roster. Scoped to the declared table on purpose: a learner can own
// a species this SKU does not offer (a retired breed from an older table), and
// that says nothing about wrapping the current one.
func allDeclaredOwned(declared []growth.BreedWeight, owned map[string]bool) bool {
	if len(declared) == 0 {
		return false
	}
	for _, w := range declared {
		if !owned[w.Species] {
			return false
		}
	}
	return true
}

// toEggOddsDTO preserves the provider's ordering rather than imposing its own.
// chora-tenancy publishes the declared table sorted DESC by probability, and
// ExcludeOwnedSpecies keeps survivors in input order, so both tables reach the
// learner in the order the SKU declared them.
func toEggOddsDTO(in []growth.BreedWeight) []eggOddsWeightDTO {
	out := make([]eggOddsWeightDTO, 0, len(in))
	for _, w := range in {
		out = append(out, eggOddsWeightDTO{Species: w.Species, Probability: w.Probability, Rarity: w.Rarity})
	}
	return out
}

// eggOddsCompanionIDFromRequest reads the {id} wildcard set by the router
// pattern, falling back to parsing the path so the handler stays correct if it
// is ever dispatched from the /v1/me/companions/ subtree instead of matched
// directly.
func eggOddsCompanionIDFromRequest(r *http.Request) string {
	if id := strings.TrimSpace(r.PathValue("id")); id != "" {
		return id
	}
	rest := strings.TrimPrefix(r.URL.Path, eggOddsPathPrefix)
	id, ok := strings.CutSuffix(strings.TrimSuffix(rest, "/"), eggOddsPathSuffix)
	if !ok || strings.Contains(id, "/") {
		return ""
	}
	return strings.TrimSpace(id)
}
