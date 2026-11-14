// Package integration holds the §17.3 fixture-repository integration tests.
// Unlike tests/e2e (which builds synthetic in-memory evidence), these tests
// operate on real fixture repositories under examples/, including real git
// change injection.
package integration

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/gitutil"
)

// Stable keys of the three declared test units in examples/sample-inject.
const (
	injectDirectKey    = "orders/test_core.py::test_total"
	injectIndirectKey  = "service/test_service.py::test_invoice_total"
	injectUnrelatedKey = "utils/test_report.py::test_render"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func copyInjectFixture(t *testing.T, dst string) {
	t.Helper()
	src := filepath.Join("..", "..", "examples", "sample-inject")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture repository: %v", err)
	}
}

// initInjectRepository copies examples/sample-inject into a fresh temporary
// directory and creates a real git repository with one baseline commit (c0).
// Repository-local settings keep the history deterministic on any host.
func initInjectRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	copyInjectFixture(t, repo)
	runGit(t, repo, "init")
	runGit(t, repo, "config", "core.autocrlf", "false")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	runGit(t, repo, "config", "user.email", "fixture@example.test")
	runGit(t, repo, "config", "user.name", "IVP Fixture")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "c0: fixture baseline")
	return repo
}

// buildInjectEvidence assembles the immutable evidence snapshot for one
// change set: file nodes for every fixture source, test-target nodes keyed to
// the declared units, TestCovers and DependsOn edges, the three declared test
// units, and a LINE coverage observation on the indirect unit whose line
// interval is read from the real injected diff hunks.
func buildInjectEvidence(t *testing.T, changes gitutil.ChangeSet, now time.Time, repoID uuid.UUID) (analysis.EvidenceSnapshot, map[string]domain.UUID) {
	t.Helper()
	snapshot := domain.RepositorySnapshot{
		ID: uuid.New(), RepositoryID: repoID,
		CommitSHA: changes.HeadSHA, BaseSHA: changes.BaseSHA,
		TreeHash: changes.TreeHash, WorktreeHash: changes.WorktreeHash,
		SubmoduleDigest:     changes.SubmoduleDigest,
		BuildConfigDigest:   "build-config",
		AdapterSetDigest:    "generic@1",
		GraphVersion:        1,
		EvidenceGeneratedAt: now,
		IsComplete:          true,
	}
	fileNode := func(key, path string) domain.GraphNode {
		return domain.GraphNode{ID: uuid.New(), SnapshotID: snapshot.ID, NodeType: domain.NodeSourceFile,
			StableKey: key, Path: path, EvidenceSource: "fixture-index", Confidence: 1, ObservedAt: now}
	}
	testNode := func(key string) domain.GraphNode {
		return domain.GraphNode{ID: uuid.New(), SnapshotID: snapshot.ID, NodeType: domain.NodeTestTarget,
			StableKey: key, EvidenceSource: "fixture-index", Confidence: 1, ObservedAt: now}
	}
	core := fileNode("file:orders/core.py", "orders/core.py")
	service := fileNode("file:service/service.py", "service/service.py")
	utilsReport := fileNode("file:utils/report.py", "utils/report.py")
	pyproject := fileNode("config:pyproject.toml", "pyproject.toml")
	direct := testNode(injectDirectKey)
	indirect := testNode(injectIndirectKey)
	unrelated := testNode(injectUnrelatedKey)
	nodes := []domain.GraphNode{core, service, utilsReport, pyproject, direct, indirect, unrelated}

	edge := func(from, to domain.UUID, kind domain.GraphEdgeType) domain.GraphEdge {
		return domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshot.ID, FromNodeID: from, ToNodeID: to,
			EdgeType: kind, Confidence: 1, EvidenceSource: "fixture-index", ObservedAt: now}
	}
	edges := []domain.GraphEdge{
		edge(direct.ID, core.ID, domain.EdgeTestCovers),
		edge(indirect.ID, service.ID, domain.EdgeTestCovers),
		edge(unrelated.ID, utilsReport.ID, domain.EdgeTestCovers),
		edge(service.ID, core.ID, domain.EdgeDependsOn),
	}

	unit := func(key string, samples int, p95 int64, sources []string) domain.TestUnit {
		return domain.TestUnit{ID: uuid.New(), SnapshotID: snapshot.ID, StableKey: key, Adapter: "generic",
			UnitType: domain.UnitTestCase, EstimatedMillis: 1000, DurationP95Millis: p95, DurationSampleCount: samples,
			Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: key, SourcePaths: sources,
			Metadata: map[string]any{"test_config_digest": "test-config-v1"}}
	}
	units := []domain.TestUnit{
		unit(injectDirectKey, 5, 1000, []string{"orders/core.py"}),
		unit(injectIndirectKey, 5, 1000, []string{"service/service.py"}),
		unit(injectUnrelatedKey, 0, 0, []string{"utils/report.py"}),
	}

	var coverage []domain.CoverageObservation
	if len(changes.Files) > 0 && len(changes.Files[0].DiffHunks) > 0 {
		hunk := changes.Files[0].DiffHunks[0]
		coverage = []domain.CoverageObservation{{
			TestUnitKey: injectIndirectKey, CoveredStableKey: "file:orders/core.py", CoveredPath: "orders/core.py",
			LineStart: hunk.Start, LineEnd: hunk.End, SourceType: "pytest-json", ReportHash: "report-sha256",
			RunID: uuid.New(), ObservedAt: now, EnvironmentFingerprint: "env",
			BuildConfigDigest: "build-config", TestConfigDigest: "test-config-v1",
			Precision: domain.CoverageLine, TestSpecific: true, EvidenceConfidence: 1,
		}}
	}

	evidence := analysis.EvidenceSnapshot{
		Snapshot: snapshot, ChangedFiles: changes.Files, Nodes: nodes, Edges: edges,
		Coverage: coverage, Units: units,
		GraphComplete: true, DiscoveryReliable: true, CoverageExpected: true,
		PolicyTrusted: true, ToolchainComparable: true, CapturedAt: now,
		AdapterCapabilityVersions: map[string]string{"generic": "1"},
	}
	ids := map[string]domain.UUID{
		"core": core.ID, "service": service.ID, "utils": utilsReport.ID,
		"direct": direct.ID, "indirect": indirect.ID, "unrelated": unrelated.ID,
	}
	return evidence, ids
}

func hasReason(codes []domain.SelectedReasonCode, want domain.SelectedReasonCode) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

func hasEscalation(reasons []domain.EscalationReason, want domain.EscalationReasonCode) bool {
	for _, reason := range reasons {
		if reason.Code == want {
			return true
		}
	}
	return false
}

// TestChangeInjectionSelectsDirectAndIndirectTests implements inventory row
// IVP-17-3-bc8538a00a (§17.3): a real git repository is built from
// examples/sample-inject, a known change is injected into orders/core.py, and
// the resulting change set must drive a READY selective plan that contains
// both the directly changed test and the reverse-dependency consumer test
// while omitting the unrelated test. Injecting a second change into
// pyproject.toml must escalate to FULL_REQUIRED with
// CONFIG_OR_TOOLCHAIN_CHANGE.
func TestChangeInjectionSelectsDirectAndIndirectTests(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for change injection: %v", err)
	}
	ctx := context.Background()
	repo := initInjectRepository(t)

	// Inject a known change into orders/core.py and commit it as c1.
	corePath := filepath.Join(repo, "orders", "core.py")
	original, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatalf("read orders/core.py: %v", err)
	}
	injected := append(append([]byte(nil), original...),
		[]byte("\ndef grand_total(items):\n    return total(apply_discount(items, 0.1))\n")...)
	if err := os.WriteFile(corePath, injected, 0o644); err != nil {
		t.Fatalf("write orders/core.py: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "c1: inject change into orders/core.py")

	changes, err := gitutil.DetectChanges(ctx, repo, "HEAD~1")
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	if len(changes.Files) != 1 || changes.Files[0].Path != "orders/core.py" ||
		changes.Files[0].Status != domain.ChangeModified {
		t.Fatalf("injected change set = %+v, want one MODIFIED orders/core.py", changes.Files)
	}
	if len(changes.Files[0].DiffHunks) == 0 {
		t.Fatalf("injected change must expose at least one diff hunk for line coverage: %+v", changes.Files[0])
	}
	if changes.BaseSHA == "" || changes.HeadSHA == "" || changes.BaseSHA == changes.HeadSHA {
		t.Fatalf("change set must carry distinct base/head SHAs: base=%q head=%q", changes.BaseSHA, changes.HeadSHA)
	}

	// Effective policy from product defaults with a configured full-suite
	// argv and no mandatory check IDs (the fixture declares none).
	cfg := config.Defaults()
	cfg.Execution.FullSuiteArgv = []string{"python3", "-m", "pytest", "-q"}
	cfg.Safety.AlwaysRunCheckIDs = nil
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("injection policy config: %v", err)
	}
	policy := config.ToDomainPolicy(cfg, 1, nil)
	if policy.FullSuiteTargetRef != "full-suite" {
		t.Fatalf("policy full-suite target ref = %q, want full-suite", policy.FullSuiteTargetRef)
	}

	now := time.Now().UTC()
	evidence, ids := buildInjectEvidence(t, changes, now, uuid.New())

	// Direct impact analysis: exactly the changed file is an entity, and the
	// reverse dependency walk reaches the service plus both of its tests.
	impact := analysis.AnalyzeImpact(changes.Files, evidence.Nodes, evidence.Edges, policy, now, true, false, false, false)
	if len(impact.Entities) != 1 || impact.Entities[0].Path != "orders/core.py" {
		t.Fatalf("impact entities = %+v, want the single changed file orders/core.py", impact.Entities)
	}
	if impact.Entities[0].Weight < 0.99 {
		t.Fatalf("sole changed entity weight = %f, want ~1.0 after normalization", impact.Entities[0].Weight)
	}
	if impact.ReverseFanout != 1 {
		t.Fatalf("reverse fanout = %d, want 1 (service depends on orders/core.py)", impact.ReverseFanout)
	}
	wantImpacted := map[domain.UUID]bool{ids["core"]: true, ids["service"]: true, ids["direct"]: true, ids["indirect"]: true}
	if len(impact.ImpactedNodeIDs) != len(wantImpacted) {
		t.Fatalf("impacted node count = %d, want %d (core, service, direct test, indirect test)", len(impact.ImpactedNodeIDs), len(wantImpacted))
	}
	for id := range wantImpacted {
		if !impact.ImpactedNodeIDs[id] {
			t.Fatalf("expected node %s to be impacted: %+v", id, impact.ImpactedNodeIDs)
		}
	}

	req := domain.PlanRequest{
		RepositoryID: evidence.Snapshot.RepositoryID, SnapshotID: evidence.Snapshot.ID,
		BaseSHA: changes.BaseSHA, HeadSHA: changes.HeadSHA,
		CurrentWorktreeHash: changes.WorktreeHash,
		Mode:                domain.ModeFastFeedback, Now: now,
		FullSuiteEstimateMillis: 60_000,
		AdapterID:               "generic", AdapterSupported: true, AdapterDiscoveryOK: true,
		EnvironmentFingerprint: "env",
		PolicyVersion:          1, PlanTTL: 5 * time.Minute,
		PolicySnapshotTrusted: true,
	}
	plan, err := analysis.CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("injected change produced %s instead of READY: escalations=%+v explanation=%s",
			plan.Status, plan.EscalationReasons, plan.Explanation)
	}
	if len(plan.EscalationReasons) != 0 {
		t.Fatalf("READY plan must carry no escalation reasons: %+v", plan.EscalationReasons)
	}
	for _, check := range plan.HardRuleChecks {
		if check.Triggered {
			t.Fatalf("hard rule %s must not trigger on the injected source change: %+v", check.Code, check)
		}
	}
	if plan.Mode != domain.ModeFastFeedback || plan.RiskLevel != domain.RiskMedium {
		t.Fatalf("plan mode/risk = %s/%s, want FAST_FEEDBACK/MEDIUM (contributions %+v)",
			plan.Mode, plan.RiskLevel, plan.RiskContributions)
	}
	if plan.RiskScore != 27 {
		t.Fatalf("risk score = %d, want 27 (contributions %+v)", plan.RiskScore, plan.RiskContributions)
	}
	if plan.WeightedCoverage < policy.MediumRiskMinWeightedCoverage {
		t.Fatalf("weighted coverage = %f, want >= %f", plan.WeightedCoverage, policy.MediumRiskMinWeightedCoverage)
	}
	if plan.SelectionConfidence < policy.MinimumSelectionConfidence {
		t.Fatalf("selection confidence = %f, want >= %f", plan.SelectionConfidence, policy.MinimumSelectionConfidence)
	}
	if plan.EstimatedDurationMillis != 4000 {
		t.Fatalf("estimated duration = %d ms, want 4000 ms (2 x 1000 ms P95 + 2 x 1000 ms startup overhead)", plan.EstimatedDurationMillis)
	}
	if len(plan.MandatoryUnits) != 0 {
		t.Fatalf("fixture declares no mandatory checks but plan has %+v", plan.MandatoryUnits)
	}
	if plan.FullSuiteFallbackRef != "full-suite" {
		t.Fatalf("full-suite fallback ref = %q, want full-suite", plan.FullSuiteFallbackRef)
	}

	if len(plan.SelectedUnits) != 2 {
		t.Fatalf("selected %d units, want 2 (direct + indirect): %+v", len(plan.SelectedUnits), plan.SelectedUnits)
	}
	byKey := map[string]domain.PlanUnit{}
	for _, unit := range plan.SelectedUnits {
		byKey[unit.StableKey] = unit
	}
	directUnit, ok := byKey[injectDirectKey]
	if !ok {
		t.Fatalf("directly changed test %q missing from selection: %+v", injectDirectKey, plan.SelectedUnits)
	}
	if !hasReason(directUnit.ReasonCodes, domain.ReasonDirectlyChangedTest) {
		t.Fatalf("direct unit reason codes = %v, want DIRECTLY_CHANGED_TEST", directUnit.ReasonCodes)
	}
	indirectUnit, ok := byKey[injectIndirectKey]
	if !ok {
		t.Fatalf("reverse-dependency consumer test %q missing from selection: %+v", injectIndirectKey, plan.SelectedUnits)
	}
	if !hasReason(indirectUnit.ReasonCodes, domain.ReasonReverseDependencyConsumer) {
		t.Fatalf("indirect unit reason codes = %v, want REVERSE_DEPENDENCY_CONSUMER", indirectUnit.ReasonCodes)
	}

	if len(plan.OmittedUnits) != 1 {
		t.Fatalf("omitted %d units, want 1 (unrelated): %+v", len(plan.OmittedUnits), plan.OmittedUnits)
	}
	omitted := plan.OmittedUnits[0]
	if omitted.StableKey != injectUnrelatedKey ||
		len(omitted.ReasonCodes) != 1 || omitted.ReasonCodes[0] != domain.OmitOutsideAffectedTargets {
		t.Fatalf("omitted unit = %+v, want %s with OUTSIDE_AFFECTED_TARGETS", omitted, injectUnrelatedKey)
	}

	// A second injection into a hard full-suite path must escalate.
	t.Run("pyproject injection escalates to full suite", func(t *testing.T) {
		pyprojectPath := filepath.Join(repo, "pyproject.toml")
		data, err := os.ReadFile(pyprojectPath)
		if err != nil {
			t.Fatalf("read pyproject.toml: %v", err)
		}
		appended := append(append([]byte(nil), data...), []byte("\n# injected toolchain change\n")...)
		if err := os.WriteFile(pyprojectPath, appended, 0o644); err != nil {
			t.Fatalf("write pyproject.toml: %v", err)
		}
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "-m", "c2: inject config change into pyproject.toml")

		changes2, err := gitutil.DetectChanges(ctx, repo, "HEAD~1")
		if err != nil {
			t.Fatalf("DetectChanges (c2): %v", err)
		}
		if len(changes2.Files) != 1 || changes2.Files[0].Path != "pyproject.toml" {
			t.Fatalf("second injected change set = %+v, want one MODIFIED pyproject.toml", changes2.Files)
		}

		now2 := time.Now().UTC()
		evidence2, _ := buildInjectEvidence(t, changes2, now2, evidence.Snapshot.RepositoryID)
		req2 := req
		req2.SnapshotID = evidence2.Snapshot.ID
		req2.BaseSHA = changes2.BaseSHA
		req2.HeadSHA = changes2.HeadSHA
		req2.CurrentWorktreeHash = changes2.WorktreeHash
		req2.Now = now2
		plan2, err := analysis.CreatePlan(req2, evidence2, policy)
		if err != nil {
			t.Fatalf("CreatePlan (c2): %v", err)
		}
		if plan2.Status != domain.PlanFullRequired {
			t.Fatalf("pyproject.toml change produced %s instead of FULL_REQUIRED: explanation=%s",
				plan2.Status, plan2.Explanation)
		}
		if !hasEscalation(plan2.EscalationReasons, domain.EscConfigOrToolchainChange) {
			t.Fatalf("escalations = %+v, want CONFIG_OR_TOOLCHAIN_CHANGE", plan2.EscalationReasons)
		}
	})
}
