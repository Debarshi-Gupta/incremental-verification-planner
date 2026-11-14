# ADR 0022: The adapter-granularity expansion step is vacuous because candidates are discovered at the supported granularity

## Status

Accepted

## Context

The LLD's planning pseudocode (Appendix-C, line 1421) contains the step `selected = adapterExpandToSupportedGranularity(selected)`: after the planner selects units, the selection is expanded to the granularity the adapter actually supports.

The inventory row `IVP-APPENDIX-C-80cc57ce08` recorded that no such expansion exists anywhere in the repository, and that units that cannot be selected at the discovered granularity are instead omitted with `OmitNotSelectableGranularity` or, when they are mandatory, escalated as not safely runnable at the discovered granularity (`internal/analysis/planner.go`, asserted by `internal/analysis/planner_test.go`).

What the implementation actually does:

- Every adapter discovers its units **at the granularity it supports**
  (`GRADLE_TASK` for Gradle, `MAVEN_MODULE_GOAL` for Maven, `BAZEL_TEST_TARGET`
  for Bazel, `PYTEST_NODEID` for pytest, `JEST_TEST_FILE` for Jest,
  `NX_PROJECT_TARGET` for Nx, `DECLARED_UNIT` for the generic adapter).
- The planner's candidate set is derived from those discovered units, so a
  candidate never exists at a granularity finer than the adapter's.
- A unit that cannot be selected at the discovered granularity is omitted with
  a reason, and a mandatory unit that cannot be selected escalates the plan.
  Nothing is ever widened silently.

## Decision

**The expansion step is not implemented, because in this implementation it has
no input to act on.** A candidate finer than the adapter's supported
granularity cannot exist: the candidates are the discovered units, and the
discovered units are already at the supported granularity. The pseudocode
assumes a planner that may produce finer-grained candidates (for example a
method-level selection inside a test class); this planner never does, because
ADR 0008 fixed the Gradle granularity at the task level and the other adapters
declare their granularity as the unit they can actually run.

The fail-closed behavior stays: a unit that cannot be selected at the
discovered granularity is omitted with a reason, and a mandatory unit that
cannot be selected escalates the plan. That is the conservative direction — the
plan never claims a selection the adapter cannot honor.

## Alternatives considered

1. **Implement the expansion anyway.** Rejected: it would be dead code today
   (no candidate can be finer than the supported granularity), and a
   speculative implementation of a vacuous step would have to invent the
   finer-grained candidate model the step assumes.
2. **Widen a mandatory unit silently to a coarser one.** Rejected: a coarser
   unit may cover more than the change requires and would replace a precise
   refusal with an imprecise claim; the LLD's own rule is to escalate when a
   mandatory check cannot be run safely.
3. **Narrow the planner to a fixed granularity.** Rejected: the granularity is
   a property of the adapter and is already declared honestly per adapter.

## Consequences

The LLD pseudocode step is resolved as *not applicable to this implementation*
rather than as missing. The row is satisfied by the documented disposition
together with the direct assertions that pin the fail-closed behavior
(`internal/analysis/planner_test.go`: a unit that cannot be selected at the
discovered granularity is omitted with a reason, and a mandatory unit that
cannot be selected escalates the plan).

**Revisit criteria:** if an adapter ever exposes candidates finer than the
granularity it can select — for example a Gradle test class discovered next to
the task that runs it — the expansion becomes real and this ADR must be
replaced by an implementation.
