# ADR 0001: Sanitized report artifact storage

**Status:** Accepted  
**Date:** 2026-10-30

## Context

The LLD requires optional, bounded, tenant-scoped report objects with a filesystem backend for single-node installations and an S3-compatible backend for distributed installations. Artifacts must not affect plan correctness, and raw logs or source content must not become a default upload path.

## Decision

- Accept only JSON and JUnit-style XML test reports through the API. Reject plain-text uploads and malformed reports.
- Cap each object at 16 MiB, allow at most two concurrent uploads per API process, and expire objects after 90 days by default.
- Redact credential-like values, ANSI/control data, source paths, free-form failure text, command/output fields, and unknown JSON/XML fields before storage. Treat this as best-effort sanitization, not permission to upload arbitrary logs.
- Generate object keys from tenant and artifact UUIDs. Persist the execution association, digest, media type, size, sanitization state, and expiry in PostgreSQL. Check tenant scope again during retrieval.
- Use an owner-controlled filesystem directory for single-node deployments and an S3 Signature Version 4 compatible path-style endpoint over TLS for distributed deployments.
- Delete expired objects before removing their metadata. A failed object deletion leaves metadata for a later retry. Missing optional objects never alter plan decisions.

## Consequences

Structured report detail remains available while source paths and free-form failure text are removed. Deployments that need larger reports must change the explicit size limit and retain bounded concurrent uploads. Filesystem deployments must provide persistent storage; scaled Helm deployments use S3-compatible storage.
