// companion_egg_odds_handler_test.go: the effective per-learner odds surface.
//
// The load-bearing test here is TestEggOdds_EffectiveOddsMatchTheRollPool: the
// published table MUST be the same object the reveal rolls against, produced by
// the same growth.ExcludeOwnedSpecies call. Anything that recomputes the
// renormalisation independently would pass the arithmetic tests below and still
// drift from the roll the first time either copy changed, which is exactly the
// declared-vs-observed gap the IMDA D2 chi-square fairness audit reads.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

const (
	eggOddsCompanionID = "01970000-0000-7000-b000-000000000001"
	eggOddsSKU         = "egg.premium.v1"
)

// eggOddsDeclared mirrors the tenancy 0030 five-hero premium SKU. Sums to 100.
func eggOddsDeclared() []growth.BreedWeight {
	return []growth.BreedWeight{
		{Species: "owl", Probability: 15, Rarity: "common"},
		{Species: "fox", Probability: 13, Rarity: "common"},
		{Species: "penguin", Probability: 12, Rarity: "uncommon"},
		{Species: "dragon", Probability: 30, Rarity: "rare"},
		{Species: "phoenix", Probability: 30, Rarity: "legendary"},
	}
}

// ----- fakes -----

type fakeEggOddsGrowth struct {
	row      *growth.CompanionGrowthRow
	rowErr   error
	owned    map[string]bool
	ownedErr error

	// gotOwnedArgs records the (tenant, owner, exclude) triple so the test can
	// prove the exclusion is scoped exactly as RevealBreed scopes it.
	gotOwnedArgs [3]string
}

func (f *fakeEggOddsGrowth) GetGrowthRow(_ context.Context, _, _ string) (*growth.CompanionGrowthRow, error) {
	if f.rowErr != nil {
		return nil, f.rowErr
	}
	return f.row, nil
}

func (f *fakeEggOddsGrowth) OwnerSpeciesSet(_ context.Context, tenantID, ownerGCID, excludeCompanionID string) (map[string]bool, error) {
	f.gotOwnedArgs = [3]string{tenantID, ownerGCID, excludeCompanionID}
	if f.ownedErr != nil {
		return nil, f.ownedErr
	}
	return f.owned, nil
}

type fakeEggOddsDist struct {
	dist []growth.BreedWeight
	err  error
}

func (f *fakeEggOddsDist) Lookup(_, _ string) ([]growth.BreedWeight, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.dist, nil
}

// ----- harness -----

func seedEggOddsServer(t *testing.T, owned map[string]bool, dist []growth.BreedWeight) (*Server, *fakeEggOddsGrowth) {
	t.Helper()
	srv := NewServer()
	g := &fakeEggOddsGrowth{
		row: &growth.CompanionGrowthRow{
			CompanionID: eggOddsCompanionID,
			TenantID:    testTenant,
			OwnerGCID:   testGCID,
			EggSku:      eggOddsSKU,
		},
		owned: owned,
	}
	srv.CompanionEggOdds = &CompanionEggOddsHandler{Growth: g, Dist: &fakeEggOddsDist{dist: dist}}
	return srv, g
}

func eggOddsPath(companionID string) string {
	return "/v1/me/companions/" + companionID + "/egg-odds"
}

func getEggOdds(t *testing.T, srv *Server, companionID string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, eggOddsPath(companionID), nil)
	if headers == nil {
		headers = map[string]string{"X-Tenant-Id": testTenant, "gcid": testGCID}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

type eggOddsBody struct {
	CompanionID  string `json:"companion_id"`
	EggSKU       string `json:"egg_sku"`
	DeclaredOdds []struct {
		Species     string  `json:"species"`
		Probability float64 `json:"probability"`
		Rarity      string  `json:"rarity"`
	} `json:"declared_odds"`
	EffectiveOdds []struct {
		Species     string  `json:"species"`
		Probability float64 `json:"probability"`
		Rarity      string  `json:"rarity"`
	} `json:"effective_odds"`
	ExcludedSpecies []string `json:"excluded_species"`
	AllSpeciesOwned bool     `json:"all_species_owned"`
}

func decodeEggOdds(t *testing.T, w *httptest.ResponseRecorder) eggOddsBody {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body.String())
	}
	var out eggOddsBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return out
}

func sumEffective(b eggOddsBody) float64 {
	var s float64
	for _, w := range b.EffectiveOdds {
		s += w.Probability
	}
	return s
}

// ----- the ruling -----

// Owner ruling 2026-08-07: a species the learner already owns is impossible on
// the next reveal, so advertising its declared weight is a false statement. The
// survivors carry the whole 100.
func TestEggOdds_ExcludesOwnedSpeciesAndRenormalises(t *testing.T) {
	owned := map[string]bool{"fox": true, "owl": true, "penguin": true}
	srv, _ := seedEggOddsServer(t, owned, eggOddsDeclared())

	got := decodeEggOdds(t, getEggOdds(t, srv, eggOddsCompanionID, nil))

	if len(got.EffectiveOdds) != 2 {
		t.Fatalf("effective_odds has %d entries, want 2 (dragon + phoenix): %+v", len(got.EffectiveOdds), got.EffectiveOdds)
	}
	want := map[string]float64{"dragon": 50, "phoenix": 50}
	for _, w := range got.EffectiveOdds {
		if owned[w.Species] {
			t.Errorf("effective_odds still advertises %q, which the learner already owns", w.Species)
		}
		if math.Abs(w.Probability-want[w.Species]) > 0.01 {
			t.Errorf("%s effective probability = %v, want %v", w.Species, w.Probability, want[w.Species])
		}
	}
	if s := sumEffective(got); math.Abs(s-100) > 0.01 {
		t.Errorf("effective_odds sum = %v, want 100", s)
	}
	if got.AllSpeciesOwned {
		t.Error("all_species_owned = true, but two species are still unseen")
	}
}

// Requirement: surface BOTH numbers. A table that silently changed is worse
// than either alone, so the raw SKU odds must survive untouched alongside the
// effective ones, and the exclusions must be named.
func TestEggOdds_PublishesDeclaredAlongsideEffective(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{"fox": true, "owl": true, "penguin": true}, eggOddsDeclared())

	got := decodeEggOdds(t, getEggOdds(t, srv, eggOddsCompanionID, nil))

	if len(got.DeclaredOdds) != 5 {
		t.Fatalf("declared_odds has %d entries, want the untouched 5-species SKU table: %+v", len(got.DeclaredOdds), got.DeclaredOdds)
	}
	declaredWant := map[string]float64{"owl": 15, "fox": 13, "penguin": 12, "dragon": 30, "phoenix": 30}
	for _, w := range got.DeclaredOdds {
		if math.Abs(w.Probability-declaredWant[w.Species]) > 0.01 {
			t.Errorf("declared %s = %v, want %v (the SKU table must not be renormalised)", w.Species, w.Probability, declaredWant[w.Species])
		}
	}
	gotExcluded := append([]string(nil), got.ExcludedSpecies...)
	sort.Strings(gotExcluded)
	if len(gotExcluded) != 3 || gotExcluded[0] != "fox" || gotExcluded[1] != "owl" || gotExcluded[2] != "penguin" {
		t.Errorf("excluded_species = %v, want [fox owl penguin]", gotExcluded)
	}
	if got.EggSKU != eggOddsSKU {
		t.Errorf("egg_sku = %q, want %q", got.EggSKU, eggOddsSKU)
	}
	if got.CompanionID != eggOddsCompanionID {
		t.Errorf("companion_id = %q, want %q", got.CompanionID, eggOddsCompanionID)
	}
}

// THE anti-drift test. The published effective table must be byte-for-byte what
// growth.ExcludeOwnedSpecies produces, because that is the pool RevealBreed
// rolls. A second implementation of the renormalisation would pass every other
// test in this file and still break declared == observed.
func TestEggOdds_EffectiveOddsMatchTheRollPool(t *testing.T) {
	owned := map[string]bool{"fox": true, "owl": true, "penguin": true}
	srv, _ := seedEggOddsServer(t, owned, eggOddsDeclared())

	got := decodeEggOdds(t, getEggOdds(t, srv, eggOddsCompanionID, nil))

	// The exact pool the reveal rolls, from the shared domain function.
	rollPool := growth.ExcludeOwnedSpecies(eggOddsDeclared(), owned)
	if len(got.EffectiveOdds) != len(rollPool) {
		t.Fatalf("effective_odds has %d entries, roll pool has %d", len(got.EffectiveOdds), len(rollPool))
	}
	bySpecies := map[string]growth.BreedWeight{}
	for _, w := range rollPool {
		bySpecies[w.Species] = w
	}
	for _, w := range got.EffectiveOdds {
		ref, ok := bySpecies[w.Species]
		if !ok {
			t.Fatalf("effective_odds advertises %q, which is not in the roll pool", w.Species)
		}
		if w.Probability != ref.Probability {
			t.Errorf("%s: published %v but the roll uses %v (declared != observed)", w.Species, w.Probability, ref.Probability)
		}
		if w.Rarity != ref.Rarity {
			t.Errorf("%s: published rarity %q but the roll uses %q", w.Species, w.Rarity, ref.Rarity)
		}
	}
	// And the exclusion must be scoped exactly as RevealBreed scopes it:
	// (tenant, owner, THIS companion excluded).
	srvFake := getEggOddsFake(t, srv)
	if srvFake.gotOwnedArgs != [3]string{testTenant, testGCID, eggOddsCompanionID} {
		t.Errorf("OwnerSpeciesSet called with %v, want (tenant, gcid, this companion) as RevealBreed does", srvFake.gotOwnedArgs)
	}
}

// WRAP: once every offered species is owned there is nothing left to exclude,
// so the effective odds ARE the declared odds and the hatch is never blocked.
func TestEggOdds_Wrap_AllOwned_EffectiveEqualsDeclared(t *testing.T) {
	owned := map[string]bool{"owl": true, "fox": true, "penguin": true, "dragon": true, "phoenix": true}
	srv, _ := seedEggOddsServer(t, owned, eggOddsDeclared())

	got := decodeEggOdds(t, getEggOdds(t, srv, eggOddsCompanionID, nil))

	if len(got.EffectiveOdds) != 5 {
		t.Fatalf("wrap: effective_odds has %d entries, want all 5 back: %+v", len(got.EffectiveOdds), got.EffectiveOdds)
	}
	want := map[string]float64{"owl": 15, "fox": 13, "penguin": 12, "dragon": 30, "phoenix": 30}
	for _, w := range got.EffectiveOdds {
		if math.Abs(w.Probability-want[w.Species]) > 0.01 {
			t.Errorf("wrap: %s effective = %v, want the declared %v", w.Species, w.Probability, want[w.Species])
		}
	}
	if len(got.ExcludedSpecies) != 0 {
		t.Errorf("wrap: excluded_species = %v, want empty (nothing is excluded once the roster wraps)", got.ExcludedSpecies)
	}
	if !got.AllSpeciesOwned {
		t.Error("wrap: all_species_owned = false, but the learner owns every offered species")
	}
}

func TestEggOdds_NoneOwned_EffectiveEqualsDeclared(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())

	got := decodeEggOdds(t, getEggOdds(t, srv, eggOddsCompanionID, nil))

	if len(got.EffectiveOdds) != 5 {
		t.Fatalf("effective_odds has %d entries, want 5: %+v", len(got.EffectiveOdds), got.EffectiveOdds)
	}
	if len(got.ExcludedSpecies) != 0 {
		t.Errorf("excluded_species = %v, want empty", got.ExcludedSpecies)
	}
	if got.AllSpeciesOwned {
		t.Error("all_species_owned = true for a learner who owns nothing")
	}
}

// A learner can own a species this SKU does not offer (a retired breed from an
// older table). It must not appear in excluded_species: nothing was excluded
// from THIS table on its account.
func TestEggOdds_OwnedSpeciesOutsideTheSKUIsNotReportedAsExcluded(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{"turtle": true, "wolf": true}, eggOddsDeclared())

	got := decodeEggOdds(t, getEggOdds(t, srv, eggOddsCompanionID, nil))

	if len(got.ExcludedSpecies) != 0 {
		t.Errorf("excluded_species = %v, want empty (neither species is in this SKU)", got.ExcludedSpecies)
	}
	if len(got.EffectiveOdds) != 5 {
		t.Errorf("effective_odds has %d entries, want the full 5: an off-table species must not shrink the pool", len(got.EffectiveOdds))
	}
	if got.AllSpeciesOwned {
		t.Error("all_species_owned = true, but none of the SKU's species are owned")
	}
}

// A malformed SKU must NOT be laundered. ExcludeOwnedSpecies renormalises its
// survivors to 100, so a sum-20 table would be published as a healthy-looking
// sum-100 one and hide the upstream misconfiguration. RevealBreed validates the
// DECLARED table first and refuses; the odds surface must refuse identically.
func TestEggOdds_MalformedDeclaredDistributionFailsLoud(t *testing.T) {
	broken := []growth.BreedWeight{
		{Species: "owl", Probability: 10, Rarity: "common"},
		{Species: "fox", Probability: 10, Rarity: "common"},
	}
	srv, _ := seedEggOddsServer(t, map[string]bool{"owl": true}, broken)

	w := getEggOdds(t, srv, eggOddsCompanionID, nil)

	if w.Code < 500 {
		t.Fatalf("status %d, want a 5xx for a declared table that does not sum to 100: %s", w.Code, w.Body.String())
	}
	if bodyHasOdds(w) {
		t.Errorf("a malformed SKU was published as odds anyway: %s", w.Body.String())
	}
}

func TestEggOdds_UnknownCompanionIs404(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	getEggOddsFake(t, srv).rowErr = growth.ErrCompanionNotFound

	if w := getEggOdds(t, srv, eggOddsCompanionID, nil); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404: %s", w.Code, w.Body.String())
	}
}

// Another learner's pod is not found, never forbidden: 404 leaks no existence,
// and it is the same answer RevealBreed gives (ErrCompanionNotFound on an owner
// mismatch).
func TestEggOdds_CrossOwnerIs404(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	getEggOddsFake(t, srv).row.OwnerGCID = "01970000-0000-7000-9000-0000000000ff"

	w := getEggOdds(t, srv, eggOddsCompanionID, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 for another learner's pod: %s", w.Code, w.Body.String())
	}
	if bodyHasOdds(w) {
		t.Errorf("another learner's odds leaked: %s", w.Body.String())
	}
}

func TestEggOdds_UnknownSKUIs404(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	srv.CompanionEggOdds = &CompanionEggOddsHandler{
		Growth: getEggOddsFake(t, srv),
		Dist:   &fakeEggOddsDist{err: growth.ErrSkuNotFound},
	}

	if w := getEggOdds(t, srv, eggOddsCompanionID, nil); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for an unknown SKU: %s", w.Code, w.Body.String())
	}
}

func TestEggOdds_NotWiredIs503(t *testing.T) {
	srv := NewServer() // CompanionEggOdds nil
	if w := getEggOdds(t, srv, eggOddsCompanionID, nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 when the handler is not wired: %s", w.Code, w.Body.String())
	}
}

func TestEggOdds_RequiresLearnerContext(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"no headers", map[string]string{}},
		{"tenant only", map[string]string{"X-Tenant-Id": testTenant}},
		{"gcid only", map[string]string{"gcid": testGCID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := getEggOdds(t, srv, eggOddsCompanionID, tc.headers)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEggOdds_MethodNotAllowed(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	req := httptest.NewRequest(http.MethodPost, eggOddsPath(eggOddsCompanionID), nil)
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 405 or 404 for POST: %s", w.Code, w.Body.String())
	}
}

// A read error from the owned-species query must fail loud. Falling back to the
// declared odds would publish a table the reveal will not roll.
func TestEggOdds_OwnerSpeciesSetErrorFailsLoud(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	getEggOddsFake(t, srv).ownedErr = errors.New("pg: connection lost")

	w := getEggOdds(t, srv, eggOddsCompanionID, nil)
	if w.Code < 500 {
		t.Fatalf("status %d, want a 5xx when the owned-species read fails: %s", w.Code, w.Body.String())
	}
	if bodyHasOdds(w) {
		t.Errorf("odds published despite a failed owned-species read: %s", w.Body.String())
	}
}

// ----- helpers -----

func getEggOddsFake(t *testing.T, srv *Server) *fakeEggOddsGrowth {
	t.Helper()
	h, ok := srv.CompanionEggOdds.(*CompanionEggOddsHandler)
	if !ok {
		t.Fatalf("CompanionEggOdds is %T, want *CompanionEggOddsHandler", srv.CompanionEggOdds)
	}
	f, ok := h.Growth.(*fakeEggOddsGrowth)
	if !ok {
		t.Fatalf("handler Growth is %T, want *fakeEggOddsGrowth", h.Growth)
	}
	return f
}

func bodyHasOdds(w *httptest.ResponseRecorder) bool {
	var probe struct {
		Effective []json.RawMessage `json:"effective_odds"`
		Declared  []json.RawMessage `json:"declared_odds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &probe); err != nil {
		return false
	}
	return len(probe.Effective) > 0 || len(probe.Declared) > 0
}

// ----- direct-dispatch + error-mapping coverage -----

// The handler must also work when invoked directly rather than matched by the
// router pattern, because then r.PathValue("id") is empty and the id comes from
// the path. An untested fallback is not a fallback.
func TestEggOdds_ParsesCompanionIDFromPathWithoutRouterWildcard(t *testing.T) {
	g := &fakeEggOddsGrowth{
		row: &growth.CompanionGrowthRow{
			CompanionID: eggOddsCompanionID, TenantID: testTenant,
			OwnerGCID: testGCID, EggSku: eggOddsSKU,
		},
		owned: map[string]bool{"fox": true},
	}
	h := NewCompanionEggOddsHandler(g, &fakeEggOddsDist{dist: eggOddsDeclared()})

	req := httptest.NewRequest(http.MethodGet, eggOddsPath(eggOddsCompanionID), nil)
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req) // no router: PathValue("id") is empty

	got := decodeEggOdds(t, w)
	if got.CompanionID != eggOddsCompanionID {
		t.Errorf("companion_id = %q, want %q (path fallback did not resolve the id)", got.CompanionID, eggOddsCompanionID)
	}
	if len(got.EffectiveOdds) != 4 {
		t.Errorf("effective_odds has %d entries, want 4 (fox excluded)", len(got.EffectiveOdds))
	}
}

func TestEggOdds_UnresolvableIDIs400(t *testing.T) {
	h := NewCompanionEggOddsHandler(
		&fakeEggOddsGrowth{owned: map[string]bool{}},
		&fakeEggOddsDist{dist: eggOddsDeclared()},
	)
	for _, path := range []string{
		"/v1/me/companions//egg-odds",             // empty id
		"/v1/me/companions/a/b/egg-odds",          // nested, not a bare id
		"/v1/me/companions/" + eggOddsCompanionID, // no /egg-odds suffix
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Tenant-Id", testTenant)
		req.Header.Set("gcid", testGCID)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", path, w.Code, w.Body.String())
		}
	}
}

// A constructor with nil collaborators must 503 rather than panic: the wiring
// hands one of these back when the pgx pool is absent.
func TestEggOdds_NilCollaboratorsAre503(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, eggOddsPath(eggOddsCompanionID), nil)
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	NewCompanionEggOddsHandler(nil, nil).ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 for a handler built without a pool: %s", w.Code, w.Body.String())
	}
}

// A non-sentinel read error is an outage, not a missing pod: 500, never a 404
// that would read as "you have no such Companion".
func TestEggOdds_GrowthReadErrorIs500(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	getEggOddsFake(t, srv).rowErr = errors.New("pg: connection refused")

	w := getEggOdds(t, srv, eggOddsCompanionID, nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500: %s", w.Code, w.Body.String())
	}
}

// Likewise for the cross-domain SKU lookup: an unreachable chora-tenancy is a
// 500, distinct from the 404 an unknown SKU earns.
func TestEggOdds_SKULookupErrorIs500(t *testing.T) {
	srv, _ := seedEggOddsServer(t, map[string]bool{}, eggOddsDeclared())
	srv.CompanionEggOdds = &CompanionEggOddsHandler{
		Growth: getEggOddsFake(t, srv),
		Dist:   &fakeEggOddsDist{err: errors.New("grpc: connection refused")},
	}

	w := getEggOdds(t, srv, eggOddsCompanionID, nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500: %s", w.Code, w.Body.String())
	}
	if bodyHasOdds(w) {
		t.Errorf("odds published despite an unreachable SKU provider: %s", w.Body.String())
	}
}
