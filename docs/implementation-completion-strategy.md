# IVP completion strategy

**Date:** 2026-10-31
**Purpose:** Close the gap between the current repository state and the LLD
(`docs/Incremental_Verification_Planner_LLD.md`) as the normative source of truth.
**Method:** Full source review of every `internal/`, `cmd/`, `api/`, `deploy/`,
`tests/`, and `scripts/` package against LLD §§0–23 and Appendices A–D, plus a live
build/test probe on this host.

---

## 1. Current state assessment

### 1.1 What exists (verified by direct code review)

| Area | State |
|---|---|
| Domain model, enums, reason codes (App. A), outcomes (App. B) | All 45 reason codes and 9 outcomes defined (`internal/domain/enums.go:99-166`) |
| Planner (LLD §6) | All 9 stages present in `internal/analysis/planner.go`; risk scoring, greedy set-cover, deterministic tie-breaks, hard-escalation rules mostly present |
| Adapters (LLD §8) | All 7 adapters implement the full 9-method interface; real native-tool discovery for Gradle/Bazel/Nx/pytest/Jest; no stubs, no TODOs |
| Execution runner (LLD §9) | argv-only execution, env allowlist, output caps, timeouts, process groups (Unix), redaction, failure classes, plan revalidation before/between/after commands |
| API (LLD §12) | All 16 LLD §12.1 operations + 5 extras registered with role checks, pagination (repos/metrics), request IDs, body limits, rate limits, idempotency incl. 409 mapping |
| Persistence (LLD §13) | All 20 required tables + idempotency table; RLS on 19 tables; immutability triggers; cross-tenant tests |
| Audit (LLD §10) | Comparability, 4-way miss classification, no autonomous downgrade, transactional quarantine creation, Wilson CI |
| Security (LLD §14) | OIDC validation, server-side role mapping, redaction, artifact sanitizer, SBOM + Trivy + Cosign in CI |
| Observability (LLD §15) | All 16 Prometheus metric families registered with bounded labels; 11/13 alerts |
| Deployment | Dockerfile, Compose, Helm chart, retention job, backup/restore scripts, dashboards |
| Governance | 853-row requirement inventory with generator + tests; implementation log; 12 ADRs (0001–0012) |

### 1.2 Live probe results (this host, 2026-10-31)

| Check | Result |
|---|---|
| `go build ./...` | **FAIL** — `internal/service/backend.go:238:10: no new variables on left side of :=` |
| Host constraints | Windows; no Docker engine, no Helm, no PostgreSQL, `CGO_ENABLED=0`/no GCC (no race detector), no native JVM/Bazel/Nx toolchains |

**The committed HEAD does not compile.** The last commit ("Continue implementation",
2026-10-31 02:16) introduced the defect after the last recorded passing validation
(2026-10-30). Everything below assumes Wave 0 restores a green build first.

### 1.3 Why the 5% tracker score understates reality

`docs/implementation-tracker.md` reports 5.0% implementation coverage because ~797
of 853 requirement rows are `UNREVIEWED`, not because the code is absent. Source
review shows the core product is substantially built; the true gap is concentrated
in (a) safety/correctness defects, (b) adapter evidence quality, (c) the §17 test
program, and (d) operational validation that this Windows host cannot perform.
The inventory review is a **bookkeeping and evidence** task, not a build task, and
must run in parallel with — not block — the functional waves.

---

## 2. P0 — Correctness and safety defects (fix before any other work)

These violate normative LLD safety rules or break the build. Each fix needs a
regression test proving the conservative direction.

| # | Defect | Evidence | Required fix |
|---|---|---|---|
| P0-1 | **Build break** — `hash, _ :=` redeclares function-scoped `hash` | `internal/service/backend.go:238` (declared at `:210`) | Change to `=`; add `go build ./...` as the first CI step order guarantee (already present — this escaped only because the commit was never pushed through CI) |
| P0-2 | **Quarantine auto-clears on expiry alone** — gating query treats `expires_at <= now()` as inactive, so selective gating resumes without acknowledgement + clean audit | `internal/service/backend.go:500` (`AND (expires_at IS NULL OR expires_at>now())`) | Active = `status IN ('QUARANTINED','ACKNOWLEDGED')` regardless of expiry; expiry only enables the *clear* path. LLD §7.2: "must not auto-clear solely because time passed" |
| P0-3 | **Never-expiring quarantines are un-clearable** — clear requires `expires_at IS NOT NULL AND <= now` | `internal/persistence/audits.go:233` | Allow clearing ACKNOWLEDGED quarantines with `expires_at IS NULL` once a clean comparable post-acknowledgement audit exists |
| P0-4 | **RISK_BASED_CI calibration is not observe-scoped** — calibration counts *any* execution, not OBSERVE-mode runs; `modes.risk_based_ci.audit_sample_rate` is validated/frozen but unused (audits read `safety.audit.sample_rate`) | `internal/persistence/reads.go:100-116`; `internal/config/precedence.go:101` | Count only OBSERVE-mode executions/audits; wire or delete the duplicate rate field and reject divergence in validation |
| P0-5 | **Ctrl-C is not wired** — no `signal.NotifyContext` in `cmd/ivp/main.go`; exit code 8 unreachable; Unix `Setpgid` orphans the child test process on terminal SIGINT | `cmd/ivp/main.go:18`, `internal/cli/run.go:351` | `signal.NotifyContext`; forward SIGINT/SIGTERM to the process group; persist CANCELLED state; flush bounded report; return 8 (LLD §16 row 15, §11.2) |
| P0-6 | **Determinism leaks** — plan detail strings built by map iteration; evidence refs tie-break only on `ObservedAt` | `internal/analysis/planner.go:284-288`, `:1339-1347` | Sort keys before string build; add stable-key secondary sort. LLD §6.3.9 requires reproducible plans |
| P0-7 | **pytest report-path mismatch** — Discover stamps `.ivp/reports/pytest.xml`, Materialize writes `pytest-<hash>.xml` | `internal/adapters/pytest/pytest.go:57` vs `:98` | Unify on the hashed path in both places |

**Wave 0 gate:** `gofmt`, `go vet ./...`, `go test ./...`, `go build ./cmd/...` green
locally; new regression tests for P0-2, P0-3, P0-4, P0-5, P0-6, P0-7; tracker rows
for each marked `TESTED`.

*Executed 2026-11-01: all seven P0 defects fixed with regression tests (two extra
P0-class defects found while wiring P0-5 were fixed as well); see the Wave 0 entry
in `docs/implementation-log.md`. P0-4's "duplicate rate field" was resolved by
scoping rather than deleting — ADR 0004 gives each key its own mode, so there is
no divergence left to reject.*

---

## 3. Gap register (functional, P1)

Grouped by subsystem. "Partial" items list the specific missing behavior.

### 3.1 Adapters (LLD §8, §17.2)

1. **No checked-in fixture corpus.** All fixtures are inline literals; LLD §20
   requires `internal/adapters/fixtures/` (and repo-level `testdata/`). Build a
   per-adapter × 11-category matrix: full suite, selected suite, failure, skipped,
   zero tests, malformed, truncated, missing, timeout/interrupt, unknown target,
   unicode/spaces/ANSI/secret-looking output — plus **two tool versions** per
   adapter where output formats evolved.
2. **No equivalent-outcome contract test.** Add a shared table-driven suite asserting
   identical canonical classification (pass/fail/skip/zero/infra) for every adapter's
   `ParseResults`, and that setup failure is never a pass.
3. **Gradle:** verify task *type* supports `--tests` (currently a name heuristic that
   accepts `compileTestJava`); class-level filter only, no method level — declare
   honest capability or implement. *Decision recorded: ADR 0008 keeps the name
   heuristic as a candidate-only rule guarded at execution time
   (`ZERO_TESTS`/`REPORT_PARSE` infra failure is never a pass), declares
   class-level `GRADLE_TASK` granularity with no method-level claim, and keeps the
   false-positive/confidence tension tracked here as an open gap.*
4. **Maven:** blind `-Dtest` filtering without Surefire/Failsafe provider check;
   reactor graph ignores parent-POM/dependencyManagement inheritance (no effective
   POM). Implement provider detection and conservative reactor expansion.
   *Decision recorded: ADR 0007 pins discovery without an effective POM (static
   bounded POM parsing, `maven:reactor-module-model` confidence 0.75, conservative
   `#verify`-only-with-Failsafe detection, class-level filtering). Remaining open
   in this item: the `surefire_enabled`/`failsafe_enabled` keys are accepted but
   unenforced (ADR 0007, final bullet).*
5. **Bazel:** no `cquery`/configuration transitions while claiming
   `CONFIGURED_TARGET_GRAPH`; BEP parser lacks skipped/flaky/timeout categories and
   parses only the first report file.
6. **Jest:** no multi-project (`--projects`) resolution; capabilities claim
   `junit-xml` but Materialize never emits it.
7. **pytest:** no coverage.py per-test contexts when configured (LLD §8.6).
8. **Generic:** dead no-op loop `generic.go:179-185` removed and interface assertion
   added (Wave 0); hardcoded version string documented as `generic-config-v1` in
   ADR 0009. *Resolved.*
9. **Config validation depth:** native adapters validate only NUL bytes; validate
   report paths, coverage paths, and `FullSuiteArgv`.

### 3.2 Planner (LLD §6–§7)

1. **Stage 9 explanation incomplete** — add change summary, snapshot IDs, and
   adapter/toolchain versions (LLD §6.3.9). Preserve omission audit trail when
   escalating (`fullRequired` currently clears `OmittedUnits`).
2. **Dead escalation inputs** — `BroadRefactor` is never set; `IsGenerated` and
   `DetectedRiskLabels` are never populated by `ivp index`, so hard rules 3/4 are
   partially dormant in production paths. Wire classification in `internal/cli/index.go`.
3. **Missing hard rules** — "too many affected targets" and the org-reviewed narrow
   exception mechanism for global-impact files (LLD §6.3 rule 3).
4. **Stage 4/8** — no integration/E2E candidate union member beyond configured
   paths; ordering lacks the high-risk-consumer and integration/E2E tiers.
5. **4 reason codes never emitted** — `INTEGRATION_BOUNDARY`,
   `OUTSIDE_AFFECTED_TARGETS`, `EXCLUDED_BY_REVIEWED_POLICY`, `ENVIRONMENT_MISMATCH`.
   Emit them or document a schema/algorithm-version change per Appendix A.
6. **3 audit outcomes never surface on executions** — `AUDIT_MISS`,
   `AUDIT_POSSIBLE_MISS`, `AUDIT_INCOMPARABLE` must be assignable as execution
   outcomes (Appendix B) when audits reclassify a run.
7. **Criticality/severity hard-coded** — make policy-configurable per LLD Stage 5/6.
8. **Flaky classification** has no minimum-observation gate (`persistence/history.go:57`);
   a 1-sample flake rate of 1.0 destroys selection reliability. Add the §7.3 minimum.
9. **JSON Schema is unwired and drifting** — `schemas/ivp-config.schema.json` is
   referenced by no code; the shipped default config fails its own schema; constraint
   caps diverge (excerpt bytes, calibration minimums). Generate or test the schema
   against `config.Defaults()` and the §5.2 example in CI. *Resolved (Wave 1): ADR
   0011 chooses hand-maintained schema + parity gate;
   `internal/config/schema_parity_test.go` now tests struct↔schema key parity,
   `additionalProperties:false`, `config.Defaults()` + both shipped templates +
   the LLD §5.2 example against the schema, template key-completeness, and
   schema↔`Validate()` verdict agreement on ~40 boundary cases; drifted bounds and
   templates were fixed in the same wave.*
10. **`analysis.follow_renames` / `include_untracked` are unwired** — always on
    regardless of config. Either wire them or remove them from the schema (conservative
    default: keep always-on and document).
11. **Signed offline policy snapshots** — `ValidateOfflinePolicy` is dead code; the
    CLI never plans offline. LLD §20 permits local planning only with a signed,
    unexpired snapshot. Implement or record an ADR that offline planning is
    intentionally disabled and only full-suite fallback runs offline (most
    conservative — acceptable). *Decision recorded: ADR 0006 — offline planning is
    intentionally disabled; offline mode runs the full-suite fallback only, with
    `offline_policy` locked to its product default (`validate.go:42-44`).*
12. **Five graph edge types are never produced, and conflict/cycle detection is
    dormant** — `ivp index` emits only `DEPENDS_ON` and `TEST_COVERS`
    (`internal/cli/index.go:260,272,290,361,385,416-422`), yet the planner already
    consumes `CONTRACT_CONSUMER`, `OWNED_BY`, and `COLOCATED_WITH`
    (`internal/analysis/planner.go:495,508-513,609`) and `BUILT_FROM` /
    `CONFIGURES` exist only as enum + validation (`internal/domain/enums.go:36-37`,
    `validation.go:108`) with no producer and no consumer. Likewise no producer
    ever sets `Conflict=true` or declares a cycle, so `GraphHasConflicts` /
    `GraphHasCycles` consumers (`internal/analysis/graph.go:49,61,206`,
    `planner.go:158,218`) can never fire (LLD §4.4 lines 197-203, §13.3 line 970).
    Absence is safe — no false edges, dangling edges still force FULL_REQUIRED —
    but impact analysis is under-informed and the conflict/cycle gates are dead.
    Emit these edges during indexing (ownership, contracts, colocated conventions,
    generated-output provenance, config keys) or record an ADR deferring each type.
13. **Plan has no uncertainty bounds** — LLD §6.2 (line 415) requires estimated
    runtime and cost "including uncertainty bounds", and the §6.3 economic rule
    (line 445) refers to an uncertainty interval overlapping the full suite's
    runtime. The plan carries only point estimates (`EstimatedDurationMillis`,
    `EstimatedComputeMillis`, `internal/domain/types.go:192-193`), the economic
    gate compares point estimates only (`internal/analysis/planner.go:344-361`),
    and no P50–P95 interval is aggregated into the plan (per-unit P50/P95 exist
    in `internal/persistence/history.go:30,53-54` but are consumed only as the
    per-unit cost at `planner.go:1014-1024`). Conservative behavior: per-unit
    costs use P95 whenever sample count permits, which keeps the gate safe on
    the high side; a plan-level interval remains unimplemented. Inventory row
    `IVP-6-2-fd59dc2ae2` is tracked as IN_PROGRESS against this item.

### 3.3 Execution (LLD §9)

1. **OOM/termination-signal classification missing** — no `WaitStatus.Signaled()`
   check; a SIGKILL/OOM child collapses into generic `PROCESS_EXIT`.
2. **Outbox unbounded/unencrypted** — CLI writes `.ivp/outbox/<uuid>.json` with no
   byte/record cap and no encryption; the bounded `execution.Outbox` is test-only.
   Productionize it (bounds + at-rest protection for sensitive metadata).
3. **State machine naming** diverges from spec (`START_REQUESTED/EXECUTING/RESULT_READY/UPLOADED`
   vs `CREATED/VALIDATING/RUNNING/PARSING/COMPLETED`). Either rename or record an
   ADR mapping the two vocabularies and add `FAILED_TESTS` as a state.
   *Decision recorded: ADR 0005 maps the two vocabularies without renaming,
   including where `FAILED_TESTS` fits; no rename is pending.*
4. **§9.4 gaps** — no env-fingerprint recompute/compare mid-execution (digest only
   recorded); no policy-driven "immediate broad diagnosis on first failure".
5. **Windows child-tree kill** — no job object; mitigated by Windows refusing
   selective execution, but full-suite timeouts leak grandchildren. Implement job
   objects or document.
6. **Duplicate execution loop** — `scheduler.RunPlan` vs CLI `executeCommands`;
   consolidate on one path.
7. **Exit-code tests** — LLD §17.1 requires tests for every CLI exit code; none exist.
   Add a table-driven suite covering 0–8 including the code-4 misclassification in
   `report.go:22`.

### 3.4 Persistence & API (LLD §12–§13)

1. **§13.3 graph strategy (largest gap)** — no adjacency precomputation, no
   affected-target index, no derived-index cache, and traversal is unbounded in
   memory per plan (`analysis/graph.go:45-117`). Implement: snapshot-ingestion
   adjacency materialization, affected-target index, per-snapshot cache keyed by
   snapshot hash + algorithm version, and policy-driven node/depth limits that mark
   the graph incomplete (→ escalate) when reached. Add the missing `executions`
   index on `(repository, status, started_at)`.
2. **No superseding correction records** for misparsed results (LLD §12.4).
3. **Scheduled audit orchestration (LLD §10.2)** — no cadence config, no due-run
   selection, no server-side job; audits are submit-driven only. Add
   `safety.audit.cadence` (default daily), a due-run query, and a maintenance-job
   entry point that marks repositories overdue; wire the alert.
4. **§10.6 calibration metrics missing** — runtime/compute reduction, regression
   recall, selection precision, fallback/escalation rate, discovery/parse error
   rates, and breakdowns by adapter / changed-file class / algorithm version.
5. **Adjudication write path** — `adjudicated_by` column exists but nothing writes
   or exposes it; privilege changes and adjudications are not audit-logged (§14).
6. **`admin` role has no endpoints** — tenant/identity management per §14 is absent.
7. **Observability wiring** — `ivp_adapter_errors_total` has no production caller
   (alert `IVPAdapterErrorsHigh` can never fire); `ivp_full_fallback_total` only ever
   emits `FULL_REQUIRED` instead of real reason codes; tracing exporter is nil so no
   span is exported, and there are zero CLI/execution/audit spans (§15.2). Add the
   two missing alerts (unacknowledged possible-miss SLA, cross-tenant violation).
8. **Hardening** — `Retry-After` on 429; LRU/TTL rate-limit map; DB statement
   timeouts; unit-result pagination; per-endpoint roles in OpenAPI; OpenAPI↔types
   sync test (adopt `oapi-codegen` or a drift test).
   *Sync test resolved (Wave 1): ADR 0010 chooses a hand-maintained spec plus
   `internal/api/openapi_drift_test.go`, which asserts method+path parity in both
   directions via `chi.Walk` over `NewRouter(Options{})` — including the
   root-mounted health endpoints via path-level `servers`. Remaining open in this
   item: per-endpoint roles/security in the spec, `Retry-After`, rate-limit map
   hygiene, statement timeouts, pagination.*

### 3.5 Deployment, docs, governance

1. **ADRs required** (LLD §0.2: ambiguous choices must be documented): state-machine
   naming; offline planning disposition; Maven discovery without effective POM;
   Gradle task-typing heuristic; generic-adapter version constant; OpenAPI codegen
   strategy; schema-generation strategy. *All seven recorded in Wave 0/1:
   ADR 0005 (state-machine mapping), 0006 (offline planning disabled,
   full-suite-fallback only), 0007 (Maven discovery without effective POM), 0008
   (Gradle task-typing name heuristic), 0009 (generic adapter version constant),
   0010 (OpenAPI: hand-maintained + drift test), 0011 (config schema:
   hand-maintained + parity test). Wave 0 additionally produced ADR 0003
   (discovery report-path patterns) and ADR 0004 (mode-scoped audit sample
   rate); Wave 1 inventory review additionally produced ADR 0012 (invalid
   config fails closed without synthesizing a FULL_REQUIRED plan, LLD line
   272). Requirement met.*
2. **Docs:** threat model, release checklist, SLO measurement report with explicit
   deviations (§22), updated operations runbook (audit cadence, quarantine runbook),
   `docs/adapter-support.md` capability matrix refreshed after adapter fixes.
3. **Requirement inventory:** review remaining ~797 rows with code references and
   validation commands; resolve contradictory LLD criteria via ADR before marking.

---

## 4. Validation program (P2, LLD §17)

The local host cannot run Docker/PostgreSQL/native tools/race. **The strategy is
CI-first:** every blocked validation becomes a workflow on Linux runners, and the
local loop stays `gofmt + vet + test + build + tracker-check`.

| Suite | Design |
|---|---|
| 17.3 native integration | New `tests/integration/` with 7 ephemeral fixture repositories (Gradle multi-project, Maven reactor, Bazel graph, Nx workspace, pytest package, Jest multi-project, generic). Nightly workflow installs pinned toolchains (SDKMAN/npm/bazelisk) and runs injected-change scenarios proving direct + indirect selection and hard-rule escalation |
| 17.4 planner correctness | Extend `internal/analysis` tests with the full checklist (deletion/rename preservation, edge-addition monotonicity, contract expansion, determinism, zero-selection never passes) |
| 17.5 audit & safety | Fixtures: missed-test audit → quarantine; flaky → POSSIBLE_MISS; mismatched environment → INCOMPARABLE; **time alone never clears quarantine** (regression for P0-2); org checks not overridable; API-down fallback; upload retry never re-runs; payload leak scan |
| 17.6 DB/API | Migration-from-empty and from previous version; tenant isolation on every endpoint + joins; idempotency 409 path (currently untested); body/row limits, malformed JSON, pagination, rate limit, sanitized errors |
| 17.7 performance | Synthetic graphs at 1k/10k/100k units and 10k/50k/500k edges; diffs of 1/10/100/500 files; concurrent index updates + plan storms with backpressure assertions |
| 17.8 historical replay | Upgrade `tests/historical-replay` into the release gate for RISK_BASED_CI: chronological train/calibration/eval split, Wilson intervals, incomparable classification — **kept disabled until org thresholds are met on held-out repos** |
| 17.9 release gates | Already: unit, vet, race (CI), SBOM, govulncheck, Trivy, Cosign. Add: Helm render (`helm template` + kubeconform), Prometheus rule lint, Compose config, backup/restore execution in a job with a throwaway Postgres, container scan on every PR |

---

## 5. Delivery waves

Sequential gates; each wave ends with a green CI run and a tracker update. Waves 2–4
are parallelizable across agents because they touch disjoint packages.

| Wave | Scope | Exit gate |
|---|---|---|
| **0. Restore green build + safety fixes** | P0-1…P0-7 | Local `go test ./...` green; regression tests for every P0; §23 scenarios 11/12 covered by new tests |
| **1. Evidence & inventory** | Review all 853 rows with code refs; ADRs for ambiguities; schema↔defaults parity test | `generate-tracker.py --check` + no contradictory open criteria |
| **2. Functional completion** | §3.1 adapters, §3.2 planner, §3.3 execution, §3.4 items 1–3 (graph strategy, scheduled audits, calibration metrics) | New unit tests per item; fixture corpus + equivalent-outcome contract test green |
| **3. Validation program** | §4: integration/e2e/DB-API/security/performance suites + CI workflows | All suites pass on Linux runners; nightly native-tool workflow green |
| **4. Operations & release** | Tracing exporter + spans, alert completion, admin/privilege endpoints, supersede records, backup/restore gates, Helm/Prometheus validation, threat model, runbook, release checklist | §22 acceptance criteria each marked met or listed as an explicit deviation |
| **5. Acceptance** | Appendix D definition-of-done: run and report every test command, adapter version matrix, demo reports for each adapter, confirmed-miss quarantine demo, offline-fallback demo, TARGETED_PASS labelling demo, worktree-mutation demo, migration/restore evidence, SBOM + scan summary | Signed-off DoD checklist; RISK_BASED_CI remains disabled with calibration plan |

### Ordering rationale

- Wave 0 first because a broken build invalidates all other evidence and P0-2/P0-3
  are live safety violations (a quarantined repo silently resumes selective gating).
- Inventory (Wave 1) is bookkeeping that must not gate coding; it runs in parallel
  and supplies the traceability record §22 demands.
- Graph strategy and scheduled audits sit in Wave 2 because they change behavior the
  validation suites in Wave 3 must then test.
- Everything environment-blocked locally (Postgres, Docker, Helm, native tools, race)
  is deferred to CI-hosted waves rather than attempted on this host.

---

## 6. Working rules during execution

1. **Conservative default:** when the LLD is ambiguous, choose the behavior that
   reduces optimization, never verification, and record it in `docs/adr/`.
2. **No stubs:** every adapter/API touched must be fully implemented and tested;
   unsupported ecosystems fall back to the generic adapter or full-suite.
3. **Safety invariants to assert in tests, not just review:** mandatory checks never
   omit; invalid/stale evidence only lowers confidence; zero selected tests is never
   a pass; TARGETED_PASS is never displayed as "all tests passed"; results append-only;
   quarantine never clears on time alone.
4. **Commit hygiene:** never commit a state that fails `go build ./...` + `go vet` +
   `go test`; update `docs/implementation-log.md` and the inventory in the same wave.
5. **RISK_BASED_CI stays disabled** until Wave 5 calibration evidence and explicit
   maintainer approval exist (LLD §7.2, default disabled).

---

## 7. Summary of the gap (one paragraph)

*This section describes the gap as it stood on 2026-10-30, before the wave
plan below was executed. It is kept as the baseline of the plan; the closing
state is recorded below and in `docs/implementation-log.md`.*

The repository is a substantially complete modular monolith — every LLD subsystem
(domain, planner, 7 adapters, runner, API, persistence, audit, security, telemetry,
deployment) exists and is mostly faithful — but HEAD does not compile, two quarantine
defects violate the LLD's conservative-safety rule, several escalation inputs and
reason codes are dead in production paths, the adapter fixture/contract corpus and
the entire §17 native-integration/E2E/performance program are missing, the §13.3
graph-storage strategy and scheduled-audit orchestration are unimplemented, tracing
and two metric/alert wirings are inert, and ~797 of 853 inventory rows are unreviewed.
The five-wave plan above restores a green build and safety first, closes functional
gaps in parallel tracks, moves all environment-blocked validation into CI, and ends
with an evidence-backed Appendix-D definition-of-done.

### Closing state (2026-11-14)

The plan is executed. The inventory holds **853 rows reviewed, 0 `UNREVIEWED`,
0 `IN_PROGRESS`, 0 `NOT_STARTED`** — 476 `TESTED` and 377 `IMPLEMENTED`, an
implementation-coverage lower bound of 100.0% and a verified-completion lower
bound of 55.8%. The adapter fixture/contract corpus, the §13.3 graph strategy
(the derived index and the traversal bounds), the scheduled-audit orchestration,
the §10.6 calibration metrics, the alert wiring, the §17 validation program
(nightly native fixtures, performance and historical replay) and the tracing
exporter all exist and are asserted. Running the database-backed suites against
a real PostgreSQL — possible because the Docker daemon was provisioned on the
build host — exposed and closed three defects that the compile-only gate had
hidden: the migrations had never applied, the snapshot identity had drifted
from the code, and the row-level security policies were not forced. The
remaining environment-gated rows (the backup/restore drill, the native-toolchain
nightly job, the production availability measurement) are recorded as
`IMPLEMENTED` with their limitations, and the deviations are published in
`docs/slo-report.md`.
