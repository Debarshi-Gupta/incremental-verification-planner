package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RetentionResult reports deleted expiring artifact objects/metadata and raw
// test history. Audit records and aggregate safety outcomes are retained.
type RetentionResult struct {
	ArtifactsDeleted   int64 `json:"artifacts_deleted"`
	HistoryRowsDeleted int64 `json:"history_rows_deleted"`
}

const retentionTenantBatchSize = 100

// PruneExpired applies retention inside each tenant's RLS context. The
// maintenance process can enumerate tenant IDs, but it never disables RLS or
// performs an unscoped delete.
func (d *DB) PruneExpired(ctx context.Context, now, historyBefore time.Time) (RetentionResult, error) {
	return d.PruneExpiredWithArtifacts(ctx, now, historyBefore, nil)
}

// PruneExpiredWithArtifacts deletes the stored object before its metadata row.
// If object removal fails, the row remains so the next maintenance run retries
// it instead of losing the only reference to the object.
func (d *DB) PruneExpiredWithArtifacts(ctx context.Context, now, historyBefore time.Time, deleteObject func(context.Context, string) error) (RetentionResult, error) {
	var result RetentionResult
	if d == nil || d.Pool == nil {
		return result, errors.New("database unavailable")
	}
	if now.IsZero() || historyBefore.IsZero() || historyBefore.After(now) {
		return result, errors.New("retention timestamps are invalid")
	}
	var tenantCursor uuid.UUID
	for {
		tenants, err := d.retentionTenants(ctx, tenantCursor)
		if err != nil {
			return result, err
		}
		if len(tenants) == 0 {
			break
		}
		for _, tenant := range tenants {
			for {
				batch, err := d.expiredArtifactBatch(ctx, tenant, now)
				if err != nil {
					return result, err
				}
				if len(batch) == 0 {
					break
				}
				for _, item := range batch {
					if deleteObject != nil {
						if err := deleteObject(ctx, item.ObjectKey); err != nil {
							return result, err
						}
					}
					deleted, err := d.DeleteExpiredArtifactMetadata(ctx, item, now)
					if err != nil {
						return result, err
					}
					if deleted {
						result.ArtifactsDeleted++
					}
				}
			}

			var history int64
			err := d.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `DELETE FROM test_history WHERE tenant_id=$1 AND started_at < $2`, tenant, historyBefore)
				if err != nil {
					return err
				}
				history = tag.RowsAffected()
				return nil
			})
			if err != nil {
				return result, err
			}
			result.HistoryRowsDeleted += history
		}
		tenantCursor = tenants[len(tenants)-1]
		if len(tenants) < retentionTenantBatchSize {
			break
		}
	}
	return result, nil
}

func (d *DB) retentionTenants(ctx context.Context, after uuid.UUID) ([]uuid.UUID, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id FROM tenants WHERE id > $1 ORDER BY id LIMIT $2`, after, retentionTenantBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tenants []uuid.UUID
	for rows.Next() {
		var tenant uuid.UUID
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	return tenants, rows.Err()
}
