# ADR 0014: Reviewed unit exclusions are a four-field, org-scoped, date-validated policy entry

## Status

Accepted

## Context

LLD line 1040 allows an organization to keep a unit out of selective selection through a reviewed exclusion, but does not specify the record's shape, who may grant it, how freshness is judged, or what the planner must say when it omits the unit. Without those, an exclusion is an unreviewable silent hole in selection: a unit could disappear from every plan with no trace and no date to challenge it.

What exists in code:

- `domain.ReviewedExclusion` carries `unit_key`, `reviewer`, `reviewed_at`, `rationale` (`internal/domain/policy.go:53-58`); `Policy.ReviewedExclusions` is the policy field (`policy.go:30`).
- `config.validateReviewedExclusions` enforces every field (`internal/config/validate.go`), the JSON schema requires all four (`schemas/ivp-config.schema.json` `$defs/reviewedExclusion`), and `schema_parity_test.go` asserts the schema and `Validate()` agree.
- `config.ToDomainPolicy` maps configuration into policy, and `enforceOrganizationConstraints` applies the organization-scope rule (`internal/config/precedence.go`).
- The planner reads exclusions through `analysis.ReviewedExclusions` → `ReviewedExclusionKeys` (`internal/analysis/classify.go:244-258`) and applies them at `internal/analysis/planner.go:698,743,405`.

## Decision

**1. Every field is mandatory.** `unit_key`, `reviewer`, `reviewed_at`, and `rationale` must all be present and non-empty; a missing `unit_key` is a configuration error, not "exclude everything". Each field is trimmed and must match `^\S(?:.*\S)?$` so whitespace-only values are rejected.

**2. `reviewed_at` must be a real, non-future calendar date** in `YYYY-MM-DD` form: a strict `^\d{4}-\d{2}-\d{2}$` pattern *and* a `time.Parse` round-trip (rejecting `2026-02-31` and `2026-13-01`), and not later than today. Entries with duplicate `unit_key` values are rejected so the effective decision for a unit is unambiguous.

**3. The exclusion is organization-scoped by byte-matching the repository.** `enforceOrganizationConstraints` requires every entry's repository to byte-match the baseline entry set: the organization's set may be a **subset** of the baseline (an org narrows rather than widens), but no repository may carry an entry the baseline does not. This keeps a tenant from quietly exempting its own units from a reviewed org-wide decision.

**4. The planner keeps an excluded unit out of the selectable pool but never out of the report.** In `buildCandidates` a reviewed, non-mandatory unit is dropped from the candidate list (`planner.go:743`); a mandatory gate is never silenced by an exclusion. `omissions` then reports it with reason `EXCLUDED_BY_REVIEWED_POLICY` and the explanation *"unit is excluded by a reviewed policy entry; this is not proof of irrelevance"* (`planner.go:1024-1026`) — the omission code is evaluated before the "no impact edge" branch so the reason names the policy rather than implying absence of evidence.

**5. Entries without a unit key are ignored defensively by the planner** (`ReviewedExclusionKeys` trims and skips empties) even though the configuration layer already rejects them. The planner must not fail a plan because of a malformed policy it did not build.

## Alternatives considered

1. **A free-form `excluded_units: [string]` list.** Rejected: an exclusion with no reviewer, date, or rationale cannot be re-adjudicated when it is wrong, which is precisely the case the LLD's "reviewed" qualifier exists to prevent.
2. **Allow `reviewed_at` to be omitted or open-ended.** Rejected: staleness is the only signal that an exclusion still reflects reality. A date that may be absent makes every entry permanently valid.
3. **Exclude the unit silently (no omission entry).** Rejected: the LLD requires omitted units to state why they were not selected, and silence would make an excluded unit indistinguishable from an unselected one — hiding a deliberate policy decision inside ordinary selection noise.
4. **Let a repository-level entry override an organization-level one.** Rejected: it inverts the precedence the configuration layer implements and would let a tenant widen its own exemptions. Subset-only matching keeps the org in control.
5. **Allow exclusions to silence mandatory checks.** Rejected: `AlwaysRunCheckIDs` are safety gates; an administrative exclusion must not be able to disable a gate the policy configured as always-on.

## Consequences

An exclusion is now a reviewable artifact: it names who decided, when, and why, and it survives schema validation, `Validate()`, and the organization-scope check. Operators can enumerate and expire entries by date; the planner surfaces every exclusion as a first-class omission reason instead of a silent gap. Because `reviewed_at` is compared against "today", a policy file ages out by validation failure rather than by silently remaining in force — deployments that pin old policy files must refresh the date. Inventory row `IVP-APPENDIX-A-9561eb2bff` is reviewed against this ADR.
