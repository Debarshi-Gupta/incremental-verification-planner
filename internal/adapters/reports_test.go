package adapters

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseJUnitCountsAndStatuses(t *testing.T) {
	data := []byte(`<testsuites><testsuite name="unit" tests="3" failures="1" skipped="1" time="0.3"><testcase classname="pkg.C" name="pass" time="0.1"/><testcase classname="pkg.C" name="fail"><failure message="assertion">details</failure></testcase><testcase classname="pkg.C" name="skip"><skipped/></testcase></testsuite></testsuites>`)
	got, err := ParseJUnit(data, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Tests != 3 || got.Passed != 1 || got.Failed != 1 || got.Skipped != 1 || got.ZeroTests || got.Duration != 100*time.Millisecond || len(got.Cases) != 3 || got.Cases[0].Duration != 100*time.Millisecond {
		t.Fatalf("unexpected parsed counts: %+v", got)
	}
}

func TestParseJUnitRejectsMalformedAndZero(t *testing.T) {
	if _, err := ParseJUnit([]byte(`<testsuite><testcase>`), 0); err == nil {
		t.Fatal("malformed report accepted")
	}
	got, err := ParseJUnit([]byte(`<testsuite tests="0"/>`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ZeroTests || got.Tests != 0 {
		t.Fatalf("expected explicit zero-test report: %+v", got)
	}
}

func TestCoverageParsers(t *testing.T) {
	lcov, err := ParseCoverage([]byte("TN:\nSF:src/a.go\nDA:1,1\nDA:2,0\nLF:2\nLH:1\nend_of_record\n"), "lcov")
	if err != nil {
		t.Fatal(err)
	}
	if lcov.Files["src/a.go"].LinesFound != 2 || lcov.Files["src/a.go"].LinesHit != 1 || lcov.LineCoverage != 0.5 {
		t.Fatalf("bad LCOV parse: %+v", lcov)
	}
	jacoco, err := ParseCoverage([]byte(`<report><package name="x"><class name="x/A" sourcefilename="A.java"><lines><line nr="1" mi="1" ci="0"/><line nr="2" mi="0" ci="1"/></lines></class></package></report>`), "jacoco")
	if err != nil {
		t.Fatal(err)
	}
	if jacoco.Files["x/A.java"].LinesFound != 2 || jacoco.Files["x/A.java"].LinesHit != 1 {
		t.Fatalf("bad JaCoCo parse: %+v", jacoco)
	}
	cobertura, err := ParseCoverage([]byte(`<coverage><packages><package><classes><class filename="pkg/test.py"><lines><line number="1" hits="1"/><line number="2" hits="0"/></lines></class></classes></package></packages></coverage>`), "coverage.py")
	if err != nil {
		t.Fatal(err)
	}
	if cobertura.Files["pkg/test.py"].LinesFound != 2 || cobertura.Files["pkg/test.py"].LinesHit != 1 {
		t.Fatalf("bad coverage.py parse: %+v", cobertura)
	}
}

func TestCoveragePyJSONParserAndFormatSniffing(t *testing.T) {
	fixture := []byte(`{"meta":{"version":"7.0"},"files":{"pkg/test_api.py":{"executed_lines":[1,2],"missing_lines":[3],"summary":{"num_statements":3,"covered_lines":2,"missing_lines":1}}}}`)
	for _, format := range []string{"coverage.py", "coverage.py-json"} {
		got, err := ParseCoverage(fixture, format)
		if err != nil {
			t.Fatalf("parse coverage.py fixture as %s: %v", format, err)
		}
		if !got.Complete || got.Files["pkg/test_api.py"].LinesFound != 3 || got.Files["pkg/test_api.py"].LinesHit != 2 || got.LineCoverage != 2.0/3.0 {
			t.Fatalf("unexpected coverage.py JSON result for %s: %+v", format, got)
		}
	}
	malformed := []byte(`{"files":{"pkg/test_api.py":{"executed_lines":[1],"missing_lines":[2],"summary":{"num_statements":3,"covered_lines":2,"missing_lines":1}}}}`)
	if _, err := ParseCoverage(malformed, "coverage.py-json"); err == nil {
		t.Fatal("coverage.py JSON with mismatched line counts was accepted")
	}
}

func TestResolveWithinRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if _, err := ResolveWithin(root, "../outside"); err == nil {
		t.Fatal("parent traversal accepted")
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveWithin(root, filepath.Join("link", "secret.txt")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestReadReportIsBoundedAndContained(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.xml"), []byte("123456"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(root, "report.xml", 5); err == nil {
		t.Fatal("oversized report accepted")
	}
	if _, err := ReadReport(root, "../report.xml", 10); err == nil {
		t.Fatal("outside report accepted")
	}
	if _, err := ReadReportSet(root, []string{"report.xml", "missing.xml"}, 10); err == nil {
		t.Fatal("partial report set accepted")
	}
}
