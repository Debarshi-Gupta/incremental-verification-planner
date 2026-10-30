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
		for _, found := range result.Units {
			u := adapters.DomainUnit(adapter.ID(), snapshotID, found)
			if adapter.ID() != "generic" {
				u.StableKey = adapter.ID() + ":" + u.StableKey
			}
			u.SourcePaths = append([]string(nil), found.Paths...)
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
			u.Metadata = map[string]any{"display_name": found.DisplayName, "dependencies": append([]string(nil), found.Dependencies...), "report_paths": append([]string(nil), found.ReportPaths...), "coverage_paths": append([]string(nil), found.CoveragePaths...), "timeout_millis": found.Timeout.Milliseconds(), "trusted_config": found.TrustedConfig}
			u.LastSeenAt = now
			units = append(units, u)
			unitNodeID := uuid.New()
			nodeType := domain.NodeTestTarget
			if u.UnitType == domain.UnitTestCase || u.UnitType == domain.UnitTestFile || u.UnitType == domain.UnitTestMethod {
				nodeType = domain.NodeTestCase
			}
			if u.UnitType == domain.UnitCheck {
				nodeType = domain.NodeCheckTarget
			}
			var primaryPath string
			if len(found.Paths) > 0 {
				primaryPath = filepath.ToSlash(found.Paths[0])
			}
			nodes = append(nodes, domain.GraphNode{ID: unitNodeID, SnapshotID: snapshotID, NodeType: nodeType, StableKey: "unit:" + u.StableKey, Path: primaryPath, DisplayName: found.DisplayName, Labels: append([]string(nil), found.RiskLabels...), EvidenceSource: adapter.ID(), Confidence: 0.95, ObservedAt: now, Attributes: map[string]any{"unit_key": u.StableKey}})
			if len(found.Paths) == 0 {
				complete = false
				reasons = append(reasons, adapter.ID()+" unit lacks source path or coverage mapping")
				continue
			}
			for _, p := range found.Paths {
				p = filepath.ToSlash(p)
				if _, pathErr := domain.NormalizeRepoPath(p); pathErr != nil {
					complete = false
					reasons = append(reasons, adapter.ID()+" emitted an invalid source path")
					continue
				}
				sourceID := findSourceNode(nodes, p)
				if sourceID == uuid.Nil {
					sourceID = uuid.New()
					nodes = append(nodes, domain.GraphNode{ID: sourceID, SnapshotID: snapshotID, NodeType: domain.NodeSourceFile, StableKey: "file:" + p, Path: p, DisplayName: p, EvidenceSource: adapter.ID(), Confidence: 0.95, ObservedAt: now})
				}
				edges = append(edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: unitNodeID, ToNodeID: sourceID, EdgeType: domain.EdgeTestCovers, Confidence: 0.85, EvidenceSource: adapter.ID(), ObservedAt: now})
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
	snapshot := domain.RepositorySnapshot{ID: snapshotID, RepositoryID: repo.ID, CommitSHA: changes.HeadSHA, BaseSHA: changes.BaseSHA, TreeHash: changes.TreeHash, WorktreeHash: changes.WorktreeHash, SubmoduleDigest: changes.SubmoduleDigest, ToolchainDigest: toolchainDigest, BuildConfigDigest: configDigest, AdapterSetDigest: adapterDigest, GraphVersion: 1, EvidenceGeneratedAt: now, IsComplete: complete, CompletenessReasons: uniqueSorted(reasons)}
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
func findSourceNode(nodes []domain.GraphNode, path string) uuid.UUID {
	for _, node := range nodes {
		if node.NodeType == domain.NodeSourceFile && node.Path == path {
			return node.ID
		}
	}
	return uuid.Nil
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
