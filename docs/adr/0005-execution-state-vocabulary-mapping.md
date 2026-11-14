# ADR 0005: Execution state vocabularies are mapped, and FAILED_TESTS is an outcome

## Status

Accepted

## Context

LLD §9.2 (lines 680-685) specifies `CREATED -> VALIDATING -> RUNNING -> PARSING -> COMPLETED` with alternative terminal/error states `CANCELLED, TIMED_OUT, INFRA_ERROR, STALE_PLAN, ESCALATED, FAILED_TESTS`, and requires that a state transition be persisted locally before a remote status update is attempted. The implementation uses three smaller vocabularies that do not use those names:

1. The CLI's persisted local run record (`internal/cli/run.go:30-33`, `State string`) uses `START_REQUESTED` (`run.go:295`), `EXECUTING` (`run.go:309`, resume at `run.go:425`), `RESULT_READY` (`run.go:603`, `run.go:625`), `UPLOADED` (`run.go:1414`), and `OFFLINE_COMPLETE` (`run.go:381`).
2. The execution runner's terminal status (`internal/execution/runner.go:27-36`) is `COMPLETED, CANCELLED, TIMED_OUT, INFRA_ERROR, STALE_PLAN, ESCALATED`, stored on the server row as `executions.status` (insert as `RUNNING` at `internal/persistence/plans_executions.go:151`, terminal update at `plans_executions.go:468`).
3. Plans additionally carry `internal/domain/enums.go:74-84` (`READY, FULL_REQUIRED, STALE, EXECUTING, PASSED, FAILED, CANCELLED, ERROR`).

`CREATED`, `VALIDATING`, `PARSING`, and `FAILED_TESTS` appear nowhere as persisted states. Test failures are carried by the result classification instead: `execution.Status` plus `domain.ExecutionOutcome` (`TARGETED_FAIL`/`FULL_VERIFICATION_FAIL` set at `runner.go:397-402`), the process exit code (1 for test failure), and plan status `FAILED`. Renaming the persisted local states would strand records written by interrupted runs (resume logic keys off the literal strings at `run.go:411`, `run.go:431`, `run.go:1402`, `internal/cli/audit.go:141`, `audit.go:330`) and would change the exit-code/report behaviour covered by existing tests, without changing what is stored or when.

## Decision

- Keep all three code vocabularies; document the mapping to the LLD state machine instead of renaming persisted data.
- Mapping (LLD state -> code representation):

  | LLD §9.2 state | Code representation |
  |---|---|
  | `CREATED` | `localRun.State = START_REQUESTED`, written once the plan is fetched and validated (`run.go:295`) |
  | `VALIDATING` | folded into `START_REQUESTED`: validation happens between plan fetch and record creation, so a validation failure produces no run record and a non-zero exit instead of a stuck state |
  | `RUNNING` | `EXECUTING` locally (`run.go:309`, `run.go:425`) and `RUNNING` on the server row (`plans_executions.go:151`) |
  | `PARSING` | folded into the `EXECUTING -> RESULT_READY` transition: parsing is synchronous in the same process; a parse failure is classified `REPORT_PARSE`/`ZERO_TESTS` infra error (`runner.go:372-389`) and recorded with `RESULT_READY` |
  | `COMPLETED` | `RESULT_READY` (results durable locally, `run.go:603`), then `UPLOADED` after remote acknowledgement (`run.go:1414`) or `OFFLINE_COMPLETE` for the full-suite fallback path (`run.go:381`) |
  | `CANCELLED`, `TIMED_OUT`, `INFRA_ERROR`, `STALE_PLAN`, `ESCALATED` | `execution.Status` (`runner.go:30-35`) plus the matching exit code |
  | `FAILED_TESTS` | outcome, not a state: `OutcomeTargetedFail`/`OutcomeFullVerificationFail` (`runner.go:397-402`), exit code 1, plan status `FAILED` (`enums.go:82`) |

- The LLD persistence guarantee holds for every mapped transition: each state is saved through `saveLocalRun` before any upload is attempted (`run.go:310`, `run.go:604`, `run.go:1414`), and uploads are idempotent, so a crash between persist and upload is recoverable by resume.
- Consumers must continue to read `Outcome` (never the lifecycle state) to decide whether tests failed; report classification, audit selection (`audit.go:330`), and exit codes are all keyed on it.

## Alternatives considered

1. Rename the code vocabulary to the LLD names. Rejected: it changes persisted on-disk state strings that interrupted runs must resume from, ripples through resume/audit/exit-code paths, and gains no correctness because the information already exists.
2. Add `FAILED_TESTS` as a fourth state alongside `Outcome`. Rejected: two independent fields would then encode test failure and could disagree; every existing consumer keys off `Outcome`, and a state-only signal would let a record claim `FAILED_TESTS` while the outcome says otherwise.
3. Persist `VALIDATING` and `PARSING` as distinct states. Rejected: both are bounded synchronous steps inside one process; persisting them adds writes no consumer reads, and failure handling is already explicit (`REPORT_PARSE`, non-zero exit before record creation).

## Consequences

Requirement rows for LLD §9.2 are reviewed against this mapping, with the folding of `VALIDATING`/`PARSING` and the outcome-based `FAILED_TESTS` recorded as limitations where the literal LLD state names are absent. Anyone adding a new execution lifecycle state must extend this table and persist it locally before remote updates. If a future API consumer genuinely needs a `FAILED_TESTS` state, it must be derived from `Outcome` at the API boundary rather than stored twice.
