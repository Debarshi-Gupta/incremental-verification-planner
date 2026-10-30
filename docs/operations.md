# Operations runbook

## Configuration and startup

Set `IVP_DATABASE_URL`, `IVP_OIDC_ISSUER`, `IVP_OIDC_AUDIENCE`, and `IVP_OIDC_CLIENT_ID` through an environment/secret mount. Production startup must fail closed if OIDC or database configuration is absent. Use `.env.example` only for local placeholders. Configure organization membership in the database before granting access.

Configure `IVP_POLICY_SIGNING_PRIVATE_KEY` as a base64 encoded Ed25519 private key in the API secret store, and pin its matching base64 encoded public key as `IVP_POLICY_VERIFY_PUBLIC_KEY` in every CLI/CI executor environment. The API marks a policy trusted only when it can sign the repository, version, and canonical content hash. The CLI verifies that signature against the pinned key before planning or selective execution; missing or mismatched key material forces the configured full-suite fallback. Rotate keys by distributing the new public key to clients before switching the API signing key.

Record accepted-change outcomes with `ivp outcome --change-key <stable-id> --started-at <RFC3339> --accepted-at <RFC3339> --accepted-correct --compute-ms <n> --cost-microusd <n> --human-correction-minutes <n>`. Outcomes are append-only, can reference a completed IVP execution, and contribute to rolling repository time and cost averages only when marked accepted-correct.

For local Compose development, `.env.example` enables `IVP_DEV_AUTH=1` only while `IVP_ENV=development`. The API applies migrations and creates the configured local tenant/user in this mode. The development authenticator accepts only its configured `X-Tenant-ID`; never enable it in production.

## Database

Run forward-only Goose migrations before starting API replicas. Readiness stays false if migrations or database checks fail. The Helm retention CronJob runs `ivp maintenance retention`, deleting expired report objects before their metadata and removing raw test history older than `IVP_RAW_HISTORY_RETENTION_DAYS` (90 days by default); it retains audit and aggregate safety records. Back up PostgreSQL daily and configure WAL/PITR according to the operator's recovery objectives. Test restore quarterly and on major releases. Missing optional artifacts do not invalidate stored plan metadata; record that loss in the report.

## Report artifacts

The API accepts bounded JSON and XML test reports at `POST /api/v1/executions/{execution_id}/artifacts` (16 MiB maximum, two concurrent uploads per API process); arbitrary logs are not accepted. IVP redacts ANSI controls, common credential patterns, and other control characters before storage, then returns an opaque artifact ID, size, digest, and expiry. Authenticated tenant members with reader-or-higher access retrieve an unexpired object through `GET /api/v1/artifacts/{artifact_id}`; cross-tenant IDs are returned as unavailable. Upload requires executor role and an execution belonging to the same tenant.

Use `IVP_ARTIFACT_BACKEND=filesystem` and `IVP_ARTIFACT_DIR` for a single-node deployment. The directory must be on a persistent, access-controlled volume. Use `IVP_ARTIFACT_BACKEND=s3` for distributed deployments and configure `IVP_S3_ENDPOINT`, `IVP_S3_REGION`, `IVP_S3_BUCKET`, `IVP_S3_ACCESS_KEY`, and `IVP_S3_SECRET_KEY`; optional session credentials use `IVP_S3_SESSION_TOKEN`. The endpoint must use HTTPS outside local development. In Helm, filesystem mode uses the artifact PVC and one replica; multi-replica deployments require S3-compatible storage and network-policy egress to that endpoint.

## Incidents

- Audit miss: quarantine the affected repository/adapter/rule scope immediately, force full verification, preserve the incident and require maintainer acknowledgement plus one clean comparable audit before release.
- API or policy outage: do not execute a stale selective plan. Run configured full verification and preserve a bounded local outbox report for idempotent upload.
- Migration failure: keep readiness false; stop rollout and restore from the tested backup if rollback is needed.
- Cross-tenant access: page the security owner, preserve audit records, revoke affected tokens, and investigate all scoped queries.

## Dashboards and alerts

Import `deploy/observability/ivp-dashboard.json` into Grafana and load `deploy/observability/ivp-alerts.yaml` into Prometheus rule evaluation. The rules use emitted IVP counters plus `kube-state-metrics` for retention Job status. Tune thresholds against local traffic before paging; audit misses are always critical because they indicate the planner omitted a failing test. The dashboard intentionally uses only bounded labels emitted by the service and contains no repository identifiers, paths, or test names.

Tagged releases publish the API image only after the Trivy scan passes, then sign and verify the immutable registry digest with Cosign keyless identity from the release workflow. Production Helm values must use that digest and consumers should verify the Cosign certificate identity against this repository's release workflow before deployment.

## Backup and recovery

Back up policies, snapshots, graph evidence, executions, audit records, quarantines, and migrations with the database. Back up filesystem artifacts or the S3 bucket separately while retaining tenant-scoped object keys. Restore both stores independently into an isolated instance, run migrations, verify tenant boundaries, object digests, and representative plan/audit records, then promote only after checks pass.

For a production recovery, first stop or scale API writers to zero and preserve the failed database and artifact store read-only. Restore the latest full PostgreSQL backup to a separate instance, replay archived WAL to the incident recovery point, then run the PostgreSQL schema/tenant check before updating the production database endpoint. Restore filesystem artifacts or the S3 bucket independently and compare object digests with artifact metadata; missing optional reports must remain marked unavailable in execution reports. Restore signing and OIDC secrets from the secret manager, start one API replica, confirm readiness and tenant-scoped reads, then restore normal replica count. Record the recovered timestamp and data loss window. Set and document the deployment's RPO/RTO in the database operator configuration; IVP does not choose organization-wide recovery objectives.

For filesystem artifacts, `./scripts/verify-artifact-backup.ps1 -ArtifactDirectory <mounted-artifact-directory> -BackupPath <new-archive.zip>` creates a new archive, restores it to a temporary directory, and compares every object path, size, and SHA-256 digest. It refuses to overwrite an existing backup. For S3-compatible storage, use the provider's versioned backup or replication workflow and verify restored object digests against the PostgreSQL metadata.

On a PostgreSQL host with the client tools installed, run `./scripts/verify-postgres-backup.ps1 -DatabaseUrl $env:IVP_DATABASE_URL` to create a custom-format dump, restore it to a uniquely named temporary database on the same server, and verify the IVP schema before removing only that temporary database. Run it against a non-production replica or backup-validation instance with a role permitted to create and drop databases. This verifies database dump/restore; it does not verify artifact-store recovery or point-in-time recovery.

