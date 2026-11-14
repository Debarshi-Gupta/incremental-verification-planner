# Bounds review

LLD §22 requires that no unbounded query, graph traversal, process spawning,
output buffering or in-memory artifact accumulation is possible. This document
lists every bound that implements that requirement, the value it uses and the
code that enforces it. A bound that is not listed here does not exist.

## Graph queries and traversal

| Bound | Value | Where |
|---|---|---|
| Traversal node budget | 100,000 nodes per plan | `domain.Policy.TraversalLimits.MaxNodes`, `internal/analysis/graph.go` |
| Traversal depth budget | 64 dependency hops | `domain.Policy.TraversalLimits.MaxDepth`, `internal/analysis/graph.go` |
| Changed-file budget | `max_changed_files_for_selective_plan` (default 500) | `internal/analysis/planner.go` |
| Affected-target budget | the changed-file budget (ADR 0018) | `internal/analysis/planner.go` |
| Derived-index cache size | 8 snapshots, least-recently-used eviction | `internal/analysis/derived_index.go` |
| Dependency fanout escalation | 250 | `domain.Policy.DependencyFanoutEscalation`, `internal/analysis/graph.go` |
| Dependency cap per entity | 100 | `domain.Policy.DependencyCap`, `internal/analysis/graph.go` |
| Reverse fanout per entity | 100 | `internal/analysis/graph.go` |

A reached traversal bound marks the graph incomplete and escalates the plan
(`GRAPH_INCOMPLED`): the traversal is never silently truncated, and a
truncated result never becomes a confident selective plan.

## Process spawning

| Bound | Value | Where |
|---|---|---|
| Concurrent commands per runner | `MaxConcurrent` (default 1) | `internal/execution/runner.go` |
| Command timeout | per command, 30–60 minutes by adapter | `internal/adapters/*`, `internal/execution/runner.go` |
| Termination grace | bounded, then the process group is killed | `internal/execution/runner.go` |
| Retry count | `execution.failure_retry_count` (0–3) | `internal/config/validate.go` |
| API request retries | bounded with backoff | `internal/cli/client.go` |
| Artifact upload concurrency | 2 concurrent uploads per API | `internal/api/server.go` |

## Output buffering

| Bound | Value | Where |
|---|---|---|
| Captured command output | `MaxOutputBytes` (default 1 MiB) | `internal/execution/runner.go` |
| Report read limit | `adapters.DefaultReportLimit` | `internal/adapters/reports.go` |
| Artifact upload limit | `MaxBytes` (default 16 MiB, hard maximum `MaxAllowedBytes`) | `internal/artifact/store.go` |
| API body limit | `MaxBodyBytes` (24 MiB) | `internal/api/server.go` |
| Telemetry label values | bounded allowlist, never free text | `internal/telemetry/metrics.go` |
| Log and error output | bounded by `security.Redact` | `internal/security/redaction.go` |

## Artifact accumulation

| Bound | Value | Where |
|---|---|---|
| Stored artifact size | 16 MiB per object, streaming-limited on write | `internal/artifact/store.go` |
| Artifact retention | `safety.audit.retention` window | `internal/service/artifacts.go` |
| Retention batch size | 250 rows per batch | `internal/persistence/artifacts.go` |
| Expired-artifact batch size | 250 rows per batch | `internal/persistence/artifacts.go` |
| Artifact count per tenant | bounded by the retention window | `internal/persistence/retention.go` |
| Raw test-history retention | configured window, aggregated history kept | `internal/persistence/retention.go` |
| Plan and execution payloads | bounded by the API body limit | `internal/api/server.go` |

## What is deliberately not bounded

- The immutable raw graph itself: a snapshot is stored as it was produced, and
  its size is bounded by the API body limit and the upload size limits rather
  than by a row count.
- The coverage history of a snapshot: it is loaded per plan for the selected
  unit keys only, and the loader is bounded by the snapshot's unit set.

Each bound above is exercised by a test in the package that owns it, and the
plans that reach a bound are escalated rather than truncated.
