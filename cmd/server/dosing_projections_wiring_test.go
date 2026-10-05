// dosing_projections_wiring_test.go — CHO-2167 RED.
//
// The bug in one sentence: `active_path_topics` and `topic_accuracy` had pg
// adapters that the composition root could never bind, because Server/ExtServer
// held the CONCRETE *inmem.* types. Production therefore ran the daily dose's
// CURIOSITY + WEAKNESS slots off per-pod memory. Verified live 2026-07-14:
// active_path_topics = 0 rows (never written since the table was created in
// migration 0004), while topic_accuracy = 3 rows written by a DIFFERENT
// consumer (DerivedWeaknessProjector, which DOES hold the pg adapter) — a
// split-brain the dose composer never read.
//
// The wiring rule under test: pg when the pool is available, in-memory ONLY as
// the dev fallback, and LOUD about which one is live. A volatile projection
// masquerading as durable is precisely this defect.
package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// newLazyTestPool builds a *pgxpool.Pool that never dials: min_conns=0 means
// pgxpool.New creates no idle connections, and this test never acquires one.
// It exercises the "is a pool present?" binding decision — which adapter gets
// chosen — without needing a live database.
func newLazyTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(),
		"postgres://u:p@127.0.0.1:1/chora_consumption?sslmode=disable&pool_min_conns=0")
	if err != nil {
		t.Fatalf("build lazy pool: %v", err)
	}
	return pool
}

// No pool ⇒ the in-memory fallback is retained (dev/test), never a nil repo.
func TestWireDosingProjections_NoPool_KeepsInmemFallback(t *testing.T) {
	srv := &httpadapter.Server{
		TopicAccuracy:    inmem.NewTopicAccuracyRepo(),
		ActivePathTopics: inmem.NewActivePathTopicsRepo(),
	}

	wireDosingProjections(srv, nil)

	if _, ok := srv.TopicAccuracy.(*inmem.TopicAccuracyRepo); !ok {
		t.Errorf("no pool: want the in-memory TopicAccuracy fallback, got %T", srv.TopicAccuracy)
	}
	if _, ok := srv.ActivePathTopics.(*inmem.ActivePathTopicsRepo); !ok {
		t.Errorf("no pool: want the in-memory ActivePathTopics fallback, got %T", srv.ActivePathTopics)
	}
}

// Nil server must not panic (mirrors the other wiring guards).
func TestWireDosingProjections_NilServerIsNoOp(t *testing.T) {
	wireDosingProjections(nil, nil)
}

// 🔴 THE BUG. With a pool available the DURABLE adapter must be bound. Before
// the fix this was impossible to even express: Server.TopicAccuracy was typed
// *inmem.TopicAccuracyRepo, so no pg repo could be assigned to it.
//
// pgxpool.New is lazy (it does not dial until first acquire), so a
// syntactically-valid DSN is enough to exercise the binding decision without a
// live database.
func TestWireDosingProjections_WithPool_BindsPgAdapters(t *testing.T) {
	pool := newLazyTestPool(t)
	defer pool.Close()

	srv := &httpadapter.Server{
		TopicAccuracy:    inmem.NewTopicAccuracyRepo(),
		ActivePathTopics: inmem.NewActivePathTopicsRepo(),
	}

	wireDosingProjections(srv, pool)

	if _, ok := srv.TopicAccuracy.(*pg.TopicAccuracyRepo); !ok {
		t.Errorf("pool available: want the pg TopicAccuracy adapter, got %T "+
			"(the dose's WEAKNESS slot is reading a volatile map while pg holds the real rows)", srv.TopicAccuracy)
	}
	if _, ok := srv.ActivePathTopics.(*pg.ActivePathTopicsRepo); !ok {
		t.Errorf("pool available: want the pg ActivePathTopics adapter, got %T "+
			"(the dose's CURIOSITY slot is reading a map wiped on every pod restart)", srv.ActivePathTopics)
	}
}
