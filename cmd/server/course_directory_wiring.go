// course_directory_wiring.go — composition-root wiring for the course_directory
// projection (course_id → title; CHO-2059 follow-up). pg-only: the read-model is
// meaningless without a database, so when no pgx pool is present the projection
// stays inactive (the subscriber + push endpoint are simply not mounted, and the
// progress_mirror / ReadLearnerProfile consumers fall back to the leak-free
// generic noun — the projection is purely additive).
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireCourseDirectoryRepo constructs the CourseMetadataSubscriber on the supplied
// ExtServer over the pgx-backed pg.CourseDirectoryRepo when a pgx pool is
// available, and injects the SAME repo as the OPTIONAL course_id → title reader
// on the Phyllis Server (progress_mirror). No-op (logs) when pool is nil. MUST
// run before wireSubscriberPushHandlers so the course-metadata push endpoint
// mounts. The gRPC ReadLearnerProfile RPC gets its own repo instance in main.go
// (mirrors the LearnerProfile reader wiring).
func wireCourseDirectoryRepo(ext *httpadapter.ExtServer, srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: course_directory projection NOT wired (no pgx pool); Companion course names inactive (falls back to generic noun)")
		return
	}
	repo := pg.NewCourseDirectoryRepo(pg.NewPgxTxRunner(pool))
	ext.WireCourseDirectoryPg(repo)
	srv.CourseDirectory = repo
	// Same pg repo also feeds the READ side on ExtServer so the course-learn
	// read (getMeLearningPathByCourse) resolves the real course title instead
	// of the "Course <uuid>" bootstrap placeholder (CHO-2059 heading fix).
	ext.CourseDirectory = repo
	log.Printf("consumption: course_directory projection wired (pool=chora_consumption, pg-backed, CHO-2059)")
}
