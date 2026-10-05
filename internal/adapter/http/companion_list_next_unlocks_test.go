package http

// companion_list_next_unlocks_test.go: the companion LIST read carries
// next_unlocks per row (UX Track U, C1a; unblocks C3's stage-up teases).
//
// next_unlocks already existed, served only by handleGetGrowth. C3 renders a
// named tease per roster row and will not fall back to N growth calls, which
// is the debt #44 the list's inline growth_state exists to pay off.
//
// catalogue_active is load-bearing and must not be dropped in transit. The
// R3-9 tease exists to tell "unlocks next and is awake" from "unlocks next,
// still dark"; without the flag a tease promises a Skill the learner cannot
// use the moment they earn it, which is a worse experience than no tease.
//
// The list must NEVER fail on this. The roster is the page; a preview is a
// nicety. Every read failure here omits the previews for that row and serves
// the roster, mirroring learnerCatalogue in the ritual handlers.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type unlockPathReader struct {
	path *companion.SpeciesPath
	err  error
	seen []string
}

func (r *unlockPathReader) ActivePathForSpecies(_ context.Context, species string) (*companion.SpeciesPath, error) {
	r.seen = append(r.seen, species)
	if r.err != nil {
		return nil, r.err
	}
	return r.path, nil
}

type unlockCatalogue struct {
	entries map[string]companion.CatalogEntry
	err     error
	calls   int
}

func (c *unlockCatalogue) ListCatalogue(context.Context) (map[string]companion.CatalogEntry, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.entries, nil
}

type listUnlockWire struct {
	Items []struct {
		CompanionID string `json:"companion_id"`
		GrowthState *struct {
			NextUnlocks []struct {
				SkillKey        string `json:"skill_key"`
				SkillKind       string `json:"skill_kind"`
				UnlocksAtStage  int    `json:"unlocks_at_stage"`
				CatalogueActive bool   `json:"catalogue_active"`
			} `json:"next_unlocks"`
		} `json:"growth_state"`
	} `json:"items"`
}

func listUnlocks(t *testing.T, s *Server) listUnlockWire {
	t.Helper()
	// Straight at the list handler: the shared `do` helper drives the growth
	// SUBTREE router, which does not own /v1/me/companions.
	req := httptest.NewRequest(http.MethodGet, "/v1/me/companions", nil)
	req.Header.Set("X-Tenant-Id", "t-1")
	req.Header.Set("gcid", "gcid-1")
	w := httptest.NewRecorder()
	s.handleCompanionInstances(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", w.Code, w.Body.String())
	}
	var got listUnlockWire
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) == 0 {
		t.Fatal("no companions on the list read; the fixture proves nothing")
	}
	return got
}

func TestCompanionList_CarriesNextUnlocksPerRow(t *testing.T) {
	s := unlockServer(t, &unlockPathReader{path: unlockPath()}, &unlockCatalogue{entries: unlockCatalogueEntries()})

	got := listUnlocks(t, s)
	previews := got.Items[0].GrowthState.NextUnlocks
	if len(previews) == 0 {
		t.Fatal("no next_unlocks on the list row; C3 would have to make N growth calls")
	}
	if previews[0].SkillKey == "" || previews[0].UnlocksAtStage == 0 {
		t.Errorf("preview is missing its identity: %+v", previews[0])
	}
}

// The flag that makes the tease honest.
func TestCompanionList_NextUnlocksKeepsCatalogueActive(t *testing.T) {
	s := unlockServer(t, &unlockPathReader{path: unlockPath()}, &unlockCatalogue{entries: unlockCatalogueEntries()})

	var sawActive, sawDark bool
	for _, p := range listUnlocks(t, s).Items[0].GrowthState.NextUnlocks {
		if p.CatalogueActive {
			sawActive = true
		} else {
			sawDark = true
		}
	}
	if !sawActive || !sawDark {
		t.Errorf("the fixture must carry BOTH an awake and a dark skill or this proves nothing "+
			"(active=%t dark=%t): without the flag a tease promises a Skill the learner cannot use",
			sawActive, sawDark)
	}
}

// One catalogue read for the whole list, not one per row. A roster of eight
// companions must not make eight identical catalogue reads.
func TestCompanionList_ReadsTheCatalogueOncePerRequest(t *testing.T) {
	cat := &unlockCatalogue{entries: unlockCatalogueEntries()}
	s := unlockServerWithTwo(t, &unlockPathReader{path: unlockPath()}, cat)

	_ = listUnlocks(t, s)
	if cat.calls != 1 {
		t.Errorf("ListCatalogue called %d times for one list request; want 1", cat.calls)
	}
}

// A path read failure omits the previews and STILL serves the roster.
func TestCompanionList_PathFailureStillServesTheRoster(t *testing.T) {
	s := unlockServer(t, &unlockPathReader{err: errors.New("connection refused")},
		&unlockCatalogue{entries: unlockCatalogueEntries()})

	got := listUnlocks(t, s)
	if gs := got.Items[0].GrowthState; gs != nil && len(gs.NextUnlocks) != 0 {
		t.Errorf("previews served from a failed path read: %+v", gs.NextUnlocks)
	}
}

// A catalogue failure does the same. Both are the roster's rule: the page is
// the payload, the preview is a nicety.
func TestCompanionList_CatalogueFailureStillServesTheRoster(t *testing.T) {
	s := unlockServer(t, &unlockPathReader{path: unlockPath()},
		&unlockCatalogue{err: errors.New("connection refused")})

	got := listUnlocks(t, s)
	if gs := got.Items[0].GrowthState; gs != nil && len(gs.NextUnlocks) != 0 {
		t.Errorf("previews served from a failed catalogue read: %+v", gs.NextUnlocks)
	}
}

// Unwired seams are the ordinary state in a unit server and must be silent,
// not a 500 and not an empty roster.
func TestCompanionList_UnwiredPreviewSeamsStillServeTheRoster(t *testing.T) {
	s := unlockServer(t, nil, nil)
	_ = listUnlocks(t, s)
}

// ---- harness ---------------------------------------------------------------

// unlockPath is sized to the BAND RULE, not to intuition. At stage 1
// NextUnlocksForStage previews path positions 2 to 4 (cumulative 1 through
// stage 1, 4 through stage 2 under DefaultStageUnlockCounts), so a two-entry
// path yields exactly ONE preview and a catalogue_active assertion over it
// proves nothing. Four entries put an awake skill and a dark one both inside
// the previewed band.
func unlockPath() *companion.SpeciesPath {
	return &companion.SpeciesPath{
		PathID: "p-1", Species: "ember", Version: 1, Active: true,
		Entries: []string{"progress_mirror", "explain_anew", "map_sight", "flashcard_forge"},
	}
}

func unlockCatalogueEntries() map[string]companion.CatalogEntry {
	return map[string]companion.CatalogEntry{
		"progress_mirror": {SkillKey: "progress_mirror", Name: "Progress Mirror", Active: true},
		"explain_anew":    {SkillKey: "explain_anew", Name: "Explain It Differently", Active: true},
		// Dark on purpose: released nowhere, so a tease naming it must say so.
		"map_sight":       {SkillKey: "map_sight", Name: "Map Sight", Active: false},
		"flashcard_forge": {SkillKey: "flashcard_forge", Name: "Flashcard Forge", Active: true},
	}
}

type unlockInstances struct{ entries []*companion.RosterEntry }

func (r *unlockInstances) Create(context.Context, *companion.Instance, int) error { return nil }
func (r *unlockInstances) Get(context.Context, string) (*companion.Instance, error) {
	return nil, nil
}
func (r *unlockInstances) ListByOwner(context.Context, string, string) ([]*companion.Instance, error) {
	return nil, nil
}
func (r *unlockInstances) ListRosterByOwner(context.Context, string, string) ([]*companion.RosterEntry, error) {
	return r.entries, nil
}
func (r *unlockInstances) Update(context.Context, *companion.Instance) error { return nil }
func (r *unlockInstances) SoftDelete(context.Context, string) error          { return nil }

func rosterRow(id string) *companion.RosterEntry {
	return &companion.RosterEntry{
		Instance: &companion.Instance{CompanionID: id, TenantID: "t-1", OwnerGCID: "gcid-1", Name: "Ember"},
		Growth:   &companion.GrowthSnapshot{Stage: 1, StageName: "hatchling", CurrentBreed: "ember"},
	}
}

func unlockServer(t *testing.T, paths companion.SpeciesPathReader, cat *unlockCatalogue) *Server {
	t.Helper()
	return unlockServerRows(t, paths, cat, rosterRow("fam-1"))
}

// unlockServerWithTwo proves the per-request reads are shared: two rows of the
// SAME species must still make one catalogue read and one path read.
func unlockServerWithTwo(t *testing.T, paths companion.SpeciesPathReader, cat *unlockCatalogue) *Server {
	t.Helper()
	return unlockServerRows(t, paths, cat, rosterRow("fam-1"), rosterRow("fam-2"))
}

func unlockServerRows(t *testing.T, paths companion.SpeciesPathReader, cat *unlockCatalogue,
	rows ...*companion.RosterEntry) *Server {
	t.Helper()
	s := &Server{CompanionInstances: &unlockInstances{entries: rows}}
	if paths != nil {
		s.SpeciesPaths = paths
	}
	if cat != nil {
		s.SkillCatalog = cat
	}
	return s
}
