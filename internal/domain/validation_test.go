package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeRepoPathRequiresCanonicalRepositoryRelativePaths(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"src/pkg/file.go", true},
		{"docs/README.md", true},
		{"", false},
		{"../outside", false},
		{"src/../outside", false},
		{"/etc/passwd", false},
		{`src\\outside.go`, false},
		{"src//file.go", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeRepoPath(tc.input)
			if tc.want && err != nil {
				t.Fatalf("NormalizeRepoPath(%q): %v", tc.input, err)
			}
			if !tc.want && err == nil {
				t.Fatalf("NormalizeRepoPath(%q) accepted unsafe/noncanonical path %q", tc.input, got)
			}
		})
	}
}

func TestValidateCoverageDoesNotRelabelSuiteCoverageAsPerTest(t *testing.T) {
	c := CoverageObservation{TestUnitKey: "suite", CoveredPath: "src/a.go", SourceType: "coverage.py", ReportHash: "sha256:report", RunID: uuid.New(),
		ObservedAt: time.Now().UTC(), EnvironmentFingerprint: "env-1", Precision: CoverageFile, SuiteLevel: true, TestSpecific: true, EvidenceConfidence: 1}
	if err := ValidateCoverageObservation(c); err == nil {
		t.Fatal("suite-level coverage was accepted as test-specific")
	}
}

func TestSnapshotDigestIsStableAcrossUnorderedReasons(t *testing.T) {
	s := RepositorySnapshot{ID: uuid.New(), RepositoryID: uuid.New(), CommitSHA: "head", BaseSHA: "base", TreeHash: "tree", WorktreeHash: "worktree", GraphVersion: 3,
		EvidenceGeneratedAt: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), IsComplete: false, CompletenessReasons: []string{"missing coverage", "graph partial"}}
	a, err := SnapshotDigest(s)
	if err != nil {
		t.Fatal(err)
	}
	s.CompletenessReasons[0], s.CompletenessReasons[1] = s.CompletenessReasons[1], s.CompletenessReasons[0]
	b, err := SnapshotDigest(s)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("snapshot digest changed when reason order changed: %s != %s", a, b)
	}
}
