# ADR 0013: Convention-derived edge producers, change classification, and environment-mismatch escalation

## Status

Accepted

## Context

LLD §4.4 (edge inventory) and §6.1 name five graph edge types — `BUILT_FROM`, `CONFIGURES`, `CONTRACT_CONSUMER`, `OWNED_BY`, `COLOCATED_WITH` — plus `riskLabels = classifyCriticalPaths(changes, effectivePolicy)` (LLD line 1385), the broad-refactor escalation (LLD line 1407), the Stage-4 `INTEGRATION_BOUNDARY` candidate-union member (LLD line 456), and `ENVIRONMENT_MISMATCH`. Wave 1 found the types declared and validated (`internal/domain/enums.go:36-40`, `validation.go:108`) but **no producer** emitting them, and the classification/broad-refactor predicates unimplemented. The LLD fixes none of the pairing conventions, confidence values, token vocabulary, or thresholds, so the producers could not be written without an explicit decision.

What exists in code:

- Edge *validation* accepts all seven types and requires identity, snapshot, provenance, and confidence in `[0,1]` (`internal/domain/validation.go:100-114`).
- `internal/analysis/conventions.go` now holds the pure producers (`CoLocationPairings`, `BuiltFromPairs`, `ConfiguredByPairs`, `SchemaConsumers`, `ParseCodeowners`, `CodeownerRulesFor`) so `ivp index` and the server-side evidence reader share one implementation.
- `internal/cli/conventions.go` turns those producers into graph evidence (`buildConventionEvidence`, `readCodeowners`, `unitOwnerStableKey`) and `internal/cli/index.go:75,207,440` wires them into the index record.
- `internal/analysis/classify.go` holds `ClassifyRiskLabels`, `DetectBroadRefactor`, `IsIntegrationBoundaryUnit`, and `ReviewedExclusionKeys`; `internal/analysis/planner.go:42,172,184-190,207-229,504-560,644-728` consumes them.
- Conflict marking is `analysis.MarkConflictingEdges`, called on `planEdges` in `internal/service/backend.go` before evidence construction.

## Decision

**1. Producers emit nothing when their input is absent.** No CODEOWNERS file, no generated suffix match, no schema import, no co-located counterpart ⇒ no edge. Missing content only removes evidence; it never fabricates it.

**2. Pairing conventions and confidence values are fixed as follows.**

| Edge | Producer | Confidence | `EvidenceSource` | `Attributes["precision"]` |
|---|---|---|---|---|
| `BUILT_FROM` | `BuiltFromPairs`: exact generated-suffix table (`_pb.go`→`.proto`, `_string.go`→`.go`, `_grpc.pb.go`→`.proto`, …); most specific suffix wins; input must be tracked | 0.9 | `conventions:generated-suffix` | `GENERATED_SUFFIX` |
| `CONFIGURES` | `ConfiguredByPairs`: build/test target path → `isBuildConfigPath` config with the **nearest ancestor directory** (longest matching `path.Dir`); repo-root configs match any target; equal-distance ties resolve to the first config in sorted tracked-path order | 0.85 | `conventions:nearest-config` | `NEAREST_ANCESTOR_CONFIG` |
| `CONTRACT_CONSUMER` | consumer file → `api:<schema path>`; `SchemaConsumers` sniffs tracked non-test files for an exact proto `import` or a case-insensitive byte reference to the schema path, bounded at **512 KiB per file** and **8 MiB per schema**, skipping unreadable/oversized files | 0.7 | `conventions:schema-reference` | `SCHEMA_REFERENCE` |
| `OWNED_BY` | every tracked **source-file node** and every **unit node** → `owner:<handle>`; ownership comes only from explicit CODEOWNERS files (no fallback) | 0.9 | `conventions:codeowners` | `CODEOWNERS` |
| `COLOCATED_WITH` | `CoLocationPairings`: conventional test path → its source counterpart when both are tracked; a test never pairs with another test | 0.5 | `conventions:co-location` | `CONVENTION_COLOCATION` |

**3. CODEOWNERS handling.** All three standard locations (`.github/CODEOWNERS`, `CODEOWNERS`, `docs/CODEOWNERS`) are read in that order and **unioned**: a path's owners are every handle from every matching rule across every file. Handles are kept verbatim including a leading `@`; `!`-negated rules are dropped. Pattern normalization: a leading `/` marks a root-relative pattern and is stripped, a trailing `/` becomes a directory pattern (`dir/` → `dir/**`), and a still slash-free pattern matches at any depth (`**/name`).

**4. Test-path conventions include pytest's default `test_*.py`** in `isTestPath`/`testStem`, alongside `_test.go`, `.test.ts`, `.spec.js`, `Test.java`, `_spec.rb`, `_test.php`, and friends.

**5. Convention nodes are created lazily with stable keys** — `owner:<handle>`, `config:<path>`, `api:<path>` (`StableOwnerKey`/`StableConfigKey`/`StableSchemaKey`) — so repeated indexing of the same repository produces identical node identities (`graph_nodes UNIQUE(snapshot_id,node_type,stable_key)`). Edges are deduplicated by `(from, to, type)`; an edge whose endpoints are missing or identical is dropped.

**6. A unit records its owner as `Metadata["owner"]` = the first owner of its first owned source path** (empty ⇒ the key is absent). The planner's owner mapping compares that value against the owner-node stable keys of impacted nodes, so file-anchored `OWNED_BY` edges reach units.

**7. `unitForNode` resolves `file:` keys through `unitsBySourcePath`.** Convention edges are file-anchored, but candidates are units; without this resolution `COLOCATED_WITH`/`CONTRACT_CONSUMER` edges could never contribute coverage.

**8. `COLOCATED_WITH` cannot alone justify a selection.** The planner multiplies edge confidence by 0.25, so a 0.5-confidence convention edge yields coverage quality **0.125** — below every shipped coverage threshold. The escalation is asserted in `TestColocationEdgeCannotAloneJustifySelectivePlan`.

**9. `CONTRACT_CONSUMER` expands candidates but never propagates impact.** A changed schema reaches its consumers from the impacted schema side (`planner.go:551-559`); a changed consumer reaches the schema from the impacted consumer side (`planner.go:535-547`). Both set `PUBLIC_CONTRACT_CONSUMER`.

**10. Change classification (`classifyCriticalPaths`).** Labels are derived from path tokens only — never file content. `pathTokens` unions plain split-on-non-alphanumeric tokens with camelCase-boundary-split tokens (both lowercased), so `integrationTest` yields `integration`, `authMiddleware` yields `auth`, and `author` stays a whole token that never classifies as authentication. Vocabulary lives in `riskLabelTokens`; detection order is the canonical `riskLabelOrder` (AUTHENTICATION, AUTHORIZATION, CRITICAL_PATH, CRYPTOGRAPHY, DATA_MIGRATION, DEPLOYMENT, PAYMENT, PUBLIC_CONTRACT, RELEASE, SECURITY_CRITICAL) so byte-identical change sets produce byte-identical labels. **Labels already present on a change are retained**: non-canonical extras are trimmed, deduped, and sorted after the canonical ones. Paths matching `DocumentationPathPatterns` contribute **no** path-derived label — a documentation edit must never force the full suite.

**11. Broad refactor = ≥25 changed files AND ≥10 distinct directories** (`broadRefactorMinFiles`, `broadRefactorMinDirs`), evaluated over both current and old paths, and **never** for a documentation-only change set. Triggering escalates `TOO_MANY_CHANGED_FILES`.

**12. `ENVIRONMENT_MISMATCH` escalates only when a fingerprint is declared but not comparable.** If `req.EnvironmentFingerprint` is non-empty and `evidence.ToolchainComparable` is false, no environment-sensitive evidence in the snapshot can be trusted for this request, so verification falls back to the full suite. When the fingerprint is absent (the e2e path sends none) the rule is recorded as evaluated-and-not-triggered rather than escalated — escalating on an undeclared fingerprint would make every offline plan `FULL_REQUIRED`.

**13. Conflict predicate (`MarkConflictingEdges`).** Two situations mark an edge as `Conflict` without ever dropping it: (a) duplicate `(from, to, type)` edges whose `precision` attributes differ, and (b) opposite-direction pairs of the same edge type. All involved edges are retained and marked so the effective relation reads as uncertain; re-running on already-marked edges marks nothing new. The planner escalates `GRAPH_CONFLICT` when the evidence declares conflicts, and `graphConfidence` zeroes a conflicting edge's contribution.

## Alternatives considered

1. **Emit edges at a single fixed confidence (say 1.0) and let the planner discount them.** Rejected: confidence is the graph's honesty about provenance, and `graphConfidence` averages impacted-edge confidence into selection confidence. A convention pairing is weaker evidence than an observed test-coverage edge, and flattening them would let a co-location guess carry the same weight as a measured run.
2. **Infer ownership from commit history or `git blame` when CODEOWNERS is absent.** Rejected: it fabricates evidence the LLD does not sanction, is nondeterministic across clones, and would make `OWNED_BY` unreviewable. The LLD's posture elsewhere is to omit rather than guess.
3. **Propagate impact along `CONTRACT_CONSUMER` edges.** Rejected: the LLD treats consumer expansion as a *candidate-union* rule (line 17.4), not an impact rule. Propagating would enlarge the blast radius of every schema edit and inflate `ReverseFanout`, biasing both risk and the fanout escalation.
4. **Classify risk labels from file contents (imports, decorators, annotations).** Rejected: content sniffing is language-specific, expensive, and unstable across formatter/refactor noise. Path vocabulary is deterministic, reviewable, and matches the LLD's "classifyCriticalPaths(**changes**)" signature.
5. **Raise `COLOCATED_WITH` above the coverage thresholds so co-location alone can justify skipping.** Rejected outright: LLD line 203 calls this edge low-confidence evidence that "cannot alone justify skipping broader tests", and the 0.5 × 0.25 = 0.125 product is what makes that sentence true in code.
6. **Escalate `ENVIRONMENT_MISMATCH` whenever fingerprints differ.** Rejected: fingerprints frequently differ benignly (host vs. container) and the LLD gives no comparability oracle; escalating on *difference* would either force full suite everywhere or require a heuristic. Escalating only on *declared-but-incomparable* is the conservative reading that still lets an undeclared-fingerprint flow proceed.

## Consequences

`ivp index` now emits a strictly larger, deterministic graph; repositories with no conventions change nothing, so no existing snapshot becomes stale by this ADR alone. The five edge types are consumed by the planner for candidate reasons and coverage, and `MarkConflictingEdges` runs on every server-side plan so contradictory convention evidence escalates rather than silently merging. Because confidence values are load-bearing (they feed `graphConfidence` and coverage quality), changing any value in the table above is an evidence-format change requiring a graph-version bump. Inventory rows `IVP-4-4-5272a642b2`, `IVP-4-4-c07cfda714`, `IVP-4-4-d10360c18b`, `IVP-4-4-b8dcc3d843`, `IVP-4-4-3fb68cd04d`, `IVP-4-4-4f754381aa`, `IVP-17-4-c46190ee6a`, `IVP-APPENDIX-A-f6b14b28d8`, `IVP-APPENDIX-A-07ff7ca869`, `IVP-APPENDIX-A-9561eb2bff`, `IVP-APPENDIX-A-dab19765b4`, and `IVP-APPENDIX-C-2384312418` are reviewed against this ADR.
