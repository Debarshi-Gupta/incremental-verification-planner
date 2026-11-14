# ADR 0018: Rule 8's "too many affected targets" trigger reuses the changed-files cap and the TOO_MANY_CHANGED_FILES code

## Status

Accepted

## Context

LLD line 442 (§6.3 rule 8) escalates when "changed files, affected targets, or dependency fanout exceed configured limits". Row `IVP-6-3-e1060ab19f` requires the affected-targets trigger. Two questions had no LLD answer: which configuration key caps affected targets (none exists), and which escalation code it reports (Appendix A pins the vocabulary to existing codes — `TOO_MANY_CHANGED_FILES` is the rule-8 family code).

## Decision

**1. The affected-target cap reuses `max_changed_files_for_selective_plan`.** One configured blast-radius limit guards both size triggers of rule 8: a change that fans into more build/test targets than it has changed files is already covered conceptually by the same "too big to select" knob. No new configuration key is introduced, so schema, templates, and precedence stay untouched by this row.

**2. The trigger shares the `TOO_MANY_CHANGED_FILES` escalation code.** Appendix A's reason-code vocabulary is closed; adding an `AFFECTED_TARGETS` code would require a documented schema/algorithm version change for a distinction the LLD does not make. The escalation **detail** distinguishes the trigger: `"affected target count N exceeds limit M"` versus the changed-file and broad-refactor details.

**3. Count definition:** impacted nodes (`impact.ImpactedNodeIDs`) whose `NodeType` is `NodeBuildTarget` or `NodeTestTarget`. The gate evaluates as a hard rule with an explicit pass check (`"affected target count N is within policy"`) so `HardRuleChecks` records rule 8's outcome either way.

**4. Cap of 0 disables the target-count trigger** (same convention as the changed-file count), and the check is recorded as satisfied.

## Alternatives considered

1. **Add `max_affected_targets_for_selective_plan`.** Rejected: a second cap with the same semantics invites divergent tuning (one loose, one tight) that silently defeats the rule; nothing in the LLD asks for two knobs.
2. **Report the fanout escalation code (`HIGH_DEPENDENCY_FANOUT`).** Rejected: fanout is rule 9's reverse-dependency metric, a different rule; conflating them would misreport which safety condition fired.
3. **New `AFFECTED_TARGETS` escalation code.** Rejected: the Appendix A vocabulary is closed without a version change, and the detail string already carries the distinction.

## Consequences

Rule 8 now has all three LLD triggers implemented under one code with distinct details (`TestHardRuleBlastRadiusTriggers` asserts each sub-trigger and the within-caps pass check). Operators tune blast radius with one key. Rows `IVP-6-3-e1060ab19f` and the rule-8 parts of `IVP-6-3-afb679b66c` are satisfied; the broad-refactor trigger's "many files across many directories" heuristic predates this ADR and keeps its detail wording.
