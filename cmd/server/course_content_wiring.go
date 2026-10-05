// course_content_wiring.go — composition root for the durable course-content
// read projection (CHO-1612 D2).
//
// The EXT-scope server (NewExtServer) backs the course-content projection with
// an in-memory adapter that is lost on pod restart + split across replicas,
// causing transient empty reads on GET /v1/me/courses/{id}/content during
// deploy churn. When a pgx pool is available this swaps in the pg-backed
// ProjectionRepo (durable + replica-consistent) and rebuilds the
// content_composed subscriber to write into it.
//
// In-memory remains the fallback for tests / dev without a database (matches
// chat_wiring.go + companion_instance_wiring.go). MUST run BEFORE
// wireSubscriberPushHandlers so the course-content push handler binds the
// pg-backed subscriber.
//
// Per feedback_no_inline_config the pg pool is supplied by bootstrapDBPool;
// this helper has no env knowledge of its own. Per multi-tenant-rls the pg
// adapter runs rls.ApplySession internally; tenant context flows from the HTTP
// middleware on the request context.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// wireCourseContentRepo swaps the in-memory course-content projection on the
// supplied ExtServer for the pgx-backed pg.CourseContentRepo when a pgx pool
// is available. No-op (logs) when pool is nil.
func wireCourseContentRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: CourseContent projection NOT swapped (no pgx pool); in-memory adapter retained (NOT durable across restart)")
		return
	}
	txRunner := pg.NewPgxTxRunner(pool)
	ext.WireCourseContentPg(pg.NewCourseContentRepo(txRunner))
	log.Printf("consumption: CourseContent projection wired (pool=chora_consumption, pg-backed, durable)")
}

// wireAtomIndexRepo swaps the in-memory atom_index projection on the supplied
// ExtServer for the pgx-backed pg.AtomIndexRepo when a pgx pool is available,
// rebuilding every subscriber that reads/writes atom_index. No-op (logs) when
// pool is nil. MUST run before ShareDosingProjections + wireSubscriberPushHandlers.
//
// Returns the pg-backed atom_index repo (or nil when no pool) so the composition
// root can ALSO hand it to the Consumption gRPC server's RecommendAtomsForLearner
// RPC — the sanctioned RAG-retrieval surface the AI Kernel recommender crew calls
// (it reads the SAME projection the HTTP daily-dose path uses).
func wireAtomIndexRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) atom_index.Repo {
	if pool == nil {
		log.Printf("consumption: atom_index projection NOT swapped (no pgx pool); in-memory adapter retained (NOT durable across restart)")
		return nil
	}
	txRunner := pg.NewPgxTxRunner(pool)
	repo := pg.NewAtomIndexRepo(txRunner)
	ext.WireAtomIndexPg(repo)
	log.Printf("consumption: atom_index projection wired (pool=chora_consumption, pg-backed, durable)")
	return repo
}

// wireLearningPathRepo swaps the in-memory LearningPath repo on the supplied
// ExtServer for the pgx-backed pg.LearningPathRepo when a pgx pool is
// available (R3 durability) so a bootstrapped path survives pod restart and is
// consistent across replicas — fixing the 404 PATH_NOT_BOOTSTRAPPED that the
// volatile in-memory repo produced after every restart. No-op (logs) when pool
// is nil. MUST run AFTER wireAtomIndexRepo (the rebuilt subscribers bind the
// already-pg s.AtomIndex) and BEFORE wireSubscriberPushHandlers.
//
// Returns the pg-backed repo (or nil when no pool — mirror of
// wireAtomIndexRepo) so the composition root can ALSO hand it to the Phyllis
// Server's daily-dose handler (L4 Fix-1, CHO-1702): the dose atom universe
// reads the SAME learning_paths projection the EXT-scope routes serve.
func wireLearningPathRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) learning_path.Repo {
	if pool == nil {
		log.Printf("consumption: LearningPath repo NOT swapped (no pgx pool); in-memory adapter retained (paths lost on restart)")
		return nil
	}
	txRunner := pg.NewPgxTxRunner(pool)
	repo := pg.NewLearningPathRepo(txRunner)
	ext.WireLearningPathPg(repo)
	log.Printf("consumption: LearningPath repo wired (pool=chora_consumption, pg-backed, durable across restart + replicas)")
	return repo
}
