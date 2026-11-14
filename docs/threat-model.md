# Threat model

Scope: the IVP server, the CLI/executor and the artifacts they produce. The
assets, the trust boundaries, the threats and the mitigations that are
implemented today are listed here; every mitigation names the code that
implements it, so the model cannot drift away from the product.

## Assets

| Asset | Why it matters |
|---|---|
| Verification plans and their explanations | a plan decides which checks a change may skip |
| Evidence (snapshots, graph, coverage, test history) | a plan is only as trustworthy as its evidence |
| Execution results and the aggregate outcome | the outcome decides whether a change is accepted |
| Audit records and quarantines | the audit trail is the compliance record |
| Stored artifacts (sanitized reports) | they may contain test output |
| Organization policy and its signature | the policy is the authority for every safety gate |
| Tenant identity and the repository registry | the tenant boundary is the isolation boundary |

## Trust boundaries

1. **Repository to server.** The repository is untrusted: a change set, a
   snapshot, a coverage report and a plan request all arrive from it.
2. **CLI to server.** The CLI authenticates with an OIDC identity token; the
   server resolves the tenant membership server-side.
3. **Server to database.** The application connects as a database role; every
   tenant-owned table carries `tenant_id` and a row-level security policy.
4. **Executor to the build system.** The executor runs adapter-materialized
   argv inside the workspace; the command arguments and the environment are
   the boundary.
5. **Execution to the server.** Results are uploaded with the execution
   identity and are bound to the plan, the snapshot and the environment
   fingerprint.

## Threats and mitigations

| Threat | Mitigation | Where |
|---|---|---|
| A repository convinces the server that a risky change is safe | hard escalation rules, a signed policy, and full verification as the fallback | `internal/analysis/planner.go`, `internal/service/backend.go` |
| A stale snapshot or an incomparable environment is used to justify a selective plan | snapshot identity, environment fingerprint comparison, staleness thresholds, evidence comparability gates | `internal/persistence/plans_executions.go`, `internal/service/plan_gates.go` |
| Evidence from a previous dependency or adapter version is used | dependency digest on the snapshot and the coverage, adapter-version compatibility check | `internal/domain/coverage_comparability.go`, `internal/domain/adapter_compat.go` |
| A cross-tenant read or write | tenant-scoped queries, row-level security (forced), tenant-neutral error responses | `internal/persistence/*`, `internal/api/artifacts.go` |
| A credential or a source file leaks into a payload, a log, a telemetry label or an artifact | central redaction (secrets, source paths, credential shapes), bounded metric labels, artifact sanitization, the unified leak regression | `internal/security/redaction.go`, `internal/service/leak_regression_test.go` |
| An execution escapes the workspace or leaks the environment | allowed executable list, environment allowlist, workspace-confined paths, bounded output | `internal/execution/runner.go`, `internal/adapters/reports.go` |
| A user is tricked into running an unsafe command | the plan is materialized by the adapter, the full-suite fallback is printed instead of executed on a policy failure | `internal/cli/run.go` |
| An audit miss is ignored | quarantine in the same transaction, acknowledgement SLA, alerting | `internal/audit/service.go`, `internal/persistence/audits.go` |
| A malicious artifact is uploaded | size bounds, sanitization, SHA-256 verification, per-tenant object keys | `internal/service/artifacts.go`, `internal/artifact/store.go` |
| The build system output is unbounded | output byte caps, concurrency caps, report size caps | `internal/execution/runner.go`, `internal/adapters/reports.go` |
| A replayed or duplicated request repeats a side effect | idempotency keys and an idempotency table | `internal/api/server.go`, `internal/persistence/*` |

## Residual risk

- The redaction of a credential is pattern-based; an unknown credential shape
  is not scrubbed, and the artifact sanitizer remains the last line of defence.
- The build system itself is trusted to describe its own test set; a build
  that hides a test cannot be detected from the outside.
- The availability and latency objectives of LLD §15.4 are measured by the
  repository's own pipeline, so the numbers are only as good as the reference
  machine they were taken on (see `docs/slo-report.md`).

## Review

The model is reviewed with every change that adds a surface (a new endpoint, a
new adapter capability, a new configuration key) and at every release; the
release checklist requires the review to be repeated and recorded.
