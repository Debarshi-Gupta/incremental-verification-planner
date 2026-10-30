package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/execution"
	"github.com/ivp/ivp/internal/gitutil"
)

func (c *commands) audit(ctx context.Context, out io.Writer, cfg config.Config, selectedRef, fullRef, adapterOverride, ruleScope string) error {
	root, err := gitutil.RepositoryRoot(ctx, ".")
	if err != nil {
		return exitError{2, err}
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
	var full *localRun
	if strings.TrimSpace(fullRef) != "" {
		full, err = findLocalRun(root, fullRef)
		if err != nil {
			return err
		}
	} else {
		if cfg.FullSuiteArgv() == nil || len(cfg.FullSuiteArgv()) == 0 {
			return exitError{3, errors.New("audit requires execution.full_suite_argv")}
		}
		fmt.Fprintf(out, "Running the saved plan as a full suite for audit comparison.\n")
		runErr := c.runPlan(ctx, out, cfg, plan, "", true)
		if runErr != nil && ExitCode(runErr) != 1 {
			return runErr
		}
		full, err = newestFullRun(root, plan.ID, selected.ExecutionID)
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
		if err := uploadLocalResult(ctx, out, client, plan.RepositoryID, full, localRunPath(root, full.LocalID), out); err != nil {
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
	_, record, err := loadIndex(root)
	if err != nil {
		return err
	}
	if record.Snapshot.ID != plan.SnapshotID {
		return exitError{5, errors.New("local snapshot does not match the plan; an equivalent-environment audit cannot be established")}
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
		TestConfigDigest:       record.Snapshot.BuildConfigDigest,
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
	failureByAdapter := map[string][]audit.Failure{}
	for _, result := range full.UnitResults {
		if result.Status != "FAILED" && result.Status != "ERROR" {
			continue
		}
		if selectedSet[result.TestUnitKey] {
			continue
		}
		adapter := unitAdapter[result.TestUnitKey]
		if adapter == "" {
			continue
		}
		failureByAdapter[adapter] = append(failureByAdapter[adapter], audit.Failure{TestUnitKey: result.TestUnitKey, Reproduced: false, RelatedToChange: false})
	}
	if full.Outcome == domain.OutcomeFullVerificationFail {
		if full.Summary.FailureClasses["FULL_SUITE_FAILURE_DETAILS_INCOMPLETE"] > 0 {
			return exitError{4, errors.New("full-suite report could not map every failing test to an immutable plan unit; audit was not submitted")}
		}
		if len(failureByAdapter) == 0 {
			return exitError{4, errors.New("full suite failed but its reports did not identify an omitted failing plan unit; audit was not submitted")}
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
		return exitError{1, errors.New("audit found possible or confirmed misses; the affected adapter scope is quarantined")}
	}
	return nil
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
