package analysis

import (
	"testing"

	"github.com/ivp/ivp/internal/domain"
)

func TestClassifyRiskLabelsUsesWholeTokenPathVocabulary(t *testing.T) {
	policy := domain.Policy{
		CriticalPathPatterns:      []string{"billing/**"},
		DocumentationPathPatterns: []string{"**/*.md", "docs/**"},
	}
	changes := []domain.ChangedFile{
		{Path: "internal/auth/middleware.go"},
		{Path: "internal/author/parser.go"}, // "author" must not classify as authentication
		{Path: "pkg/cryptox/hash.go"},       // "cryptox" must not classify as cryptography
		{Path: "internal/session/store.go"},
		{Path: "proto/checkout/v1/checkout.proto"},
		{Path: "db/migrate/0042_add_index.sql"},
		{Path: "infra/deploy/k8s.yaml"},
		{Path: "secrets/rotation.go"},
		{Path: "billing/invoice.go"}, // critical path pattern
		{Path: "docs/auth.md"},       // documentation carries no path-derived label
		{Path: "README.md"},
	}
	out := ClassifyRiskLabels(changes, policy)
	byPath := map[string][]string{}
	for _, f := range out {
		byPath[f.Path] = f.DetectedRiskLabels
	}
	want := map[string][]string{
		"internal/auth/middleware.go":      {"AUTHENTICATION", "AUTHORIZATION"},
		"internal/author/parser.go":        nil,
		"pkg/cryptox/hash.go":              nil,
		"internal/session/store.go":        {"AUTHENTICATION"},
		"proto/checkout/v1/checkout.proto": {"PUBLIC_CONTRACT"},
		"db/migrate/0042_add_index.sql":    {"DATA_MIGRATION"},
		"infra/deploy/k8s.yaml":            {"DEPLOYMENT"},
		"secrets/rotation.go":              {"SECURITY_CRITICAL"},
		"billing/invoice.go":               {"CRITICAL_PATH", "PAYMENT"},
		"docs/auth.md":                     nil,
		"README.md":                        nil,
	}
	for path, wantLabels := range want {
		got := byPath[path]
		if len(got) != len(wantLabels) {
			t.Errorf("%s labels = %v, want %v", path, got, wantLabels)
			continue
		}
		for i := range wantLabels {
			if got[i] != wantLabels[i] {
				t.Errorf("%s labels = %v, want %v (canonical order)", path, got, wantLabels)
				break
			}
		}
	}
}

func TestClassifyRiskLabelsRetainsPreexistingAndExtraLabels(t *testing.T) {
	changes := []domain.ChangedFile{
		{Path: "pkg/util.go", DetectedRiskLabels: []string{"TENANT_BOUNDARY", " auth ", "AUTHENTICATION"}},
	}
	out := ClassifyRiskLabels(changes, domain.Policy{})
	// Canonical labels first, then pre-existing non-canonical extras sorted;
	// everything trimmed and deduplicated.
	want := []string{"AUTHENTICATION", "AUTH", "TENANT_BOUNDARY"}
	got := out[0].DetectedRiskLabels
	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels = %v, want %v", got, want)
		}
	}
	// The input slice must not be mutated.
	if len(changes[0].DetectedRiskLabels) != 3 || changes[0].DetectedRiskLabels[1] != " auth " {
		t.Fatalf("input change mutated: %+v", changes[0].DetectedRiskLabels)
	}
}

func TestDetectBroadRefactorThresholdsAndDocumentationExemption(t *testing.T) {
	policy := domain.Policy{DocumentationPathPatterns: []string{"**/*.md", "docs/**"}}
	below := make([]domain.ChangedFile, 0, broadRefactorMinFiles-1)
	for i := 0; i < broadRefactorMinFiles-1; i++ {
		below = append(below, domain.ChangedFile{Path: "pkg/dir" + string(rune('a'+i%26)) + "/f" + string(rune('a'+i/26)) + ".go"})
	}
	if DetectBroadRefactor(below, policy) {
		t.Error("change below the file threshold classified as broad refactor")
	}
	// 25 files but only one directory: not broad.
	oneDir := make([]domain.ChangedFile, 0, broadRefactorMinFiles)
	for i := 0; i < broadRefactorMinFiles; i++ {
		oneDir = append(oneDir, domain.ChangedFile{Path: "pkg/single/f" + string(rune('a'+i)) + ".go"})
	}
	if DetectBroadRefactor(oneDir, policy) {
		t.Error("single-directory change classified as broad refactor")
	}
	// Thresholds: >=25 files across >=10 dirs.
	meets := make([]domain.ChangedFile, 0, broadRefactorMinFiles)
	for i := 0; i < broadRefactorMinFiles; i++ {
		meets = append(meets, domain.ChangedFile{Path: "pkg/dir" + string(rune('a'+i)) + "/f.go"})
	}
	if !DetectBroadRefactor(meets, policy) {
		t.Errorf("threshold change (25 files, %d dirs) not classified as broad refactor", broadRefactorMinDirs)
	}
	// Documentation-only change sets are exempt even at threshold size.
	docs := make([]domain.ChangedFile, 0, broadRefactorMinFiles)
	for i := 0; i < broadRefactorMinFiles; i++ {
		docs = append(docs, domain.ChangedFile{Path: "docs/topic" + string(rune('a'+i)) + ".md"})
	}
	if DetectBroadRefactor(docs, policy) {
		t.Error("documentation-only change set classified as broad refactor")
	}
}

func TestIsIntegrationBoundaryUnitUsesLabelsAndKeysOnly(t *testing.T) {
	for _, unit := range []domain.TestUnit{
		{StableKey: "gradle:integrationTest", RiskLabels: []string{"integration"}},
		{StableKey: "pytest:contract", RiskLabels: []string{"E2E_SMOKE"}},
		{StableKey: "e2e:checkout-flow"},
		{StableKey: "jest:app", ModuleKey: "endtoend-suite"},
		{StableKey: "jest:app", TargetRef: "integration-tests"},
	} {
		if !IsIntegrationBoundaryUnit(unit) {
			t.Errorf("unit %+v not detected as integration boundary", unit)
		}
	}
	for _, unit := range []domain.TestUnit{
		{StableKey: "pytest:unit", RiskLabels: []string{"UNIT"}, SourcePaths: []string{"tests/integration_helpers.py"}},
		{StableKey: "gradle:test"},
	} {
		if IsIntegrationBoundaryUnit(unit) {
			t.Errorf("unit %+v falsely detected as integration boundary (source paths must not count)", unit)
		}
	}
}

func TestReviewedExclusionKeysIgnoresBlankKeys(t *testing.T) {
	policy := domain.Policy{ReviewedExclusions: []domain.ReviewedExclusion{
		{UnitKey: "unit:a", Reviewer: "r", ReviewedAt: "2026-01-01", Rationale: "x"},
		{UnitKey: "  ", Reviewer: "r", ReviewedAt: "2026-01-01", Rationale: "x"},
	}}
	keys := ReviewedExclusionKeys(policy)
	if len(keys) != 1 || !keys["unit:a"] {
		t.Fatalf("ReviewedExclusionKeys = %v, want exactly [unit:a]", keys)
	}
	if ReviewedExclusionKeys(domain.Policy{}) != nil {
		t.Fatal("empty policy must yield nil exclusion keys")
	}
}
