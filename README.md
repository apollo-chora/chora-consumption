# chora-consumption

## About

chora-consumption is the Go service that owns Chora's learner-facing consumption data and APIs. It serves learning paths, atom sessions, daily-dose and retention data, Companion state, goals, Growth Edges, transcripts, and per-user Knowledge Graph data, with additional Companion chat, memory, rituals, skills, and campaign surfaces. The service persists to PostgreSQL, publishes domain events through a transactional outbox to NATS JetStream, and can use an S3-compatible object store for encrypted weakness uploads.

## Quick start

Prerequisites:

- Go 1.26.1 or newer
- PostgreSQL for durable operation
- NATS JetStream for event publishing and subscribers when running the full service stack
- An S3-compatible object store when weakness blob uploads are enabled

The repository is a standalone Go module:

```sh
git clone https://github.com/apollo-chora/chora-consumption.git
cd chora-consumption

go build ./...
go test ./...
```

For local configuration, start from the checked-in example:

```sh
cp .env.example .env
```

The example uses PostgreSQL at `postgres:5432` and NATS at `nats:4222`. Set `CHORA_DB_DSN` and `CHORA_OUTBOX_DSN` to reachable PostgreSQL DSNs when running outside that network. Set `NATS_URL` to a reachable NATS JetStream server when event delivery is required.

## Usage

Run the service with:

```sh
go run ./cmd/server
```

The HTTP server listens on `PORT`, defaulting to `8080`. The gRPC server listens on `CHORA_GRPC_PORT`, defaulting to `9090`.

Health endpoints:

```
GET /healthz
GET /health
GET /readyz
```

The HTTP API has two route families on the same listener.

Legacy and service routes include:

```
POST   /api/learning-paths
GET    /api/learning-paths/{id}
GET    /api/learning-paths
POST   /api/sessions
PATCH  /api/sessions/{id}/complete
GET    /api/companions/{gcid}
POST   /api/companions/{gcid}/xp
```

The newer `/v1` and domain routes include learner-scoped learning paths and atom sessions, topic retention, practice budget, transcripts, Growth Edges and uploads, goals, maps, concept graph authoring and suggestions, course content, Companion acquisition and bindings, Knowledge Graph canvas operations, and tenant-scoped Knowledge Graph routes. The EXT route tree also exposes:

```
POST  /learning-paths
POST  /learning-paths/{id}/enrollments
GET   /learning-paths/{id}/progress?gcid=X
POST  /sessions
PATCH /sessions/{id}/answer
POST  /sessions/{id}:abandon
GET   /sessions/{id}
GET   /knowledge-graph/discover?topic=X&depth=N
GET   /me/knowledge-graph/heatmap
```

The repository's HTTP handlers use tenant and learner context from the request, including the `X-Tenant-Id` and GCID context used by the service's routing and RLS adapters.

The gRPC server registers `chora.services.consumption.v1.Consumption`, `KnowledgeGraphService`, the gRPC health service, and `CompanionGrowth` when its production wiring is available. `RecommendAtomsForLearner` is implemented against the local atom-index projection; other contract-registered RPCs currently use the generated gRPC `Unimplemented...` implementations until their domain wiring is added.

Important runtime configuration:

| Variable | Purpose | Default |
| --- | --- | --- |
| `PORT` | HTTP listener port | `8080` |
| `CHORA_GRPC_PORT` | gRPC listener port | `9090` |
| `CHORA_DB_DSN` | PostgreSQL DSN for the consumption database | unset |
| `CHORA_OUTBOX_DSN` | PostgreSQL DSN for the transactional outbox | unset |
| `CHORA_OUTBOX_WORKER_ID` | Outbox dispatcher worker ID | `HOSTNAME` or local fallback |
| `NATS_URL` | NATS JetStream URL | unset |
| `CHORA_SOURCE_PROJECT` | Event envelope source-project label | `chora-local` |
| `CHORA_ENVIRONMENT` | Environment label | `dev` |
| `WEAKNESS_BLOB_KEK_ENV` | Name of the environment variable containing the base64 AES-256 KEK | unset |
| `WEAKNESS_UPLOADS_BUCKET` | S3 bucket for weakness uploads | `chora-weakness-uploads` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC trace endpoint | `http://otel-collector:4317` |

When `CHORA_DB_DSN` and `CHORA_OUTBOX_DSN` are unset, the service uses in-memory repositories instead of a PostgreSQL connection. When `NATS_URL` is unset, the outbox dispatcher falls back to the in-process event bus and subscribers are not bound to a durable broker.

The container image is built from the repository root with the included Dockerfile. CI publishes multi-architecture images as `walfa/chora-consumption`, including `latest` for pushes to `main` and SHA-tagged images for builds.

## Development

The project uses the standard Go module layout:

```
cmd/server/       service entrypoint and production wiring
internal/domain/  domain models and ports
internal/adapter/ HTTP, gRPC, PostgreSQL, in-memory, event, and external-service adapters
config/           runtime YAML configuration
migrations/       PostgreSQL schema migrations
```

Build and test with:

```sh
go build ./...
go test ./...
```

The test suite is organized alongside the implementation under `cmd/server/` and `internal/`. Repository adapters have both PostgreSQL implementations for production wiring and in-memory implementations used by unit tests and local operation without a database.

Database migrations live under `migrations/`. The files are numbered and include both forward and, where applicable, down migrations. The repository expects them to be applied with the shared Chora migration tooling rather than by the service binary itself.

For container builds, the repository's Dockerfile uses a Go builder stage and a distroless non-root runtime image:

```sh
docker build -t chora-consumption .
```
