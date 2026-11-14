# Plan to 100% Implementation (LLD-complete)

**Date:** 2026-11-06 · **Status:** executed — complete (2026-11-14)
**Scope:** close the 101 inventory rows that are not yet IMPLEMENTED/TESTED
(75 IN_PROGRESS + 26 NOT_STARTED of 853). Testing coverage beyond implementation
completeness (the other 372 IMPLEMENTED-but-unasserted rows) is **out of scope**
for this plan and stays tracked as Wave 3 work in
`docs/implementation-completion-strategy.md` §4/§5.

**Result (2026-11-14):** the definition of done below is met.
`docs/requirements-inventory.json` shows **853 rows reviewed, 0 `UNREVIEWED`,
0 `IN_PROGRESS`, 0 `NOT_STARTED`** — 510 `TESTED` and 343 `IMPLEMENTED`, so the
implementation-coverage lower bound is **100.0%** and the verified-completion
lower bound is **59.8%**. The tracker
(`docs/implementation-tracker.md`, generated) and the dated entries in
`docs/implementation-log.md` carry the per-package evidence. The rows that
remain `IMPLEMENTED` rather than `TESTED` are the ones whose criterion needs an
operator environment (a production month of availability measurements, S3
recovery, a native-toolchain run); their limitations are recorded per row, and
the program that lifts them is tracked in
`docs/implementation-completion-strategy.md` §4.

**Definition of done:** `docs/requirements-inventory.json` shows
`IMPLEMENTED + TESTED = 853`, `IN_PROGRESS = 0`, `NOT_STARTED = 0`;
`scripts/generate-tracker.py --check` passes; local gate
(`gofmt` over `git ls-files '*.go'`, `go vet ./...`, `go vet -tags integration ./...`,
`go test ./... -count=1`, `go build ./...`) stays green after every package;
`docs/implementation-log.md` records each package; ambiguous LLD choices get an
ADR in `docs/adr/` before the row is marked.

---

## Work packages (execution order, 101 rows total)

Ordering rationale: fixtures (A) unblock adapter tests (D); producers (B) unblock
dormant-rule work in C; E–G are independent streams that can interleave; H–I need
CI; J is mostly environment-gated; K is the final reconciliation pass.

### WP-A — Fixture corpus & replay (13 rows) · §3.1 items 1–2, LLD §20 · sizing L

Build `internal/adapters/fixtures/` + repo `testdata/` (per-adapter × 11-category
matrix × two tool versions) and the equivalent-outcome contract test; real fixture
repositories for Gradle multi-project, Maven reactor (Surefire+Failsafe), Nx,
Bazel multi-package, pytest (conftest/coverage.py), Jest multi-project, generic
integration project, and an injectable-change repo; historical-replay corpus with
train/calibration/evaluation split and tree reconstruction.

- `IVP-17-3-a64e4d97a5`, `IVP-17-3-c0a7e16b83`, `IVP-17-3-505d587c63`,
  `IVP-17-3-231c121373`, `IVP-17-3-bc8538a00a`, `IVP-17-3-9a3e1e831c`,
  `IVP-17-3-bfa578e511`, `IVP-17-3-4b5958b676`
- `IVP-17-8-b40182f484`, `IVP-17-8-afbad6c53e`, `IVP-17-8-473e859722`,
  `IVP-17-8-0044156b18`
- `IVP-8-9-0c90419c6a`

**Depends on:** nothing. **Unblocks:** WP-D, WP-H.

### WP-B — Dormant producers (15 rows) · §3.2 items 2, 5, 6, 12 · sizing L

Emit the five missing graph edge types (`BUILT_FROM`, `CONFIGURES`,
`CONTRACT_CONSUMER`, `OWNED_BY`, `COLOCATED_WITH`) or ADR-defer each with LLD
justification; produce conflict/cycle flags or ADR why none can occur; wire
`IsGenerated` / `DetectedRiskLabels` / `BroadRefactor` classification in
`internal/cli/index.go`; emit the four missing reason codes
(`INTEGRATION_BOUNDARY`, `OUTSIDE_AFFECTED_TARGETS`, `EXCLUDED_BY_REVIEWED_POLICY`,
`ENVIRONMENT_MISMATCH`) or record the schema/algorithm-version change per
Appendix A; make `AUDIT_MISS` / `AUDIT_POSSIBLE_MISS` / `AUDIT_INCOMPARABLE`
assignable as execution outcomes.

- `IVP-4-4-5272a642b2`, `IVP-4-4-c07cfda714`, `IVP-4-4-d10360c18b`,
  `IVP-4-4-b8dcc3d843`, `IVP-4-4-3fb68cd04d`, `IVP-4-4-4f754381aa`
- `IVP-APPENDIX-A-f6b14b28d8`, `IVP-APPENDIX-A-07ff7ca869`,
  `IVP-APPENDIX-A-dab19765b4`, `IVP-APPENDIX-A-9561eb2bff`
- `IVP-APPENDIX-B-e1086ec7de`, `IVP-APPENDIX-B-04e643ccd6`,
  `IVP-APPENDIX-B-01bd3ba3f1`
- `IVP-APPENDIX-C-2384312418`, `IVP-17-4-c46190ee6a`

**Depends on:** nothing (can run parallel to WP-A). **Unblocks:** dormant-rule
assertions inside WP-C.

### WP-C — Planner completeness (11 rows) · §3.2 items 1, 3, 4, 7, 8, 13 · sizing L

Stage-9 explanation gains change summary, snapshot IDs, adapter/toolchain versions
and preserves `OmittedUnits` on escalation; execution ordering gains the
high-risk-consumer and integration/E2E tiers plus the candidate-union member;
hard rules get the too-many-affected-targets assertion and the org-reviewed
narrow-exception mechanism; criticality/severity become policy-configurable;
flaky classification gets the §7.3 minimum-observation gate; plan carries a
P50–P95 uncertainty interval consumed by the economic rule; optional SCIP/symbol
index decision (implement or ADR-defer — LLD marks it "may").

- `IVP-6-3-d987c30ca0`, `IVP-6-3-0af6cc6122`, `IVP-6-3-4f7130f911`,
  `IVP-6-3-fc4576327f`, `IVP-6-3-64477ace27`, `IVP-6-3-a145a03704`,
  `IVP-6-3-a3c4956810`, `IVP-6-3-e1060ab19f`, `IVP-6-3-afb679b66c`
- `IVP-6-2-fd59dc2ae2`, `IVP-7-3-ae7099457b`

**Depends on:** WP-B for the risk-label-dependent rows. **Unblocks:** WP-K meta rows.

### WP-D — Adapter correctness (10 rows) · §3.1 items 3–7, 9 · sizing M

Gradle task-*type* verification for `--tests` (honest capability per ADR 0008);
Maven `surefire_enabled`/`failsafe_enabled` enforcement (ADR 0007 final bullet);
Bazel `cquery`/configuration transitions + BEP skipped/flaky/timeout categories +
all report files; Jest `--projects` multi-project resolution; pytest coverage.py
per-test contexts; native-adapter config validation depth (report paths, coverage
paths, `FullSuiteArgv`); remove/use dead `domain.IsBinary`; SCIP enrichment
disposition (ties to `IVP-6-3-64477ace27`).

- `IVP-1-4-24ab0a4135`, `IVP-1-4-dab5c9f276`, `IVP-1-4-deb25fcb74`,
  `IVP-1-4-305624a705`, `IVP-1-4-5bf8cb3eaa`, `IVP-1-4-3c64516aac`,
  `IVP-1-4-1ee163f12a`
- `IVP-8-4-6f949872cd`, `IVP-8-7-fce12b10a9`, `IVP-4-3-b8c7109977`

**Depends on:** WP-A (fixtures for the new behaviors). **Unblocks:** WP-H contract
suites.

### WP-E — Persistence & graph strategy (6 rows) · §3.4 items 1–2 · sizing L

Snapshot-ingestion adjacency materialization, affected-target index, derived-index
cache keyed by snapshot hash + algorithm version, policy-driven traversal
node/depth limits that mark the graph incomplete (→ escalate) with
`domain.Policy` gaining the field, the missing `executions` index on
`(repository, status, started_at)`, and superseding correction records for
misparsed results (LLD §12.4).

- `IVP-13-3-a18d1ebe10`, `IVP-13-3-ca3c110287`, `IVP-13-3-52f080dd9b`
- `IVP-APPENDIX-C-ae232967e6`, `IVP-12-4-e5999c8ed3`, `IVP-2-2-be6eab19d1`

**Depends on:** nothing (parallel stream). **Unblocks:** §13.3 performance rows.

### WP-F — Audits, calibration metrics & alerts (10 rows) · §3.4 items 3, 4, 7 · sizing M

Scheduled-audit orchestration (`safety.audit.cadence`, due-run query,
maintenance-job entry point, alert wiring); the six §10.6 calibration metrics
(runtime/compute reduction, regression recall, selection precision,
fallback/escalation rate, discovery/parse error rates, adapter/class/version
breakdowns); the two missing alerts (unacknowledged possible-miss SLA,
cross-tenant violation) plus validation of the existing-rule coverage row.

- `IVP-10-2-4b9b41728a`
- `IVP-10-6-8fd0a9ef2e`, `IVP-10-6-3eaf80fcc1`, `IVP-10-6-8c3381be10`,
  `IVP-10-6-aae52ce0bc`, `IVP-10-6-e5112a94d2`, `IVP-10-6-52ce99fd9c`
- `IVP-15-3-3f1cdf5908`, `IVP-15-3-67ee91aa95`, `IVP-3-2-d83f3d50ab`

**Depends on:** WP-E for persisted run data where metrics read history.

### WP-G — Execution lifecycle & robustness (6 rows) · §3.3/§4 · sizing M

Mid-execution environment-fingerprint recompute/compare (§9.4); adapter-version
evidence invalidation; OBSERVE reset on algorithm/policy major change;
dependency-version keying in coverage history; one end-to-end
report→aggregate-result workflow test; artifact loss/failure independence
regression.

- `IVP-9-4-4f98f31c8e`, `IVP-18-4-4782df5725`, `IVP-18-4-58d757fbfb`,
  `IVP-23-00f4844fed`, `IVP-3-2-1422ad2d0d`, `IVP-2-2-7bb4cc9171`

**Depends on:** nothing (parallel stream).

### WP-H — Validation suites & CI workflows (5 rows) · §4 · sizing S

Exit-code table suite for 0–8 (incl. code-4 misclassification); unified
secret/leak regression across API errors, logs, telemetry; `schedule:` nightly
workflow; wire `tests/performance` + `tests/historical-replay` into the
validation program; resolve the "not all suites pass locally" row.

- `IVP-17-1-9521e66b4b`, `IVP-17-5-157d99d47a`, `IVP-17-9-8a454428dd`,
  `IVP-21-35daf17a72`, `IVP-22-92d7bbfe9e`

**Depends on:** WP-A, WP-D for fixture-backed cases. **Environment:** nightly
execution and performance/historical runs are CI-only.

### WP-I — SLO measurement (4 rows) · §3.5 item 2, LLD §15.4/§22 · sizing M

Build the SLO report: 99.9% monthly availability, plan-creation p95, indexing p95
(≤100 changed files), published per §15.4; explicit deviations documented.

- `IVP-15-4-a14daae2cc`, `IVP-15-4-15426b47b6`, `IVP-15-4-b98266eff5`,
  `IVP-22-a5e6c63745`

**Depends on:** WP-H (suites producing measurements) + CI evidence (**decision 1**).

### WP-J — Operations, docs & environment-gated rows (15 rows) · §3.5 · sizing M

Backup/restore validation (dump/restore check, restore verification, missing-object
reporting, independent S3 restore); Docker volume/build + Helm rendering
validation; repository-tree document; adapter sample outputs in
`docs/adapter-support.md`; security/SBOM doc wiring; artifact retrieval endpoint +
cross-tenant tests; artifact size/concurrency/retention bounds; PostgreSQL-backed
migration/FK/tenant-isolation and retention/delete-retry rows; adapter-granularity
expansion disposition (ADR 0008 already fixes the conservative behavior — decide
implement vs. document-as-designed).

- `IVP-18-3-b65b8be0e5`, `IVP-18-3-ac12a2467e`, `IVP-18-3-af67b18a0e`
- `IVP-18-1-7a9f81b583`, `IVP-APPENDIX-D-991e8cee94`, `IVP-APPENDIX-D-92cb96b0e4`,
  `IVP-APPENDIX-D-9db1236001`, `IVP-APPENDIX-D-aa66ce9159`
- `IVP-22-d499a7d3a4`, `IVP-22-cb087dc896`
- `IVP-13-1-8a4a472a33`, `IVP-13-2-3cb6725389`, `IVP-2-3-2e8fbb814e`
- `IVP-APPENDIX-C-80cc57ce08`, `IVP-15-3-1341698e42`

**Depends on:** environment (**decision 1**) for the PostgreSQL/Docker/Helm/S3 rows.

### WP-K — Meta-row reconciliation (6 rows) · sizing S

Summary rows close only when their underlying gaps close: section 0
(`IVP-0-0ecb23f302`, `IVP-0-37d497e851`, `IVP-0-974d9e5168`, `IVP-0-d69e5867da`)
and section 2.2 (`IVP-2-2-9e0530b7bc`, `IVP-2-2-30c0aa5286`).

**Depends on:** WP-A–WP-J complete. Final pass also re-runs the full local gate,
regenerates the tracker, and appends the Wave-2 log entry.

---

## Standard exit gate (every WP)

1. Local gate green: `gofmt -l` over `git ls-files '*.go'`, `go vet ./...`,
   `go vet -tags integration ./...`, `go test ./... -count=1`, `go build ./...`.
2. New/changed behavior has direct assertions (rows may land IMPLEMENTED first,
   TESTED in the same package when the assertion is the row's criterion).
3. Inventory updated through the batch workflow: batch JSON → field/exact-ID
   validator → `merge_updates.py` → totals check (853/853/0 expected).
4. `python scripts/generate-tracker.py --check` passes.
5. `docs/implementation-log.md` gains a dated entry with the validation table.
6. Ambiguous LLD choices resolved by ADR in `docs/adr/` **before** marking.
7. Nothing committed; no state that fails gates is ever written.

## Decisions (resolved)

- **Decision 1 — CI evidence credit. Resolved by provisioning locally
  (2026-11-14).** The Docker daemon was started on the build host and
  PostgreSQL 16 runs through testcontainers, so the database-backed suites
  execute here instead of being credited from CI. That immediately exposed
  three defects that had never been reachable — the migrations had never
  applied (goose split the dollar-quoted bodies), the snapshot identity had
  drifted from the code, and the row-level security policies were not forced
  and therefore never applied to the connecting role. The rows that still
  need an operator environment (the backup/restore drill against a real
  instance, the native-toolchain nightly job, the production availability
  measurement) are recorded as `IMPLEMENTED` with that limitation.
- **Decision 2 — implement vs. ADR-defer. Resolved (2026-11-12 → 2026-11-14).**
  The SCIP/symbol index is ADR-deferred (ADR 0016: no producer exists, so the
  consumer path is implemented and asserted instead), the org-reviewed narrow
  exception is implemented (ADR 0019), the adapter-granularity expansion is
  documented as designed because it is vacuous for candidates discovered at
  the supported granularity (ADR 0022), and the graph edge types without an
  LLD-mandated producer keep their conservative behavior.
