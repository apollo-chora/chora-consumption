# chora-consumption

Content Consumption domain service for Chora — one of the 5 core CHORA domains.
Owns the learner-facing consumption aggregates: `LearningPath`, `AtomicSession`,
`Companion` (the RPG companion **domain entity**, not the AI agent),
`KnowledgeGraph`, `Goal`, `Campaign`, `Dose`, `Weakness`, and the per-learner
`LearnerProfile` / `StudentTranscript` read models.

The service is cloud-neutral: PostgreSQL for persistence, NATS JetStream for
events, an S3-compatible object store for weakness blobs, env-backed
configuration for secrets. No cloud account or managed services are required.

## What it does

1. **HTTP API** (`:8080`) — learner-facing reads/writes: learning paths,
   sessions, companions, daily dose, proofing tests, dose preferences,
   concept graph, streak/XP, weakness uploads, growth edges.
2. **gRPC API** (`:9090`) — synchronous service calls (CompanionGrowth,
   Consumption, FogOrchestrator, KnowledgeGraph surfaces) for the BFF.
3. **Event consumers** — cross-domain subscribers bound to the NATS event
   bus: enrollment-created, atom-created/published/updated, course
   content/metadata, learner-profile, campaign XP, companion growth,
   weakness outputs, KG fog invalidation, closure pseudonymisation.
4. **Outbox publisher** — domain events are written to
   `chora_consumption.outbox_events` transactionally, then drained to the
   event bus by the outbox dispatcher (binary protobuf payloads, JSON
   fallback for topics without an encoder).

## Architecture

- **Compute**: any host running the Go binary or the container image.
- **Database**: PostgreSQL (`chora_consumption`). Schema changes live in
  `migrations/` and are applied with the shared migration runner.
- **Event bus**: NATS JetStream (stream `CHORA_EVENTS`, DLQ stream
  `CHORA_DLQ`). Subscribers bind via `chora-common/eventbus` consumers.
- **Object store**: S3-compatible (MinIO locally) for weakness upload blobs,
  encrypted per-blob with AES-256-GCM (DEK wrapped by `CHORA_BLOB_KEK_ENV`).
- **Ports**: HTTP `:8080`; gRPC `:9090`.

## Configuration

Copy the example environment file:

```sh
cp .env.example .env
```

Important variables:

| Variable | Purpose | Local default |
| --- | --- | --- |
| `PORT` | HTTP port | `8080` |
| `CHORA_GRPC_PORT` | gRPC port | `9090` |
| `CHORA_DB_DSN` | PostgreSQL connection string (app_rw role) | Compose PostgreSQL |
| `CHORA_OUTBOX_DSN` | Outbox PostgreSQL connection string | same as `CHORA_DB_DSN` |
| `NATS_URL` | NATS JetStream event bus | `nats://nats:4222` |
| `CHORA_SOURCE_PROJECT` | Project label stamped into event envelopes | `chora-local` |
| `CHORA_ENVIRONMENT` | Environment label (dev/staging/prod) | `dev` |
| `WEAKNESS_BLOB_KEK_ENV` | Env var name holding the base64 AES-256 KEK | unset (blobs disabled) |
| `WEAKNESS_UPLOADS_BUCKET` | S3 bucket for weakness uploads | `chora-weakness-uploads` |
| `CHORA_AGENT_GUARDRAIL_MAPPING` | Agent→tier guardrail mapping YAML | `config/agent-guardrail-mapping.yaml` |
| `CHORA_PII_CLOSURE_MAP_PATH` | PII closure map YAML | `config/PII_Closure_Map.yaml` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC trace endpoint | `http://otel-collector:4317` |

## Run locally

```sh
go run ./cmd/server
```

With `CHORA_DB_DSN` and `NATS_URL` set, the service wires the pg repos, the
outbox dispatcher, and the event-bus subscribers. Unset, it runs on in-memory
adapters (not durable).

## Database migrations

Forward migrations are every `migrations/*.sql` except `*.down.sql`. Apply
them with the shared runner from `chora-stack/scripts/migrate.sh` (mount the
repo's `migrations/` at `/migrations` and set `CHORA_MIGRATE_DSN`).

## Tests

```sh
go build ./...
go test ./...
```

The test suite is hermetic (in-memory adapters, no external services needed).
