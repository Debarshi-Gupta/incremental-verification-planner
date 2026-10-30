package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/execution"
	"github.com/ivp/ivp/internal/gitutil"
	"github.com/ivp/ivp/internal/security"
)

func (c *commands) audit(ctx context.Context, out io.Writer, cfg config.Config, selectedRef, fullRef, adapterOverride, ruleScope string, sample bool) error {
	root, err := gitutil.RepositoryRoot(ctx, ".")
	if err != nil {
		return exitError{2, err}
	}
	if selectedRef == "latest" {
		latest, latestErr := newestSelectiveRun(root)
		if latestErr != nil {
			return latestErr
		}
		if latest == nil {
			fmt.Fprintln(out, "No uploaded selective execution is available for audit; skipping this sample.")
			return nil
		}
		if sample && !auditSampleSelected(latest.ExecutionID, cfg.Safety.Audit.SampleRate) {
			fmt.Fprintln(out, "The latest selective execution was not selected by safety.audit.sample_rate; skipping this audit sample.")
			return nil
		}
		selectedRef = latest.ExecutionID.String()
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		return err
	}
	selected, err := findLocalRun(root, selectedRef)
	if err != nil {
		return err
	}
	if selected.FullSuite || selected.ExecutionID == uuid.Nil {
		return exitError{2, errors.New("--run must identify an uploaded selective execution report")}
	}
	var plan domain.VerificationPlan
	if err := client.request(ctx, http.MethodGet, "/api/v1/plans/"+selected.PlanID.String(), nil, "", &plan); err != nil {
		return err
	}
	if plan.ID != selected.PlanID || plan.RepositoryID == uuid.Nil {
		return exitError{6, errors.New("the selected report does not match a valid server plan")}
	}
	_, record, err := loadIndex(root)
	if err != nil {
		return err
	}
	if record.Snapshot.ID != plan.SnapshotID {
		return exitError{5, errors.New("local snapshot does not match the plan; an equivalent-environment audit cannot be established")}
	}
	var full *localRun
	auditRoot, fullRoot := root, root
	cleanupAudit := func() {}
	auditRootIsolated := false
	if strings.TrimSpace(fullRef) != "" {
		full, err = findLocalRun(root, fullRef)
		if err != nil {
			return err
		}
		if !full.IsolatedWorkspace {
			return exitError{5, errors.New("full-suite audit reports must come from an isolated equivalent workspace; omit --full-run to let IVP create one")}
		}
		if full.ReportPath != "" {
			fullRoot = filepath.Dir(filepath.Dir(filepath.Dir(full.ReportPath)))
			auditRoot = fullRoot
		}
		auditRootIsolated = true
	} else {
		if cfg.FullSuiteArgv() == nil || len(cfg.FullSuiteArgv()) == 0 {
			return exitError{3, errors.New("audit requires execution.full_suite_argv")}
		}
		auditRoot, cleanupAudit, err = createAuditWorkspace(ctx, root)
		if err != nil {
			return exitError{4, fmt.Errorf("could not create an isolated equivalent audit workspace: %w", err)}
		}
		defer func() { cleanupAudit() }()
		auditRootIsolated, fullRoot = true, auditRoot
		if err := saveAuditIndex(auditRoot, record); err != nil {
			return exitError{6, err}
		}
		fmt.Fprintf(out, "Running the saved plan's full suite in an isolated equivalent workspace for audit comparison.\n")
		runErr := c.runPlanCoreAt(ctx, out, auditRoot, cfg, plan, "", true, true)
		if runErr != nil && ExitCode(runErr) != 1 {
			return runErr
		}
		full, err = newestFullRun(auditRoot, plan.ID, selected.ExecutionID)
		if err != nil {
			return err
		}
	}
	if full.PlanID != selected.PlanID || !full.FullSuite || full.ExecutionID == uuid.Nil {
		return exitError{2, errors.New("full-suite report must be a completed full execution for the same plan")}
	}
	if selected.State == "RESULT_READY" {
		if err := uploadLocalResult(ctx, out, client, plan.RepositoryID, selected, localRunPath(root, selected.LocalID), out); err != nil {
			return err
		}
	}
	if full.State == "RESULT_READY" {
		if err := uploadLocalArtifacts(ctx, fullRoot, client, full); err != nil {
			return err
		}
		if err := uploadLocalResult(ctx, out, client, plan.RepositoryID, full, localRunPath(fullRoot, full.LocalID), out); err != nil {
			return err
		}
	}
	if selected.State != "UPLOADED" {
		return exitError{4, errors.New("selected execution result is not uploaded; retry `ivp run --plan <plan-id>` first")}
	}
	if full.State != "UPLOADED" {
		return exitError{4, errors.New("full-suite execution result is not uploaded; retry `ivp run --plan <plan-id> --full-suite` first")}
	}
	if selected.WorktreeHash == "" || selected.WorktreeHash != full.WorktreeHash || selected.WorktreeHash != plan.WorktreeHash {
		return exitError{5, errors.New("audit executions do not share the plan worktree fingerprint")}
	}
	selectedUnits := plannedUnits(plan)
	selectedKeys := make([]string, 0, len(selectedUnits))
	selectedSet := make(map[string]bool, len(selectedUnits))
	adapterSet := make(map[string]bool)
	unitAdapter := make(map[string]string)
	for _, unit := range record.Units {
		unitAdapter[unit.StableKey] = unit.Adapter
	}
	for _, unit := range selectedUnits {
		if unit.StableKey == "" || selectedSet[unit.StableKey] {
			continue
		}
		selectedSet[unit.StableKey] = true
		selectedKeys = append(selectedKeys, unit.StableKey)
		if adapter := unitAdapter[unit.StableKey]; adapter != "" {
			adapterSet[adapter] = true
		}
	}
	sort.Strings(selectedKeys)
	if len(selectedKeys) == 0 {
		return exitError{5, errors.New("plan contains no auditable selected or mandatory unit keys")}
	}
	fingerprint := audit.Fingerprint{
		SourceTreeHash:         record.Snapshot.TreeHash,
		DependencyDigest:       record.Snapshot.BuildConfigDigest,
		BuildConfigDigest:      record.Snapshot.BuildConfigDigest,
		ToolchainDigest:        selected.EnvironmentDigest,
		TestConfigDigest:       record.Snapshot.TestConfigDigest,
		EnvironmentClass:       runtime.GOOS + "-" + runtime.GOARCH + "-" + environmentClass(),
		RequiredServicesDigest: requiredServicesDigest(),
	}
	fullFingerprint := fingerprint
	fullFingerprint.ToolchainDigest = full.EnvironmentDigest
	if selected.EnvironmentDigest != full.EnvironmentDigest {
		fullFingerprint.EnvironmentClass += "-different-toolchain"
	}
	if fingerprint.RequiredServicesDigest == "" {
		return exitError{4, errors.New("audit requires IVP_REQUIRED_SERVICES_DIGEST: a SHA-256 digest of the required service set in this execution environment")}
	}
	changedRelatedUnits := relatedUnits(record)
	if cfg.Safety.Audit.RepeatFailedTestCount > 0 && hasOmittedFailures(full.UnitResults, selectedSet) && !auditRootIsolated {
		var cleanup func()
		auditRoot, cleanup, err = createAuditWorkspace(ctx, root)
		if err != nil {
			return exitError{4, fmt.Errorf("could not create an isolated equivalent audit workspace: %w", err)}
		}
		defer cleanup()
		if err := saveAuditIndex(auditRoot, record); err != nil {
			return exitError{6, err}
		}
	}
	failureByAdapter := map[string][]audit.Failure{}
	seenFailures := map[string]bool{}
	for _, result := range full.UnitResults {
		if result.Status != "FAILED" && result.Status != "ERROR" {
			continue
		}
		if selectedSet[result.TestUnitKey] {
			continue
		}
		if seenFailures[result.TestUnitKey] {
			continue
		}
		seenFailures[result.TestUnitKey] = true
		adapter := unitAdapter[result.TestUnitKey]
		if adapter == "" {
			continue
		}
		reproduced, flaky := false, false
		repeatCount := 0
		if cfg.Safety.Audit.RepeatFailedTestCount > 0 {
			repeatCount = cfg.Safety.Audit.RepeatFailedTestCount
			reproduced, flaky, err = repeatFailedUnit(ctx, auditRoot, cfg, plan, record, result.TestUnitKey, repeatCount)
			if err != nil {
				return exitError{4, fmt.Errorf("failed-test repeat was inconclusive for %s: %w", security.Redact(result.TestUnitKey, 160), err)}
			}
		}
		failureByAdapter[adapter] = append(failureByAdapter[adapter], audit.Failure{TestUnitKey: result.TestUnitKey, Reproduced: reproduced, Flaky: flaky, RelatedToChange: changedRelatedUnits[result.TestUnitKey], RepeatCount: repeatCount})
	}
	if full.Outcome == domain.OutcomeFullVerificationFail {
		if full.Summary.FailureClasses["FULL_SUITE_FAILURE_DETAILS_INCOMPLETE"] > 0 {
			fmt.Fprintln(out, "Full-suite reports could not attribute every failure; the audit will be recorded as unresolved and affected omitted adapter scopes will be quarantined.")
		} else if len(failureByAdapter) == 0 {
			fmt.Fprintln(out, "The full suite failed without an attributable omitted unit; the audit will be recorded as unresolved and affected omitted adapter scopes will be quarantined.")
		}
	}
	if adapterOverride != "" {
		if !validAdapter(adapterOverride) {
			return exitError{2, errors.New("--adapter must be gradle, maven, bazel, nx, pytest, jest, or generic")}
		}
		adapterSet = map[string]bool{adapterOverride: true}
	}
	if ruleScope == "" {
		return exitError{2, errors.New("--rule-scope is required")}
	}
	if full.Outcome == domain.OutcomeFullVerificationFail && adapterOverride == "" {
		adapterSet = make(map[string]bool, len(failureByAdapter))
		for adapter := range failureByAdapter {
			adapterSet[adapter] = true
		}
		if full.Summary.FailureClasses["FULL_SUITE_FAILURE_DETAILS_INCOMPLETE"] > 0 || len(failureByAdapter) == 0 {
			for _, omitted := range plan.OmittedUnits {
				if adapter := unitAdapter[omitted.StableKey]; adapter != "" {
					adapterSet[adapter] = true
				}
			}
		}
		if len(adapterSet) == 0 {
			for _, adapter := range record.Adapters {
				if adapter != "" {
					adapterSet[adapter] = true
				}
			}
		}
	}
	if len(adapterSet) == 0 {
		return exitError{4, errors.New("the plan has no adapter-scoped units to audit")}
	}
	adapters := make([]string, 0, len(adapterSet))
	for adapter := range adapterSet {
		adapters = append(adapters, adapter)
	}
	sort.Strings(adapters)
	missFound := false
	for _, adapter := range adapters {
		request := api.AuditRequest{
			FullExecutionID:     full.ExecutionID.String(),
			FailureOutputHash:   outputDigest(full.CommandResults),
			Adapter:             adapter,
			RuleScope:           ruleScope,
			SelectedOutcome:     selected.Outcome,
			FullOutcome:         full.Outcome,
			SelectedKeys:        selectedKeys,
			Failures:            failureByAdapter[adapter],
			SelectedFingerprint: fingerprint,
			FullFingerprint:     fullFingerprint,
		}
		var result struct {
			Disposition    audit.Disposition  `json:"Disposition"`
			Quarantine     bool               `json:"Quarantine"`
			MissedTestKeys []string           `json:"MissedTestKeys"`
			Record         domain.AuditRecord `json:"Record"`
		}
		path := "/api/v1/executions/" + selected.ExecutionID.String() + "/audits"
		if err := client.request(ctx, http.MethodPost, path, request, newIdempotencyKey(), &result); err != nil {
			return err
		}
		fmt.Fprintf(out, "Audit %s: %s; missed units=%d; quarantine=%t\n", adapter, result.Disposition, len(result.MissedTestKeys), result.Quarantine)
		if result.Quarantine {
			missFound = true
		}
	}
	if missFound {
		return exitError{7, errors.New("audit found possible or confirmed misses; the affected adapter scope is quarantined")}
	}
	return nil
}

func newestSelectiveRun(root string) (*localRun, error) {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, exitError{4, errors.New("local execution outbox is unavailable")}
	}
	var latest *localRun
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			continue
		}
		var run localRun
		if json.Unmarshal(data, &run) != nil || run.FullSuite || run.State != "UPLOADED" || run.ExecutionID == uuid.Nil || run.Outcome != domain.OutcomeTargetedPass && run.Outcome != domain.OutcomeTargetedFail {
			continue
		}
		if latest == nil || run.CompletedAt.After(latest.CompletedAt) {
			copy := run
			latest = &copy
		}
	}
	return latest, nil
}

func auditSampleSelected(executionID uuid.UUID, rate float64) bool {
	if rate <= 0 || rate > 1 || executionID == uuid.Nil {
		return false
	}
	if rate >= 1 {
		return true
	}
	digest := sha256.Sum256([]byte(executionID.String()))
	value := float64(digest[0]) / 256.0
	return value < rate
}

func hasOmittedFailures(results []api.ExecutionUnitResult, selected map[string]bool) bool {
	for _, result := range results {
		if (result.Status == "FAILED" || result.Status == "ERROR") && !selected[result.TestUnitKey] {
			return true
		}
	}
	return false
}

func repeatFailedUnit(ctx context.Context, root string, cfg config.Config, plan domain.VerificationPlan, record indexRecord, stableKey string, repeatCount int) (bool, bool, error) {
	if repeatCount < 1 || repeatCount > 10 {
		return false, false, errors.New("repeat_failed_test_count must be between 1 and 10")
	}
	var unit domain.TestUnit
	found := false
	for _, candidate := range record.Units {
		if candidate.StableKey == stableKey {
			unit, found = candidate, true
			break
		}
	}
	if !found {
		return false, false, errors.New("failed unit is absent from the immutable local snapshot")
	}
	selectedUnit := domain.PlanUnit{UnitID: unit.ID, StableKey: unit.StableKey, UnitType: unit.UnitType, TargetRef: unit.TargetRef, SelectionGranularity: unit.SelectionGranularity, Mandatory: unit.Mandatory}
	singlePlan := plan
	singlePlan.SelectedUnits = []domain.PlanUnit{selectedUnit}
	singlePlan.MandatoryUnits = nil
	singlePlan.OmittedUnits = nil
	commands, err := materializeSelected(root, cfg, singlePlan, record)
	if err != nil || len(commands) != 1 {
		if err == nil {
			err = errors.New("adapter did not materialize exactly one failed unit")
		}
		return false, false, err
	}
	adapter := adapterByID(unit.Adapter)
	if adapter == nil {
		return false, false, errors.New("failed unit adapter is unavailable")
	}
	runner, err := execution.NewRunner(root, execution.RunnerOptions{MaxConcurrent: 1, MaxOutputBytes: int64(cfg.Execution.MaxOutputBytes)})
	if err != nil {
		return false, false, err
	}
	failures, passes := 0, 0
	for i := 0; i < repeatCount; i++ {
		result := runner.RunAndParse(ctx, adapter, commands[0], adapters.ParseRequest{ReportPaths: commands[0].ReportPaths, MaxReportBytes: commands[0].MaxOutputBytes})
		switch result.Outcome {
		case domain.OutcomeTargetedFail:
			failures++
		case domain.OutcomeTargetedPass:
			passes++
		default:
			return false, false, fmt.Errorf("repeat %d ended as %s", i+1, result.Outcome)
		}
	}
	// The original full-suite observation was a failure. Any passing repeat
	// demonstrates inconsistent outcomes, including the case where every
	// repeat passes, so retain it as flaky rather than treating it as reproduced.
	return failures > 0, passes > 0, nil
}

func createAuditWorkspace(ctx context.Context, root string) (string, func(), error) {
	parent, err := os.MkdirTemp("", "ivp-audit-")
	if err != nil {
		return "", nil, err
	}
	workspace := filepath.Join(parent, "checkout")
	cleanup := func() {
		cmd := exec.Command("git", "-C", root, "worktree", "remove", "--force", workspace)
		_ = cmd.Run()
		_ = os.RemoveAll(parent)
	}
	add := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", workspace, "HEAD")
	if output, err := add.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git worktree add failed: %s", security.Redact(string(output), 160))
	}
	diff := exec.CommandContext(ctx, "git", "-C", root, "diff", "--binary", "HEAD", "--")
	patchData, err := diff.Output()
	if err != nil || len(patchData) > 64<<20 {
		cleanup()
		return "", nil, errors.New("working-tree diff could not be copied into the audit workspace")
	}
	if len(patchData) > 0 {
		apply := exec.CommandContext(ctx, "git", "-C", workspace, "apply", "--binary", "-")
		apply.Stdin = bytes.NewReader(patchData)
		if output, err := apply.CombinedOutput(); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("working-tree diff could not be applied: %s", security.Redact(string(output), 160))
		}
	}
	list := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z")
	untracked, err := list.Output()
	if err != nil || len(untracked) > 64<<20 {
		cleanup()
		return "", nil, errors.New("untracked audit inputs could not be enumerated")
	}
	for _, rel := range strings.Split(string(untracked), "\x00") {
		if rel == "" {
			continue
		}
		if _, err := domain.NormalizeRepoPath(rel); err != nil {
			cleanup()
			return "", nil, errors.New("untracked audit input has an unsafe repository path")
		}
		source := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() {
			cleanup()
			return "", nil, errors.New("untracked audit input is not a regular file")
		}
		destination, err := adapters.ResolveWithin(workspace, rel)
		if err != nil {
			cleanup()
			return "", nil, errors.New("untracked audit input escaped the isolated workspace")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			cleanup()
			return "", nil, err
		}
		data, err := os.ReadFile(source)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	sourceIndex, err := gitIndexPath(ctx, root)
	if err != nil {
		cleanup()
		return "", nil, errors.New("source git index could not be located")
	}
	targetIndex, err := gitIndexPath(ctx, workspace)
	if err != nil {
		cleanup()
		return "", nil, errors.New("isolated git index could not be located")
	}
	indexData, err := os.ReadFile(sourceIndex)
	if err != nil {
		cleanup()
		return "", nil, errors.New("source git index could not be read")
	}
	if err := os.WriteFile(targetIndex, indexData, 0o600); err != nil {
		cleanup()
		return "", nil, errors.New("source git index could not be copied into the audit workspace")
	}
	sourceTree, sourceErr := gitutil.TreeFingerprint(ctx, root)
	copyTree, copyErr := gitutil.TreeFingerprint(ctx, workspace)
	if sourceErr != nil || copyErr != nil || sourceTree != copyTree {
		cleanup()
		return "", nil, errors.New("isolated workspace does not match the indexed source tree")
	}
	sourceSubmodules, sourceErr := gitutil.SubmoduleDigest(ctx, root)
	copySubmodules, copyErr := gitutil.SubmoduleDigest(ctx, workspace)
	if sourceErr != nil || copyErr != nil || sourceSubmodules != copySubmodules {
		cleanup()
		return "", nil, errors.New("isolated workspace submodules are not equivalent")
	}
	sourceFingerprint, sourceErr := gitutil.WorktreeFingerprint(ctx, root)
	copyFingerprint, copyErr := gitutil.WorktreeFingerprint(ctx, workspace)
	if sourceErr != nil || copyErr != nil || sourceFingerprint != copyFingerprint {
		cleanup()
		return "", nil, errors.New("isolated workspace fingerprint differs from the selected execution")
	}
	return workspace, cleanup, nil
}

func gitIndexPath(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--git-path", "index")
	data, err := cmd.Output()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", errors.New("git index path is empty")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	return filepath.Clean(name), nil
}

func saveAuditIndex(root string, record indexRecord) error {
	path, err := adapters.ResolveWithin(root, filepath.Join(".ivp", "index.json"))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func relatedUnits(record indexRecord) map[string]bool {
	nodeByID := make(map[uuid.UUID]domain.GraphNode, len(record.Nodes))
	changedNodeIDs := make(map[uuid.UUID]bool)
	changedPaths := make(map[string]bool, len(record.ChangedFiles)*2)
	for _, change := range record.ChangedFiles {
		changedPaths[change.Path] = true
		if change.OldPath != "" {
			changedPaths[change.OldPath] = true
		}
	}
	for _, node := range record.Nodes {
		nodeByID[node.ID] = node
		if node.NodeType == domain.NodeSourceFile && changedPaths[node.Path] {
			changedNodeIDs[node.ID] = true
		}
	}
	reverse := make(map[uuid.UUID][]uuid.UUID)
	for _, edge := range record.Edges {
		if edge.EdgeType == domain.EdgeDependsOn || edge.EdgeType == domain.EdgeTestCovers {
			reverse[edge.ToNodeID] = append(reverse[edge.ToNodeID], edge.FromNodeID)
		}
	}
	queue := make([]uuid.UUID, 0, len(changedNodeIDs))
	seen := make(map[uuid.UUID]bool, len(changedNodeIDs))
	for id := range changedNodeIDs {
		queue = append(queue, id)
		seen[id] = true
	}
	related := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if node, ok := nodeByID[id]; ok && (node.NodeType == domain.NodeTestTarget || node.NodeType == domain.NodeTestCase || node.NodeType == domain.NodeBuildTarget || node.NodeType == domain.NodeCheckTarget) {
			if key, ok := node.Attributes["unit_key"].(string); ok && key != "" {
				related[key] = true
			}
			if strings.HasPrefix(node.StableKey, "unit:") {
				related[strings.TrimPrefix(node.StableKey, "unit:")] = true
			}
		}
		for _, consumer := range reverse[id] {
			if !seen[consumer] {
				seen[consumer] = true
				queue = append(queue, consumer)
			}
		}
	}
	return related
}

func findLocalRun(root, reference string) (*localRun, error) {
	if data, err := os.ReadFile(reference); err == nil {
		var run localRun
		if json.Unmarshal(data, &run) == nil && run.PlanID != uuid.Nil {
			return &run, nil
		}
		return nil, exitError{2, errors.New("run report JSON is invalid")}
	}
	executionID, err := uuid.Parse(reference)
	if err != nil {
		return nil, exitError{2, errors.New("run reference must be an execution UUID or local report JSON file")}
	}
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return nil, exitError{6, err}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, exitError{4, errors.New("local execution outbox is unavailable")}
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var run localRun
		if json.Unmarshal(data, &run) == nil && run.ExecutionID == executionID {
			return &run, nil
		}
	}
	return nil, exitError{4, errors.New("execution report is not present in the local outbox")}
}

func newestFullRun(root string, planID, exclude uuid.UUID) (*localRun, error) {
	dir, err := safeWorkspacePath(root, filepath.Join(".ivp", "outbox"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, exitError{4, errors.New("local execution outbox is unavailable")}
	}
	var latest *localRun
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var run localRun
		if json.Unmarshal(data, &run) != nil || run.PlanID != planID || run.ExecutionID == exclude || !run.FullSuite || run.State != "UPLOADED" {
			continue
		}
		if latest == nil || run.CompletedAt.After(latest.CompletedAt) {
			copy := run
			latest = &copy
		}
	}
	if latest == nil {
		return nil, exitError{4, errors.New("no uploaded full-suite execution was found for the selected plan")}
	}
	return latest, nil
}

func requiredServicesDigest() string {
	value := strings.TrimSpace(os.Getenv("IVP_REQUIRED_SERVICES_DIGEST"))
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return strings.ToLower(value)
}

func outputDigest(results []execution.Result) string {
	values := make([]string, 0, len(results)*2)
	for _, result := range results {
		if result.StdoutHash != "" {
			values = append(values, result.StdoutHash)
		}
		if result.StderrHash != "" {
			values = append(values, result.StderrHash)
		}
	}
	sort.Strings(values)
	encoded, _ := json.Marshal(values)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validAdapter(value string) bool {
	switch value {
	case "gradle", "maven", "bazel", "nx", "pytest", "jest", "generic":
		return true
	default:
		return false
	}
}
