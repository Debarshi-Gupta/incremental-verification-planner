# Adapter golden fixture corpus

Static report fixtures consumed by the shared equivalent-outcome contract
test `internal/adapters/equivalent_outcome_test.go`. The corpus implements
strategy §3.1 item 1 and the LLD §8.9 sentence: "Each adapter has golden
test fixtures from at least two supported tool versions and tests for
malformed reports, paths with spaces, unicode paths, timeouts, and
zero-test output."

Evidence date: 2026-11-10. The files are hand-maintained test data — no
generator is checked in — so edit them directly and keep the expected
layout table inside the contract test in sync.

## Layout

```
fixtures/<adapter>/<tool-version>/<category>.<ext>
```

14 version directories × 9 files = **126 files** (7 adapters × 2 tool
versions × 9 file-backed categories), plus this README. Eleven categories
exist in total; two of them (`missing`, `unknown_target`) are
absence-asserted and therefore have no file.

## Categories and expected canonical outcome

| category | file | canonical outcome | shape |
|---|---|---|---|
| `full_suite` | yes | `PASS` | whole suite executed, all green |
| `selected_suite` | yes | `PASS` | one selected target/class, green |
| `failure` | yes | `FAIL` | one reported test failure, exit 1 |
| `skipped` | yes | `PASS` | skips **alongside passes** (see note below) |
| `zero_tests` | yes | `ZERO` | report present, zero executed tests, exit 0 |
| `malformed` | yes | `INFRA` | syntactically invalid report (bad XML attrs, broken JSON) |
| `truncated` | yes | `INFRA` | report cut mid-element/mid-token |
| `missing` | **no** (absence) | `INFRA` | declared report path, file never written, no stdout |
| `timeout_interrupt` | yes (`.txt`) | `INFRA` | file holds the interrupted **stdout**; the declared report path is never written; exit 124 |
| `unknown_target` | **no** (absence) | `INFRA` | `Materialize` rejects a key absent from the snapshot with `ErrUnknownTarget`, then an absent-report `ParseResults` stays `INFRA` |
| `odd_output` | yes | `PASS` | unicode/spaces/ANSI/secret-looking output, exit 0 |

The `skipped` category is canonical `PASS` for all seven adapters
(deliberate Option A): the fixtures carry skipped cases *next to* passing
cases, so no adapter lands on a skip-only result. See the BEP note below.

## Adapters, versions, formats, report paths

| adapter | tool versions | report format | materialized report path used by the contract test |
|---|---|---|---|
| `gradle` | `7.6`, `8.7` | JUnit XML, single `<testsuite>` root | `build/test-results/test/TEST-com.example.AppTest.xml` |
| `maven` | `surefire-2.22`, `surefire-3.2` | JUnit XML, single `<testsuite>` root | `target/surefire-reports/TEST-com.example.AccountServiceTest.xml` |
| `nx` | `15`, `19` | `15`: jest JSON; `19`: JUnit XML, `<testsuites>` root | `15`: `.ivp/reports/nx-7a3f91c2.json`; `19`: `reports/web-junit.xml` |
| `bazel` | `5`, `7` | Bazel BEP JSON Lines (`.jsonl`) | `.ivp/reports/bazel-9d4e5f6a.jsonl` |
| `pytest` | `7`, `8` | JUnit XML, `<testsuites>` root | `.ivp/reports/pytest-3e8c10bf.xml` |
| `jest` | `27`, `29` | jest JSON | `.ivp/reports/jest-5f2a9d77.json` |
| `generic` | `config-v1`, `config-v2` | `config-v1`: JUnit XML, `<testsuites>` root; `config-v2`: jest JSON | `config-v1`: `reports/generic-report.xml`; `config-v2`: `reports/generic-report.json` |

Report paths follow each adapter's discovery/Materialize conventions
(gradle/maven under the build output tree, pytest/jest/bazel under
`.ivp/reports/<tool>-<hash>.<ext>`, nx and generic are config-driven);
the hash segments are fixture-local and only need to be stable.

## Which pairs evolved

- **bazel** genuinely evolved: version `5` summaries carry `totalRunCount`
  only, version `7` adds `totalNumTests`. `parseBEP` reads
  `totalNumTests` first and falls back to `totalRunCount`.
- **nx** and **generic** carry two config-driven output shapes rather than
  a tool-version change: nx 15 emits jest JSON while nx 19 emits JUnit
  XML, and generic `config-v1`/`config-v2` switch between JUnit XML and
  jest JSON.
- **gradle**, **maven**, **pytest**, and **jest** have the same format
  across their pair; only timestamps, durations, and cosmetic details
  differ between the two versions.

## odd_output composition

- **JSON fixtures** (jest, nx 15, generic v2, bazel) encode the ANSI
  escape as the six-character JSON escape for U+001B; both Python's
  encoder and Go's decoder round-trip it, so parsed test names carry
  real ESC bytes afterwards.
- **XML fixtures** cannot carry U+001B at all: Go's `encoding/xml`
  rejects a raw ESC in text, attributes, character references, and
  CDATA alike. The ANSI sequence and the secret-looking strings
  (`password=hunter2`, `AKIAFAKEEXAMPLEKEY`) therefore live in the
  **top-level XML comment** (which the decoder tolerates and discards)
  and in test names / `<system-out>` payloads, which are ordinary report
  content.
- The contract test asserts that no secret-looking string and no ESC
  byte ever surfaces in `ParsedResults.Reasons` (parser explanation
  strings), regardless of what the report contained.

## skipped ⇒ PASS note (BEP skip gap, strategy 3.1-5)

JUnit and jest fixtures express skips as real skipped/pending cases next
to passing cases. Bazel's BEP parser now classifies a skipped test
(`skipped: true` or a `SKIPPED` status) as a skipped case, and the
skipped/timeout/flaky categories are covered by
`internal/adapters/bazel/bazel_bep_test.go`; the shared contract test
still keeps every adapter's `skipped` fixture green-on-executed-subsets,
which is what makes the cross-adapter equivalence assertion (one canonical
outcome per category for all seven adapters) hold literally. The skip-only
class of the classifier vocabulary is exercised separately by
`TestCanonicalOutcomeClassifierCoversFullVocabulary` in the contract
test.

## Consumer and commands

The only consumer is `internal/adapters/equivalent_outcome_test.go`
(`package adapters_test`), which materializes each fixture into a fresh
temporary repository root at the report path above and classifies the
adapter's `ParseResults` through a single shared canonical outcome
function (`PASS` / `FAIL` / `ZERO` / `SKIP_ONLY` / `INFRA`).

From the repository root:

```powershell
$env:GOTOOLCHAIN='local'
& ".\.tooling\go\bin\go.exe" test ./internal/adapters/... -count=1
```

## History

An earlier untracked draft corpus (created 2026-11-09, nine categories,
different version labels, no tests and no README) was consolidated into
this layout on 2026-11-10; the stale draft directories were moved out of
the repository to a temporary backup rather than deleted.
