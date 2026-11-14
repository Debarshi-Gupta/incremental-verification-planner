# ADR 0022: Execution environment fingerprint comparison at execution start

## Status

Accepted

## Context

LLD 9.4 lists "Execution environment fingerprint differs from plan fingerprint" as a condition that must escalate or invalidate a selective run. LLD 9.3 additionally requires that the plan is revalidated immediately before the first command.

Until now the execution environment fingerprint was only *recorded*: `StartExecution` stored the digest the client reported, and the plan creation path compared the client's toolchain digest with the indexed snapshot. Nothing compared the fingerprint a plan was created for with the fingerprint an execution actually ran in, so an environment change between planning and execution was invisible.

The fingerprint is a canonical digest over metadata only (`domain.CanonicalDigest`); it never contains raw environment values.

## Decision

- The plan records the environment fingerprint it was planned for in `VerificationPlan.EnvironmentFingerprint`. The service sets it from the plan request's execution context; it is part of the immutable plan document.
- At execution start, a selective execution whose reported environment fingerprint differs from the plan fingerprint is refused. The refusal is not an error the client may ignore: the plan is downgraded to `FULL_REQUIRED` in the same transaction, so the planned subset can never run in an environment it was not planned for, and the authoritative full-suite fallback remains executable.
- A full-suite execution is not bound by the plan's selective fingerprint: it verifies the current tree in the current environment and stays authoritative.
- A submitted selective result whose execution environment differs from the plan fingerprint is never recorded as a targeted pass or failure. The execution and its plan become `STALE`.
- The executor can recompute the fingerprint locally before the first command (`execution.AdapterToolchainDigest`, `execution.ValidateEnvironment`) and report it through `PlanRevalidation.EnvironmentFingerprint`. `execution.ValidatePlan` refuses a plan whose recomputed fingerprint differs, which the executor classifies as a stale plan and answers with the full-suite fallback.
- An unknown fingerprint on either side is not treated as a difference. IVP never claims a comparison it could not perform, and it never treats "unknown" as "equal".

## Alternatives considered

1. Compare the fingerprints only in the local executor. Rejected because the server is the component that owns the immutable plan; a client that skips the local check must still be prevented from recording a selective pass under a different environment.
2. Reject every execution whose fingerprint differs, including full-suite executions. Rejected because the full suite is the safe fallback for exactly this situation; refusing it would leave the repository without any verification path.
3. Record the difference as a warning and continue. Rejected because LLD 9.4 requires escalation or invalidation, and a silent continuation would attach a selective result to an environment the plan never described.

## Consequences

A plan created in one environment cannot be executed selectively in another; the executor must run the configured full suite, which is more expensive but honest. The environment fingerprint is part of the stored plan document, so historical plans remain interpretable. The local recomputation helper is available to the CLI, but the CLI currently reports the fingerprint recorded in its index record, so the end-to-end detection still depends on a client that reports the fingerprint of the environment it actually runs in.
