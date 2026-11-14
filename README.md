# Incremental Verification Planner (IVP)

IVP is a conservative verification planner with a Go control plane and a local CLI executor. It uses repository graph, coverage, policy, and history evidence to recommend checks, but a targeted pass never implies that omitted tests pass. If evidence, policy, or execution state is unsafe, IVP selects the configured full-verification path.

## Status

The LLD-complete implementation is finished: the requirement inventory holds
**853 rows, all reviewed, with 0 not-started and 0 in-progress** — 510 `TESTED`
and 343 `IMPLEMENTED`, an implementation-coverage lower bound of **100.0%** and
a verified-completion lower bound of **59.8%**. The current scores are
generated into [`docs/implementation-tracker.md`](docs/implementation-tracker.md),
and the per-package evidence is recorded in
[`docs/implementation-log.md`](docs/implementation-log.md). The rows that are
`IMPLEMENTED` rather than `TESTED` are the ones whose criterion needs an
operator environment (a production month of availability measurements, S3
recovery, a native-toolchain run); each carries its limitation.

Selective final-CI gating stays disabled by default, and the deviations from
the service levels are published in [`docs/slo-report.md`](docs/slo-report.md).

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

