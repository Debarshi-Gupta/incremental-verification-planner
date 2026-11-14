# Repository tree and supported adapter versions

This document is generated from the repository by `scripts/generate-repository-tree.py`. Do not edit it by hand; run the script and commit the result.

## Repository tree

```
├── .env.example
├── .gitlab-ci.yml
├── docker-compose.yaml
├── Dockerfile
├── go.mod
├── LICENSE
├── Makefile
├── README.md
├── api/
│   └── openapi.yaml
├── cmd/
│   ├── ivp/
│   │   ├── main.go
│   │   ├── signal_send_notwindows.go
│   │   ├── signal_send_windows.go
│   │   └── signal_test.go
│   └── ivp-api/
│       ├── main.go
│       └── main_test.go
├── config/
│   └── default-config.yaml
├── deploy/
│   ├── helm/
│   │   └── ivp/
│   │       ├── Chart.yaml
│   │       ├── values.yaml
│   │       └── templates/
│   │           ├── _helpers.tpl
│   │           ├── artifact-pvc.yaml
│   │           ├── deployment.yaml
│   │           ├── ingress.yaml
│   │           ├── migration-job.yaml
│   │           ├── networkpolicy.yaml
│   │           ├── pdb.yaml
│   │           ├── retention-cronjob.yaml
│   │           ├── service.yaml
│   │           ├── serviceaccount.yaml
│   │           └── servicemonitor.yaml
│   └── observability/
│       ├── ivp-alerts.yaml
│       ├── ivp-dashboard.json
│       ├── otel-collector.yaml
│       └── prometheus.yaml
├── docs/
│   ├── adapter-support.md
│   ├── architecture.md
│   ├── implementation-100-plan.md
│   ├── implementation-assessment.md
│   ├── implementation-completion-strategy.md
│   ├── operations.md
│   ├── research-evaluation.md
│   ├── security.md
│   └── adr/
│       ├── 0001-report-artifact-storage.md
│       ├── 0002-comparable-history-and-failure-retries.md
│       ├── 0003-discovery-report-path-patterns.md
│       ├── 0004-mode-scoped-audit-sample-rate.md
│       ├── 0005-execution-state-vocabulary-mapping.md
│       ├── 0006-offline-planning-full-suite-fallback-only.md
│       ├── 0007-maven-discovery-without-effective-pom.md
│       ├── 0008-gradle-task-typing-name-heuristic.md
│       ├── 0009-generic-adapter-version-constant.md
│       ├── 0010-openapi-handwritten-with-drift-test.md
│       ├── 0011-handwritten-config-schema-with-parity-test.md
│       ├── 0012-invalid-config-fails-closed-without-synthesizing-full-required-plan.md
│       ├── 0013-convention-edge-producers-and-change-classification.md
│       ├── 0014-reviewed-unit-exclusions.md
│       ├── 0015-audit-execution-outcome-reclassification.md
│       ├── 0016-symbol-index-deferred-with-file-granularity-fallback.md
│       ├── 0017-stage-8-execution-ordering-tiers.md
│       ├── 0018-rule-8-affected-targets-shares-the-changed-files-cap.md
│       ├── 0019-org-reviewed-narrow-exceptions-to-hard-rules.md
│       ├── 0020-binary-changes-lower-selection-confidence.md
│       ├── 0021-derived-graph-index-and-traversal-bounds.md
│       ├── 0022-execution-environment-fingerprint-comparison.md
│       ├── 0023-adapter-version-compatibility-check.md
│       ├── 0024-ci-selective-eligibility-reset.md
│       └── 0025-dependency-version-coverage-comparability.md
├── examples/
│   ├── sample-bazel/
│   │   ├── .bazelversion
│   │   ├── MODULE.bazel
│   │   ├── api/
│   │   │   ├── BUILD.bazel
│   │   │   ├── service.py
│   │   │   └── service_test.py
│   │   ├── common/
│   │   │   ├── BUILD.bazel
│   │   │   ├── math.py
│   │   │   └── math_test.py
│   │   └── web/
│   │       ├── app.py
│   │       ├── app_test.py
│   │       └── BUILD.bazel
│   ├── sample-generic/
│   │   ├── app/
│   │   │   ├── api.py
│   │   │   └── core.py
│   │   ├── tests/
│   │   │   ├── test_api.py
│   │   │   └── test_core.py
│   │   └── tools/
│   │       ├── format_check.py
│   │       └── scan_secrets.py
│   ├── sample-gradle/
│   │   ├── build.gradle
│   │   ├── gradle.properties
│   │   ├── gradlew
│   │   ├── gradlew.bat
│   │   ├── settings.gradle
│   │   ├── .fixture-output/
│   │   │   ├── deps-app.txt
│   │   │   ├── deps-root.txt
│   │   │   ├── deps-shared.txt
│   │   │   ├── projects.txt
│   │   │   ├── tasks-app.txt
│   │   │   ├── tasks-root.txt
│   │   │   ├── tasks-shared.txt
│   │   │   └── version.txt
│   │   ├── app/
│   │   │   ├── build.gradle
│   │   │   └── src/
│   │   │       ├── integrationTest/
│   │   │       │   └── java/
│   │   │       │       └── com/
│   │   │       │           └── example/
│   │   │       │               └── app/
│   │   │       │                   └── AppIT.java
│   │   │       ├── main/
│   │   │       │   └── java/
│   │   │       │       └── com/
│   │   │       │           └── example/
│   │   │       │               └── app/
│   │   │       │                   └── App.java
│   │   │       └── test/
│   │   │           └── java/
│   │   │               └── com/
│   │   │                   └── example/
│   │   │                       └── app/
│   │   │                           └── AppTest.java
│   │   ├── service/
│   │   │   └── build.gradle
│   │   └── shared/
│   │       ├── build.gradle
│   │       └── src/
│   │           ├── main/
│   │           │   └── java/
│   │           │       └── com/
│   │           │           └── example/
│   │           │               └── shared/
│   │           │                   └── MathUtil.java
│   │           └── test/
│   │               └── java/
│   │                   └── com/
│   │                       └── example/
│   │                           └── shared/
│   │                               └── MathUtilTest.java
│   ├── sample-inject/
│   │   ├── pyproject.toml
│   │   ├── orders/
│   │   │   ├── core.py
│   │   │   └── test_core.py
│   │   ├── service/
│   │   │   ├── service.py
│   │   │   └── test_service.py
│   │   └── utils/
│   │       ├── report.py
│   │       └── test_report.py
│   ├── sample-jest/
│   │   ├── package.json
│   │   ├── build/
│   │   │   └── jest-junit/
│   │   │       └── ivp-junit-reporter-d40d03c6e36f1103.js
│   │   └── packages/
│   │       ├── core/
│   │       │   ├── math.js
│   │       │   └── math.test.js
│   │       └── ui/
│   │           ├── label.js
│   │           └── label.test.js
│   ├── sample-maven/
│   │   ├── mvnw
│   │   ├── pom.xml
│   │   ├── core/
│   │   │   ├── pom.xml
│   │   │   └── src/
│   │   │       ├── main/
│   │   │       │   └── java/
│   │   │       │       └── com/
│   │   │       │           └── example/
│   │   │       │               └── core/
│   │   │       │                   └── Calc.java
│   │   │       └── test/
│   │   │           └── java/
│   │   │               └── com/
│   │   │                   └── example/
│   │   │                       └── core/
│   │   │                           └── CalcTest.java
│   │   └── service/
│   │       ├── pom.xml
│   │       └── src/
│   │           ├── it/
│   │           │   └── java/
│   │           │       └── com/
│   │           │           └── example/
│   │           │               └── service/
│   │           │                   └── ReceiptIT.java
│   │           ├── main/
│   │           │   └── java/
│   │           │       └── com/
│   │           │           └── example/
│   │           │               └── service/
│   │           │                   └── ReceiptService.java
│   │           └── test/
│   │               └── java/
│   │                   └── com/
│   │                       └── example/
│   │                           └── service/
│   │                               └── ReceiptServiceTest.java
│   ├── sample-nx/
│   │   ├── nx.json
│   │   └── apps/
│   │       ├── api/
│   │       │   ├── project.json
│   │       │   └── src/
│   │       │       ├── main.spec.ts
│   │       │       └── main.ts
│   │       └── web/
│   │           ├── project.json
│   │           └── src/
│   │               ├── main.spec.ts
│   │               └── main.ts
│   └── sample-pytest/
│       ├── conftest.py
│       ├── pyproject.toml
│       ├── app/
│       │   ├── calc.py
│       │   └── report.py
│       └── tests/
│           ├── test_calc.py
│           └── test_report.py
├── internal/
│   ├── adapters/
│   │   ├── adapter.go
│   │   ├── contract_test.go
│   │   ├── domain.go
│   │   ├── domain_test.go
│   │   ├── equivalent_outcome_test.go
│   │   ├── reports.go
│   │   ├── reports_test.go
│   │   ├── bazel/
│   │   │   ├── bazel.go
│   │   │   ├── bazel_bep_test.go
│   │   │   ├── bazel_cquery_test.go
│   │   │   ├── bazel_fixture_test.go
│   │   │   └── bazel_test.go
│   │   ├── builtin/
│   │   │   ├── registry.go
│   │   │   └── registry_test.go
│   │   ├── fixtures/
│   │   │   ├── README.md
│   │   │   ├── bazel/
│   │   │   │   ├── 5/
│   │   │   │   │   ├── failure.jsonl
│   │   │   │   │   ├── full_suite.jsonl
│   │   │   │   │   ├── malformed.jsonl
│   │   │   │   │   ├── odd_output.jsonl
│   │   │   │   │   ├── selected_suite.jsonl
│   │   │   │   │   ├── skipped.jsonl
│   │   │   │   │   ├── timeout_interrupt.txt
│   │   │   │   │   ├── truncated.jsonl
│   │   │   │   │   └── zero_tests.jsonl
│   │   │   │   └── 7/
│   │   │   │       ├── failure.jsonl
│   │   │   │       ├── full_suite.jsonl
│   │   │   │       ├── malformed.jsonl
│   │   │   │       ├── odd_output.jsonl
│   │   │   │       ├── selected_suite.jsonl
│   │   │   │       ├── skipped.jsonl
│   │   │   │       ├── timeout_interrupt.txt
│   │   │   │       ├── truncated.jsonl
│   │   │   │       └── zero_tests.jsonl
│   │   │   ├── generic/
│   │   │   │   ├── config-v1/
│   │   │   │   │   ├── failure.xml
│   │   │   │   │   ├── full_suite.xml
│   │   │   │   │   ├── malformed.xml
│   │   │   │   │   ├── odd_output.xml
│   │   │   │   │   ├── selected_suite.xml
│   │   │   │   │   ├── skipped.xml
│   │   │   │   │   ├── timeout_interrupt.txt
│   │   │   │   │   ├── truncated.xml
│   │   │   │   │   └── zero_tests.xml
│   │   │   │   └── config-v2/
│   │   │   │       ├── failure.json
│   │   │   │       ├── full_suite.json
│   │   │   │       ├── malformed.json
│   │   │   │       ├── odd_output.json
│   │   │   │       ├── selected_suite.json
│   │   │   │       ├── skipped.json
│   │   │   │       ├── timeout_interrupt.txt
│   │   │   │       ├── truncated.json
│   │   │   │       └── zero_tests.json
│   │   │   ├── gradle/
│   │   │   │   ├── 7.6/
│   │   │   │   │   ├── failure.xml
│   │   │   │   │   ├── full_suite.xml
│   │   │   │   │   ├── malformed.xml
│   │   │   │   │   ├── odd_output.xml
│   │   │   │   │   ├── selected_suite.xml
│   │   │   │   │   ├── skipped.xml
│   │   │   │   │   ├── timeout_interrupt.txt
│   │   │   │   │   ├── truncated.xml
│   │   │   │   │   └── zero_tests.xml
│   │   │   │   └── 8.7/
│   │   │   │       ├── failure.xml
│   │   │   │       ├── full_suite.xml
│   │   │   │       ├── malformed.xml
│   │   │   │       ├── odd_output.xml
│   │   │   │       ├── selected_suite.xml
│   │   │   │       ├── skipped.xml
│   │   │   │       ├── timeout_interrupt.txt
│   │   │   │       ├── truncated.xml
│   │   │   │       └── zero_tests.xml
│   │   │   ├── jest/
│   │   │   │   ├── 27/
│   │   │   │   │   ├── failure.json
│   │   │   │   │   ├── full_suite.json
│   │   │   │   │   ├── malformed.json
│   │   │   │   │   ├── odd_output.json
│   │   │   │   │   ├── selected_suite.json
│   │   │   │   │   ├── skipped.json
│   │   │   │   │   ├── timeout_interrupt.txt
│   │   │   │   │   ├── truncated.json
│   │   │   │   │   └── zero_tests.json
│   │   │   │   └── 29/
│   │   │   │       ├── failure.json
│   │   │   │       ├── full_suite.json
│   │   │   │       ├── malformed.json
│   │   │   │       ├── odd_output.json
│   │   │   │       ├── selected_suite.json
│   │   │   │       ├── skipped.json
│   │   │   │       ├── timeout_interrupt.txt
│   │   │   │       ├── truncated.json
│   │   │   │       └── zero_tests.json
│   │   │   ├── maven/
│   │   │   │   ├── surefire-2.22/
│   │   │   │   │   ├── failure.xml
│   │   │   │   │   ├── full_suite.xml
│   │   │   │   │   ├── malformed.xml
│   │   │   │   │   ├── odd_output.xml
│   │   │   │   │   ├── selected_suite.xml
│   │   │   │   │   ├── skipped.xml
│   │   │   │   │   ├── timeout_interrupt.txt
│   │   │   │   │   ├── truncated.xml
│   │   │   │   │   └── zero_tests.xml
│   │   │   │   └── surefire-3.2/
│   │   │   │       ├── failure.xml
│   │   │   │       ├── full_suite.xml
│   │   │   │       ├── malformed.xml
│   │   │   │       ├── odd_output.xml
│   │   │   │       ├── selected_suite.xml
│   │   │   │       ├── skipped.xml
│   │   │   │       ├── timeout_interrupt.txt
│   │   │   │       ├── truncated.xml
│   │   │   │       └── zero_tests.xml
│   │   │   ├── nx/
│   │   │   │   ├── 15/
│   │   │   │   │   ├── failure.json
│   │   │   │   │   ├── full_suite.json
│   │   │   │   │   ├── malformed.json
│   │   │   │   │   ├── odd_output.json
│   │   │   │   │   ├── selected_suite.json
│   │   │   │   │   ├── skipped.json
│   │   │   │   │   ├── timeout_interrupt.txt
│   │   │   │   │   ├── truncated.json
│   │   │   │   │   └── zero_tests.json
│   │   │   │   └── 19/
│   │   │   │       ├── failure.xml
│   │   │   │       ├── full_suite.xml
│   │   │   │       ├── malformed.xml
│   │   │   │       ├── odd_output.xml
│   │   │   │       ├── selected_suite.xml
│   │   │   │       ├── skipped.xml
│   │   │   │       ├── timeout_interrupt.txt
│   │   │   │       ├── truncated.xml
│   │   │   │       └── zero_tests.xml
│   │   │   └── pytest/
│   │   │       ├── 7/
│   │   │       │   ├── failure.xml
│   │   │       │   ├── full_suite.xml
│   │   │       │   ├── malformed.xml
│   │   │       │   ├── odd_output.xml
│   │   │       │   ├── selected_suite.xml
│   │   │       │   ├── skipped.xml
│   │   │       │   ├── timeout_interrupt.txt
│   │   │       │   ├── truncated.xml
│   │   │       │   └── zero_tests.xml
│   │   │       └── 8/
│   │   │           ├── failure.xml
│   │   │           ├── full_suite.xml
│   │   │           ├── malformed.xml
│   │   │           ├── odd_output.xml
│   │   │           ├── selected_suite.xml
│   │   │           ├── skipped.xml
│   │   │           ├── timeout_interrupt.txt
│   │   │           ├── truncated.xml
│   │   │           └── zero_tests.xml
│   │   ├── generic/
│   │   │   ├── generic.go
│   │   │   ├── generic_fixture_test.go
│   │   │   └── generic_test.go
│   │   ├── gradle/
│   │   │   ├── gradle.go
│   │   │   ├── gradle_fixture_test.go
│   │   │   └── gradle_test.go
│   │   ├── jest/
│   │   │   ├── jest.go
│   │   │   ├── jest_fixture_test.go
│   │   │   ├── jest_test.go
│   │   │   ├── junit.go
│   │   │   ├── junit_test.go
│   │   │   ├── projects.go
│   │   │   ├── projects_test.go
│   │   │   ├── report_paths_test.go
│   │   │   └── reporter/
│   │   │       └── ivp-junit-reporter.js
│   │   ├── maven/
│   │   │   ├── maven.go
│   │   │   ├── maven_fixture_test.go
│   │   │   ├── maven_provider_test.go
│   │   │   └── maven_test.go
│   │   ├── native/
│   │   │   ├── base.go
│   │   │   └── base_test.go
│   │   ├── nx/
│   │   │   ├── nx.go
│   │   │   ├── nx_fixture_test.go
│   │   │   └── nx_test.go
│   │   └── pytest/
│   │       ├── pytest.go
│   │       ├── pytest_fixture_test.go
│   │       ├── pytest_test.go
│   │       └── report_paths_test.go
│   ├── analysis/
│   │   ├── classify.go
│   │   ├── classify_test.go
│   │   ├── conventions.go
│   │   ├── conventions_test.go
│   │   ├── derived_index.go
│   │   ├── derived_index_test.go
│   │   ├── evidence.go
│   │   ├── glob.go
│   │   ├── graph.go
│   │   ├── planner.go
│   │   ├── planner_completeness_test.go
│   │   ├── planner_test.go
│   │   └── risk.go
│   ├── api/
│   │   ├── artifacts.go
│   │   ├── artifacts_tenant_test.go
│   │   ├── openapi_drift_test.go
│   │   ├── server.go
│   │   └── server_test.go
│   ├── artifact/
│   │   ├── s3.go
│   │   ├── store.go
│   │   └── store_test.go
│   ├── audit/
│   │   ├── classifier.go
│   │   ├── classifier_test.go
│   │   ├── metrics.go
│   │   ├── schedule.go
│   │   └── service.go
│   ├── cli/
│   │   ├── audit.go
│   │   ├── audit_test.go
│   │   ├── cli.go
│   │   ├── client.go
│   │   ├── conventions.go
│   │   ├── conventions_test.go
│   │   ├── coverage_contexts_test.go
│   │   ├── default-config.yaml
│   │   ├── index.go
│   │   ├── outcome.go
│   │   ├── plan.go
│   │   ├── report.go
│   │   └── run.go
│   ├── config/
│   │   ├── adapter.go
│   │   ├── config_test.go
│   │   ├── defaults.go
│   │   ├── load.go
│   │   ├── precedence.go
│   │   ├── readiness.go
│   │   ├── schema_parity_test.go
│   │   ├── types.go
│   │   └── validate.go
│   ├── domain/
│   │   ├── adapter_compat.go
│   │   ├── adapter_compat_test.go
│   │   ├── coverage_comparability.go
│   │   ├── coverage_comparability_test.go
│   │   ├── enums.go
│   │   ├── fingerprint.go
│   │   ├── policy.go
│   │   ├── types.go
│   │   ├── upgrade.go
│   │   ├── upgrade_test.go
│   │   ├── validation.go
│   │   └── validation_test.go
│   ├── execution/
│   │   ├── environment.go
│   │   ├── environment_test.go
│   │   ├── outbox.go
│   │   ├── outbox_test.go
│   │   ├── process_unix.go
│   │   ├── process_windows.go
│   │   ├── runner.go
│   │   ├── runner_test.go
│   │   └── scheduler.go
│   ├── gitutil/
│   │   ├── fingerprint.go
│   │   ├── git.go
│   │   └── git_test.go
│   ├── persistence/
│   │   ├── artifacts.go
│   │   ├── artifacts_integration_test.go
│   │   ├── artifacts_test.go
│   │   ├── audits.go
│   │   ├── audits_test.go
│   │   ├── corrections.go
│   │   ├── corrections_test.go
│   │   ├── coverage_history.go
│   │   ├── db.go
│   │   ├── execution_lifecycle_integration_test.go
│   │   ├── generate-sqlc.ps1
│   │   ├── history.go
│   │   ├── history_test.go
│   │   ├── idempotency.go
│   │   ├── outcomes.go
│   │   ├── plans_executions.go
│   │   ├── postgres_integration_test.go
│   │   ├── quarantine_calibration_integration_test.go
│   │   ├── reads.go
│   │   ├── repositories.go
│   │   ├── retention.go
│   │   ├── retention_integration_test.go
│   │   ├── snapshot_read.go
│   │   ├── sqlc.yaml
│   │   ├── upgrade_state.go
│   │   ├── migrations/
│   │   │   ├── 001_init.sql
│   │   │   ├── 002_artifact_execution.sql
│   │   │   ├── 003_accepted_change_outcomes.sql
│   │   │   ├── 004_snapshot_test_config_digest.sql
│   │   │   ├── 005_test_history_configuration_identity.sql
│   │   │   ├── 006_result_corrections.sql
│   │   │   └── 007_dependency_and_upgrade_identity.sql
│   │   ├── queries/
│   │   │   └── repositories.sql
│   │   └── sqlc/
│   │       ├── db.go
│   │       ├── models.go
│   │       ├── querier.go
│   │       └── repositories.sql.go
│   ├── security/
│   │   ├── path.go
│   │   ├── policy_signature.go
│   │   ├── principal.go
│   │   ├── redaction.go
│   │   └── redaction_test.go
│   ├── service/
│   │   ├── artifact_independence_test.go
│   │   ├── artifact_tenant_isolation_integration_test.go
│   │   ├── artifacts.go
│   │   ├── artifacts_test.go
│   │   ├── audit_scenarios_integration_test.go
│   │   ├── backend.go
│   │   ├── backend_metrics_test.go
│   │   ├── e2e_report_workflow_test.go
│   │   ├── plan_gates.go
│   │   ├── plan_gates_test.go
│   │   ├── plan_lifecycle_integration_test.go
│   │   └── quarantine_gating_integration_test.go
│   └── telemetry/
│       ├── metrics.go
│       ├── metrics_test.go
│       └── tracing.go
├── schemas/
│   └── ivp-config.schema.json
├── scripts/
│   ├── backup-postgres.ps1
│   ├── generate-repository-tree.py
│   ├── generate-tracker.py
│   ├── ivp-postgres.psm1
│   ├── test_generate_tracker.py
│   ├── verify-artifact-backup.ps1
│   ├── verify-helm-render.ps1
│   ├── verify-postgres-backup.ps1
│   └── verify-postgres-restore-drill.ps1
└── tests/
    ├── e2e/
    │   └── planner_test.go
    ├── historical-replay/
    │   ├── cli_test.go
    │   ├── corpus.go
    │   ├── corpus_test.go
    │   ├── main.go
    │   ├── main_test.go
    │   ├── split.go
    │   ├── split_test.go
    │   ├── tree.go
    │   ├── tree_test.go
    │   └── testdata/
    │       ├── .gitattributes
    │       └── corpus.jsonl
    ├── integration/
    │   └── change_injection_test.go
    └── performance/
        └── impact_test.go
```

The tree lists 461 source-controlled files.

## Supported adapter versions

The tool versions are the golden-fixture corpus directories the shared equivalent-outcome contract test drives (`internal/adapters/equivalent_outcome_test.go`); every listed version is exercised for every report category. The selection granularity is the adapter's declared `Capabilities.SelectableGranularity`, and the report formats are its declared `Capabilities.ResultFormats`.

| Adapter | Supported tool versions | Selection granularity | Report formats |
|---|---|---|---|
| bazel | `5`, `7` | `BAZEL_TEST_TARGET` | bazel-bep-json |
| generic | `config-v1`, `config-v2` | `DECLARED_UNIT` | junit-xml, jest-json |
| gradle | `7.6`, `8.7` | `GRADLE_TASK` | junit-xml |
| jest | `27`, `29` | `JEST_TEST_FILE` | jest-json, junit-xml |
| maven | `surefire-2.22`, `surefire-3.2` | `MAVEN_MODULE_GOAL` | surefire-xml, failsafe-xml |
| nx | `15`, `19` | `NX_PROJECT_TARGET` | junit-xml, jest-json |
| pytest | `7`, `8` | `PYTEST_NODEID` | junit-xml |

## Layout

| Path | Purpose |
|---|---|
| `cmd/ivp/` | CLI entry point (`ivp`): plan, run, audit, report, index. |
| `cmd/ivp-api/` | API and maintenance entry point (`ivp-api`): HTTP server, migrations, retention. |
| `api/` | Hand-written OpenAPI 3.1 description of the HTTP surface. |
| `internal/analysis/` | Planner, impact analysis, derived graph index, risk scoring. |
| `internal/adapters/` | The seven build-system adapters and their golden fixture corpus. |
| `internal/api/` | HTTP routing, request validation, idempotency, error mapping. |
| `internal/artifact/` | Bounded tenant-scoped artifact stores (filesystem and S3). |
| `internal/audit/` | Audit comparison and quarantine decisions. |
| `internal/cli/` | CLI command implementations. |
| `internal/config/` | Configuration schema, defaults, precedence, and validation. |
| `internal/domain/` | Shared domain types, enums, and validation. |
| `internal/execution/` | Bounded process runner and scheduler. |
| `internal/persistence/` | PostgreSQL schema, migrations, and repositories. |
| `internal/security/` | Path safety, redaction, principals, policy signatures. |
| `internal/telemetry/` | Prometheus metrics and OpenTelemetry tracing. |
| `deploy/helm/ivp/` | Kubernetes chart: API, migration job, retention CronJob, PDB, NetworkPolicy. |
| `deploy/observability/` | Prometheus alert rules, scrape config, collector and dashboard. |
| `docs/` | Architecture, operations, security, threat model, ADRs, and generated documents. |
| `scripts/` | Operational verification and generation scripts. |
| `schemas/` | JSON schema for the repository configuration file. |
| `tests/` | End-to-end, performance, and historical-replay suites. |
