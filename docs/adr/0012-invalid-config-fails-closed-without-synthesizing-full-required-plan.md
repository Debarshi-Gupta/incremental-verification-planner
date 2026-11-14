# ADR 0012: Invalid configuration fails closed (error + exit, no plan) instead of synthesizing a FULL_REQUIRED plan

## Status

Accepted

## Context

LLD line 272 states: "Configuration is schema-validated on every run. Unknown keys are errors rather than ignored values. Invalid config results in FULL_REQUIRED plus a useful error; the CLI must not guess missing fields." Read literally, an invalid `.ivp/config.yaml` should yield a plan document with status `FULL_REQUIRED` *and* a useful error. Wave 1 inventory review (`IVP-5-1-11cc2c82c7`) found the implementation takes the strictly more conservative branch: it refuses to produce a plan at all.

What exists in code:

- Strict parsing: `config.Decode` enables `yaml.Decoder.KnownFields(true)`; unknown keys, duplicate keys, multiple YAML documents, and trailing data are errors wrapped in `ErrInvalidConfig` (`internal/config/load.go:36-52`). Validation failures surface through the same path (`load.go:49`).
- No guessing: `config.LoadFile` overlays the repository file onto `config.Defaults()` and validates the result (`load.go:15-32`); a missing file yields validated product defaults (precedence layer 1, LLD line 266), and an invalid file yields an error — in neither case are field values inferred from partial input.
- Fail-closed CLI: every config-consuming command runs through `withConfig`, which converts a load/validation failure into `exitError{2, err}` (`internal/cli/cli.go:390-396`); `cmd/ivp/main.go:30-33` prints the error to stderr and exits with `cli.ExitCode` (2 for configuration/usage, 4 for trust/policy, 5 for policy changed, 6 unclassified; `internal/cli/cli.go:34-40`). Required-but-missing fields are likewise errors, not guesses: empty base ref (`internal/cli/plan.go:25-27`), unconfigured or non-absolute server endpoint (`internal/cli/client.go:83-89`), untrusted/unverifiable policy envelope failing closed to the full-suite fallback (`client.go:54-66`), policy version changed while planning (`plan.go:115-116`).
- FULL_REQUIRED where a plan does exist: `CreatePlan` initializes every plan with `Status: domain.PlanFullRequired` and only relaxes to READY after every hard check passes (`internal/analysis/planner.go:47-57`); escalations re-enter `fullRequired` (`planner.go:944-959`). Any unverified input therefore produces a real plan whose status is FULL_REQUIRED with a configured full-suite fallback unit.
- Tests: unknown-key rejection and the ~40 schema↔`Validate()` boundary verdicts (`internal/config/schema_parity_test.go`), unsafe repository overrides rejected (`internal/config/config_test.go:93-108`), FULL_REQUIRED defaults and escalations (`internal/analysis/planner_test.go:41-88`, `tests/e2e/planner_test.go:14-44`).

## Decision

- **Fail closed on invalid configuration: the CLI aborts with the `ErrInvalidConfig` detail on stderr and exit code 2, and produces no plan document.** This is the conservative reading of LLD line 272: refusing every plan is strictly stronger than emitting a `FULL_REQUIRED` plan, because no artifact exists that a downstream consumer could mistake for a usable selection, and "the CLI must not guess missing fields" holds literally — no value from a rejected document is ever applied.
- **`FULL_REQUIRED` remains the status of every plan the planner creates whenever any input is unverified** (default status at `planner.go:50`, escalations at `planner.go:944`), which is where the LLD's "results in FULL_REQUIRED" outcome is realized for plans that do exist — including the configured full-suite fallback unit and escalation reasons attached to the plan.
- **Exit codes are the "useful error" channel** (2 configuration, 4 trust/policy, 5 policy changed, 6 evidence/snapshot), so scripts can distinguish "fix your config" from "fix your environment" without parsing text.
- This ADR is the referenced interpretation for inventory row `IVP-5-1-11cc2c82c7`; the row is reviewed as IMPLEMENTED against this ADR rather than against the literal artifact wording.

## Alternatives considered

1. **Emit a synthetic plan with status `FULL_REQUIRED` when config validation fails.** Rejected: plans are immutable and execution-bound (LLD line 256); synthesizing one from a document that just failed validation would bake unvalidated values (argv, thresholds, endpoints) into an artifact consumed later by the executor, and no producer/test path exists for such a document today. It would also weaken fail-closed behaviour: callers could receive "a plan" and proceed where today they must stop.
2. **Fall back to product defaults with a warning when the repository file is invalid.** Rejected: this is exactly the guessing LLD line 272 forbids — the user's file was rejected, so defaults may not reflect intent (for example a typo'd key silently dropping a hard-full-suite path rule while the run proceeds as if configured).
3. **Record the literal wording as an unmet criterion (row NOT_STARTED / gap-register item).** Rejected: every safety property the sentence asks for — schema validation on every run, unknown keys as errors, no guessing, a useful error, and full-verification obligation whenever anything is unverified — is implemented and tested; only the shape of the artifact differs, and the shape actually shipped is the safer of the two.

## Consequences

Reviewers and inventory rows treat exit-code-2-with-detail as satisfying "invalid config results in FULL_REQUIRED plus a useful error", with this ADR cited wherever the literal wording appears, so the LLD-vs-implementation difference is an acknowledged decision rather than contradictory open criteria at the Wave 1 exit gate. Operators and CI must treat exit code 2 as a hard configuration failure to be fixed before verification (never as a retryable or ignorable condition), and the full-suite obligation in that state falls on the operator — consistent with ADR 0006's fail-closed offline posture. If a future release needs the literal artifact (for example an API-driven flow that must return a plan envelope for every request), it should synthesize `FULL_REQUIRED` plans only from *validated* defaults and attach the validation error as an escalation reason, never from the rejected document itself.
