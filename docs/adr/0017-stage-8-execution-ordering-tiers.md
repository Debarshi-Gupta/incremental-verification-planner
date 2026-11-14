# ADR 0017: Stage-8 execution ordering is a six-tier ranking with utility as the intra-tier key

## Status

Accepted

## Context

LLD line 514-520 (§6.3 Stage 8) orders selected units: mandatory gates first, then "checks with the highest expected fault-detection value per second", then direct tests, high-risk dependency consumers, integration/E2E tests, and finally remaining tests. The LLD does not define "high-risk dependency consumer" precisely, nor how ties inside a tier break. Before this change `makePlanUnits` sorted only on `mandatory → utility → direct → stable key`, so direct tests could precede checks and integration tests had no place at all.

## Decision

**1. `planExecutionTier` assigns each candidate exactly one tier** (`internal/analysis/planner.go`):

| Tier | Meaning | Condition |
|---|---|---|
| 0 | mandatory gates | `candidate.mandatory` |
| 1 | checks by fault-detection value per second | `UnitType == UnitCheck` (utility ranks inside the tier) |
| 2 | direct tests covering changed lines/symbols | `candidate.direct` |
| 3 | high-risk dependency consumers | see rule 2 |
| 4 | integration/E2E tests | reason `INTEGRATION_BOUNDARY` present |
| 5 | remaining selected tests | everything else |

The switch order is deliberate: a mandatory check stays a gate (tier 0), a direct check still ranks with checks (tier 1 precedes tier 2), mirroring the LLD's "mandatory first, checks second, direct tests third".

**2. "High-risk dependency consumer" is defined as:** the candidate was reached through `REVERSE_DEPENDENCY_CONSUMER` or `PUBLIC_CONTRACT_CONSUMER` **and** its coverage includes at least one impacted entity marked `Critical`. Criticality comes from `graph.go:126-127` (security/auth/payment labels, `CriticalPathPatterns`, and the deliberate "unknown is HIGH" rule). Consumers of ordinary entities fall to the remaining-tests tier even with the highest utility — otherwise a cheap consumer of an irrelevant file would jump ahead of direct tests.

**3. Ties break on utility descending, then direct, then stable key** — deterministic for identical inputs; the previous tie-break chain is preserved below the tier comparison.

## Alternatives considered

1. **Sort only on utility within one flat list.** Rejected: it loses the LLD's explicit stage ordering; a cheap integration test would outrank a direct test.
2. **Define high-risk consumers as any reverse-dependency consumer.** Rejected: fanout-wide consumers of non-critical code are ordinary impact propagation, not high risk; it would promote most of the suite into tier 3.
3. **Put checks after direct tests (literal reading of "direct tests" before "checks").** Rejected: the LLD lists checks second; checks are cheap gates whose fault-detection value per second is the stated ordering currency.

## Consequences

Execution order is now a directly assertable contract (`TestStage8ExecutionOrderingTiers` asserts the full seven-unit order plus each tier boundary). Plan output order equals execution order, so executors that consume `SelectedUnits` sequentially honor the LLD's staging without re-sorting. Rows `IVP-6-3-d987c30ca0`, `IVP-6-3-0af6cc6122`, and the ordering part of `IVP-6-3-afb679b66c` are satisfied by this ADR's implementation.
