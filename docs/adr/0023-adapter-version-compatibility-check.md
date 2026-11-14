# ADR 0023: Adapter version compatibility check

## Status

Accepted

## Context

LLD 18.4 requires: "Adapter version changes invalidate adapter-specific evidence unless a compatibility check proves it remains comparable."

Adapter versions were captured (the client reports `adapter_versions`, the plan stores them in `adapter_capabilities`) but never compared, so coverage produced by one adapter version kept justifying selection after the tool was upgraded. Adapter-specific evidence is: the coverage reports the adapter produced, and the discovery evidence (test units and graph edges) the adapter emitted.

No adapter publishes a machine-readable compatibility declaration, so the check has to be derived from the version identity itself.

## Decision

- The compatibility check is `domain.AdapterVersionCompatible(recorded, current)`. It is deliberately narrow:
  - identical versions are comparable;
  - a version that keeps the same evidence contract (the `<contract>-` prefix, for example `pytest`) and the same major and minor components stays comparable, so a patch release cannot silently invalidate a repository's evidence;
  - every other change (different contract, major bump, minor bump, unparsable version) is **not** comparable and invalidates the adapter's evidence.
- The recorded version is the version that produced the evidence: the coverage observation carries the adapter version that produced the report, and the snapshot records the adapter versions that produced its discovery evidence.
- The current version is the version the client reports for the plan request.
- When a detected change is not comparable, the affected evidence is removed from the planner input: the coverage observations of that adapter are dropped and the test units of that adapter become non-selectable, so they stay visible in the omission audit trail but can no longer be selected.
- The plan is then escalated to `FULL_REQUIRED` with the existing escalation codes `COVERAGE_STALE` and `GRAPH_STALE`. No new reason code is introduced (LLD Appendix A is a closed vocabulary).
- An unknown version on either side is not a detected change. IVP cannot detect a change it was never told about, and treating "unknown" as "changed" would invalidate every repository whose adapter does not report a version.

## Alternatives considered

1. Accept every adapter version change as comparable. Rejected because LLD 18.4 explicitly invalidates adapter-specific evidence on a version change.
2. Reject every adapter version change, including patch releases. Rejected because a patch release does not change the evidence contract, and invalidating it would force full verification after every tool update without adding safety.
3. Let the repository configuration declare compatible version ranges. Rejected for now: the configuration schema is owned by another work package, and a configuration-driven compatibility claim would let a repository weaken the LLD 18.4 rule.
4. Add a dedicated escalation code such as `ADAPTER_VERSION_CHANGED`. Rejected because Appendix A pins the reason-code vocabulary.

## Consequences

Evidence produced by an adapter version that changed incompatibly is no longer used for selection, and such plans escalate to the authoritative full suite. The recorded versions are only as good as the client that reports them: the CLI currently reports the adapter versions of its index record, so the detection becomes effective once a client reports the versions of the environment it actually runs in. The compatibility rule is deliberately conservative and is documented here so that a future change to it is an explicit, reviewable decision.
