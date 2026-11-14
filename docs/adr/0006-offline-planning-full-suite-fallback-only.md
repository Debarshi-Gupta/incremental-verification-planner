# ADR 0006: Offline planning stays disabled; offline execution is full-suite fallback only

## Status

Accepted

## Context

The LLD allows local planning only on trusted evidence: full-verification fallback is mandatory when the server or policy cannot be trusted (LLD line 19), the planner must fall back when no valid, signed local policy snapshot is available (LLD line 444, rule 10), and the CLI may produce a locally computed full plan when the API is unavailable "unless the policy snapshot is locally signed, unexpired and known compatible with the same algorithm version" (LLD line 1233) — with acceptance tests for expired-snapshot fallback (LLD lines 1283, 1439).

What exists in code:

- Planning is API-only: `ivp plan` fetches the repository, the signed policy, and the plan from the API (`internal/cli/plan.go:76-111`); there is no code path that computes a selective plan locally.
- Policy signature verification exists only for API-fetched envelopes: `getTrustedPolicy` verifies `trusted`, `Ed25519`, hash, and signature against `IVP_POLICY_VERIFY_PUBLIC_KEY` (`internal/cli/client.go:40-74`, `internal/security/policy_signature.go:61`), and fails closed to the full-suite fallback (`client.go:55`, `client.go:63`).
- There is no local policy snapshot file reader/writer anywhere: nothing persists a signed snapshot to disk for later offline use, and nothing loads one.
- `execution.ValidateOfflinePolicy` (`internal/execution/outbox.go:276-290`) implements the snapshot trust/expiry/algorithm-version checks but is called only by its own test (`internal/execution/outbox_test.go:49-63`) — no production caller.
- `server.offline_policy` is locked to `FULL_SUITE_FALLBACK` by validation (`internal/config/validate.go:42-44`) and by the precedence rules (`internal/config/precedence.go:67`), so no other offline mode can be configured.
- When planning cannot be trusted, the CLI runs only the locally configured full-suite argv (`runOfflineFullSuite`, `internal/cli/run.go:316-324`) and records `OFFLINE_COMPLETE` (`run.go:381`); with no configured argv it prints fallback instructions and exits non-zero (`run.go:321-323`).

## Decision

- Local selective planning is intentionally disabled. While no signed, unexpired, algorithm-compatible snapshot is stored and loaded from disk, the CLI never computes a selective plan without the API; the only offline behaviour is the configured full-suite fallback (the conservative reading of LLD lines 19/444/1233).
- The signature verification code (`security.VerifyPolicyEnvelope`, `client.getTrustedPolicy`) stays the single trust path for policy material, and it stays fail-closed.
- `execution.ValidateOfflinePolicy` remains as the tested predicate that a future snapshot loader must call; wiring it up is deferred until snapshot distribution exists (tracked in `docs/implementation-completion-strategy.md` §3.2.11).
- `server.offline_policy` keeps accepting only `FULL_SUITE_FALLBACK`, so the configuration surface cannot imply an offline capability the CLI does not have.

## Alternatives considered

1. Implement offline planning with a locally stored signed snapshot now. Rejected for this phase: snapshot distribution, expiry refresh, key rotation, algorithm-version compatibility checks, and tamper handling are all undefined in code today; shipping a partial version would let the CLI gate selectively on stale organization policy — the opposite of the conservative behaviour LLD §0 requires.
2. Trust an unsigned local snapshot or the last-seen policy cache. Rejected: LLD line 444 requires a *signed* snapshot, and a cached unsigned policy could silently authorize selective CI after a policy change.
3. Remove `ValidateOfflinePolicy` as dead code. Rejected: it encodes the trust rules the acceptance criteria (LLD lines 1283, 1439) will need, and deleting it would discard verified logic rather than resolve the gap.

## Consequences

Offline runs are always full verification, so availability is reduced when the API is down (no selective speed-up), but correctness is preserved: a selective plan can never be produced from untrusted evidence. The dead-code character of `ValidateOfflinePolicy` is now an acknowledged, deliberate state rather than an accident, and requirement rows covering offline planning are reviewed with this ADR as their reference. Enabling offline planning later requires a snapshot storage/loader that calls `ValidateOfflinePolicy` and compares `AlgorithmVersion` with the planner's version before any selective decision.
