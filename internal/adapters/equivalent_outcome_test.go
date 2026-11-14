// Shared equivalent-outcome contract suite for every adapter's ParseResults
// (strategy section 3.1 items 1-2, LLD section 8.9). It drives all 126
// golden fixtures under fixtures/ through each adapter and asserts one
// canonical classification per category across adapters and tool versions,
// that setup failure is never a pass, and the section 8.9 edge cases:
// malformed reports, paths with spaces, unicode paths, timeouts, and
// zero-test output.
package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/bazel"
	"github.com/ivp/ivp/internal/adapters/generic"
	"github.com/ivp/ivp/internal/adapters/gradle"
	"github.com/ivp/ivp/internal/adapters/jest"
	"github.com/ivp/ivp/internal/adapters/maven"
	"github.com/ivp/ivp/internal/adapters/nx"
	"github.com/ivp/ivp/internal/adapters/pytest"
)

// canonicalOutcome is the shared classification vocabulary the contract
// suite asserts for every adapter and every corpus category.
type canonicalOutcome string

const (
	outcomePass     canonicalOutcome = "PASS"
	outcomeFail     canonicalOutcome = "FAIL"
	outcomeZero     canonicalOutcome = "ZERO"
	outcomeSkipOnly canonicalOutcome = "SKIP_ONLY"
	outcomeInfra    canonicalOutcome = "INFRA"
)

// corpusANSIEscape is U+001B, written numerically so this file carries no
// raw control bytes.
const corpusANSIEscape = 0x1b

// unknownTargetKey is materialized against an empty snapshot to exercise
// the unknown-target setup failure without any fixture file.
const unknownTargetKey = "not-in-the-snapshot"

// corpusFixturesDir is relative to the package directory; go test runs the
// binary with the package directory as working directory.
const corpusFixturesDir = "fixtures"

var allCorpusOutcomes = []canonicalOutcome{outcomePass, outcomeFail, outcomeZero, outcomeSkipOnly, outcomeInfra}

// canonicalOutcomeOf classifies one ParseResults outcome into the shared
// five-way vocabulary. An unreadable or unparsable report set is an
// infrastructure failure and can never become a pass; a reported failure
// outranks everything else a complete report can say.
func canonicalOutcomeOf(res adapters.ParsedResults, err error) canonicalOutcome {
	switch {
	case err != nil || !res.Complete || !res.HasReliableReport:
		return outcomeInfra
	case res.Failed > 0 || res.Errors > 0:
		return outcomeFail
	case res.ZeroTests:
		return outcomeZero
	case res.Skipped > 0 && res.Passed == 0:
		return outcomeSkipOnly
	default:
		return outcomePass
	}
}

// formatCategories are the eight file-backed report categories.
var formatCategories = []string{
	"full_suite", "selected_suite", "failure", "skipped",
	"zero_tests", "malformed", "truncated", "odd_output",
}

// corpusCategories is the full eleven-category vocabulary from strategy
// section 3.1 item 1. "missing" and "unknown_target" are absence-asserted
// and therefore have no fixture file.
var corpusCategories = []string{
	"full_suite", "selected_suite", "failure", "skipped",
	"zero_tests", "malformed", "truncated", "missing",
	"timeout_interrupt", "unknown_target", "odd_output",
}

// expectedCorpusOutcome is the single canonical classification each
// category must produce for every adapter and both tool versions. The
// "skipped" category is deliberately PASS everywhere: its fixtures carry
// skips next to passes, because bazel's parseBEP can never express a
// skip-only result (it does not set Skipped at all) and the cross-adapter
// equivalence assertion must hold literally. The skip-only class is
// covered synthetically by TestCanonicalOutcomeClassifierCoversFullVocabulary.
var expectedCorpusOutcome = map[string]canonicalOutcome{
	"full_suite":        outcomePass,
	"selected_suite":    outcomePass,
	"failure":           outcomeFail,
	"skipped":           outcomePass,
	"zero_tests":        outcomeZero,
	"malformed":         outcomeInfra,
	"truncated":         outcomeInfra,
	"missing":           outcomeInfra,
	"timeout_interrupt": outcomeInfra,
	"unknown_target":    outcomeInfra,
	"odd_output":        outcomePass,
}

// setupFailureCategories must never classify as a pass regardless of what
// any parser reported.
var setupFailureCategories = map[string]bool{
	"malformed":         true,
	"truncated":         true,
	"missing":           true,
	"timeout_interrupt": true,
	"unknown_target":    true,
}

// corpusExitCode mirrors how the execution runner reports each class:
// failures and truncated output exit 1, a timeout exits 124, everything
// else exits 0.
func corpusExitCode(category string) int {
	switch category {
	case "failure", "truncated":
		return 1
	case "timeout_interrupt":
		return 124
	default:
		return 0
	}
}

// corpusSpec binds one registered adapter to its two supported tool
// versions, the fixture extension of its report format, and the
// materialized report path the contract test writes fixtures to.
type corpusSpec struct {
	id       string
	adapter  adapters.Adapter
	versions []string
	report   map[string]string
	ext      map[string]string
}

var corpusSpecs = []corpusSpec{
	{
		id: "gradle", adapter: gradle.New(), versions: []string{"7.6", "8.7"},
		report: map[string]string{
			"7.6": "build/test-results/test/TEST-com.example.AppTest.xml",
			"8.7": "build/test-results/test/TEST-com.example.AppTest.xml",
		},
		ext: map[string]string{"7.6": "xml", "8.7": "xml"},
	},
	{
		id: "maven", adapter: maven.New(), versions: []string{"surefire-2.22", "surefire-3.2"},
		report: map[string]string{
			"surefire-2.22": "target/surefire-reports/TEST-com.example.AccountServiceTest.xml",
			"surefire-3.2":  "target/surefire-reports/TEST-com.example.AccountServiceTest.xml",
		},
		ext: map[string]string{"surefire-2.22": "xml", "surefire-3.2": "xml"},
	},
	{
		id: "nx", adapter: nx.New(), versions: []string{"15", "19"},
		report: map[string]string{
			"15": ".ivp/reports/nx-7a3f91c2.json",
			"19": "reports/web-junit.xml",
		},
		ext: map[string]string{"15": "json", "19": "xml"},
	},
	{
		id: "bazel", adapter: bazel.New(), versions: []string{"5", "7"},
		report: map[string]string{
			"5": ".ivp/reports/bazel-9d4e5f6a.jsonl",
			"7": ".ivp/reports/bazel-9d4e5f6a.jsonl",
		},
		ext: map[string]string{"5": "jsonl", "7": "jsonl"},
	},
	{
		id: "pytest", adapter: pytest.New(), versions: []string{"7", "8"},
		report: map[string]string{
			"7": ".ivp/reports/pytest-3e8c10bf.xml",
			"8": ".ivp/reports/pytest-3e8c10bf.xml",
		},
		ext: map[string]string{"7": "xml", "8": "xml"},
	},
	{
		id: "jest", adapter: jest.New(), versions: []string{"27", "29"},
		report: map[string]string{
			"27": ".ivp/reports/jest-5f2a9d77.json",
			"29": ".ivp/reports/jest-5f2a9d77.json",
		},
		ext: map[string]string{"27": "json", "29": "json"},
	},
	{
		id: "generic", adapter: generic.New(), versions: []string{"config-v1", "config-v2"},
		report: map[string]string{
			"config-v1": "reports/generic-report.xml",
			"config-v2": "reports/generic-report.json",
		},
		ext: map[string]string{"config-v1": "xml", "config-v2": "json"},
	},
}

// fixtureBytes reads one corpus fixture relative to the package directory.
func fixtureBytes(t *testing.T, id, version, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusFixturesDir, id, version, name))
	if err != nil {
		t.Fatalf("read fixture %s/%s/%s: %v", id, version, name, err)
	}
	return data
}

// writeReport materializes report bytes at the adapter's report path under
// root, using the slash-separated corpus path as the relative location.
func writeReport(t *testing.T, root, reportRel string, data []byte) {
	t.Helper()
	dst := filepath.Join(root, filepath.FromSlash(reportRel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create report directory: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

// corpusParseRequest builds the ParseRequest for one corpus row. The
// unknown-target row additionally proves the setup failure at its first
// surface: Materialize rejects a key that is absent from the snapshot.
func corpusParseRequest(t *testing.T, spec corpusSpec, version, category, root string) adapters.ParseRequest {
	t.Helper()
	report := spec.report[version]
	req := adapters.ParseRequest{Root: root, ExitCode: corpusExitCode(category)}
	switch category {
	case "timeout_interrupt":
		// The interrupted stdout exists, but the declared report path is
		// never written: the runner killed the command first.
		req.Stdout = fixtureBytes(t, spec.id, version, "timeout_interrupt.txt")
		req.ReportPaths = []string{report}
	case "missing":
		// Neither report nor stdout: the tool never produced anything.
		req.ReportPaths = []string{report}
	case "unknown_target":
		if err := spec.adapter.ValidateConfig(adapters.AdapterConfig{}); err != nil {
			t.Errorf("%s rejected the empty default config: %v", spec.id, err)
		}
		_, err := spec.adapter.Materialize(context.Background(), adapters.MaterializeRequest{
			Root:         root,
			TargetKeys:   []string{unknownTargetKey},
			KnownTargets: nil,
		})
		if !errors.Is(err, adapters.ErrUnknownTarget) {
			t.Errorf("%s materialized unknown target %q: err=%v, want ErrUnknownTarget", spec.id, unknownTargetKey, err)
		}
		req.ReportPaths = []string{report}
	default:
		name := category + "." + spec.ext[version]
		writeReport(t, root, report, fixtureBytes(t, spec.id, version, name))
		req.ReportPaths = []string{report}
	}
	return req
}

// TestFixtureCorpusClassifiesEquivalentlyAcrossAdapters is the shared
// table-driven contract from strategy section 3.1 item 2: every adapter,
// for every category and both tool versions, must classify identically
// into the canonical vocabulary, and setup failure is never a pass.
func TestFixtureCorpusClassifiesEquivalentlyAcrossAdapters(t *testing.T) {
	type observation struct {
		spec     corpusSpec
		version  string
		category string
		outcome  canonicalOutcome
		res      adapters.ParsedResults
		parseErr error
	}
	var observed []observation
	for _, spec := range corpusSpecs {
		for _, version := range spec.versions {
			for _, category := range corpusCategories {
				name := spec.id + "/" + version + "/" + category
				// t.Run without Parallel runs the closure inline, so the
				// observation slice stays single-writer.
				t.Run(name, func(t *testing.T) {
					root := t.TempDir()
					req := corpusParseRequest(t, spec, version, category, root)
					res, parseErr := spec.adapter.ParseResults(context.Background(), req)
					obs := observation{
						spec:     spec,
						version:  version,
						category: category,
						outcome:  canonicalOutcomeOf(res, parseErr),
						res:      res,
						parseErr: parseErr,
					}
					observed = append(observed, obs)
					if want := expectedCorpusOutcome[category]; obs.outcome != want {
						t.Errorf("canonical outcome = %s, want %s (res=%+v err=%v)", obs.outcome, want, obs.res, obs.parseErr)
					}
				})
			}
		}
	}

	wantRows := 0
	for _, spec := range corpusSpecs {
		wantRows += len(spec.versions) * len(corpusCategories)
	}
	if len(observed) != wantRows {
		t.Fatalf("collected %d observations, want %d", len(observed), wantRows)
	}

	// (a) One canonical outcome per category across all adapters and both
	// tool versions of each adapter.
	byCategory := map[string]map[canonicalOutcome][]string{}
	for _, obs := range observed {
		if byCategory[obs.category] == nil {
			byCategory[obs.category] = map[canonicalOutcome][]string{}
		}
		label := obs.spec.id + "/" + obs.version
		outcomes := byCategory[obs.category]
		outcomes[obs.outcome] = append(outcomes[obs.outcome], label)
	}
	if len(byCategory) != len(corpusCategories) {
		t.Errorf("observed %d distinct categories, want %d", len(byCategory), len(corpusCategories))
	}
	for _, category := range corpusCategories {
		outcomes := byCategory[category]
		if len(outcomes) == 0 {
			t.Errorf("category %s has no observations", category)
			continue
		}
		if len(outcomes) > 1 {
			for out, labels := range outcomes {
				t.Errorf("category %s diverges: outcome %s observed for %s", category, out, strings.Join(labels, ", "))
			}
		}
		want := expectedCorpusOutcome[category]
		for out, labels := range outcomes {
			if out != want {
				t.Errorf("category %s: outcome %s observed for %s, want %s", category, out, strings.Join(labels, ", "), want)
			}
		}
	}

	// (b) Setup failure is never a pass, and a pass always rests on a
	// complete, reliable report.
	for _, obs := range observed {
		label := obs.spec.id + "/" + obs.version + "/" + obs.category
		if obs.outcome == outcomePass && (!obs.res.Complete || !obs.res.HasReliableReport) {
			t.Errorf("%s: PASS without a complete reliable report (res=%+v err=%v)", label, obs.res, obs.parseErr)
		}
		if setupFailureCategories[obs.category] && obs.outcome == outcomePass {
			t.Errorf("%s: setup failure classified PASS", label)
		}
	}

	// (c) Version identity: within one adapter, both tool versions must
	// agree on every category.
	type versionOutcome struct {
		version string
		outcome canonicalOutcome
	}
	versionRows := map[string]map[string][]versionOutcome{}
	for _, obs := range observed {
		cats := versionRows[obs.spec.id]
		if cats == nil {
			cats = map[string][]versionOutcome{}
			versionRows[obs.spec.id] = cats
		}
		cats[obs.category] = append(cats[obs.category], versionOutcome{version: obs.version, outcome: obs.outcome})
	}
	for id, cats := range versionRows {
		for category, rows := range cats {
			if len(rows) < 2 {
				t.Errorf("%s/%s observed only %d tool version(s), want 2", id, category, len(rows))
				continue
			}
			for _, row := range rows[1:] {
				if row.outcome != rows[0].outcome {
					t.Errorf("%s category %s: tool version %s classified %s, tool version %s classified %s",
						id, category, rows[0].version, rows[0].outcome, row.version, row.outcome)
				}
			}
		}
	}

	// (d) odd_output: parser explanation strings must never carry the
	// ANSI escape byte or secret-looking content, no matter what the
	// report contained.
	for _, obs := range observed {
		if obs.category != "odd_output" {
			continue
		}
		label := obs.spec.id + "/" + obs.version + "/odd_output"
		for _, reason := range obs.res.Reasons {
			if corpusReasonIsDirty(reason) {
				t.Errorf("%s leaked control/secret content into Reasons: %q", label, reason)
			}
		}
		if len(obs.res.Cases) == 0 {
			t.Errorf("%s parsed no test cases from a passing report", label)
		}
	}
}

// corpusReasonIsDirty reports whether a parser reason string carries an
// ESC byte or one of the corpus's secret-looking markers.
func corpusReasonIsDirty(reason string) bool {
	if strings.ContainsRune(reason, corpusANSIEscape) {
		return true
	}
	for _, marker := range []string{"hunter2", "AKIAFAKEEXAMPLEKEY"} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

// TestFixtureCorpusLayoutMatchesContractTable pins the checked-in layout:
// exactly the 126 expected files (7 adapters x 2 versions x 9 file-backed
// categories), the README, and no files for the two absence-asserted
// categories.
func TestFixtureCorpusLayoutMatchesContractTable(t *testing.T) {
	want := map[string]bool{}
	for _, spec := range corpusSpecs {
		for _, version := range spec.versions {
			for _, category := range formatCategories {
				want[spec.id+"/"+version+"/"+category+"."+spec.ext[version]] = true
			}
			want[spec.id+"/"+version+"/timeout_interrupt.txt"] = true
		}
	}
	if len(want) != 126 {
		t.Fatalf("contract table expects %d fixture files, want 126", len(want))
	}

	got := map[string]bool{}
	err := filepath.WalkDir(corpusFixturesDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(corpusFixturesDir, path)
		if relErr != nil {
			return relErr
		}
		key := filepath.ToSlash(rel)
		if key == "README.md" {
			return nil
		}
		got[key] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", corpusFixturesDir, err)
	}

	for key := range want {
		if !got[key] {
			t.Errorf("missing fixture file %s", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("unexpected file in fixture corpus: %s", key)
		}
	}
	if len(got) != 126 {
		t.Errorf("fixture corpus holds %d files, want 126", len(got))
	}

	// The two absence-asserted categories must not have files.
	for key := range got {
		base := key[strings.LastIndex(key, "/")+1:]
		for _, absent := range []string{"missing", "unknown_target"} {
			if strings.HasPrefix(base, absent+".") {
				t.Errorf("absence-asserted category %q has a fixture file: %s", absent, key)
			}
		}
	}

	readme, statErr := os.Stat(filepath.Join(corpusFixturesDir, "README.md"))
	if statErr != nil {
		t.Errorf("fixtures README missing: %v", statErr)
	} else if readme.Size() == 0 {
		t.Errorf("fixtures README is empty")
	}
	for key := range got {
		info, infoErr := os.Stat(filepath.Join(corpusFixturesDir, filepath.FromSlash(key)))
		if infoErr != nil {
			t.Errorf("stat %s: %v", key, infoErr)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("fixture %s is empty", key)
		}
	}
}

// TestCorpusReportsParseFromPathsWithSpacesAndUnicode covers the section
// 8.9 "paths with spaces, unicode paths" cases: each adapter must parse a
// golden report materialized at a path containing spaces, Cyrillic,
// CJK, and accented characters.
func TestCorpusReportsParseFromPathsWithSpacesAndUnicode(t *testing.T) {
	for _, spec := range corpusSpecs {
		for _, version := range spec.versions {
			t.Run(spec.id+"/"+version, func(t *testing.T) {
				root := t.TempDir()
				rel := fmt.Sprintf("odd output/путь/测试/отчёт 2026 report.%s", spec.ext[version])
				writeReport(t, root, rel, fixtureBytes(t, spec.id, version, "full_suite."+spec.ext[version]))
				res, err := spec.adapter.ParseResults(context.Background(), adapters.ParseRequest{
					Root:        root,
					ReportPaths: []string{rel},
					ExitCode:    0,
				})
				if got := canonicalOutcomeOf(res, err); got != outcomePass {
					t.Errorf("outcome from spaced unicode path %q = %s, want PASS (res=%+v err=%v)", rel, got, res, err)
				}
				if !res.Complete || !res.HasReliableReport {
					t.Errorf("report from spaced unicode path was not complete and reliable: %+v", res)
				}
			})
		}
	}
}

// TestZeroTestReportsAreNeverPass covers the section 8.9 zero-test
// output case: a well-formed report that executed zero tests classifies
// ZERO, never PASS, for every adapter and tool version.
func TestZeroTestReportsAreNeverPass(t *testing.T) {
	for _, spec := range corpusSpecs {
		for _, version := range spec.versions {
			t.Run(spec.id+"/"+version, func(t *testing.T) {
				root := t.TempDir()
				writeReport(t, root, spec.report[version], fixtureBytes(t, spec.id, version, "zero_tests."+spec.ext[version]))
				res, err := spec.adapter.ParseResults(context.Background(), adapters.ParseRequest{
					Root:        root,
					ReportPaths: []string{spec.report[version]},
					ExitCode:    0,
				})
				got := canonicalOutcomeOf(res, err)
				if got == outcomePass {
					t.Errorf("zero-test report classified PASS (res=%+v err=%v)", res, err)
				}
				if got != outcomeZero {
					t.Errorf("zero-test outcome = %s, want ZERO (res=%+v err=%v)", got, res, err)
				}
				if !res.ZeroTests {
					t.Errorf("ZeroTests flag not set for a zero-test report: %+v", res)
				}
			})
		}
	}
}

// TestTimeoutInterruptIsNeverPass covers the section 8.9 timeout case:
// interrupted stdout with a declared-but-unwritten report classifies
// INFRA and never PASS for every adapter and tool version.
func TestTimeoutInterruptIsNeverPass(t *testing.T) {
	for _, spec := range corpusSpecs {
		for _, version := range spec.versions {
			t.Run(spec.id+"/"+version, func(t *testing.T) {
				root := t.TempDir()
				stdout := fixtureBytes(t, spec.id, version, "timeout_interrupt.txt")
				res, err := spec.adapter.ParseResults(context.Background(), adapters.ParseRequest{
					Root:        root,
					ReportPaths: []string{spec.report[version]},
					Stdout:      stdout,
					ExitCode:    124,
				})
				got := canonicalOutcomeOf(res, err)
				if got == outcomePass {
					t.Errorf("interrupted run classified PASS (res=%+v err=%v)", res, err)
				}
				if got != outcomeInfra {
					t.Errorf("interrupted outcome = %s, want INFRA (res=%+v err=%v)", got, res, err)
				}
				if err == nil {
					t.Errorf("interrupted run without a report surfaced no error: %+v", res)
				}
				if res.Complete {
					t.Errorf("interrupted run reported a complete result: %+v", res)
				}
			})
		}
	}
}

// TestCanonicalOutcomeClassifierCoversFullVocabulary pins the canonical
// classifier itself against synthetic results, including the SKIP_ONLY
// class that corpus fixtures cannot reach through bazel's BEP parser.
func TestCanonicalOutcomeClassifierCoversFullVocabulary(t *testing.T) {
	cases := []struct {
		name string
		res  adapters.ParsedResults
		err  error
		want canonicalOutcome
	}{
		{
			name: "passing report",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Tests: 3, Passed: 3},
			want: outcomePass,
		},
		{
			name: "reported test failure",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Tests: 2, Passed: 1, Failed: 1},
			want: outcomeFail,
		},
		{
			name: "process error without a reported failure",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Tests: 1, Errors: 1},
			want: outcomeFail,
		},
		{
			name: "failure outranks the zero-test flag",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Failed: 1, ZeroTests: true},
			want: outcomeFail,
		},
		{
			name: "zero-test report",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, ZeroTests: true},
			want: outcomeZero,
		},
		{
			name: "zero outranks skip-only",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Tests: 2, Skipped: 2, ZeroTests: true},
			want: outcomeZero,
		},
		{
			name: "skip-only report without passes",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Tests: 2, Skipped: 2},
			want: outcomeSkipOnly,
		},
		{
			name: "unreadable report set",
			res:  adapters.ParsedResults{Complete: false, HasReliableReport: false, Reasons: []string{"test report set could not be read completely"}},
			err:  errors.New("open report: no such file"),
			want: outcomeInfra,
		},
		{
			name: "incomplete parse",
			res:  adapters.ParsedResults{Complete: false, HasReliableReport: true},
			want: outcomeInfra,
		},
		{
			name: "complete but unreliable",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: false, Tests: 3, Passed: 3},
			want: outcomeInfra,
		},
		{
			name: "parser error after parsed counters",
			res:  adapters.ParsedResults{Complete: true, HasReliableReport: true, Tests: 3, Passed: 3},
			err:  errors.New("malformed report"),
			want: outcomeInfra,
		},
	}

	seen := map[canonicalOutcome]bool{}
	for _, tc := range cases {
		got := canonicalOutcomeOf(tc.res, tc.err)
		seen[got] = true
		tc, got := tc, got
		t.Run(tc.name, func(t *testing.T) {
			if got != tc.want {
				t.Errorf("canonicalOutcomeOf() = %s, want %s", got, tc.want)
			}
		})
	}
	for _, want := range allCorpusOutcomes {
		if !seen[want] {
			t.Errorf("classifier vocabulary has no case producing %s", want)
		}
	}
}
