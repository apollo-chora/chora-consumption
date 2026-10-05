// resonance_titles_wiring.go: composition root for the character sheet's
// resonant titles (D3 slice 1b, CHO-2047).
//
// Slice 1 shipped the ResonanceTitleReader port and the growth-read enrichment,
// and left the field nil in every deployment. The profile has therefore been
// telling every learner that their resonant concept is gone, for concepts that
// are alive. This binds the two projections that already hold the names.
//
// Both are in-domain: the learner's ConceptNode graph (bound by
// wireConceptGraphReRoot) and the local atom_index projection fed by
// chora.creation.atom.created.v1 (bound by wireAtomIndexRepo). Atom titles are
// OWNED by chora_creation, and reading that database here would be a cross-DB
// query, which is forbidden; the projection is the sanctioned copy. Per
// secrets-and-env this file reads no env: both collaborators arrive already
// built from main.
//
// MUST run AFTER wireAtomIndexRepo (sets srv.AtomIndex) and
// wireConceptGraphReRoot (sets ext.Concepts).
package main

import (
	"log"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
)

// wireResonanceTitles binds srv.ResonanceTitles when BOTH projections are live,
// and deliberately leaves it nil otherwise.
//
// Refusing to build a half resolver is the point. Nil is the state the Server
// documents and handles: no titles, and the profile's honest "this concept is
// gone" copy. A resolver holding one nil arm would instead look wired and
// report that arm unwired on every single read. Both roads end in an absent
// title, which is exactly why the choice has to be made here, loudly, at boot,
// rather than discovered later from a screen that cannot tell them apart.
func wireResonanceTitles(srv *httpadapter.Server, ext *httpadapter.ExtServer) {
	if srv == nil || ext == nil {
		return
	}
	// The ATOM arm binds on its own (D2 tail a follow-up). The both-arms rule
	// below is right for the PROFILE, which reads a concept AND an atom; it was
	// wrong for the ritual source titles, which read atoms only, and it left
	// every grounded step nameless wherever the concept graph was absent. That
	// is a liveness fact about concepts gating atoms, which it should never do.
	if srv.AtomIndex != nil {
		srv.AtomTitles = httpadapter.NewAtomTitleReader(srv.AtomIndex)
	} else {
		log.Printf("consumption: atom titles NOT wired (no atom_index projection); " +
			"a grounded ritual step will show no sources rather than naming them")
	}
	if ext.Concepts == nil || srv.AtomIndex == nil {
		log.Printf("consumption: resonant titles NOT wired (concepts=%T atom_index=%T); "+
			"the character sheet will say a concept is gone rather than name it",
			ext.Concepts, srv.AtomIndex)
		return
	}
	srv.ResonanceTitles = httpadapter.NewResonanceTitleResolver(ext.Concepts, srv.AtomIndex)
	log.Printf("consumption: resonant titles wired (D3 CHO-2047): concept graph + atom_index projection")
}
