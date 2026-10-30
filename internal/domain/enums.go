package domain

type ChangeStatus string

const (
	ChangeAdded       ChangeStatus = "ADDED"
	ChangeModified    ChangeStatus = "MODIFIED"
	ChangeDeleted     ChangeStatus = "DELETED"
	ChangeRenamed     ChangeStatus = "RENAMED"
	ChangeCopied      ChangeStatus = "COPIED"
	ChangeTypeChanged ChangeStatus = "TYPE_CHANGED"
)

type GraphNodeType string

const (
	NodeSourceFile      GraphNodeType = "SOURCE_FILE"
	NodeSymbol          GraphNodeType = "SYMBOL"
	NodeBuildTarget     GraphNodeType = "BUILD_TARGET"
	NodeTestTarget      GraphNodeType = "TEST_TARGET"
	NodeTestCase        GraphNodeType = "TEST_CASE"
	NodeCheckTarget     GraphNodeType = "CHECK_TARGET"
	NodeConfig          GraphNodeType = "CONFIG"
	NodeAPISchema       GraphNodeType = "API_SCHEMA"
	NodeGeneratedOutput GraphNodeType = "GENERATED_OUTPUT"
	NodeMigration       GraphNodeType = "MIGRATION"
	NodeRuntimeService  GraphNodeType = "RUNTIME_SERVICE"
	NodeOwner           GraphNodeType = "OWNER"
)

type GraphEdgeType string

const (
	EdgeDependsOn        GraphEdgeType = "DEPENDS_ON"
	EdgeTestCovers       GraphEdgeType = "TEST_COVERS"
	EdgeBuiltFrom        GraphEdgeType = "BUILT_FROM"
	EdgeConfigures       GraphEdgeType = "CONFIGURES"
	EdgeContractConsumer GraphEdgeType = "CONTRACT_CONSUMER"
	EdgeOwnedBy          GraphEdgeType = "OWNED_BY"
	EdgeColocatedWith    GraphEdgeType = "COLOCATED_WITH"
)

type TestUnitType string

const (
	UnitTestCase     TestUnitType = "TEST_CASE"
	UnitTestMethod   TestUnitType = "TEST_METHOD"
	UnitTestFile     TestUnitType = "TEST_FILE"
	UnitTestClass    TestUnitType = "TEST_CLASS"
	UnitBuildTarget  TestUnitType = "BUILD_TARGET"
	UnitProjectSuite TestUnitType = "PROJECT_SUITE"
	UnitCheck        TestUnitType = "CHECK"
)

type CoveragePrecision string

const (
	CoverageLine   CoveragePrecision = "LINE"
	CoverageSymbol CoveragePrecision = "SYMBOL"
	CoverageFile   CoveragePrecision = "FILE"
	CoverageTarget CoveragePrecision = "TARGET"
	CoverageModule CoveragePrecision = "MODULE"
)

type PlanMode string

const (
	ModeFull         PlanMode = "FULL"
	ModeObserve      PlanMode = "OBSERVE"
	ModeFastFeedback PlanMode = "FAST_FEEDBACK"
	ModeRiskBasedCI  PlanMode = "RISK_BASED_CI"
)

type PlanStatus string

const (
	PlanReady        PlanStatus = "READY"
	PlanFullRequired PlanStatus = "FULL_REQUIRED"
	PlanStale        PlanStatus = "STALE"
	PlanExecuting    PlanStatus = "EXECUTING"
	PlanPassed       PlanStatus = "PASSED"
	PlanFailed       PlanStatus = "FAILED"
	PlanCancelled    PlanStatus = "CANCELLED"
	PlanError        PlanStatus = "ERROR"
)

type RiskLevel string

const (
	RiskLow     RiskLevel = "LOW"
	RiskMedium  RiskLevel = "MEDIUM"
	RiskHigh    RiskLevel = "HIGH"
	RiskUnknown RiskLevel = "UNKNOWN"
)

type SelectedReasonCode string

const (
	ReasonDirectlyChangedTest        SelectedReasonCode = "DIRECTLY_CHANGED_TEST"
	ReasonCoversChangedLine          SelectedReasonCode = "COVERS_CHANGED_LINE"
	ReasonCoversChangedSymbol        SelectedReasonCode = "COVERS_CHANGED_SYMBOL"
	ReasonAffectedBuildTarget        SelectedReasonCode = "AFFECTED_BUILD_TARGET"
	ReasonReverseDependencyConsumer  SelectedReasonCode = "REVERSE_DEPENDENCY_CONSUMER"
	ReasonPublicContractConsumer     SelectedReasonCode = "PUBLIC_CONTRACT_CONSUMER"
	ReasonIntegrationBoundary        SelectedReasonCode = "INTEGRATION_BOUNDARY"
	ReasonHistoricalFailureRelevance SelectedReasonCode = "HISTORICAL_FAILURE_RELEVANCE"
	ReasonConfiguredPathMapping      SelectedReasonCode = "CONFIGURED_PATH_MAPPING"
	ReasonOwnerMapping               SelectedReasonCode = "OWNER_MAPPING"
	ReasonMandatoryPolicyCheck       SelectedReasonCode = "MANDATORY_POLICY_CHECK"
	ReasonHighRiskCoverage           SelectedReasonCode = "HIGH_RISK_COVERAGE"
	ReasonFallbackFullSuite          SelectedReasonCode = "FALLBACK_FULL_SUITE"
)

type OmissionReasonCode string

const (
	OmitNoImpactEdgeObserved         OmissionReasonCode = "NO_IMPACT_EDGE_OBSERVED"
	OmitOutsideAffectedTargets       OmissionReasonCode = "OUTSIDE_AFFECTED_TARGETS"
	OmitCoverageAlreadySatisfied     OmissionReasonCode = "COVERAGE_ALREADY_SATISFIED"
	OmitLowerMarginalCoveragePerCost OmissionReasonCode = "LOWER_MARGINAL_COVERAGE_PER_COST"
	OmitNotSelectableGranularity     OmissionReasonCode = "NOT_SELECTABLE_AT_REQUESTED_GRANULARITY"
	OmitNotInActiveAdapterScope      OmissionReasonCode = "NOT_IN_ACTIVE_ADAPTER_SCOPE"
	OmitExcludedByReviewedPolicy     OmissionReasonCode = "EXCLUDED_BY_REVIEWED_POLICY"
)

type EscalationReasonCode string

const (
	EscUnsupportedAdapter        EscalationReasonCode = "UNSUPPORTED_ADAPTER"
	EscGraphIncomplete           EscalationReasonCode = "GRAPH_INCOMPLETE"
	EscGraphStale                EscalationReasonCode = "GRAPH_STALE"
	EscGraphConflict             EscalationReasonCode = "GRAPH_CONFLICT"
	EscCoverageStale             EscalationReasonCode = "COVERAGE_STALE"
	EscCoverageMissing           EscalationReasonCode = "COVERAGE_MISSING"
	EscConfigOrToolchainChange   EscalationReasonCode = "CONFIG_OR_TOOLCHAIN_CHANGE"
	EscLockfileChange            EscalationReasonCode = "LOCKFILE_CHANGE"
	EscPublicContractChange      EscalationReasonCode = "PUBLIC_CONTRACT_CHANGE"
	EscMigrationChange           EscalationReasonCode = "MIGRATION_CHANGE"
	EscSecurityCriticalChange    EscalationReasonCode = "SECURITY_CRITICAL_CHANGE"
	EscHighDependencyFanout      EscalationReasonCode = "HIGH_DEPENDENCY_FANOUT"
	EscTooManyChangedFiles       EscalationReasonCode = "TOO_MANY_CHANGED_FILES"
	EscNoTestsSelected           EscalationReasonCode = "NO_TESTS_SELECTED"
	EscLowSelectionConfidence    EscalationReasonCode = "LOW_SELECTION_CONFIDENCE"
	EscLowWeightedCoverage       EscalationReasonCode = "LOW_WEIGHTED_COVERAGE"
	EscMandatoryCheckUnresolved  EscalationReasonCode = "MANDATORY_CHECK_UNRESOLVED"
	EscPlanEconomicallyUnhelpful EscalationReasonCode = "PLAN_ECONOMICALLY_UNHELPFUL"
	EscPolicyUnavailable         EscalationReasonCode = "POLICY_UNAVAILABLE"
	EscPlanStale                 EscalationReasonCode = "PLAN_STALE"
	EscQuarantineActive          EscalationReasonCode = "QUARANTINE_ACTIVE"
	EscAdapterDiscoveryFailed    EscalationReasonCode = "ADAPTER_DISCOVERY_FAILED"
	EscReportParseFailed         EscalationReasonCode = "REPORT_PARSE_FAILED"
	EscEnvironmentMismatch       EscalationReasonCode = "ENVIRONMENT_MISMATCH"
	EscWorktreeMutated           EscalationReasonCode = "WORKTREE_MUTATED"
)

const (
	OutcomeTargetedPass         ExecutionOutcome = "TARGETED_PASS"
	OutcomeFullVerificationPass ExecutionOutcome = "FULL_VERIFICATION_PASS"
	OutcomeTargetedFail         ExecutionOutcome = "TARGETED_FAIL"
	OutcomeFullVerificationFail ExecutionOutcome = "FULL_VERIFICATION_FAIL"
	OutcomeInfraFailure         ExecutionOutcome = "INFRA_FAILURE"
	OutcomeStale                ExecutionOutcome = "STALE"
	OutcomeAuditMiss            ExecutionOutcome = "AUDIT_MISS"
	OutcomeAuditPossibleMiss    ExecutionOutcome = "AUDIT_POSSIBLE_MISS"
	OutcomeAuditIncomparable    ExecutionOutcome = "AUDIT_INCOMPARABLE"
)

const (
	AuditNoMissObserved AuditOutcome = "NO_MISS_OBSERVED"
	AuditPossibleMiss   AuditOutcome = "POSSIBLE_MISS"
	AuditConfirmedMiss  AuditOutcome = "CONFIRMED_MISS"
	AuditIncomparable   AuditOutcome = "INCOMPARABLE"
	AuditFailed         AuditOutcome = "AUDIT_FAILED"
)
