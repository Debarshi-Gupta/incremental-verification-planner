package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
	"github.com/ivp/ivp/internal/telemetry"
	"github.com/jackc/pgx/v5"
)

// Backend is the tenant-scoped API application service. It is the only layer
// that translates HTTP actions into planner, policy and persistence calls.
type Backend struct {
	DB      *persistence.DB
	Metrics *telemetry.Metrics
	Audit   audit.Service
	Now     func() time.Time
}

func NewBackend(db *persistence.DB, metrics *telemetry.Metrics) *Backend {
	return &Backend{DB: db, Metrics: metrics, Audit: audit.Service{Store: db, Metrics: metrics, QuarantineDuration: 7 * 24 * time.Hour}, Now: time.Now}
}

func (b *Backend) Ready(ctx context.Context) error {
	if b == nil || b.DB == nil {
		return errors.New("database unavailable")
	}
	return b.DB.Ready(ctx)
}

func (b *Backend) Execute(ctx context.Context, action api.Action) (any, int, error) {
	if b == nil || b.DB == nil {
		return nil, 0, apiErr(http.StatusServiceUnavailable, "BACKEND_UNAVAILABLE", "service backend is unavailable", true)
	}
	tenant, err := uuid.Parse(action.Principal.TenantID)
	if err != nil {
		return nil, 0, apiErr(http.StatusForbidden, "TENANT_INVALID", "tenant membership is invalid", false)
	}
	actor, _ := uuid.Parse(action.Principal.UserID)
	repoID, idErr := uuid.Parse(action.ResourceID)
	if action.ResourceID != "" && idErr != nil {
		return nil, 0, apiErr(http.StatusBadRequest, "RESOURCE_ID_INVALID", "resource identifier is invalid", false)
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	switch action.Operation {
	case "identity.me":
		return map[string]any{"subject": action.Principal.Subject, "tenant_id": action.Principal.TenantID, "user_id": action.Principal.UserID, "role": action.Principal.Role}, http.StatusOK, nil
	case "repositories.create":
		v := action.Payload.(api.RepositoryRequest)
		repo, err := b.DB.RegisterRepository(ctx, tenant, actor, domain.Repository{Slug: v.Slug, CanonicalURL: v.CanonicalURL, DefaultBranch: v.DefaultBranch})
		if err != nil {
			return nil, 0, storageError(err)
		}
		return repo, http.StatusCreated, nil
	case "repositories.list":
		limit, err := parseLimit(queryValue(action.Query, "limit"), 50, 200)
		if err != nil {
			return nil, 0, apiErr(400, "PAGINATION_INVALID", err.Error(), false)
		}
		page, err := b.DB.ListRepositories(ctx, tenant, limit, queryValue(action.Query, "cursor"))
		if err != nil {
			return nil, 0, storageError(err)
		}
		return page, http.StatusOK, nil
	case "repositories.get":
		repo, err := b.DB.GetRepository(ctx, tenant, repoID)
		if err != nil {
			return nil, 0, storageError(err)
		}
		return repo, http.StatusOK, nil
	case "snapshots.create":
		v := action.Payload.(api.SnapshotRequest)
		if v.Snapshot.RepositoryID != repoID {
			return nil, 0, apiErr(400, "SNAPSHOT_REPOSITORY_MISMATCH", "snapshot repository does not match route", false)
		}
		id, err := b.DB.SaveSnapshot(ctx, persistence.SnapshotBundle{TenantID: tenant, Snapshot: v.Snapshot, Nodes: v.Nodes, Edges: v.Edges, TestUnits: v.TestUnits, Coverage: v.Coverage})
		if err != nil {
			return nil, 0, storageError(err)
		}
		return map[string]any{"snapshot_id": id, "immutable": true}, http.StatusCreated, nil
	case "policy.get":
		return b.getPolicy(ctx, tenant, repoID)
	case "policy.update":
		v := action.Payload.(api.PolicyRequest)
		return b.updatePolicy(ctx, tenant, actor, repoID, v)
	case "plans.create":
		v := action.Payload.(api.PlanRequest)
		return b.createPlan(ctx, tenant, repoID, v, now)
	case "plans.get":
		planID, _ := uuid.Parse(action.ResourceID)
		plan, err := b.DB.GetPlan(ctx, tenant, planID)
		if err != nil {
			return nil, 0, storageError(err)
		}
		return plan, http.StatusOK, nil
	case "executions.start":
		v := action.Payload.(api.ExecutionStartRequest)
		planID, _ := uuid.Parse(action.ResourceID)
		execution, err := b.DB.StartExecution(ctx, tenant, actor, planID, action.IdempotencyKey, v.WorktreeHash, v.EnvironmentDigest, v.FullSuite, now)
		if err != nil {
			return nil, 0, storageError(err)
		}
		return execution, http.StatusCreated, nil
	case "executions.result":
		v := action.Payload.(persistence.ResultSubmission)
		executionID, _ := uuid.Parse(action.ResourceID)
		if err := b.DB.SubmitExecutionResult(ctx, tenant, executionID, v); err != nil {
			return nil, 0, storageError(err)
		}
		return map[string]any{"execution_id": executionID, "outcome": v.Outcome, "accepted": true}, http.StatusOK, nil
	case "executions.get":
		executionID, _ := uuid.Parse(action.ResourceID)
		execution, err := b.DB.GetExecution(ctx, tenant, executionID)
		if err != nil {
			return nil, 0, storageError(err)
		}
		return execution, http.StatusOK, nil
	case "audits.submit":
		return b.submitAudit(ctx, tenant, repoID, action, now)
	case "metrics.repository":
		limit, err := parseLimit(queryValue(action.Query, "limit"), 30, 100)
		if err != nil {
			return nil, 0, apiErr(400, "PAGINATION_INVALID", err.Error(), false)
		}
		metrics, err := b.DB.GetRepositoryMetrics(ctx, tenant, repoID, limit, queryValue(action.Query, "cursor"))
		if err != nil {
			return nil, 0, storageError(err)
		}
		return metrics, http.StatusOK, nil
	case "quarantine.create":
		v := action.Payload.(api.QuarantineRequest)
		err := b.DB.CreateQuarantine(ctx, tenant, actor, repoID, v.ScopeKey, v.ReasonCode, nil, v.ExpiresAt)
		if err != nil {
			return nil, 0, storageError(err)
		}
		if b.Metrics != nil {
			b.Metrics.Quarantine(v.ReasonCode)
		}
		return map[string]any{"repository_id": repoID, "scope_key": v.ScopeKey, "status": "QUARANTINED"}, http.StatusCreated, nil
	case "quarantine.acknowledge":
		v := action.Payload.(api.AcknowledgeRequest)
		if err := audit.Acknowledge(ctx, b.DB, tenant, actor, repoID, action.Scope, v.Reason); err != nil {
			return nil, 0, storageError(err)
		}
		return map[string]any{"repository_id": repoID, "scope_key": action.Scope, "status": "ACKNOWLEDGED"}, http.StatusOK, nil
	default:
		return nil, 0, apiErr(http.StatusNotFound, "ROUTE_NOT_SUPPORTED", "API operation is not supported", false)
	}
}

func (b *Backend) getPolicy(ctx context.Context, tenant, repo uuid.UUID) (any, int, error) {
	raw, version, hash, err := b.DB.ActivePolicy(ctx, tenant, repo)
	if err == nil {
		return map[string]any{"policy": raw, "version": version, "policy_hash": hash, "trusted": true}, http.StatusOK, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, storageError(err)
	}
	policy := config.ToDomainPolicy(config.Defaults(), 1, nil)
	data, _ := json.Marshal(policy)
	return map[string]any{"policy": policy, "version": 1, "policy_hash": digest(data), "trusted": true, "source": "builtin-conservative"}, http.StatusOK, nil
}

func (b *Backend) updatePolicy(ctx context.Context, tenant, actor, repo uuid.UUID, v api.PolicyRequest) (any, int, error) {
	var proposed domain.Policy
	if err := json.Unmarshal(v.Policy, &proposed); err != nil {
		return nil, 0, apiErr(400, "POLICY_INVALID", "policy does not match the supported schema", false)
	}
	var canonical any
	if err := json.Unmarshal(v.Policy, &canonical); err != nil {
		return nil, 0, apiErr(400, "POLICY_INVALID", "policy JSON is invalid", false)
	}
	canonicalJSON, _ := json.Marshal(canonical)
	if !strings.EqualFold(v.PolicyHash, digest(canonicalJSON)) {
		return nil, 0, apiErr(422, "POLICY_HASH_MISMATCH", "policy hash does not match canonical policy content", false)
	}
	base := config.ToDomainPolicy(config.Defaults(), 1, nil)
	if err := validateMonotonicPolicy(proposed, base); err != nil {
		return nil, 0, apiErr(422, "POLICY_WEAKENS_SAFETY", "policy would weaken protected safety requirements", false)
	}
	version, err := b.DB.SavePolicy(ctx, tenant, actor, repo, proposed, digest(canonicalJSON))
	if err != nil {
		return nil, 0, storageError(err)
	}
	return map[string]any{"version": version, "policy_hash": digest(canonicalJSON), "trusted": true}, http.StatusOK, nil
}

func (b *Backend) createPlan(ctx context.Context, tenant, repoID uuid.UUID, v api.PlanRequest, now time.Time) (any, int, error) {
	snapshotID, err := uuid.Parse(v.SnapshotID)
	if err != nil {
		return nil, 0, apiErr(400, "SNAPSHOT_ID_INVALID", "snapshot id is invalid", false)
	}
	bundle, err := b.DB.LoadSnapshot(ctx, tenant, repoID, snapshotID)
	if err != nil {
		return nil, 0, storageError(err)
	}
	policy, policyVersion, _, err := b.effectivePolicy(ctx, tenant, repoID)
	if err != nil {
		return nil, 0, storageError(err)
	}
	if len(v.ChangedFiles) == 0 {
		return nil, 0, apiErr(400, "CHANGE_SET_REQUIRED", "normalized changed-file metadata is required", false)
	}
	if v.WorktreeHash != bundle.Snapshot.WorktreeHash || v.BaseSHA != bundle.Snapshot.BaseSHA || v.HeadSHA != bundle.Snapshot.CommitSHA {
		return nil, 0, apiErr(http.StatusConflict, "SNAPSHOT_STALE", "requested base, head, or worktree fingerprint differs from the immutable snapshot", false)
	}
	adapter := "unknown"
	for _, candidate := range v.ClientCapabilities.Adapters {
		if candidate != "" {
			adapter = candidate
			break
		}
	}
	knownAdapter := map[string]bool{"gradle": true, "maven": true, "bazel": true, "nx": true, "pytest": true, "jest": true, "generic": true}
	if b.Metrics != nil {
		b.Metrics.SetGraphCompleteness(boolMetric(bundle.Snapshot.IsComplete))
	}
	capabilities := make(map[string]string, len(v.ClientCapabilities.AdapterVersions))
	for k, x := range v.ClientCapabilities.AdapterVersions {
		capabilities[k] = x
	}
	policy.Quarantined = false // active quarantine is enforced by the policy read path and audit writer
	if q, ok := b.quarantined(ctx, tenant, repoID, adapter); ok {
		policy.Quarantined = true
		policy.QuarantineScope = q
	}
	evidence := analysis.EvidenceSnapshot{Snapshot: bundle.Snapshot, ChangedFiles: v.ChangedFiles, Nodes: bundle.Nodes, Edges: bundle.Edges, Coverage: bundle.Coverage, Units: bundle.TestUnits,
		GraphComplete: bundle.Snapshot.IsComplete, GraphMissingEdges: !bundle.Snapshot.IsComplete, DiscoveryReliable: bundle.Snapshot.IsComplete,
		CoverageExpected: len(bundle.Coverage) > 0, PolicyTrusted: true, ToolchainComparable: v.ExecutionContext.ToolchainDigest != "" && v.ExecutionContext.ToolchainDigest == bundle.Snapshot.ToolchainDigest,
		CapturedAt: now, AdapterCapabilityVersions: capabilities}
	request := domain.PlanRequest{RepositoryID: repoID, SnapshotID: snapshotID, BaseSHA: v.BaseSHA, HeadSHA: v.HeadSHA, CurrentWorktreeHash: v.WorktreeHash, Mode: v.Mode, Now: now,
		AdapterID: adapter, AdapterSupported: knownAdapter[adapter] && !strings.EqualFold(v.ClientCapabilities.OS, "windows"), AdapterDiscoveryOK: bundle.Snapshot.IsComplete,
		AdapterCapabilities: capabilities, EnvironmentFingerprint: v.ExecutionContext.ToolchainDigest, PolicyVersion: policyVersion, PlanTTL: 15 * time.Minute, PolicySnapshotTrusted: true}
	for _, unit := range bundle.TestUnits {
		request.FullSuiteEstimateMillis += unit.EstimatedMillis
	}
	plan, err := analysis.CreatePlan(request, evidence, policy)
	if err != nil {
		return nil, 0, apiErr(422, "PLAN_INVALID", "planner could not safely create a plan", false)
	}
	if err := b.DB.SavePlan(ctx, tenant, plan); err != nil {
		return nil, 0, storageError(err)
	}
	if b.Metrics != nil {
		b.Metrics.Plan(string(plan.Mode), adapter, string(plan.Status), 0, plan.SelectionConfidence, plan.WeightedCoverage)
		if plan.Status != domain.PlanReady {
			b.Metrics.Fallback("FULL_REQUIRED", adapter)
		}
		for _, u := range plan.SelectedUnits {
			b.Metrics.Selected(adapter, string(u.UnitType), 1)
		}
	}
	return plan, http.StatusCreated, nil
}

func (b *Backend) effectivePolicy(ctx context.Context, tenant, repo uuid.UUID) (domain.Policy, int64, string, error) {
	raw, version, hash, err := b.DB.ActivePolicy(ctx, tenant, repo)
	if err == nil {
		var policy domain.Policy
		if e := json.Unmarshal(raw, &policy); e != nil {
			return policy, 0, "", e
		}
		policy.Version = version
		policy.PolicyAvailable = true
		return policy, version, hash, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Policy{}, 0, "", err
	}
	policy := config.ToDomainPolicy(config.Defaults(), 1, nil)
	return policy, 1, "builtin", nil
}

func (b *Backend) submitAudit(ctx context.Context, tenant, repo uuid.UUID, a api.Action, now time.Time) (any, int, error) {
	v := a.Payload.(api.AuditRequest)
	selectedID, err := uuid.Parse(a.ResourceID)
	if err != nil {
		return nil, 0, apiErr(400, "EXECUTION_ID_INVALID", "execution id is invalid", false)
	}
	fullID, err := uuid.Parse(v.FullExecutionID)
	if err != nil {
		return nil, 0, apiErr(400, "FULL_EXECUTION_ID_INVALID", "full_execution_id is required and must be a UUID", false)
	}
	selected, err := b.DB.GetExecution(ctx, tenant, selectedID)
	if err != nil {
		return nil, 0, storageError(err)
	}
	plan, err := b.DB.GetPlan(ctx, tenant, selected.PlanID)
	if err != nil {
		return nil, 0, storageError(err)
	}
	in := audit.Input{TenantID: tenant, RepositoryID: repo, PlanID: plan.ID, SelectedExecutionID: selected.ID, FullExecutionID: fullID, Adapter: v.Adapter, RuleScope: v.RuleScope,
		SelectedOutcome: v.SelectedOutcome, FullOutcome: v.FullOutcome, SelectedKeys: v.SelectedKeys, Failures: v.Failures, SelectedFingerprint: v.SelectedFingerprint, FullFingerprint: v.FullFingerprint, FailureOutputHash: v.FailureOutputHash, Now: now}
	result, err := b.Audit.Process(ctx, in)
	if err != nil {
		return nil, 0, storageError(err)
	}
	return result, http.StatusCreated, nil
}

func (b *Backend) quarantined(ctx context.Context, tenant, repo uuid.UUID, adapter string) (string, bool) {
	var scope string
	err := b.DB.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT scope_key FROM quarantines WHERE tenant_id=$1 AND repository_id=$2 AND status IN ('QUARANTINED','ACKNOWLEDGED') AND (expires_at IS NULL OR expires_at>now()) AND (scope_key=$3 OR scope_key LIKE $4 OR scope_key='all/all') ORDER BY created_at DESC LIMIT 1`, tenant, repo, adapter+"/all", adapter+"/%").Scan(&scope)
	})
	return scope, err == nil
}

func validateMonotonicPolicy(p, base domain.Policy) error {
	if p.LowRiskMaxScore > base.LowRiskMaxScore || p.MediumRiskMaxScore > base.MediumRiskMaxScore || p.LowRiskMinWeightedCoverage < base.LowRiskMinWeightedCoverage || p.MediumRiskMinWeightedCoverage < base.MediumRiskMinWeightedCoverage || p.MinimumSelectionConfidence < base.MinimumSelectionConfidence || p.MaxSelectiveRuntimeFraction > base.MaxSelectiveRuntimeFraction || p.MaxChangedFilesForSelectivePlan > base.MaxChangedFilesForSelectivePlan {
		return errors.New("threshold is less restrictive than built-in baseline")
	}
	if p.RiskBasedCIEnabled || p.RiskBasedCIApproved {
		return errors.New("risk-based CI needs out-of-band organization approval")
	}
	if !stringSetContains(p.AlwaysRunCheckIDs, base.AlwaysRunCheckIDs) || !stringSetContains(p.HardFullSuitePaths, base.HardFullSuitePaths) || !stringSetContains(p.HardFullSuiteRiskLabels, base.HardFullSuiteRiskLabels) {
		return errors.New("mandatory checks or hard rules were removed")
	}
	if p.GraphStaleness <= 0 || p.CoverageStaleness <= 0 || p.DependencyCap < 1 || p.DependencyFanoutEscalation < 1 {
		return errors.New("policy bounds are invalid")
	}
	return nil
}
func stringSetContains(values, required []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		seen[v] = true
	}
	for _, v := range required {
		if !seen[v] {
			return false
		}
	}
	return true
}
func parseLimit(value string, fallback, max int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("limit must be between 1 and %d", max)
	}
	return n, nil
}
func queryValue(query map[string][]string, key string) string {
	if values := query[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}
func boolMetric(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func apiErr(status int, code, message string, retryable bool) error {
	return &api.APIError{Status: status, Code: code, Message: message, Retryable: retryable}
}
func storageError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apiErr(http.StatusNotFound, "NOT_FOUND", "requested tenant resource was not found", false)
	}
	if err == nil {
		return nil
	}
	return apiErr(http.StatusUnprocessableEntity, "REQUEST_REJECTED", security.Redact(err.Error(), 300), false)
}

var _ api.Backend = (*Backend)(nil)
var _ = json.RawMessage{}
