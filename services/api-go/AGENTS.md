# api-go — Implementing Agent Guide

## Role

`api-go` is a **clean-room Go + Temporal** re-implementation of the Centaur
durable control plane (`services/api-rs`).  The Rust implementation continues
to run in production while parity is established; do **not** modify
`services/api-rs` as part of this work.

## What you must deliver

1. Go packages under `internal/` that make every test in this directory tree
   pass (`go test ./...`).
2. A Temporal Worker binary under `cmd/worker/` that drives the session
   workflow and activity set.
3. An HTTP server binary under `cmd/api/` that exposes the same HTTP contract
   documented in `SPEC.md §3`.
4. Database migrations under `migrations/` that are a strict superset of the
   ones the existing Rust implementation uses (same schema, same column names,
   same Postgres NOTIFY payloads).

## Behaviour contract

`SPEC.md` is the single source of truth for every observable behaviour.  The
test files in this directory encode those behaviours as Go table-driven tests.
The test files are the oracle — make the tests pass; do **not** change the
assertions.

## Architecture boundaries you must preserve

- **Do not change** the HTTP API surface (`/api/session/{thread}`, `/api/session/{thread}/messages`,
  `/api/session/{thread}/execute`, `/api/session/{thread}/events`).
- **Do not change** the Postgres schema column names or the NOTIFY channel name
  (`centaur_session_events`).
- **Do not add** ingress/platform knowledge (Slack, GitHub, Discord) to this
  service.  Ingress stays in the existing TypeScript bots.
- **Do not add** sandbox implementation knowledge (Kubernetes CRDs, iron-proxy
  configuration) to this service; use the `SandboxBackend` interface defined in
  `internal/sandbox/`.
- Credentials must never appear in log output, durable event payloads, or
  commit history.

## Tech stack

- Go 1.23+
- `go.temporal.io/sdk` for durable workflows and activities.
- `github.com/jackc/pgx/v5` for Postgres.
- `net/http` or `github.com/go-chi/chi` for the HTTP router.
- `go.uber.org/zap` for structured logging.
- Standard library `testing` + `github.com/stretchr/testify` for tests.

## Validation

```bash
go build ./...
go vet ./...
go test ./...                                  # unit + non-DB integration
SESSION_RUNTIME_TEST_DATABASE_URL=<pg-url> go test ./internal/store/... -run Integration
CENTAUR_API_URL=http://127.0.0.1:18080 go test ./e2e/... -run TestAPI
SANDBOX_E2E_IMPLS=local go test ./e2e/... -run TestSandbox -v
```

## Migration gate

A PR may only land when:

1. All unit tests pass without any environment variables.
2. All DB integration tests pass against a real Postgres URL.
3. All API E2E tests pass with a running `cmd/api` binary and a real Postgres.
4. The existing Rust `cargo test --workspace` (in `services/api-rs`) still
   passes unchanged — no Rust code may be modified.

## Directory map

```
services/api-go/
├── AGENTS.md                  ← this file
├── SPEC.md                    ← full behaviour specification
├── go.mod
├── cmd/
│   ├── api/main.go            ← HTTP server entry point
│   └── worker/main.go         ← Temporal worker entry point
├── internal/
│   ├── session/               ← wire types (ThreadKey, HarnessType, etc.)
│   ├── runtime/               ← session lifecycle, output parsing, cleanup
│   ├── store/                 ← Postgres repository
│   └── sandbox/               ← SandboxBackend interface + local impl
├── workflows/                 ← Temporal workflow definitions
├── e2e/                       ← black-box API and sandbox E2E tests
└── migrations/                ← SQL migration files (copy from api-rs)
```
