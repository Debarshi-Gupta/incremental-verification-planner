# ADR 0021: The derived graph index is an in-process, snapshot-keyed cache, and traversal is bounded by policy

## Status

Accepted

## Context

LLD §13.3 (lines 883-890) requires a graph query strategy that does not "issue recursive SQL on unbounded repository graphs for every plan": on snapshot ingestion it asks for validated and deduplicated nodes/edges, adjacency lists precomputed by edge type, connected target/module mappings, an affected-target index for common source→target queries, the immutable raw graph kept as the source of truth, and a read-only derived index cached by snapshot hash and algorithm version. It adds that very large graphs must be traversed in bounded batches with node/depth limits from policy, and that "if a bound is reached, mark graph incomplete and escalate; never silently truncate and return a confident selective plan".

What exists: `internal/analysis/graph.go` rebuilt the reverse-dependency, coverage and dependency adjacency maps for every request (the strategy §3.4.1 gap), there was no affected-target index, no derived-index cache, and `domain.Policy` had no traversal-limits field at all.

The LLD fixes the requirements but not where the derived index lives, how it is validated on reuse, or how the traversal limits are configured.

## Decision

**1. The derived index is an in-process, snapshot-keyed cache.** `internal/analysis/derived_index.go` derives, once per snapshot: adjacency lists by edge type (`ReverseDeps`, `ReverseCoverage`, `DependencyAdj`, `ContractConsumers`), connected target/module mappings (`ConnectedTargets`, connected in both directions so a lookup never misses a neighbour), and an affected-target index (`AffectedTargets`, source path → affected build/test target keys). The immutable raw graph stays the source of truth; the index is derived from it and never overrides it.

**2. The cache key is the snapshot hash and the algorithm version** (`DerivedIndexKey`), exactly as the LLD states. Because a hash can only identify content if the content is the same, a cached index is additionally validated against an order-independent **content fingerprint** of the nodes and edges before it is reused; a mismatch rebuilds the index. (Counting nodes and edges is not enough: an earlier draft of this implementation reused an index for a different graph with identical counts, which silently produced an empty traversal — the fingerprint is the fix.)

**3. The affected-target closure is computed from the target side.** For every build/test target the index walks its dependency adjacency once and records the source files it reaches. Targets are typically far fewer than files, so the cost follows the target count instead of the file count.

**4. Traversal limits live on `domain.Policy.TraversalLimits`** (`MaxNodes`, `MaxDepth`), matching the LLD's `effectivePolicy.traversalLimits`. A zero bound means unbounded. The effective policy sets concrete bounds (100 000 nodes, depth 64) in `config.ToDomainPolicy`, the same place the other policy-level graph thresholds (`DependencyFanoutEscalation`, `DependencyCap`) are set, so no new configuration key, schema change or template change is introduced.

**5. A reached bound marks the graph incomplete and escalates.** `AnalyzeImpactIndexed` stops expanding and sets `ImpactResult.Incomplete` with the reason naming the bound; the planner records `GRAPH_INCOMPLETE` with that reason, which forces `FULL_REQUIRED`. The traversal is never truncated silently.

**6. The index is bounded in memory.** The cache keeps at most eight entries and evicts the least recently used one, so a long-lived process cannot grow without limit.

## Alternatives considered

1. **Materialized SQL views plus recursive SQL per plan.** Rejected: it puts a database round-trip on the planning hot path, needs a migration whenever the graph model changes, and cannot express the connected target/module mapping the LLD asks for. The in-process index is derived from the same immutable raw graph and is cached per snapshot.
2. **Unbounded traversal.** Rejected: the LLD explicitly requires node/depth bounds for very large graphs and forbids silently truncated confident plans.
3. **Validating the cache by node and edge counts only.** Rejected: counts do not identify content; the implementation bug described in decision 2 is the proof.
4. **New configuration keys for the traversal bounds.** Rejected for now: the sibling graph thresholds are policy-level constants rather than configuration keys, and adding keys would touch the schema, both templates and the parity tests for no operational gain. A future ADR can expose them when an operator needs to tune them.
5. **Exposing the affected-target index through a new adapter/graph API.** Rejected: the index is derived inside the analysis package, and the planner consumes it through `AnalyzeImpactIndexed`; no public surface change is needed.

## Consequences

Repeated plans against the same snapshot reuse one derived index instead of rebuilding adjacency per request, the affected-target index answers the common source→target query directly, and a graph too large for the policy bounds produces an explicit `GRAPH_INCOMPLETE` escalation instead of a confident but truncated selective plan. Inventory rows `IVP-13-3-a18d1ebe10`, `IVP-13-3-ca3c110287`, `IVP-13-3-52f080dd9b` and `IVP-APPENDIX-C-ae232967e6` are satisfied by this ADR's implementation; the persistence-side materialized views of row `IVP-2-2-be6eab19d1` remain a documented limitation.
