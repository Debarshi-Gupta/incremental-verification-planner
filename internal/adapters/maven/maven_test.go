package maven

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

const rootPOMFixture = `<project>
  <modelVersion>4.0.0</modelVersion>
  <artifactId>sample</artifactId>
  <modules><module>service</module></modules>
  <build><pluginManagement><plugins><plugin><artifactId>maven-failsafe-plugin</artifactId></plugin></plugins></pluginManagement></build>
</project>`

const modulePOMFixture = `<project>
  <modelVersion>4.0.0</modelVersion>
  <artifactId>service</artifactId>
</project>`

func TestMavenDiscoveryUsesReactorAndFailsafeFixtures(t *testing.T) {
	root := t.TempDir()
	writePOMFixture(t, root, "pom.xml", rootPOMFixture)
	writePOMFixture(t, root, filepath.Join("service", "pom.xml"), modulePOMFixture)

	got, err := New().Discover(context.Background(), adapters.DiscoverRequest{Root: root, Config: adapters.AdapterConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || len(got.Units) != 3 {
		t.Fatalf("Maven discovery complete=%v units=%+v reasons=%v", got.Complete, got.Units, got.Reasons)
	}
	keys := map[string]bool{}
	for _, unit := range got.Units {
		keys[unit.Key] = true
	}
	for _, key := range []string{"#test", "#verify", "service#test"} {
		if !keys[key] {
			t.Errorf("Maven fixture did not produce target %q: %v", key, keys)
		}
	}
}

func TestMavenDiscoveryMarksEscapingReactorModuleIncomplete(t *testing.T) {
	root := t.TempDir()
	pom := `<project><artifactId>sample</artifactId><modules><module>../outside</module></modules></project>`
	writePOMFixture(t, root, "pom.xml", pom)

	got, err := New().Discover(context.Background(), adapters.DiscoverRequest{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.Complete {
		t.Fatalf("Maven escape fixture was incorrectly complete: %+v", got)
	}
	for _, unit := range got.Units {
		if unit.Module == "../outside" || unit.Key == "../outside#test" {
			t.Fatalf("Maven escape fixture created an outside target: %+v", unit)
		}
	}
}

func writePOMFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
