# Validation suites

Every suite of the validation program, the command that runs it and where it
runs. The matrix is machine-checked: `scripts/verify-suite-matrix.py` asserts
that every command listed here is wired into a workflow and that every test
command in the workflows is listed here, so the matrix cannot drift away from
what CI actually executes.

| Suite | Command | Runs on |
|---|---|---|
| Format | `test -z "$(gofmt -l $(git ls-files '*.go'))"` | pull request |
| Vet | `go vet ./...` | pull request |
| Requirement inventory | `python3 scripts/generate-tracker.py --check` | pull request |
| Requirement inventory tests | `python3 scripts/test_generate_tracker.py` | pull request |
| Unit and contract tests | `go test -race ./...` | pull request |
| Conservative-planning end-to-end | `go test -tags=e2e ./tests/e2e/...` | pull request |
| Database integration | `go test -tags=integration ./internal/persistence ./internal/service` | pull request, nightly |
| Performance and service levels | `go test -tags performance ./tests/performance/...` | pull request, nightly |
| Historical replay | `go test ./tests/historical-replay/...` | pull request, nightly |
| Native adapter fixtures | `go test -tags native ./tests/native/...` | nightly |
| Vulnerability check | `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` | pull request |
| SBOM | `go run github.com/anchore/syft/cmd/syft@v1.41.1 dir:. -o spdx-json=ivp.spdx.json` | pull request |

## What runs where

- **Pull request** — every suite that needs no external service: the unit,
  contract, end-to-end, performance, historical-replay, format, vet,
  inventory, vulnerability and SBOM checks. The database-backed suite runs
  against a PostgreSQL service container.
- **Nightly** — the native adapter fixtures over the checked-in sample
  projects with the real Gradle, Maven, Bazel, Node and Python toolchains
  installed, plus the database-backed, performance and historical-replay
  suites.
- **Local** — the same pull-request suites run with `go test ./... -count=1`
  plus `go vet ./...`, `go vet -tags integration ./...` and
  `go build ./...`. The database-backed suite runs locally when a PostgreSQL
  is reachable (the integration suites start one through testcontainers); the
  native fixtures need the native toolchains and skip when they are absent.

## Environment-gated suites

The suites that cannot run on every machine are gated by their environment and
say so in their output rather than silently passing:

- the database-backed suites need PostgreSQL (testcontainers starts one);
- the native fixture suite skips per adapter when its tool is not installed;
- the performance measurements assert the LLD §15.4 budgets and can be
  retuned for a slower reference machine through `IVP_SLO_PLAN_CREATION_P95_MS`
  and `IVP_SLO_INDEXING_P95_MS`.
