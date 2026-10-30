-- name: CreateRepository :one
INSERT INTO repositories (tenant_id, slug, canonical_url, default_branch)
VALUES ($1, $2, $3, $4)
RETURNING id, tenant_id, slug, canonical_url, default_branch, created_at, updated_at;

-- name: GetRepository :one
SELECT id, tenant_id, slug, canonical_url, default_branch, created_at, updated_at
FROM repositories
WHERE tenant_id = $1 AND id = $2;

-- name: ListRepositories :many
SELECT id, tenant_id, slug, canonical_url, default_branch, created_at, updated_at
FROM repositories
WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id > $2)
ORDER BY id
LIMIT $3;

-- name: ResolveMembership :one
SELECT u.id AS user_id, tm.tenant_id, tm.role, u.disabled_at
FROM users u
JOIN tenant_memberships tm ON tm.user_id = u.id
WHERE u.oidc_subject = $1 AND tm.tenant_id = $2;

-- name: GetActivePolicy :one
SELECT policy_json, version, policy_hash
FROM repository_policies
WHERE tenant_id = $1 AND repository_id = $2 AND is_active;

