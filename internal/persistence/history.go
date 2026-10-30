package persistence

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ApplyTestHistory overlays comparable execution history onto the immutable
// snapshot units. Historical p95 duration is used as a conservative planning
// estimate; unrelated environment results are excluded.
func (d *DB) ApplyTestHistory(ctx context.Context, tenantID, repositoryID uuid.UUID, environment, testConfigDigest string, window time.Duration, units []domain.TestUnit) ([]domain.TestUnit, error) {
	result := append([]domain.TestUnit(nil), units...)
	if environment == "" || testConfigDigest == "" || window <= 0 || len(result) == 0 {
		return result, nil
	}
	cutoff := time.Now().UTC().Add(-window)
	byKey := make(map[string]int, len(result))
	keys := make([]string, 0, len(result))
	for i := range result {
		byKey[result[i].StableKey] = i
		keys = append(keys, result[i].StableKey)
	}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT stable_test_key,
			COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY duration_ms),0)::bigint,
			COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY duration_ms),0)::bigint,
			count(*), count(*) FILTER (WHERE outcome='FAIL'),
			count(*) FILTER (WHERE flake_classification='FLAKY')
			FROM test_history
			WHERE tenant_id=$1 AND repository_id=$2 AND environment_digest=$3
			AND test_config_digest=$4 AND outcome IN ('PASS','FAIL') AND duration_ms>0
			AND started_at >= $5 AND stable_test_key = ANY($6)
			GROUP BY stable_test_key`, tenantID, repositoryID, environment, testConfigDigest, cutoff, keys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var p50, p95, samples, failures, flaky int64
			if err := rows.Scan(&key, &p50, &p95, &samples, &failures, &flaky); err != nil {
				return err
			}
			i, ok := byKey[key]
			if !ok || samples <= 0 {
				continue
			}
			unit := &result[i]
			unit.DurationP50Millis = p50
			unit.DurationP95Millis = p95
			unit.DurationSampleCount = int(samples)
			unit.HistoricalFailures = int(failures)
			unit.FlakeRate = float64(flaky) / float64(samples)
			if p95 > unit.EstimatedMillis {
				unit.EstimatedMillis = p95
			}
		}
		return rows.Err()
	})
	return result, err
}
