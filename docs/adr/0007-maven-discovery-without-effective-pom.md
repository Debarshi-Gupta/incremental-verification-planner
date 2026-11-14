# ADR 0007: Maven discovery uses the reactor module model, without invoking Maven for an effective POM

## Status

Accepted

## Context

LLD §8 requires adapters to discover build/test units from the workspace with honest evidence about how each unit was derived. The Maven adapter parses each `pom.xml` itself (`internal/adapters/maven/maven.go:52-94`): the XML model keeps `artifactId`, `modules`, direct `dependencies`, `build.plugins`, and `build.plugins.pluginManagement`, and has no field for `<parent>`, `<profiles>`, `<properties>`, or `<dependencyManagement>`. Discovery walks the reactor by breadth-first traversal of each module's own `<modules>` entries, workspace-confined and size-capped (`maven.go:108-183`), declares a `<module>#test` unit unconditionally (`maven.go:162-163`), and declares `<module>#verify` only when `maven-failsafe-plugin` appears in that module's own build or pluginManagement (`maven.go:95-107`, `maven.go:164-167`). Every build target is published with `EvidenceSource: "maven:reactor-module-model"` and `Confidence: 0.75` (`maven.go:199`). Materialization appends `-pl <module> -am` plus the goal and, when changed test classes are known, a class-level `-Dtest=`/`-Dit.test=` filter (`maven.go:256-268`, classes derived at `maven.go:290-328`). No test executes Maven to compute an effective POM.

What the model therefore cannot see: plugin configuration inherited from a parent POM (Surefire/Failsafe versions, `skipTests`, `failIfNoTests`), `dependencyManagement`-managed versions, parent-declared plugin defaults, and profiles. The configuration keys `adapters.maven.surefire_enabled` / `failsafe_enabled` exist in the config schema and templates but are not read by the adapter (`internal/config/types.go:126-127`, no consumer in `maven.go`).

## Decision

- Discovery never shells out to Maven. No `mvn help:effective-pom`, no dependency resolution, no model-building goal. Reasons: an effective-POM request needs a JDK, Maven, and usually network access at discovery time (indexing must work offline); it executes build extensions and plugins, so discovery would run arbitrary project code; the result depends on remote repository state, so digests and discovery output would stop being deterministic; and it multiplies per-module cost across the reactor.
- Parse only in-repository POMs, one file per module, bounded (`8 MiB` cap, `maven.go:89-90`) and workspace-confined (`ResolveWithin`, `maven.go:81`). Aggregation itself is not inherited in Maven — `<modules>` lives in each aggregating POM — so the BFS over each module's own `<modules>` entries *is* the complete reactor; only configuration inheritance is invisible.
- Because configuration inheritance is invisible, the adapter advertises what it actually derived: `EvidenceSource: "maven:reactor-module-model"` with `Confidence: 0.75` on every target (`maven.go:199`), never tool-attested graph evidence, so the planner weighs it below a tool-reported graph.
- Provider detection is conservative in the safe direction: `#verify` is declared only when Failsafe is visible in that module (`hasFailsafe`); `#test` is declared always because Surefire binds to the `test` phase for standard packaging by default. An inherited `<skipTests>` or a missing provider cannot create a false pass: a selected unit that reports zero tests is classified `ZERO_TESTS` infra failure (`internal/execution/runner.go:383-389`) and an incomplete/missing report is `REPORT_PARSE` (`runner.go:372-381`).
- Filtering stays class-level (`changedTestClasses`, `maven.go:290-328`): method-level patterns cannot be checked against inherited Surefire configuration, and a wrong pattern cannot produce a pass either way — if Surefire fails the filtered build the run is a failure, and if it passes without reports the run is a `ZERO_TESTS` infra failure (`runner.go:383-389`).
- `surefire_enabled` / `failsafe_enabled` remain accepted configuration keys but stay unenforced by the adapter until provider detection exists; this is recorded as an open gap in `docs/implementation-completion-strategy.md` §3.1.4 rather than being silently honored.

## Alternatives considered

1. Run `mvn help:effective-pom` per module at discovery. Rejected: needs toolchain and network at index time, executes project plugins (untrusted code), non-deterministic against remote state, and slow enough to break incremental indexing.
2. Vendor a Maven model parser and resolve `<parent>` chains from disk (in-repo parents only). Rejected as a *principle* change rather than a fix: remote parents and plugin default versions remain unresolvable offline, so confidence could never honestly reach tool-attested levels; kept as a possible incremental improvement, not as this decision.
3. Treat the parsed POM as complete (raise `Confidence` above 0.75, drop `EvidenceSource`). Rejected: it would claim knowledge the adapter does not have, which is exactly the silent-shortcut behaviour LLD §0 forbids.
4. Suppress `-Dtest=` filtering because it may not match inherited configuration. Rejected: a filtered command that matches nothing still fails closed (Surefire build failure, or `ZERO_TESTS` infra classification when reports are missing), whereas removing filtering only costs speed and keeps the same correctness.

## Consequences

Maven reactor evidence stays at confidence 0.75 and plans relying on it are weighted accordingly. Repositories that inherit Surefire configuration may see `#test` units that do nothing (classified `ZERO_TESTS`, an infra failure that escalates rather than passes) or class filters that fail the build; both directions are loud, so operators see the gap instead of silent coverage loss. The dormant `surefire_enabled`/`failsafe_enabled` keys and the missing parent/dependencyManagement resolution remain tracked adapter work; when provider detection lands, this ADR must be revisited because the confidence and unit-declaration rules would change.

## Update (2026-11-12, WP-D) — the provider keys are enforced

The final bullet above is now stale: `surefire_enabled` and `failsafe_enabled` are read by the adapter instead of being accepted and ignored. Everything else in this ADR is unchanged — discovery still never shells out to Maven, the reactor model still publishes at confidence 0.75, filtering is still class-level, and `#verify` is still declared only when Failsafe is visible in the module.

- **Tri-state keys.** `adapters.maven.surefire_enabled` / `failsafe_enabled` are decoded as tri-state (`*bool`): an absent key means *unset*, which keeps the provider enabled with the adapter's default behaviour; only an explicit `false` disables a provider. A missing key can therefore never silently mean "disabled". `config.Defaults()` seeds both to `true`, and the published JSON Schema is unchanged (both keys remain plain booleans — unsetness is a Go-side concept only).
- **Discovery.** `surefire_enabled: false` means no `<module>#test` unit is declared; `failsafe_enabled: false` means no `<module>#verify` unit is declared. Each case reports a `DiscoveryResult.Reason` ("Surefire disabled by configuration; no surefire test units declared" / "Failsafe disabled by configuration; no failsafe verify units declared") so the absence is visible rather than silent. Unset keys keep the unit set exactly as before, and `#verify` still additionally requires a visible `maven-failsafe-plugin`.
- **Materialization.** A goal belonging to a disabled provider is refused, so a stale snapshot cannot materialize a `test`/`verify` command for a provider that was switched off.

What this update does **not** change: enforcement is adapter-wide, not per module; `surefire_enabled: true` still does not prove that a Surefire provider exists (no effective-POM resolution — see above), so such a unit may still report zero tests and be classified `ZERO_TESTS`; and disabling a provider removes those units from the snapshot inventory, so such repositories fall back to the existing "no tests selected → FULL_SUITE" policy rather than a new escalation path.
