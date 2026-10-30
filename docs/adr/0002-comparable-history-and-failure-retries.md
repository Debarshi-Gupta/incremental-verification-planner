# ADR 0002: Comparable history windows and failed-test retries

## Status

Accepted

## Context

The LLD requires bounded, comparable test history, conservative duration estimates, and a retry policy that defaults to one retry (two total attempts). It does not define a history-window field or a repository-config key for execution retries. History from different test configurations or environments must not be treated as comparable.

## Decision

- Use a 180-day default `analysis.history_window_days` for test-history planning. The organization baseline owns the maximum; repository configuration may shorten the window but may not extend it.
- Require matching environment and test-configuration digests before history informs duration, failure, or flake estimates. Legacy rows without a test-configuration digest are excluded.
- Use `execution.failure_retry_count`, defaulting to one and bounded to zero through three. An organization may change the baseline; repository configuration cannot lower the organization value.
- Preserve every execution attempt in `execution_unit_results`. A failure in any attempt remains a failed verification outcome even when a retry passes. Store one history sample per unit per execution, with summed comparable-attempt duration and `FLAKY` classification when attempts disagree.
- Keep coverage freshness governed by `analysis.coverage_staleness_days`; the test-history window does not make old coverage fresh.

## Alternatives considered

1. Use a fixed history period with no configuration. Rejected because repositories can have materially different release and test cadences.
2. Let repository configuration extend the organization history window or reduce required retries. Rejected because local settings must not weaken organization-level evidence policy.
3. Count each retry attempt as a separate duration sample. Rejected because this would overstate the number of independent executions and distort percentile estimates.
4. Convert a failed first attempt into a passing verification when a retry passes. Rejected because the LLD requires preserving failure evidence and forbids another command from turning a failing selected run into a pass.

## Consequences

History estimates use only comparable recent observations and can remain unavailable for legacy or low-sample tests; the planner therefore retains its conservative fallback. Retries add execution cost, and mixed outcomes lower reliability through the recorded flake rate while the original failed run remains a failed gate. Validation of the new behavior is deferred to the separate test and validation task.
