package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (d *DB) LoadSnapshot(ctx context.Context, tenantID, repositoryID, snapshotID uuid.UUID) (SnapshotBundle, error) {
	bundle := SnapshotBundle{TenantID: tenantID}
	var s domain.RepositorySnapshot
	var graphVersion int64
	var completeness []byte
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id,repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,adapter_digest,graph_version,completeness_json,created_at
			FROM repository_snapshots WHERE tenant_id=$1 AND repository_id=$2 AND id=$3`, tenantID, repositoryID, snapshotID).Scan(&s.ID, &s.RepositoryID, &s.CommitSHA, &s.BaseSHA, &s.TreeHash, &s.WorktreeHash, &s.SubmoduleDigest, &s.ToolchainDigest, &s.BuildConfigDigest, &s.AdapterSetDigest, &graphVersion, &completeness, &s.EvidenceGeneratedAt); err != nil {
			return err
		}
		s.GraphVersion = graphVersion
		var complete struct {
			Complete bool     `json:"complete"`
			Reasons  []string `json:"reasons"`
		}
		if err := json.Unmarshal(completeness, &complete); err != nil {
			return fmt.Errorf("decode snapshot completeness: %w", err)
		}
		s.IsComplete = complete.Complete
		s.CompletenessReasons = complete.Reasons
		bundle.Snapshot = s
		rows, err := tx.Query(ctx, `SELECT id,snapshot_id,stable_key,node_type,path,display_name,confidence,evidence_source,observed_at,valid_until,attributes_json FROM graph_nodes WHERE tenant_id=$1 AND snapshot_id=$2 ORDER BY stable_key`, tenantID, snapshotID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n domain.GraphNode
			var attrs []byte
			if err := rows.Scan(&n.ID, &n.SnapshotID, &n.StableKey, &n.NodeType, &n.Path, &n.DisplayName, &n.Confidence, &n.EvidenceSource, &n.ObservedAt, &n.ValidUntil, &attrs); err != nil {
				rows.Close()
				return err
			}
			var payload struct {
				Labels     []string       `json:"labels"`
				Attributes map[string]any `json:"attributes"`
			}
			if err := json.Unmarshal(attrs, &payload); err != nil {
				rows.Close()
				return err
			}
			n.Labels = payload.Labels
			n.Attributes = payload.Attributes
			bundle.Nodes = append(bundle.Nodes, n)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,snapshot_id,from_node_id,to_node_id,edge_type,confidence,evidence_source,observed_at,valid_until,attributes_json FROM graph_edges WHERE tenant_id=$1 AND snapshot_id=$2 ORDER BY from_node_id,to_node_id,edge_type`, tenantID, snapshotID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e domain.GraphEdge
			var attrs []byte
			if err := rows.Scan(&e.ID, &e.SnapshotID, &e.FromNodeID, &e.ToNodeID, &e.EdgeType, &e.Confidence, &e.EvidenceSource, &e.ObservedAt, &e.ValidUntil, &attrs); err != nil {
				rows.Close()
				return err
			}
			var payload struct {
				Attributes map[string]any `json:"attributes"`
				Conflict   bool           `json:"conflict"`
			}
			if err := json.Unmarshal(attrs, &payload); err != nil {
				rows.Close()
				return err
			}
			e.Attributes = payload.Attributes
			e.Conflict = payload.Conflict
			bundle.Edges = append(bundle.Edges, e)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,snapshot_id,stable_key,unit_type,adapter,module_key,mandatory,risk_labels_json,selectable,granularity,metadata_json FROM test_units WHERE tenant_id=$1 AND snapshot_id=$2 ORDER BY stable_key`, tenantID, snapshotID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u domain.TestUnit
			var risks, metadata []byte
			if err := rows.Scan(&u.ID, &u.SnapshotID, &u.StableKey, &u.UnitType, &u.Adapter, &u.ModuleKey, &u.Mandatory, &risks, &u.Selectable, &u.SelectionGranularity, &metadata); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(risks, &u.RiskLabels); err != nil {
				rows.Close()
				return err
			}
			var m struct {
				Metadata           map[string]any `json:"metadata"`
				TargetRef          string         `json:"target_ref"`
				EstimatedMillis    int64          `json:"estimated_millis"`
				DurationP50        int64          `json:"duration_p50_millis"`
				DurationP95        int64          `json:"duration_p95_millis"`
				SampleCount        int            `json:"duration_sample_count"`
				FlakeRate          float64        `json:"flake_rate"`
				LastSeenAt         time.Time      `json:"last_seen_at"`
				Granularity        string         `json:"granularity"`
				ExecutionGroup     string         `json:"execution_group"`
				SourcePaths        []string       `json:"source_paths"`
				HistoricalFailures int            `json:"historical_failures"`
			}
			if err := json.Unmarshal(metadata, &m); err != nil {
				rows.Close()
				return err
			}
			u.Metadata = m.Metadata
			u.TargetRef = m.TargetRef
			u.EstimatedMillis = m.EstimatedMillis
			u.DurationP50Millis = m.DurationP50
			u.DurationP95Millis = m.DurationP95
			u.DurationSampleCount = m.SampleCount
			u.FlakeRate = m.FlakeRate
			u.LastSeenAt = m.LastSeenAt
			u.SelectionGranularity = m.Granularity
			u.ExecutionGroup = m.ExecutionGroup
			u.SourcePaths = m.SourcePaths
			u.HistoricalFailures = m.HistoricalFailures
			bundle.TestUnits = append(bundle.TestUnits, u)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT u.stable_key,n.stable_key,n.path,c.line_start,c.line_end,c.source,c.report_hash,c.run_id,c.observed_at,c.environment_digest,c.build_config_digest,c.test_config_digest,c.precision,c.suite_level,c.test_specific,c.evidence_confidence
			FROM coverage_observations c JOIN test_units u ON u.id=c.test_unit_id AND u.snapshot_id=c.snapshot_id JOIN graph_nodes n ON n.id=c.covered_node_id AND n.snapshot_id=c.snapshot_id
			WHERE c.tenant_id=$1 AND c.snapshot_id=$2 ORDER BY u.stable_key,n.stable_key,c.observed_at`, tenantID, snapshotID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c domain.CoverageObservation
			var run string
			if err := rows.Scan(&c.TestUnitKey, &c.CoveredStableKey, &c.CoveredPath, &c.LineStart, &c.LineEnd, &c.SourceType, &c.ReportHash, &run, &c.ObservedAt, &c.EnvironmentFingerprint, &c.BuildConfigDigest, &c.TestConfigDigest, &c.Precision, &c.SuiteLevel, &c.TestSpecific, &c.EvidenceConfidence); err != nil {
				rows.Close()
				return err
			}
			if run != "" {
				if id, err := uuid.Parse(run); err == nil {
					c.RunID = id
				}
			}
			bundle.Coverage = append(bundle.Coverage, c)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		return nil
	})
	return bundle, err
}
