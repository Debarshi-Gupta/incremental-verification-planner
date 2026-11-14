# Adapter sample reports

These are the sample successful and failed reports of every supported adapter.
They are published from the checked-in fixture corpus
(`internal/adapters/fixtures/<adapter>/<tool version>/<category>.<ext>`) by
`scripts/generate-adapter-samples.py`, so the documented samples are the very
reports the adapter contract test consumes; the script's `--check` mode fails
when a sample drifts from its fixture.

| Adapter | Successful report | Failed report | Shape |
|---|---|---|---|
| Gradle | `gradle-success.xml` | `gradle-failure.xml` | JUnit XML |
| Maven | `maven-success.xml` | `maven-failure.xml` | JUnit XML |
| Bazel | `bazel-success.jsonl` | `bazel-failure.jsonl` | build event protocol (JSON lines) |
| pytest | `pytest-success.xml` | `pytest-failure.xml` | JUnit XML |
| Jest | `jest-success.json` | `jest-failure.json` | Jest JSON result |
| Nx | `nx-success.xml` | `nx-failure.xml` | JUnit XML |
| Generic | `generic-success.json` | `generic-failure.json` | generic JSON report |

## The shared contract

Every adapter must satisfy the same contract; the samples show the shapes the
contract expects.

1. **A successful run** reports every executed test as passed, with a stable
   test identity, a duration and a report hash. The canonical outcome is
   `PASS`.
2. **A failed run** reports at least one failed test with its failure class
   and fingerprint. The canonical outcome is `FAIL` for a test failure and
   `INFRA` for a parser, setup or infrastructure failure.
3. **A setup failure is never a pass.** A report that cannot be parsed, a
   truncated report, a report with zero tests and a missing report are all
   classified as infrastructure failures, never as passes.
4. **The outcome classification is identical across adapters.** The shared
   contract test (`internal/adapters/equivalent_outcome_test.go`) classifies
   every fixture of every adapter through one canonical classifier and asserts
   the same outcome for the same category, so the samples above cannot drift
   apart.
5. **The samples are sanitized.** They contain no source content, no
   environment values and no credentials; the artifact sanitizer applies the
   same rules to the reports a run uploads.

The per-adapter boundary (what each adapter may and may not claim) is
documented in `docs/adapter-support.md`.
