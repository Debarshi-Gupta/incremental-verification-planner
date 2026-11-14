# ADR 0003: Discovery-time report paths use globs over hashed report files

## Status

Accepted

## Context

The LLD requires that report artifacts be discoverable, tamper-evident, and stale-proof, and that selective execution correlate per-unit results with the reports those runs produce. The execution layer materializes pytest and jest reports at content-hashed paths (`.ivp/reports/pytest-<hash>.xml`, `.ivp/reports/jest-<hash>.json`) so concurrent or repeated runs cannot overwrite each other's evidence and every uploaded artifact has a stable digest. Discovery, however, advertised fixed report names (`.ivp/reports/pytest.xml`, `.ivp/reports/jest.json`) in each unit's `report_paths` metadata. Those fixed names are never produced by any execution, so unit-level report correlation, full-suite report collection, and artifact upload silently found nothing for those two adapters. The selective path cannot be known at discovery time because it is derived from the materialized command's target key at execution time.

## Decision

- Discovery emits a glob over the materialized report directory instead of a fixed name: `.ivp/reports/pytest*.xml` for pytest and `.ivp/reports/jest*.json` for jest, matching the existing gradle/maven `TEST-*.xml` precedent.
- Every consumer expands `report_paths` through `filepath.Glob` (`execution.expandReportPaths`, `FingerprintReports`, `uploadLocalArtifacts`) and only ever parses paths returned by `FreshReports`, which fingerprints files before and after a command and drops byte-identical stale matches. Parsers therefore never consume a leftover report from a prior run.
- Report paths remain workspace-confined: expansion resolves each match with `ResolveWithin`, so a glob cannot escape the repository root.
- Scope the pattern to the adapter's own report prefix (`pytest*`, `jest*`) so one adapter's glob never picks up another adapter's artifacts in the shared `.ivp/reports/` directory.

## Alternatives considered

1. Unify on the hashed path at discovery. Rejected because the hash input is only known once the execution command is materialized; discovery cannot compute it without duplicating execution-time state.
2. Rewrite unit metadata with the concrete hashed path after execution. Rejected because it changes discovery output retroactively, breaks plan/unit identity stability across runs, and leaves the pre-execution metadata wrong for report previews and full-suite collection.
3. Stop hashing report filenames and write fixed names. Rejected because it destroys concurrent-run isolation and the tamper-evident per-artifact identity required for upload and audit evidence.
4. Parse whatever files exist under the prefix without freshness checks. Rejected because stale reports from a prior run would be attributed to the current execution; freshness fingerprinting is mandatory.

## Consequences

pytest and jest unit results, full-suite collection, and artifact uploads now locate real reports. Consumers must keep treating `report_paths` as patterns rather than literal paths; code that assumes a single concrete file would regress, so the glob contract is enforced by the shared expansion helpers. A report that matches the glob but was not rewritten by the current command is filtered out by `FreshReports`, preserving the stale-report guarantee.
