package persistence

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ArtifactMetadata struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	ExecutionID uuid.UUID `json:"execution_id"`
	ObjectKey   string    `json:"-"`
	MediaType   string    `json:"media_type"`
	SizeBytes   int64     `json:"size_bytes"`
	SHA256      string    `json:"sha256"`
	Sanitized   bool      `json:"sanitized"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type ExpiredArtifact struct {
	TenantID  uuid.UUID
	ID        uuid.UUID
	ObjectKey string
}

const expiredArtifactBatchSize = 250

func (d *DB) SaveArtifact(ctx context.Context, actorID uuid.UUID, artifact ArtifactMetadata) error {
	if err := validateArtifactMetadata(artifact); err != nil {
		return err
	}
	return d.TenantTx(ctx, artifact.TenantID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM executions WHERE tenant_id=$1 AND id=$2)`, artifact.TenantID, artifact.ExecutionID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("artifact execution does not belong to this tenant")
		}
		_, err := tx.Exec(ctx, `INSERT INTO artifacts(id,tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, artifact.ID, artifact.TenantID, artifact.ExecutionID, artifact.ObjectKey, artifact.MediaType, artifact.SizeBytes, artifact.SHA256, artifact.Sanitized, artifact.ExpiresAt, artifact.CreatedAt)
		if err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, artifact.TenantID, actorID, "artifact.upload", "execution", artifact.ExecutionID.String(), map[string]any{"artifact_id": artifact.ID.String(), "size_bytes": artifact.SizeBytes, "sha256": artifact.SHA256, "media_type": artifact.MediaType})
	})
}

func validateArtifactMetadata(item ArtifactMetadata) error {
	if item.ID == uuid.Nil || item.TenantID == uuid.Nil || item.ExecutionID == uuid.Nil || !item.Sanitized || item.CreatedAt.IsZero() || item.ExpiresAt.IsZero() || !item.ExpiresAt.After(item.CreatedAt) || item.ExpiresAt.After(item.CreatedAt.Add(365*24*time.Hour)) {
		return errors.New("artifact metadata is incomplete or unsafe")
	}
	wantKey := item.TenantID.String() + "/" + item.ID.String()
	if item.ObjectKey != wantKey {
		return errors.New("artifact object key must be scoped to its tenant and artifact identifiers")
	}
	if item.MediaType != "application/json" && item.MediaType != "application/xml" && item.MediaType != "text/xml" {
		return errors.New("artifact media type is unsupported")
	}
	if item.SizeBytes <= 0 || item.SizeBytes > 64<<20 {
		return errors.New("artifact size is outside the allowed range")
	}
	digest, err := hex.DecodeString(item.SHA256)
	if err != nil || len(digest) != 32 {
		return errors.New("artifact SHA-256 digest is invalid")
	}
	return nil
}

func (d *DB) GetArtifact(ctx context.Context, tenantID, artifactID uuid.UUID) (ArtifactMetadata, error) {
	var artifact ArtifactMetadata
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id,tenant_id,execution_id,object_key,media_type,size_bytes,sha256,sanitized,expires_at,created_at
			FROM artifacts WHERE tenant_id=$1 AND id=$2`, tenantID, artifactID).
			Scan(&artifact.ID, &artifact.TenantID, &artifact.ExecutionID, &artifact.ObjectKey, &artifact.MediaType, &artifact.SizeBytes, &artifact.SHA256, &artifact.Sanitized, &artifact.ExpiresAt, &artifact.CreatedAt)
	})
	return artifact, err
}

func (d *DB) expiredArtifactBatch(ctx context.Context, tenant uuid.UUID, now time.Time) ([]ExpiredArtifact, error) {
	var expired []ExpiredArtifact
	err := d.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,object_key FROM artifacts WHERE tenant_id=$1 AND expires_at <= $2 ORDER BY expires_at,id LIMIT $3`, tenant, now, expiredArtifactBatchSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ExpiredArtifact
			item.TenantID = tenant
			if err := rows.Scan(&item.ID, &item.ObjectKey); err != nil {
				return err
			}
			expired = append(expired, item)
		}
		return rows.Err()
	})
	return expired, err
}

func (d *DB) DeleteExpiredArtifactMetadata(ctx context.Context, item ExpiredArtifact, now time.Time) (bool, error) {
	var deleted bool
	err := d.TenantTx(ctx, item.TenantID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM artifacts WHERE tenant_id=$1 AND id=$2 AND object_key=$3 AND expires_at <= $4`, item.TenantID, item.ID, item.ObjectKey, now)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected() == 1
		return nil
	})
	return deleted, err
}
