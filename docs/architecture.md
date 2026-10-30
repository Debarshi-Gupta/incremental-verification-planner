# Architecture

IVP is a modular monolith control plane and local CLI executor. The API stores tenant-scoped immutable snapshots, evidence, policies, plans, executions, and audits in PostgreSQL. The CLI scans the repository, builds adapter evidence, requests a plan, materializes adapter-owned argv locally, revalidates the worktree, executes checks, sanitizes results, and uploads aggregate metadata. The API never executes repository commands and never receives repository source blobs.

The domain and analysis packages are shared by the API and CLI. The planner is deterministic and versioned. An incomplete, stale, unsupported, or conflicted graph reduces optimization or requires full verification. A targeted pass is reported as `TARGETED_PASS`; only all required configured checks can produce `FULL_VERIFICATION_PASS`.

## Trust boundaries

- Repository files, build tools, test output, paths, and adapter reports are untrusted.
- The CLI runs commands as the caller, with argument arrays, bounded output, an environment allowlist, and repository-root containment.
- The API accepts authenticated, validated metadata and sanitized reports only.
- Database queries scope every resource through tenant membership and immutable parent relations.
- Identity roles come from server-managed membership, not untrusted token role claims.

## Data flow

`workspace scan → adapter evidence → immutable snapshot → plan request → deterministic policy and analysis → local target materialization → execution → sanitized result upload → comparable audit and calibration`.

Sanitized JSON/JUnit XML reports are optional artifacts associated with an execution. The API sanitizes them, stores tenant-scoped metadata in PostgreSQL, and writes the bounded object to a filesystem or S3-compatible store. Plan creation and outcome do not depend on object availability. See [ADR 0001](adr/0001-report-artifact-storage.md) for limits and sanitization behavior.

When server policy or evidence is unavailable or untrusted, the safe outcome is the repository's configured full-suite command. A signed, unexpired, algorithm-compatible local policy can be used only where the LLD permits it.

