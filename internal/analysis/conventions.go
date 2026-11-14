package analysis

import (
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/ivp/ivp/internal/domain"
)

// Convention-derived graph evidence (LLD §6.1 edge inventory: BUILT_FROM,
// CONFIGURES, CONTRACT_CONSUMER, OWNED_BY, COLOCATED_WITH) and conflict
// marking. Producers are pure over repository paths/contents so `ivp index`
// (CLI) and the server-side evidence reader share one implementation. Exact
// pairing conventions are fixed by ADR 0013; when a convention's input is
// absent (no CODEOWNERS file, no schema imports, no generated input) the
// producer emits nothing rather than guessing.

// generatedOutputSuffix maps a generated artifact to the input suffix it is
// built from. Only exact-suffix matches are paired (ADR 0013).
var generatedOutputSuffix = []struct{ out, input string }{
	{"_grpc.pb.go", ".proto"},
	{"_pb2_grpc.py", ".proto"},
	{"_pb2.pyi", ".proto"},
	{"_pb2.py", ".proto"},
	{"_pb2.pyi", ".proto"},
	{"_string.go", ".go"},
	{".pb.go", ".proto"},
	{".pb.cc", ".proto"},
	{".pb.h", ".proto"},
	{".pb.rs", ".proto"},
}

// protoImportRe matches `import "path.proto";` declarations in proto files.
var protoImportRe = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"\r\n]+)"\s*;`)

// CoLocationPairings returns convention pairs (test path, source path) for
// tracked test files whose conventional source counterpart is also tracked.
// A test file never pairs with another test file (ADR 0013).
func CoLocationPairings(tracked []string) [][2]string {
	present := make(map[string]bool, len(tracked))
	for _, p := range tracked {
		present[p] = true
	}
	var out [][2]string
	seen := map[[2]string]bool{}
	for _, rel := range tracked {
		if !isTestPath(rel) {
			continue
		}
		src, ok := testStem(rel)
		if !ok || isTestPath(src) || !present[src] {
			continue
		}
		key := [2]string{rel, src}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sortPairs(out)
	return out
}

// IsTestPath reports whether a repository path looks like a test file under
// the shipped naming conventions.
func IsTestPath(p string) bool { return isTestPath(p) }

func isTestPath(p string) bool {
	base := path.Base(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, ".test.jsx") ||
		strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.js") ||
		strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, "Test.java") ||
		strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, ".spec.rb") || strings.HasSuffix(base, "_test.php") {
		return true
	}
	if strings.HasSuffix(stem, "Test") && ext == ".java" {
		return true
	}
	// pytest's default file pattern: test_*.py.
	if ext == ".py" && strings.HasPrefix(base, "test_") {
		return true
	}
	if strings.HasSuffix(stem, "_test") || strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return true
	}
	return false
}

// testStem maps a test file path to the source file stem it conventionally
// tests ("pkg/foo_test.go" -> "pkg/foo.go").
func testStem(p string) (string, bool) {
	base := path.Base(p)
	cases := []struct{ suffix, replacement string }{
		{"_test.go", ".go"},
		{"_test.py", ".py"},
		{"_test.ts", ".ts"},
		{"_test.tsx", ".tsx"},
		{"_test.js", ".js"},
		{"_test.jsx", ".jsx"},
		{"_spec.rb", ".rb"},
		{"_spec.js", ".js"},
		{"_spec.ts", ".ts"},
		{"_test.php", ".php"},
		{".test.ts", ".ts"},
		{".test.tsx", ".tsx"},
		{".test.js", ".js"},
		{".test.jsx", ".jsx"},
		{".spec.ts", ".ts"},
		{".spec.js", ".js"},
		{"Test.java", ".java"},
	}
	for _, c := range cases {
		if strings.HasSuffix(base, c.suffix) {
			return path.Join(path.Dir(p), strings.TrimSuffix(base, c.suffix)+c.replacement), true
		}
	}
	// pytest's default file pattern: test_auth.py -> auth.py.
	if path.Ext(base) == ".py" && strings.HasPrefix(base, "test_") {
		if stem := strings.TrimPrefix(base, "test_"); stem != "" {
			return path.Join(path.Dir(p), stem), true
		}
	}
	return "", false
}

// BuiltFromPairs pairs generated artifacts with their input files when the
// input is tracked in the repository. The most specific suffix wins (ADR 0013).
func BuiltFromPairs(tracked []string) [][2]string {
	present := make(map[string]bool, len(tracked))
	for _, p := range tracked {
		present[p] = true
	}
	type pair struct{ gen, input string }
	seen := map[pair]bool{}
	var out [][2]string
	for _, p := range tracked {
		for _, suf := range generatedOutputSuffix {
			if !strings.HasSuffix(p, suf.out) {
				continue
			}
			input := strings.TrimSuffix(p, suf.out) + suf.input
			if input != p && present[input] && !seen[pair{p, input}] {
				seen[pair{p, input}] = true
				out = append(out, [2]string{p, input})
			}
			break // most specific suffix wins
		}
	}
	sortPairs(out)
	return out
}

// ConfiguredByPairs pairs a build/test target path with the configuration
// file that governs it: the nearest ancestor-directory config, falling back to
// the repo-root config. Both inputs are repository paths; the caller supplies
// only files that exist (ADR 0013).
func ConfiguredByPairs(targetPaths []string, configPaths []string) [][2]string {
	var out [][2]string
	seen := map[[2]string]bool{}
	for _, target := range targetPaths {
		if target == "" {
			continue
		}
		best, bestLen := "", -1
		for _, cfg := range configPaths {
			if cfg == "" {
				continue
			}
			cfgDir := path.Dir(cfg)
			if cfgDir != "." && !isAncestorDir(cfgDir, target) {
				continue
			}
			if len(cfgDir) > bestLen {
				best, bestLen = cfg, len(cfgDir)
			}
		}
		if best == "" {
			continue
		}
		key := [2]string{target, best}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sortPairs(out)
	return out
}

func isAncestorDir(dir, p string) bool {
	if dir == "." || dir == "" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// SchemaConsumers scans tracked source files for imports/references of the
// given schema path. The scan is bounded by maxBytes total and skips files
// larger than maxFileBytes; a file that cannot be read is silently skipped
// (missing content only removes evidence, never fabricates it).
func SchemaConsumers(repoRoot, schemaPath string, tracked []string, maxFileBytes int64, maxBytes int64) []string {
	var total int64
	seen := map[string]bool{}
	var out []string
	needle := []byte(schemaPath)
	for _, rel := range tracked {
		if rel == schemaPath || isTestPath(rel) {
			continue
		}
		if total >= maxBytes {
			break
		}
		full := path.Join(repoRoot, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() || info.Size() > int64(maxFileBytes) || info.Size() > maxBytes-total {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		total += int64(len(data))
		if importsSchema(data, schemaPath) || bytesContainFold(data, needle) {
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	sortStrings(out)
	return out
}

func importsSchema(data []byte, schemaPath string) bool {
	for _, m := range protoImportRe.FindAllSubmatch(data, -1) {
		if len(m) > 1 && string(m[1]) == schemaPath {
			return true
		}
	}
	return false
}

func bytesContainFold(haystack, needle []byte) bool {
	return strings.Contains(strings.ToLower(string(haystack)), strings.ToLower(string(needle)))
}

// CodeownersRule is one parsed CODEOWNERS rule: a path pattern and the
// handles that own matching paths.
type CodeownersRule struct {
	pattern string
	owners  []string
}

// ParseCodeowners parses CODEOWNERS syntax: blank lines, `#` comments,
// `!`-negated patterns, and whitespace-separated owner lists. Invalid lines
// (no owners) are skipped.
func ParseCodeowners(content string) []CodeownersRule {
	var rules []CodeownersRule
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		pattern := fields[0]
		if strings.HasPrefix(pattern, "!") {
			continue
		}
		owners := make([]string, 0, len(fields)-1)
		for _, o := range fields[1:] {
			if o != "" {
				owners = append(owners, o)
			}
		}
		if len(owners) == 0 {
			continue
		}
		rules = append(rules, CodeownersRule{pattern: pattern, owners: owners})
	}
	return rules
}

// normalizeCodeownersPattern converts a CODEOWNERS pattern to the glob
// dialect understood by GlobMatch: a leading slash marks a repo-root-relative
// path and is removed, a trailing slash is a directory pattern that matches
// everything beneath it, and a pattern that is still slash-free matches at
// any depth (ADR 0013).
func normalizeCodeownersPattern(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	rooted := strings.HasPrefix(p, "/")
	p = strings.TrimPrefix(p, "/")
	if strings.HasSuffix(p, "/") {
		p += "**"
	}
	if !rooted && !strings.Contains(p, "/") {
		p = "**/" + p
	}
	return p
}

// CodeownerRulesFor returns owning handles for a repository path. Ownership
// comes only from explicit CODEOWNERS files; there is no fallback (ADR 0013).
func CodeownerRulesFor(rules []CodeownersRule, repoPath string) []string {
	if len(rules) == 0 {
		return nil
	}
	owners := []string{}
	seen := map[string]bool{}
	for _, r := range rules {
		if !GlobMatch(normalizeCodeownersPattern(r.pattern), repoPath) {
			continue
		}
		for _, o := range r.owners {
			if !seen[o] {
				seen[o] = true
				owners = append(owners, o)
			}
		}
	}
	return owners
}

// StableOwnerKey is the deterministic graph-node stable key for an owner.
func StableOwnerKey(handle string) string { return "owner:" + handle }

// StableConfigKey is the deterministic graph-node stable key for a config file.
func StableConfigKey(repoPath string) string { return "config:" + repoPath }

// StableSchemaKey is the deterministic graph-node stable key for an API schema.
func StableSchemaKey(repoPath string) string { return "api:" + repoPath }

// MarkConflictingEdges flags duplicate (same endpoints and type with
// differing precision) and contradictory opposite-direction pairs of the same
// edge type. All involved edges are retained and marked; nothing is dropped
// (ADR 0013).
func MarkConflictingEdges(edges []domain.GraphEdge) int {
	type sig struct {
		from, to domain.UUID
		kind     domain.GraphEdgeType
	}
	bySig := map[sig][]int{}
	for i, e := range edges {
		key := sig{e.FromNodeID, e.ToNodeID, e.EdgeType}
		bySig[key] = append(bySig[key], i)
	}
	marked := 0
	mark := func(i int) {
		if !edges[i].Conflict {
			edges[i].Conflict = true
			marked++
		}
	}
	for _, idxs := range bySig {
		if len(idxs) < 2 {
			continue
		}
		precisions := map[string]bool{}
		for _, i := range idxs {
			prec, _ := edges[i].Attributes["precision"].(string)
			precisions[prec] = true
		}
		if len(precisions) > 1 {
			for _, i := range idxs {
				mark(i)
			}
		}
	}
	for key, idxs := range bySig {
		if len(idxs) == 0 {
			continue
		}
		other, ok := bySig[sig{key.to, key.from, key.kind}]
		if !ok {
			continue
		}
		for _, i := range idxs {
			mark(i)
		}
		for _, i := range other {
			mark(i)
		}
	}
	return marked
}

// --- helpers ---

func sortPairs(pairs [][2]string) {
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && pairs[j][0] < pairs[j-1][0]; j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
