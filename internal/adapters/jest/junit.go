package jest

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ivp/ivp/internal/adapters"
)

// junitReporterTemplate is the JUnit reporter module the adapter hands to
// jest through --reporters. Jest does not accept reporter options on the
// command line and the third party jest-junit reporter only honours its
// output path through environment variables that the execution runner does
// not allowlist, so the adapter ships its own reporter module and bakes the
// report path into it.
//
//go:embed reporter/ivp-junit-reporter.js
var junitReporterTemplate string

// junitOutputToken is replaced with the absolute path of the JUnit report when
// the reporter module is materialized into the workspace.
const junitOutputToken = "__IVP_JUNIT_OUTPUT_PATH__"

// junitReportPath returns the deterministic repository relative path of the
// JUnit XML report produced for a materialized selection. It follows the
// adapter's existing convention of a hash derived file name.
func junitReportPath(key string) string {
	return filepath.ToSlash(filepath.Join("build", "jest-junit", "jest-"+shortHash(key)+".xml"))
}

// writeJunitReporter materializes the JUnit reporter module with the report
// path baked in and returns the absolute path of the written module. The
// module is written next to the report it produces so that both stay inside
// the workspace build directory.
func writeJunitReporter(root, report string) (string, error) {
	abs, err := adapters.ResolveWithin(root, report)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("jest JUnit report directory could not be created: %w", err)
	}
	module := filepath.Join(dir, "ivp-junit-reporter-"+shortHash(report)+".js")
	content := strings.ReplaceAll(junitReporterTemplate, junitOutputToken, jsStringLiteral(abs))
	if err := os.WriteFile(module, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("jest JUnit reporter could not be written: %w", err)
	}
	return module, nil
}

// jsStringLiteral escapes a value for use inside a JavaScript string literal.
// A JSON string is a valid JavaScript string literal, so the encoding of
// JSON.stringify is reused without its quotes.
func jsStringLiteral(value string) string {
	quoted, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(quoted[1 : len(quoted)-1])
}
