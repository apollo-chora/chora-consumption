# chora-consumption

> The learner-facing consumption service: learning paths, atom sessions, daily-dose
> and retention data, Companion state, goals, Growth Edges, transcripts, and the
> per-user Knowledge Graph.

Go. PostgreSQL for durable state, a transactional outbox to NATS JetStream for
domain events, and an S3-compatible store for weakness uploads. It is the busiest
service in the stack by migration count and the one that talks to the most peers.

## What it is

Everything a learner accumulates lives here. The service groups into four areas:

- **Learning** — learning paths, enrolments, atom sessions and their answers,
  topic retention, practice budget, course content.
- **Companion** — Companion state and XP, chat, memory, rituals, skills, campaigns,
  acquisition and bindings.
- **Growth** — Growth Edges and their uploads, diagnosis state, goals, maps, the
  concept graph and its authoring/suggestion surfaces.
- **Knowledge Graph** — per-user KG data, canvas operations, discovery, heatmap, and
  tenant-scoped KG routes.

It serves two transports on two ports: HTTP on `PORT` (default `8080`) and gRPC on
`CHORA_GRPC_PORT` (default `9090`). In the stack, `compose.yml` `expose`s both and
publishes neither to the host.

### The gRPC surface is mostly stubs

The gRPC server registers `chora.services.consumption.v1.Consumption`,
`KnowledgeGraphService`, the standard gRPC health service, and `CompanionGrowth`.
Only `RecommendAtomsForLearner` is implemented, against the local atom-index
projection; the other contract-registered RPCs fall through to the generated
`Unimplemented...` implementations until their domain wiring lands. Do not build an
integration against the unimplemented ones yet — check the response code rather than
assuming a compiled service satisfies the whole contract.

## Prerequisites

| Route | What you need |
| --- | --- |
| In the stack | A working `chora-stack` deployment. PostgreSQL, NATS and Garage come from it, as do the identity, delivery, creation, model-gateway and AI-kernel peers. |
| Standalone (durable) | Go **1.26.1 or newer**, PostgreSQL with this repository's schema applied, and NATS JetStream for events. |
| Standalone (no dependencies) | Go only: with the DSNs unset the service uses in-memory repositories and an in-process event bus. |
| Weakness uploads | An S3-compatible bucket. In the stack `garage-init` creates `weakness-uploads`. |

## Credentials

This service holds **no model-provider credentials**. It calls the model gateway for
embeddings rather than a provider directly, and its one outbound credential is the
object store.

| Value | Where it comes from | Variable |
| --- | --- | --- |
| PostgreSQL password | the stack's `POSTGRES_PASSWORD`, carried into both DSNs as `chora_consumption_app_rw` | `CHORA_DB_DSN`, `CHORA_OUTBOX_DSN` |
| Object-storage credentials | the stack's Garage key and secret | `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` |
| NATS endpoint | the stack's NATS service; no credential locally | `NATS_URL` |
| Embedding endpoint | the stack sets it to the gateway | `CHORA_MODEL_GATEWAY_GRPC_URL` |
| Embedding model id | the stack's `.env` — required by **Compose**, not read by this service | `EMBEDDING_LOGICAL_MODEL_ID` |
| Weakness blob KEK | you generate it, only when envelope encryption is enabled | the variable **named by** `WEAKNESS_BLOB_KEK_ENV` |

Two different things are easy to confuse here. `EMBEDDING_LOGICAL_MODEL_ID` is a
**Compose-level interpolation requirement** (`${EMBEDDING_LOGICAL_MODEL_ID:?...}`),
so the stack refuses to run without it — but **this service does not read it**; the
identifier appears only in comments and tests. What actually turns this service's
embedding paths on is `CHORA_MODEL_GATEWAY_GRPC_URL`: with it unset, the campaign
retrieval lane and the derived-weakness projector log themselves `OFF` and Companion
memory is built without an embedder.

The provider key behind the embedding route belongs to the model gateway, not to this
service, and is documented in
[`chora-stack/README.md`](../chora-stack/README.md#credentials).

**Weakness uploads are plaintext by default.** Envelope encryption and its
crypto-shred guarantee are gated on `WEAKNESS_BLOB_ENVELOPE_ENABLED=true`. With the
flag off — which is how `compose.yml` leaves it — the service logs
`blob envelope crypto-shred DISABLED (DARK) — plaintext upload, no shred` and stores
uploads as-is. Turning it on requires a database pool and a base64 AES-256 key, and
fails loud without them rather than silently storing plaintext.

## Run it in the stack

`compose.yml` does four things:

1. Builds the image from the sibling checkout at `../chora-consumption`.
2. Runs `consumption-migrate` first — a `postgres:18.6-alpine` one-shot applying this
   repository's migrations to `chora_consumption`.
3. Starts the service only after a long fan-in: `postgres` and `nats` healthy,
   `nats-init` and `garage-init` completed, `garage` healthy, the migrate one-shot
   completed, plus `model-gateway`, `identity`, `delivery`, `creation` and
   `ai-kernel-orchestrator` started.
4. Publishes no host port; it `expose`s `8080` and `9090`.

```sh
cd chora-stack
docker compose up -d consumption
docker compose ps consumption
```

That dependency list makes this service a good canary, but read the conditions: the
infrastructure peers use `service_healthy` and `service_completed_successfully`, while
`model-gateway`, `identity`, `delivery`, `creation` and `ai-kernel-orchestrator` use
`service_started`. Compose only waits for those five to *start* — a peer that starts
and then crashes does not hold `consumption` in `Created`. It leaves the corresponding
feature broken at runtime instead, which is the harder failure to spot.

Beyond the credentials above, Compose sets the in-network peer URLs:
`CHORA_MODEL_GATEWAY_GRPC_URL`, `CHORA_IDENTITY_MANA_GRPC_URL`,
`CHORA_DELIVERY_GRPC_BASE_URL`, `SVC_CREATION_GRPC_URL`,
`AI_KERNEL_ORCHESTRATOR_URL`, and the four `S3_*` values against
`http://garage:3900` with `WEAKNESS_UPLOADS_BUCKET=weakness-uploads`.

Probe it from inside the network:

```sh
docker compose exec -T nats wget -qO- http://consumption:8080/readyz
```

## Run it standalone

Fully in-memory, no database and no broker:

```sh
git clone https://github.com/apollo-chora/chora-consumption.git
cd chora-consumption
go run ./cmd/server
```

That path is real, not a stub: with `CHORA_DB_DSN` and `CHORA_OUTBOX_DSN` unset the
service selects in-memory repositories and an in-process event bus, and with
`NATS_URL` unset no subscriber binds to a durable broker.

It is a **partial** mode, not a full offline clone. Several features switch off rather
than being replaced: the campaign retrieval lane and the derived-weakness projector
need an embedder (`CHORA_MODEL_GATEWAY_GRPC_URL`), the weakness-analysis consumer needs
a database pool, an embedder and an event bus together, and `CompanionGrowth` is not
registered without its production wiring. Expect HTTP surfaces over empty in-memory
data — not a working Growth or Companion pipeline — and everything gone on restart.

Durable, against your own database. **This service does not read `.env`** — it reads
the process environment, so export the values (or use a launcher that loads the file):

```sh
export CHORA_DB_DSN='postgres://user:pass@localhost:5432/chora_consumption?sslmode=disable'
export CHORA_OUTBOX_DSN="$CHORA_DB_DSN"
export NATS_URL=nats://localhost:4222
go run ./cmd/server
```

Apply the migrations first — the service does not migrate itself. Use the stack's
`scripts/migrate.sh` or any runner honouring the same conventions (forward-only,
`schema_migrations`, checksum immutability).

`.env.example` is a reference, not a working config: it points at `postgres:5432` and
`nats:4222`, the Compose service names, which do not resolve on a developer machine.

Build, test, container:

```sh
go build ./...
go test ./...
docker build -t chora-consumption .
```

The image is a distroless non-root runtime. CI publishes multi-arch images as
`walfa/chora-consumption`, `latest` on pushes to `main` plus SHA-tagged builds.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listener port |
| `CHORA_GRPC_PORT` | `9090` | gRPC listener port |
| `CHORA_DB_DSN` | unset | PostgreSQL DSN; unset selects in-memory repositories |
| `CHORA_OUTBOX_DSN` | unset | PostgreSQL DSN for the transactional outbox |
| `CHORA_OUTBOX_WORKER_ID` | `HOSTNAME`, else a local fallback | outbox dispatcher identity |
| `NATS_URL` | unset | NATS JetStream URL; unset means the in-process bus and no durable subscriber |
| `CHORA_SOURCE_PROJECT` | `chora-local` | event envelope source-project label |
| `CHORA_ENVIRONMENT` | `dev` | environment label |
| `EMBEDDING_LOGICAL_MODEL_ID` | **required by the stack** | the embedding model id callers stamp on the wire |
| `WEAKNESS_UPLOADS_BUCKET` | `chora-weakness-uploads` (stack: `weakness-uploads`) | bucket for weakness uploads |
| `WEAKNESS_BLOB_ENVELOPE_ENABLED` | off | enable envelope encryption and crypto-shred for uploads |
| `WEAKNESS_BLOB_KEK_ENV` | unset; falls back to `CHORA_BLOB_KEK` **only when** envelope encryption is enabled | the *name* of the variable holding the base64 AES-256 KEK |
| `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | unset | object-store connection |
| `S3_FORCE_PATH_STYLE`, `S3_REGION` | unset (stack: `true`, `us-east-1`) | Garage needs path-style addressing |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://otel-collector:4317` | OTLP/gRPC trace endpoint |

Two indirect settings are worth reading carefully. `WEAKNESS_UPLOADS_BUCKET`'s
documented default is `chora-weakness-uploads`, but the stack overrides it to
`weakness-uploads` — and `garage-init` creates `weakness-uploads`, not the default
name. And `WEAKNESS_BLOB_KEK_ENV` holds the *name* of another variable, not the key
itself; it defaults to `CHORA_BLOB_KEK`.

## Verify it works

### It is up

```sh
docker compose exec -T nats wget -qO- http://consumption:8080/healthz
docker compose exec -T nats wget -qO- http://consumption:8080/readyz
```

```json
{"status":"ok","time":"..."}
{"status":"ready","time":"..."}
```

`readyz` is deliberately terse here — it reports readiness, not a component
inventory, so it does not tell you whether the database or the object store is
reachable. Use the checks below for that.

### Its migrations landed

```sh
docker compose exec -T postgres psql -U chora_consumption_migrate -d chora_consumption \
  -c "select count(*) from schema_migrations;"
```

With the forward set at 101 files, expect a count at or just under that — the runner
records one row per applied file.

### Its HTTP surface answers

These routes need tenant and learner context, so an unadorned request is a `400`, not
a list. Send both headers:

```sh
docker compose exec -T nats wget -qO- \
  --header="X-Tenant-Id: 00000000-0000-7000-8000-000000000001" \
  --header="gcid: 1ebe8ad8-218d-5185-ab8a-f64fcf558f8d" \
  http://consumption:8080/api/learning-paths
```

`{"items":[]}` is the pass — an empty list, because the seeded tenant has no paths
yet. A `400` means the headers did not arrive; a connection error means the service
is not up.

### Its gRPC port is bound

```sh
docker compose exec -T nats wget -qO- -T 3 http://consumption:9090/ 2>&1 | head -c 120
```

The gRPC port speaks gRPC, not HTTP, so a valid probe is not expected to succeed.
`Connection reset by peer` is the pass — it proves a listener is there and is
rejecting an HTTP request, which is exactly what a gRPC server does. A proper health
check needs a gRPC client against the registered health service.

### Weakness uploads are actually protected

```sh
docker compose logs consumption | grep -i 'blob envelope'
```

`DISABLED (DARK)` is the default and means uploads are plaintext. `ENABLED (WS-5)`
means the key was found and wrapping is active.

## Troubleshooting

- **`consumption` stays in `Created`** — one of its many dependencies never became
  ready: `postgres`, `nats`, `nats-init`, `garage`, `garage-init`,
  `consumption-migrate`, `model-gateway`, `identity`, `delivery`, `creation`, or
  `ai-kernel-orchestrator`. Check that dependency's log, not this one.
- **Compose refuses to run with `required variable EMBEDDING_LOGICAL_MODEL_ID is
  missing`** — set it in `chora-stack/.env`. It gates every command in that
  directory, not just this service.
- **Everything is lost on restart** — `CHORA_DB_DSN` and `CHORA_OUTBOX_DSN` are
  unset, so the service is on in-memory repositories. That is the intended
  dependency-free mode, not durable storage.
- **Events are published but no consumer sees them** — `NATS_URL` is unset, so the
  outbox dispatcher fell back to the in-process bus and nothing left the process.
- **Weakness uploads are stored unencrypted** — `WEAKNESS_BLOB_ENVELOPE_ENABLED` is
  not `true`. Enabling it without a database pool or without a decodable base64
  AES-256 key in the variable named by `WEAKNESS_BLOB_KEK_ENV` makes the service
  refuse to boot — deliberately, so it never stores plaintext by accident.
- **Weakness uploads fail with a bucket error** — the bucket name must match what
  exists. The stack uses `weakness-uploads`, created by `garage-init`; the code's
  documented default is `chora-weakness-uploads`, which `garage-init` does not
  create.
- **A gRPC call returns `Unimplemented`** — only `RecommendAtomsForLearner` is wired.
  The others are generated stubs awaiting domain wiring.
- **`go test ./...` fails on repository adapters** — the PostgreSQL adapters expect a
  live database; the in-memory implementations are what the unit suite exercises.
- **Traces never reach Tempo** — `OTEL_EXPORTER_OTLP_ENDPOINT` is set with an
  `http://` scheme in this stack, which the exporter rejects (`too many colons in
  address`). Every service in the stack logs this; nothing but tracing is affected.

## API surface

Two route families share one listener: a legacy/service set, and the newer `/v1` and
domain routes.

**Legacy and service routes**

| Method | Path |
| --- | --- |
| `GET` | `/healthz`, `/health`, `/readyz` |
| `POST` | `/api/learning-paths` |
| `GET` | `/api/learning-paths`, `/api/learning-paths/{id}` |
| `POST` | `/api/sessions` |
| `PATCH` | `/api/sessions/{id}/complete` |
| `GET` | `/api/companions/{gcid}` |
| `POST` | `/api/companions/{gcid}/xp` |

**EXT route tree** (mounted without the `/api` prefix)

| Method | Path |
| --- | --- |
| `POST` | `/learning-paths`, `/learning-paths/{id}/enrollments` |
| `GET` | `/learning-paths/{id}/progress?gcid=X` |
| `POST` | `/sessions` |
| `PATCH` | `/sessions/{id}/answer` |
| `POST` | `/sessions/{id}:abandon` |
| `GET` | `/sessions/{id}` |
| `GET` | `/knowledge-graph/discover?topic=X&depth=N` |
| `GET` | `/me/knowledge-graph/heatmap` |

The `/v1` and domain routes cover learner-scoped learning paths and atom sessions,
retention, practice budget, transcripts, Growth Edges and uploads, goals, maps,
concept-graph authoring and suggestions, course content, Companion acquisition and
bindings, Knowledge Graph canvas operations, and tenant-scoped KG routes. Handlers
take tenant and learner context from the request (`X-Tenant-Id` plus the GCID
context) and the RLS adapters apply it to every query.

## Architecture

```text
cmd/server/        entrypoint and production wiring
internal/domain/   domain models and ports
internal/adapter/  HTTP, gRPC, PostgreSQL, in-memory, event and external-service adapters
config/            runtime YAML configuration
migrations/        PostgreSQL schema
```

Hexagonal, with the same split every Chora Go service uses: domain ports are
implemented twice — a PostgreSQL adapter for production and an in-memory adapter for
unit tests and dependency-free local runs. That duality is what makes the
"no database" quickstart honest rather than a stub.

The service is an unusually heavy *consumer* of its peers, which is visible in the
Compose wiring: identity for mana, delivery for courses, creation for atoms, the model
gateway for embeddings, and the AI kernel for dispatch. A failure in any of them
surfaces here as a stalled feature rather than a startup error, because those clients
are wired lazily.

## Events

Domain events go through a PostgreSQL transactional outbox and are then published to
NATS JetStream, so a crash between the state change and the publish replays instead
of losing the event.

| Concern | Setting |
| --- | --- |
| Outbox store | `chora_consumption` (same database as the service) |
| Dispatcher identity | `CHORA_OUTBOX_WORKER_ID` |
| Broker | `NATS_URL`; unset falls back to the in-process bus |
| Streams | `CHORA_EVENTS` for normal subjects, `CHORA_DLQ` for dead letters |
| Source label | `CHORA_SOURCE_PROJECT` on every envelope |

The service is both a producer and a consumer: several features subscribe to subjects
produced by sibling services, which is why `nats-init` is a hard dependency rather
than a convenience.

## Migrations

**101 forward migration files**, applied by the stack's `scripts/migrate.sh` in the
`consumption-migrate` one-shot against `chora_consumption`. The runner is not part of
this repository.

The set runs from `0001_initial.sql` and `0002_per_user_knowledge_graph.sql` through
to `0121_embedding_dimensions_1024.up.sql` and `9999_grant_app_roles.sql`, and is the
largest in the platform — it covers learning paths, sessions, Companion state, goals,
Growth Edges, uploads, the Knowledge Graph, the concept graph, campaigns, skills and
retention.

Two files carry non-obvious requirements:

- **`0121_embedding_dimensions_1024`** widens every embedding column to the
  embedding route's native width. pgvector cannot cast between widths, so `ALTER` on
  a populated column discards the stored vectors; on this deployment it is a pure
  schema change because the columns were empty, and a populated one would need a
  re-embed pass.
- **`9999_grant_app_roles.sql`** carries the grants for the `_app_rw` role. Without
  it the service connects but every query fails on permissions.

The `*.down.sql` siblings are destructive rollbacks and are never applied: the runner
takes every `*.sql` except `*.down.sql`, records a SHA-256 checksum per file in
`schema_migrations`, and refuses a file whose contents changed after it was applied.
Because the set is this large, a partially applied run is worth checking against
`schema_migrations` before re-running — the runner is not wrapped in a single
transaction, so earlier files stay committed when a later one fails.
