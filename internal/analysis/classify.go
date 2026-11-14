package analysis

import (
	"path"
	"sort"
	"strings"

	"github.com/ivp/ivp/internal/domain"
)

// Change classification (LLD Stage 2: riskLabels = classifyCriticalPaths(
// changes, effectivePolicy), line 1385) plus the broad-refactor and
// integration-boundary predicates. Classification reads only changed paths and
// reviewed policy configuration; it never infers risk from file content.
// Path vocabulary, token tables, and thresholds are fixed by ADR 0013.

// riskLabelOrder is the canonical emission order for detected risk labels so
// identical change sets always produce byte-identical labels.
var riskLabelOrder = []string{
	"AUTHENTICATION",
	"AUTHORIZATION",
	"CRITICAL_PATH",
	"CRYPTOGRAPHY",
	"DATA_MIGRATION",
	"DEPLOYMENT",
	"PAYMENT",
	"PUBLIC_CONTRACT",
	"RELEASE",
	"SECURITY_CRITICAL",
}

// riskLabelTokens maps a canonical risk label to its whole-token path
// vocabulary. Matching is whole-token (path split on non-alphanumeric bytes),
// so "author" never classifies as authentication while "auth" does. "auth" is
// listed under both authentication and authorization because it is ambiguous
// and both are hard full-suite labels (ADR 0013).
var riskLabelTokens = map[string][]string{
	"AUTHENTICATION":    {"auth", "authn", "authentication", "login", "signin", "session"},
	"AUTHORIZATION":     {"auth", "authz", "authorization", "permission", "permissions", "rbac"},
	"CRYPTOGRAPHY":      {"crypto", "cryptography", "cipher", "tls", "ssl"},
	"DEPLOYMENT":        {"deploy", "deployment", "deployments"},
	"PAYMENT":           {"billing", "payment", "payments"},
	"RELEASE":           {"release", "releases"},
	"SECURITY_CRITICAL": {"secret", "secrets", "security"},
}

// Broad-refactor thresholds: a change set touching at least
// broadRefactorMinDirs distinct directories and broadRefactorMinFiles files
// is a refactor whose blast radius cannot be bounded by graph evidence
// (ADR 0013). Documentation-only change sets never classify as broad refactors.
const (
	broadRefactorMinDirs  = 10
	broadRefactorMinFiles = 25
)

// ClassifyRiskLabels returns a copy of the change set with DetectedRiskLabels
// populated for every changed file. Labels already present on the change are
// retained; classification only adds to them.
func ClassifyRiskLabels(changes []domain.ChangedFile, policy domain.Policy) []domain.ChangedFile {
	if len(changes) == 0 {
		return changes
	}
	out := append([]domain.ChangedFile(nil), changes...)
	for i := range out {
		out[i].DetectedRiskLabels = classifyRiskLabels(out[i], policy)
	}
	return out
}

func classifyRiskLabels(f domain.ChangedFile, policy domain.Policy) []string {
	paths := []string{f.Path}
	if f.OldPath != "" && f.OldPath != f.Path {
		paths = append(paths, f.OldPath)
	}
	set := make(map[string]bool, len(f.DetectedRiskLabels)+2)
	extras := make(map[string]bool, len(f.DetectedRiskLabels))
	for _, label := range f.DetectedRiskLabels {
		trimmed := strings.ToUpper(strings.TrimSpace(label))
		if trimmed == "" {
			continue
		}
		set[trimmed] = true
		if !isCanonicalRiskLabel(trimmed) {
			extras[trimmed] = true
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		// Documentation paths carry no path-derived risk label: docs cannot be
		// a contract, migration, or security-critical source artifact under the
		// shipped patterns, and a documentation edit must not force full suite.
		if matchAny(p, policy.DocumentationPathPatterns) {
			continue
		}
		if isContractPath(p) {
			set["PUBLIC_CONTRACT"] = true
		}
		if isMigrationPath(p) {
			set["DATA_MIGRATION"] = true
		}
		if matchAny(p, policy.CriticalPathPatterns) {
			set["CRITICAL_PATH"] = true
		}
		tokens := pathTokens(p)
		for _, label := range riskLabelOrder {
			for _, want := range riskLabelTokens[label] {
				if contains(tokens, want) {
					set[label] = true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for _, label := range riskLabelOrder {
		if set[label] {
			out = append(out, label)
		}
	}
	if len(extras) > 0 {
		trimmed := make([]string, 0, len(extras))
		for label := range extras {
			trimmed = append(trimmed, label)
		}
		sort.Strings(trimmed)
		out = append(out, trimmed...)
	}
	return out
}

func isCanonicalRiskLabel(label string) bool {
	for _, canonical := range riskLabelOrder {
		if canonical == label {
			return true
		}
	}
	return false
}

// pathTokens splits a repository path or label into lowercase alphanumeric
// tokens. Both the plain split and a camelCase-aware split contribute, so
// "authMiddleware" yields "auth" and "integrationTest" yields "integration"
// while "author" stays one token (ADR 0013).
func pathTokens(p string) []string {
	simple := splitAlnumTokens(strings.ToLower(p))
	camel := splitAlnumTokens(strings.ToLower(camelSeparated(p)))
	seen := make(map[string]bool, len(simple)+len(camel))
	out := make([]string, 0, len(simple)+len(camel))
	for _, tokens := range [][]string{simple, camel} {
		for _, token := range tokens {
			if token != "" && !seen[token] {
				seen[token] = true
				out = append(out, token)
			}
		}
	}
	return out
}

func splitAlnumTokens(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

// camelSeparated inserts spaces at camelCase boundaries: lower/digit to
// upper, and the final upper of an acronym run before a lowercase letter
// ("HTTPServer" -> "HTTP Server").
func camelSeparated(value string) string {
	runes := []rune(value)
	var b strings.Builder
	b.Grow(len(value) + 8)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9' || (prev >= 'A' && prev <= 'Z' && nextLower) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DetectBroadRefactor reports whether the change set is a broad refactor:
// at least broadRefactorMinFiles changed files spread over at least
// broadRefactorMinDirs distinct directories, excluding documentation-only
// change sets (ADR 0013). The result is ORed with the evidence flag supplied
// by the snapshot.
func DetectBroadRefactor(changes []domain.ChangedFile, policy domain.Policy) bool {
	if len(changes) < broadRefactorMinFiles {
		return false
	}
	if allDocumentation(changes, policy.DocumentationPathPatterns) {
		return false
	}
	dirs := make(map[string]bool, len(changes))
	for _, f := range changes {
		for _, p := range []string{f.Path, f.OldPath} {
			if p == "" {
				continue
			}
			dirs[path.Dir(p)] = true
		}
	}
	return len(dirs) >= broadRefactorMinDirs
}

// IsIntegrationBoundaryUnit reports whether a test unit is an integration or
// E2E boundary unit (LLD Stage 4: "Integration/E2E tests configured for
// affected services/modules"). Detection is label- or key-based only; a unit
// is never assumed to be an integration test from its source paths alone
// (ADR 0013).
func IsIntegrationBoundaryUnit(unit domain.TestUnit) bool {
	for _, label := range unit.RiskLabels {
		if hasIntegrationToken(label) {
			return true
		}
	}
	for _, value := range []string{unit.TargetRef, unit.ModuleKey, unit.ExecutionGroup, unit.StableKey} {
		if hasIntegrationToken(value) {
			return true
		}
	}
	return false
}

func hasIntegrationToken(value string) bool {
	for _, token := range pathTokens(value) {
		switch token {
		case "integration", "e2e", "endtoend":
			return true
		}
	}
	return false
}

// ReviewedExclusionKeys returns the set of unit keys excluded by a reviewed
// policy entry. Entries without a unit key are ignored defensively; the
// configuration layer rejects them before a policy is built.
func ReviewedExclusionKeys(policy domain.Policy) map[string]bool {
	if len(policy.ReviewedExclusions) == 0 {
		return nil
	}
	keys := make(map[string]bool, len(policy.ReviewedExclusions))
	for _, entry := range policy.ReviewedExclusions {
		if key := strings.TrimSpace(entry.UnitKey); key != "" {
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// hardPathExceptionFor returns the reviewed narrow exception that covers a
// changed file for a given hard rule (LLD 6.3 Stage 3 line 437: "unless an
// organization-specific reviewed policy explicitly defines a narrower, safe
// exception"; ADR 0019). An exception matches when its rule names the
// escalation being evaluated and its glob matches either side of the change,
// mirroring the HardFullSuitePaths gate. Only rule-3 path codes are passed
// here; label escalations are never evaluated for exceptions (ADR 0019 §3),
// and CONFIG_OR_TOOLCHAIN_CHANGE can never match because configuration
// validation rejects it as an exception rule (ADR 0019 §1).
func hardPathExceptionFor(policy domain.Policy, code domain.EscalationReasonCode, f domain.ChangedFile) (domain.HardPathException, bool) {
	for _, exception := range policy.HardPathExceptions {
		if exception.Path == "" || exception.Rule != string(code) {
			continue
		}
		if GlobMatch(exception.Path, f.Path) || (f.OldPath != "" && GlobMatch(exception.Path, f.OldPath)) {
			return exception, true
		}
	}
	return domain.HardPathException{}, false
}

// IsContractPath reports whether a repository path is a public API schema or
// contract description under the shipped patterns (LLD 4.3 risk labels).
func IsContractPath(p string) bool { return isContractPath(p) }

// GlobalImpactCategory is the Stage-2 classification of global-impact files
// (LLD 6.3 Stage 2: "Classify global-impact files: build definitions,
// lockfiles, compiler/toolchain versions, test runner configuration, CI
// workflows, shared configuration, schema contracts, code generation
// definitions, deployment manifests, database migrations, dependency
// injection registries, and unknown generated-file relationships"). The
// vocabulary is fixed; the empty category means the file carries no
// global-impact classification.
type GlobalImpactCategory string

const (
	CategoryBuildDefinition    GlobalImpactCategory = "BUILD_DEFINITION"
	CategoryLockfile           GlobalImpactCategory = "LOCKFILE"
	CategoryToolchainVersion   GlobalImpactCategory = "TOOLCHAIN_VERSION"
	CategoryTestRunnerConfig   GlobalImpactCategory = "TEST_RUNNER_CONFIG"
	CategoryCIWorkflow         GlobalImpactCategory = "CI_WORKFLOW"
	CategorySharedConfig       GlobalImpactCategory = "SHARED_CONFIG"
	CategorySchemaContract     GlobalImpactCategory = "SCHEMA_CONTRACT"
	CategoryCodeGeneration     GlobalImpactCategory = "CODE_GENERATION"
	CategoryDeploymentManifest GlobalImpactCategory = "DEPLOYMENT_MANIFEST"
	CategoryDatabaseMigration  GlobalImpactCategory = "DATABASE_MIGRATION"
	CategoryDIRegistry         GlobalImpactCategory = "DI_REGISTRY"
	CategoryGeneratedRelation  GlobalImpactCategory = "GENERATED_RELATIONSHIP"
)

// ClassifyGlobalImpact maps a repository path to its global-impact category.
// Matching is path-based and deterministic (specificity precedence: contracts,
// migrations and lockfiles first, because those predicates predate the
// classifier and pin the hard-rule escalation codes; then CI, test-runner,
// build, toolchain, codegen, deployment, DI, shared config). Documentation
// paths never classify (ADR 0013). The pattern set is deliberately
// conservative: an unclassifiable path returns "" rather than guessing.
func ClassifyGlobalImpact(p string, policy domain.Policy) GlobalImpactCategory {
	if p == "" {
		return ""
	}
	lower := strings.ToLower(p)
	base := path.Base(lower)
	if matchAny(p, policy.DocumentationPathPatterns) || strings.HasPrefix(lower, "docs/") || strings.HasSuffix(lower, ".md") {
		return ""
	}
	switch {
	case isContractPath(p):
		return CategorySchemaContract
	case isMigrationPath(p):
		return CategoryDatabaseMigration
	case isLockfilePath(p):
		return CategoryLockfile
	case isCIWorkflowPath(lower, base):
		return CategoryCIWorkflow
	case isTestRunnerConfigPath(base):
		return CategoryTestRunnerConfig
	case isBuildDefinitionPath(base):
		return CategoryBuildDefinition
	case isToolchainVersionPath(base):
		return CategoryToolchainVersion
	case isCodeGenerationPath(lower, base):
		return CategoryCodeGeneration
	case isDeploymentManifestPath(lower, base):
		return CategoryDeploymentManifest
	case isDIRegistryPath(lower, base):
		return CategoryDIRegistry
	case isSharedConfigPath(lower):
		return CategorySharedConfig
	}
	return ""
}

// ClassifyGlobalImpactFile classifies one changed file: the new path first,
// then the old path (renames must still surface the global-impact side), and
// finally the unknown generated-file relationship fallback — a generated file
// with no recognized global-impact shape is classified
// GENERATED_RELATIONSHIP, never silently ignored (LLD 6.3 Stage 2).
func ClassifyGlobalImpactFile(f domain.ChangedFile, policy domain.Policy) GlobalImpactCategory {
	if category := ClassifyGlobalImpact(f.Path, policy); category != "" {
		return category
	}
	if f.OldPath != "" {
		if category := ClassifyGlobalImpact(f.OldPath, policy); category != "" {
			return category
		}
	}
	if f.IsGenerated {
		return CategoryGeneratedRelation
	}
	return ""
}

// globalImpactForChange returns the most escalation-relevant category across
// both path sides of a change. The rank preserves the historical hard-rule
// switch precedence (lockfile, then migration, then contract), so renaming
// either side into a hard category keeps its specific escalation code.
func globalImpactForChange(f domain.ChangedFile, policy domain.Policy) GlobalImpactCategory {
	best, bestRank := GlobalImpactCategory(""), 1<<30
	for _, p := range []string{f.Path, f.OldPath} {
		category := ClassifyGlobalImpact(p, policy)
		if category == "" {
			continue
		}
		rank := 3
		switch category {
		case CategoryLockfile:
			rank = 0
		case CategoryDatabaseMigration:
			rank = 1
		case CategorySchemaContract:
			rank = 2
		}
		if rank < bestRank {
			best, bestRank = category, rank
		}
	}
	return best
}

func isCIWorkflowPath(lower, base string) bool {
	return strings.HasPrefix(lower, ".github/workflows/") || strings.HasPrefix(lower, ".circleci/") ||
		strings.HasPrefix(lower, ".buildkite/") || base == ".gitlab-ci.yml" || base == "jenkinsfile" ||
		strings.HasPrefix(base, "azure-pipelines") || base == "bitbucket-pipelines.yml"
}

func isTestRunnerConfigPath(base string) bool {
	return base == "pytest.ini" || base == "tox.ini" || strings.HasPrefix(base, "jest.config.") ||
		strings.HasPrefix(base, "vitest.config.") || base == "nx.json" || base == "project.json" ||
		strings.HasPrefix(base, ".mocharc") || strings.HasPrefix(base, "karma.conf.") ||
		strings.HasPrefix(base, "playwright.config.") || strings.HasPrefix(base, "cypress.config.")
}

func isBuildDefinitionPath(base string) bool {
	return base == "pom.xml" || strings.HasPrefix(base, "build.gradle") || strings.HasPrefix(base, "settings.gradle") ||
		base == "module.bazel" || strings.HasPrefix(base, "workspace") || base == "build" || base == "build.bazel" ||
		base == "package.json" || base == "pyproject.toml" || base == "setup.py" || base == "go.mod" ||
		base == "cargo.toml" || base == "makefile" || base == "cmakelists.txt" || base == "gemfile" ||
		base == "composer.json" || base == "mix.exs" || base == "mvnw" || base == "gradlew" ||
		base == "libs.versions.toml"
}

func isToolchainVersionPath(base string) bool {
	return base == ".tool-versions" || base == ".nvmrc" || base == ".node-version" || base == ".python-version" ||
		base == ".java-version" || base == ".go-version" || base == ".ruby-version" || base == ".sdkmanrc" ||
		strings.HasPrefix(base, "rust-toolchain")
}

func isCodeGenerationPath(lower, base string) bool {
	return strings.HasPrefix(lower, "codegen/") || strings.Contains(lower, "/codegen/") ||
		strings.HasPrefix(base, "codegen.") || base == "buf.gen.yaml" || base == "buf.gen.yml" ||
		base == "sqlc.yaml" || base == "sqlc.json" || base == "graphql-codegen.yml" || base == "graphql-codegen.yaml" ||
		strings.HasPrefix(base, "openapi-generator") || strings.HasPrefix(base, "swagger-codegen") ||
		strings.HasPrefix(base, ".graphqlrc")
}

func isDeploymentManifestPath(lower, base string) bool {
	return strings.HasPrefix(base, "dockerfile") || strings.HasPrefix(base, "docker-compose") ||
		base == "chart.yaml" || base == "kustomization.yaml" || base == "kustomization.yml" ||
		base == "skaffold.yaml" || base == "serverless.yml" ||
		strings.Contains(lower, "/deploy/") || strings.Contains(lower, "/deployment/") ||
		strings.Contains(lower, "/helm/") || strings.Contains(lower, "/k8s/") || strings.Contains(lower, "/manifests/") ||
		strings.HasSuffix(base, ".tf") || strings.HasSuffix(base, ".tfvars")
}

func isDIRegistryPath(lower, base string) bool {
	return base == "wire_gen.go" || base == "di.go" || strings.HasSuffix(base, "_inject.go") ||
		strings.Contains(lower, "/di/") || base == "spring.factories" ||
		strings.HasSuffix(base, ".inject.yaml") || strings.HasSuffix(base, ".inject.yml")
}

func isSharedConfigPath(lower string) bool {
	return strings.HasPrefix(lower, "config/") || strings.HasPrefix(lower, "configs/") ||
		strings.HasSuffix(lower, "/settings.yaml") || strings.HasSuffix(lower, "/settings.yml")
}
