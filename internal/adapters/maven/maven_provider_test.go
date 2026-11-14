package maven

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// mavenProviderBool returns a pointer to a copy of value so a test can express
// an explicit provider toggle in the adapter configuration view.
func mavenProviderBool(value bool) *bool {
	return &value
}

// mavenProviderFixture writes the shared two-module reactor fixture. The root
// POM advertises maven-failsafe-plugin in pluginManagement, so the root module
// owns a #verify unit and the service module does not.
func mavenProviderFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writePOMFixture(t, root, "pom.xml", rootPOMFixture)
	writePOMFixture(t, root, filepath.Join("service", "pom.xml"), modulePOMFixture)
	return root
}

func mavenUnitKeys(units []adapters.TestUnit) []string {
	keys := make([]string, 0, len(units))
	for _, unit := range units {
		keys = append(keys, unit.Key)
	}
	sort.Strings(keys)
	return keys
}

func mavenHasReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, want) {
			return true
		}
	}
	return false
}

// TestMavenDiscoveryOmitsSurefireUnitsWhenDisabled proves the
// adapters.maven.surefire_enabled key is enforced: no surefire-backed #test
// unit may be declared, and the adapter must say why.
func TestMavenDiscoveryOmitsSurefireUnitsWhenDisabled(t *testing.T) {
	root := mavenProviderFixture(t)

	got, err := New().Discover(context.Background(), adapters.DiscoverRequest{
		Root:   root,
		Config: adapters.AdapterConfig{Enabled: true, SurefireEnabled: mavenProviderBool(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := mavenUnitKeys(got.Units)
	for _, key := range keys {
		if strings.HasSuffix(key, "#test") {
			t.Fatalf("surefire is disabled but a surefire unit was declared: %v", keys)
		}
	}
	if len(keys) != 1 || keys[0] != "#verify" {
		t.Fatalf("disabled surefire must keep only the failsafe unit, got %v", keys)
	}
	if !mavenHasReason(got.Reasons, surefireDisabledReason) {
		t.Fatalf("discovery reasons %v must report the disabled surefire provider", got.Reasons)
	}
	if mavenHasReason(got.Reasons, failsafeDisabledReason) {
		t.Fatalf("discovery reasons %v must not report a disabled failsafe provider", got.Reasons)
	}
}

// TestMavenDiscoveryOmitsFailsafeUnitsWhenDisabled proves the
// adapters.maven.failsafe_enabled key is enforced: no failsafe-backed #verify
// unit may be declared, even where the Failsafe plugin is visible.
func TestMavenDiscoveryOmitsFailsafeUnitsWhenDisabled(t *testing.T) {
	root := mavenProviderFixture(t)

	got, err := New().Discover(context.Background(), adapters.DiscoverRequest{
		Root:   root,
		Config: adapters.AdapterConfig{Enabled: true, FailsafeEnabled: mavenProviderBool(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := mavenUnitKeys(got.Units)
	for _, key := range keys {
		if strings.HasSuffix(key, "#verify") {
			t.Fatalf("failsafe is disabled but a failsafe unit was declared: %v", keys)
		}
	}
	if len(keys) != 2 || keys[0] != "#test" || keys[1] != "service#test" {
		t.Fatalf("disabled failsafe must keep the surefire units, got %v", keys)
	}
	if !mavenHasReason(got.Reasons, failsafeDisabledReason) {
		t.Fatalf("discovery reasons %v must report the disabled failsafe provider", got.Reasons)
	}
	if mavenHasReason(got.Reasons, surefireDisabledReason) {
		t.Fatalf("discovery reasons %v must not report a disabled surefire provider", got.Reasons)
	}
}

// TestMavenDiscoveryKeepsUnitsWhenProviderKeysUnset pins ADR 0007's default
// behavior for an unset configuration: every module keeps its #test unit and
// #verify stays restricted to modules with a visible Failsafe plugin.
func TestMavenDiscoveryKeepsUnitsWhenProviderKeysUnset(t *testing.T) {
	root := mavenProviderFixture(t)

	got, err := New().Discover(context.Background(), adapters.DiscoverRequest{
		Root:   root,
		Config: adapters.AdapterConfig{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := mavenUnitKeys(got.Units)
	if len(keys) != 3 || keys[0] != "#test" || keys[1] != "#verify" || keys[2] != "service#test" {
		t.Fatalf("unset provider keys changed the unit set: %v", keys)
	}
	if mavenHasReason(got.Reasons, surefireDisabledReason) || mavenHasReason(got.Reasons, failsafeDisabledReason) {
		t.Fatalf("unset provider keys must not report a disabled provider: %v", got.Reasons)
	}
}

// TestMavenDiscoveryKeepsVerifyUnitOnlyWithVisibleFailsafe proves an enabled
// Failsafe provider still declares #verify only where the plugin is visible,
// and that the visible-plugin rule is independent of the configuration toggle.
func TestMavenDiscoveryKeepsVerifyUnitOnlyWithVisibleFailsafe(t *testing.T) {
	root := t.TempDir()
	writePOMFixture(t, root, "pom.xml", `<project><artifactId>sample</artifactId><modules><module>service</module></modules></project>`)
	writePOMFixture(t, root, filepath.Join("service", "pom.xml"), modulePOMFixture)

	got, err := New().Discover(context.Background(), adapters.DiscoverRequest{
		Root: root,
		Config: adapters.AdapterConfig{
			Enabled:         true,
			SurefireEnabled: mavenProviderBool(true),
			FailsafeEnabled: mavenProviderBool(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := mavenUnitKeys(got.Units)
	if len(keys) != 2 || keys[0] != "#test" || keys[1] != "service#test" {
		t.Fatalf("an enabled failsafe provider must still require a visible plugin: %v", keys)
	}
	if mavenHasReason(got.Reasons, failsafeDisabledReason) {
		t.Fatalf("discovery reasons %v must not report a disabled failsafe provider", got.Reasons)
	}
}

// TestMavenMaterializeRefusesDisabledProviderGoals proves a disabled provider
// cannot be materialized even when a stale snapshot still lists its target.
func TestMavenMaterializeRefusesDisabledProviderGoals(t *testing.T) {
	ctx := context.Background()
	a := New()

	_, err := a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         mavenProviderFixture(t),
		Config:       adapters.AdapterConfig{Enabled: true, SurefireEnabled: mavenProviderBool(false)},
		TargetKeys:   []string{"#test"},
		KnownTargets: []string{"#test"},
	})
	if err == nil || !strings.Contains(err.Error(), "Surefire is disabled by configuration") {
		t.Fatalf("materializing a surefire goal while surefire is disabled must fail, got %v", err)
	}

	_, err = a.Materialize(ctx, adapters.MaterializeRequest{
		Root:         mavenProviderFixture(t),
		Config:       adapters.AdapterConfig{Enabled: true, FailsafeEnabled: mavenProviderBool(false)},
		TargetKeys:   []string{"#verify"},
		KnownTargets: []string{"#verify"},
	})
	if err == nil || !strings.Contains(err.Error(), "Failsafe is disabled by configuration") {
		t.Fatalf("materializing a failsafe goal while failsafe is disabled must fail, got %v", err)
	}
}
