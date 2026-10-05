// weakness_mana_wiring.go — composition root for the WS-4 (CHO-1956) upfront
// mana gate at the Growth-Edge upload door (ADR-205 D6).
//
// DARK by default. The gate is ENABLED only when WEAKNESS_UPFRONT_MANA_ENABLED=
// true. That flag is COUPLED to the crew cutover (WEAKNESS_CREW_MODE=graph): the
// graduated graph crew owns the refund-on-fail, so reserving mana upfront while
// the live crew is still single-shot (no refund) would STRAND a reservation on a
// failed analysis. The owner flips both together at cutover. Until then the
// upload door accepts free with an empty reservation_id (today's behaviour).
//
// Fail-loud: when the flag is on, the mana quoter + Companion-instance repo MUST
// be present, or the service refuses to boot — never a silent free pass.
//
// Per feedback_no_inline_config the env read happens ONLY here at the composition
// root; the handler + domain gate never touch the environment.
package main

import (
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// wireGrowthEdgeUpfrontMana enables the upload-door mana gate when
// WEAKNESS_UPFRONT_MANA_ENABLED=true. MUST run AFTER NewServer (so srv.ManaQuoter
// is resolved from CHORA_IDENTITY_MANA_GRPC_URL) and is a no-op when disabled.
func wireGrowthEdgeUpfrontMana(srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("WEAKNESS_UPFRONT_MANA_ENABLED")), "true") {
		log.Printf("consumption: growth-edge upfront mana DISABLED (WEAKNESS_UPFRONT_MANA_ENABLED unset/false) — uploads free, empty reservation_id [WS-4 dark; couples to WEAKNESS_CREW_MODE=graph]")
		return
	}

	// Enabled — every dependency is mandatory. Fail-loud at boot (never accept a
	// premium upload free because a dep was forgotten).
	if srv == nil || srv.ManaQuoter == nil {
		log.Fatalf("consumption: WEAKNESS_UPFRONT_MANA_ENABLED=true but ManaQuoter unset — set CHORA_IDENTITY_MANA_GRPC_URL. Refusing to boot (fail-loud; no silent free pass).")
	}
	if pool == nil {
		log.Fatalf("consumption: WEAKNESS_UPFRONT_MANA_ENABLED=true but no pgx pool for the Companion gate. Refusing to boot.")
	}

	instRepo := pg.NewCompanionInstanceRepo(pg.NewPgxTxRunner(pool))
	ext.WeaknessMana = wu.NewManaGate(
		true,
		clients.NewCompanionPresence(instRepo),
		clients.NewWeaknessManaReserver(srv.ManaQuoter),
	)
	log.Printf("consumption: growth-edge upfront mana ENABLED (WS-4) — premium gate active (active-Companion + reserve-on-accept @ action_code=%s)", companion.ActionWeaknessAnalysis)
}
