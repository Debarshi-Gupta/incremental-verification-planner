# ADR 0019: Org-reviewed narrow exceptions to hard full-suite rules are explicit path entries with audit details

## Status

Accepted

## Context

LLD line 437 (§6.3 rule 3) mandates hard full-suite escalation on lockfile, migration, and public-contract changes, and row `IVP-6-3-a3c4956810` (LLD "narrow exception" vocabulary) requires that an organization can grant a **reviewed, narrow exception** to a hard rule. The rule-3 codes (`LOCKFILE_CHANGE`, `MIGRATION_CHANGE`, `PUBLIC_CONTRACT_CHANGE`) must keep firing for everyone else, and Appendix A forbids new codes. Without a design, the choice is between an unreviewable bypass and no exception at all.

Sibling precedent: ADR 0014 defines reviewed unit exclusions (four mandatory fields, org-scoped, date-validated, surfaced as an omission reason). This ADR follows the same review philosophy for hard-rule paths.

## Decision

**1. Exceptions are path entries, not unit entries.** Configuration key `safety.hard_path_exceptions`, a list of entries mirroring `reviewed_exclusions`' shape: `path` (the glob matched against the changed file), `reviewer`, `reviewed_at` (same `YYYY-MM-DD` + non-future validation as ADR 0014), `rationale`, and `rule` — which of `LOCKFILE_CHANGE`, `MIGRATION_CHANGE`, `PUBLIC_CONTRACT_CHANGE` the exception covers. Every field is mandatory; duplicates (same path+rule) are rejected.

**2. Exceptions are org-scoped like ADR 0014.** `enforceOrganizationConstraints` applies the same subset-only repository matching: an org may narrow the baseline exception set, never widen it.

**3. The planner skips only the matched hard-rule escalation, with an audit check.** When a changed file matches `HardFullSuitePaths` **and** its Stage-2 classification maps to a rule with an active exception, the planner records a `HardRuleChecks` entry with `Triggered=false` and detail `"hard-rule exception applied by <reviewer> on <date>: <rationale>"` instead of the escalation. The plan stays READY only if no other safety condition fires. **Labels are never exceptable** (`HardFullSuiteRiskLabels` has no exception path): a `SECURITY_CRITICAL` label escalation is unconditional.

**4. Exceptions never suppress the classification itself.** `ClassifyGlobalImpact` and the Stage-9 change summary still report the file's category — the exception changes the escalation decision, not the evidence.

**5. Validation fails closed.** A malformed exception (missing field, bad date, unknown `rule` value) fails config validation like any other policy error (ADR 0012): no plan is synthesized from a policy that cannot express its own exceptions.

## Alternatives considered

1. **Silent config flag `allow_selective_on_lockfile_change: true`.** Rejected: it is unreviewable (no reviewer/date/rationale), applies repo-wide, and cannot distinguish one migration from a destructive schema change.
2. **Exception by unit key (like ADR 0014).** Rejected: hard rules fire on *changed files*, not test units; a unit-level exception cannot name the file that triggered rule 3.
3. **Allow exceptions to cover label escalations.** Rejected: risk labels are the last unconditional safety net; an exceptable label defeats the purpose of a hard rule.
4. **Exception downgrades escalation to a warning instead of suppressing it.** Rejected: the LLD's plan statuses have no "warned" state; a non-triggered hard rule recorded in `HardRuleChecks` is the auditable equivalent.

## Consequences

Organizations can carve out reviewed, dated, rationaled path-level exceptions without touching the closed escalation vocabulary, and every applied exception is visible in the plan's hard-rule checks. The exception is as reviewable as a unit exclusion (ADR 0014) and fails closed like invalid config (ADR 0012). Inventory row `IVP-6-3-a3c4956810` is satisfied by `safety.hard_path_exceptions` plus the planner skip and its tests.
