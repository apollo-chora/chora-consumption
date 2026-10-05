// bootstrap.go — production wiring helpers for chora-consumption.
//
// Per `feedback_resilience_priority` + secrets-and-env: production
// dependencies sourced from env vars. Local dev with NO env vars set falls
// through to in-memory adapters so the service starts offline.
//
// FAIL-LOUD POLICY (paydown 2026-05-13 — user directive):
// when an env var IS set but resolution fails (secret fetch timeout,
// pgx pool bootstrap timeout, sql.Open / Ping error), the service log.Fatals
// so kubelet CrashLoopBackOffs and retries with exponential backoff. Silent
// fallback to in-memory previously masked transient cold-start failures and
// kept the pod reporting Ready while serving stale in-memory data.
//
// Environment contract (mirrors chora-identity):
//
//	CHORA_DB_DSN_SECRET_ID         — env-backed secret name resolving
//	                                 to a chora_consumption DSN (app_rw).
//	CHORA_DB_DSN                   — direct DSN (dev override).
//	CHORA_DB_PROJECT               — project label for secret resolution.
//	                                 Defaults to chora-local.
//	CHORA_DB_REWRITE_FROM_PORT / CHORA_DB_REWRITE_TO_PORT
//	                               — Pgbouncer 6432 → 5432 rewrite for
//	                                 build-out phase.
//	CHORA_BOOTSTRAP_TIMEOUT_SECONDS — Gate-7: bootstrap context budget
//	                                 (default 30s; K8s sets 90s).
//
//	CHORA_OUTBOX_DSN_SECRET_ID     — env-backed secret name resolving
//	                                 to a chora_consumption DSN for the
//	                                 per-domain outbox PostgresStore.
//	CHORA_OUTBOX_DSN               — direct DSN (dev override).
//	CHORA_OUTBOX_WORKER_ID         — dispatcher worker_id; defaults to
//	                                 HOSTNAME.
//
//	NATS_URL                       — NATS JetStream broker URL for the
//	                                 event bus. Unset → in-memory bus
//	                                 (publish-only; subscribers NOT bound).
//
// All env vars empty in dev → fallthrough to in-memory adapters.
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/db"
	"github.com/apollo-chora/chora-common/eventbus"
	cgcsecrets "github.com/apollo-chora/chora-common/secrets"

	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

// consumptionDBRuntimeParams returns the per-connection Postgres GUCs that
// keep a chora_consumption write from hanging forever (fail-loud). Set on
// every pooled connection via BootstrapOptions.RuntimeParams.
//
//   - lock_timeout=3s: a statement blocked on a row lock (e.g. UPDATE goals
//     behind a concurrent holder) ERRORs ("canceling statement due to lock
//     timeout") instead of waiting indefinitely and leaking the request
//     goroutine. 3s sits under the gateway's 6s per-call timeout, so
//     chora-consumption fails loud (500) before the gateway 504s — the exact
//     PATCH /v1/me/goals/{id} defect (CHO-2005).
//   - idle_in_transaction_session_timeout=60s: reaps a leaked open
//     transaction so its row locks release automatically — defusing the
//     class of stuck holder that blocked every goal PATCH during the walk.
//
// No statement_timeout (owner steer): long read paths must not be capped.
// Values are resilience constants (cf. db.DefaultPoolConfig).
func consumptionDBRuntimeParams() map[string]string {
	return map[string]string{
		"lock_timeout":                        "3s",
		"idle_in_transaction_session_timeout": "60s",
	}
}

// bootstrapDBPool returns a pgxpool.Pool for chora_consumption when the
// environment is configured for it; nil otherwise.
func bootstrapDBPool(ctx context.Context) (*pgxpool.Pool, func()) {
	dsn := os.Getenv("CHORA_DB_DSN")
	secretID := os.Getenv("CHORA_DB_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		log.Printf("consumption: CHORA_DB_DSN / CHORA_DB_DSN_SECRET_ID unset — using in-memory repositories")
		return nil, nil
	}

	project := os.Getenv("CHORA_DB_PROJECT")
	if project == "" {
		project = "chora-local"
	}

	var fetcher db.SecretFetcher
	var sclient *cgcsecrets.Client
	if secretID != "" && dsn == "" {
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			log.Fatalf("consumption: secret init failed (env set, fail-loud): %v", err)
		}
		sclient = c
		fetcher = c
	}

	rewriteFrom, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_FROM_PORT"))
	rewriteTo, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_TO_PORT"))

	// Gate-7 fix: env-driven bootstrap context (default 30s). Set
	// CHORA_BOOTSTRAP_TIMEOUT_SECONDS=90 in the deployment env.
	bootstrapSecs, _ := strconv.Atoi(os.Getenv("CHORA_BOOTSTRAP_TIMEOUT_SECONDS"))
	if bootstrapSecs <= 0 {
		bootstrapSecs = 30
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, time.Duration(bootstrapSecs)*time.Second)
	defer cancel()

	pool, err := db.Bootstrap(bootstrapCtx, db.BootstrapOptions{
		DSN:             dsn,
		SecretID:        secretID,
		SecretFetcher:   fetcher,
		RewriteFromPort: rewriteFrom,
		RewriteToPort:   rewriteTo,
		AppName:         "chora-consumption",
		RuntimeParams:   consumptionDBRuntimeParams(),
	})
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		log.Fatalf("consumption: pgx pool bootstrap failed (env set, fail-loud — kubelet will CrashLoopBackOff): %v", err)
	}

	shutdown := func() {
		pool.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	}
	return pool, shutdown
}

// bootstrapBus returns a NATS JetStream eventbus when NATS_URL is set,
// otherwise nil (the caller falls back to the in-memory bus for the outbox
// dispatcher; subscribers are NOT bound without a broker). The returned
// closure is the shutdown hook; caller defers it.
//
// Environment contract:
//
//	NATS_URL — JetStream broker URL (e.g. nats://127.0.0.1:4222).
//	           Unset → nil bus (in-process fallback; NOT durable).
func bootstrapBus(ctx context.Context) (eventbus.Bus, func()) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		return nil, nil
	}
	bus, err := eventbus.NewJetStream(eventbus.JetStreamConfig{URL: url})
	if err != nil {
		log.Printf("consumption: NATS JetStream init failed: %v — falling back to in-memory bus (subscribers NOT bound)", err)
		return nil, nil
	}
	return bus, func() { _ = bus.Close() }
}

// consumerConfig is the shared durable-consumer tuning for every
// chora-consumption subscriber: at-least-once with a 30s ack window, five
// delivery attempts, and the canonical _dlq.<subject> dead-letter routing.
//
// The dotted subscription id is safe as Name — eventbus sanitises it to a
// NATS-legal durable name internally.
func consumerConfig(name, subject string) eventbus.ConsumerConfig {
	return eventbus.ConsumerConfig{
		Name:       name,
		Subject:    subject,
		MaxDeliver: 5,
		AckWait:    30 * time.Second,
		Backoff: []time.Duration{
			1 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second,
		},
		DLQSubject: eventbus.DLQSubject(subject),
	}
}

// -----------------------------------------------------------------------------
// M12.3 W1.5b — per-domain outbox bootstrap helpers (mirror chora-creation
// W1.5a + chora-tenancy W2b).
//
// Env contract (paydown 2026-05-13):
//   CHORA_OUTBOX_DSN_SECRET_ID — env-backed secret name (resolved via the
//                                chora-common secrets Client). Recommended
//                                for production.
//   CHORA_OUTBOX_DSN           — direct DSN (dev override; takes precedence).
//   CHORA_OUTBOX_WORKER_ID     — dispatcher worker_id; defaults to HOSTNAME.
//
// Both unset → return nil/nil (dev fallback). Either set + error → log.Fatal
// per fail-loud paydown directive.
// -----------------------------------------------------------------------------

func bootstrapOutboxDB(ctx context.Context) (*sql.DB, func()) {
	dsn := os.Getenv("CHORA_OUTBOX_DSN")
	secretID := os.Getenv("CHORA_OUTBOX_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		return nil, nil
	}

	var sclient *cgcsecrets.Client
	if dsn == "" {
		project := os.Getenv("CHORA_DB_PROJECT")
		if project == "" {
			project = "chora-local"
		}
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			log.Fatalf("consumption: outbox secret init failed (env set, fail-loud): %v", err)
		}
		sclient = c
		bootstrapSecs, _ := strconv.Atoi(os.Getenv("CHORA_BOOTSTRAP_TIMEOUT_SECONDS"))
		if bootstrapSecs <= 0 {
			bootstrapSecs = 30
		}
		resolveCtx, cancel := context.WithTimeout(ctx, time.Duration(bootstrapSecs)*time.Second)
		defer cancel()
		resolved, err := c.GetSecret(resolveCtx, secretID)
		if err != nil {
			_ = c.Close()
			log.Fatalf("consumption: outbox secret fetch %q failed (env set, fail-loud): %v", secretID, err)
		}
		dsn = resolved
	}

	// Optional port rewrite (Pgbouncer 6432 → 5432).
	if rewriteFrom, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_FROM_PORT")); rewriteFrom != 0 {
		if rewriteTo, _ := strconv.Atoi(os.Getenv("CHORA_DB_REWRITE_TO_PORT")); rewriteTo != 0 {
			rewritten, err := db.RewriteDSNPort(dsn, rewriteFrom, rewriteTo)
			if err != nil {
				if sclient != nil {
					_ = sclient.Close()
				}
				log.Fatalf("consumption: outbox DSN port rewrite failed (env set, fail-loud): %v", err)
			}
			dsn = rewritten
		}
	}

	dbh, err := sql.Open("pgx", dsn)
	if err != nil {
		dbh, err = sql.Open("postgres", dsn)
	}
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		log.Fatalf("consumption: outbox sql.Open failed (env set, fail-loud): %v", err)
	}
	if pingErr := dbh.PingContext(ctx); pingErr != nil {
		_ = dbh.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
		log.Fatalf("consumption: outbox db.Ping failed (env set, fail-loud): %v", pingErr)
	}
	return dbh, func() {
		_ = dbh.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	}
}

func outboxWorkerID() string {
	if v := os.Getenv("CHORA_OUTBOX_WORKER_ID"); v != "" {
		return v
	}
	if v := os.Getenv("HOSTNAME"); v != "" {
		return v
	}
	return "chora-consumption-local"
}

type sqlDBAdapter struct {
	db *sql.DB
}

func (a sqlDBAdapter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return a.db.ExecContext(ctx, query, args...)
}

func (a sqlDBAdapter) QueryContext(ctx context.Context, query string, args ...any) (consumptionoutbox.SQLRows, error) {
	return a.db.QueryContext(ctx, query, args...)
}

var _ consumptionoutbox.SQLDB = sqlDBAdapter{}
