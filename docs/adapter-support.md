# Adapter support contract

The production support matrix is Gradle, Maven, Bazel, Nx, pytest, Jest, and the generic command adapter. Each adapter must discover units, report its true selection/coverage precision, materialize only snapshot-known targets, parse native results, and reject missing, malformed, oversized, incompatible, or zero-test reports as successful outcomes.

| Adapter | Selection boundary | Evidence/report formats |
|---|---|---|
| Gradle | Test task, class/method only when task supports filters | JUnit XML, JaCoCo XML |
| Maven | Reactor module and provider-supported class/method filter | Surefire/Failsafe XML, JaCoCo XML |
| Bazel | Discovered canonical test labels | BEP and configured coverage formats |
| Nx | Discovered project/target/configuration | Configured Jest JSON/JUnit/LCOV or target parser |
| pytest | Exact collected node IDs or test file paths | JUnit XML, coverage.py XML/JSON and optional contexts |
| Jest | Exact paths and related-test candidate generation | Jest JSON/JUnit, LCOV |
| Generic | Explicit target and safe argv template only | Explicit mappings; no command inference |

Native-tool integration tests are required in Linux CI. Fixture/contract tests do not claim that a native integration ran.

