# ADR 0025: Dependency version as a coverage comparability dimension

## Status

Accepted

## Context

LLD 23 scenario 6 requires the implementation to simulate: "The coverage report belongs to a previous dependency version; planner must mark it stale."

Coverage comparability was keyed on the environment, build-configuration and test-configuration digests only. A lockfile change that does not change the toolchain or the configuration produced a coverage report that was still treated as fresh, although the resolved dependency set - and therefore the code under test - had changed.

The dependency version is already part of the audit fingerprint (`audit.Fingerprint.DependencyDigest`), but it was not part of the planning evidence.

## Decision

- The snapshot records the dependency version it was produced with (`RepositorySnapshot.DependencyDigest`). It is part of the immutable snapshot identity: the same tree indexed against a different dependency set is a different snapshot.
- Every coverage observation records the dependency version of the report (`CoverageObservation.DependencyDigest`) and the adapter version that produced it (`CoverageObservation.AdapterVersion`). Both are part of the coverage provenance, next to the environment, build-configuration and test-configuration digests that were already stored.
- A coverage observation is **stale** for a snapshot when the snapshot records a dependency version and the observation does not carry the same one. A snapshot without a dependency version has no dependency dimension, so no dependency change is detected and the existing comparability rules apply unchanged.
- A stale observation is removed from the planner input and the plan is escalated to `FULL_REQUIRED` with the existing escalation code `COVERAGE_STALE`. No new reason code is introduced.
- A coverage submission that claims a dependency version other than the executed snapshot's is rejected: the report does not describe the tree it is attached to. A submission without a dependency version inherits the executed snapshot's version, because the run executed exactly that tree.
- The coverage history read path returns the dependency and adapter provenance of every observation instead of filtering it away, so the planner can distinguish "no coverage" from "coverage from a previous dependency version".

## Alternatives considered

1. Keep filtering coverage by dependency version in the database query. Rejected because the requirement is to *mark* the report stale and to report why, not to hide it; a filtered query makes the stale report indistinguishable from a missing one.
2. Treat the dependency version as part of the build-configuration digest. Rejected because the build configuration and the resolved dependency set change for different reasons and at different times; collapsing them would make a dependency rollback indistinguishable from a configuration change and would hide the specific reason for staleness.
3. Only warn about stale dependency versions. Rejected because LLD 22 requires that invalid or unknown evidence lowers confidence or forces a fallback, and never makes selection more aggressive.

## Consequences

A dependency change invalidates the coverage evidence that was produced under the previous dependency set, so the affected plans escalate to the authoritative full suite instead of trusting an outdated report. The dependency version has to be published by the indexing client; until it is, snapshots record no dependency version and the existing comparability rules apply unchanged. The snapshot identity now includes the dependency version and the adapter versions, so two snapshots of the same tree with different dependency sets are stored separately instead of colliding.
