package domain

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"strings"
)

var (
	ErrInvalidPath     = errors.New("invalid repository-relative path")
	ErrInvalidEvidence = errors.New("invalid evidence")
)

// NormalizeRepoPath validates and canonicalizes a POSIX-style repository path.
// Backslashes and absolute paths are rejected rather than reinterpreted.
func NormalizeRepoPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, `\`) || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, raw)
	}
	return clean, nil
}

func ValidateCanonicalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("canonical URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("canonical URL must use http or https")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("canonical URL must have a host and must not contain credentials, query, or fragment")
	}
	return nil
}

func ValidateChangedFile(f ChangedFile) error {
	if _, err := NormalizeRepoPath(f.Path); err != nil {
		return err
	}
	if f.Status != ChangeAdded && f.Status != ChangeModified && f.Status != ChangeDeleted && f.Status != ChangeRenamed && f.Status != ChangeCopied && f.Status != ChangeTypeChanged {
		return fmt.Errorf("unknown change status %q", f.Status)
	}
	if (f.Status == ChangeRenamed || f.Status == ChangeCopied) && f.OldPath == "" {
		return fmt.Errorf("%s change requires old_path", f.Status)
	}
	if f.OldPath != "" {
		if _, err := NormalizeRepoPath(f.OldPath); err != nil {
			return fmt.Errorf("old path: %w", err)
		}
	}
	for i, h := range f.DiffHunks {
		if h.Start < 0 || h.End < h.Start {
			return fmt.Errorf("diff hunk %d has invalid range", i)
		}
	}
	return nil
}

func ValidateSnapshot(s RepositorySnapshot) error {
	if s.ID == (UUID{}) || s.RepositoryID == (UUID{}) {
		return fmt.Errorf("%w: snapshot and repository IDs are required", ErrInvalidEvidence)
	}
	if s.CommitSHA == "" || s.BaseSHA == "" || s.TreeHash == "" || s.WorktreeHash == "" {
		return fmt.Errorf("%w: commit, base, tree, and worktree fingerprints are required", ErrInvalidEvidence)
	}
	if s.GraphVersion < 0 || s.EvidenceGeneratedAt.IsZero() {
		return fmt.Errorf("%w: graph version and evidence timestamp are invalid", ErrInvalidEvidence)
	}
	return nil
}

func ValidateGraphNode(n GraphNode, snapshotID UUID) error {
	if n.ID == (UUID{}) || n.SnapshotID != snapshotID || n.StableKey == "" || n.EvidenceSource == "" || n.ObservedAt.IsZero() {
		return fmt.Errorf("%w: graph node has missing identity, snapshot, stable key, or provenance", ErrInvalidEvidence)
	}
	if math.IsNaN(n.Confidence) || math.IsInf(n.Confidence, 0) || n.Confidence < 0 || n.Confidence > 1 {
		return fmt.Errorf("%w: node confidence must be in [0,1]", ErrInvalidEvidence)
	}
	switch n.NodeType {
	case NodeSourceFile, NodeSymbol, NodeBuildTarget, NodeTestTarget, NodeTestCase, NodeCheckTarget, NodeConfig, NodeAPISchema, NodeGeneratedOutput, NodeMigration, NodeRuntimeService, NodeOwner:
	default:
		return fmt.Errorf("%w: unknown graph node type %q", ErrInvalidEvidence, n.NodeType)
	}
	if n.Path != "" {
		if _, err := NormalizeRepoPath(n.Path); err != nil {
			return fmt.Errorf("node path: %w", err)
		}
	}
	return nil
}

func ValidateGraphEdge(e GraphEdge, snapshotID UUID) error {
	if e.ID == (UUID{}) || e.SnapshotID != snapshotID || e.FromNodeID == (UUID{}) || e.ToNodeID == (UUID{}) || e.EvidenceSource == "" || e.ObservedAt.IsZero() {
		return fmt.Errorf("%w: graph edge has missing identity, snapshot, endpoint, or provenance", ErrInvalidEvidence)
	}
	if math.IsNaN(e.Confidence) || math.IsInf(e.Confidence, 0) || e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("%w: edge confidence must be in [0,1]", ErrInvalidEvidence)
	}
	switch e.EdgeType {
	case EdgeDependsOn, EdgeTestCovers, EdgeBuiltFrom, EdgeConfigures, EdgeContractConsumer, EdgeOwnedBy, EdgeColocatedWith:
	default:
		return fmt.Errorf("%w: unknown graph edge type %q", ErrInvalidEvidence, e.EdgeType)
	}
	if e.FromNodeID == e.ToNodeID && e.EdgeType == EdgeDependsOn {
		return fmt.Errorf("%w: self dependency edge", ErrInvalidEvidence)
	}
	return nil
}

func ValidateCoverageObservation(c CoverageObservation) error {
	if c.TestUnitKey == "" || (c.CoveredStableKey == "" && c.CoveredPath == "") || c.SourceType == "" || c.ReportHash == "" || c.RunID == (UUID{}) || c.EnvironmentFingerprint == "" || c.ObservedAt.IsZero() {
		return fmt.Errorf("%w: coverage mapping is missing identity, report hash, or environment fingerprint", ErrInvalidEvidence)
	}
	if c.SuiteLevel && c.TestSpecific {
		return fmt.Errorf("%w: suite-level coverage cannot be labeled test-specific", ErrInvalidEvidence)
	}
	if c.Precision == CoverageLine && (c.LineStart < 1 || c.LineEnd < c.LineStart) {
		return fmt.Errorf("%w: line coverage has invalid line interval", ErrInvalidEvidence)
	}
	if math.IsNaN(c.EvidenceConfidence) || math.IsInf(c.EvidenceConfidence, 0) || c.EvidenceConfidence < 0 || c.EvidenceConfidence > 1 {
		return fmt.Errorf("%w: coverage confidence must be in [0,1]", ErrInvalidEvidence)
	}
	switch c.Precision {
	case CoverageLine, CoverageSymbol, CoverageFile, CoverageTarget, CoverageModule:
	default:
		return fmt.Errorf("%w: unknown coverage precision %q", ErrInvalidEvidence, c.Precision)
	}
	if c.CoveredPath != "" {
		if _, err := NormalizeRepoPath(c.CoveredPath); err != nil {
			return fmt.Errorf("coverage path: %w", err)
		}
	}
	return nil
}

func ValidateTestUnit(unit TestUnit, snapshotID UUID) error {
	if unit.ID == (UUID{}) || unit.SnapshotID != snapshotID || unit.StableKey == "" || unit.Adapter == "" {
		return fmt.Errorf("%w: test unit has missing identity, snapshot, stable key, or adapter", ErrInvalidEvidence)
	}
	switch unit.UnitType {
	case UnitTestCase, UnitTestMethod, UnitTestFile, UnitTestClass, UnitBuildTarget, UnitProjectSuite, UnitCheck:
	default:
		return fmt.Errorf("%w: unknown test unit type %q", ErrInvalidEvidence, unit.UnitType)
	}
	if unit.EstimatedMillis < 0 || unit.DurationP50Millis < 0 || unit.DurationP95Millis < 0 || unit.DurationSampleCount < 0 {
		return fmt.Errorf("%w: test unit duration data cannot be negative", ErrInvalidEvidence)
	}
	if math.IsNaN(unit.FlakeRate) || math.IsInf(unit.FlakeRate, 0) || unit.FlakeRate < 0 || unit.FlakeRate > 1 {
		return fmt.Errorf("%w: test unit flake rate must be in [0,1]", ErrInvalidEvidence)
	}
	if unit.Selectable && unit.SelectionGranularity == "" {
		return fmt.Errorf("%w: selectable test unit requires declared selection granularity", ErrInvalidEvidence)
	}
	for _, sourcePath := range unit.SourcePaths {
		if _, err := NormalizeRepoPath(sourcePath); err != nil {
			return fmt.Errorf("test unit source path: %w", err)
		}
	}
	return nil
}
