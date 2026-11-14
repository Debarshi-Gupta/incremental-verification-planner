# ADR 0020: Binary changes lower selection confidence by a fixed factor unless build metadata identifies their consumers

## Status

Accepted

## Context

LLD line 188 (§4.3) fixes the rule but not its magnitude: "Binary files cannot use line-level coverage and therefore lower selection confidence unless build metadata identifies their consumers." The inventory row `IVP-4-3-b8c7109977` asks for the same behavior and notes that the only existing artifact was an unused `domain.ChangedFile.IsBinary` field — nothing produced it and nothing consumed it.

Three questions had no LLD answer: how binary-ness is detected, how much confidence is lowered, and what "build metadata identifies their consumers" means operationally.

## Decision

**1. Binary detection happens in the Git producer, from two independent signals.** `internal/gitutil/git.go` marks a change binary when Git's diff prints `Binary files ... differ` (authoritative for tracked modifications, deletions, and renames) **or** when the working-tree file contains a NUL byte in its first 8000 bytes (Git's own heuristic, covering added and untracked files that are not part of the diff). The signal populates the existing `domain.ChangedFile.IsBinary` field; no new schema or vocabulary is introduced.

**2. Line-level coverage over a binary path is never used.** In the planner's coverage loop (`internal/analysis/planner.go`), a `CoverageLine` observation whose covered path is binary is downgraded to file granularity before the quality factor and the reason code are computed. The selection therefore never claims `COVERS_CHANGED_LINE` for binary content.

**3. Confidence is lowered by a fixed multiplicative factor of 0.75** (`binaryConfidenceFactor`), applied only when a binary changed file's consumers are **not** identified by build metadata. The existing `LOW_SELECTION_CONFIDENCE` rule then decides the outcome, so a binary change whose consumers are unknown escalates to full verification instead of claiming a selective plan — the conservative direction the LLD intends. The factor is a single named constant, not a hidden weight change, so it can be retuned without touching the confidence formula.

**4. "Build metadata identifies their consumers" means the dependency graph names them.** A binary file whose graph node has known reverse-dependency consumers (`ReverseFanout > 0` in the impact analysis) keeps its confidence; a binary file with no identified consumers is penalized. A binary file that is not in the graph at all is therefore penalized, which is the conservative direction.

**5. The reason is visible, never silent.** The `LOW_SELECTION_CONFIDENCE` escalation detail names the affected binary paths, so the escalation says why it happened.

## Alternatives considered

1. **Escalate every binary change unconditionally.** Rejected: the LLD explicitly carves out the case where build metadata identifies the consumers, so an unconditional escalation would contradict the rule and make the exception dead text.
2. **Treat binary coverage as file precision only and change nothing else.** Rejected: the LLD requires the confidence to be lowered; a file-precision downgrade alone moves the number by ~0.002 and would let an unknown-consumer binary change keep a selective plan.
3. **Subtract a fixed absolute amount instead of multiplying.** Rejected: an absolute subtraction interacts badly with the weighted sum (a low-confidence plan could be pushed negative and clamped), while a multiplicative factor scales the whole estimate proportionally and stays inside `[0,1]`.
4. **Detect binary content only by sniffing the working tree.** Rejected as the sole signal: deleted binary files have no working-tree content, so their detection would depend on the file still existing.
5. **Add a new escalation code such as `BINARY_CHANGE`.** Rejected: Appendix A's reason-code vocabulary is closed without a documented version change, and the existing `LOW_SELECTION_CONFIDENCE` code already describes the outcome.

## Consequences

A binary change with unknown consumers now escalates with a named reason instead of silently planning selective verification; a binary change whose consumers are identified by the dependency graph keeps its confidence, and its coverage is still never claimed at line granularity. Producer and consumer are both directly asserted (`TestDetectChangesMarksBinaryFiles`, `TestBinaryChangeLowersSelectionConfidenceWithoutIdentifiedConsumers`). Inventory row `IVP-4-3-b8c7109977` is satisfied with the limitation that the confidence factor (0.75) is a documented judgment call rather than an LLD-specified value.
