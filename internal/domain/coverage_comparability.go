package domain

// CoverageDependencyStale reports whether a coverage observation was produced
// under a different dependency version than the snapshot it is evaluated
// against (LLD 23 scenario 6: "The coverage report belongs to a previous
// dependency version; planner must mark it stale").
//
// A snapshot without a recorded dependency digest has no dependency dimension,
// so no dependency change was detected and the observation keeps its existing
// comparability rules. As soon as the snapshot records a dependency digest, a
// report without a matching digest cannot be shown to belong to the current
// dependency version and is therefore stale.
func CoverageDependencyStale(observation CoverageObservation, snapshot RepositorySnapshot) bool {
	if snapshot.DependencyDigest == "" {
		return false
	}
	return observation.DependencyDigest != snapshot.DependencyDigest
}

// CoverageAdapterStale reports whether a coverage observation was produced by
// an adapter version that is no longer comparable with the version in effect
// now (LLD 18.4). The recorded version is the one that produced the report,
// the current version is the one reported by the client that requests the
// plan. An unknown version on either side means no change was detected, so the
// observation is not marked stale.
func CoverageAdapterStale(observation CoverageObservation, currentVersion string) (bool, string) {
	change := AdapterVersionChanged("", observation.AdapterVersion, currentVersion)
	if !change.Detected {
		return false, change.Reason
	}
	return !change.Comparable, change.Reason
}
