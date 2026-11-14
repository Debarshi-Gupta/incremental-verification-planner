# ADR 0004: Audit sampling rate is scoped to the audited plan's mode

## Status

Accepted

## Context

The LLD specifies two distinct audit sampling rates: `safety.audit.sample_rate` governs audits of ordinary plan executions (LLD 10.2), while `modes.risk_based_ci.audit_sample_rate` governs audits of RISK_BASED_CI executions (LLD 7.2), where each execution is a policy decision that must be verified at a rate appropriate to that mode. The audit command sampled the latest selective execution using `safety.audit.sample_rate` unconditionally, so RISK_BASED_CI executions were sampled by the wrong configuration key: operators tuning the risk-based rate had no effect, and the mode-specific rate documented in the LLD was dead configuration. The sampling decision must also be able to name the rate it used so an operator can tell which policy governed a given audit decision.

## Decision

- The audit command resolves the rate from the fetched plan's mode through `auditSampleRate(cfg, mode)`: `modes.risk_based_ci.audit_sample_rate` for `RISK_BASED_CI` plans, `safety.audit.sample_rate` for every other mode (`FULL`, `OBSERVE`, `FAST_FEEDBACK`, and an unset mode, which is conservative because it falls back to the general safety rate).
- The sampling decision runs after the plan is fetched, because the mode is a property of the plan, not of the local invocation; the command cannot know the rate before it knows which plan it is auditing.
- The skip message names the configuration key that produced the rate (`modes.risk_based_ci.audit_sample_rate` or `safety.audit.sample_rate`) so the operator can trace the decision back to configuration.
- Sampling stays a deterministic function of the execution ID (`auditSampleSelected`), so re-auditing the same execution yields the same decision regardless of which rate source applies.

## Alternatives considered

1. Sample every mode with `safety.audit.sample_rate`. Rejected because it makes the RISK_BASED_CI-specific rate dead configuration and contradicts LLD 7.2, which assigns risk-based CI its own sampling rate.
2. Sample RISK_BASED_CI executions at 100% unconditionally. Rejected because it overrides an operator's explicitly configured rate and would make high-throughput risk-based pipelines audit-bound; the LLD expresses the requirement as a configurable rate, not a floor.
3. Resolve the rate from the command line (a `--mode` flag) instead of the plan. Rejected because the plan is the authoritative record of the mode that produced the execution being audited; a flag could disagree with the plan and audit under the wrong policy.

## Consequences

RISK_BASED_CI executions are now sampled by their mode-specific rate, and tuning either key affects only the modes it governs. The audit decision is made after a plan fetch, so sampling cannot be pre-computed for offline use; that ordering is inherent because the mode is plan data. Both rates must remain within `[0,1]` and are validated by configuration loading; a rate of 0 disables sampling for that mode and a rate of 1 audits every execution.
