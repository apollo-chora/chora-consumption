// dose_composer_v2_wiring.go — composition root for the ADR-224 (CHO-2045)
// daily-dose composer v2: subject/KG-balanced allocation + seeded weighted
// sampling on the weakness + curiosity slots.
//
// DARK by default behind DOSE_COMPOSER_V2_ENABLED. Per feedback_no_inline_config
// the flag is read here at the composition root and set on the Server; the
// handler + domain never touch the environment. Enabling it is safe on any
// learner — a single-subject / single-topic dose stays byte-identical to v1;
// the balance only activates as a learner's KG portfolio grows.
package main

import (
	"log"
	"os"
	"strings"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
)

func doseComposerV2Enabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("DOSE_COMPOSER_V2_ENABLED")), "true")
}

// wireDoseComposerV2 flips the Server onto the ADR-224 v2 dose selection when
// DOSE_COMPOSER_V2_ENABLED=true. Dark by default ⇒ the v1 strict global-shakiest
// fill (byte-identical) stays in charge.
func wireDoseComposerV2(srv *httpadapter.Server) {
	if !doseComposerV2Enabled() {
		log.Printf("consumption: dose composer v2 DISABLED (DARK) — set DOSE_COMPOSER_V2_ENABLED=true to enable [ADR-224 / CHO-2045]")
		return
	}
	srv.ComposerV2Enabled = true
	log.Printf("consumption: dose composer v2 ENABLED — subject/KG balance + seeded weighted sampling [ADR-224 / CHO-2045]")
}
