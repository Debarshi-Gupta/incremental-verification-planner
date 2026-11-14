package domain

import (
	"strconv"
	"strings"
)

// AdapterVersionChange describes one adapter whose version differs between the
// version that produced adapter-specific evidence and the version that is in
// effect now (LLD 18.4: "Adapter version changes invalidate adapter-specific
// evidence unless a compatibility check proves it remains comparable").
type AdapterVersionChange struct {
	// Adapter is the adapter identity (for example "pytest").
	Adapter string
	// Recorded is the version that produced the evidence.
	Recorded string
	// Current is the version observed now.
	Current string
	// Detected reports whether a version change was observed at all. It is
	// false when either side is unknown: IVP cannot detect a change it was
	// never told about.
	Detected bool
	// Comparable reports whether the compatibility check proved that the
	// evidence produced by Recorded stays comparable with Current.
	Comparable bool
	// Reason explains the outcome of the compatibility check.
	Reason string
}

// AdapterVersionCompatible applies the compatibility check for a changed
// adapter version.
//
// The check is deliberately narrow and version-shape based:
//
//   - identical versions are comparable;
//   - a version that keeps the same evidence contract (the "<contract>-"
//     prefix) and the same major and minor components stays comparable, so a
//     patch-level release cannot silently invalidate a repository's evidence;
//   - every other change (different contract, major bump, minor bump, or an
//     unparsable version) is NOT comparable and therefore invalidates the
//     adapter-specific evidence.
//
// An empty version on either side means the version is unknown. IVP cannot
// detect a change it was never told about, so an unknown version yields
// "not comparable" without a detected change; callers distinguish the two
// cases with AdapterVersionChange.Detected.
func AdapterVersionCompatible(recorded, current string) (bool, string) {
	recorded = strings.TrimSpace(recorded)
	current = strings.TrimSpace(current)
	if recorded == current {
		return true, "the adapter version is unchanged"
	}
	if recorded == "" || current == "" {
		return false, "the adapter version that produced the evidence is unknown"
	}
	recordedContract, recordedVersion, recordedOK := parseAdapterVersion(recorded)
	currentContract, currentVersion, currentOK := parseAdapterVersion(current)
	if !recordedOK || !currentOK {
		return false, "the adapter version format is not comparable"
	}
	if recordedContract != currentContract {
		return false, "the adapter evidence contract changed"
	}
	if recordedVersion[0] != currentVersion[0] {
		return false, "the adapter major version changed"
	}
	if recordedVersion[1] != currentVersion[1] {
		return false, "the adapter minor version changed"
	}
	return true, "the adapter patch release keeps the same evidence contract, major, and minor version"
}

// AdapterVersionChanged evaluates the compatibility check for one adapter and
// returns the structured outcome. The change is reported as detected only when
// both versions are known and differ.
func AdapterVersionChanged(adapter, recorded, current string) AdapterVersionChange {
	change := AdapterVersionChange{Adapter: adapter, Recorded: recorded, Current: current}
	if strings.TrimSpace(recorded) == "" || strings.TrimSpace(current) == "" {
		change.Reason = "the adapter version is unknown, so no version change was detected"
		return change
	}
	if recorded == current {
		change.Comparable = true
		change.Reason = "the adapter version is unchanged"
		return change
	}
	change.Detected = true
	change.Comparable, change.Reason = AdapterVersionCompatible(recorded, current)
	return change
}

// parseAdapterVersion splits an adapter version into its evidence contract
// identifier and its numeric components. The contract is the leading
// non-numeric component ("pytest-8.2" -> "pytest"); versions without a
// contract use an empty contract.
func parseAdapterVersion(raw string) (string, [3]int, bool) {
	contract := ""
	remainder := raw
	if index := strings.IndexAny(raw, "-_ "); index > 0 {
		candidate := raw[:index]
		if _, err := strconv.Atoi(candidate); err != nil {
			contract = candidate
			remainder = raw[index+1:]
		}
	}
	var components [3]int
	parts := strings.Split(remainder, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return contract, components, false
	}
	for i, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 0 {
			return contract, components, false
		}
		components[i] = value
	}
	return contract, components, true
}
