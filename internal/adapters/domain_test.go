package adapters

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func TestDomainUnitPreservesAdapterMetadata(t *testing.T) {
	snapshot := uuid.New()
	u := TestUnit{Key: "wallet-unit", Kind: "CHECK", Runner: "jest", Paths: []string{"services/wallet/**"}, Dependencies: []string{"db-fixture"}, Mandatory: true, SelectableGranularity: "DECLARED_UNIT", Filterable: true, Timeout: 2 * time.Minute, EstimatedDuration: 45 * time.Second, ResourceGroup: "wallet-db", ReportPaths: []string{".ivp/result.xml"}, CoveragePaths: []string{"coverage/lcov.info"}, TrustedConfig: true}
	domainUnit := DomainUnit("generic", snapshot, u)
	if domainUnit.SnapshotID != snapshot || domainUnit.UnitType != domain.UnitCheck || domainUnit.EstimatedMillis != 45000 || len(domainUnit.SourcePaths) != 1 {
		t.Fatalf("domain mapping lost unit fields: %+v", domainUnit)
	}
	restored := FromDomainUnit(domainUnit)
	if restored.Runner != "jest" || restored.Timeout != 2*time.Minute || len(restored.ReportPaths) != 1 || len(restored.CoveragePaths) != 1 {
		t.Fatalf("adapter metadata did not round-trip: %+v", restored)
	}
}
