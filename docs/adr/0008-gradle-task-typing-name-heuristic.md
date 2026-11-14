# ADR 0008: Gradle test tasks are typed by a name heuristic, guarded by zero-test evidence checks

## Status

Accepted

## Context

LLD §8.2 requires the adapter to discover Gradle test units and to declare honest selection capabilities. The adapter cannot ask Gradle what *type* a task is: `gradle tasks --all` prints names plus descriptions, and the adapter parses those lines with a regex (`internal/adapters/gradle/gradle.go:53`, `parseTestTaskNames` at `gradle.go:73-88`) keeping every task whose name contains `test` (case-insensitive, `gradle.go:78`). The unit test explicitly accepts `compileTestJava` as a candidate (`internal/adapters/gradle/gradle_test.go:26`), so the heuristic knowingly admits compile-only tasks. Units are declared at `GRADLE_TASK` granularity with `Filterable: true` (`gradle.go:165`), `--tests` is appended only when `supportsGradleTestFilter` accepts the task name (`gradle.go:286-291`, `gradle.go:305-307`), and class filtering is derived from changed sources only (`changedTestClasses`, `gradle.go:317-356`). Capabilities advertise `SelectableGranularity: "GRADLE_TASK"` and `SupportsFiltering: true` (`gradle.go:21`); build-target evidence is `gradle:project-layout` at confidence 0.60 (`gradle.go:120`).

The open question was whether to keep this name heuristic (and its known false positives) or attempt real task typing.

## Decision

- Keep the name-based heuristic as the *candidate* rule. Determining a task's type would require evaluating the project's build logic (init scripts, `help --task`, configuration-phase introspection): that executes untrusted project code at discovery time, costs a configuration run per candidate, and makes discovery output depend on plugins and environment — the same reasons recorded in ADR 0007 for not invoking Maven.
- Treat heuristic results as candidates, never as proof of test execution. The correctness guard lives at execution time: a selected task that produces no usable test reports is classified `ZERO_TESTS`/`REPORT_PARSE` infra failure (`internal/execution/runner.go:383-389`, `runner.go:372-381`), which is never a pass. A compile-only task such as `compileTestJava` can therefore only cost availability (escalation to the full suite), never correctness.
- Filtering stays gated and class-level: `--tests` is emitted only for tasks whose name satisfies `supportsGradleTestFilter` (`gradle.go:305-307`), with classes derived from changed files (`gradle.go:317-356`) — implementing LLD §8.2 line 590 ("use `--tests` only for a Test task where the task supports them; if the task's type/behavior is unknown, execute the whole task"): tasks the gate rejects run whole, and tasks the gate accepts but that turn out not to accept `--tests` fail the build loudly rather than silently filtering. Nothing claims method-level selection: the capability granularity is `GRADLE_TASK` (`gradle.go:21`).
- `Filterable: true` on a unit means "this task is a selection boundary and is class-filtered when supported" (`gradle.go:162-164`); `SupportsFiltering` stays true because Materialize enforces the per-task gate rather than emitting `--tests` blindly.
- The heuristic's false positives and the `DiscoveryReliable: true` / confidence `0.60` tension stay visible as tracked gaps (`docs/implementation-completion-strategy.md` §3.1) rather than being papered over in capability declarations.

## Alternatives considered

1. Type tasks by running `gradle help --task <name>` (or configuration-phase introspection) during discovery. Rejected: still needs the name heuristic to produce candidates, executes project build logic during indexing, multiplies discovery cost per project/task, and is non-reproducible across plugin versions.
2. Maintain a denylist of non-test names (`compileTestJava`, `compileTestKotlin`, `testClasses`, ...). Rejected as the primary control: task names are plugin- and project-defined, so any denylist is incomplete by construction; the zero-test execution check is the complete control and already exists.
3. Stop declaring candidate units until they are proven to run tests (e.g., by a probe run at discovery). Rejected: a probe executes tests during indexing, which discovery must never do, and would destroy incremental index performance.
4. Lower capability granularity claims to `CLASS` or `NONE`. Rejected: `GRADLE_TASK` is exactly what is implemented — the task is the selection boundary — and understating it would remove honest capability information.

## Consequences

Gradle discovery stays offline-safe and deterministic, at the cost of occasionally selecting a task that runs no tests; such a selection fails loudly (`ZERO_TESTS`) and escalates instead of passing, so the failure mode is extra verification, not missed verification. Unit rows for LLD §8.2 are reviewed against this heuristic with the `compileTestJava` false positive recorded as a known limitation. If a future enhancement adds task-type evidence (for example from a cached configuration report), this ADR must be revisited because the candidate rule and the confidence values would change.

## Update (2026-11-12, WP-D) — the `--tests` gate now requires Gradle's Test-task naming convention

The decision above is unchanged in substance: the name heuristic stays the candidate rule, no task typing is executed, the correctness guard stays at execution time, and no method-level claim is made. One detail was tightened, and the reasoning is recorded here because it changes which tasks may be filtered.

- **Before:** `supportsGradleTestFilter` accepted a task whose lower-cased name ended in `test` (`test`, `integrationTest`, and also `latest`, `unittest`, `integrationtest`).
- **After:** it accepts only `test` (case-insensitively) or a name ending in the camel-case suffix `Test` (`integrationTest`, `unitTest`, `functionalTest`).

**Why:** the discovery candidate rule keeps *every* task whose name contains `test`, so `latest` is a discoverable candidate. The old predicate therefore emitted `--tests` for a task whose type is not a Gradle `Test` task, which contradicts LLD line 590 ("if the task's type/behavior is unknown, execute the whole task"). The new predicate recognizes Gradle's `Test`-task naming convention and runs everything else whole. This is exactly the direction ADR 0008's alternatives rejected — a name-based rule instead of a denylist — but with the convention stated narrowly enough that a coincidence like `latest` no longer qualifies.

**Effect:** strictly more verification, never less — the only tasks that change behavior are the ones that are no longer narrowed. Real `Test` tasks with non-conventional names (`allTests`, lowercase `unittest`) lose class filtering and run whole, which the row's limitations record.

**Current line references (the numbers cited above drifted with the WP-D change):** the gate predicate is `internal/adapters/gradle/gradle.go:317-334`, the gate call site is `gradle.go:298`, the `--tests` append is `gradle.go:301-303`, and `changedTestClasses` is `gradle.go:344-382`. The direct assertions are `TestSupportsGradleTestFilterAcceptsOnlyTestTasks`, `TestGradleMaterializeAppliesTestFilterOnlyToSupportedTasks`, `TestGradleMaterializeRunsWholeTaskWhenTaskTypeIsUnknown` and `TestGradleDiscoveryDeclaresClassLevelOnlyCapability` (`internal/adapters/gradle/gradle_test.go`).
