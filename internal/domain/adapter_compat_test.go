package domain

import "testing"

func TestAdapterVersionCompatibleRules(t *testing.T) {
	cases := []struct {
		name       string
		recorded   string
		current    string
		comparable bool
	}{
		{name: "identical versions stay comparable", recorded: "pytest-8.2", current: "pytest-8.2", comparable: true},
		{name: "patch release keeps the evidence contract", recorded: "pytest-8.2", current: "pytest-8.2.1", comparable: true},
		{name: "minor release is not comparable", recorded: "pytest-8.2", current: "pytest-8.3", comparable: false},
		{name: "major release is not comparable", recorded: "pytest-8.2", current: "pytest-9.0", comparable: false},
		{name: "different evidence contract is not comparable", recorded: "pytest-8.2", current: "junit-8.2", comparable: false},
		{name: "unparsable version is not comparable", recorded: "pytest-8.2", current: "pytest-next", comparable: false},
	}
	for _, testCase := range cases {
		comparable, reason := AdapterVersionCompatible(testCase.recorded, testCase.current)
		if comparable != testCase.comparable {
			t.Fatalf("%s: comparable=%v reason=%q", testCase.name, comparable, reason)
		}
		if reason == "" {
			t.Fatalf("%s: the compatibility check must explain its outcome", testCase.name)
		}
	}
}

func TestAdapterVersionChangedDetectsOnlyKnownChanges(t *testing.T) {
	unchanged := AdapterVersionChanged("pytest", "pytest-8.2", "pytest-8.2")
	if unchanged.Detected || !unchanged.Comparable {
		t.Fatalf("an unchanged version must not be reported as a change: %+v", unchanged)
	}
	unknown := AdapterVersionChanged("pytest", "", "pytest-8.2")
	if unknown.Detected {
		t.Fatalf("an unknown recorded version must not be reported as a detected change: %+v", unknown)
	}
	changed := AdapterVersionChanged("pytest", "pytest-8.2", "pytest-9.0")
	if !changed.Detected || changed.Comparable {
		t.Fatalf("a major adapter version change must be detected and not comparable: %+v", changed)
	}
	if changed.Adapter != "pytest" || changed.Recorded != "pytest-8.2" || changed.Current != "pytest-9.0" {
		t.Fatalf("the change must keep its provenance: %+v", changed)
	}
}
