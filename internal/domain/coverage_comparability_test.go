package domain

import "testing"

func TestCoverageDependencyStaleMarksPreviousDependencyVersion(t *testing.T) {
	snapshot := RepositorySnapshot{DependencyDigest: "sha256:dep-current"}
	previous := CoverageObservation{DependencyDigest: "sha256:dep-previous"}
	if !CoverageDependencyStale(previous, snapshot) {
		t.Fatal("coverage from a previous dependency version must be stale")
	}
	current := CoverageObservation{DependencyDigest: "sha256:dep-current"}
	if CoverageDependencyStale(current, snapshot) {
		t.Fatal("coverage from the current dependency version must stay comparable")
	}
	unstamped := CoverageObservation{}
	if !CoverageDependencyStale(unstamped, snapshot) {
		t.Fatal("coverage without a dependency version cannot be shown to match the snapshot")
	}
	withoutDimension := RepositorySnapshot{}
	if CoverageDependencyStale(previous, withoutDimension) {
		t.Fatal("a snapshot without a dependency dimension has no dependency change to detect")
	}
}

func TestCoverageAdapterStaleOnlyFiresOnDetectedIncompatibleChanges(t *testing.T) {
	same := CoverageObservation{AdapterVersion: "pytest-8.2"}
	if stale, _ := CoverageAdapterStale(same, "pytest-8.2"); stale {
		t.Fatal("an unchanged adapter version must stay comparable")
	}
	patch := CoverageObservation{AdapterVersion: "pytest-8.2"}
	if stale, reason := CoverageAdapterStale(patch, "pytest-8.2.1"); stale {
		t.Fatalf("a compatible patch release must stay comparable: %s", reason)
	}
	major := CoverageObservation{AdapterVersion: "pytest-8.2"}
	stale, reason := CoverageAdapterStale(major, "pytest-9.0")
	if !stale || reason == "" {
		t.Fatalf("an incompatible adapter version change must mark the coverage stale: %v %q", stale, reason)
	}
	unknown := CoverageObservation{}
	if stale, _ := CoverageAdapterStale(unknown, "pytest-9.0"); stale {
		t.Fatal("coverage without a recorded adapter version must not be reported as a detected change")
	}
}
