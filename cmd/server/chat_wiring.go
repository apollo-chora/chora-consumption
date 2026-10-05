// chat_wiring.go — composition root for the ADR-154 Companion conversational
// chat surface.
//
// Wires the CompanionChatSessionRepo (pg-backed) onto the Server when a
// pgx pool is available. When the pool is nil, srv.CompanionChatSessions
// stays nil and the chat handler skips session persistence — the engine
// session is then single-turn-only (sessions still WORK, but conversation
// history is not preserved across requests). The 402 mana envelope path
// continues to work without the repo (mana state lives in chora-identity).
//
// Per feedback_no_inline_config the pg pool is supplied by bootstrapDBPool;
// this helper has no env knowledge of its own.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireChatSessions attaches the CompanionChatSessionRepo to the supplied
// Server when a pgx pool is available. When pool is nil it logs + returns
// without attaching (the chat handler degrades to single-turn mode).
func wireChatSessions(srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: companion chat session repo NOT wired (no pgx pool); per-turn mode only")
		return
	}
	txRunner := pg.NewPgxTxRunner(pool)
	srv.CompanionChatSessions = pg.NewCompanionChatSessionRepo(txRunner)
	log.Printf("consumption: CompanionChatSessionRepo wired (pool=chora_consumption)")
}
