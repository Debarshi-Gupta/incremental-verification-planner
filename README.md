# Incremental Verification Planner (IVP)

IVP is a conservative verification planner with a Go control plane and a local CLI executor. It uses repository graph, coverage, policy, and history evidence to recommend checks, but a targeted pass never implies that omitted tests pass. If evidence, policy, or execution state is unsafe, IVP selects the configured full-verification path.

## Status

This repository is under implementation against the production specification in the supplied Low-Level Design. See [`docs/implementation-tracker.md`](docs/implementation-tracker.md) for feature and validation status. Selective final-CI gating is disabled by default.

## Requirements

- Go 1.27.3 (pinned in `go.mod` and CI)
- PostgreSQL 16 or newer for the API
- Linux or macOS for selective local execution; Windows runs refuse selective execution and print the configured full-suite fallback
- Docker Compose and Helm only for their respective deployment workflows

## Local API quick start

The Compose development profile enables the explicitly gated local identity, applies database migrations, and provisions the configured local tenant and user. It is for local development only.

```sh
docker compose --env-file .env.example up --build
```

The API listens at `http://localhost:8080`. Check readiness with `curl http://localhost:8080/health/ready`. Authenticated local requests must include `X-Tenant-ID: 11111111-1111-4111-8111-111111111111`. Do not use the sample development identity or database password in a shared or production environment.

## CLI quick start

```sh
go run ./cmd/ivp version
go run ./cmd/ivp init
go run ./cmd/ivp doctor
```

For production, configure OIDC and PostgreSQL using the secret-backed environment variables documented in `docs/operations.md`, apply migrations as a deployment step, and start `ivp-api`.

## Development

```sh
make fmt
make test
make test-integration
make build
```

See [`docs/operations.md`](docs/operations.md) for database, container, backup, and recovery procedures and [`docs/adapter-support.md`](docs/adapter-support.md) for adapter limitations.

