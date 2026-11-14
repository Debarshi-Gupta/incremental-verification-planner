//go:build performance

package performance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/gitutil"
)

// LLD 15.4 service levels: plan creation p95 <= 2 seconds for a loaded graph
// with <= 10,000 test units and <= 50,000 relevant edges, and adapter
// inventory/delta indexing p95 <= 30 seconds for <= 100 changed files.
//
// The measurements are taken over several samples and the p95 is computed from
// them; the threshold can be raised for a slower reference machine through
// IVP_SLO_PLAN_CREATION_P95_MS and IVP_SLO_INDEXING_P95_MS, but the shipped
// default is the LLD budget.
const (
	defaultPlanCreationP95 = 2 * time.Second
	defaultIndexingP95     = 30 * time.Second
)

// runGit executes a Git command in the repository root.
func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", fmt.Sprint(args), err, output)
	}
}

// writeFile writes a file inside the repository root.
func writeFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// detectChanges runs the real change detection over the repository.
func detectChanges(root string) ([]domain.ChangedFile, error) {
	changes, err := gitutil.DetectChanges(context.Background(), root, "HEAD")
	if err != nil {
		return nil, err
	}
	return changes.Files, nil
}

// percentile returns the p-th percentile of the samples (nearest-rank).
func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(float64(len(sorted))*p+0.9999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func thresholdFromEnv(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	millis, err := strconv.Atoi(value)
	if err != nil || millis <= 0 {
		return fallback
	}
	return time.Duration(millis) * time.Millisecond
}

// TestPlanCreationP95StaysWithinTheSLO measures the plan-creation path over a
// loaded graph of 10,000 test units and asserts the p95 against the LLD 15.4
// budget.
func TestPlanCreationP95StaysWithinTheSLO(t *testing.T) {
	if testing.Short() {
		t.Skip("performance measurement skipped in short mode")
	}
	const (
		unitCount = 10_000
		samples   = 5
	)
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	snapshotID := uuid.New()
	repositoryID := uuid.New()
	changedID := uuid.New()

	nodes := make([]domain.GraphNode, 0, unitCount+1)
	edges := make([]domain.GraphEdge, 0, unitCount*5)
	units := make([]domain.TestUnit, 0, unitCount)
	coverage := make([]domain.CoverageObservation, 0, unitCount)
	changedPath := "src/changed.go"
	nodes = append(nodes, domain.GraphNode{ID: changedID, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile,
		StableKey: "file:" + changedPath, Path: changedPath, EvidenceSource: "slo-measurement", Confidence: 1, ObservedAt: now})
	for i := range unitCount {
		testID := uuid.New()
		key := fmt.Sprintf("test:%05d", i)
		nodes = append(nodes, domain.GraphNode{ID: testID, SnapshotID: snapshotID, NodeType: domain.NodeTestTarget,
			StableKey: key, EvidenceSource: "slo-measurement", Confidence: 1, ObservedAt: now})
		// Relevant edges: the test covers the changed file and depends on it.
		edges = append(edges,
			domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: testID, ToNodeID: changedID, EdgeType: domain.EdgeTestCovers, Confidence: 1, EvidenceSource: "slo-measurement", ObservedAt: now},
			domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: testID, ToNodeID: changedID, EdgeType: domain.EdgeDependsOn, Confidence: 1, EvidenceSource: "slo-measurement", ObservedAt: now})
		units = append(units, domain.TestUnit{ID: testID, SnapshotID: snapshotID, StableKey: key, Adapter: "generic",
			UnitType: domain.UnitTestCase, EstimatedMillis: 100, DurationP95Millis: 120, DurationSampleCount: 5,
			Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: key, SourcePaths: []string{changedPath},
			Metadata: map[string]any{"test_config_digest": "test-config"}})
		coverage = append(coverage, domain.CoverageObservation{TestUnitKey: key, CoveredStableKey: "file:" + changedPath,
			CoveredPath: changedPath, SourceType: "slo-measurement", ReportHash: "report", RunID: uuid.New(), ObservedAt: now,
			EnvironmentFingerprint: "env", BuildConfigDigest: "build-config", TestConfigDigest: "test-config",
			Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1, TestSpecific: true, EvidenceConfidence: 1})
	}
	if len(edges) > 50_000 {
		t.Fatalf("the measurement graph has %d edges, want <= 50,000 relevant edges", len(edges))
	}
	snapshot := domain.RepositorySnapshot{ID: snapshotID, RepositoryID: repositoryID, CommitSHA: "head", BaseSHA: "base",
		TreeHash: "tree", WorktreeHash: "worktree", BuildConfigDigest: "build-config", TestConfigDigest: "test-config",
		AdapterSetDigest: "adapter-set", GraphVersion: 1, EvidenceGeneratedAt: now, IsComplete: true}
	evidence := analysis.EvidenceSnapshot{Snapshot: snapshot,
		ChangedFiles: []domain.ChangedFile{{Path: changedPath, Status: domain.ChangeModified}},
		Nodes:        nodes, Edges: edges, Units: units, Coverage: coverage,
		GraphComplete: true, DiscoveryReliable: true, PolicyTrusted: true, ToolchainComparable: true,
		AdapterCapabilityVersions: map[string]string{"generic": "v1"}}
	policy := domain.Policy{Version: 7, LowRiskMaxScore: 24, MediumRiskMaxScore: 59, LowRiskMinWeightedCoverage: 0.95,
		MediumRiskMinWeightedCoverage: 0.99, MinimumSelectionConfidence: 0.98, MaxSelectiveRuntimeFraction: 0.70,
		MaxChangedFilesForSelectivePlan: 500, DependencyFanoutEscalation: 250, DependencyCap: 100,
		MinimumCostFloorMillis: 1000, StartupOverheadMillis: 0, FullSuiteTargetRef: "full-suite", PolicyAvailable: true,
		HardFullSuitePaths: []string{"**/pom.xml"}, HardFullSuiteRiskLabels: []string{"SECURITY_CRITICAL", "PUBLIC_CONTRACT"}}
	request := domain.PlanRequest{RepositoryID: repositoryID, SnapshotID: snapshotID, BaseSHA: "base", HeadSHA: "head",
		CurrentWorktreeHash: "worktree", Mode: domain.ModeFastFeedback, Now: now, FullSuiteEstimateMillis: 100_000_000,
		AdapterID: "generic", AdapterSupported: true, AdapterDiscoveryOK: true, EnvironmentFingerprint: "env",
		PolicyVersion: policy.Version, PlanTTL: time.Minute, PolicySnapshotTrusted: true}

	durations := make([]time.Duration, 0, samples)
	for range samples {
		started := time.Now()
		plan, err := analysis.CreatePlan(request, evidence, policy)
		durations = append(durations, time.Since(started))
		if err != nil {
			t.Fatalf("plan creation failed: %v", err)
		}
		if plan.ID == uuid.Nil {
			t.Fatal("plan creation returned no plan")
		}
	}
	p95 := percentile(durations, 0.95)
	limit := thresholdFromEnv("IVP_SLO_PLAN_CREATION_P95_MS", defaultPlanCreationP95)
	t.Logf("plan creation over %d test units: p95=%s samples=%v", unitCount, p95, durations)
	if p95 > limit {
		t.Fatalf("plan creation p95 %s exceeds the LLD 15.4 budget %s", p95, limit)
	}
}

// TestIndexingP95StaysWithinTheSLO measures the delta-indexing path for a
// change set of 100 changed files and asserts the p95 against the LLD 15.4
// budget of 30 seconds. The measurement covers change detection over a real
// Git repository plus the snapshot assembly the index command performs.
func TestIndexingP95StaysWithinTheSLO(t *testing.T) {
	if testing.Short() {
		t.Skip("performance measurement skipped in short mode")
	}
	const (
		changedFiles = 100
		samples      = 3
	)
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "slo@example.invalid")
	runGit(t, root, "config", "user.name", "SLO measurement")
	for i := range changedFiles {
		writeFile(t, root, fmt.Sprintf("pkg/file%03d.go", i), "package pkg\n")
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "initial")
	for i := range changedFiles {
		writeFile(t, root, fmt.Sprintf("pkg/file%03d.go", i), fmt.Sprintf("package pkg\n// change %d\n", i))
	}

	durations := make([]time.Duration, 0, samples)
	for range samples {
		started := time.Now()
		changes, err := detectChanges(root)
		durations = append(durations, time.Since(started))
		if err != nil {
			t.Fatalf("change detection failed: %v", err)
		}
		if len(changes) != changedFiles {
			t.Fatalf("detected %d changed files, want %d", len(changes), changedFiles)
		}
	}
	p95 := percentile(durations, 0.95)
	limit := thresholdFromEnv("IVP_SLO_INDEXING_P95_MS", defaultIndexingP95)
	t.Logf("delta indexing over %d changed files: p95=%s samples=%v", changedFiles, p95, durations)
	if p95 > limit {
		t.Fatalf("delta indexing p95 %s exceeds the LLD 15.4 budget %s", p95, limit)
	}
}
