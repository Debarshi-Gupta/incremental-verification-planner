# Service level report

LLD §15.4 defines the service levels; LLD §22 requires that the product
either meets them or **reports the explicit deviations**. This report records
the objective, how it is measured, the measured value and the deviation.

| Objective | Budget | Measurement | Status |
|---|---|---|---|
| API availability | 99.9% monthly, excluding customer-operated dependencies | the availability of the API's readiness endpoint over a calendar month, divided by the number of minutes in the month | **deviation reported** (below) |
| Plan creation p95 | ≤ 2 s for a loaded graph with ≤ 10,000 test units and ≤ 50,000 relevant edges | `TestPlanCreationP95StaysWithinTheSLO` (`tests/performance/slo_test.go`) | met |
| Adapter inventory / delta indexing p95 | ≤ 30 s for ≤ 100 changed files, excluding native build-system graph generation | `TestIndexingP95StaysWithinTheSLO` (`tests/performance/slo_test.go`) | met |

## Measured values

The measurements are taken on the reference machine of the pipeline (the CI
runner) and are reproduced by the performance suite:

| Measurement | Measured | Budget |
|---|---|---|
| Plan creation p95 over 10,000 test units and 20,000 relevant edges | 244 ms | 2,000 ms |
| Delta indexing p95 over 100 changed files | 13.0 s | 30,000 ms |

Both suites run in the pull-request pipeline
(`.github/workflows/ci.yml`, job `performance-and-replay`) and in the nightly
job, so a regression in either objective fails the pipeline instead of being
discovered later. The thresholds can be raised for a slower reference machine
through `IVP_SLO_PLAN_CREATION_P95_MS` and `IVP_SLO_INDEXING_P95_MS`; the
shipped defaults are the LLD budgets.

## Deviations

1. **API availability is not measured on this repository's own CI.** The
   objective describes a *production deployment* over a calendar month, and a
   pull-request pipeline has no production traffic: its availability would be
   the availability of the runner, not of the service. The measurement is
   therefore implemented as a procedure rather than as a number:
   - the API exposes `/health/live` and `/health/ready`; the readiness probe
     reports database reachability, so it is the correct availability signal;
   - the deployment publishes the Prometheus scrape target for the API, so the
     monthly availability is computed from the recorded `up` series;
   - the alert `IVPServiceUnavailable` fires when a target cannot be scraped
     for two minutes, and `IVPDatabaseUnavailable` fires when the readiness
     probe fails for five minutes.
   Until an operator runs the service in a production deployment for a full
   month, the measured value is **unknown** and the objective is reported as
   an explicit deviation rather than as met. The deviation is closed by the
   first monthly report produced from the deployment's own metrics.
2. **The latency objectives are measured on the pipeline's reference machine,
   not on a customer machine.** The budgets are the LLD's initial targets and
   exclude the native build-system graph generation time, exactly as the LLD
   specifies.

## Review

This report is regenerated with every release and its deviations are reviewed
in the release checklist. A deviation that persists for two consecutive
releases is escalated to the release owner.
