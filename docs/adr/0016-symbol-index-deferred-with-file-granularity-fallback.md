# ADR 0016: Symbol-level change mapping is deferred; file and build-target granularity is the operating mode

## Status

Accepted

## Context

LLD line 428 (§6.3 Stage 2) requires changed lines to map onto impacted entities and then says: "Otherwise use file and build-target granularity." LLD line 405 lists symbol-level impact as a language-capability-dependent input ("Language-specific: symbols, signatures, dependencies, overrides, macros, generated files"), and row `IVP-6-3-64477ace27` asks for per-symbol mapping of changed lines/symbols onto tests that cover them.

What exists in code:

- `domain.NodeSymbol` is a legal graph-node type (`internal/domain/enums.go:18`) and `domain.CoverageSymbol` a legal coverage precision (`enums.go:59`), with validation cases in `internal/domain/validation.go:88,132`.
- `precisionForNode` already downgrades `SYMBOL` coverage to `CoverageSymbol`, and `reasonForPrecision` already maps `CoverageSymbol` → `COVERS_CHANGED_SYMBOL` (`internal/analysis/planner.go:1467-1490`).
- A repo-wide search found **no producer** of `NodeSymbol` nodes or `SymbolIndex` data anywhere in the repository: no adapter, extractor, or fixture emits symbol-level evidence.

## Decision

**1. No symbol index is built in this implementation.** The LLD's "Otherwise" clause is the normative path here: changed lines/symbols map onto impacted entities at **file and build-target granularity** (`entitiesForCoverage`, `changedLineOverlap` in `planner.go`). Row `IVP-6-3-64477ace27` is satisfied by that path plus the consumer-side assertion below, and its inventory entry records the limitation.

**2. The symbol consumer path is implemented and asserted, not stubbed.** When symbol-level evidence *is* supplied (a `NodeSymbol` graph node reachable through an `EDGE_TEST_COVERS` edge), `reasonForPrecision` emits `COVERS_CHANGED_SYMBOL` exactly as the vocabulary requires. `TestChangedLinesMapAtFileGranularityWithoutSymbolIndex` asserts both halves: line-precision coverage outside the diff hunks does **not** map to `COVERS_CHANGED_LINE`, and no `COVERS_CHANGED_SYMBOL` reason is ever fabricated when no symbol evidence exists.

**3. Revisit criteria.** A future ADR may add a symbol-index producer (language-aware extractor feeding `NodeSymbol` nodes) once at least one adapter can emit symbol evidence with a stable key. At that point only producers change; the consumer mapping asserted here already conforms.

## Alternatives considered

1. **Build a best-effort symbol index now (regex/tree-sitter heuristics).** Rejected: without a language-aware producer the index would be wrong in exactly the ambiguous cases the LLD's conservative fallback exists for, and a wrong symbol index is worse than honest file granularity.
2. **Emit `COVERS_CHANGED_SYMBOL` from file-level overlap as a "conservative" alias.** Rejected: it fabricates precision the evidence does not carry and would poison selection confidence; the reason code's meaning would stop matching the vocabulary.
3. **Delete the symbol consumer path until a producer exists.** Rejected: the vocabulary row requires the mapping to work when precision `SYMBOL` arrives, and deleting it would guarantee a future regression.

## Consequences

Selection operates at file/build-target granularity with a directly asserted symbol consumer path. Inventory row `IVP-6-3-64477ace27` is TESTED with the limitation "no symbol-index producer exists; file/build-target granularity is the LLD-sanctioned operating mode" (ADR-defer). No schema or algorithm-version change is required when a producer later arrives, because `NodeSymbol`/`COVERS_CHANGED_SYMBOL` are already in the domain vocabulary.
