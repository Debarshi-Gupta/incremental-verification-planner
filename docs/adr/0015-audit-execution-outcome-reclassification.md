# ADR 0015: An audit reclassifies the audited execution's stored outcome, and AUDIT_* counts as its originating pass

## Status

Accepted

## Context

LLD Appendix B defines `AUDIT_MISS`, `AUDIT_POSSIBLE_MISS`, and `AUDIT_INCOMPARABLE` as execution outcomes, but the LLD does not say *when* an existing execution's outcome is rewritten to one of them, nor how a later audit should treat an execution an earlier audit already reclassified. Two failure modes follow: leaving the stale `TARGETED_PASS` in place hides that the run was later contradicted, and rejecting a re-audit of a reclassified execution would make the audit chain unable to correct itself.

What exists in code:

- The three outcomes are declared in `internal/domain/enums.go:163-165` with `IsPassOutcome`, `IsAuditOutcome`, and `IsPassLikeOutcome` helpers (`enums.go:168-186`).
- `audit.Classify` decides the disposition; an `AUDIT_*` selected outcome must still reach the miss analysis (`internal/audit/classifier.go:98-112`).
- `persistence.AuditOutcomeTransition` (pure) maps disposition → outcome, and `auditOutcomeMatches` (pure) reconciles a persisted outcome against a declared one (`internal/persistence/audits.go:135-163`).
- `RecordAuditAndQuarantine` performs the guarded `UPDATE executions SET outcome=…` inside the audit transaction (`audits.go:70-83`).
- `api/server.go` accepts the three values as valid execution outcomes; `audit/classifier.go` classifies through `IsPassLikeOutcome`.

## Decision

**1. Disposition maps to outcome one-to-one.**

| Audit disposition | New execution outcome |
|---|---|
| `CONFIRMED_MISS` | `AUDIT_MISS` |
| `POSSIBLE_MISS` | `AUDIT_POSSIBLE_MISS` |
| `INCOMPARABLE` | `AUDIT_INCOMPARABLE` |
| `NO_MISS_OBSERVED`, `NOT_ATTRIBUTABLE`, `AUDIT_FAILED` | *unchanged* |

A clean, not-attributable, or failed audit establishes nothing about the run, so it must not overwrite the outcome the run earned.

**2. Reclassification is allowed only from a pass-like outcome.** The current outcome must be a `TARGETED_PASS`/`FULL_VERIFICATION_PASS` or an existing `AUDIT_*`. Failing (`TARGETED_FAIL`, `FULL_VERIFICATION_FAIL`), `INFRA_FAILURE`, and `STALE` executions keep their outcome: an audit cannot upgrade a failure into a pass-shaped record, and a failed run is already the conservative answer. This is enforced in the pure `AuditOutcomeTransition`, so the rule is testable without a database.

**3. `AUDIT_*` is equivalent to its originating pass everywhere an outcome is compared.** Both `auditOutcomeMatches` (does the persisted outcome satisfy the outcome an audit input declares?) and `audit.Classify`'s pass gate (`IsPassLikeOutcome`) treat an `AUDIT_*` outcome as the pass it was reclassified from. Consequences: a re-audit may declare `TARGETED_PASS` for an execution now stored as `AUDIT_MISS` and still be accepted, and a re-audit of a reclassified execution still reaches the miss analysis rather than falling into the "selected run failed, no miss" branch. Equivalence requires **both** sides to be pass-like and at least one to be `AUDIT_*`, so `AUDIT_MISS` never matches `TARGETED_FAIL`.

**4. The rewrite is a guarded, in-transaction update.** The audit transaction re-reads the selected execution's outcome, validates it against the declared one, then runs `UPDATE … WHERE tenant_id=$1 AND id=$2 AND outcome=$4`. If `RowsAffected() != 1` the outcome changed concurrently and the whole audit fails with *"audit selected execution outcome changed concurrently"* rather than silently overwriting a competing re-audit. The audit record insert and the outcome update share one transaction, so no state exists in which an audit record says one thing and the execution says another.

**5. The transition is exposed as pure functions** (`AuditOutcomeTransition`, `auditOutcomeMatches`) precisely so the rules above carry direct local assertions; the SQL path is exercised by the integration-tagged scenario tests.

## Alternatives considered

1. **Never rewrite the stored outcome; record the reclassification only in `audit_records`.** Rejected: `executions.outcome` is what dashboards, SLO queries, and calibration read. Leaving `TARGETED_PASS` in place after a confirmed miss would report a contradicted run as a success and corrupt every downstream metric.
2. **Reject any audit whose declared outcome differs from the stored one.** Rejected: it makes the chain non-idempotent and non-correctable — once an execution is reclassified, no further audit could ever run against it, so an erroneous first audit could never be fixed.
3. **Allow reclassification from failing outcomes too (e.g. `TARGETED_FAIL` → `AUDIT_INCOMPARABLE`).** Rejected: `INCOMPARABLE` describes a comparison that could not be made, and rewriting a legitimate failure with it would erase a true negative. Failures are already the conservative terminal state.
4. **Treat each `AUDIT_*` value as distinct from every other during matching (no pass-equivalence).** Rejected: it would require the caller to know which pass an execution came from, information the database holds and the caller does not, and would make re-audits fail on correct input.
5. **Perform the update outside the audit transaction with best-effort semantics.** Rejected: it opens a window where the audit record and the execution outcome disagree, which is exactly the inconsistency the transaction exists to prevent.

## Consequences

An audited run's stored outcome now reflects what the audit established, so reporting and calibration see `AUDIT_*` instead of a stale pass, while a failed run is never rewritten. Re-audits are idempotent and correctable: the same pair can be audited repeatedly, and a later audit supersedes an earlier one under a row-level guard. Because equivalence is load-bearing for both matching and classification, narrowing `IsPassLikeOutcome` would break re-audit flows — it is a contract, not an implementation detail. The DB rewrite itself is covered by the CI-tagged integration test (`internal/service/audit_scenarios_integration_test.go`), while the decision rules are asserted locally by `internal/persistence/audits_test.go` and `internal/audit/classifier_test.go`. Inventory rows `IVP-APPENDIX-B-e1086ec7de`, `IVP-APPENDIX-B-04e643ccd6`, and `IVP-APPENDIX-B-01bd3ba3f1` are reviewed against this ADR.
