package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
	"github.com/jackc/pgx/v5"
)

type RepositoryPage struct {
	Items      []domain.Repository `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

func (d *DB) RegisterRepository(ctx context.Context, tenantID, actorID uuid.UUID, repo domain.Repository) (domain.Repository, error) {
	if repo.Slug == "" || strings.TrimSpace(repo.CanonicalURL) == "" {
		return repo, errors.New("repository slug and canonical URL are required")
	}
	if u, err := url.ParseRequestURI(repo.CanonicalURL); err != nil || (u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "file") {
		return repo, errors.New("canonical URL must be an absolute https, ssh, or file URL")
	}
	if repo.DefaultBranch == "" {
		repo.DefaultBranch = "main"
	}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO repositories(tenant_id,slug,canonical_url,default_branch)
			VALUES($1,$2,$3,$4) RETURNING id,tenant_id,created_at,updated_at`, tenantID, repo.Slug, repo.CanonicalURL, repo.DefaultBranch).
			Scan(&repo.ID, &repo.TenantID, &repo.CreatedAt, &repo.UpdatedAt)
		if err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, tenantID, actorID, "repository.register", "repository", repo.ID.String(), nil)
	})
	return repo, err
}

func (d *DB) ListRepositories(ctx context.Context, tenantID uuid.UUID, limit int, after string) (RepositoryPage, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	page := RepositoryPage{Items: make([]domain.Repository, 0)}
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,slug,canonical_url,default_branch,created_at,updated_at
			FROM repositories WHERE tenant_id=$1 AND ($2='' OR id::text>$2) ORDER BY id LIMIT $3`, tenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var repo domain.Repository
			if err := rows.Scan(&repo.ID, &repo.TenantID, &repo.Slug, &repo.CanonicalURL, &repo.DefaultBranch, &repo.CreatedAt, &repo.UpdatedAt); err != nil {
				return err
			}
			page.Items = append(page.Items, repo)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextCursor = page.Items[len(page.Items)-1].ID.String()
		}
		return nil
	})
	return page, err
}

func (d *DB) GetRepository(ctx context.Context, tenantID, repositoryID uuid.UUID) (domain.Repository, error) {
	var repo domain.Repository
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id,tenant_id,slug,canonical_url,default_branch,created_at,updated_at FROM repositories WHERE tenant_id=$1 AND id=$2`, tenantID, repositoryID).
			Scan(&repo.ID, &repo.TenantID, &repo.Slug, &repo.CanonicalURL, &repo.DefaultBranch, &repo.CreatedAt, &repo.UpdatedAt)
	})
	return repo, err
}

func (d *DB) SavePolicy(ctx context.Context, tenantID, actorID, repositoryID uuid.UUID, policy any, policyHash string) (int64, error) {
	encoded, err := json.Marshal(policy)
	if err != nil {
		return 0, err
	}
	if policyHash == "" {
		return 0, errors.New("policy hash required")
	}
	var version int64
	err = d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM repositories WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, repositoryID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repository_policies SET is_active=false WHERE tenant_id=$1 AND repository_id=$2 AND is_active`, tenantID, repositoryID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM repository_policies WHERE tenant_id=$1 AND repository_id=$2`, tenantID, repositoryID).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO repository_policies(tenant_id,repository_id,version,policy_json,policy_hash,created_by,is_active) VALUES($1,$2,$3,$4,$5,$6,true)`, tenantID, repositoryID, version, encoded, policyHash, actorID); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, tenantID, actorID, "policy.update", "repository", repositoryID.String(), map[string]any{"version": version, "policy_hash": policyHash})
	})
	return version, err
}

func (d *DB) ActivePolicy(ctx context.Context, tenantID, repositoryID uuid.UUID) (json.RawMessage, int64, string, error) {
	var policy []byte
	var version int64
	var hash string
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT policy_json,version,policy_hash FROM repository_policies WHERE tenant_id=$1 AND repository_id=$2 AND is_active`, tenantID, repositoryID).Scan(&policy, &version, &hash)
	})
	return json.RawMessage(policy), version, hash, err
}

type SnapshotBundle struct {
	TenantID  uuid.UUID
	Snapshot  domain.RepositorySnapshot
	Nodes     []domain.GraphNode
	Edges     []domain.GraphEdge
	TestUnits []domain.TestUnit
	Coverage  []domain.CoverageObservation
}

// SaveSnapshot writes a complete immutable evidence snapshot in one transaction.
// It rejects unsafe paths, duplicate keys, cross-snapshot references, and oversized uploads.
func (d *DB) SaveSnapshot(ctx context.Context, b SnapshotBundle) (uuid.UUID, error) {
	if len(b.Nodes) > 500_000 || len(b.Edges) > 2_000_000 || len(b.TestUnits) > 200_000 || len(b.Coverage) > 2_000_000 {
		return uuid.Nil, errors.New("snapshot exceeds configured evidence limits")
	}
	s := b.Snapshot
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if err := domain.ValidateSnapshot(s); err != nil {
		return uuid.Nil, err
	}
	if s.TestConfigDigest == "" {
		// Keep older API clients safe while migrating: their coverage history is
		// compatible only with the same build configuration.
		s.TestConfigDigest = s.BuildConfigDigest
	}
	if s.ToolchainDigest == "" || s.BuildConfigDigest == "" || s.TestConfigDigest == "" || s.AdapterSetDigest == "" {
		return uuid.Nil, errors.New("snapshot toolchain, build, test configuration, and adapter fingerprints are required")
	}
	completeness, err := json.Marshal(map[string]any{"complete": s.IsComplete, "reasons": s.CompletenessReasons})
	if err != nil {
		return uuid.Nil, err
	}
	var finalID uuid.UUID
	err = d.TenantTx(ctx, b.TenantID.String(), func(tx pgx.Tx) error {
		var repoExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE tenant_id=$1 AND id=$2)`, b.TenantID, s.RepositoryID).Scan(&repoExists); err != nil {
			return err
		}
		if !repoExists {
			return errors.New("repository not found in tenant")
		}
		err := tx.QueryRow(ctx, `INSERT INTO repository_snapshots(id,tenant_id,repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,test_config_digest,adapter_digest,graph_version,completeness_json)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(repository_id,commit_sha,base_sha,tree_hash,worktree_hash,submodule_digest,toolchain_digest,config_digest,test_config_digest,adapter_digest,graph_version,completeness_json) DO NOTHING RETURNING id`, s.ID, b.TenantID, s.RepositoryID, s.CommitSHA, s.BaseSHA, s.TreeHash, s.WorktreeHash, s.SubmoduleDigest, s.ToolchainDigest, s.BuildConfigDigest, s.TestConfigDigest, s.AdapterSetDigest, s.GraphVersion, completeness).Scan(&finalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT id FROM repository_snapshots WHERE tenant_id=$1 AND repository_id=$2 AND commit_sha=$3 AND base_sha=$4 AND tree_hash=$5 AND worktree_hash=$6 AND submodule_digest=$7 AND toolchain_digest=$8 AND config_digest=$9 AND test_config_digest=$10 AND adapter_digest=$11 AND graph_version=$12 AND completeness_json=$13`, b.TenantID, s.RepositoryID, s.CommitSHA, s.BaseSHA, s.TreeHash, s.WorktreeHash, s.SubmoduleDigest, s.ToolchainDigest, s.BuildConfigDigest, s.TestConfigDigest, s.AdapterSetDigest, s.GraphVersion, completeness).Scan(&finalID)
		}
		if err != nil {
			return err
		}
		if finalID != s.ID {
			return nil
		} // Equivalent immutable snapshot already exists; never merge evidence into it.
		nodeIDs := make(map[string]uuid.UUID, len(b.Nodes))
		nodeDBIDs := make(map[uuid.UUID]uuid.UUID, len(b.Nodes))
		nodeStableKeys := make(map[string]struct{}, len(b.Nodes))
		for i := range b.Nodes {
			n := &b.Nodes[i]
			if n.SnapshotID != uuid.Nil && n.SnapshotID != finalID {
				return errors.New("graph node references another snapshot")
			}
			n.SnapshotID = finalID
			if n.ID == uuid.Nil {
				n.ID = uuid.New()
			}
			if n.StableKey == "" || n.NodeType == "" {
				return fmt.Errorf("graph node %d is missing a stable key or type", i)
			}
			if n.Path != "" {
				normalized, e := security.NormalizeRepoPath(n.Path)
				if e != nil {
					return fmt.Errorf("graph node path: %w", e)
				}
				n.Path = normalized
			}
			if err := domain.ValidateGraphNode(*n, finalID); err != nil {
				return fmt.Errorf("graph node %q: %w", n.StableKey, err)
			}
			if _, exists := nodeIDs[string(n.NodeType)+"\x00"+n.StableKey]; exists {
				return fmt.Errorf("duplicate graph node key %q", n.StableKey)
			}
			if _, exists := nodeStableKeys[n.StableKey]; exists {
				return fmt.Errorf("graph stable key %q is ambiguous across node types", n.StableKey)
			}
			nodeIDs[string(n.NodeType)+"\x00"+n.StableKey] = n.ID
			nodeStableKeys[n.StableKey] = struct{}{}
			nodeDBIDs[n.ID] = n.ID
			attrs, e := json.Marshal(map[string]any{"labels": n.Labels, "attributes": n.Attributes})
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO graph_nodes(id,tenant_id,snapshot_id,stable_key,node_type,path,display_name,confidence,evidence_source,observed_at,valid_until,attributes_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, n.ID, b.TenantID, finalID, n.StableKey, n.NodeType, n.Path, n.DisplayName, n.Confidence, n.EvidenceSource, n.ObservedAt, n.ValidUntil, attrs); e != nil {
				return e
			}
		}
		for i := range b.Edges {
			e := &b.Edges[i]
			if e.SnapshotID != uuid.Nil && e.SnapshotID != finalID {
				return errors.New("edge references another snapshot")
			}
			e.SnapshotID = finalID
			if e.ID == uuid.Nil {
				e.ID = uuid.New()
			}
			if _, ok := nodeDBIDs[e.FromNodeID]; !ok {
				return errors.New("edge source node is absent from snapshot")
			}
			if _, ok := nodeDBIDs[e.ToNodeID]; !ok {
				return errors.New("edge target node is absent from snapshot")
			}
			if err := domain.ValidateGraphEdge(*e, finalID); err != nil {
				return fmt.Errorf("graph edge %d: %w", i, err)
			}
			attrs, er := json.Marshal(map[string]any{"attributes": e.Attributes, "conflict": e.Conflict})
			if er != nil {
				return er
			}
			if _, er = tx.Exec(ctx, `INSERT INTO graph_edges(id,tenant_id,snapshot_id,from_node_id,to_node_id,edge_type,confidence,evidence_source,observed_at,valid_until,attributes_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, e.ID, b.TenantID, finalID, e.FromNodeID, e.ToNodeID, e.EdgeType, e.Confidence, e.EvidenceSource, e.ObservedAt, e.ValidUntil, attrs); er != nil {
				return er
			}
		}
		unitIDs := make(map[string]uuid.UUID, len(b.TestUnits))
		for i := range b.TestUnits {
			u := &b.TestUnits[i]
			if u.SnapshotID != uuid.Nil && u.SnapshotID != finalID {
				return errors.New("test unit references another snapshot")
			}
			u.SnapshotID = finalID
			if u.StableKey == "" || u.Adapter == "" || u.UnitType == "" {
				return fmt.Errorf("test unit %d lacks key, adapter, or type", i)
			}
			if u.EstimatedMillis < 0 || u.DurationP50Millis < 0 || u.DurationP95Millis < 0 || u.DurationSampleCount < 0 || u.HistoricalFailures < 0 || u.FlakeRate < 0 || u.FlakeRate > 1 {
				return fmt.Errorf("test unit %q has invalid duration or flake statistics", u.StableKey)
			}
			switch u.UnitType {
			case domain.UnitTestCase, domain.UnitTestMethod, domain.UnitTestFile, domain.UnitTestClass, domain.UnitBuildTarget, domain.UnitProjectSuite, domain.UnitCheck:
			default:
				return fmt.Errorf("test unit %q has unknown unit type", u.StableKey)
			}
			if _, exists := unitIDs[u.StableKey]; exists {
				return fmt.Errorf("duplicate test unit %q", u.StableKey)
			}
			if u.ID == uuid.Nil {
				u.ID = uuid.New()
			}
			unitIDs[u.StableKey] = u.ID
			meta := map[string]any{"metadata": u.Metadata, "target_ref": u.TargetRef, "estimated_millis": u.EstimatedMillis, "duration_p50_millis": u.DurationP50Millis, "duration_p95_millis": u.DurationP95Millis, "duration_sample_count": u.DurationSampleCount, "flake_rate": u.FlakeRate, "last_seen_at": u.LastSeenAt, "granularity": u.SelectionGranularity, "execution_group": u.ExecutionGroup, "source_paths": u.SourcePaths, "historical_failures": u.HistoricalFailures}
			metaJSON, er := json.Marshal(meta)
			if er != nil {
				return er
			}
			riskJSON, er := json.Marshal(u.RiskLabels)
			if er != nil {
				return er
			}
			if _, er = tx.Exec(ctx, `INSERT INTO test_units(id,tenant_id,snapshot_id,stable_key,unit_type,adapter,module_key,mandatory,risk_labels_json,selectable,granularity,metadata_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, u.ID, b.TenantID, finalID, u.StableKey, u.UnitType, u.Adapter, u.ModuleKey, u.Mandatory, riskJSON, u.Selectable, u.SelectionGranularity, metaJSON); er != nil {
				return er
			}
		}
		for _, c := range b.Coverage {
			if err := domain.ValidateCoverageObservation(c); err != nil {
				return fmt.Errorf("coverage observation: %w", err)
			}
			unitID, ok := unitIDs[c.TestUnitKey]
			if !ok {
				return fmt.Errorf("coverage references unknown test unit %q", c.TestUnitKey)
			}
			var nodeID uuid.UUID
			var resolveErr error
			if c.CoveredStableKey != "" {
				resolveErr = tx.QueryRow(ctx, `SELECT id FROM graph_nodes WHERE snapshot_id=$1 AND stable_key=$2`, finalID, c.CoveredStableKey).Scan(&nodeID)
			} else {
				resolveErr = tx.QueryRow(ctx, `SELECT id FROM graph_nodes WHERE snapshot_id=$1 AND path=$2 ORDER BY stable_key LIMIT 1`, finalID, c.CoveredPath).Scan(&nodeID)
			}
			if resolveErr != nil {
				return fmt.Errorf("coverage references unknown node %q: %w", c.CoveredStableKey, resolveErr)
			}
			if c.TestSpecific && c.SuiteLevel {
				return errors.New("coverage cannot be both test-specific and suite-level")
			}
			if !c.TestSpecific && !c.SuiteLevel {
				return errors.New("coverage precision must identify test-specific or suite-level evidence")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO coverage_observations(tenant_id,snapshot_id,test_unit_id,covered_node_id,precision,source,report_hash,run_id,environment_digest,build_config_digest,test_config_digest,line_start,line_end,suite_level,test_specific,evidence_confidence,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, b.TenantID, finalID, unitID, nodeID, c.Precision, c.SourceType, c.ReportHash, c.RunID, c.EnvironmentFingerprint, c.BuildConfigDigest, c.TestConfigDigest, c.LineStart, c.LineEnd, c.SuiteLevel, c.TestSpecific, c.EvidenceConfidence, c.ObservedAt); err != nil {
				return err
			}
		}
		return nil
	})
	return finalID, err
}

func insertAuditEvent(ctx context.Context, tx pgx.Tx, tenantID, actorID uuid.UUID, action, resourceType, resourceID string, metadata any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var actor any
	if actorID != uuid.Nil {
		actor = actorID
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(tenant_id,actor_id,action,resource_type,resource_id,metadata_json) VALUES($1,$2,$3,$4,$5,$6)`, tenantID, actor, action, resourceType, resourceID, b)
	return err
}
