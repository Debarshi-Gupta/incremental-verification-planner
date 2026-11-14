# ADR 0024: CI selective eligibility reset after a major algorithm or policy change

## Status

Accepted

## Context

LLD 18.4 requires: "On major algorithm/policy change, reset CI selective eligibility to OBSERVE until re-calibrated."

Nothing implemented this. The RISK_BASED_CI gate in the planner only checked the current calibration counters, so a repository that had been calibrated under an older algorithm or an older policy kept its selective CI gating after the change. Calibration evidence produced under the previous algorithm or policy does not justify gating under the new one.

The algorithm version is an explicit, stored property of every plan (`ivp-planner-v1.0.0`). Policy versions are opaque, monotonically increasing integers owned by the policy store.

## Decision

- The repository's algorithm and policy versions are observed and recorded per repository in `repository_upgrade_state`.
- The first observation establishes a baseline. It is not a change, so it does not reset anything.
- A **major algorithm change** is a change of the major component of the algorithm version (`ivp-planner-v1.x` to `ivp-planner-v2.x`). A minor or patch release does not reset the repository. A version that cannot be parsed and differs from the recorded one is treated as a major change, because it cannot be shown to be compatible.
- A **policy change** is any change of the effective policy version. Policy versions are opaque integers, so IVP cannot prove that a new policy keeps the calibration of the previous one; the conservative reading treats every policy version change as major.
- On a detected major change the repository is reset: the reset time and the calibration and audit counters observed at that moment are stored. Evidence observed before the reset never counts towards re-calibration.
- While a repository is reset and not re-calibrated, `RISK_BASED_CI` requests are answered with an `OBSERVE` plan: the selective recommendation is still produced and compared, but the authoritative full suite remains the CI authority. Other requested modes are not rewritten.
- The repository is re-calibrated when the calibration runs and comparable audits observed *since the reset* reach the policy thresholds (`minimum_calibration_runs`, `minimum_audit_runs`).
- The effective policy exposes the current eligibility (`CISelectiveEligible`, `CISelectiveResetReason`) so that a client can see why CI selective gating is unavailable.

## Alternatives considered

1. Keep using the pre-change calibration counters. Rejected because calibration evidence gathered under a different algorithm or policy does not describe the behaviour of the new one.
2. Reset only the RISK_BASED_CI approval flag. Rejected because the approval is an organizational decision, and silently revoking it would hide the reason; the mode is rewritten to OBSERVE instead and the reason is reported.
3. Reset on every algorithm change, including patch releases. Rejected because a patch release does not change the selection behaviour and would force every repository through a re-calibration cycle for no safety gain.
4. Reset only when the policy changes. Rejected because the selection algorithm version is stored in every plan and is the more likely source of behavioural change.

## Consequences

After an upgrade, CI selective gating is unavailable until the repository produced new calibration runs and comparable audits; the requests are answered with OBSERVE plans instead, which keeps the audit machinery running. The reset state is server-owned and per repository, so a repository cannot restore its own eligibility. A repository that never had a recorded baseline is not reset, because no change was detected; the limitation is documented rather than guessed around.
