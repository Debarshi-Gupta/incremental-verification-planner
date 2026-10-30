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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/bazel"
	"github.com/ivp/ivp/internal/adapters/generic"
	"github.com/ivp/ivp/internal/adapters/gradle"
	"github.com/ivp/ivp/internal/adapters/jest"
	"github.com/ivp/ivp/internal/adapters/maven"
	"github.com/ivp/ivp/internal/adapters/nx"
	"github.com/ivp/ivp/internal/adapters/pytest"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/gitutil"
	"github.com/ivp/ivp/internal/security"
)

type indexRecord struct {
	Snapshot        domain.RepositorySnapshot `json:"snapshot"`
	Units           []domain.TestUnit         `json:"units"`
	Nodes           []domain.GraphNode        `json:"nodes"`
	Edges           []domain.GraphEdge        `json:"edges"`
	ChangedFiles    []domain.ChangedFile      `json:"changed_files"`
	AdapterVersions map[string]string         `json:"adapter_versions"`
	Adapters        []string                  `json:"adapters"`
}
type snapshotUpload struct {
	Snapshot  domain.RepositorySnapshot    `json:"snapshot"`
	Nodes     []domain.GraphNode           `json:"nodes"`
	Edges     []domain.GraphEdge           `json:"edges"`
	TestUnits []domain.TestUnit            `json:"test_units"`
	Coverage  []domain.CoverageObservation `json:"coverage"`
}

func (c *commands) index(ctx context.Context, out io.Writer, cfg config.Config, full bool) error {
	client, err := newAPIClient(cfg)
	if err != nil {
		return err
	}
	root, err := gitutil.RepositoryRoot(ctx, ".")
	if err != nil {
		return exitError{2, fmt.Errorf("repository root: %w", err)}
	}
	changes, err := gitutil.DetectChanges(ctx, root, cfg.Analysis.BaseRef)
	if err != nil {
		return exitError{6, fmt.Errorf("index change metadata: %w", err)}
	}
	repo, err := client.repository(ctx, cfg.Repository.Slug)
	if err != nil {
		return err
	}
	windowsUnsupported := runtime.GOOS == "windows"
	if windowsUnsupported {
		fb, _ := fallbackInstruction(cfg)
		fmt.Fprintf(out, "Windows does not support selective execution. Full-suite fallback argv: %s\n", fb)
	}
	all := registry()
	var units []domain.TestUnit
	var nodes []domain.GraphNode
	var edges []domain.GraphEdge
	var discovered []string
	versions := map[string]string{}
	complete := true
	var reasons []string
	edgeLimitReported := false
	appendEdge := func(edge domain.GraphEdge) {
		if len(edges) >= 2_000_000 {
			complete = false
			if !edgeLimitReported {
				reasons = append(reasons, "graph edge inventory exceeded the API limit")
				edgeLimitReported = true
			}
			return
		}
		edges = append(edges, edge)
	}
	type dependencyEvidence struct {
		adapter string
		from    uuid.UUID
		refs    []string
	}
	type buildLinkEvidence struct {
		adapter string
		from    uuid.UUID
		refs    []string
	}
	var pendingDependencies []dependencyEvidence
	var pendingBuildLinks []buildLinkEvidence
	type discoveredBuildTarget struct {
		adapter string
		target  adapters.BuildTarget
	}
	var discoveredBuildTargets []discoveredBuildTarget
	unitNodesByRef := map[string][]uuid.UUID{}
	affectedByAdapter := map[string][]string{}
	type sourceMappingEvidence struct {
		adapter string
		mapping adapters.SourceTestMapping
	}
	var sourceMappings []sourceMappingEvidence
	sourceCoverageComplete := map[string]bool{}
	buildDependencyComplete := map[string]bool{}
	now := time.Now().UTC()
	snapshotID := uuid.New()
	for _, adapter := range all {
		adapterCfg := cfg.ToAdapterConfig(adapter.ID(), nil)
		if !adapterCfg.Enabled {
			continue
		}
		detection, detectErr := adapter.Detect(ctx, root)
		if detectErr != nil {
			complete = false
			reasons = append(reasons, adapter.ID()+" detection failed")
			continue
		}
		if !detection.Detected {
			continue
		}
		discovered = append(discovered, adapter.ID())
		if windowsUnsupported {
			complete = false
			reasons = append(reasons, adapter.ID()+" discovery is not executed on Windows")
			continue
		}
		result, discoverErr := adapter.Discover(ctx, adapters.DiscoverRequest{Root: root, Config: adapterCfg, ChangedFiles: changedPaths(changes.Files), BaseSHA: changes.BaseSHA, HeadSHA: changes.HeadSHA, Full: full, MaxOutputBytes: int64(cfg.Execution.MaxOutputBytes)})
		if discoverErr != nil || !result.Complete {
			complete = false
			reasons = append(reasons, adapter.ID()+" discovery is incomplete")
		}
		if result.Version != "" {
			versions[adapter.ID()] = safeVersion(result.Version)
		} else if version, versionErr := adapter.Version(ctx, root); versionErr == nil {
			versions[adapter.ID()] = safeVersion(version)
		}
		for _, reason := range result.Reasons {
			reasons = append(reasons, adapter.ID()+": "+security.Redact(reason, 160))
		}
		if len(result.AffectedTargets) > 0 {
			affectedByAdapter[adapter.ID()] = append([]string(nil), result.AffectedTargets...)
		}
		for _, mapping := range result.SourceMappings {
			sourceMappings = append(sourceMappings, sourceMappingEvidence{adapter: adapter.ID(), mapping: mapping})
		}
		for _, target := range result.BuildTargets {
			discoveredBuildTargets = append(discoveredBuildTargets, discoveredBuildTarget{adapter: adapter.ID(), target: target})
		}
		sourceCoverageComplete[adapter.ID()] = result.SourceToTestEvidenceComplete
		buildDependencyComplete[adapter.ID()] = result.BuildDependencyEvidenceComplete
		for _, found := range result.Units {
			if len(units) >= 200000 {
				complete = false
				reasons = append(reasons, "test-unit inventory exceeded the API limit")
				break
			}
			u := adapters.DomainUnit(adapter.ID(), snapshotID, found)
			if adapter.ID() != "generic" {
				u.StableKey = adapter.ID() + ":" + u.StableKey
			}
			u.SourcePaths = u.SourcePaths[:0]
			for _, sourcePath := range found.Paths {
				sourcePath = filepath.ToSlash(sourcePath)
				if _, pathErr := domain.NormalizeRepoPath(sourcePath); pathErr != nil {
					complete = false
					reasons = append(reasons, adapter.ID()+" emitted an invalid unit path")
					continue
				}
				u.SourcePaths = append(u.SourcePaths, sourcePath)
			}
			if u.StableKey == "" {
				complete = false
				continue
			}
			if u.EstimatedMillis == 0 {
				u.EstimatedMillis = found.EstimatedDuration.Milliseconds()
			}
			if u.EstimatedMillis == 0 && found.Timeout > 0 {
				u.EstimatedMillis = found.Timeout.Milliseconds()
			}
			if u.TargetRef == "" {
				u.TargetRef = found.Key
			}
			u.Metadata = map[string]any{"display_name": found.DisplayName, "runner": found.Runner, "dependencies": append([]string(nil), found.Dependencies...), "report_paths": append([]string(nil), found.ReportPaths...), "coverage_paths": append([]string(nil), found.CoveragePaths...), "timeout_millis": found.Timeout.Milliseconds(), "trusted_config": found.TrustedConfig}
			u.LastSeenAt = now
			units = append(units, u)
			unitNodeID := uuid.New()
			nodeType := domain.NodeTestTarget
			if u.UnitType == domain.UnitBuildTarget {
				nodeType = domain.NodeBuildTarget
			}
			if u.UnitType == domain.UnitTestCase || u.UnitType == domain.UnitTestFile || u.UnitType == domain.UnitTestMethod {
				nodeType = domain.NodeTestCase
			}
			if u.UnitType == domain.UnitCheck {
				nodeType = domain.NodeCheckTarget
			}
			var primaryPath string
			if len(u.SourcePaths) > 0 {
				primaryPath = u.SourcePaths[0]
			}
			nodes = append(nodes, domain.GraphNode{ID: unitNodeID, SnapshotID: snapshotID, NodeType: nodeType, StableKey: "unit:" + u.StableKey, Path: primaryPath, DisplayName: found.DisplayName, Labels: append([]string(nil), found.RiskLabels...), EvidenceSource: adapter.ID(), Confidence: 0.95, ObservedAt: now, Attributes: map[string]any{"unit_key": u.StableKey}})
			addUnitNodeRef(unitNodesByRef, adapter.ID()+"\x00"+u.TargetRef, unitNodeID)
			addUnitNodeRef(unitNodesByRef, adapter.ID()+"\x00"+u.StableKey, unitNodeID)
			pendingDependencies = append(pendingDependencies, dependencyEvidence{adapter: adapter.ID(), from: unitNodeID, refs: append([]string(nil), found.Dependencies...)})
			pendingBuildLinks = append(pendingBuildLinks, buildLinkEvidence{adapter: adapter.ID(), from: unitNodeID, refs: append([]string(nil), found.BuildTargets...)})
		}
	}
	buildNodeByRef := make(map[string]uuid.UUID, len(discoveredBuildTargets))
	buildTargetByRef := make(map[string]adapters.BuildTarget, len(discoveredBuildTargets))
	for _, item := range discoveredBuildTargets {
		target := item.target
		if target.Key == "" || target.EvidenceSource == "" || target.Confidence <= 0 || target.Confidence > 1 || strings.ContainsAny(target.SourcePattern, "\\\r\n\x00") {
			complete = false
			reasons = append(reasons, item.adapter+" emitted invalid build-target evidence")
			continue
		}
		ref := item.adapter + "\x00" + target.Key
		if _, duplicate := buildNodeByRef[ref]; duplicate {
			complete = false
			reasons = append(reasons, item.adapter+" emitted a duplicate build-target key")
			continue
		}
		nodeID := uuid.New()
		stableKey := "build:" + item.adapter + ":" + target.Key
		nodes = append(nodes, domain.GraphNode{ID: nodeID, SnapshotID: snapshotID, NodeType: domain.NodeBuildTarget, StableKey: stableKey,
			DisplayName: target.Key, EvidenceSource: target.EvidenceSource, Confidence: target.Confidence, ObservedAt: now,
			Attributes: map[string]any{"adapter": item.adapter, "target_key": target.Key, "source_pattern": target.SourcePattern}})
		buildNodeByRef[ref] = nodeID
		buildTargetByRef[ref] = target
	}
	for _, item := range discoveredBuildTargets {
		from := buildNodeByRef[item.adapter+"\x00"+item.target.Key]
		if from == uuid.Nil {
			continue
		}
		for _, dependency := range item.target.Dependencies {
			to := buildNodeByRef[item.adapter+"\x00"+dependency]
			if to == uuid.Nil || to == from {
				complete = false
				reasons = append(reasons, item.adapter+" emitted an unresolved build-target dependency")
				continue
			}
			appendEdge(domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: from, ToNodeID: to, EdgeType: domain.EdgeDependsOn,
				Confidence: item.target.Confidence, EvidenceSource: item.target.EvidenceSource, ObservedAt: now})
		}
	}
	for _, link := range pendingBuildLinks {
		for _, ref := range link.refs {
			to := buildNodeByRef[link.adapter+"\x00"+ref]
			if to == uuid.Nil {
				complete = false
				reasons = append(reasons, link.adapter+" test unit referenced an undiscovered build target")
				continue
			}
			appendEdge(domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: link.from, ToNodeID: to, EdgeType: domain.EdgeDependsOn,
				Confidence: 0.85, EvidenceSource: link.adapter + ":unit-build-target", ObservedAt: now})
		}
	}
	for _, dependency := range pendingDependencies {
		for _, ref := range dependency.refs {
			matches := unitNodesByRef[dependency.adapter+"\x00"+ref]
			if len(matches) != 1 {
				complete = false
				reasons = append(reasons, dependency.adapter+" emitted an unresolved or ambiguous build dependency")
				continue
			}
			to := matches[0]
			if to == dependency.from {
				complete = false
				reasons = append(reasons, dependency.adapter+" emitted a self-referential build dependency")
				continue
			}
			appendEdge(domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: dependency.from, ToNodeID: to, EdgeType: domain.EdgeDependsOn, Confidence: 0.9, EvidenceSource: dependency.adapter + ":discovery", ObservedAt: now})
		}
	}
	sourceNodeByPath := make(map[string]uuid.UUID)
	for _, node := range nodes {
		if node.NodeType == domain.NodeSourceFile && node.Path != "" {
			sourceNodeByPath[node.Path] = node.ID
		}
	}
	addSourceNode := func(path, evidenceSource string, attributes map[string]any) uuid.UUID {
		path = filepath.ToSlash(path)
		if id := sourceNodeByPath[path]; id != uuid.Nil {
			return id
		}
		if _, pathErr := domain.NormalizeRepoPath(path); pathErr != nil {
			complete = false
			reasons = append(reasons, "repository inventory contains an invalid path")
			return uuid.Nil
		}
		if len(nodes) >= 500000 {
			complete = false
			reasons = append(reasons, "source inventory exceeded the graph node limit")
			return uuid.Nil
		}
		id := uuid.New()
		nodes = append(nodes, domain.GraphNode{ID: id, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:" + path, Path: path, DisplayName: path, EvidenceSource: evidenceSource, Confidence: 1, ObservedAt: now, Attributes: attributes})
		sourceNodeByPath[path] = id
		return id
	}
	inventoryCmd := exec.CommandContext(ctx, "git", "ls-files", "-z")
	inventoryCmd.Dir = root
	tracked, inventoryErr := inventoryCmd.Output()
	if inventoryErr != nil {
		complete = false
		reasons = append(reasons, "tracked source inventory could not be enumerated")
	} else if len(tracked) > 64<<20 {
		complete = false
		reasons = append(reasons, "tracked source inventory exceeded the input limit")
	} else {
		for _, path := range strings.Split(string(tracked), "\x00") {
			if path == "" {
				continue
			}
			addSourceNode(path, "git-index", map[string]any{"tracked": true})
		}
	}
	for _, change := range changes.Files {
		for _, path := range []string{change.Path, change.OldPath} {
			if path == "" {
				continue
			}
			path = filepath.ToSlash(path)
			if _, pathErr := domain.NormalizeRepoPath(path); pathErr != nil {
				complete = false
				reasons = append(reasons, "git change metadata contains an invalid repository path")
				continue
			}
			addSourceNode(path, "git-diff", map[string]any{"change_status": change.Status})
		}
	}
	for ref, target := range buildTargetByRef {
		if target.SourcePattern == "" {
			continue
		}
		buildNodeID := buildNodeByRef[ref]
		for sourcePath, sourceID := range sourceNodeByPath {
			if !analysis.GlobMatch(target.SourcePattern, sourcePath) {
				continue
			}
			adapterID := strings.SplitN(ref, "\x00", 2)[0]
			appendEdge(domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: buildNodeID, ToNodeID: sourceID,
				EdgeType: domain.EdgeDependsOn, Confidence: target.Confidence, EvidenceSource: target.EvidenceSource,
				ObservedAt: now, Attributes: map[string]any{"precision": "PROJECT_SOURCE_MEMBERSHIP", "path_pattern": target.SourcePattern, "adapter": adapterID}})
		}
	}
	// Related-test results are explicit adapter discovery evidence tied to the
	// current change set. They are retained with provenance and conservative
	// confidence, and never promoted to complete evidence by themselves.
	for adapterID, targets := range affectedByAdapter {
		for _, target := range targets {
			matches := unitNodesByRef[adapterID+"\x00"+target]
			if len(matches) != 1 {
				complete = false
				reasons = append(reasons, adapterID+" related-test candidate was unresolved or ambiguous")
				continue
			}
			for _, change := range changes.Files {
				for _, changedPath := range []string{change.Path, change.OldPath} {
					if changedPath == "" {
						continue
					}
					sourceID := sourceNodeByPath[filepath.ToSlash(changedPath)]
					if sourceID == uuid.Nil {
						continue
					}
					appendEdge(domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: matches[0], ToNodeID: sourceID, EdgeType: domain.EdgeDependsOn, Confidence: 0.65, EvidenceSource: adapterID + ":related-test-candidates", ObservedAt: now, Attributes: map[string]any{"precision": "CHANGE_SET_CANDIDATE", "change_set_head": changes.HeadSHA}})
				}
			}
		}
	}
	// Explicit mappings and native project-membership relations are usable for
	// candidate generation when the changed path matches their declared scope.
	// Module membership is recorded as a build dependency, not as proof that a
	// test directly covered the source file.
	for _, evidence := range sourceMappings {
		mapping := evidence.mapping
		if mapping.Confidence <= 0 || mapping.Confidence > 1 || (mapping.Precision != "FILE" && mapping.Precision != "MODULE") || strings.ContainsAny(mapping.PathPattern, "\\\r\n\x00") {
			complete = false
			reasons = append(reasons, evidence.adapter+" source mapping was invalid")
			continue
		}
		matches := unitNodesByRef[evidence.adapter+"\x00"+mapping.TargetKey]
		if len(matches) != 1 || mapping.PathPattern == "" {
			complete = false
			reasons = append(reasons, evidence.adapter+" source mapping was unresolved or invalid")
			continue
		}
		for _, change := range changes.Files {
			for _, changedPath := range []string{change.Path, change.OldPath} {
				if changedPath == "" || !analysis.GlobMatch(mapping.PathPattern, filepath.ToSlash(changedPath)) {
					continue
				}
				sourceID := sourceNodeByPath[filepath.ToSlash(changedPath)]
				if sourceID == uuid.Nil {
					continue
				}
				edgeType := domain.EdgeTestCovers
				if mapping.Precision == "MODULE" {
					edgeType = domain.EdgeDependsOn
				}
				appendEdge(domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: matches[0], ToNodeID: sourceID,
					EdgeType: edgeType, Confidence: mapping.Confidence, EvidenceSource: mapping.EvidenceSource, ObservedAt: now,
					Attributes: map[string]any{"precision": mapping.Precision, "path_pattern": mapping.PathPattern}})
			}
		}
	}
	if len(discovered) == 0 {
		complete = false
		reasons = append(reasons, "no enabled adapter was detected")
	}
	if len(units) == 0 {
		complete = false
		reasons = append(reasons, "no runnable verification units were discovered")
	}
	// An empty edge set can be valid (for example, an isolated project with no
	// declared dependencies). Completeness is established only by adapter
	// attestations below; observed edges alone never promote the snapshot.
	for _, adapterID := range discovered {
		if !sourceCoverageComplete[adapterID] {
			complete = false
			reasons = append(reasons, adapterID+" did not attest complete source-to-test evidence")
		}
		if !buildDependencyComplete[adapterID] {
			complete = false
			reasons = append(reasons, adapterID+" did not attest complete build dependency evidence")
		}
	}
	// Duplicate stable keys from two adapters are unsafe because planner and
	// persistence identity is per snapshot; fail closed before upload.
	seen := map[string]bool{}
	for _, unit := range units {
		if seen[unit.StableKey] {
			complete = false
			reasons = append(reasons, "multiple adapters emitted the same stable unit key")
			break
		}
		seen[unit.StableKey] = true
	}
	adapterDigest, err := domain.CanonicalDigest(struct {
		Names    []string
		Versions map[string]string
	}{sorted(discovered), versions})
	if err != nil {
		return exitError{6, err}
	}
	toolchainDigest, err := domain.CanonicalDigest(versions)
	if err != nil {
		return exitError{6, err}
	}
	configDigest, err := buildConfigDigest(ctx, root, changes.Files)
	if err != nil {
		return exitError{6, err}
	}
	testConfigDigest, err := domain.CanonicalDigest(struct {
		BuildConfigDigest string
		Adapters          config.AdaptersConfig
		FullSuiteArgv     []string
		FailureRetryCount int
	}{configDigest, cfg.Adapters, cfg.Execution.FullSuiteArgv, cfg.Execution.FailureRetryCount})
	if err != nil {
		return exitError{6, err}
	}
	for i := range units {
		if units[i].Metadata == nil {
			units[i].Metadata = map[string]any{}
		}
		units[i].Metadata["test_config_digest"] = testConfigDigest
	}
	snapshot := domain.RepositorySnapshot{ID: snapshotID, RepositoryID: repo.ID, CommitSHA: changes.HeadSHA, BaseSHA: changes.BaseSHA, TreeHash: changes.TreeHash, WorktreeHash: changes.WorktreeHash, SubmoduleDigest: changes.SubmoduleDigest, ToolchainDigest: toolchainDigest, BuildConfigDigest: configDigest, TestConfigDigest: testConfigDigest, AdapterSetDigest: adapterDigest, GraphVersion: 1, EvidenceGeneratedAt: now, IsComplete: complete, CompletenessReasons: uniqueSorted(reasons)}
	record := indexRecord{Snapshot: snapshot, Units: units, Nodes: nodes, Edges: edges, ChangedFiles: changes.Files, AdapterVersions: versions, Adapters: sorted(discovered)}
	cachePath, err := adapters.ResolveWithin(root, filepath.Join(".ivp", "index.json"))
	if err != nil {
		return exitError{6, errors.New("local IVP metadata path is unsafe")}
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return exitError{6, err}
	}
	cacheData, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return exitError{6, err}
	}
	if err = os.WriteFile(cachePath, cacheData, 0o600); err != nil {
		return exitError{6, err}
	}
	var response struct {
		SnapshotID uuid.UUID `json:"snapshot_id"`
	}
	if err := client.request(ctx, http.MethodPost, "/api/v1/repositories/"+repo.ID.String()+"/snapshots", snapshotUpload{Snapshot: snapshot, Nodes: nodes, Edges: edges, TestUnits: units, Coverage: []domain.CoverageObservation{}}, newIdempotencyKey(), &response); err != nil {
		return err
	}
	if response.SnapshotID != uuid.Nil {
		record.Snapshot.ID = response.SnapshotID
		cacheData, err = json.MarshalIndent(record, "", "  ")
		if err != nil {
			return exitError{6, err}
		}
		if err = os.WriteFile(cachePath, cacheData, 0o600); err != nil {
			return exitError{6, err}
		}
	}
	fmt.Fprintf(out, "Snapshot: %s\nRepository: %s\nAdapters: %s\nDiscovered units: %d\nGraph complete: %t\n", record.Snapshot.ID, repo.Slug, strings.Join(record.Adapters, ", "), len(units), complete)
	for _, reason := range record.Snapshot.CompletenessReasons {
		fmt.Fprintf(out, "Evidence limitation: %s\n", reason)
	}
	if !complete {
		return exitError{6, errors.New("snapshot metadata uploaded as incomplete; planning will require full verification")}
	}
	return nil
}

func addUnitNodeRef(index map[string][]uuid.UUID, key string, nodeID uuid.UUID) {
	if key == "" || nodeID == uuid.Nil {
		return
	}
	for _, existing := range index[key] {
		if existing == nodeID {
			return
		}
	}
	index[key] = append(index[key], nodeID)
}

func registry() []adapters.Adapter {
	return []adapters.Adapter{gradle.New(), maven.New(), bazel.New(), nx.New(), pytest.New(), jest.New(), generic.New()}
}
func changedPaths(files []domain.ChangedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
		if f.OldPath != "" {
			out = append(out, f.OldPath)
		}
	}
	return out
}
func safeVersion(version string) string {
	version = security.Redact(strings.TrimSpace(version), 128)
	if i := strings.IndexAny(version, "\r\n"); i >= 0 {
		version = version[:i]
	}
	return version
}
func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func buildConfigDigest(ctx context.Context, root string, changes []domain.ChangedFile) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		return "", errors.New("unable to enumerate tracked configuration files")
	}
	paths := map[string]bool{}
	for _, p := range strings.Split(string(raw), "\x00") {
		if p != "" && isBuildConfigPath(p) {
			paths[p] = true
		}
	}
	for _, f := range changes {
		if isBuildConfigPath(f.Path) {
			paths[f.Path] = true
		}
		if isBuildConfigPath(f.OldPath) {
			paths[f.OldPath] = true
		}
	}
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	h := sha256.New()
	var total int64
	for _, p := range ordered {
		if _, err := domain.NormalizeRepoPath(p); err != nil {
			return "", err
		}
		full, err := adapters.ResolveWithin(root, p)
		if errors.Is(err, adapters.ErrInvalidPath) {
			return "", errors.New("build configuration path resolves outside repository")
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.Size() > 8<<20 {
			return "", fmt.Errorf("build configuration file %s exceeds size limit", p)
		}
		total += info.Size()
		if total > 128<<20 {
			return "", errors.New("build configuration digest input exceeds size limit")
		}
		file, err := os.Open(full)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, file)
		file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		_, _ = io.WriteString(h, "\x00"+filepath.ToSlash(p)+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func isBuildConfigPath(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	lower := strings.ToLower(filepath.ToSlash(p))
	if lower == ".ivp/config.yaml" || lower == ".ivp/config.yml" || lower == "ivp.yaml" || lower == "ivp.yml" {
		return true
	}
	if strings.Contains(lower, "/migrations/") || strings.HasPrefix(lower, "migrations/") {
		return true
	}
	for _, needle := range []string{"lock", "package.json", "pom.xml", "build.gradle", "settings.gradle", "gradle.properties", "gradle-wrapper", "module.bazel", "workspace", "build.bazel", "/build", "nx.json", "project.json", "pytest.ini", "pyproject.toml", "jest.config", "tox.ini", "setup.cfg", ".github/workflows/", ".gitlab-ci", "dockerfile", "compose", "openapi", "schema"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return strings.HasPrefix(base, "requirements") || strings.HasSuffix(base, ".toml") && strings.Contains(lower, "/gradle/")
}
