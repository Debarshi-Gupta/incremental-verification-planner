package persistence

import (
	"context"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

// LoadCoverageHistory returns the newest compatible, repository-owned
// coverage observation for each stable test-unit/source-file pair. Historical
// observations are attached to the current evidence view by canonical path;
// they remain suite-level unless the originating runner provided per-test
// contexts.
func (d *DB) LoadCoverageHistory(ctx context.Context, tenantID, repositoryID uuid.UUID, environmentDigest, buildConfigDigest, testConfigDigest string, unitKeys []string) ([]domain.CoverageObservation, error) {
	if environmentDigest == "" || buildConfigDigest == "" || testConfigDigest == "" || len(unitKeys) == 0 {
		return nil, nil
	}
	observations := make([]domain.CoverageObservation, 0)
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (u.stable_key,n.path)
			u.stable_key,n.stable_key,n.path,c.line_start,c.line_end,c.source,c.report_hash,c.run_id,c.observed_at,
			c.environment_digest,c.build_config_digest,c.test_config_digest,c.precision,c.suite_level,c.test_specific,c.evidence_confidence
			FROM coverage_observations c
			JOIN test_units u ON u.id=c.test_unit_id AND u.snapshot_id=c.snapshot_id
			JOIN graph_nodes n ON n.id=c.covered_node_id AND n.snapshot_id=c.snapshot_id
			JOIN repository_snapshots s ON s.id=c.snapshot_id AND s.tenant_id=c.tenant_id
			WHERE c.tenant_id=$1 AND s.repository_id=$2 AND c.environment_digest=$3 AND c.build_config_digest=$4 AND c.test_config_digest=$5
			AND c.observed_at >= now()-interval '180 days' AND u.stable_key=ANY($6) AND n.node_type='SOURCE_FILE'
			ORDER BY u.stable_key,n.path,c.observed_at DESC`, tenantID, repositoryID, environmentDigest, buildConfigDigest, testConfigDigest, unitKeys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var observation domain.CoverageObservation
			var runID string
			if err := rows.Scan(&observation.TestUnitKey, &observation.CoveredStableKey, &observation.CoveredPath, &observation.LineStart, &observation.LineEnd,
				&observation.SourceType, &observation.ReportHash, &runID, &observation.ObservedAt, &observation.EnvironmentFingerprint,
				&observation.BuildConfigDigest, &observation.TestConfigDigest, &observation.Precision, &observation.SuiteLevel,
				&observation.TestSpecific, &observation.EvidenceConfidence); err != nil {
				return err
			}
			parsed, err := uuid.Parse(runID)
			if err != nil {
				continue
			}
			observation.RunID = parsed
			observations = append(observations, observation)
		}
		return rows.Err()
	})
	return observations, err
}
