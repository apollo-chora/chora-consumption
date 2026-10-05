package events

import "flag"

// updateWireFixture regenerates the committed Go/Python wire artifact.
// Deliberately opt-in: an auto-regenerating fixture would rubber-stamp every
// rename and defeat the whole purpose of pinning the contract.
var updateWireFixture = flag.Bool("update", false, "regenerate the concept_suggestion.requested.v1 wire fixture")
