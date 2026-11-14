package jest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

// TestMaterializeDeclaresJUnitReportAndReporter proves that the advertised
// junit-xml result format is really produced: the materialized command runs a
// JUnit reporter and declares the JUnit report that reporter writes.
func TestMaterializeDeclaresJUnitReportAndReporter(t *testing.T) {
	root := t.TempDir()
	key := "packages/core/math.test.js"
	command, err := New().Materialize(context.Background(), adapters.MaterializeRequest{
		Root: root, TargetKeys: []string{key}, KnownTargets: []string{key},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := ".ivp/reports/jest-" + shortHash(key) + ".json"
	wantJUnit := "build/jest-junit/jest-" + shortHash(key) + ".xml"
	if len(command.ReportPaths) != 2 || command.ReportPaths[0] != wantJSON || command.ReportPaths[1] != wantJUnit {
		t.Fatalf("materialized report paths = %v, want [%s %s]", command.ReportPaths, wantJSON, wantJUnit)
	}
	reporter := reporterArgument(t, command.Args)
	if !strings.HasSuffix(reporter, ".js") {
		t.Fatalf("JUnit reporter argument = %q, want a reporter module path", reporter)
	}
	if !filepath.IsAbs(reporter) {
		t.Fatalf("JUnit reporter argument = %q, want an absolute workspace path", reporter)
	}
	absRoot, err := adapters.ResolveWithin(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if rel, relErr := filepath.Rel(absRoot, reporter); relErr != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("JUnit reporter %q is outside the workspace %q", reporter, absRoot)
	}
	source, err := os.ReadFile(reporter)
	if err != nil {
		t.Fatalf("JUnit reporter module was not written: %v", err)
	}
	absJUnit, err := adapters.ResolveWithin(root, wantJUnit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), jsStringLiteral(absJUnit)) {
		t.Fatalf("JUnit reporter module does not carry the declared report path %q:\n%s", absJUnit, source)
	}
	if strings.Contains(string(source), junitOutputToken) {
		t.Fatalf("JUnit reporter module still carries the unresolved output token:\n%s", source)
	}
}

// TestParseResultsPrefersJestJSONOverJUnitXML proves that the JUnit report of
// a run is an additional artifact: the JSON result report stays the
// authoritative document and the test counts are not doubled.
func TestParseResultsPrefersJestJSONOverJUnitXML(t *testing.T) {
	root := t.TempDir()
	jsonReport := ".ivp/reports/jest-" + shortHash("tests/a.test.js") + ".json"
	junitReport := "build/jest-junit/jest-" + shortHash("tests/a.test.js") + ".xml"
	for report, content := range map[string]string{
		jsonReport:  `{"numTotalTests":2,"numPassedTests":2,"numFailedTests":0,"numPendingTests":0,"numTodoTests":0,"testResults":[{"name":"tests/a.test.js","assertionResults":[{"fullName":"works","status":"passed","duration":1},{"fullName":"also works","status":"passed","duration":2}]}]}`,
		junitReport: `<?xml version="1.0" encoding="utf-8"?><testsuites name="jest tests" tests="2" failures="0" errors="0" skipped="0" time="0.003"><testsuite name="tests/a.test.js" tests="2" failures="0" errors="0" skipped="0" time="0.003"><testcase classname="tests/a.test.js" name="works" time="0.001"></testcase><testcase classname="tests/a.test.js" name="also works" time="0.002"></testcase></testsuite></testsuites>`,
	} {
		abs := filepath.Join(root, filepath.FromSlash(report))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := New().ParseResults(context.Background(), adapters.ParseRequest{
		Root: root, ReportPaths: []string{jsonReport, junitReport}, ExitCode: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Tests != 2 || parsed.Passed != 2 || parsed.Failed != 0 {
		t.Fatalf("parsed results = %+v, want 2 tests and 2 passes without double counting", parsed)
	}
}

// TestJunitReporterWritesJunitXML exercises the reporter module with a real
// node runtime. The produced XML must be a JUnit report that the shared JUnit
// parser understands, so the advertised junit-xml format is a real artifact.
func TestJunitReporterWritesJunitXML(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed on this host: the JUnit reporter cannot be exercised")
	}
	root := t.TempDir()
	report := junitReportPath("packages/core/math.test.js")
	module, err := writeJunitReporter(root, report)
	if err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(root, "results.json")
	payload := `{"testResults":[{"testFilePath":"packages/core/math.test.js","status":"failed","assertionResults":[{"fullName":"math adds","status":"passed","duration":3},{"fullName":"math divides","status":"failed","duration":7,"failureMessages":["expected 1 to equal 2"]},{"fullName":"math skips","status":"pending","duration":null}]}]}`
	if err := os.WriteFile(results, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, module, results, root).CombinedOutput()
	if err != nil {
		t.Fatalf("JUnit reporter failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(report)))
	if err != nil {
		t.Fatalf("the JUnit report was not written: %v", err)
	}
	if !strings.Contains(string(data), "<testsuites") || !strings.Contains(string(data), "<testcase") {
		t.Fatalf("the JUnit report is not a JUnit document:\n%s", data)
	}
	parsed, err := adapters.ParseJUnit(data, 1)
	if err != nil {
		t.Fatalf("the JUnit report is not parseable: %v\n%s", err, data)
	}
	if parsed.Tests != 3 || parsed.Failed != 1 || parsed.Skipped != 1 || parsed.Passed != 1 {
		t.Fatalf("parsed JUnit report = %+v, want 3 tests, 1 failed, 1 skipped and 1 passed", parsed)
	}
}

// reporterArgument returns the JUnit reporter argument of a materialized jest
// command.
func reporterArgument(t *testing.T, args []string) string {
	t.Helper()
	for _, arg := range args {
		if strings.HasPrefix(arg, "--reporters=") && arg != "--reporters=default" {
			return strings.TrimPrefix(arg, "--reporters=")
		}
	}
	t.Fatalf("the materialized command carries no JUnit reporter: %v", args)
	return ""
}
