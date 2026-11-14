package analysis

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func TestCreatePlanBuildsExplainableTargetedPass(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("expected READY, got %s with reasons %+v", plan.Status, plan.EscalationReasons)
	}
	if len(plan.SelectedUnits) != 1 || plan.SelectedUnits[0].StableKey != "test:unit" {
		t.Fatalf("unexpected selected units: %+v", plan.SelectedUnits)
	}
	if plan.WeightedCoverage < 0.95 || plan.SelectionConfidence < 0.98 {
		t.Fatalf("plan missed policy gates: coverage=%v confidence=%v", plan.WeightedCoverage, plan.SelectionConfidence)
	}
	if !strings.Contains(plan.Explanation, "TARGETED_PASS") || !strings.Contains(plan.Explanation, "nothing about unselected tests") {
		t.Fatalf("explanation does not distinguish selective from full-suite outcomes: %q", plan.Explanation)
	}
	if plan.AlgorithmVersion != AlgorithmVersion || len(plan.EvidenceReferences) < 2 {
		t.Fatalf("missing planner version or evidence references: %+v", plan)
	}
	for _, check := range plan.HardRuleChecks {
		if check.Triggered {
			t.Errorf("READY plan contains triggered hard rule %s", check.Code)
		}
	}
}

func TestCreatePlanEscalatesForStaleFingerprintAndUnavailableFallback(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	req.CurrentWorktreeHash = "changed-after-snapshot"
	policy.FullSuiteTargetRef = ""
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("unsafe evidence must require full verification, got %s", plan.Status)
	}
	if !hasReason(plan, domain.EscPlanStale) || !hasReason(plan, domain.EscPolicyUnavailable) {
		t.Fatalf("expected stale and unavailable-policy escalation, got %+v", plan.EscalationReasons)
	}
	if len(plan.SelectedUnits) == 0 || plan.SelectedUnits[0].StableKey != "FULL_SUITE" {
		t.Fatalf("full-suite fallback target missing: %+v", plan.SelectedUnits)
	}
	if plan.SelectedUnits[0].TargetRef != "" {
		t.Fatalf("planner invented a missing local command target: %+v", plan.SelectedUnits[0])
	}
}

func TestCreatePlanEscalatesWhenHardPathChangesOrGraphIsDangling(t *testing.T) {
	t.Run("hard path", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles = []domain.ChangedFile{{Path: "pom.xml", Status: domain.ChangeModified}}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscConfigOrToolchainChange) {
			t.Fatalf("hard path did not force full verification: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
	})

	t.Run("dangling edge", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.Edges = append(evidence.Edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID,
			FromNodeID: uuid.New(), ToNodeID: evidence.Nodes[0].ID, EdgeType: domain.EdgeDependsOn, Confidence: 1, EvidenceSource: "fixture"})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscGraphIncomplete) {
			t.Fatalf("dangling graph edge did not force full verification: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
	})
}

func TestCreatePlanOrderingAndReasonsAreDeterministic(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	first, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != second.Status || first.RiskScore != second.RiskScore || first.RiskContributions[0] != second.RiskContributions[0] ||
		!reflect.DeepEqual(first.SelectedUnits, second.SelectedUnits) || !reflect.DeepEqual(first.HardRuleChecks, second.HardRuleChecks) ||
		!reflect.DeepEqual(first.EscalationReasons, second.EscalationReasons) {
		t.Fatalf("same normalized evidence produced different plans:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

// P0-6a regression: escalation detail strings for unrunnable mandatory checks
// were built by iterating a map, so identical inputs produced byte-different
// plans. Detail emission must be sorted and reproducible (LLD 6.3.9).
func TestMandatoryUnrunnableEscalationDetailIsDeterministic(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	keys := []string{"check:alpha", "check:bravo", "check:charlie", "check:delta"}
	for _, key := range keys {
		evidence.Units = append(evidence.Units, domain.TestUnit{
			ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, StableKey: key, Adapter: "generic",
			UnitType: domain.UnitCheck, EstimatedMillis: 500, DurationP95Millis: 500,
			DurationSampleCount: 5, Selectable: false, Mandatory: true, TargetRef: key,
		})
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, "mandatory check is not safely runnable at the discovered granularity: "+key)
	}
	want := strings.Join(parts, "; ")
	var seen string
	for run := 0; run < 20; run++ {
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		var detail string
		for _, reason := range plan.EscalationReasons {
			if reason.Code == domain.EscMandatoryCheckUnresolved {
				detail = reason.Detail
			}
		}
		if detail == "" {
			t.Fatalf("run %d: EscMandatoryCheckUnresolved missing from %+v", run, plan.EscalationReasons)
		}
		if run == 0 {
			seen = detail
			if detail != want {
				t.Fatalf("escalation detail not in sorted key order:\n got: %s\nwant: %s", detail, want)
			}
		} else if detail != seen {
			t.Fatalf("run %d produced a different escalation detail:\nfirst: %s\nthis:  %s", run, seen, detail)
		}
	}
}

// P0-6b regression: evidence references that share kind, stable key, and
// digest must order by observation time, so permuted input slices cannot
// change the emitted plan (LLD 6.3.9 reproducibility).
func TestEvidenceReferencesStableOrderForEqualKeys(t *testing.T) {
	_, evidence, _ := readyFixture(t)
	sharedRun := uuid.New()
	early := domain.CoverageObservation{TestUnitKey: "test:unit", ReportHash: "same-digest", RunID: sharedRun,
		ObservedAt: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}
	late := domain.CoverageObservation{TestUnitKey: "test:unit", ReportHash: "same-digest", RunID: sharedRun,
		ObservedAt: time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)}

	forward := evidence
	forward.Coverage = []domain.CoverageObservation{early, late}
	backward := evidence
	backward.Coverage = []domain.CoverageObservation{late, early}
	impactAB := ImpactResult{Entities: []ImpactEntity{{StableKey: "file:z", Weight: 1}, {StableKey: "file:a", Weight: 1}}}
	impactBA := ImpactResult{Entities: []ImpactEntity{{StableKey: "file:a", Weight: 1}, {StableKey: "file:z", Weight: 1}}}

	refsAB := evidenceRefs(forward, impactAB)
	refsBA := evidenceRefs(backward, impactBA)
	if !reflect.DeepEqual(refsAB, refsBA) {
		t.Fatalf("permuted inputs produced different evidence references:\nAB=%+v\nBA=%+v", refsAB, refsBA)
	}
	var matched []domain.EvidenceReference
	for _, ref := range refsAB {
		if ref.Kind == "COVERAGE_REPORT" && ref.Digest == "same-digest" {
			matched = append(matched, ref)
		}
	}
	if len(matched) != 2 || !matched[0].ObservedAt.Before(matched[1].ObservedAt) {
		t.Fatalf("equal-key evidence references are not ordered by ObservedAt: %+v", matched)
	}
}

// LLD 6.3 hard escalation rule 11: a selection without proven runtime
// advantage inside the configured safety fraction must fall back to full
// verification.
func TestCreatePlanEscalatesWhenSelectionLacksRuntimeAdvantage(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	// 80s selected versus a 100s full suite exceeds the 0.70 safety fraction.
	evidence.Units[0].EstimatedMillis = 80_000
	evidence.Units[0].DurationP95Millis = 80_000
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscPlanEconomicallyUnhelpful) {
		t.Fatalf("selection without runtime advantage did not escalate: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
	}
}

func TestGreedySetCoverUsesStableTieBreak(t *testing.T) {
	policy := domain.Policy{MinimumCostFloorMillis: 1}
	a := &candidate{unit: domain.TestUnit{StableKey: "a"}, duration: 10, coverage: map[string]float64{"file:x": 1}}
	b := &candidate{unit: domain.TestUnit{StableKey: "b"}, duration: 10, coverage: map[string]float64{"file:x": 1}}
	entities := []ImpactEntity{{StableKey: "file:x", Weight: 1}}
	selected, coverage, _, impossible := greedySetCover([]*candidate{b, a}, entities, 0.95, policy)
	if impossible || coverage != 1 || len(selected) != 1 || selected[0].unit.StableKey != "a" {
		t.Fatalf("expected stable-key tie break to select a, got %+v coverage=%v impossible=%v", selected, coverage, impossible)
	}
}

func readyFixture(t *testing.T) (domain.PlanRequest, EvidenceSnapshot, domain.Policy) {
	t.Helper()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
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
		BuildConfigDigest: "build-config", TestConfigDigest: "test-config", Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1, TestSpecific: true,
		EvidenceConfidence: 1}
	evidence := EvidenceSnapshot{Snapshot: snapshot, ChangedFiles: []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified}},
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
	req := domain.PlanRequest{RepositoryID: repositoryID, SnapshotID: snapshotID, BaseSHA: "base", HeadSHA: "head", CurrentWorktreeHash: "worktree",
		Mode: domain.ModeFastFeedback, Now: now, FullSuiteEstimateMillis: 100_000, AdapterID: "generic", AdapterSupported: true,
		AdapterDiscoveryOK: true, EnvironmentFingerprint: "env", PolicyVersion: policy.Version, PlanTTL: 10 * time.Minute, PolicySnapshotTrusted: true}
	return req, evidence, policy
}

func hasReason(plan domain.VerificationPlan, code domain.EscalationReasonCode) bool {
	for _, reason := range plan.EscalationReasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func hasSelectedReason(plan domain.VerificationPlan, stableKey string, code domain.SelectedReasonCode) bool {
	for _, unit := range plan.SelectedUnits {
		if unit.StableKey != stableKey {
			continue
		}
		for _, reason := range unit.ReasonCodes {
			if reason == code {
				return true
			}
		}
	}
	return false
}

func omittedReason(plan domain.VerificationPlan, stableKey string) (domain.OmissionReasonCode, bool) {
	for _, unit := range plan.OmittedUnits {
		if unit.StableKey != stableKey {
			continue
		}
		for _, code := range unit.ReasonCodes {
			return code, true
		}
		return "", false
	}
	return "", false
}

// WP-B: a declared request fingerprint whose indexed snapshot toolchain is not
// comparable cannot justify a selective plan, because no environment-sensitive
// evidence in the snapshot can be trusted for this request (ADR 0013).
func TestCreatePlanEscalatesWhenEnvironmentFingerprintIsNotComparable(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	evidence.ToolchainComparable = false
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscEnvironmentMismatch) {
		t.Fatalf("incomparable fingerprint must fall back to full verification: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
	}

	// The same request with a comparable toolchain records the check but does
	// not escalate on it.
	req, evidence, policy = readyFixture(t)
	comparable, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if hasReason(comparable, domain.EscEnvironmentMismatch) {
		t.Fatalf("comparable fingerprint escalated: %+v", comparable.EscalationReasons)
	}
	var check *domain.HardRuleCheck
	for i := range comparable.HardRuleChecks {
		if comparable.HardRuleChecks[i].Code == domain.EscEnvironmentMismatch {
			check = &comparable.HardRuleChecks[i]
		}
	}
	if check == nil {
		t.Fatalf("ENVIRONMENT_MISMATCH hard rule was never evaluated: %+v", comparable.HardRuleChecks)
	}
	if check.Triggered {
		t.Fatalf("ENVIRONMENT_MISMATCH triggered for a comparable toolchain: %+v", check)
	}
}

// LLD 17.4: a changed public contract expands the candidate set to its
// consumers. The consumer file itself is untouched, so only the
// CONTRACT_CONSUMER edge can reach it.
func TestContractConsumerEdgeExpandsCandidates(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	snapshotID := evidence.Snapshot.ID
	evidence.ChangedFiles = []domain.ChangedFile{{Path: "api/petstore.yaml", Status: domain.ChangeModified}}
	schema := domain.GraphNode{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeAPISchema,
		StableKey: "api:api/petstore.yaml", Path: "api/petstore.yaml", EvidenceSource: "fixture", Confidence: 1, ObservedAt: req.Now}
	consumer := domain.GraphNode{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeSourceFile,
		StableKey: "file:client/petstore.go", Path: "client/petstore.go", EvidenceSource: "fixture", Confidence: 1, ObservedAt: req.Now}
	evidence.Nodes = append(evidence.Nodes, schema, consumer)
	evidence.Edges = append(evidence.Edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID,
		FromNodeID: consumer.ID, ToNodeID: schema.ID, EdgeType: domain.EdgeContractConsumer,
		Confidence: 0.7, EvidenceSource: "conventions:schema-reference", ObservedAt: req.Now})
	evidence.Units = append(evidence.Units, domain.TestUnit{ID: uuid.New(), SnapshotID: snapshotID, StableKey: "test:client",
		Adapter: "generic", UnitType: domain.UnitTestCase, EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5,
		Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:client", SourcePaths: []string{"client/petstore.go"}})

	// The consumer is the only evidence for the schema, so the policy accepts
	// the coverage the convention edge itself provides; this isolates the
	// expansion behavior from the default coverage thresholds.
	policy.LowRiskMinWeightedCoverage = 0.4
	policy.MediumRiskMinWeightedCoverage = 0.4
	policy.MinimumSelectionConfidence = 0.85

	impact := AnalyzeImpact(evidence.ChangedFiles, evidence.Nodes, evidence.Edges, policy, req.Now, true, false, false, false)
	entityByPath := map[string]ImpactEntity{}
	for _, entity := range impact.Entities {
		entityByPath[entity.Path] = entity
	}
	candidates, byKey, _, invalid := buildCandidates(req, evidence, policy, impact, entityByPath)
	if invalid {
		t.Fatal("fixture evidence was rejected as malformed")
	}
	consumerCandidate := byKey["test:client"]
	if consumerCandidate == nil {
		t.Fatalf("contract consumer was not admitted as a candidate: %+v", candidates)
	}
	if got := consumerCandidate.coverage["file:api/petstore.yaml"]; math.Abs(got-0.7*0.65) > 1e-9 {
		t.Fatalf("contract-consumer coverage = %v, want %v", got, 0.7*0.65)
	}
	if !consumerCandidate.reasons[domain.ReasonPublicContractConsumer] {
		t.Fatalf("missing PUBLIC_CONTRACT_CONSUMER reason: %+v", consumerCandidate.reasons)
	}

	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("expected READY, got %s with reasons %+v", plan.Status, plan.EscalationReasons)
	}
	if !hasSelectedReason(plan, "test:client", domain.ReasonPublicContractConsumer) {
		t.Fatalf("selected contract consumer does not carry PUBLIC_CONTRACT_CONSUMER: %+v", plan.SelectedUnits)
	}
}

// LLD Stage 4: integration/E2E tests configured for affected services enter the
// candidate union with the INTEGRATION_BOUNDARY reason.
func TestIntegrationBoundaryUnitCarriesItsReasonCode(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	// The boundary unit's coverage quality is deliberately moderate, so the
	// confidence gate is relaxed to observe the reason code in isolation.
	policy.MinimumSelectionConfidence = 0.90
	evidence.Units = append(evidence.Units, domain.TestUnit{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID,
		StableKey: "test:integration.e2e", Adapter: "generic", UnitType: domain.UnitTestCase,
		RiskLabels: []string{"INTEGRATION"}, EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5,
		Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:integration.e2e",
		SourcePaths: []string{"src/main.go"}})
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("expected READY, got %s with reasons %+v", plan.Status, plan.EscalationReasons)
	}
	if !hasSelectedReason(plan, "test:integration.e2e", domain.ReasonIntegrationBoundary) {
		t.Fatalf("selected integration unit does not carry INTEGRATION_BOUNDARY: %+v", plan.SelectedUnits)
	}
}

// Omission reasons distinguish "no edge to an affected target in a complete
// graph" from "excluded by a reviewed policy entry"; neither is proof of
// irrelevance (ADR 0013/0014).
func TestOmissionCodesDistinguishUnaffectedAndReviewedUnits(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	policy.ReviewedExclusions = []domain.ReviewedExclusion{{
		UnitKey: "test:excluded", Reviewer: "release-owner", ReviewedAt: "2026-11-01",
		Rationale: "covered by an org-approved verification plan",
	}}
	evidence.Units = append(evidence.Units,
		domain.TestUnit{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, StableKey: "test:unrelated", Adapter: "generic",
			UnitType: domain.UnitTestCase, EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5,
			Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:unrelated", SourcePaths: []string{"src/other.go"}},
		domain.TestUnit{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, StableKey: "test:excluded", Adapter: "generic",
			UnitType: domain.UnitTestCase, EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5,
			Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:excluded", SourcePaths: []string{"src/another.go"}})
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("expected READY, got %s with reasons %+v", plan.Status, plan.EscalationReasons)
	}
	if code, ok := omittedReason(plan, "test:unrelated"); !ok || code != domain.OmitOutsideAffectedTargets {
		t.Fatalf("unrelated unit omission = %q (found=%v), want %q", code, ok, domain.OmitOutsideAffectedTargets)
	}
	if code, ok := omittedReason(plan, "test:excluded"); !ok || code != domain.OmitExcludedByReviewedPolicy {
		t.Fatalf("reviewed unit omission = %q (found=%v), want %q", code, ok, domain.OmitExcludedByReviewedPolicy)
	}
	if _, ok := omittedReason(plan, "test:unit"); ok {
		t.Fatalf("selected unit must not be omitted: %+v", plan.OmittedUnits)
	}
}

// LLD line 203: the co-location confidence cap keeps COLOCATED_WITH from ever
// satisfying a coverage threshold on its own.
func TestColocationEdgeCannotAloneJustifySelectivePlan(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	snapshotID := evidence.Snapshot.ID
	sourceID := evidence.Nodes[0].ID
	// Remove the authoritative coverage evidence so the only remaining link
	// from a test to the changed source is the convention edge.
	evidence.Coverage = nil
	evidence.Edges = nil
	testFile := domain.GraphNode{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeSourceFile,
		StableKey: "file:src/main_test.go", Path: "src/main_test.go", EvidenceSource: "fixture", Confidence: 1, ObservedAt: req.Now}
	evidence.Nodes = append(evidence.Nodes, testFile)
	evidence.Edges = append(evidence.Edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID,
		FromNodeID: testFile.ID, ToNodeID: sourceID, EdgeType: domain.EdgeColocatedWith,
		Confidence: 0.5, EvidenceSource: "conventions:co-location", ObservedAt: req.Now})
	evidence.Units = []domain.TestUnit{{ID: uuid.New(), SnapshotID: snapshotID, StableKey: "test:colocated", Adapter: "generic",
		UnitType: domain.UnitTestCase, EstimatedMillis: 1000, DurationP95Millis: 1000, DurationSampleCount: 5,
		Selectable: true, SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:colocated", SourcePaths: []string{"src/main_test.go"}}}

	impact := AnalyzeImpact(evidence.ChangedFiles, evidence.Nodes, evidence.Edges, policy, req.Now, true, false, false, false)
	entityByPath := map[string]ImpactEntity{}
	for _, entity := range impact.Entities {
		entityByPath[entity.Path] = entity
	}
	_, byKey, _, invalid := buildCandidates(req, evidence, policy, impact, entityByPath)
	if invalid {
		t.Fatal("fixture evidence was rejected as malformed")
	}
	colocated := byKey["test:colocated"]
	if colocated == nil {
		t.Fatal("co-located unit was not admitted as a candidate")
	}
	if got := colocated.coverage["file:src/main.go"]; math.Abs(got-0.5*0.25) > 1e-9 {
		t.Fatalf("co-location coverage = %v, want %v (confidence cap x planner factor)", got, 0.5*0.25)
	}
	if !colocated.reasons[domain.ReasonConfiguredPathMapping] {
		t.Fatalf("missing co-location reason: %+v", colocated.reasons)
	}

	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("co-location evidence alone must not justify a selective plan: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
	}
	if !hasReason(plan, domain.EscLowWeightedCoverage) || !hasReason(plan, domain.EscLowSelectionConfidence) {
		t.Fatalf("expected coverage and confidence escalations, got %+v", plan.EscalationReasons)
	}
}

// Ownership is explicit graph evidence: a unit whose recorded owner matches the
// owner node of an impacted file earns the OWNER_MAPPING reason (ADR 0013).
func TestOwnershipEdgeContributesOwnerMappingReason(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	snapshotID := evidence.Snapshot.ID
	owner := domain.GraphNode{ID: uuid.New(), SnapshotID: snapshotID, NodeType: domain.NodeOwner,
		StableKey: StableOwnerKey("@platform"), DisplayName: "@platform",
		EvidenceSource: "conventions:codeowners", Confidence: 0.9, ObservedAt: req.Now}
	evidence.Nodes = append(evidence.Nodes, owner)
	evidence.Edges = append(evidence.Edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID,
		FromNodeID: evidence.Nodes[0].ID, ToNodeID: owner.ID, EdgeType: domain.EdgeOwnedBy,
		Confidence: 0.9, EvidenceSource: "conventions:codeowners", ObservedAt: req.Now})
	evidence.Units[0].Metadata["owner"] = StableOwnerKey("@platform")

	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanReady {
		t.Fatalf("expected READY, got %s with reasons %+v", plan.Status, plan.EscalationReasons)
	}
	if !hasSelectedReason(plan, "test:unit", domain.ReasonOwnerMapping) {
		t.Fatalf("owner-matched unit does not carry OWNER_MAPPING: %+v", plan.SelectedUnits)
	}

	// A unit that records a different owner must not inherit the reason.
	evidence.Units[0].Metadata["owner"] = StableOwnerKey("@someone-else")
	other, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if hasSelectedReason(other, "test:unit", domain.ReasonOwnerMapping) {
		t.Fatalf("unrelated owner was mapped onto the unit: %+v", other.SelectedUnits)
	}
}

// A conflicted edge escalates the plan instead of being silently merged: the
// conflict is retained on the evidence and the planner refuses to let the
// contradictory relation justify skipping anything (ADR 0013 §13).
func TestConflictedEdgeEscalatesRatherThanSilentlyMerging(t *testing.T) {
	req, evidence, policy := readyFixture(t)
	if len(evidence.Edges) == 0 {
		t.Fatal("fixture carries no edges to conflict")
	}
	evidence.Edges[0].Conflict = true
	plan, err := CreatePlan(req, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscGraphConflict) {
		t.Fatalf("conflicted edge did not escalate to GRAPH_CONFLICT: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
	}
	if !evidence.Edges[0].Conflict {
		t.Fatal("the conflict was dropped from the evidence instead of being retained")
	}
}

// Hard risk labels escalate from both the changed-file classification and the
// impacted graph entity's labels.
func TestHardRiskLabelsEscalateFromChangedFilesAndGraphEntities(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(domain.PlanRequest, *EvidenceSnapshot, domain.Policy)
		wantCode domain.EscalationReasonCode
	}{
		{
			name: "changed file security label",
			mutate: func(_ domain.PlanRequest, evidence *EvidenceSnapshot, _ domain.Policy) {
				evidence.ChangedFiles[0].DetectedRiskLabels = []string{"SECURITY_CRITICAL"}
			},
			wantCode: domain.EscSecurityCriticalChange,
		},
		{
			name: "changed file public contract label",
			mutate: func(_ domain.PlanRequest, evidence *EvidenceSnapshot, _ domain.Policy) {
				evidence.ChangedFiles[0].DetectedRiskLabels = []string{"PUBLIC_CONTRACT"}
			},
			wantCode: domain.EscPublicContractChange,
		},
		{
			name: "graph entity label",
			mutate: func(_ domain.PlanRequest, evidence *EvidenceSnapshot, _ domain.Policy) {
				evidence.Nodes[0].Labels = []string{"SECURITY_CRITICAL"}
			},
			wantCode: domain.EscSecurityCriticalChange,
		},
		{
			// The planner classifies the change set itself (planner.go:42), so
			// a contract path with no pre-existing label still escalates.
			name: "path classified by the planner",
			mutate: func(_ domain.PlanRequest, evidence *EvidenceSnapshot, _ domain.Policy) {
				evidence.ChangedFiles[0].Path = "api/openapi.yaml"
			},
			wantCode: domain.EscPublicContractChange,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, evidence, policy := readyFixture(t)
			tc.mutate(req, &evidence, policy)
			plan, err := CreatePlan(req, evidence, policy)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Status != domain.PlanFullRequired || !hasReason(plan, tc.wantCode) {
				t.Fatalf("hard risk label did not escalate to %s: status=%s reasons=%+v", tc.wantCode, plan.Status, plan.EscalationReasons)
			}
		})
	}
}
