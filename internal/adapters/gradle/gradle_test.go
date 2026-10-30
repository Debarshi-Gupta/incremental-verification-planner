package gradle

import (
	"reflect"
	"testing"
)

func TestGradleDiscoveryParsersUseProjectAndTaskFixtures(t *testing.T) {
	projectFixture := []byte(`Root project 'sample'
+--- Project ':api'
+--- Project ':service:worker'
+--- Project ':api'
`)
	if got, want := parseProjectNames(projectFixture), []string{":api", ":service:worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Gradle projects = %v, want %v", got, want)
	}

	taskFixture := []byte(`Verification tasks
-------------------
 test - Runs the test suite
 integrationTest - Runs integration tests
 check - Runs verification tasks
 compileTestJava - Compiles test sources
 test - Duplicate task output
`)
	if got, want := parseTestTaskNames(taskFixture), []string{"test", "integrationTest", "compileTestJava"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Gradle test tasks = %v, want %v", got, want)
	}
}
