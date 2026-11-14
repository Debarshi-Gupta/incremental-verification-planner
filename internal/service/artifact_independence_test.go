package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/artifact"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
)

// failingArtifactStore rejects every write. It models an artifact storage
// outage: artifact storage is optional for plan correctness, so the failure
// must never change a plan.
type failingArtifactStore struct{}

func (failingArtifactStore) Put(context.Context, string, string, io.Reader, int64) (artifact.Metadata, error) {
	return artifact.Metadata{}, errors.New("artifact storage is unavailable")
}

func (failingArtifactStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, artifact.ErrUnavailable
}

func (failingArtifactStore) Delete(context.Context, string) error { return artifact.ErrUnavailable }

// artifactIndependenceFixture builds a deterministic planner input: one
// changed source file, one discovered test unit, and one comparable coverage
// observation.
func artifactIndependenceFixture(t *testing.T) (domain.PlanRequest, analysis.EvidenceSnapshot, domain.Policy) {
	t.Helper()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	repositoryID, snapshotID, unitID := uuid.New(), uuid.New(), uuid.New()
	sourceID, testID := uuid.New(), uuid.New()
	unit := domain.TestUnit{ID: unitID, SnapshotID: snapshotID, StableKey: "test:unit", Adapter: "generic", UnitType: domain.UnitTestCase,
		EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5, Selectable: true,
		SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:unit", SourcePaths: []string{"src/main.go"},
		Metadata: map[string]any{"test_config_digest": "test-config"}}
	source := domain.GraphNode{ID: sourceID, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:src/main.go", Path: "src/main.go",
		EvidenceSource: "fixture", Confidence: 1, ObservedAt: now}
	testNode := domain.GraphNode{ID: testID, SnapshotID: snapshotID, NodeType: domain.NodeTestTarget, StableKey: "test:unit", EvidenceSource: "fixture", Confidence: 1, ObservedAt: now}
	snapshot := domain.RepositorySnapshot{ID: snapshotID, RepositoryID: repositoryID, CommitSHA: "head", BaseSHA: "base", TreeHash: "tree",
		WorktreeHash: "worktree", BuildConfigDigest: "build-config", AdapterSetDigest: "adapter-set", GraphVersion: 1,
		EvidenceGeneratedAt: now, IsComplete: true}
	observation := domain.CoverageObservation{TestUnitKey: unit.StableKey, CoveredStableKey: source.StableKey, CoveredPath: source.Path,
		SourceType: "fixture", ReportHash: "report-digest", RunID: uuid.New(), ObservedAt: now, EnvironmentFingerprint: "env",
		BuildConfigDigest: "build-config", TestConfigDigest: "test-config", Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1,
		TestSpecific: true, EvidenceConfidence: 1}
	evidence := analysis.EvidenceSnapshot{Snapshot: snapshot, ChangedFiles: []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified}},
		Nodes: []domain.GraphNode{source, testNode}, Edges: []domain.GraphEdge{{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: testID, ToNodeID: sourceID,
			EdgeType: domain.EdgeTestCovers, Confidence: 1, EvidenceSource: "fixture", ObservedAt: now}},
		Coverage: []domain.CoverageObservation{observation}, Units: []domain.TestUnit{unit},
		History:       []domain.HistoryObservation{{TestUnitKey: unit.StableKey, RelatedStableKey: "src/main.go", Comparable: true, Outcome: "PASS", SampleCount: 5, ObservedAt: now}},
		GraphComplete: true, DiscoveryReliable: true, PolicyTrusted: true, ToolchainComparable: true, AdapterCapabilityVersions: map[string]string{"generic": "v1"}}
	policy := domain.Policy{Version: 7, LowRiskMaxScore: 24, MediumRiskMaxScore: 59, LowRiskMinWeightedCoverage: 0.95,
		MediumRiskMinWeightedCoverage: 0.99, MinimumSelectionConfidence: 0.98, MaxSelectiveRuntimeFraction: 0.70,
		MaxChangedFilesForSelectivePlan: 500, GraphStaleness: 24 * time.Hour, CoverageStaleness: 30 * 24 * time.Hour,
		DependencyFanoutEscalation: 250, DependencyCap: 100, MinimumCostFloorMillis: 1000, StartupOverheadMillis: 0,
		HardFullSuitePaths: []string{"**/pom.xml"}, HardFullSuiteRiskLabels: []string{"SECURITY_CRITICAL", "PUBLIC_CONTRACT"},
		FullSuiteTargetRef: "full-suite", PolicyAvailable: true, HighRiskSelectiveFeedbackAllowed: true}
	request := domain.PlanRequest{RepositoryID: repositoryID, SnapshotID: snapshotID, BaseSHA: "base", HeadSHA: "head", CurrentWorktreeHash: "worktree",
		Mode: domain.ModeFastFeedback, Now: now, FullSuiteEstimateMillis: 100_000, AdapterID: "generic", AdapterSupported: true,
		AdapterDiscoveryOK: true, EnvironmentFingerprint: "env", PolicyVersion: policy.Version, PlanTTL: 10 * time.Minute, PolicySnapshotTrusted: true}
	return request, evidence, policy
}

// planDecision is the part of a plan that decides what is executed. It must
// never depend on artifact storage.
type planDecision struct {
	Status              domain.PlanStatus
	Mode                domain.PlanMode
	RiskScore           int
	RiskLevel           domain.RiskLevel
	WeightedCoverage    float64
	SelectionConfidence float64
	SelectedUnits       []domain.PlanUnit
	MandatoryUnits      []domain.PlanUnit
	OmittedUnits        []domain.OmittedUnit
	EscalationReasons   []domain.EscalationReason
	HardRuleChecks      []domain.HardRuleCheck
	Explanation         string
	AlgorithmVersion    string
	PolicyVersion       int64
}

func decisionOf(plan domain.VerificationPlan) planDecision {
	return planDecision{Status: plan.Status, Mode: plan.Mode, RiskScore: plan.RiskScore, RiskLevel: plan.RiskLevel,
		WeightedCoverage: plan.WeightedCoverage, SelectionConfidence: plan.SelectionConfidence, SelectedUnits: plan.SelectedUnits,
		MandatoryUnits: plan.MandatoryUnits, OmittedUnits: plan.OmittedUnits, EscalationReasons: plan.EscalationReasons,
		HardRuleChecks: plan.HardRuleChecks, Explanation: plan.Explanation, AlgorithmVersion: plan.AlgorithmVersion,
		PolicyVersion: plan.PolicyVersion}
}

// TestArtifactStoreFailureAndLossDoNotChangeThePlan is the regression test for
// LLD 2.2: "Artifact storage is optional for plan correctness; losing an
// artifact must not change the plan."
func TestArtifactStoreFailureAndLossDoNotChangeThePlan(t *testing.T) {
	request, evidence, policy := artifactIndependenceFixture(t)
	baseline, err := analysis.CreatePlan(request, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Status != domain.PlanReady {
		t.Fatalf("the fixture must produce an executable plan: %+v", baseline.EscalationReasons)
	}
	tenant, user, execution := uuid.New(), uuid.New(), uuid.New()
	repository := &memoryArtifactRepository{}
	store := &memoryArtifactStore{}
	manager := &ArtifactManager{DB: repository, Store: store, MaxBytes: 4096, Retention: time.Hour}
	info, err := manager.Upload(context.Background(), tenant, user, execution, "application/json", strings.NewReader(`{"tests":[{"name":"ok","status":"passed"}]}`))
	if err != nil {
		t.Fatalf("the artifact upload must succeed before the loss: %v", err)
	}
	row, ok := repository.rows[info.ID]
	if !ok {
		t.Fatal("the artifact metadata was not stored")
	}
	// The artifact is lost: the object and its metadata disappear.
	if err := store.Delete(context.Background(), row.ObjectKey); err != nil {
		t.Fatal(err)
	}
	delete(repository.rows, info.ID)
	if _, _, err := manager.Open(context.Background(), tenant, info.ID); err == nil {
		t.Fatal("the lost artifact must not be served")
	}
	// A failing artifact store must be reported honestly.
	failing := &ArtifactManager{DB: &memoryArtifactRepository{}, Store: failingArtifactStore{}, MaxBytes: 4096, Retention: time.Hour}
	if _, err := failing.Upload(context.Background(), tenant, user, execution, "application/json", strings.NewReader(`{"tests":[{"name":"ok","status":"passed"}]}`)); err == nil {
		t.Fatal("an unavailable artifact store must be reported")
	}
	// The plan is created again from the identical evidence.
	afterLoss, err := analysis.CreatePlan(request, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decisionOf(baseline), decisionOf(afterLoss)) {
		t.Fatalf("losing an artifact changed the plan:\nbefore=%+v\nafter=%+v", decisionOf(baseline), decisionOf(afterLoss))
	}
	// The API surface behaves the same way: the upload fails loudly and the
	// plan is unaffected.
	router := api.NewRouter(api.Options{Backend: &workflowBackend{}, Artifacts: failing,
		Authenticator: workflowAuthenticator(uuid.NewString(), security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 4096})
	request2 := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+execution.String()+"/artifacts", strings.NewReader(`{"tests":[{"name":"ok","status":"passed"}]}`))
	request2.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request2)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("the artifact outage must be reported: status=%d body=%s", response.Code, response.Body.String())
	}
	afterOutage, err := analysis.CreatePlan(request, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decisionOf(baseline), decisionOf(afterOutage)) {
		t.Fatalf("an artifact outage changed the plan:\nbefore=%+v\nafter=%+v", decisionOf(baseline), decisionOf(afterOutage))
	}
}
