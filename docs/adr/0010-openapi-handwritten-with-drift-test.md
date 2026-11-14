# ADR 0010: Hand-written API types with a hand-maintained OpenAPI spec and a route drift test (codegen deferred)

## Status

Accepted

## Context

The LLD requires that "API input/output types must be declared in OpenAPI and generated from the same source as validation types" (LLD §12 line 789) and that "API schema: OpenAPI 3.1; checked-in generated client types are not hand-edited" (LLD §2.2 line 75). The strategy review left the codegen choice open: "per-endpoint roles in OpenAPI; OpenAPI↔types sync test (adopt `oapi-codegen` or a drift test)" (`docs/implementation-completion-strategy.md` §3.4.8), and §3.5.1 lists an "OpenAPI codegen strategy" ADR as required.

Current state: HTTP handlers are hand-written chi routes in `internal/api/server.go` (`NewRouter` at `server.go:206`, the `/api/v1` route group at `:240`, 21 API operations at `:251-271` plus root-mounted `/health/live` and `/health/ready` at `:221`/`:224` and the Prometheus `/metrics` exposition at `:236`); request/response types are hand-written Go structs in the `api` package; `api/openapi.yaml` is hand-maintained. `go.mod` contains no `oapi-codegen`, `kin-openapi`, or JSON-Schema dependency. Required roles are enforced in code at each route registration (`dispatch(opts, "…", …, []security.Role{…}, …)`), not in the spec.

Two LLD ambiguities surfaced while pinning this down:

1. **Health-endpoint mounting.** §12 line 789 says "All endpoints live under /api/v1", but the §12.1 endpoint table (lines 792-793) lists `GET /health/live` and `GET /health/ready` at the root. The code mounts them at the root, outside the `/api/v1` group.
2. **Outcome enum scope.** The spec's `ExecutionResult.outcome` enum (`api/openapi.yaml:281`) lists six values while `internal/domain/enums.go:163-165` defines nine; the three audit outcomes (`AUDIT_MISS`, `AUDIT_POSSIBLE_MISS`, `AUDIT_INCOMPARABLE`, LLD Appendix B lines 1365-1367) are currently rejected by the execution write path (`validOutcome`, `internal/persistence/plans_executions.go:476-483`) — tracked as strategy gap §3.2.6.

## Decision

- **Keep hand-written handlers and a hand-maintained `api/openapi.yaml` as the near-term strategy; enforce sync with a drift test, not codegen.** The drift test `internal/api/openapi_drift_test.go` walks the live router with `chi.Walk` over `NewRouter(Options{})` and asserts, in both directions, that the set of `METHOD path` operations equals the set documented in `api/openapi.yaml` (with each path item's effective server applied — the global `/api/v1` server, or the path-level `servers` override). A second test rejects unknown operation keys in the spec. Adding, removing, renaming, or remounting a route without updating the spec (or vice versa) fails `go test ./...`, which CI runs with `-race` (`.github/workflows/ci.yml:39`).
- **Health endpoints are root-mounted and the spec says so explicitly.** Per OpenAPI's Path Item Object `servers` field, `/health/live` and `/health/ready` now carry `servers: [{url: /}]` in the spec, matching `server.go:221-224` and the §12.1 table. This resolves the §12 contradiction conservatively: the endpoint table (more specific) wins over the §12 general sentence, and the versioned tenant API remains uniformly under `/api/v1`.
- **`/metrics` is deliberately outside the API contract.** It is Prometheus exposition on the operational port surface, not a tenant API; the drift test excludes it by name and the spec does not document it.
- **Roles stay code-enforced and out of the spec for now.** The drift test covers operation parity; adding per-endpoint `security`/role documentation to the spec remains open in the strategy gap register (§3.4.8) and is deferred with codegen (below).
- **The narrow `outcome` enum is kept until strategy gap §3.2.6 lands.** The spec must describe what the server actually accepts; while `validOutcome` rejects the three audit outcomes, widening the enum would document a behavior that does not exist. The enum extension and the persistence/API change must ship together (conservative: reject more input now).
- **Trigger to adopt `oapi-codegen` (revisit this ADR):** (a) a published external client/consumer of the API appears, (b) a second service reuses this spec, or (c) any response-shape drift bug escapes the drift test. At that point: generate server request/response types and validation from `api/openapi.yaml`, keep the hand-written route wiring (the drift test then also guards generated-vs-router parity), and never hand-edit generated files per LLD line 75.

## Alternatives considered

1. **Adopt `oapi-codegen` now** (LLD line 75's "generated client types are not hand-edited" reads as an instruction to generate). Rejected for now: there is no external client today, the handler layer's `dispatch` operation-string indirection would still be hand-written, and introducing generation would either rewrite the `api` package types wholesale (high-churn, unreviewable in an evidence wave) or generate a parallel unused set. The chosen drift test provides the sync guarantee the LLD actually needs (types and spec agree) without that churn; the generation trigger above records when to switch.
2. **Do nothing — keep the spec hand-maintained without a test.** Rejected: this is precisely the drift the strategy flagged (§3.4.8), and the health-mounting contradiction found while writing the test proves drift already occurred.
3. **Serve validation from the spec at runtime (e.g. `kin-openapi` request validation).** Rejected as a first step: Go structs plus hand-written validation already enforce the contract server-side, and runtime spec validation adds a dependency and per-request cost without closing any currently open gap. Re-evaluate together with codegen.
4. **Widen the outcome enum now to match `domain.ExecutionOutcome`.** Rejected: it would document acceptance of values the write path rejects (`plans_executions.go:476-483`), i.e. spec-vs-runtime drift of the exact kind this ADR exists to prevent.

## Consequences

`go test ./...` (locally and CI `-race`) is now the gate that keeps `api/openapi.yaml`, the chi router, and the operation set in agreement; any route change without a spec change fails the build. The §12 health-path contradiction is resolved in favor of the endpoint table and recorded here, so inventory rows covering health endpoints and spec mounting are no longer ambiguous. Known-open spec work is explicitly enumerated rather than lurking: per-endpoint role/security documentation and the outcome-enum extension both ship later (strategy §3.4.8 and §3.2.6 respectively) and each will require the drift/shape tests to be updated in the same change. The ADR's trigger list defines exactly when the "hand-maintained" half of this decision must be reconsidered, so the choice cannot silently rot into unmanaged divergence.
