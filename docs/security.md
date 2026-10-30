# Security and privacy

IVP treats repository content, filenames, tool output, and configuration as untrusted. API requests are size-limited and schema-validated. Commands use executable/argument arrays, workspace-contained working directories, explicit environment allowlists, timeouts, process-group cancellation, bounded output, and redaction. Repository source and full environment values are never sent to the control plane.

Production API authentication uses OIDC JWT validation for signature, issuer, audience, and expiry. Authorization is determined from server-controlled tenant membership. Repository configuration cannot remove organization-required checks, weaken hard escalation rules, or enable selective CI gating without organization policy permission.

Artifacts are optional and sanitized, bounded, tenant-scoped, and expire under configured retention. Prometheus labels avoid repository IDs, filenames, test names, commit hashes, and user IDs. Audit history is append-only. Deployments must use TLS, non-root containers, injected secrets, least privilege, database backups, SBOM publication, dependency/image scans, and signed releases.

