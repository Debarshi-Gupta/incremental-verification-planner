package analysis

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

// LLD 6.3 Stage 8: order selected units mandatory gates -> checks by
// fault-detection value per second -> direct tests -> high-risk dependency
// consumers -> integration/E2E tests -> remaining tests.
func TestStage8ExecutionOrderingTiers(t *testing.T) {
	entities := []ImpactEntity{
		{StableKey: "file:crit", Weight: 0.6, Critical: true},
		{StableKey: "file:ordinary", Weight: 0.4, Critical: false},
	}
	newCandidate := func(key string, unitType domain.TestUnitType, utility float64) *candidate {
		return &candidate{
			unit:     domain.TestUnit{StableKey: key, UnitType: unitType},
			reasons:  map[domain.SelectedReasonCode]bool{},
			coverage: map[string]float64{},
			utility:  utility,
			duration: 1000,
		}
	}
	mandatoryCheck := newCandidate("check:mandatory", domain.UnitCheck, 0.01)
	mandatoryCheck.mandatory = true
	plainCheck := newCandidate("check:lint", domain.UnitCheck, 0.10)
	directTest := newCandidate("test:direct", domain.UnitTestCase, 0.20)
	directTest.direct = true
	highRiskConsumer := newCandidate("test:high-risk-consumer", domain.UnitTestCase, 0.05)
	highRiskConsumer.reasons[domain.ReasonReverseDependencyConsumer] = true
	highRiskConsumer.coverage["file:crit"] = 0.9
	ordinaryConsumer := newCandidate("test:consumer", domain.UnitTestCase, 0.99)
	ordinaryConsumer.reasons[domain.ReasonReverseDependencyConsumer] = true
	ordinaryConsumer.coverage["file:ordinary"] = 0.9
	integration := newCandidate("test:integration.e2e", domain.UnitTestCase, 0.95)
	integration.reasons[domain.ReasonIntegrationBoundary] = true
	remaining := newCandidate("test:remaining", domain.UnitTestCase, 0.50)

	// Input order is deliberately shuffled; tiers, not input order, decide.
	selected := []*candidate{remaining, integration, ordinaryConsumer, directTest, plainCheck, mandatoryCheck, highRiskConsumer}
	units := makePlanUnits(selected, entities, false)
	got := make([]string, 0, len(units))
	for _, unit := range units {
		got = append(got, unit.StableKey)
	}
	want := []string{
		"check:mandatory",         // 1. mandatory gates
		"check:lint",              // 2. checks by value per second (utility within tier)
		"test:direct",             // 3. direct tests
		"test:high-risk-consumer", // 4. high-risk dependency consumers
		"test:integration.e2e",    // 5. integration/E2E tests
		"test:consumer",           // 6. remaining tests, utility desc within tier
		"test:remaining",
	}
	if len(got) != len(want) {
		t.Fatalf("ordered %d units, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execution order[%d] = %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}

	// A consumer without critical coverage is not a high-risk consumer: it
	// stays in the remaining-tests tier even with the highest utility.
	if tier := planExecutionTier(ordinaryConsumer, entities); tier != 5 {
		t.Fatalf("ordinary consumer tier = %d, want 5 (remaining tests)", tier)
	}
	if tier := planExecutionTier(highRiskConsumer, entities); tier != 3 {
		t.Fatalf("high-risk consumer tier = %d, want 3", tier)
	}
	if tier := planExecutionTier(integration, entities); tier != 4 {
		t.Fatalf("integration tier = %d, want 4", tier)
	}
}

// LLD 6.3 Stage 9: every plan status carries the change summary, snapshot
// identifiers, and adapter/toolchain versions; escalating preserves the
// omission audit trail (strategy 3.2 gap 1).
func TestStage9ExplanationCarriesChangeSummaryAndSnapshotIDs(t *testing.T) {
	t.Run("ready plan", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.Snapshot.ToolchainDigest = "toolchain-digest-abc"
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("fixture must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		for _, want := range []string{
			"change summary: 1 file(s)",
			"[src/main.go]",
			"global-impact files: none",
			"snapshot identifiers: snapshot=",
			"base=base",
			"head=head",
			"worktree=worktree",
			"adapter capability versions: generic=v1",
			"toolchain digest: toolchain-digest-abc",
		} {
			if !strings.Contains(plan.Explanation, want) {
				t.Errorf("READY explanation missing %q:\n%s", want, plan.Explanation)
			}
		}
	})

	t.Run("hard-rule escalation keeps stage9 context and omission trail", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles = append(evidence.ChangedFiles, domain.ChangedFile{Path: "pom.xml", Status: domain.ChangeAdded})
		evidence.Units = append(evidence.Units, domain.TestUnit{
			ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, StableKey: "test:unrelated", Adapter: "generic",
			UnitType: domain.UnitTestCase, EstimatedMillis: 500, Selectable: true,
			SelectionGranularity: "DECLARED_UNIT", TargetRef: "test:unrelated",
		})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired {
			t.Fatalf("pom.xml must force FULL_REQUIRED, got %s", plan.Status)
		}
		for _, want := range []string{
			"change summary: 2 file(s)",
			"global-impact files: BUILD_DEFINITION=pom.xml",
			"snapshot identifiers: snapshot=",
			"adapter capability versions: generic=v1",
		} {
			if !strings.Contains(plan.Explanation, want) {
				t.Errorf("escalated explanation missing %q:\n%s", want, plan.Explanation)
			}
		}
		if len(plan.OmittedUnits) != 1 {
			t.Fatalf("escalated plan omitted %d units, want 1 audit-trail unit: %+v", len(plan.OmittedUnits), plan.OmittedUnits)
		}
		omitted := plan.OmittedUnits[0]
		if omitted.StableKey != "test:unrelated" || len(omitted.ReasonCodes) != 1 || omitted.ReasonCodes[0] != domain.OmitOutsideAffectedTargets {
			t.Fatalf("unexpected omission record: %+v", omitted)
		}
		if !strings.Contains(omitted.Explanation, "escalated to full verification") {
			t.Fatalf("omission explanation must be labelled as escalation audit trail: %q", omitted.Explanation)
		}
	})

	t.Run("full mode keeps stage9 context and omission trail", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		req.Mode = domain.ModeFull
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired {
			t.Fatalf("FULL mode must escalate, got %s", plan.Status)
		}
		if !strings.Contains(plan.Explanation, "change summary: 1 file(s)") ||
			!strings.Contains(plan.Explanation, "snapshot identifiers: snapshot=") {
			t.Fatalf("FULL-mode explanation lost Stage-9 context: %s", plan.Explanation)
		}
		if len(plan.OmittedUnits) != 1 || plan.OmittedUnits[0].StableKey != "test:unit" {
			t.Fatalf("FULL-mode plan must record the omission audit trail, got %+v", plan.OmittedUnits)
		}
	})
}

// LLD 6.3 Stage 2: classify global-impact files across the full category
// vocabulary, with the generated-relationship fallback and the documentation
// exemption.
func TestGlobalImpactFileClassification(t *testing.T) {
	policy := domain.Policy{}
	cases := []struct {
		path string
		want GlobalImpactCategory
	}{
		{"pom.xml", CategoryBuildDefinition},
		{"services/payments/build.gradle.kts", CategoryBuildDefinition},
		{"package-lock.json", CategoryLockfile},
		{".tool-versions", CategoryToolchainVersion},
		{"pytest.ini", CategoryTestRunnerConfig},
		{"packages/web/jest.config.js", CategoryTestRunnerConfig},
		{".github/workflows/ci.yml", CategoryCIWorkflow},
		{"config/database.yaml", CategorySharedConfig},
		{"api/openapi.yaml", CategorySchemaContract},
		{"schemas/event.proto", CategorySchemaContract},
		{"buf.gen.yaml", CategoryCodeGeneration},
		{"deploy/k8s/api.yaml", CategoryDeploymentManifest},
		{"infra/main.tf", CategoryDeploymentManifest},
		{"db/migrations/001_init.sql", CategoryDatabaseMigration},
		{"cmd/api/wire_gen.go", CategoryDIRegistry},
		{"src/main.go", ""},
		{"docs/deployment.md", ""},
	}
	for _, tc := range cases {
		if got := ClassifyGlobalImpact(tc.path, policy); got != tc.want {
			t.Errorf("ClassifyGlobalImpact(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	// Unknown generated-file relationship: a generated path with no
	// recognized global-impact shape is classified, never ignored.
	if got := ClassifyGlobalImpactFile(domain.ChangedFile{Path: "internal/api/zz_generated.pb.go", IsGenerated: true}, policy); got != CategoryGeneratedRelation {
		t.Fatalf("generated file classification = %q, want %q", got, CategoryGeneratedRelation)
	}
	// A recognized category wins over the generated fallback.
	if got := ClassifyGlobalImpactFile(domain.ChangedFile{Path: "config/zz_generated.yaml", IsGenerated: true}, policy); got != CategorySharedConfig {
		t.Fatalf("generated shared config classification = %q, want %q", got, CategorySharedConfig)
	}
	// Renames surface the global-impact side of the change.
	if got := ClassifyGlobalImpactFile(domain.ChangedFile{Path: "src/new_name.go", OldPath: "pom.xml", Status: domain.ChangeRenamed}, policy); got != CategoryBuildDefinition {
		t.Fatalf("rename classification = %q, want %q", got, CategoryBuildDefinition)
	}
}

// The hard-rule switch picks its escalation code from the Stage-2
// classification while the HardFullSuitePaths gate stays authoritative.
func TestHardRuleEscalationCodesFollowGlobalImpactClassification(t *testing.T) {
	cases := []struct {
		name       string
		change     domain.ChangedFile
		hardPaths  []string
		wantCode   domain.EscalationReasonCode
		wantDetail string
	}{
		{
			name:       "lockfile",
			change:     domain.ChangedFile{Path: "package-lock.json", Status: domain.ChangeModified},
			hardPaths:  []string{"**/*lock*"},
			wantCode:   domain.EscLockfileChange,
			wantDetail: "lockfile or dependency-resolution input changed: package-lock.json",
		},
		{
			name:       "migration",
			change:     domain.ChangedFile{Path: "db/migrations/002_add_index.sql", Status: domain.ChangeAdded},
			hardPaths:  []string{"**/migrations/**"},
			wantCode:   domain.EscMigrationChange,
			wantDetail: "database migration changed: db/migrations/002_add_index.sql",
		},
		{
			name:       "schema contract",
			change:     domain.ChangedFile{Path: "api/openapi.yaml", Status: domain.ChangeModified},
			hardPaths:  []string{"**/openapi.yaml"},
			wantCode:   domain.EscPublicContractChange,
			wantDetail: "public contract or schema changed: api/openapi.yaml",
		},
		{
			name:       "shared config names its category",
			change:     domain.ChangedFile{Path: "config/app.yaml", Status: domain.ChangeModified},
			hardPaths:  []string{"config/**"},
			wantCode:   domain.EscConfigOrToolchainChange,
			wantDetail: "build, test, toolchain, or global configuration changed (SHARED_CONFIG): config/app.yaml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, evidence, policy := readyFixture(t)
			policy.HardFullSuitePaths = tc.hardPaths
			evidence.ChangedFiles = []domain.ChangedFile{tc.change}
			plan, err := CreatePlan(req, evidence, policy)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Status != domain.PlanFullRequired || !hasReason(plan, tc.wantCode) {
				t.Fatalf("status=%s reasons=%+v, want %s", plan.Status, plan.EscalationReasons, tc.wantCode)
			}
			found := false
			for _, reason := range plan.EscalationReasons {
				if reason.Code == tc.wantCode && strings.Contains(reason.Detail, tc.wantDetail) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no %s detail containing %q in %+v", tc.wantCode, tc.wantDetail, plan.EscalationReasons)
			}
		})
	}
}

// LLD 6.2: the plan carries a P50-P95 uncertainty interval aggregated from
// per-unit samples, and LLD 6.3 rule 11 consumes it in the economic gate.
func TestRuntimeUncertaintyIntervalIsBoundedAndConsumed(t *testing.T) {
	t.Run("interval aggregates per-unit p50 and p95", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.Units[0].DurationP50Millis = 400
		evidence.Units[0].DurationP95Millis = 1000
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("fixture must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if plan.EstimatedDurationP50Millis != 400 || plan.EstimatedDurationP95Millis != 1000 {
			t.Fatalf("interval = [%d, %d], want [400, 1000] (point estimate %d)",
				plan.EstimatedDurationP50Millis, plan.EstimatedDurationP95Millis, plan.EstimatedDurationMillis)
		}
		if plan.EstimatedDurationP50Millis > plan.EstimatedDurationP95Millis {
			t.Fatalf("P50 %d must not exceed P95 %d", plan.EstimatedDurationP50Millis, plan.EstimatedDurationP95Millis)
		}
		if !strings.Contains(plan.Explanation, "estimated runtime P50 400 ms to P95 1000 ms") {
			t.Fatalf("explanation missing runtime interval: %s", plan.Explanation)
		}
	})

	t.Run("without samples both bounds collapse to the point estimate", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.Units[0].DurationSampleCount = 0
		evidence.Units[0].DurationP50Millis = 0
		evidence.Units[0].DurationP95Millis = 0
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("fixture must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if plan.EstimatedDurationP50Millis != plan.EstimatedDurationMillis || plan.EstimatedDurationP95Millis != plan.EstimatedDurationMillis {
			t.Fatalf("bounds [%d, %d] must equal the point estimate %d without samples",
				plan.EstimatedDurationP50Millis, plan.EstimatedDurationP95Millis, plan.EstimatedDurationMillis)
		}
	})

	t.Run("interval overlapping the full-suite runtime makes selection uneconomic", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		// Fraction 1.5 exceeds 1.0 so the point-estimate clause cannot fire:
		// P95=120s < 150s ceiling, but [90s, 120s] straddles the 100s
		// full-suite estimate, which LLD 6.3 rule 11 treats as uneconomic.
		policy.MaxSelectiveRuntimeFraction = 1.5
		evidence.Units[0].DurationP50Millis = 90_000
		evidence.Units[0].DurationP95Millis = 120_000
		evidence.Units[0].EstimatedMillis = 120_000
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscPlanEconomicallyUnhelpful) {
			t.Fatalf("overlapping interval must escalate: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
		detail := ""
		for _, reason := range plan.EscalationReasons {
			if reason.Code == domain.EscPlanEconomicallyUnhelpful {
				detail = reason.Detail
			}
		}
		if !strings.Contains(detail, "P50=90000ms P95=120000ms") || !strings.Contains(detail, "uncertainty interval") {
			t.Fatalf("economic escalation must report the interval: %q", detail)
		}
		if plan.EstimatedDurationP50Millis != 90_000 || plan.EstimatedDurationP95Millis != 120_000 {
			t.Fatalf("escalated plan must retain the rejected interval, got [%d, %d]",
				plan.EstimatedDurationP50Millis, plan.EstimatedDurationP95Millis)
		}
	})

	t.Run("interval entirely below the ceiling stays economic", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		policy.MaxSelectiveRuntimeFraction = 1.5
		// P50 above the full-suite point means no overlap, and P95 stays
		// within the 150s ceiling.
		evidence.Units[0].DurationP50Millis = 110_000
		evidence.Units[0].DurationP95Millis = 120_000
		evidence.Units[0].EstimatedMillis = 120_000
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("non-overlapping interval within the ceiling must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if hasReason(plan, domain.EscPlanEconomicallyUnhelpful) {
			t.Fatalf("unexpected economic escalation: %+v", plan.EscalationReasons)
		}
	})
}

// LLD 6.3 rule 8: changed-file count, affected-target count, and broad
// refactor all escalate through TOO_MANY_CHANGED_FILES (Appendix A pins the
// escalation vocabulary; ADR 0018 documents the shared code).
func TestHardRuleBlastRadiusTriggers(t *testing.T) {
	t.Run("changed-file count exceeds the selective-plan limit", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		policy.MaxChangedFilesForSelectivePlan = 1
		evidence.ChangedFiles = []domain.ChangedFile{
			{Path: "src/a.go", Status: domain.ChangeModified},
			{Path: "src/b.go", Status: domain.ChangeModified},
		}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscTooManyChangedFiles) {
			t.Fatalf("changed-file cap must escalate: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
		if !detailContains(plan, domain.EscTooManyChangedFiles, "exceed the selective-plan limit") {
			t.Fatalf("missing changed-file detail: %+v", plan.EscalationReasons)
		}
	})

	t.Run("broad refactor escalates", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles = nil
		for i := 0; i < 25; i++ {
			evidence.ChangedFiles = append(evidence.ChangedFiles, domain.ChangedFile{
				Path:   fmt.Sprintf("src/pkg%d/file%d.go", i%10, i),
				Status: domain.ChangeModified,
			})
		}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscTooManyChangedFiles) {
			t.Fatalf("broad refactor must escalate: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
		if !detailContains(plan, domain.EscTooManyChangedFiles, "broad refactor") {
			t.Fatalf("missing broad-refactor detail: %+v", plan.EscalationReasons)
		}
	})

	t.Run("affected target count exceeds the limit", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		policy.MaxChangedFilesForSelectivePlan = 2
		sourceID := evidence.Nodes[0].ID // src/main.go
		for i := 0; i < 3; i++ {
			targetID := uuid.New()
			evidence.Nodes = append(evidence.Nodes, domain.GraphNode{
				ID: targetID, SnapshotID: evidence.Snapshot.ID, NodeType: domain.NodeBuildTarget,
				StableKey: fmt.Sprintf("target:app%d", i), EvidenceSource: "fixture", Confidence: 1, ObservedAt: req.Now,
			})
			evidence.Edges = append(evidence.Edges, domain.GraphEdge{
				ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, FromNodeID: targetID, ToNodeID: sourceID,
				EdgeType: domain.EdgeDependsOn, Confidence: 1, EvidenceSource: "fixture", ObservedAt: req.Now,
			})
		}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscTooManyChangedFiles) {
			t.Fatalf("affected-target cap must escalate: status=%s reasons=%+v", plan.Status, plan.EscalationReasons)
		}
		// 3 fixture build targets + the fixture's own test:unit NodeTestTarget
		// are all impacted through src/main.go.
		if !detailContains(plan, domain.EscTooManyChangedFiles, "affected target count 4 exceeds limit 2") {
			t.Fatalf("missing affected-target detail: %+v", plan.EscalationReasons)
		}
	})

	t.Run("within both caps the rule is checked and not triggered", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		policy.MaxChangedFilesForSelectivePlan = 500
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range plan.HardRuleChecks {
			if check.Code != domain.EscTooManyChangedFiles {
				continue
			}
			if check.Triggered {
				t.Fatalf("rule 8 must not trigger within caps: %+v", check)
			}
			if !strings.Contains(check.Detail, "affected target count") || !strings.Contains(check.Detail, "changed-file count") {
				t.Fatalf("rule-8 check must record both trigger outcomes: %+v", check)
			}
			return
		}
		t.Fatalf("rule 8 was not checked at all: %+v", plan.HardRuleChecks)
	})
}

// LLD 6.3 Stage 2: with no language/symbol index in the system, changed lines
// map at file and build-target granularity (the LLD's explicit fallback);
// COVERS_CHANGED_SYMBOL is only ever emitted when symbol-level evidence is
// actually supplied (ADR 0016).
func TestChangedLinesMapAtFileGranularityWithoutSymbolIndex(t *testing.T) {
	t.Run("line precision follows diff hunks", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles[0].DiffHunks = []domain.LineRange{{Start: 10, End: 20}}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("fixture must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		codes := plan.SelectedUnits[0].ReasonCodes
		if !containsReason(codes, domain.ReasonDirectlyChangedTest) {
			t.Fatalf("direct test must be selected: %+v", codes)
		}
		if containsReason(codes, domain.ReasonCoversChangedLine) {
			t.Fatalf("line-precision coverage outside the diff hunks must not map: %+v", codes)
		}
		if containsReason(codes, domain.ReasonCoversChangedSymbol) {
			t.Fatalf("no symbol index exists; COVERS_CHANGED_SYMBOL must never be fabricated: %+v", codes)
		}

		evidence.ChangedFiles[0].DiffHunks = []domain.LineRange{{Start: 1, End: 5}}
		plan, err = CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !containsReason(plan.SelectedUnits[0].ReasonCodes, domain.ReasonCoversChangedLine) {
			t.Fatalf("overlapping line-precision coverage must map: %+v", plan.SelectedUnits[0].ReasonCodes)
		}
	})

	t.Run("symbol precision maps when an index supplies evidence", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		testNodeID := evidence.Nodes[1].ID
		symbolID := uuid.New()
		evidence.Nodes = append(evidence.Nodes, domain.GraphNode{
			ID: symbolID, SnapshotID: evidence.Snapshot.ID, NodeType: domain.NodeSymbol,
			StableKey: "symbol:DoWork", Path: "src/main.go", EvidenceSource: "fixture", Confidence: 1, ObservedAt: req.Now,
		})
		evidence.Edges = append(evidence.Edges, domain.GraphEdge{
			ID: uuid.New(), SnapshotID: evidence.Snapshot.ID, FromNodeID: testNodeID, ToNodeID: symbolID,
			EdgeType: domain.EdgeTestCovers, Confidence: 1, EvidenceSource: "fixture", ObservedAt: req.Now,
		})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("fixture must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if !containsReason(plan.SelectedUnits[0].ReasonCodes, domain.ReasonCoversChangedSymbol) {
			t.Fatalf("symbol-precision evidence must map to COVERS_CHANGED_SYMBOL: %+v", plan.SelectedUnits[0].ReasonCodes)
		}
	})
}

// LLD 6.3 Stage 3 rule 3 ("unless an organization-specific reviewed policy
// explicitly defines a narrower, safe exception") and ADR 0019: a reviewed
// hard-path exception suppresses only the matched path escalation, records an
// audit check carrying the review trail, keeps the Stage-9 classification
// visible, and never covers other rules or label escalations.
func TestHardPathExceptionSuppressesOnlyTheMatchedEscalation(t *testing.T) {
	reviewed := domain.HardPathException{
		Path: "package-lock.json", Rule: string(domain.EscLockfileChange),
		Reviewer: "build-owner", ReviewedAt: "2026-11-01",
		Rationale: "reviewed dependency refresh",
	}
	// The lockfile change joins the fixture's src/main.go change, and the
	// fixture unit covers the lockfile entity too so that the exception is the
	// only safety condition in play (coverage stays at policy threshold).
	withLockfile := func(t *testing.T) (domain.PlanRequest, EvidenceSnapshot, domain.Policy) {
		t.Helper()
		req, evidence, policy := readyFixture(t)
		policy.HardFullSuitePaths = []string{"**/*lock*"}
		evidence.ChangedFiles = append(evidence.ChangedFiles, domain.ChangedFile{Path: "package-lock.json", Status: domain.ChangeModified})
		evidence.Coverage = append(evidence.Coverage, domain.CoverageObservation{
			TestUnitKey: "test:unit", CoveredStableKey: "file:package-lock.json", CoveredPath: "package-lock.json",
			SourceType: "fixture", ReportHash: "report-digest", RunID: uuid.New(),
			ObservedAt: evidence.Snapshot.EvidenceGeneratedAt, EnvironmentFingerprint: "env",
			BuildConfigDigest: "build-config", TestConfigDigest: "test-config",
			Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1, TestSpecific: true, EvidenceConfidence: 1,
		})
		return req, evidence, policy
	}

	t.Run("without an exception the lockfile escalates", func(t *testing.T) {
		req, evidence, policy := withLockfile(t)
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscLockfileChange) {
			t.Fatalf("lockfile change must escalate, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		check, ok := hardRuleCheck(plan, domain.EscLockfileChange)
		if !ok || !check.Triggered {
			t.Fatalf("hard-rule check must record the triggered lockfile rule: %+v", plan.HardRuleChecks)
		}
	})

	t.Run("a reviewed exception keeps the plan ready", func(t *testing.T) {
		req, evidence, policy := withLockfile(t)
		policy.HardPathExceptions = []domain.HardPathException{reviewed}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("reviewed exception must keep the plan READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		if hasReason(plan, domain.EscLockfileChange) {
			t.Fatalf("excepted lockfile must not escalate: %+v", plan.EscalationReasons)
		}
		check, ok := hardRuleCheck(plan, domain.EscLockfileChange)
		if !ok {
			t.Fatalf("excepted hard rule must still be recorded as checked: %+v", plan.HardRuleChecks)
		}
		if check.Triggered {
			t.Fatalf("excepted hard rule must be recorded as not triggered: %+v", check)
		}
		want := "hard-rule exception applied by build-owner on 2026-11-01: reviewed dependency refresh"
		if !strings.Contains(check.Detail, want) {
			t.Fatalf("hard-rule check must carry the review trail, got %q", check.Detail)
		}
		// ADR 0019 §4: the Stage-2 classification itself stays visible in the
		// change summary; only the escalation decision changes.
		if !strings.Contains(plan.Explanation, "LOCKFILE=package-lock.json") {
			t.Fatalf("stage-9 change summary must still report the lockfile classification:\n%s", plan.Explanation)
		}
	})

	t.Run("an exception for a different rule does not suppress the escalation", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		policy.HardFullSuitePaths = []string{"**/migrations/**"}
		mismatched := reviewed
		mismatched.Path = "db/migrations/**"
		policy.HardPathExceptions = []domain.HardPathException{mismatched}
		evidence.ChangedFiles = append(evidence.ChangedFiles, domain.ChangedFile{Path: "db/migrations/002_add_index.sql", Status: domain.ChangeAdded})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscMigrationChange) {
			t.Fatalf("rule mismatch must escalate, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
	})

	t.Run("a second uncovered file still escalates", func(t *testing.T) {
		req, evidence, policy := withLockfile(t)
		policy.HardPathExceptions = []domain.HardPathException{reviewed}
		evidence.ChangedFiles = append(evidence.ChangedFiles, domain.ChangedFile{Path: "yarn.lock", Status: domain.ChangeModified})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !detailContains(plan, domain.EscLockfileChange, "yarn.lock") {
			t.Fatalf("uncovered lockfile must still escalate, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
	})

	t.Run("label escalations are never exceptable", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		// Configuration validation rejects SECURITY_CRITICAL_CHANGE as an
		// exception rule; the planner must not depend on that alone (ADR
		// 0019 §3: labels have no exception path).
		policy.HardPathExceptions = []domain.HardPathException{{
			Path: "src/security/**", Rule: string(domain.EscSecurityCriticalChange),
			Reviewer: "build-owner", ReviewedAt: "2026-11-01", Rationale: "attempted label exception",
		}}
		evidence.ChangedFiles = append(evidence.ChangedFiles, domain.ChangedFile{Path: "src/security/audit.go", Status: domain.ChangeModified})
		evidence.Coverage = append(evidence.Coverage, domain.CoverageObservation{
			TestUnitKey: "test:unit", CoveredStableKey: "file:src/security/audit.go", CoveredPath: "src/security/audit.go",
			SourceType: "fixture", ReportHash: "report-digest", RunID: uuid.New(),
			ObservedAt: evidence.Snapshot.EvidenceGeneratedAt, EnvironmentFingerprint: "env",
			BuildConfigDigest: "build-config", TestConfigDigest: "test-config",
			Precision: domain.CoverageLine, LineStart: 1, LineEnd: 1, TestSpecific: true, EvidenceConfidence: 1,
		})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != domain.PlanFullRequired || !hasReason(plan, domain.EscSecurityCriticalChange) {
			t.Fatalf("label escalation must stay unconditional, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
	})
}

// LLD 1.4 (optional SCIP enrichment): the core impact inputs MAY consume a
// SCIP-compatible symbol/reference index. This implementation deliberately
// does not (ADR 0016), so the absence is asserted here rather than assumed:
// impact stays at file granularity and no symbol-level entity is invented.
func TestSCIPEnrichmentIsAbsentByDecisionAndKeepsImpactAtFileGranularity(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	snapshotID := uuid.New()
	sourceID := uuid.New()
	nodes := []domain.GraphNode{{ID: sourceID, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile,
		StableKey: "file:src/main.go", Path: "src/main.go", EvidenceSource: "fixture", Confidence: 1, ObservedAt: now}}
	impact := AnalyzeImpact([]domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified}},
		nodes, nil, domain.Policy{}, now, true, false, false, false)
	if len(impact.Entities) != 1 {
		t.Fatalf("expected one impacted entity, got %+v", impact.Entities)
	}
	entity := impact.Entities[0]
	if entity.StableKey != "file:src/main.go" || entity.Path != "src/main.go" {
		t.Fatalf("impact must stay at file granularity without a symbol index: %+v", entity)
	}
	if entity.NodeType == domain.NodeSymbol {
		t.Fatalf("impact analysis must never invent symbol-level entities: %+v", entity)
	}
}

// LLD 4.3: binary files cannot use line-level coverage and therefore lower
// selection confidence unless build metadata identifies their consumers.
func TestBinaryChangeLowersSelectionConfidenceWithoutIdentifiedConsumers(t *testing.T) {
	t.Run("binary change without identified consumers lowers confidence", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles = []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified, IsBinary: true}}
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !hasReason(plan, domain.EscLowSelectionConfidence) {
			t.Fatalf("binary change without identified consumers must lower confidence, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		want := "binary change without build-metadata consumers lowers confidence: src/main.go"
		if !detailContains(plan, domain.EscLowSelectionConfidence, want) {
			t.Fatalf("escalation must name the binary change: %+v", plan.EscalationReasons)
		}
		if plan.Status != domain.PlanFullRequired {
			t.Fatalf("binary change must not stay READY, got %s", plan.Status)
		}
	})

	t.Run("build metadata that identifies consumers keeps confidence and never claims line coverage", func(t *testing.T) {
		req, evidence, policy := readyFixture(t)
		evidence.ChangedFiles = []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified, IsBinary: true}}
		var sourceID, testID uuid.UUID
		for _, node := range evidence.Nodes {
			switch node.StableKey {
			case "file:src/main.go":
				sourceID = node.ID
			case "test:unit":
				testID = node.ID
			}
		}
		if sourceID == uuid.Nil || testID == uuid.Nil {
			t.Fatalf("fixture nodes are missing: %+v", evidence.Nodes)
		}
		evidence.Edges = append(evidence.Edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: evidence.Snapshot.ID,
			FromNodeID: testID, ToNodeID: sourceID, EdgeType: domain.EdgeDependsOn, Confidence: 1,
			EvidenceSource: "fixture", ObservedAt: evidence.Snapshot.EvidenceGeneratedAt})
		plan, err := CreatePlan(req, evidence, policy)
		if err != nil {
			t.Fatal(err)
		}
		if hasReason(plan, domain.EscLowSelectionConfidence) {
			t.Fatalf("identified consumers must not lower confidence: %+v", plan.EscalationReasons)
		}
		if plan.Status != domain.PlanReady {
			t.Fatalf("plan must stay READY, got %s with %+v", plan.Status, plan.EscalationReasons)
		}
		for _, unit := range plan.SelectedUnits {
			if containsReason(unit.ReasonCodes, domain.ReasonCoversChangedLine) {
				t.Fatalf("binary change must never produce a line-level coverage reason: %+v", unit.ReasonCodes)
			}
		}
	})
}

func detailContains(plan domain.VerificationPlan, code domain.EscalationReasonCode, needle string) bool {
	for _, reason := range plan.EscalationReasons {
		if reason.Code == code && strings.Contains(reason.Detail, needle) {
			return true
		}
	}
	return false
}

// hardRuleCheck returns the finalized hard-rule check for a code, if the
// planner recorded one.
func hardRuleCheck(plan domain.VerificationPlan, code domain.EscalationReasonCode) (domain.HardRuleCheck, bool) {
	for _, check := range plan.HardRuleChecks {
		if check.Code == code {
			return check, true
		}
	}
	return domain.HardRuleCheck{}, false
}

func containsReason(codes []domain.SelectedReasonCode, want domain.SelectedReasonCode) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}
