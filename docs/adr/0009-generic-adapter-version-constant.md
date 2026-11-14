# ADR 0009: The generic adapter reports a configuration-contract version constant

## Status

Accepted

## Context

Every adapter must report `Version(ctx, root)` (LLD §8.1 line 580), and each execution records the adapter version for reproducibility and audit (LLD line 260); `ivp doctor` and `ivp version` surface adapter versions too (LLD lines 748, 771). Native adapters shell out to their tool (`gradle --version`, `mvn --version`). The generic adapter has no tool: it interprets repository-declared units from `.ivp/config.yaml` (`internal/adapters/generic/generic.go`, `Discover` at `generic.go:133`, `Materialize` at `generic.go:161`). Its `Version` returns the constant `"generic-config-v1"` (`generic.go:274`). The strategy review flagged the hardcoded string as acceptable but in need of a documented decision (`docs/implementation-completion-strategy.md` §3.1.8), and the adapter was the only one of seven missing the package-level interface assertion.

## Decision

- The generic adapter's reported version is a **configuration-contract version**: it names the revision of the `test_units`/`checks` schema and the interpretation rules the adapter implements (placeholder handling, path mapping, dependency ordering, redaction), not a build artifact and not a repository-specific value.
- The value is the literal `"generic-config-v1"` (`generic.go:274`), bumped manually to `v2`, `v3`, ... only when the semantics of interpreting `GenericUnitConfig` change incompatibly (for example a new placeholder, a changed mapping rule, or a stricter validation rule). A constant string keeps execution records comparable across repositories and machines: two runs recorded as `generic-config-v1` interpret their declared units the same way.
- Repository-specific differences are *not* encoded in the adapter version. The plan and execution records already store the concrete unit definitions (argv templates, mappings, dependencies), so configuration drift between repositories is visible from the stored units rather than from a per-repository version hash.
- Interface compliance for the generic adapter is enforced at compile time like every other adapter: `var _ adapters.Adapter = (*Adapter)(nil)` (`generic.go:19`), matching the assertions in the other six adapter packages. The dead no-op loop previously in `Materialize` (old lines 179-185), which iterated targets and did nothing, was removed rather than documented.

## Alternatives considered

1. Report a hash of the repository's configured units as the version. Rejected: it conflates configuration drift with adapter implementation changes, so an execution comparison would show a "new adapter version" that never changed behaviour, and cross-repository comparability of `generic-config-v1` would be lost.
2. Report the binary build version (`debug.ReadBuildInfo`). Rejected: development and local builds report no meaningful version, which would make the recorded adapter version noisy while saying nothing about the contract being versioned — the thing executions actually need to compare.
3. Report just `"generic"` with no version. Rejected: LLD line 260 requires the adapter version in execution records, and an unversioned string cannot signal a contract change at all.
4. Keep the constant but leave the interface assertion missing. Rejected: the assertion is free (the adapter already implements all nine methods) and a missing assertion is a silent drift risk for the one adapter most likely to change with config-schema work.

## Consequences

Execution records, `ivp doctor`, and `ivp version` show `generic-config-v1` regardless of where the adapter runs; analysts compare unit definitions for repository differences. Any change to how generic units are interpreted must bump the constant in the same commit — recorded here as the review trigger, since nothing enforces the bump mechanically. The compile-time assertion now catches accidental interface changes for the generic adapter during ordinary `go build`.
