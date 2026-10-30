package analysis

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

const AlgorithmVersion = "ivp-planner-v1.0.0"

type candidate struct {
	unit           domain.TestUnit
	reasons        map[domain.SelectedReasonCode]bool
	coverage       map[string]float64
	historyGain    float64
	dependencyGain float64
	utility        float64
	duration       int64
	durationKnown  bool
	mandatory      bool
	direct         bool
	selected       bool
}

// CreatePlan returns a deterministic recommendation for a normalized request.
// Any untrusted or insufficient evidence produces a FULL_REQUIRED plan with an
// abstract local fallback target; it never fabricates a selective pass.
func CreatePlan(req domain.PlanRequest, evidence EvidenceSnapshot, policy domain.Policy) (domain.VerificationPlan, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	req.Now = now
	evidence.ChangedFiles = canonicalChanges(evidence.ChangedFiles)
	ttl := req.PlanTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	plan := domain.VerificationPlan{
		ID: uuid.New(), RepositoryID: req.RepositoryID, SnapshotID: evidence.Snapshot.ID,
		BaseSHA: req.BaseSHA, HeadSHA: req.HeadSHA, WorktreeHash: evidence.Snapshot.WorktreeHash,
		Mode: req.Mode, Status: domain.PlanFullRequired, RiskLevel: domain.RiskUnknown,
		AlgorithmVersion: AlgorithmVersion, PolicyVersion: policy.Version,
		AdapterCapabilities: copyStringMap(evidence.AdapterCapabilityVersions),
		CreatedAt:           now, ExpiresAt: now.Add(ttl),
		FullSuiteFallbackRef: policy.FullSuiteTargetRef,
		SelectedUnits:        []domain.PlanUnit{}, MandatoryUnits: []domain.PlanUnit{}, OmittedUnits: []domain.OmittedUnit{},
		EscalationReasons: []domain.EscalationReason{}, HardRuleChecks: []domain.HardRuleCheck{}, EvidenceReferences: []domain.EvidenceReference{},
	}
	checks := make(map[domain.EscalationReasonCode]*domain.HardRuleCheck)
	addCheck := func(code domain.EscalationReasonCode, triggered bool, detail string) {
		if c, ok := checks[code]; ok {
			c.Triggered = c.Triggered || triggered
			if detail != "" && !strings.Contains(c.Detail, detail) {
				c.Detail += "; " + detail
			}
			return
		}
		checks[code] = &domain.HardRuleCheck{Code: code, Triggered: triggered, Detail: detail}
	}
	addEscalation := func(code domain.EscalationReasonCode, detail string) {
		addCheck(code, true, detail)
		for i := range plan.EscalationReasons {
			if plan.EscalationReasons[i].Code == code {
				if detail != "" && !strings.Contains(plan.EscalationReasons[i].Detail, detail) {
					plan.EscalationReasons[i].Detail += "; " + detail
				}
				return
			}
		}
		plan.EscalationReasons = append(plan.EscalationReasons, domain.EscalationReason{Code: code, Detail: detail, Checked: true})
	}

	// Stage 1: request, snapshot, adapter, and effective-policy trust checks.
	badFingerprint := req.RepositoryID == uuid.Nil || evidence.Snapshot.RepositoryID != req.RepositoryID ||
		req.SnapshotID == uuid.Nil || evidence.Snapshot.ID != req.SnapshotID || req.BaseSHA == "" || req.HeadSHA == "" ||
		req.BaseSHA != evidence.Snapshot.BaseSHA || req.HeadSHA != evidence.Snapshot.CommitSHA ||
		req.CurrentWorktreeHash == "" || req.CurrentWorktreeHash != evidence.Snapshot.WorktreeHash
	addCheck(domain.EscPlanStale, badFingerprint, "repository, snapshot, base/head, or current worktree fingerprint does not match")
	if badFingerprint {
		addEscalation(domain.EscPlanStale, "repository, snapshot, base/head, or current worktree fingerprint does not match")
	}
	policyTrusted := policy.PolicyAvailable && policy.Version > 0 && req.PolicyVersion > 0 &&
		evidence.PolicyTrusted && req.PolicySnapshotTrusted && policy.Version == req.PolicyVersion
	if !policyTrusted || (req.PolicyVersion != 0 && policy.Version != req.PolicyVersion) {
		addCheck(domain.EscPolicyUnavailable, true, "effective policy is unavailable, untrusted, or has a different version")
		addEscalation(domain.EscPolicyUnavailable, "effective policy is unavailable, untrusted, or has a different version")
	} else {
		addCheck(domain.EscPolicyUnavailable, false, "effective policy is trusted and version-matched")
	}
	if policy.FullSuiteTargetRef == "" {
		addEscalation(domain.EscPolicyUnavailable, "local full-suite argv is not configured; safe fallback cannot execute")
	}
	if req.Mode != domain.ModeFull && req.Mode != domain.ModeObserve && req.Mode != domain.ModeFastFeedback && req.Mode != domain.ModeRiskBasedCI {
		addEscalation(domain.EscPolicyUnavailable, "requested planning mode is missing or unsupported")
	}
	if req.Mode == domain.ModeRiskBasedCI && (!policy.RiskBasedCIEnabled || !policy.RiskBasedCIApproved || !riskBasedCalibrated(policy)) {
		addEscalation(domain.EscPolicyUnavailable, "RISK_BASED_CI lacks organization approval or required calibration/audit evidence")
	}
	if !req.AdapterSupported {
		addEscalation(domain.EscUnsupportedAdapter, "repository adapter is unsupported")
	} else {
		addCheck(domain.EscUnsupportedAdapter, false, "adapter is supported")
	}
	discoveryOK := req.AdapterDiscoveryOK && evidence.DiscoveryReliable
	if !discoveryOK {
		addEscalation(domain.EscAdapterDiscoveryFailed, "adapter could not reliably enumerate runnable verification units")
	} else {
		addCheck(domain.EscAdapterDiscoveryFailed, false, "adapter discovery is reliable")
	}
	changesValid := len(evidence.ChangedFiles) > 0
	seenChangedPaths := make(map[string]bool, len(evidence.ChangedFiles))
	for _, change := range evidence.ChangedFiles {
		if err := domain.ValidateChangedFile(change); err != nil {
			changesValid = false
			addEscalation(domain.EscGraphIncomplete, "invalid changed-file metadata: "+err.Error())
			break
		}
		if seenChangedPaths[change.Path] {
			changesValid = false
			addEscalation(domain.EscGraphConflict, "change set contains duplicate path metadata: "+change.Path)
			break
		}
		seenChangedPaths[change.Path] = true
	}
	if !changesValid {
		addEscalation(domain.EscGraphIncomplete, "change set is empty or contains invalid repository paths/status")
	}
	if err := domain.ValidateSnapshot(evidence.Snapshot); err != nil {
		addEscalation(domain.EscGraphIncomplete, "snapshot metadata is invalid: "+err.Error())
	}
	graphValid := validateGraphEvidence(evidence)
	if !graphValid {
		addEscalation(domain.EscGraphIncomplete, "graph contains invalid, duplicate, cross-snapshot, or dangling evidence")
	}
	if req.PolicyVersion != 0 && policy.Version != req.PolicyVersion {
		addEscalation(domain.EscPlanStale, "request policy version differs from the active policy")
	}
	if evidence.Snapshot.EvidenceGeneratedAt.IsZero() || now.Sub(evidence.Snapshot.EvidenceGeneratedAt) > policy.GraphStaleness || evidence.Snapshot.EvidenceGeneratedAt.After(now.Add(time.Minute)) || hasStaleGraphEvidence(evidence, now, policy.GraphStaleness) {
		addEscalation(domain.EscGraphStale, "graph evidence is older than the configured freshness threshold or has an invalid timestamp")
	} else {
		addCheck(domain.EscGraphStale, false, "graph evidence is within its freshness threshold")
	}
	graphComplete := evidence.GraphComplete && evidence.Snapshot.IsComplete && !evidence.GraphMissingEdges
	if !graphComplete {
		addEscalation(domain.EscGraphIncomplete, "snapshot or impacted dependency graph is incomplete")
	} else {
		addCheck(domain.EscGraphIncomplete, false, "snapshot and impacted graph are complete")
	}
	if evidence.GraphHasConflicts {
		addEscalation(domain.EscGraphConflict, "evidence contains contradictory impact edges")
	} else {
		addCheck(domain.EscGraphConflict, false, "no contradictory impact edges were reported")
	}
	if evidence.GraphHasCycles {
		addEscalation(domain.EscGraphConflict, "dependency graph reports a cycle")
	}
	if len(evidence.ChangedFiles) > policy.MaxChangedFilesForSelectivePlan && policy.MaxChangedFilesForSelectivePlan > 0 {
		addEscalation(domain.EscTooManyChangedFiles, fmt.Sprintf("%d changed files exceed the selective-plan limit %d", len(evidence.ChangedFiles), policy.MaxChangedFilesForSelectivePlan))
	} else {
		addCheck(domain.EscTooManyChangedFiles, false, "changed-file count is within policy")
	}
	if evidence.BroadRefactor {
		addEscalation(domain.EscTooManyChangedFiles, "change was classified as a broad refactor")
	}
	if policy.Quarantined {
		addEscalation(domain.EscQuarantineActive, "selection scope is quarantined: "+policy.QuarantineScope)
	} else {
		addCheck(domain.EscQuarantineActive, false, "selection scope is not quarantined")
	}
	if len(evidence.ChangedFiles) == 0 {
		addEscalation(domain.EscGraphIncomplete, "empty change set cannot justify selective verification")
	}
	for _, f := range evidence.ChangedFiles {
		if matchAny(f.Path, policy.HardFullSuitePaths) || (f.OldPath != "" && matchAny(f.OldPath, policy.HardFullSuitePaths)) {
			switch {
			case isLockfilePath(f.Path) || (f.OldPath != "" && isLockfilePath(f.OldPath)):
				addEscalation(domain.EscLockfileChange, "lockfile or dependency-resolution input changed: "+f.Path)
			case isMigrationPath(f.Path) || (f.OldPath != "" && isMigrationPath(f.OldPath)):
				addEscalation(domain.EscMigrationChange, "database migration changed: "+f.Path)
			case isContractPath(f.Path) || (f.OldPath != "" && isContractPath(f.OldPath)):
				addEscalation(domain.EscPublicContractChange, "public contract or schema changed: "+f.Path)
			default:
				addEscalation(domain.EscConfigOrToolchainChange, "build, test, toolchain, or global configuration changed: "+f.Path)
			}
		}
		for _, label := range f.DetectedRiskLabels {
			if containsFold(policy.HardFullSuiteRiskLabels, label) {
				if strings.EqualFold(label, "PUBLIC_CONTRACT") {
					addEscalation(domain.EscPublicContractChange, "changed entity has hard-risk label "+label)
				} else {
					addEscalation(domain.EscSecurityCriticalChange, "changed entity has hard-risk label "+label)
				}
			}
		}
	}
	impact := AnalyzeImpact(evidence.ChangedFiles, evidence.Nodes, evidence.Edges, policy, now, graphComplete, evidence.GraphMissingEdges, evidence.GraphHasConflicts, evidence.GraphHasCycles)
	plan.EvidenceReferences = evidenceRefs(evidence, impact)
	for _, entity := range impact.Entities {
		for _, label := range entity.Labels {
			if containsFold(policy.HardFullSuiteRiskLabels, label) {
				if strings.EqualFold(label, "PUBLIC_CONTRACT") {
					addEscalation(domain.EscPublicContractChange, "impacted graph entity has hard-risk label "+label)
				} else {
					addEscalation(domain.EscSecurityCriticalChange, "impacted graph entity has hard-risk label "+label)
				}
			}
		}
	}
	if impact.HasConflict {
		addEscalation(domain.EscGraphConflict, "impacted graph edges contain a conflict")
	}
	if impact.HasCycle {
		addEscalation(domain.EscGraphConflict, "impacted dependency graph contains a cycle")
	}
	if policy.DependencyFanoutEscalation > 0 && impact.ReverseFanout > policy.DependencyFanoutEscalation {
		addEscalation(domain.EscHighDependencyFanout, fmt.Sprintf("reverse dependency fanout %d exceeds limit %d", impact.ReverseFanout, policy.DependencyFanoutEscalation))
	} else {
		addCheck(domain.EscHighDependencyFanout, false, "reverse dependency fanout is within policy")
	}
	if len(evidence.ChangedFiles) > 0 && allDocumentation(evidence.ChangedFiles, policy.DocumentationPathPatterns) {
		evidence.DocumentationOnly = true
	}

	// Build the normalized changed-entity indexes and candidate union.
	entityByPath := make(map[string]ImpactEntity)
	for _, e := range impact.Entities {
		entityByPath[e.Path] = e
	}
	candidates, candidateByKey, staleCoverage, invalidCoverage := buildCandidates(req, evidence, policy, impact, entityByPath)
	if invalidCoverage {
		addEscalation(domain.EscReportParseFailed, "coverage or test-unit evidence was malformed, ambiguous, or referenced an unknown unit")
	}

	// Mandatory checks are resolved before optimization. The optimizer has no
	// operation that removes an entry from this set.
	mandatoryIDs := make(map[string]bool)
	for _, key := range policy.AlwaysRunCheckIDs {
		mandatoryIDs[key] = true
	}
	resolvedMandatory := make(map[string]bool)
	for key, c := range candidateByKey {
		if c.mandatory {
			resolvedMandatory[key] = true
		}
	}
	missingMandatory := make([]string, 0)
	for id := range mandatoryIDs {
		if !resolvedMandatory[id] {
			missingMandatory = append(missingMandatory, id)
		}
	}
	sort.Strings(missingMandatory)
	if len(missingMandatory) > 0 {
		addEscalation(domain.EscMandatoryCheckUnresolved, "configured mandatory checks are missing from adapter discovery: "+strings.Join(missingMandatory, ", "))
	} else {
		addCheck(domain.EscMandatoryCheckUnresolved, false, "all configured mandatory check IDs were resolved")
	}
	for key, c := range candidateByKey {
		if c.mandatory && (!c.unit.Selectable || c.unit.Adapter == "") {
			addEscalation(domain.EscMandatoryCheckUnresolved, "mandatory check is not safely runnable at the discovered granularity: "+key)
		}
	}

	coverageFresh := false
	unitByKey := make(map[string]domain.TestUnit, len(evidence.Units))
	for _, unit := range evidence.Units {
		if unit.StableKey != "" {
			if _, duplicate := unitByKey[unit.StableKey]; duplicate {
				continue
			}
			unitByKey[unit.StableKey] = unit
		}
	}
	for _, c := range evidence.Coverage {
		unit, exists := unitByKey[c.TestUnitKey]
		if exists && coverageFreshness(c, req, evidence.Snapshot, now, policy) == 1 && testConfigurationMatches(c, unit) {
			coverageFresh = true
			break
		}
	}
	if staleCoverage {
		addCheck(domain.EscCoverageStale, false, "stale or environment-incomparable coverage was ignored")
	} else {
		addCheck(domain.EscCoverageStale, false, "no stale coverage observations were found")
	}
	if evidence.CoverageExpected && !coverageFresh && len(candidates) == 0 {
		addCheck(domain.EscCoverageMissing, true, "coverage is required but no fresh comparable coverage exists")
	} else {
		addCheck(domain.EscCoverageMissing, false, "coverage is either available or other explicit evidence can be evaluated")
	}

	risk := CalculateRisk(evidence.ChangedFiles, impact, evidence, policy, mappedWeight(impact.Entities, candidates), req)
	plan.RiskScore, plan.RiskLevel, plan.RiskContributions = risk.Score, risk.Level, risk.Contributions
	if risk.Level == domain.RiskUnknown {
		addEscalation(domain.EscLowSelectionConfidence, "risk is UNKNOWN because "+risk.UnknownReason)
	}
	if req.Mode == domain.ModeRiskBasedCI && (risk.Level == domain.RiskHigh || risk.Level == domain.RiskUnknown) {
		addEscalation(domain.EscLowSelectionConfidence, "high or unknown risk is never eligible for selective final-CI gating")
	}
	if req.Mode == domain.ModeFastFeedback && risk.Level == domain.RiskHigh && !policy.HighRiskSelectiveFeedbackAllowed {
		addEscalation(domain.EscSecurityCriticalChange, "high-risk inner-loop selection is disabled by policy")
	}

	if req.Mode == domain.ModeFull {
		finalizeChecks(&plan, checks)
		plan.Explanation = "FULL mode was explicitly requested. The local executor must run the configured full-suite fallback. " + explanationSuffix(plan)
		return fullRequired(plan, candidateByKey, policy, "FULL mode requested"), nil
	}

	threshold := coverageThreshold(risk.Level, policy)
	selection, weightedCoverage, selectedMillis, coverageImpossible := greedySetCover(candidates, impact.Entities, threshold, policy)
	if len(selection) == 0 && !evidence.DocumentationOnly {
		addEscalation(domain.EscNoTestsSelected, "no runnable test or check unit is selected for a non-documentation change")
	} else {
		addCheck(domain.EscNoTestsSelected, false, "at least one runnable unit is selected or the change is documentation-only")
	}
	selectionConfidence := calculateConfidence(req, evidence, impact, selection, weightedCoverage, mappedWeight(impact.Entities, candidates))
	plan.SelectionConfidence = selectionConfidence
	plan.WeightedCoverage = weightedCoverage
	plan.EstimatedDurationMillis = selectedMillis
	plan.EstimatedComputeMillis = selectedMillis

	if coverageImpossible || weightedCoverage+1e-9 < threshold {
		addEscalation(domain.EscLowWeightedCoverage, fmt.Sprintf("estimated weighted impact coverage %.4f is below required %.4f", weightedCoverage, threshold))
	} else {
		addCheck(domain.EscLowWeightedCoverage, false, fmt.Sprintf("estimated weighted coverage %.4f meets required %.4f", weightedCoverage, threshold))
	}
	if selectionConfidence+1e-9 < policy.MinimumSelectionConfidence {
		addEscalation(domain.EscLowSelectionConfidence, fmt.Sprintf("selection confidence %.4f is below required %.4f", selectionConfidence, policy.MinimumSelectionConfidence))
	} else {
		addCheck(domain.EscLowSelectionConfidence, false, fmt.Sprintf("selection confidence %.4f meets required %.4f", selectionConfidence, policy.MinimumSelectionConfidence))
	}
	if evidence.CoverageExpected && !coverageFresh && (coverageImpossible || weightedCoverage+1e-9 < threshold) {
		addEscalation(domain.EscCoverageMissing, "required coverage evidence is missing and remaining mappings cannot meet policy thresholds")
	}
	if staleCoverage && (coverageImpossible || weightedCoverage+1e-9 < threshold || selectionConfidence < policy.MinimumSelectionConfidence) {
		addEscalation(domain.EscCoverageStale, "stale or incomparable coverage leaves insufficient trusted mappings")
	}
	if hasTriggered(checks) {
		finalizeChecks(&plan, checks)
		plan.Explanation = "Selective verification was not justified by the current evidence; the local executor must run the configured full-suite fallback. " + explanationSuffix(plan)
		return fullRequired(plan, candidateByKey, policy, "one or more hard safety conditions triggered"), nil
	}
	if !allDurationsKnown(selection) || req.FullSuiteEstimateMillis <= 0 || selectedMillis >= int64(float64(req.FullSuiteEstimateMillis)*policy.MaxSelectiveRuntimeFraction) {
		addEscalation(domain.EscPlanEconomicallyUnhelpful, "selected verification has no proven runtime advantage within the configured safety fraction")
		finalizeChecks(&plan, checks)
		plan.Explanation = "Selective verification is not economically justified by conservative runtime estimates; the local executor must run the configured full-suite fallback. " + explanationSuffix(plan)
		return fullRequired(plan, candidateByKey, policy, "runtime advantage is not established"), nil
	}
	if evidence.Snapshot.WorktreeHash != req.CurrentWorktreeHash {
		addEscalation(domain.EscWorktreeMutated, "worktree fingerprint changed after planning")
		finalizeChecks(&plan, checks)
		return fullRequired(plan, candidateByKey, policy, "worktree mutated during planning"), nil
	}

	plan.Status = domain.PlanReady
	plan.SelectedUnits = makePlanUnits(selection, impact.Entities, false)
	plan.MandatoryUnits = mandatoryPlanUnits(candidateByKey)
	plan.OmittedUnits = omissions(evidence.Units, selection, candidateByKey)
	finalizeChecks(&plan, checks)
	modeNote := "A TARGETED_PASS applies only to the selected checks and says nothing about unselected tests."
	if req.Mode == domain.ModeObserve {
		modeNote = "OBSERVE records this selective recommendation while the executor runs the authoritative full suite."
	}
	if req.Mode == domain.ModeFastFeedback {
		modeNote += " Final CI still requires the configured full suite."
	}
	plan.Explanation = fmt.Sprintf("Selected %d of %d discovered units for %s at risk %s (%d/100); estimated weighted impact coverage %.4f and selection confidence %.4f. %s %s",
		len(plan.SelectedUnits), len(evidence.Units), req.Mode, risk.Level, risk.Score, weightedCoverage, selectionConfidence, modeNote, explanationSuffix(plan))
	return plan, nil
}

func buildCandidates(req domain.PlanRequest, evidence EvidenceSnapshot, policy domain.Policy, impact ImpactResult, entityByPath map[string]ImpactEntity) ([]*candidate, map[string]*candidate, bool, bool) {
	byKey := make(map[string]*candidate, len(evidence.Units))
	unitByNodeKey := make(map[string][]*candidate)
	unitByStableKey := make(map[string]*candidate)
	staleCoverage, invalidCoverage := false, false
	for _, unit := range evidence.Units {
		if err := domain.ValidateTestUnit(unit, evidence.Snapshot.ID); err != nil {
			invalidCoverage = true
			continue
		}
		if unit.StableKey == "" {
			continue
		}
		if _, exists := byKey[unit.StableKey]; exists {
			invalidCoverage = true // duplicate unit identity makes mappings ambiguous
			continue
		}
		knownDuration := (unit.DurationSampleCount >= 5 && unit.DurationP95Millis > 0) || unit.EstimatedMillis > 0
		c := &candidate{unit: unit, reasons: map[domain.SelectedReasonCode]bool{}, coverage: map[string]float64{}, mandatory: unit.Mandatory || contains(policy.AlwaysRunCheckIDs, unit.StableKey), duration: estimatedDuration(unit, policy), durationKnown: knownDuration}
		byKey[unit.StableKey] = c
		unitByStableKey[unit.StableKey] = c
		if unit.TargetRef != "" {
			unitByNodeKey[unit.TargetRef] = append(unitByNodeKey[unit.TargetRef], c)
		}
		if unit.ModuleKey != "" {
			unitByNodeKey[unit.ModuleKey] = append(unitByNodeKey[unit.ModuleKey], c)
		}
		if c.mandatory {
			c.reasons[domain.ReasonMandatoryPolicyCheck] = true
			c.selected = unit.Selectable
		}
		for _, p := range unit.SourcePaths {
			for entityPath, entity := range entityByPath {
				if p == entityPath {
					addCandidateCoverage(c, entity.StableKey, 0.8, domain.ReasonConfiguredPathMapping)
				}
			}
		}
	}
	if len(byKey) > 0 {
		for key, c := range byKey {
			if c.unit.Mandatory && !c.unit.Selectable {
				c.selected = true // it must be reported as mandatory; full fallback handles execution
			}
			_ = key
		}
	}
	unitForNode := func(key string) []*candidate {
		var found []*candidate
		if c := unitByStableKey[key]; c != nil {
			found = append(found, c)
		}
		found = append(found, unitByNodeKey[key]...)
		return deduplicateCandidates(found)
	}
	entityForNode := func(node domain.GraphNode) []ImpactEntity {
		if node.Path != "" {
			if e, ok := entityByPath[node.Path]; ok {
				return []ImpactEntity{e}
			}
		}
		for _, e := range impact.Entities {
			if e.StableKey == node.StableKey {
				return []ImpactEntity{e}
			}
		}
		return nil
	}
	unitNodes := make(map[domain.UUID]domain.GraphNode, len(evidence.Nodes))
	for _, n := range evidence.Nodes {
		unitNodes[n.ID] = n
	}
	for _, edge := range evidence.Edges {
		from, fromOK := unitNodes[edge.FromNodeID]
		to, toOK := unitNodes[edge.ToNodeID]
		if !fromOK || !toOK {
			continue
		}
		if edge.EdgeType == domain.EdgeTestCovers {
			if edge.ValidUntil != nil && req.Now.After(*edge.ValidUntil) {
				staleCoverage = true
				continue
			}
			for _, c := range unitForNode(from.StableKey) {
				for _, entity := range entityForNode(to) {
					quality := edge.Confidence * nodeConfidence(from) * nodeConfidence(to) * precisionFactor(precisionForNode(to))
					addCandidateCoverage(c, entity.StableKey, quality, reasonForPrecision(precisionForNode(to)))
				}
			}
		}
		if edge.EdgeType == domain.EdgeColocatedWith {
			if !impact.ImpactedNodeIDs[from.ID] && !impact.ImpactedNodeIDs[to.ID] {
				continue
			}
			for _, entity := range append(entityForNode(from), entityForNode(to)...) {
				for _, c := range unitForNode(from.StableKey) {
					addCandidateCoverage(c, entity.StableKey, edge.Confidence*0.25, domain.ReasonConfiguredPathMapping)
				}
				for _, c := range unitForNode(to.StableKey) {
					addCandidateCoverage(c, entity.StableKey, edge.Confidence*0.25, domain.ReasonConfiguredPathMapping)
				}
			}
		}
		if (edge.EdgeType == domain.EdgeDependsOn || edge.EdgeType == domain.EdgeContractConsumer) && impact.ImpactedNodeIDs[from.ID] {
			for _, c := range unitForNode(from.StableKey) {
				if edge.EdgeType == domain.EdgeContractConsumer {
					c.reasons[domain.ReasonPublicContractConsumer] = true
				} else {
					c.reasons[domain.ReasonReverseDependencyConsumer] = true
				}
				for _, entity := range entityForNode(to) {
					addCandidateCoverage(c, entity.StableKey, edge.Confidence*0.65*nodeConfidence(from)*nodeConfidence(to), domain.ReasonAffectedBuildTarget)
				}
				c.dependencyGain = math.Max(c.dependencyGain, math.Min(0.05, edge.Confidence*0.05))
			}
		}
	}

	for _, obs := range evidence.Coverage {
		if err := domain.ValidateCoverageObservation(obs); err != nil {
			invalidCoverage = true
			continue
		}
		freshness := coverageFreshness(obs, req, evidence.Snapshot, req.Now, policy)
		if freshness <= 0 {
			staleCoverage = true
			continue
		}
		c := byKey[obs.TestUnitKey]
		if c == nil {
			invalidCoverage = true
			continue
		}
		if !testConfigurationMatches(obs, c.unit) {
			staleCoverage = true
			continue
		}
		for _, entity := range entitiesForCoverage(obs, evidence.Nodes, entityByPath, impact.Entities) {
			if obs.Precision == domain.CoverageLine && !changedLineOverlap(obs, evidence.ChangedFiles, entity.Path) {
				continue
			}
			quality := freshness * obs.EvidenceConfidence * precisionFactor(obs.Precision)
			if obs.SuiteLevel && !obs.TestSpecific {
				quality = math.Min(quality, 0.65)
			}
			addCandidateCoverage(c, entity.StableKey, quality, reasonForPrecision(obs.Precision))
		}
	}

	for _, f := range evidence.ChangedFiles {
		for _, c := range byKey {
			direct := false
			for _, p := range c.unit.SourcePaths {
				if p == f.Path || p == f.OldPath {
					direct = true
				}
			}
			if direct || strings.Contains(strings.ToLower(c.unit.StableKey), strings.ToLower(path.Base(f.Path))) {
				c.reasons[domain.ReasonDirectlyChangedTest] = true
				c.direct = true
				c.selected = c.unit.Selectable || c.mandatory
			}
		}
	}

	// Target/module graph reachability contributes candidates even when no
	// individual-test coverage is available; precision remains deliberately low.
	for _, node := range evidence.Nodes {
		if !impact.ImpactedNodeIDs[node.ID] || (node.NodeType != domain.NodeBuildTarget && node.NodeType != domain.NodeTestTarget && node.NodeType != domain.NodeTestCase) {
			continue
		}
		for _, c := range unitForNode(node.StableKey) {
			c.reasons[domain.ReasonAffectedBuildTarget] = true
			for _, e := range impact.Entities {
				if e.Path != "" {
					addCandidateCoverage(c, e.StableKey, node.Confidence*0.55, domain.ReasonAffectedBuildTarget)
				}
			}
			c.dependencyGain = math.Max(c.dependencyGain, 0.025)
		}
	}

	for _, mapping := range policy.PathMappings {
		for _, change := range evidence.ChangedFiles {
			if !GlobMatch(mapping.Pattern, change.Path) && (change.OldPath == "" || !GlobMatch(mapping.Pattern, change.OldPath)) {
				continue
			}
			for _, key := range mapping.UnitKeys {
				if c := byKey[key]; c != nil {
					c.reasons[domain.ReasonConfiguredPathMapping] = true
					for _, e := range impact.Entities {
						if e.Path == change.Path || e.Path == change.OldPath {
							addCandidateCoverage(c, e.StableKey, 0.75, domain.ReasonConfiguredPathMapping)
						}
					}
				}
			}
		}
	}

	// Ownership mappings are explicit graph evidence; inferred co-location is
	// too weak to be represented as an owner mapping.
	owners := make(map[domain.UUID][]domain.UUID)
	for _, e := range evidence.Edges {
		if e.EdgeType == domain.EdgeOwnedBy && impact.ImpactedNodeIDs[e.FromNodeID] {
			owners[e.FromNodeID] = append(owners[e.FromNodeID], e.ToNodeID)
		}
	}
	for _, node := range evidence.Nodes {
		ownerIDs := owners[node.ID]
		if len(ownerIDs) == 0 {
			continue
		}
		sort.Slice(ownerIDs, func(i, j int) bool { return ownerIDs[i].String() < ownerIDs[j].String() })
		ownerStableKeys := make(map[string]bool, len(ownerIDs))
		for _, ownerID := range ownerIDs {
			if owner, ok := unitNodes[ownerID]; ok {
				ownerStableKeys[owner.StableKey] = true
			}
		}
		for _, c := range byKey {
			if c.unit.Metadata != nil && ownerStableKeys[fmt.Sprint(c.unit.Metadata["owner"])] {
				c.reasons[domain.ReasonOwnerMapping] = true
				for _, entity := range entityForNode(node) {
					addCandidateCoverage(c, entity.StableKey, 0.7, domain.ReasonOwnerMapping)
				}
			}
		}
	}

	for _, h := range evidence.History {
		if !h.Comparable || h.FailureCount <= 0 || h.Outcome == "CANCELLED" || h.Outcome == "INFRA_FAILURE" {
			continue
		}
		c := byKey[h.TestUnitKey]
		if c == nil || !isChangedKey(h.RelatedStableKey, evidence.ChangedFiles) {
			continue
		}
		c.reasons[domain.ReasonHistoricalFailureRelevance] = true
		c.historyGain = math.Max(c.historyGain, math.Min(0.05, 0.01*float64(h.FailureCount)))
	}

	all := make([]*candidate, 0, len(byKey))
	for _, c := range byKey {
		if c.mandatory {
			c.selected = c.unit.Selectable
		}
		if !c.unit.Selectable {
			continue
		}
		if c.unit.Adapter == "" {
			continue
		}
		c.utility = candidateUtility(c, policy)
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].unit.StableKey < all[j].unit.StableKey })
	return all, byKey, staleCoverage, invalidCoverage
}

func greedySetCover(candidates []*candidate, entities []ImpactEntity, target float64, policy domain.Policy) ([]*candidate, float64, int64, bool) {
	selected := make(map[string]*candidate)
	for _, c := range candidates {
		if c.mandatory || c.direct {
			selected[c.unit.StableKey] = c
		}
	}
	selectedCoverage := func() (float64, map[string]float64) {
		byEntity := make(map[string]float64, len(entities))
		for _, c := range selected {
			for entity, quality := range c.coverage {
				if quality > byEntity[entity] {
					byEntity[entity] = quality
				}
			}
		}
		weighted := 0.0
		for _, entity := range entities {
			weighted += entity.Weight * byEntity[entity.StableKey]
		}
		return clampFloat(weighted, 0, 1), byEntity
	}
	coverage, byEntity := selectedCoverage()
	for coverage+1e-9 < target {
		var best *candidate
		bestUtility := -1.0
		for _, c := range candidates {
			if _, ok := selected[c.unit.StableKey]; ok {
				continue
			}
			gain := 0.0
			for _, entity := range entities {
				if quality := c.coverage[entity.StableKey]; quality > byEntity[entity.StableKey] {
					gain += entity.Weight * (quality - byEntity[entity.StableKey])
				}
			}
			if gain <= 0 {
				continue
			}
			marginal := gain + c.historyGain + c.dependencyGain
			cost := c.duration
			if cost < policy.MinimumCostFloorMillis {
				cost = policy.MinimumCostFloorMillis
			}
			utility := marginal * candidateReliability(c) / float64(cost)
			if utility > bestUtility+1e-15 || (math.Abs(utility-bestUtility) <= 1e-15 && (best == nil || c.unit.StableKey < best.unit.StableKey)) {
				best, bestUtility = c, utility
			}
		}
		if best == nil {
			return selectionSlice(selected), coverage, totalDuration(selected), true
		}
		selected[best.unit.StableKey] = best
		coverage, byEntity = selectedCoverage()
	}
	// Every changed critical entity must independently meet the selected target.
	for _, entity := range entities {
		if entity.Critical && byEntity[entity.StableKey]+1e-9 < target {
			return selectionSlice(selected), coverage, totalDuration(selected), true
		}
	}
	return selectionSlice(selected), coverage, totalDuration(selected), false
}

func candidateUtility(c *candidate, policy domain.Policy) float64 {
	gain := c.historyGain + c.dependencyGain
	for _, quality := range c.coverage {
		gain = math.Max(gain, quality)
	}
	cost := c.duration
	if cost < policy.MinimumCostFloorMillis {
		cost = policy.MinimumCostFloorMillis
	}
	return gain * candidateReliability(c) / float64(cost)
}

func candidateReliability(c *candidate) float64 {
	quality := 0.0
	for _, v := range c.coverage {
		if v > quality {
			quality = v
		}
	}
	reliability := 0.5 + 0.5*clampFloat(quality, 0, 1)
	if c.unit.DurationSampleCount < 5 {
		reliability *= 0.95
	}
	if c.unit.FlakeRate > 0 {
		// Flaky tests remain candidates, but their expected cost is increased and
		// their selection rationale retains the flakiness information.
		reliability *= 1 - math.Min(0.25, c.unit.FlakeRate*0.25)
	}
	return reliability
}

func calculateConfidence(req domain.PlanRequest, evidence EvidenceSnapshot, impact ImpactResult, selected []*candidate, weightedCoverage, mapped float64) float64 {
	graph := 0.0
	if evidence.GraphComplete && evidence.Snapshot.IsComplete && !evidence.GraphMissingEdges {
		graph = 1
	}
	edge := impact.GraphConfidence
	freshness := 1.0
	if len(evidence.Coverage) == 0 && len(evidence.Edges) == 0 {
		freshness = 0.75
	}
	precision := 1.0
	if len(selected) == 0 {
		precision = 0
	} else {
		for _, c := range selected {
			q := 0.0
			for _, v := range c.coverage {
				if v > q {
					q = v
				}
			}
			if q > 0 && q < precision {
				precision = q
			}
			if !c.durationKnown {
				precision = math.Min(precision, 0.75)
			} else if c.unit.DurationSampleCount < 5 {
				precision = math.Min(precision, 0.97)
			}
		}
	}
	historyFactor := 0.8
	comparable := 0
	for _, h := range evidence.History {
		if h.Comparable && h.SampleCount > 0 {
			comparable += h.SampleCount
		}
	}
	if comparable >= 5 {
		historyFactor = 1
	} else if comparable > 0 {
		historyFactor = 0.9
	}
	environment := 0.0
	if evidence.ToolchainComparable && req.EnvironmentFingerprint != "" {
		environment = 1
	}
	critical := 1.0
	for _, e := range impact.Entities {
		if !e.Critical {
			continue
		}
		covered := false
		for _, c := range selected {
			if c.coverage[e.StableKey] >= 0.95 {
				covered = true
				break
			}
		}
		if !covered {
			critical = 0
			break
		}
	}
	value := 0.20*graph + 0.15*edge + 0.15*freshness + 0.10*precision + 0.20*clampFloat(mapped, 0, 1) + 0.05*historyFactor + 0.10*environment + 0.05*critical
	return clampFloat(value, 0, 1)
}

func makePlanUnits(selected []*candidate, entities []ImpactEntity, fallback bool) []domain.PlanUnit {
	selected = append([]*candidate(nil), selected...)
	sort.SliceStable(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		if a.mandatory != b.mandatory {
			return a.mandatory
		}
		if a.utility != b.utility {
			return a.utility > b.utility
		}
		if a.direct != b.direct {
			return a.direct
		}
		return a.unit.StableKey < b.unit.StableKey
	})
	units := make([]domain.PlanUnit, 0, len(selected))
	covered := make(map[string]float64, len(entities))
	for _, c := range selected {
		codes := make([]domain.SelectedReasonCode, 0, len(c.reasons))
		for code := range c.reasons {
			codes = append(codes, code)
		}
		sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
		coverage := 0.0
		for _, e := range entities {
			quality := c.coverage[e.StableKey]
			if quality > covered[e.StableKey] {
				coverage += e.Weight * (quality - covered[e.StableKey])
				covered[e.StableKey] = quality
			}
		}
		for _, e := range entities {
			if e.Critical && c.coverage[e.StableKey] > 0 {
				codes = appendUniqueReason(codes, domain.ReasonHighRiskCoverage)
			}
		}
		if fallback {
			codes = appendUniqueReason(codes, domain.ReasonFallbackFullSuite)
		}
		if c.unit.FlakeRate > 0 {
			// Flakiness is described in the text; no noncanonical reason code is added.
		}
		explanation := "selected by deterministic weighted set-cover based on current evidence"
		if c.mandatory {
			explanation = "mandatory policy gate; never removed by optimization"
		}
		if c.direct {
			explanation = "test unit is directly changed by this diff"
		}
		if c.unit.FlakeRate > 0 {
			explanation += fmt.Sprintf("; flaky history %.3f is retained and affects retry policy", c.unit.FlakeRate)
		}
		units = append(units, domain.PlanUnit{UnitID: c.unit.ID, StableKey: c.unit.StableKey, UnitType: c.unit.UnitType,
			ReasonCodes: codes, CoverageContribution: clampFloat(coverage, 0, 1), RiskContribution: 0,
			EstimatedDurationMillis: c.duration, ExecutionGroup: c.unit.ExecutionGroup, Mandatory: c.mandatory,
			TargetRef: c.unit.TargetRef, SelectionGranularity: c.unit.SelectionGranularity, FlakeRate: c.unit.FlakeRate, Explanation: explanation})
	}
	return units
}

func mandatoryPlanUnits(byKey map[string]*candidate) []domain.PlanUnit {
	var selected []*candidate
	for _, c := range byKey {
		if c.mandatory {
			selected = append(selected, c)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].unit.StableKey < selected[j].unit.StableKey })
	return makePlanUnits(selected, nil, false)
}

func omissions(units []domain.TestUnit, selected []*candidate, byKey map[string]*candidate) []domain.OmittedUnit {
	chosen := make(map[string]bool, len(selected))
	for _, c := range selected {
		chosen[c.unit.StableKey] = true
	}
	var out []domain.OmittedUnit
	for _, unit := range units {
		if chosen[unit.StableKey] || unit.Mandatory || (byKey[unit.StableKey] != nil && byKey[unit.StableKey].mandatory) {
			continue
		}
		code := domain.OmitNoImpactEdgeObserved
		c := byKey[unit.StableKey]
		explanation := "not selected by the current evidence and policy; this is not proof of irrelevance"
		switch {
		case !unit.Selectable:
			code = domain.OmitNotSelectableGranularity
			explanation = "adapter cannot safely select this unit at the requested granularity"
		case c == nil:
			code = domain.OmitNotInActiveAdapterScope
			explanation = "unit is outside the active adapter discovery scope; this is not proof of irrelevance"
		case len(c.coverage) == 0 && !c.direct:
			code = domain.OmitNoImpactEdgeObserved
		case c.selected:
			code = domain.OmitCoverageAlreadySatisfied
		case c.utility > 0:
			code = domain.OmitLowerMarginalCoveragePerCost
		}
		out = append(out, domain.OmittedUnit{StableKey: unit.StableKey, ReasonCodes: []domain.OmissionReasonCode{code}, Explanation: explanation})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StableKey < out[j].StableKey })
	return out
}

func fullRequired(plan domain.VerificationPlan, byKey map[string]*candidate, policy domain.Policy, explanation string) domain.VerificationPlan {
	plan.Status = domain.PlanFullRequired
	plan.SelectedUnits = nil
	plan.MandatoryUnits = mandatoryPlanUnits(byKey)
	plan.OmittedUnits = nil
	fallback := domain.PlanUnit{UnitID: uuid.New(), StableKey: "FULL_SUITE", UnitType: domain.UnitProjectSuite,
		ReasonCodes: []domain.SelectedReasonCode{domain.ReasonFallbackFullSuite}, EstimatedDurationMillis: 0,
		Mandatory: true, TargetRef: policy.FullSuiteTargetRef, SelectionGranularity: "PROJECT_SUITE",
		Explanation: "safety escalation requires the locally configured authoritative full-suite command"}
	plan.SelectedUnits = append([]domain.PlanUnit{fallback}, plan.MandatoryUnits...)
	if explanation != "" {
		if plan.Explanation == "" {
			plan.Explanation = explanation
		} else {
			plan.Explanation += " " + explanation
		}
	}
	if plan.EscalationReasons == nil {
		plan.EscalationReasons = []domain.EscalationReason{}
	}
	return plan
}

func finalizeChecks(plan *domain.VerificationPlan, checks map[domain.EscalationReasonCode]*domain.HardRuleCheck) {
	plan.HardRuleChecks = plan.HardRuleChecks[:0]
	for _, c := range checks {
		plan.HardRuleChecks = append(plan.HardRuleChecks, *c)
	}
	sort.Slice(plan.HardRuleChecks, func(i, j int) bool { return plan.HardRuleChecks[i].Code < plan.HardRuleChecks[j].Code })
	sort.Slice(plan.EscalationReasons, func(i, j int) bool { return plan.EscalationReasons[i].Code < plan.EscalationReasons[j].Code })
}

func explanationSuffix(plan domain.VerificationPlan) string {
	var parts []string
	for _, contribution := range plan.RiskContributions {
		parts = append(parts, fmt.Sprintf("%s=%d (%s)", contribution.Feature, contribution.Points, contribution.Reason))
	}
	if len(parts) > 0 {
		return "Risk contributions: " + strings.Join(parts, "; ") + "."
	}
	return "No selective verification is considered proof that omitted checks would pass."
}

func selectedCount(candidates []*candidate) int {
	n := 0
	for _, c := range candidates {
		if c.mandatory || c.direct {
			n++
		}
	}
	return n
}

func selectionSlice(selected map[string]*candidate) []*candidate {
	out := make([]*candidate, 0, len(selected))
	for _, c := range selected {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].unit.StableKey < out[j].unit.StableKey })
	return out
}

func totalDuration(selected map[string]*candidate) int64 {
	var total int64
	for _, c := range selected {
		total += c.duration
	}
	return total
}

func estimatedDuration(unit domain.TestUnit, policy domain.Policy) int64 {
	var duration int64
	if unit.DurationSampleCount >= 5 && unit.DurationP95Millis > 0 {
		duration = unit.DurationP95Millis
	} else if unit.EstimatedMillis > 0 {
		duration = unit.EstimatedMillis
	} else {
		return 0
	}
	return duration + policy.StartupOverheadMillis
}

func allDurationsKnown(selected []*candidate) bool {
	for _, c := range selected {
		if !c.durationKnown {
			return false
		}
	}
	return len(selected) > 0
}

func addCandidateCoverage(c *candidate, entity string, quality float64, reason domain.SelectedReasonCode) {
	if quality <= 0 {
		return
	}
	quality = clampFloat(quality, 0, 1)
	if quality > c.coverage[entity] {
		c.coverage[entity] = quality
	}
	c.reasons[reason] = true
}

func mappedWeight(entities []ImpactEntity, candidates []*candidate) float64 {
	mapped := make(map[string]bool)
	for _, c := range candidates {
		for entity, quality := range c.coverage {
			if quality > 0 {
				mapped[entity] = true
			}
		}
	}
	total := 0.0
	for _, entity := range entities {
		if mapped[entity.StableKey] {
			total += entity.Weight
		}
	}
	return clampFloat(total, 0, 1)
}

func coverageThreshold(level domain.RiskLevel, policy domain.Policy) float64 {
	if level == domain.RiskLow {
		return policy.LowRiskMinWeightedCoverage
	}
	return policy.MediumRiskMinWeightedCoverage
}

func coverageFreshness(c domain.CoverageObservation, req domain.PlanRequest, snapshot domain.RepositorySnapshot, now time.Time, policy domain.Policy) float64 {
	if c.ObservedAt.IsZero() || now.Sub(c.ObservedAt) > policy.CoverageStaleness || c.ObservedAt.After(now.Add(time.Minute)) {
		return 0
	}
	if c.BuildConfigDigest == "" || snapshot.BuildConfigDigest == "" || c.BuildConfigDigest != snapshot.BuildConfigDigest {
		return 0
	}
	if req.EnvironmentFingerprint == "" || c.EnvironmentFingerprint != req.EnvironmentFingerprint {
		return 0
	}
	return 1
}

func testConfigurationMatches(observation domain.CoverageObservation, unit domain.TestUnit) bool {
	if observation.TestConfigDigest == "" || unit.Metadata == nil {
		return false
	}
	configured, ok := unit.Metadata["test_config_digest"].(string)
	return ok && configured != "" && configured == observation.TestConfigDigest
}

func validateGraphEvidence(evidence EvidenceSnapshot) bool {
	nodes := make(map[domain.UUID]bool, len(evidence.Nodes))
	stableKeys := make(map[string]bool, len(evidence.Nodes))
	valid := true
	for _, node := range evidence.Nodes {
		if domain.ValidateGraphNode(node, evidence.Snapshot.ID) != nil || nodes[node.ID] || stableKeys[node.StableKey] {
			valid = false
		}
		nodes[node.ID] = true
		stableKeys[node.StableKey] = true
	}
	for _, edge := range evidence.Edges {
		if domain.ValidateGraphEdge(edge, evidence.Snapshot.ID) != nil || !nodes[edge.FromNodeID] || !nodes[edge.ToNodeID] {
			valid = false
		}
	}
	return valid
}

func hasStaleGraphEvidence(evidence EvidenceSnapshot, now time.Time, maxAge time.Duration) bool {
	for _, node := range evidence.Nodes {
		if now.Sub(node.ObservedAt) > maxAge || node.ObservedAt.After(now.Add(time.Minute)) || (node.ValidUntil != nil && now.After(*node.ValidUntil)) {
			return true
		}
	}
	for _, edge := range evidence.Edges {
		if now.Sub(edge.ObservedAt) > maxAge || edge.ObservedAt.After(now.Add(time.Minute)) || (edge.ValidUntil != nil && now.After(*edge.ValidUntil)) {
			return true
		}
	}
	return false
}

func entitiesForCoverage(obs domain.CoverageObservation, nodes []domain.GraphNode, entityByPath map[string]ImpactEntity, entities []ImpactEntity) []ImpactEntity {
	if obs.CoveredPath != "" {
		if e, ok := entityByPath[obs.CoveredPath]; ok {
			return []ImpactEntity{e}
		}
	}
	for _, node := range nodes {
		if node.StableKey == obs.CoveredStableKey {
			if e, ok := entityByPath[node.Path]; ok {
				return []ImpactEntity{e}
			}
		}
	}
	for _, e := range entities {
		if e.StableKey == obs.CoveredStableKey || e.Path == obs.CoveredStableKey {
			return []ImpactEntity{e}
		}
	}
	return nil
}

func changedLineOverlap(obs domain.CoverageObservation, changes []domain.ChangedFile, entityPath string) bool {
	for _, change := range changes {
		if change.Path != entityPath && change.OldPath != entityPath {
			continue
		}
		if len(change.DiffHunks) == 0 {
			return true
		}
		for _, h := range change.DiffHunks {
			if obs.LineStart <= h.End && obs.LineEnd >= h.Start {
				return true
			}
		}
	}
	return false
}

func precisionFactor(precision domain.CoveragePrecision) float64 {
	switch precision {
	case domain.CoverageLine:
		return 1
	case domain.CoverageSymbol:
		return 0.99
	case domain.CoverageFile:
		return 0.98
	case domain.CoverageTarget:
		return 0.96
	case domain.CoverageModule:
		return 0.95
	default:
		return 0
	}
}

func precisionForNode(node domain.GraphNode) domain.CoveragePrecision {
	switch node.NodeType {
	case domain.NodeSymbol:
		return domain.CoverageSymbol
	case domain.NodeSourceFile:
		return domain.CoverageFile
	case domain.NodeBuildTarget, domain.NodeTestTarget:
		return domain.CoverageTarget
	default:
		return domain.CoverageModule
	}
}

func reasonForPrecision(precision domain.CoveragePrecision) domain.SelectedReasonCode {
	switch precision {
	case domain.CoverageLine:
		return domain.ReasonCoversChangedLine
	case domain.CoverageSymbol:
		return domain.ReasonCoversChangedSymbol
	default:
		return domain.ReasonAffectedBuildTarget
	}
}

func nodeConfidence(node domain.GraphNode) float64 {
	if node.Confidence < 0 || node.Confidence > 1 {
		return 0
	}
	return node.Confidence
}

func deduplicateCandidates(in []*candidate) []*candidate {
	seen := make(map[string]bool)
	var out []*candidate
	for _, c := range in {
		if c != nil && !seen[c.unit.StableKey] {
			seen[c.unit.StableKey] = true
			out = append(out, c)
		}
	}
	return out
}

func isChangedKey(key string, changes []domain.ChangedFile) bool {
	for _, f := range changes {
		if key == f.Path || key == f.OldPath || key == "file:"+f.Path || (f.OldPath != "" && key == "file:"+f.OldPath) {
			return true
		}
	}
	return false
}

func matchAny(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if GlobMatch(pattern, value) {
			return true
		}
	}
	return false
}

func isLockfilePath(value string) bool {
	base := strings.ToLower(path.Base(value))
	return strings.Contains(base, "lock") || strings.HasSuffix(base, ".lock") || base == "gradle-wrapper.properties"
}

func isMigrationPath(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "/migrations/") || strings.HasPrefix(value, "migrations/") || strings.Contains(value, "/db/migrate/") || strings.HasPrefix(value, "db/migrate/")
}

func isContractPath(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasSuffix(lower, ".proto") || strings.HasSuffix(lower, ".graphql") || strings.HasSuffix(lower, ".openapi.yaml") || strings.HasSuffix(lower, ".openapi.json") || strings.HasSuffix(lower, ".schema.json") || strings.Contains(lower, "/contracts/") || strings.HasPrefix(lower, "contracts/")
}

func isHardConfigPath(value string, patterns []string) bool {
	if matchAny(value, patterns) {
		return true
	}
	base := strings.ToLower(path.Base(value))
	if strings.Contains(base, "lock") || base == "pom.xml" || strings.HasPrefix(base, "build.gradle") || strings.HasPrefix(base, "settings.gradle") || base == "nx.json" || base == "pytest.ini" || strings.HasPrefix(base, "jest.config.") {
		return true
	}
	return false
}

func allDocumentation(changes []domain.ChangedFile, patterns []string) bool {
	if len(changes) == 0 {
		return false
	}
	for _, change := range changes {
		if len(patterns) > 0 && (matchAny(change.Path, patterns) || (change.OldPath != "" && matchAny(change.OldPath, patterns))) {
			continue
		}
		lower := strings.ToLower(change.Path)
		if strings.HasSuffix(lower, ".md") || strings.HasPrefix(lower, "docs/") {
			continue
		}
		return false
	}
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func appendUniqueReason(values []domain.SelectedReasonCode, target domain.SelectedReasonCode) []domain.SelectedReasonCode {
	for _, value := range values {
		if value == target {
			return values
		}
	}
	return append(values, target)
}

func riskBasedCalibrated(policy domain.Policy) bool {
	return policy.ObservedCalibrationRuns >= policy.MinimumCalibrationRuns && policy.ObservedAuditRuns >= policy.MinimumAuditRuns
}

func hasTriggered(checks map[domain.EscalationReasonCode]*domain.HardRuleCheck) bool {
	for _, check := range checks {
		if check.Triggered {
			return true
		}
	}
	return false
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func evidenceRefs(evidence EvidenceSnapshot, impact ImpactResult) []domain.EvidenceReference {
	refs := []domain.EvidenceReference{{Kind: "REPOSITORY_SNAPSHOT", StableKey: evidence.Snapshot.ID.String(), Digest: evidence.Snapshot.TreeHash, ObservedAt: evidence.Snapshot.EvidenceGeneratedAt},
		{Kind: "IMPACT_GRAPH", StableKey: fmt.Sprintf("graph:%d", evidence.Snapshot.GraphVersion), Digest: evidence.Snapshot.AdapterSetDigest, ObservedAt: evidence.Snapshot.EvidenceGeneratedAt}}
	for _, entity := range impact.Entities {
		refs = append(refs, domain.EvidenceReference{Kind: "CHANGED_ENTITY", StableKey: entity.StableKey})
	}
	for _, coverage := range evidence.Coverage {
		if coverage.ReportHash == "" {
			continue
		}
		refs = append(refs, domain.EvidenceReference{Kind: "COVERAGE_REPORT", StableKey: coverage.RunID.String(), Digest: coverage.ReportHash, ObservedAt: coverage.ObservedAt})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		if refs[i].StableKey != refs[j].StableKey {
			return refs[i].StableKey < refs[j].StableKey
		}
		return refs[i].Digest < refs[j].Digest
	})
	unique := refs[:0]
	for _, ref := range refs {
		if len(unique) > 0 {
			last := unique[len(unique)-1]
			if last.Kind == ref.Kind && last.StableKey == ref.StableKey && last.Digest == ref.Digest && last.ObservedAt.Equal(ref.ObservedAt) {
				continue
			}
		}
		unique = append(unique, ref)
	}
	return unique
}

func canonicalChanges(changes []domain.ChangedFile) []domain.ChangedFile {
	out := append([]domain.ChangedFile(nil), changes...)
	for i := range out {
		out[i].DetectedRiskLabels = uniqueSorted(out[i].DetectedRiskLabels)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].OldPath != out[j].OldPath {
			return out[i].OldPath < out[j].OldPath
		}
		return out[i].Status < out[j].Status
	})
	return out
}

func uniqueSorted(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	if len(values) < 2 {
		return values
	}
	unique := values[:1]
	for _, value := range values[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}
