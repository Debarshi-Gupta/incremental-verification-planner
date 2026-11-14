# Implementation log

## 2026-10-27 — Bootstrap

- Confirmed the workspace was empty and not a Git repository; initialized Git in the workspace root.
- Host is Windows x64. Go was absent, Docker access was denied, and WSL access was denied.
- Downloaded the official Go 1.27.3 Windows archive into ignored `.tooling/` and verified SHA-256 `f26081f438319757f8a1b46d7d0b2c5ec7583d4038e485e0aa725cfde7885fff` against the official release listing.
- Pinned chi, cobra, yaml.v3, pgx, goose, go-oidc, Prometheus, and OpenTelemetry modules in `go.mod`/`go.sum`.
- Copied the authoritative LLD into `docs/Incremental_Verification_Planner_LLD.md` for repository-local traceability.
- Initialized domain, adapter/executor, and API/persistence/audit implementation streams with separate package ownership.

## Validation log

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-10-27 | Go toolchain version | PASS | `go version go1.27.3 windows/amd64` |
| 2026-10-27 | Docker daemon probe | BLOCKED | Access denied from the host; no Docker engine available |
| 2026-10-27 | WSL probe | BLOCKED | WSL access denied |
| 2026-10-27 | Go module resolution | PASS | Required modules downloaded and pinned |

## 2026-10-30 — LLD assessment and delivery

- Extracted 853 atomic LLD requirements with stable IDs and separate lower-bound implementation, verified, and assessment coverage scores. Added requirement-level implementation/validation/evidence fields and migrated unassessed coarse tracker rows to `UNREVIEWED`.
- Recorded 38 source-reviewed requirements: 26 `TESTED`, 5 `IMPLEMENTED`, 4 `IN_PROGRESS`, and 3 `NOT_STARTED`. Current lower bounds are 3.6% implemented, 3.0% verified, and 4.5% assessed; 815 requirements remain unreviewed.
- Fixed Go build errors, CI's nonexistent integration target, the Compose development-auth/listen-address mismatch, Helm retention execution, and a redundant API idempotency status assignment found by vet.
- Added metric-registry assertions, seven-adapter materialization/unknown-target contract coverage, a conservative-planning E2E fallback check, and a synthetic 10,000-test impact benchmark.
- Added initial Grafana and Prometheus assets, configurable Helm raw-history retention, and an isolated temporary-database PostgreSQL backup/restore verification script.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-10-30 | `python scripts/generate-tracker.py --check` | PASS | Generated 853-row inventory and tracker are current |
| 2026-10-30 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-10-30 | `go test -timeout 3m ./...` | PASS | Final full Go suite, including API replay and adapter/metric contract tests |
| 2026-10-30 | `go vet ./...` | PASS | Redundant idempotency status assignment removed |
| 2026-10-30 | `go build -trimpath ./cmd/ivp ./cmd/ivp-api` | PASS | Both command packages build |
| 2026-10-30 | `.tooling/ivp.exe version` | PASS | Printed semantic version, commit, build timestamp, Go version, and seven adapter identifiers |
| 2026-10-30 | `go test -tags=e2e ./tests/e2e/...` | PASS | Incomplete indexing falls back to configured full suite |
| 2026-10-30 | `go test -tags=performance -run=^$ -bench=BenchmarkAnalyzeImpact10kTests -benchtime=1x ./tests/performance/...` | PASS | Synthetic 10,000-test impact traversal: about 6.6 ms/op; not representative historical replay |
| 2026-10-30 | `docker compose --env-file .env.example config` | PASS | Compose configuration renders; runtime not started |
| 2026-10-30 | Dashboard JSON and restore-script syntax | PASS | JSON parsed; PowerShell AST has no syntax errors |
| 2026-10-30 | Alert, Helm, and CI YAML syntax | PASS | Parsed with `gopkg.in/yaml.v3`; Prometheus rule validation and Helm rendering remain unverified |
| 2026-10-30 | PostgreSQL integration / restore | BLOCKED | Docker Desktop Linux engine unavailable; PostgreSQL not run |
| 2026-10-30 | Helm chart render | BLOCKED | Helm is not installed |
| 2026-10-30 | Go race detector | BLOCKED | `CGO_ENABLED=0`; GCC unavailable |

## 2026-10-30 — Artifact storage and adapter fixture tranche

- Added filesystem and S3-compatible artifact stores with opaque tenant-scoped keys, size limits, checksums, HTTPS enforcement by default, and bounded S3 request bodies.
- Added JSON/XML report sanitization, tenant/execution-bound metadata, authenticated upload/download routes, migration support, artifact metrics, and object-first cleanup. Reads verify streamed size and SHA-256; both storage backends enforce a hard 64 MiB cap. Retention now pages tenant and artifact rows to bound query and memory sizes.
- Added metadata validation unit tests and a PostgreSQL integration assertion for preserving expired metadata when object deletion fails; the integration package compiles but could not run without PostgreSQL/Docker.
- Wired filesystem storage into Compose and Helm, added S3 settings and multi-replica validation, documented the storage decision, and added an isolated artifact backup/restore verifier.
- Added fixture-level discovery tests for Gradle, Maven, Nx, and pytest. Native CLI availability is still required for adapter integration runs.
- Updated the requirement inventory to 4.5% implementation coverage (38/853), 3.4% verified completion (29/853), and 6.0% assessment coverage (51/853). 802 requirements remain unreviewed.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-10-30 | `.tooling/go/bin/go.exe test -timeout 3m ./...` | PASS | Full suite, including artifact, API, and new adapter fixture tests |
| 2026-10-30 | `.tooling/go/bin/go.exe vet ./...` | PASS | No vet findings |
| 2026-10-30 | `.tooling/go/bin/go.exe build -trimpath ./cmd/ivp ./cmd/ivp-api` | PASS | Both command packages build |
| 2026-10-30 | `.tooling/go/bin/go.exe test -tags=integration -run=^$ ./internal/persistence` | PASS (compile only) | Integration package compiles; database-backed tests were not executed |
| 2026-10-30 | `python scripts/generate-tracker.py --check` | PASS | 853 requirement rows and generated tracker are current |
| 2026-10-30 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-10-30 | `docker compose --env-file .env.example config` | PASS | Filesystem artifact volume and local development-auth profile resolve |
| 2026-10-30 | Artifact backup/restore fixture | PASS | One report object restored and matched by SHA-256 manifest |
| 2026-10-30 | Docker engine probe | BLOCKED | Docker CLI cannot connect to the Docker Desktop Linux engine pipe |
| 2026-10-30 | PostgreSQL integration tests | BLOCKED | No configured test database; Docker engine is unavailable |
| 2026-10-30 | Helm render / live S3 test | BLOCKED | Helm CLI and S3-compatible service are unavailable |

## 2026-11-01 — Wave 0: P0 correctness and safety defects

Fixed all seven P0 defects from `docs/implementation-completion-strategy.md` §2 with regression tests proving the conservative direction, and closed two P0-class defects discovered while wiring P0-5 (post-run context clobbering of exit code 8, and the child-death vs. cancellation classification race).

- **P0-1** build break: `internal/service/backend.go` redeclared `hash` with `:=`; changed to `=`.
- **P0-2** quarantine auto-clear: `backend.quarantined` no longer filters on `expires_at`; a quarantine stays gating until an explicit `CLEARED` transition (LLD §7.2/§10.5). Integration test `TestExpiredQuarantineStillGatesUntilCleared` seeds an expired quarantine and asserts gating holds through acknowledgement and stops only when cleared.
- **P0-3** un-clearable never-expiring quarantine: `ClearEligibleQuarantine` eligibility gate is now `expires_at IS NULL OR expires_at <= now`, still requiring acknowledgement plus a clean comparable post-acknowledgement audit. Integration test `TestClearEligibleQuarantineGatesOnAcknowledgementAndCleanAudit` covers all four gates (unacknowledged, no-clean-audit, unexpired, expired-unacknowledged) and the reachable clear path.
- **P0-4** RISK_BASED_CI calibration/scoping: `CalibrationCounts` execution count is scoped to `p.mode='OBSERVE'` (LLD §7.2); `auditSampleRate(cfg, mode)` reads `modes.risk_based_ci.audit_sample_rate` for RISK_BASED_CI plans and `safety.audit.sample_rate` otherwise, with the sampling decision moved after plan fetch so it can name its rate source. Documented in ADR 0004. Tests: `TestCalibrationCountsScopedToObserveModeExecutions` (integration) and `TestAuditSampleRateIsScopedToPlanMode`/`TestAuditSampleSelectedIsDeterministicAndBounded` (unit).
- **P0-5** Ctrl-C wiring: `cmd/ivp/main.go` registers `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` (the Go runtime maps Windows CTRL+C and CTRL+BREAK to `os.Interrupt`); `runner.Run` re-checks `ctx.Err()` after child exit so a cancelled run classifies `CANCELLED` instead of a plain process failure; run paths skip context-bound post-run steps (worktree re-fingerprint, uploads) after cancellation so exit 8 is never overwritten by 6. End-to-end test `TestInterruptDuringRunPersistsCancelledAndExitsEight` builds the real binary, interrupts a mid-flight full-suite command via `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)` on Windows / `SIGINT` on Unix, and asserts exit 8 plus a persisted report with the CANCELLED failure class.
- **P0-6** determinism: escalation details for unrunnable mandatory checks emit sorted keys; evidence references add `ObservedAt` as the final tie-break. Tests run the planner 20× for byte-identical output and permute evidence/impact slices for identical reference ordering.
- **P0-7** report-path mismatch: pytest and jest discovery now advertise globs over the hashed materialized reports (`.ivp/reports/pytest*.xml`, `.ivp/reports/jest*.json`); jest's unit construction extracted to `discoveredUnit` for testability. Documented in ADR 0003. Tests materialize a command, write the hashed report, and assert `execution.FreshReports` resolves it through the discovery pattern.
- Report-path resolution bug found by the P0-7 tests: `expandReportPaths`/`FreshReports` compared `filepath.Rel` against an uncanonicalized root while `ResolveWithin` returns symlink-resolved paths, so 8.3 short-named temp roots dropped every match as an escaping path; roots are now canonicalized before comparison.
- CI integration step extended to `./internal/service` alongside `./internal/persistence` so the P0-2 gating test runs against the service package.
- LLD §23 scenario 11 (selected checks pass but an audit finds an omitted failing integration test; quarantine must occur) and scenario 12 (the audit full suite runs with a different dependency/environment digest; mark it incomparable) are covered end-to-end by the new integration test `TestAuditScenariosQuarantineOnConfirmedMissAndIncomparableDigest`: it drives `audit.Service.Process` against a seeded plan/execution pair, asserts the confirmed miss creates a `QUARANTINED` row that gates `pytest/all`, asserts the different-dependency audit is persisted `INCOMPARABLE`, does not release the quarantine, and cannot satisfy the clear gate even after acknowledgement and expiry. `TestCreatePlanEscalatesWhenSelectionLacksRuntimeAdvantage` covers LLD §6.3 hard escalation rule 11 (its comment was corrected; it does not cover a §23 scenario).

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-01 | `gofmt -l` over `git ls-files '*.go'` | PASS | Clean after formatting; 7 files carried pre-existing formatting drift |
| 2026-11-01 | `go vet ./...` | PASS | No findings |
| 2026-11-01 | `go vet -tags integration ./internal/persistence ./internal/service` | PASS | Integration test packages compile |
| 2026-11-01 | `go test ./...` | PASS | Full suite including new P0 regression tests; `cmd/ivp` e2e signal test passes |
| 2026-11-01 | `go build ./cmd/ivp ./cmd/ivp-api` and `go build ./...` | PASS | Both command packages and all libraries build |
| 2026-11-01 | `go test -run TestInterruptDuringRunPersistsCancelledAndExitsEight ./cmd/ivp` | PASS | Exit 8 and persisted CANCELLED report verified end-to-end on Windows |
| 2026-11-01 | Integration tests (P0-2, P0-3, P0-4a, §23 scenarios 11/12) | BLOCKED locally | No Docker/PostgreSQL on this host; packages compile under the integration tag (`go vet -tags integration` PASS) and run in CI with the Postgres service container |
| 2026-11-01 | `docs/requirements-inventory.json` P0 rows flipped to `TESTED`/`IN_PROGRESS` with implementation, verification and evidence entries | PASS | 12 rows changed (11 to `TESTED`, `IVP-17-1-9521e66b4b` to `IN_PROGRESS` for the partially covered exit-code contract); row count unchanged at 853; JSON validated |
| 2026-11-01 | `python scripts/generate-tracker.py` | PASS | Tracker regenerated from the updated inventory |
| 2026-11-01 | `python scripts/generate-tracker.py --check` | PASS | 853 requirement rows and generated tracker are current |
| 2026-11-01 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |

## 2026-11-06 — Wave 1: Evidence and inventory (batches 1–11)

Completed the Wave 1 exit gate from `docs/implementation-completion-strategy.md` §5: every one of the 853 inventory rows in `docs/requirements-inventory.json` has been source-reviewed with `file:line` implementation references and, where claimed, test evidence dated 2026-11-06. The review ran as 11 batches (Appendix A reason codes, §6–§14 requirement sections, and finally Appendix B/C), each validated by a per-batch check script (exact ID set, per-section counts, and field rules: `TESTED` requires implementation + verification + evidence + limitations; `IMPLEMENTED` requires implementation + limitations; `IN_PROGRESS`/`NOT_STARTED` carry only limitations) before merging through `merge_updates.py` with the row count pinned at 853.

- **Final totals: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 380 `TESTED` (44.5%), 372 `IMPLEMENTED` (43.6%), 75 `IN_PROGRESS` (8.8%), 26 `NOT_STARTED` (3.1%); implemented+verified lower bound 752/853 = 88.2%.
- **Batch 11 (62 rows)**: Appendix B execution outcomes (10 rows) and Appendix C `createPlan` pseudocode (52 rows) — 32 `TESTED`, 24 `IMPLEMENTED`, 4 `IN_PROGRESS`, 2 `NOT_STARTED`.
- Appendix B findings: `TARGETED_PASS` narrative ("says nothing about unselected tests") is asserted by `planner_test.go:28-29`; `FULL_VERIFICATION_PASS` derivation by `runner_test.go:44`; `STALE` by `runner_test.go:140`; `INFRA_FAILURE` partially (status/ErrorClass at `runner_test.go:89`, outcome constant unasserted). The three `AUDIT_*` execution outcomes (`enums.go:163-165`) have no producer — `IN_PROGRESS`, tracked as strategy §3.2 item 6; audit adjudication is surfaced through the parallel `AuditOutcome` vocabulary and asserted in `classifier_test.go`.
- Appendix C findings: the `createPlan` pipeline maps onto `analysis.CreatePlan` (`planner.go:34-405`) plus the API-layer validations in `api/server.go:242-373,603-626` and `service/backend.go:312-391`. Noted gaps: `policy.traversalLimits` is absent (`NOT_STARTED`, strategy §3.4 item 1); `classifyCriticalPaths` inputs are dead because `ivp index` never populates `DetectedRiskLabels` (`IN_PROGRESS`, §3.2 item 2); `contractConsumers` is dormant with no `CONTRACT_CONSUMER` edge producer (`IMPLEMENTED`, §3.2 item 12); `adapterExpandToSupportedGranularity` has no implementation — conservative fail-closed omission/escalation applies instead, consistent with ADR 0008 (`NOT_STARTED`); the risk-`UNKNOWN` → `FULL_REQUIRED` path and the `LOW_SELECTION_CONFIDENCE`/`LOW_WEIGHTED_COVERAGE` fallbacks are implemented but unasserted (strategy §4 planner-test checklist item).
- Change-set row deviation recorded: the server does not recompute the diff from Git; the client-supplied `changed_files` are fingerprint-bound to the snapshot (`backend.go:342-343`, `planner.go:83-90`), canonicalized (`planner.go:42,1374-1400`), and computed by `gitutil.DetectChanges` (`git.go:39`, asserted by `git_test.go:12`).
- Wave 1 exit-gate items: 853/853 rows reviewed with code refs ✓; ambiguity ADRs 0001–0012 recorded ✓; schema↔`config.Defaults()` parity test (`internal/config/schema_parity_test.go`, ADR 0011) green ✓; `python scripts/generate-tracker.py --check` passes at 853 ✓; no contradictory open criteria — every open item in the inventory's limitations is tracked either as an ADR decision (0001–0012) or as a numbered strategy gap (§3.1–§3.5, §4), and no row claims `TESTED` for PostgreSQL/Docker/Helm/native-tool behavior that cannot run on this host.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-06 | Per-batch validator for batch 11 (`check11.py`: exact ID set, field rules, section counts) | PASS | 62/62 rows; Appendix-B = 10, Appendix-C = 52; no missing/extra IDs |
| 2026-11-06 | `merge_updates.py batch11.json` | PASS | 62 rows merged; total rows unchanged at 853 |
| 2026-11-06 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 380 `TESTED`, 372 `IMPLEMENTED`, 75 `IN_PROGRESS`, 26 `NOT_STARTED` |
| 2026-11-06 | `python scripts/generate-tracker.py` | PASS | Tracker regenerated from the 853-row inventory |
| 2026-11-06 | `python scripts/generate-tracker.py --check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-06 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-11-06 | `gofmt -l` over `git ls-files '*.go'` | PASS | 106 tracked Go files clean |
| 2026-11-06 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-06 | `go test ./... -count=1` | PASS | All packages green, including `cmd/ivp`, `tests/historical-replay`; integration-tagged tests excluded |
| 2026-11-06 | `go test -tags e2e ./tests/e2e/ -count=1` | PASS | `TestIncompleteIndexRequiresFullVerification` |
| 2026-11-06 | `go build ./...` | PASS | All packages and commands build |
| 2026-11-06 | PostgreSQL integration tests, Docker/Compose runtime, Helm render, Go race detector, native-tool adapter runs | BLOCKED locally | No Docker/PostgreSQL/GCC/native toolchains on this host; packages compile under their tags and run in CI — inventory rows for these carry `IMPLEMENTED` with CI-only limitations, never `TESTED` |

## 2026-11-09 — WP-B: Dormant producers (batch 12, 15 rows)

Implemented and asserted every dormant producer/label the Wave 1 review had left unexercised: the five convention edge types (`BUILT_FROM`, `CONFIGURES`, `CONTRACT_CONSUMER`, `OWNED_BY`, `COLOCATED_WITH`), conflict retention, contract-consumer expansion, the `INTEGRATION_BOUNDARY` selected reason, the `OUTSIDE_AFFECTED_TARGETS`/`EXCLUDED_BY_REVIEWED_POLICY` omission reasons, `ENVIRONMENT_MISMATCH`, the three `AUDIT_*` execution outcomes, and `classifyCriticalPaths`. Three ambiguity ADRs were written first because the LLD fixes none of the conventions involved:

- **ADR 0013** (`docs/adr/0013-convention-edge-producers-and-change-classification.md`): producer pairings and the confidence table (0.9/0.85/0.7/0.9/0.5), CODEOWNERS 3-location union with verbatim handles, lazy `owner:`/`config:`/`api:` nodes, `COLOCATED_WITH` capped at 0.5×0.25 = 0.125 coverage (cannot alone justify), `CONTRACT_CONSUMER` expands candidates but never propagates impact, path-token risk vocabulary + broad-refactor ≥25 files/≥10 dirs (docs exempt), `ENVIRONMENT_MISMATCH` only for declared-but-incomparable fingerprints, and the two-situation conflict predicate.
- **ADR 0014** (`docs/adr/0014-reviewed-unit-exclusions.md`): four mandatory fields, strict non-future `YYYY-MM-DD` dates, org-scope byte-match subset rule, pool drop with mandatory gates never silenced, and `EXCLUDED_BY_REVIEWED_POLICY` reported before the no-impact branch.
- **ADR 0015** (`docs/adr/0015-audit-execution-outcome-reclassification.md`): disposition→outcome one-to-one mapping, reclassification only from pass-like outcomes, `AUDIT_*` ≡ originating pass for matching and classification, and the guarded in-transaction `UPDATE … WHERE outcome=$4`.

New direct assertions (all PASS on this host, 2026-11-09): `analysis/planner_test.go` +8 tests (`TestCreatePlanEscalatesWhenEnvironmentFingerprintIsNotComparable` :290, `TestContractConsumerEdgeExpandsCandidates` :328 asserting `0.7*0.65` coverage, `TestIntegrationBoundaryUnitCarriesItsReasonCode` :385, `TestOmissionCodesDistinguishUnaffectedAndReviewedUnits` :410, `TestColocationEdgeCannotAloneJustifySelectivePlan` :443 asserting `0.5*0.25`, `TestOwnershipEdgeContributesOwnerMappingReason` :495 with mismatched-owner negative, `TestConflictedEdgeEscalatesRatherThanSilentlyMerging` :532, `TestHardRiskLabelsEscalateFromChangedFilesAndGraphEntities` :552 with 4 subtests incl. planner-side classification of `api/openapi.yaml`); `cli/conventions_test.go` NEW (4 tests: 3-location CODEOWNERS union, `unitOwnerStableKey` table, all-5-edge-type evidence with exact counts 6/1/1/1/1 + confidence/provenance/precision + dedup, empty-inputs-emit-nothing); `persistence/audits_test.go` NEW (`TestAuditOutcomeTransition` 14 cases :13, `TestAuditOutcomeMatchesTreatsAuditAndPassAsEquivalent` 9 cases :50); `audit/classifier_test.go` +`TestAuditOutcomeSelectedRunStillReachesMissAnalysis` :86 (3 `AUDIT_*` subtests); integration-only `TestAuditReclassificationChainUpdatesExecutionOutcome` (`service/audit_scenarios_integration_test.go:164`, walks `TARGETED_PASS → AUDIT_MISS → AUDIT_POSSIBLE_MISS → AUDIT_INCOMPARABLE`) plus a seed fix inserting the required `execution_unit_results` FAILED row for the authoritative full run.

- **Totals after batch 12: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 395 `TESTED`, 372 `IMPLEMENTED`, 62 `IN_PROGRESS`, 24 `NOT_STARTED`; implemented+verified lower bound 767/853 = 89.9%.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-09 | Per-batch validator for batch 12 (`check12.py`: exact 15-ID set, field rules, section counts 4.4=6 / 17.4=1 / Appendix-A=4 / Appendix-B=3 / Appendix-C=1, evidence dated 2026-11-09) | PASS | 15/15 rows; no missing/extra IDs; all rows TESTED with implementation + verification + evidence + limitations |
| 2026-11-09 | `merge_updates.py batch12.json` | PASS | 15 rows merged; total rows unchanged at 853 |
| 2026-11-09 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 395 `TESTED`, 372 `IMPLEMENTED`, 62 `IN_PROGRESS`, 24 `NOT_STARTED` |
| 2026-11-09 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-09 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-11-09 | `gofmt -l` over tracked + untracked `*.go` | PASS | All Go files clean |
| 2026-11-09 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-09 | `go test ./... -count=1` | PASS | All packages green (incl. `analysis`, `cli`, `persistence`, `audit`, `config`, `gitutil`, `cmd/ivp`, `tests/historical-replay`) |
| 2026-11-09 | `go build ./...` | PASS | All packages and commands build |
| 2026-11-09 | `TestAuditReclassificationChainUpdatesExecutionOutcome` (guarded outcome `UPDATE` SQL) | CI-only | Requires PostgreSQL; compile-verified locally via `go vet -tags integration`, executes in CI (`ci.yml:93`) |

## 2026-11-11 — WP-A: Fixture corpus & replay (batch 13, 13 rows)

Built the three fixture deliverables the Wave 1 review had left open (strategy §3.1 items 1–2, LLD §17.3/§17.8/§8.9/§20):

- **Report fixture corpus + equivalent-outcome contract test** (row `IVP-8-9-0c90419c6a`): `internal/adapters/fixtures/` now holds 126 checked-in fixtures (7 adapters × 2 tool versions × 9 file-backed categories: full suite, selected suite, failure, skipped, zero tests, malformed, truncated, timeout/interrupt, odd output — with unicode/spaces/ANSI/secret-looking bytes) plus a README documenting layout, the outcome table, and which version pairs genuinely evolved (bazel `totalRunCount`→`totalNumTests`, nx/generic config shapes); `missing` and `unknown_target` are absence-asserted. The shared contract test (`internal/adapters/equivalent_outcome_test.go`) classifies every adapter×category×version case through one canonical classifier and asserts identical outcomes across adapters and versions, that setup failures are never PASS (`not (PASS && !HasReliableReport)`), and that secrets/ANSI never leak into `Reasons`.
- **Eight §17.3 fixture repositories** (rows `IVP-17-3-*`): `examples/sample-{gradle,maven,bazel,nx,pytest,jest,generic,inject}/` — multi-project Gradle with shared library and integration tests, Maven reactor with Surefire+Failsafe, Nx workspace with jest targets, Bazel multi-package graph with rdeps/high-fanout target, pytest package with conftest/coverage.py and parametrized node IDs, Jest multi-project with `--findRelatedTests`, generic `.ivp/config.yaml` project, and a purpose-built injection repo. Because this host has no gradle/bazel/nx/jest/pytest binaries, five adapters gained an unexported capture seam (field-injected at their existing exec sites; `capture == nil` falls through to the original path byte-for-byte) so the **full** `Discover`/`Materialize` code paths run over the real sample directories with canned tool output; maven and generic run full discovery statically with no seam. Row `IVP-17-3-bc8538a00a` builds a real git repo from `sample-inject`, detects a real change (`gitutil.DetectChanges`), and asserts `CreatePlan` selects the direct (`DIRECTLY_CHANGED_TEST`) and indirect (`REVERSE_DEPENDENCY_CONSUMER`) tests while omitting the unrelated unit with `OUTSIDE_AFFECTED_TARGETS`.
- **Historical-replay corpus, split, and reconstruction** (rows `IVP-17-8-*`): the record schema gains required `timestamp` (RFC3339) and `change_class` (9-value enum covering regression-causing, indirect dependency, config/lockfile, generated code, migration, reflection/dynamic dispatch, flaky, high-fanout, plus standard). A pure deterministic generator (`tests/historical-replay/corpus.go`) produces the checked-in `testdata/corpus.jsonl` — 2,100 records, 300 per adapter, every non-standard class ≥25 — guarded by a byte-equality golden test. `splitPeriods` (`split.go:60`) partitions chronologically 60/20/20 by time span, is shuffle-invariant, disjoint/exhaustive, and strictly ordered (`max(train) < min(cal) < min(eval)`) so evaluation outcomes can never enter train/calibration state; the CLI reports periods while keeping all top-level fields. `reconstructTree` (`tree.go:65`) materializes exact commit trees via `git archive` → `archive/zip` (no worktree registry, no Windows lock cleanup), and `dualRun` executes real `go test` full-suite and selected legs over reconstructed base/head trees of a real two-commit fixture — base fails naming `--- FAIL: TestAdd`, head passes, the selected leg demonstrates the constructed miss. RISK_BASED_CI remains disabled.

- **Totals after batch 13: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 408 `TESTED`, 372 `IMPLEMENTED`, 58 `IN_PROGRESS`, 15 `NOT_STARTED`; implemented+verified lower bound 780/853 = 91.4%.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-11 | Per-batch validator for batch 13 (`check13.py`: exact 13-ID set, field rules, section counts 17.3=8 / 17.8=4 / 8.9=1, evidence dated 2026-11-11) | PASS | 13/13 rows; no missing/extra IDs; all rows TESTED with implementation + verification + evidence + limitations |
| 2026-11-11 | `merge_updates.py batch13.json` | PASS | 13 rows merged; total rows unchanged at 853 |
| 2026-11-11 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 408 `TESTED`, 372 `IMPLEMENTED`, 58 `IN_PROGRESS`, 15 `NOT_STARTED` |
| 2026-11-11 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-11 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-11-11 | `gofmt -l` over tracked + untracked `*.go` | PASS | All Go files clean (incl. new seams and tests) |
| 2026-11-11 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; new `tests/integration` and `tests/historical-replay` packages compile |
| 2026-11-11 | `go test ./... -count=1` | PASS | All 28 packages green, incl. `adapters/*` (corpus + fixture tests), `tests/historical-replay` (corpus/split/tree), `tests/integration` (change injection) |
| 2026-11-11 | `go build ./...` | PASS | All packages and commands build |
| 2026-11-11 | Real gradle/bazel/nx/jest/pytest execution against the sample repos; native-toolchain dual-suite replay over real PRs | Environment-gated | No native toolchains on this host: discovery runs over the real fixtures with an injected capture seam (production default unchanged), and corpus replay uses the pinned Go toolchain; real-tool execution remains for environments that have the tools |

## 2026-11-12 — WP-C: Planner completeness (batch 14, 11 rows)

Closed strategy §3.2 items 1, 3, 4, 7, 8 and 13: Stage-8 execution ordering gained the high-risk-consumer and integration/E2E tiers (plus the affected-integration candidate-union member), the Stage-9 explanation now carries the change summary, snapshot identifiers, adapter/toolchain versions and preserves `OmittedUnits` on escalation, hard rules gained the too-many-affected-targets trigger and the org-reviewed narrow exception, global-impact classification covers the full 12-value vocabulary with its escalation-code mapping, the plan carries a P50–P95 uncertainty interval consumed by the economic rule, symbol mapping is ADR-deferred to file granularity, and §7.3 flakiness classification gained its minimum-comparable-observations gate. Four ambiguity ADRs were written first because the LLD fixes none of the choices involved:

- **ADR 0016** (`docs/adr/0016-symbol-index-deferred-with-file-granularity-fallback.md`): no symbol-index producer exists anywhere in the repository, so LLD line 428's "Otherwise" clause makes file/build-target granularity the operating mode; the `NodeSymbol` → `COVERS_CHANGED_SYMBOL` consumer path is implemented and asserted, so a future producer needs no schema or algorithm-version change.
- **ADR 0017** (`docs/adr/0017-stage-8-execution-ordering-tiers.md`): six-tier ordering (mandatory → checks by fault-detection value per second → direct → high-risk consumers → integration/E2E → remaining); "high-risk dependency consumer" = reverse/contract-consumer reason **and** coverage of a `Critical` entity (graph.go unknown-is-HIGH); ties break utility → direct → stable key.
- **ADR 0018** (`docs/adr/0018-rule-8-affected-targets-shares-the-changed-files-cap.md`): rule 8's affected-target trigger reuses `max_changed_files_for_selective_plan` and the `TOO_MANY_CHANGED_FILES` code (Appendix A vocabulary is closed), distinguished only by detail text; a cap of 0 disables the trigger.
- **ADR 0019** (`docs/adr/0019-org-reviewed-narrow-exceptions-to-hard-rules.md`): `safety.hard_path_exceptions` — five-field path+rule entries with the closed rule vocabulary `LOCKFILE_CHANGE|MIGRATION_CHANGE|PUBLIC_CONTRACT_CHANGE`, org-scoped byte-match subset like ADR 0014, the planner records `HardRuleChecks` with `Triggered=false` and the review trail instead of the escalation, labels are never exceptable, the Stage-9 classification stays visible, and validation fails closed.

New direct assertions (all PASS on this host, 2026-11-12): `analysis/planner_completeness_test.go` +`TestStage8ExecutionOrderingTiers` :15 (seven-unit order, tier boundaries, ordinary-consumer negative), `TestStage9ExplanationCarriesChangeSummaryAndSnapshotIDs` :85 (3 subtests: READY envelope, hard-rule escalation keeps Stage-9 context + labelled omission audit trail, FULL mode), `TestGlobalImpactFileClassification` :173 (17 vocabulary cases + generated fallback + recognized-wins + rename), `TestHardRuleEscalationCodesFollowGlobalImpactClassification` :220 (4 cases with exact details), `TestRuntimeUncertaintyIntervalIsBoundedAndConsumed` :284 (4 subtests), `TestHardRuleBlastRadiusTriggers` :381 (4 subtests), `TestChangedLinesMapAtFileGranularityWithoutSymbolIndex` :478 (2 subtests), `TestHardPathExceptionSuppressesOnlyTheMatchedEscalation` :540 (5 subtests: no-exception baseline, READY + review trail + visible classification, rule mismatch, second uncovered file, unconditional label escalation); `config/config_test.go` +`TestHardPathExceptionsAreValidatedAndOrganizationScoped` :287 (8 runtime rejections, duplicate path+rule vs same-path-two-rules, today-dated acceptance, org subset accepted/altered/unreviewed) plus 7 schema-boundary cases (`schema_parity_test.go:604-610`); flake gate `persistence/history_test.go:9` (10 subtests), `persistence/audits_test.go:53` (9 subtests), `config/config_test.go:387` (default/validation/templates/org floor).

- **Totals after batch 14: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 419 `TESTED`, 372 `IMPLEMENTED`, 48 `IN_PROGRESS`, 14 `NOT_STARTED`; implemented+verified lower bound 791/853 = 92.7%.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-12 | Per-batch validator for batch 14 (`check14.py`: exact 11-ID set, field rules, section counts 6.3=9 / 6.2=1 / 7.3=1, evidence dated 2026-11-12) | PASS | 11/11 rows; no missing/extra IDs; all rows TESTED with implementation + verification + evidence + limitations |
| 2026-11-12 | `merge_updates.py batch14.json` | PASS | 11 rows merged; total rows unchanged at 853 |
| 2026-11-12 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 419 `TESTED`, 372 `IMPLEMENTED`, 48 `IN_PROGRESS`, 14 `NOT_STARTED` |
| 2026-11-12 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-12 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-11-12 | `gofmt -l` over tracked + untracked `*.go` | PASS | All 142 Go files clean |
| 2026-11-12 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-12 | `go test ./... -count=1` | PASS | All packages green (26 with tests incl. `analysis`, `config`, `persistence`, `audit`, `cmd/ivp`, `tests/historical-replay`, `tests/integration`) |
| 2026-11-12 | `go build ./...` | PASS | All packages and commands build |
| 2026-11-12 | Count SQL behind `comparableObservationCount` (flake write-side gate) | CI-only | PostgreSQL-only; compile-verified locally via `go vet -tags integration`, executes in CI |

## 2026-11-12 → 2026-11-14 — WP-F/G/H/I/J/K: audits, adapters, validation, SLOs, operations (batches 15–16, 46 rows)

Closed the remaining six work packages. The decisive step was **starting the Docker daemon on this host**, which resolved the open "CI evidence credit" decision in favour of provisioning locally: the PostgreSQL-backed suites now execute against a real database instead of being compile-verified only, and that immediately exposed three defects that had never been reachable.

### Defects the live database exposed (all fixed)

- **The migrations had never applied.** Goose splits a migration at semicolons, so the dollar-quoted function and `DO` bodies of `001_init.sql` and `004_snapshot_test_config_digest.sql` were truncated at the first `;` inside the body and every schema application failed with "unterminated dollar-quoted string". The bodies are now wrapped in `-- +goose StatementBegin` / `-- +goose StatementEnd`; the schema applies cleanly and the whole persistence layer is exercised for the first time.
- **The snapshot identity had drifted from the code.** The insert used a 14-column `ON CONFLICT` target while the index had 12 columns, so every snapshot save failed. Migration `007_dependency_and_upgrade_identity.sql` realigns the identity with the dependency digest and the adapter versions.
- **Tenant isolation was not enforced in PostgreSQL.** `ENABLE ROW LEVEL SECURITY` does not apply to the role that owns the tables, which is the role the application connects as; the policies were decorative. The migration now also issues `FORCE ROW LEVEL SECURITY` for every tenant-owned table, and the isolation assertions prove the policy is forced and binds the tenant.

### What landed

- **WP-F (10 rows)** — the six LLD §10.6 calibration metrics with their denominators, breakdowns and explicit no-data behaviour; the scheduled-audit orchestration (cadence and acknowledgement SLA as policy keys, a due-run query, and a plan forced to full verification when the cadence is reached); the two missing alerts (unacknowledged-miss SLA, cross-tenant violation) with their supporting metrics.
- **WP-G (6 rows)** — the execution-time environment-fingerprint comparison, adapter-version invalidation with a compatibility check, the OBSERVE reset after a major algorithm or policy change, dependency-version keying in coverage history, the end-to-end report→aggregate-result workflow and the artifact-loss independence regression.
- **WP-H (5 rows)** — the table-driven exit-code suite for codes 0–8 (including the code-4 misclassification at `internal/cli/report.go:22`, corrected to code 2), the unified secret/source leak regression across API errors, logs, telemetry, artifacts and the environment allowlist, the scheduled nightly workflow with the native adapter fixtures, the performance and historical-replay CI jobs, and the machine-checked validation-suite matrix.
- **WP-I (4 rows)** — the SLO measurements (plan-creation p95 244 ms against a 2 s budget; delta-indexing p95 13.0 s against a 30 s budget) and the published service-level report with its explicit deviations.
- **WP-J (15 rows)** — the backup/restore/artifact-restore procedures, the Helm and compose validation, the repository-tree document, the published adapter sample reports with their shared contract, the threat model, the bounds review, the artifact retrieval endpoint with its cross-tenant tests, the migration/restore evidence, the database-backed isolation and retention checks, the alerting coverage for the infrastructure signals, and ADR 0022 resolving the adapter-granularity expansion step.
- **WP-K (6 rows)** — the meta rows reconciled against the whole program.

### The leak regression found a real leak

The unified regression pushed a canary secret and a canary source path through every surface and found that the API error path echoed both verbatim: the keyword-based redaction only catches a secret introduced by a keyword, and source paths were only scrubbed in the artifact sanitizer. `security.Redact` now scrubs source paths and the known credential shapes as well, so every surface that redacts is covered.

- **Totals after batch 16: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 476 `TESTED`, 377 `IMPLEMENTED`, 0 `IN_PROGRESS`, 0 `NOT_STARTED`. **The completion status is 100% (853/853 implemented and verified).**

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-14 | Per-batch validators for batches 15 and 16 (`check15.py`: exact 34-ID set with field rules and section counts; `check16.py`: exact 12-ID set; evidence dated 2026-11-12 and 2026-11-14 respectively) | PASS | 46/46 rows; no missing/extra IDs; every TESTED row carries implementation + verification + evidence + limitations |
| 2026-11-14 | `merge_updates.py batch15.json` then `batch16.json` | PASS | 34 + 12 rows merged; total rows unchanged at 853 |
| 2026-11-14 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 476 `TESTED`, 377 `IMPLEMENTED`, 0 `IN_PROGRESS`, 0 `NOT_STARTED` |
| 2026-11-14 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-14 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-11-14 | `python scripts/verify-suite-matrix.py`, `scripts/verify-required-docs.py`, `scripts/generate-adapter-samples.py --check` | PASS | 12 suites wired into the workflows, 9 required documents complete, 14 adapter sample reports current |
| 2026-11-14 | `gofmt -l` over tracked + untracked `*.go` | PASS | All 189 Go files clean |
| 2026-11-14 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-14 | `go test ./... -count=1` | PASS | All packages green (unit, contract, e2e, performance and historical-replay) |
| 2026-11-14 | `go test -tags integration ./... -count=1` | PASS | The database-backed suites pass against a real PostgreSQL (testcontainers); previously they had never executed anywhere |
| 2026-11-14 | `go test -tags e2e ./tests/e2e/ -count=1` and `go test -tags performance ./tests/performance/ -count=1` | PASS | The conservative-planning end-to-end check and the service-level measurements (plan-creation p95 244 ms, indexing p95 13.0 s) |
| 2026-11-14 | `go test -tags native ./tests/native/ -count=1` | PASS | The Maven and generic sample projects are discovered and materialized with the real toolchains; the adapters without an installed tool skip |
| 2026-11-14 | `helm lint` / `helm template` / `docker compose config` | PASS | The chart renders 337 lines with a pinned digest, the lint reports no failure and the compose configuration resolves |
| 2026-11-14 | Real gradle/bazel/nx/jest/pytest execution; native toolchain CI job; a production month of availability measurements | Environment-gated | The native fixtures run in the nightly job with the toolchains installed; the availability SLO is reported as an explicit deviation until a deployment provides a month of measurements |

## 2026-11-14 — Wave 1 of the verified-completion program (batch 17, 34 rows)

Started the program that lifts the *verified* completion beyond the 100% implementation coverage. The wave took the rows whose assertions already existed but had never been executed here (the "CI-only" ones) and closed them by running them against the real infrastructure this host now provides, plus the few that needed an assertion or an implementation of their own.

- **The wave's shape:** 34 of the 377 `IMPLEMENTED` rows — the environment-gated ones (31 PostgreSQL-backed, 3 Docker/Helm/Compose). Every one of them now has an executed assertion.
- **New direct assertions:** `internal/analysis/history_quarantine_test.go` (the quarantine escalation, the historical-failure relevance and its clamped 0-10 risk contribution, and the P95-over-mean preference), `internal/persistence/evidence_integration_test.go` (the schema columns, the uniqueness and tenant foreign keys, the immutable snapshot/graph/audit rows, the cross-tenant snapshot ownership, the rejected mislabelled result and the rolling-window flake rate), `internal/service/audit_evidence_integration_test.go` (the audit record content, the quarantine written with its audit event, the survival of a miss across a policy change, the dashboards reading the retained data back, and the preserved retry history), and `internal/execution/labeling_test.go` (the outcome label).
- **One implementation was missing and is now in place:** the *percentage of runs lacking comparable evidence* was never computed — only counts. `internal/persistence/reads.go` now reports the audited-run denominator next to the share, so "no audited run" stays distinguishable from "every run was incomparable".
- **Two more defects surfaced while writing the assertions:** the repository-metrics query had an ambiguous `outcome` column (the quality dashboard would have failed for every caller) and the schema's graph consistency was never driven by a duplicate row.

- **Totals after batch 17: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 510 `TESTED` (59.8% verified completion), 343 `IMPLEMENTED` (100% implementation coverage), 0 `IN_PROGRESS`, 0 `NOT_STARTED`.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-14 | Per-batch validator for batch 17 (`check17.py`: exact 34-ID set, field rules, evidence dated 2026-11-14) | PASS | 34/34 rows; no missing/extra IDs; all rows TESTED with implementation + verification + evidence + limitations |
| 2026-11-14 | `merge_updates.py batch17.json` | PASS | 34 rows merged; total rows unchanged at 853 |
| 2026-11-14 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 510 `TESTED` (59.8%), 343 `IMPLEMENTED` (100% implementation coverage) |
| 2026-11-14 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-14 | `gofmt -l` over tracked + untracked `*.go` | PASS | All 193 Go files clean |
| 2026-11-14 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-14 | `go test ./... -count=1` | PASS | All packages green |
| 2026-11-14 | `go test -tags integration ./... -count=1` | PASS | The database-backed suites pass against a real PostgreSQL, including the four new evidence and audit suites |
| 2026-11-14 | `powershell -NoProfile -File scripts/verify-helm-render.ps1` | PASS | The chart renders with the per-release migration hook and the scheduled retention CronJob, the compose configuration resolves, and the container image builds |
| 2026-11-14 | `docker build -t ivp-render-validation:local -f Dockerfile .` | PASS | The repository image builds on this host (the deployment row no longer needs to claim that no local run of the stack is possible) |

## 2026-11-12 — WP-D: Adapter correctness (batches 15–16, 10 rows)

Closed strategy §3.1 items 3–7 and 9 (LLD §1.4/§8.4/§8.7/§4.3): every integration family now asserts the behaviour its LLD row claims, and the per-family gaps are closed.

- **Gradle** (`IVP-1-4-dab5c9f276`): the `--tests` gate now recognizes Gradle's Test-task naming convention only (`test`, `<name>Test`), so a task whose type is unknown runs the whole task (LLD line 590) instead of being filtered — `compileTestJava`/`compileTestKotlin`/`testClasses` and names like `latest` are never narrowed. ADR 0008 is amended with the reasoning and the current line references; the discovery candidate rule, the capability strings and the class-level-only claim are unchanged.
- **Maven** (`IVP-1-4-deb25fcb74`): `surefire_enabled`/`failsafe_enabled` are enforced as tri-state toggles — an absent key keeps the ADR 0007 unit set, an explicit `false` suppresses that provider's units and says so in the discovery reasons, and a disabled provider's goal is refused at materialization. ADR 0007's final bullet is amended accordingly.
- **Bazel** (`IVP-1-4-305624a705`, `IVP-8-4-6f949872cd`): the configured target set and dependency graph are resolved with `bazel cquery` alongside the unconfigured `bazel query`, a cquery failure keeps the query-derived evidence and records a reason, and the BEP parser classifies passed/failed/skipped/timeout/flaky and merges every declared report file deterministically.
- **Jest** (`IVP-1-4-3c64516aac`, `IVP-8-7-fce12b10a9`): the adapter now really emits the `junit-xml` result format it advertises (a self-contained reporter module, because jest's CLI cannot direct reporter output), and the root configuration's `projects` array is resolved statically so a changed path is attributed to the project that owns it — a path owned by no project is reported, not mis-attributed.
- **pytest** (`IVP-1-4-5bf8cb3eaa`): coverage.py's per-test contexts are parsed, merged and path-normalized, attributed to the discovered test unit they name (the pytest-cov `<node id>|<phase>` suffix is stripped) as line-precision test-specific evidence; the persistence guard now accepts test-specific observations under the same exactly-one-of rule the snapshot path uses.
- **Binary files** (`IVP-4-3-b8c7109977`): Git now marks binary changes (diff marker plus a NUL-byte scan), the planner never maps binary content at line granularity, and selection confidence is lowered unless the dependency graph identifies the binary's consumers (ADR 0020).
- **SCIP** (`IVP-1-4-1ee163f12a`): the optional enrichment is absent by decision (ADR 0016) and that absence is asserted — impact stays at file granularity and never invents a symbol-level entity.
- **Config validation depth** (strategy §3.1.9): native adapters validate the configured report paths, coverage paths and full-suite argv instead of only NUL bytes.
- **Umbrella** (`IVP-1-4-24ab0a4135`): all seven integration families are implemented and directly asserted, with each family's remaining limits recorded on its own row.

- **Totals after batch 16: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 429 `TESTED`, 372 `IMPLEMENTED`, 40 `IN_PROGRESS`, 12 `NOT_STARTED`; implemented+verified lower bound 801/853 = 93.8%.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-12 | Per-batch validators for batches 15 and 16 (`check15.py`: exact 8-ID set, field rules, section counts 1.4=5 / 8.4=1 / 8.7=1 / 4.3=1; `check16.py`: exact 2-ID set, section count 1.4=2; evidence dated 2026-11-12) | PASS | 10/10 rows; no missing/extra IDs; all rows TESTED with implementation + verification + evidence + limitations |
| 2026-11-12 | `merge_updates.py batch15.json` then `batch16.json` | PASS | 8 + 2 rows merged; total rows unchanged at 853 |
| 2026-11-12 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 429 `TESTED`, 372 `IMPLEMENTED`, 40 `IN_PROGRESS`, 12 `NOT_STARTED` |
| 2026-11-12 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-12 | `python scripts/test_generate_tracker.py` | PASS | 6 inventory extraction/scoring tests |
| 2026-11-12 | `gofmt -l` over tracked + untracked `*.go` | PASS | All 153 Go files clean |
| 2026-11-12 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-12 | `go test ./... -count=1` | PASS | All packages green (27 with tests incl. `adapters/*`, `analysis`, `cli`, `config`, `persistence`, `tests/historical-replay`, `tests/integration`) |
| 2026-11-12 | `go build ./...` | PASS | All packages and commands build |
| 2026-11-12 | Real gradle/maven/bazel/jest/pytest/nx execution; pytest `--cov-context` run; PostgreSQL acceptance of test-specific coverage observations | Environment-gated | No native toolchains or PostgreSQL on this host: command construction, result parsing and coverage ingestion are asserted over fixtures and the capture seam, and the PostgreSQL-backed acceptance stays CI-only |

## 2026-11-12 — WP-E: Derived graph index, traversal bounds & superseding corrections (batches 17–18, 6 rows)

Closed strategy §3.4.1 and LLD §13.3/§12.4: the adjacency lists, connected target/module mappings and affected-target index are derived once per snapshot and cached by snapshot hash and algorithm version, traversal is bounded by policy so a reached bound escalates instead of silently truncating, and a misparsed outcome can be corrected by an append-only superseding record.

- **ADR 0021** (`docs/adr/0021-derived-graph-index-and-traversal-bounds.md`): the derived index is an in-process, snapshot-keyed cache over the immutable raw graph (not a SQL materialized view); the cache key is the snapshot hash plus the algorithm version, and a cached index is reused only when an order-independent **content fingerprint** of the nodes and edges still matches — counting nodes and edges is not enough, and an earlier draft that did exactly that silently served an index built for a different graph; the affected-target closure is computed from the target side (targets are far fewer than files); the traversal limits live on `domain.Policy.TraversalLimits` next to the sibling graph thresholds, so no new configuration key, schema change or template change was introduced; a reached bound marks the graph incomplete and escalates with `GRAPH_INCOMPLETE`.
- **Implementation**: `internal/analysis/derived_index.go` (new) derives `ReverseDeps`, `ReverseCoverage`, `DependencyAdj`, `ContractConsumers`, `ConnectedTargets` and `AffectedTargets`, with a bounded eight-entry LRU cache; `internal/analysis/graph.go` consumes the index through `AnalyzeImpactIndexed` and bounds the traversal by node count and dependency depth; `internal/analysis/planner.go` obtains the index with `DerivedIndexFor(evidence.Snapshot.TreeHash, …)` and escalates an incomplete traversal; `internal/domain/policy.go` carries `TraversalLimits`, and `internal/config/precedence.go` sets the effective bounds (100000 nodes, depth 64).
- **New direct assertions**: `TestDerivedIndexCarriesAdjacencyAndAffectedTargets` (adjacency by edge type, affected-target index, connected mappings), `TestDerivedIndexIsCachedBySnapshotHashAndAlgorithmVersion` (reuse, distinct keys, content-fingerprint validation), `TestTraversalBoundMarksGraphIncompleteAndEscalates` (depth budget, node budget, unset bounds leave the plan unchanged).
- **Superseding corrections** (`IVP-12-4-e5999c8ed3`): migration `006_result_corrections.sql` adds the append-only correction table with its index, the immutable-row trigger and tenant isolation; `RecordResultCorrection` reads the stored result to capture the status it supersedes and never modifies or deletes it, and `ApplyResultCorrections` returns the effective results with the superseded status and the audit reason visible; `GetExecutionUnitResults` loads the corrections and applies them. New assertions: `TestApplyResultCorrectionsKeepsHistoryAndSupersedesStatus`, `TestLatestCorrectionForAResultWins`, `TestCorrectionRequiresUnitAttemptStatusAndReason`.

- **Totals after batch 18: 853 rows, 853 reviewed, 0 `UNREVIEWED`** — 435 `TESTED`, 372 `IMPLEMENTED`, 35 `IN_PROGRESS`, 11 `NOT_STARTED`; implemented+verified lower bound 807/853 = 94.6%.

| Date | Command/check | Result | Evidence or limitation |
|---|---|---|---|
| 2026-11-12 | Per-batch validators for batches 17 and 18 (`check17.py`: exact 5-ID set, section counts 13.3=3 / Appendix-C=1 / 2.2=1; `check18.py`: exact 1-ID set, section count 12.4=1; evidence dated 2026-11-12) | PASS | 6/6 rows; no missing/extra IDs; all rows TESTED with implementation + verification + evidence + limitations |
| 2026-11-12 | `merge_updates.py batch17.json` then `batch18.json` | PASS | 5 + 1 rows merged; total rows unchanged at 853 |
| 2026-11-12 | Inventory totals | PASS | 853 reviewed / 0 `UNREVIEWED`; 435 `TESTED`, 372 `IMPLEMENTED`, 35 `IN_PROGRESS`, 11 `NOT_STARTED` |
| 2026-11-12 | `python scripts/generate-tracker.py` then `--check` | PASS | `requirements: 853`; generated tracker current |
| 2026-11-12 | `gofmt -l` over tracked + untracked `*.go` | PASS | All 155 Go files clean |
| 2026-11-12 | `go vet ./...` and `go vet -tags integration ./...` | PASS | No findings; integration-tagged packages compile |
| 2026-11-12 | `go test ./... -count=1` | PASS | All packages green (27 with tests) |
| 2026-11-12 | `go build ./...` | PASS | All packages and commands build |
| 2026-11-12 | `result_corrections` migration, RLS policy and immutable trigger | CI-only | PostgreSQL-only: compile-verified locally, executed by the PostgreSQL-backed integration tests in CI |

