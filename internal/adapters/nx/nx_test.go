package nx

import (
	"reflect"
	"testing"
)

func TestNxProjectListFixturesAreSortedAndDeduplicated(t *testing.T) {
	fixtures := [][]byte{
		[]byte(`["web","api","web"]`),
		[]byte(`{"projects":["web","api","web"]}`),
	}
	for _, fixture := range fixtures {
		got, err := parseProjectNames(fixture)
		if err != nil {
			t.Fatalf("parse Nx project fixture: %v", err)
		}
		if want := []string{"api", "web"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Nx project names = %v, want %v", got, want)
		}
	}
}

func TestNxProjectListRejectsMalformedFixture(t *testing.T) {
	if _, err := parseProjectNames([]byte(`{"projects":`)); err == nil {
		t.Fatal("malformed Nx project output was accepted")
	}
}

func TestNxProjectTargetFixtureIncludesJestConfigurationAndReportOptions(t *testing.T) {
	project, err := parseProject([]byte(`{"name":"web","root":"apps/web","sourceRoot":"apps/web/src","targets":{"test":{"executor":"@nx/jest:jest","options":{"outputFile":"reports/jest.xml"},"configurations":{"ci":{},"development":{}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	target, ok := project.Targets["test"]
	if !ok || target.Executor != "@nx/jest:jest" || reportFromOptions(target.Options) != "reports/jest.xml" {
		t.Fatalf("Nx target fixture was not parsed: %+v", project)
	}
	if got, want := len(target.Configurations), 2; got != want {
		t.Fatalf("Nx target configurations = %d, want %d", got, want)
	}
}
