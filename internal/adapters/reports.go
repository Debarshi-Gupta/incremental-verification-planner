package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type LimitedBuffer struct {
	B        bytes.Buffer
	Limit    int64
	Overflow bool
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	if b.Limit <= 0 {
		b.Limit = DefaultReportLimit
	}
	remaining := b.Limit - int64(b.B.Len())
	if remaining <= 0 {
		b.Overflow = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.B.Write(p[:remaining])
		b.Overflow = true
		return len(p), nil
	}
	return b.B.Write(p)
}

// RunCapture runs a native discovery command without a shell and bounds both
// output streams. It is for metadata queries; test execution uses execution.Runner.
func RunCapture(ctx context.Context, root, executable string, args []string, limit int64) ([]byte, []byte, error) {
	if limit <= 0 {
		limit = DefaultReportLimit
	}
	cwd, err := ResolveWithin(root, ".")
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = cwd
	out, stderr := &LimitedBuffer{Limit: limit}, &LimitedBuffer{Limit: limit}
	cmd.Stdout, cmd.Stderr = out, stderr
	err = cmd.Run()
	if out.Overflow || stderr.Overflow {
		return out.B.Bytes(), stderr.B.Bytes(), fmt.Errorf("adapter discovery output exceeded %d bytes", limit)
	}
	return out.B.Bytes(), stderr.B.Bytes(), err
}

func ReadReport(root, report string, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultReportLimit
	}
	if strings.ContainsAny(report, "*?[") {
		return nil, errors.New("wildcard report path must be expanded")
	}
	p, err := ResolveWithin(root, report)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("report exceeds %d bytes", limit)
	}
	return data, nil
}

func ReadReportSet(root string, paths []string, limit int64) ([][]byte, error) {
	if limit <= 0 {
		limit = DefaultReportLimit
	}
	var all [][]byte
	var total int64
	seen := map[string]bool{}
	for _, pattern := range paths {
		if len(all) >= 256 {
			return nil, errors.New("report count exceeds 256")
		}
		if strings.ContainsAny(pattern, "*?[") {
			clean := filepath.Clean(pattern)
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return all, ErrInvalidPath
			}
			matches, err := filepath.Glob(filepath.Join(root, clean))
			if err != nil {
				return all, err
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("report pattern %q matched no files", pattern)
			}
			for _, name := range matches {
				rel, err := filepath.Rel(root, name)
				if err != nil {
					return all, err
				}
				key := filepath.Clean(rel)
				if seen[key] {
					continue
				}
				seen[key] = true
				if len(all) >= 256 {
					return nil, errors.New("report count exceeds 256")
				}
				if total >= limit {
					return nil, fmt.Errorf("report set exceeds %d bytes", limit)
				}
				data, err := ReadReport(root, rel, limit-total)
				if err != nil {
					return nil, err
				}
				total += int64(len(data))
				all = append(all, data)
			}
		} else {
			key := filepath.Clean(pattern)
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(all) >= 256 {
				return nil, errors.New("report count exceeds 256")
			}
			if total >= limit {
				return nil, fmt.Errorf("report set exceeds %d bytes", limit)
			}
			data, err := ReadReport(root, pattern, limit-total)
			if err != nil {
				return nil, err
			}
			total += int64(len(data))
			all = append(all, data)
		}
	}
	return all, nil
}

type junitSuites struct {
	XMLName  xml.Name
	Suites   []junitSuite `xml:"testsuite"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     float64      `xml:"time,attr"`
}
type junitSuite struct {
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     float64      `xml:"time,attr"`
	Cases    []junitCase  `xml:"testcase"`
	Suites   []junitSuite `xml:"testsuite"`
}
type junitCase struct {
	Name    string        `xml:"name,attr"`
	Class   string        `xml:"classname,attr"`
	Time    float64       `xml:"time,attr"`
	Failure *junitProblem `xml:"failure"`
	Error   *junitProblem `xml:"error"`
	Skipped *junitProblem `xml:"skipped"`
}
type junitProblem struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

func ParseJUnit(data []byte, exitCode int) (ParsedResults, error) {
	var root junitSuites
	if err := xml.Unmarshal(data, &root); err != nil {
		return ParsedResults{Complete: false, Reasons: []string{"malformed JUnit XML"}}, err
	}
	if root.XMLName.Local != "testsuite" && root.XMLName.Local != "testsuites" {
		return ParsedResults{Complete: false, Reasons: []string{"unexpected JUnit root element"}}, errors.New("unexpected JUnit XML root")
	}
	result := ParsedResults{Complete: true, HasReliableReport: true}
	if root.XMLName.Local == "testsuite" {
		var single junitSuite
		if err := xml.Unmarshal(data, &single); err != nil {
			return ParsedResults{Complete: false}, err
		}
		accumulateSuite(&result, single)
	} else {
		for _, suite := range root.Suites {
			accumulateSuite(&result, suite)
		}
		if len(root.Suites) == 0 {
			result.Tests, result.Failed, result.Errors, result.Skipped = root.Tests, root.Failures, root.Errors, root.Skipped
			result.Duration = seconds(root.Time)
		}
	}
	if result.Tests == 0 && exitCode == 0 {
		result.ZeroTests = true
		result.Reasons = append(result.Reasons, "report contains zero tests")
	}
	if exitCode != 0 && result.Failed+result.Errors == 0 {
		result.Errors++
		result.Reasons = append(result.Reasons, "process failed without a reported test failure")
	}
	result.Passed = result.Tests - result.Failed - result.Errors - result.Skipped
	if result.Passed < 0 {
		result.Passed = 0
	}
	return result, nil
}

func accumulateSuite(out *ParsedResults, s junitSuite) {
	if len(s.Cases) > 0 {
		for _, c := range s.Cases {
			status := "passed"
			msg := ""
			switch {
			case c.Failure != nil:
				status = "failed"
				msg = problemText(c.Failure)
				out.Failed++
			case c.Error != nil:
				status = "error"
				msg = problemText(c.Error)
				out.Errors++
			case c.Skipped != nil:
				status = "skipped"
				msg = problemText(c.Skipped)
				out.Skipped++
			default:
				out.Passed++
			}
			out.Cases = append(out.Cases, TestCase{Name: c.Name, ClassName: c.Class, Status: status, Duration: seconds(c.Time), Message: msg})
			out.Duration += seconds(c.Time)
			out.Tests++
		}
	} else if len(s.Suites) == 0 {
		out.Tests += s.Tests
		out.Failed += s.Failures
		out.Errors += s.Errors
		out.Skipped += s.Skipped
		out.Duration += seconds(s.Time)
		out.Passed += max(0, s.Tests-s.Failures-s.Errors-s.Skipped)
	}
	for _, nested := range s.Suites {
		accumulateSuite(out, nested)
	}
}
func problemText(p *junitProblem) string {
	if p == nil {
		return ""
	}
	if p.Message != "" {
		return p.Message
	}
	return strings.TrimSpace(p.Text)
}
func seconds(n float64) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n * float64(time.Second))
}

func ParseReports(ctx context.Context, req ParseRequest) (ParsedResults, error) {
	limit := req.MaxReportBytes
	if limit <= 0 {
		limit = DefaultReportLimit
	}
	reports, readErr := ReadReportSet(req.Root, req.ReportPaths, limit)
	if readErr != nil {
		return ParsedResults{Complete: false, HasReliableReport: false, Reasons: []string{"test report set could not be read completely"}}, readErr
	}
	combined := ParsedResults{Complete: true, HasReliableReport: true}
	parsedCount := 0
	for _, data := range reports {
		if ctx.Err() != nil {
			return ParsedResults{Complete: false, Reasons: []string{"parsing cancelled"}}, ctx.Err()
		}
		trim := bytes.TrimSpace(data)
		if len(trim) == 0 {
			continue
		}
		var parsed ParsedResults
		var parseErr error
		if trim[0] == '<' {
			parsed, parseErr = ParseJUnit(data, req.ExitCode)
		}
		if trim[0] == '{' {
			parsed, parseErr = parseJestJSON(data, req.ExitCode)
		}
		if parseErr != nil {
			return ParsedResults{Complete: false, Reasons: []string{"malformed or unsupported test report"}}, parseErr
		}
		if !parsed.Complete {
			continue
		}
		parsedCount++
		combined.Tests += parsed.Tests
		combined.Passed += parsed.Passed
		combined.Failed += parsed.Failed
		combined.Errors += parsed.Errors
		combined.Skipped += parsed.Skipped
		combined.Duration += parsed.Duration
		combined.Cases = append(combined.Cases, parsed.Cases...)
		combined.ZeroTests = combined.ZeroTests || parsed.ZeroTests
		combined.Reasons = append(combined.Reasons, parsed.Reasons...)
	}
	if data := bytes.TrimSpace(req.Stdout); len(data) > 0 {
		var parsed ParsedResults
		var err error
		if data[0] == '<' {
			parsed, err = ParseJUnit(data, req.ExitCode)
		} else if data[0] == '{' {
			parsed, err = parseJestJSON(data, req.ExitCode)
		}
		if err == nil && parsed.Complete {
			if parsedCount > 0 {
				return combined, nil
			}
			return parsed, nil
		}
	}
	if parsedCount > 0 {
		return combined, nil
	}
	return ParsedResults{Complete: false, HasReliableReport: false, Reasons: []string{"no supported readable test result report"}}, errors.New("no supported readable test result report")
}

type jestReport struct {
	NumTotal    int `json:"numTotalTests"`
	NumPassed   int `json:"numPassedTests"`
	NumFailed   int `json:"numFailedTests"`
	NumPending  int `json:"numPendingTests"`
	NumTodo     int `json:"numTodoTests"`
	TestResults []struct {
		Name             string `json:"name"`
		AssertionResults []struct {
			FullName        string   `json:"fullName"`
			Status          string   `json:"status"`
			Duration        float64  `json:"duration"`
			FailureMessages []string `json:"failureMessages"`
		} `json:"assertionResults"`
	} `json:"testResults"`
}

func parseJestJSON(data []byte, exitCode int) (ParsedResults, error) {
	var report jestReport
	if err := json.Unmarshal(data, &report); err != nil {
		return ParsedResults{}, err
	}
	if report.TestResults == nil {
		return ParsedResults{}, errors.New("not a Jest report")
	}
	out := ParsedResults{Tests: report.NumTotal, Passed: report.NumPassed, Failed: report.NumFailed, Skipped: report.NumPending + report.NumTodo, Complete: true, HasReliableReport: true}
	for _, suite := range report.TestResults {
		for _, c := range suite.AssertionResults {
			status := strings.ToLower(c.Status)
			if status == "pending" || status == "todo" {
				status = "skipped"
			}
			msg := strings.Join(c.FailureMessages, "\n")
			out.Cases = append(out.Cases, TestCase{Name: c.FullName, ClassName: suite.Name, Status: status, Duration: time.Duration(c.Duration) * time.Millisecond, Message: msg})
		}
	}
	if out.Tests == 0 {
		out.ZeroTests = true
		out.Reasons = append(out.Reasons, "report contains zero tests")
	}
	if exitCode != 0 && out.Failed == 0 {
		out.Errors++
		out.Reasons = append(out.Reasons, "process failed without a reported test failure")
	}
	return out, nil
}

func ParseCoverageFile(root, name, format string, limit int64) (CoverageResult, error) {
	data, err := ReadReport(root, name, limit)
	if err != nil {
		return CoverageResult{Format: format, Complete: false, Reasons: []string{err.Error()}}, err
	}
	result, err := ParseCoverage(data, format)
	normalizeCoveragePaths(root, &result)
	return result, err
}

func ParseCoverageReports(root string, paths []string, format string, limit int64) (CoverageResult, error) {
	data, err := ReadReportSet(root, paths, limit)
	if err != nil {
		return CoverageResult{Format: format, Complete: false, Reasons: []string{err.Error()}}, err
	}
	if len(data) == 0 {
		return CoverageResult{Format: format, Complete: false, Reasons: []string{"no readable coverage report"}}, errors.New("no readable coverage report")
	}
	out := CoverageResult{Format: format, Complete: true, Files: map[string]FileCoverage{}}
	for _, report := range data {
		current, parseErr := ParseCoverage(report, format)
		normalizeCoveragePaths(root, &current)
		if parseErr != nil {
			out.Complete = false
			out.Reasons = append(out.Reasons, parseErr.Error())
			continue
		}
		for path, cov := range current.Files {
			old := out.Files[path]
			old.LinesFound += cov.LinesFound
			old.LinesHit += cov.LinesHit
			out.Files[path] = old
		}
	}
	for _, cov := range out.Files {
		out.LinesFound += cov.LinesFound
		out.LinesHit += cov.LinesHit
	}
	if out.LinesFound > 0 {
		out.LineCoverage = float64(out.LinesHit) / float64(out.LinesFound)
	}
	if len(out.Files) == 0 {
		out.Complete = false
		if len(out.Reasons) == 0 {
			out.Reasons = append(out.Reasons, "coverage reports contained no repository source files")
		}
		return out, errors.New("coverage reports contained no repository source files")
	}
	if !out.Complete {
		return out, errors.New("one or more coverage reports were incomplete")
	}
	return out, nil
}

func normalizeCoveragePaths(root string, result *CoverageResult) {
	absRoot, err := ResolveWithin(root, ".")
	if err != nil {
		result.Complete = false
		result.Reasons = append(result.Reasons, "coverage repository root is invalid")
		return
	}
	normalized := make(map[string]FileCoverage, len(result.Files))
	for name, cov := range result.Files {
		name = strings.ReplaceAll(name, "\\", string(filepath.Separator))
		abs, err := ResolveWithin(absRoot, name)
		if err != nil {
			result.Complete = false
			result.Reasons = append(result.Reasons, "coverage path escaped repository root")
			continue
		}
		rel, err := filepath.Rel(absRoot, abs)
		if err != nil {
			result.Complete = false
			continue
		}
		normalized[filepath.ToSlash(rel)] = cov
	}
	result.Files = normalized
}

func ParseCoverage(data []byte, format string) (CoverageResult, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	switch f {
	case "lcov":
		return parseLCOV(data)
	case "coverage.py-json", "coverage-json":
		return parseCoveragePyJSON(data)
	case "coverage.py":
		if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '{' {
			return parseCoveragePyJSON(data)
		}
		return parseCoverageXML(data, f)
	case "jacoco", "cobertura", "xml":
		return parseCoverageXML(data, f)
	default:
		return CoverageResult{Format: f, Complete: false, Reasons: []string{"unsupported coverage format"}}, fmt.Errorf("unsupported coverage format %q", format)
	}
}

func parseCoveragePyJSON(data []byte) (CoverageResult, error) {
	const format = "coverage.py-json"
	type fileSummary struct {
		NumStatements int `json:"num_statements"`
		CoveredLines  int `json:"covered_lines"`
		MissingLines  int `json:"missing_lines"`
	}
	var report struct {
		Files map[string]struct {
			ExecutedLines []int        `json:"executed_lines"`
			MissingLines  []int        `json:"missing_lines"`
			Summary       *fileSummary `json:"summary"`
		} `json:"files"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&report); err != nil {
		return CoverageResult{Format: format, Complete: false, Reasons: []string{"invalid coverage.py JSON"}}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON has trailing data"}}, errors.New("coverage.py JSON has trailing data")
	}
	if len(report.Files) == 0 {
		return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON contains no files"}}, errors.New("coverage.py JSON contains no files")
	}
	out := CoverageResult{Format: format, Complete: true, Files: make(map[string]FileCoverage, len(report.Files))}
	for path, file := range report.Files {
		if strings.TrimSpace(path) == "" {
			return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON contains an empty file path"}}, errors.New("coverage.py JSON contains an empty file path")
		}
		found, hit := int64(len(file.ExecutedLines)+len(file.MissingLines)), int64(len(file.ExecutedLines))
		if file.Summary != nil {
			if file.Summary.NumStatements < 0 || file.Summary.CoveredLines < 0 || file.Summary.MissingLines < 0 || file.Summary.CoveredLines+file.Summary.MissingLines != file.Summary.NumStatements {
				return CoverageResult{Format: format, Complete: false, Reasons: []string{"inconsistent coverage.py JSON summary"}}, errors.New("inconsistent coverage.py JSON summary")
			}
			found, hit = int64(file.Summary.NumStatements), int64(file.Summary.CoveredLines)
			if len(file.ExecutedLines) > 0 || len(file.MissingLines) > 0 {
				if len(file.ExecutedLines)+len(file.MissingLines) != file.Summary.NumStatements || len(file.ExecutedLines) != file.Summary.CoveredLines || len(file.MissingLines) != file.Summary.MissingLines {
					return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON line lists do not match summary"}}, errors.New("coverage.py JSON line lists do not match summary")
				}
			}
		} else if len(file.ExecutedLines) == 0 && len(file.MissingLines) == 0 {
			return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON file has no line data"}}, errors.New("coverage.py JSON file has no line data")
		}
		lineSet := make(map[int]struct{}, len(file.ExecutedLines)+len(file.MissingLines))
		for _, line := range append(append([]int(nil), file.ExecutedLines...), file.MissingLines...) {
			if line <= 0 {
				return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON contains an invalid line number"}}, errors.New("coverage.py JSON contains an invalid line number")
			}
			if _, exists := lineSet[line]; exists {
				return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON contains a duplicate line number"}}, errors.New("coverage.py JSON contains a duplicate line number")
			}
			lineSet[line] = struct{}{}
		}
		if hit < 0 || found < 0 || hit > found {
			return CoverageResult{Format: format, Complete: false, Reasons: []string{"coverage.py JSON contains inconsistent counters"}}, errors.New("coverage.py JSON contains inconsistent counters")
		}
		out.Files[filepath.ToSlash(path)] = FileCoverage{LinesFound: found, LinesHit: hit}
		out.LinesFound += found
		out.LinesHit += hit
	}
	if out.LinesFound > 0 {
		out.LineCoverage = float64(out.LinesHit) / float64(out.LinesFound)
	}
	return out, nil
}

func parseLCOV(data []byte) (CoverageResult, error) {
	out := CoverageResult{Format: "lcov", Complete: true, Files: map[string]FileCoverage{}}
	var path string
	var found, hit, daFound, daHit int64
	var hasFound, hasHit bool
	flush := func() {
		if path != "" {
			fileFound, fileHit := daFound, daHit
			if hasFound {
				fileFound = found
			}
			if hasHit {
				fileHit = hit
			}
			old := out.Files[filepath.ToSlash(path)]
			old.LinesFound += fileFound
			old.LinesHit += fileHit
			out.Files[filepath.ToSlash(path)] = old
		}
		path = ""
		found = 0
		hit = 0
		daFound = 0
		daHit = 0
		hasFound = false
		hasHit = false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "SF:"):
			flush()
			path = strings.TrimPrefix(line, "SF:")
		case strings.HasPrefix(line, "LF:"):
			n, err := parseInt64(strings.TrimPrefix(line, "LF:"))
			if err != nil || n < 0 {
				return CoverageResult{Format: "lcov", Complete: false, Reasons: []string{"invalid LCOV LF counter"}}, errors.New("invalid LCOV LF counter")
			}
			found = n
			hasFound = true
		case strings.HasPrefix(line, "LH:"):
			n, err := parseInt64(strings.TrimPrefix(line, "LH:"))
			if err != nil || n < 0 {
				return CoverageResult{Format: "lcov", Complete: false, Reasons: []string{"invalid LCOV LH counter"}}, errors.New("invalid LCOV LH counter")
			}
			hit = n
			hasHit = true
		case strings.HasPrefix(line, "DA:"):
			fields := strings.Split(strings.TrimPrefix(line, "DA:"), ",")
			if len(fields) < 2 {
				return CoverageResult{Format: "lcov", Complete: false, Reasons: []string{"invalid LCOV DA record"}}, errors.New("invalid LCOV DA record")
			}
			if _, err := parseInt64(fields[0]); err != nil {
				return CoverageResult{Format: "lcov", Complete: false, Reasons: []string{"invalid LCOV DA line number"}}, err
			}
			n, err := parseInt64(fields[1])
			if err != nil || n < 0 {
				return CoverageResult{Format: "lcov", Complete: false, Reasons: []string{"invalid LCOV DA hit count"}}, errors.New("invalid LCOV DA hit count")
			}
			daFound++
			if n > 0 {
				daHit++
			}
		case line == "end_of_record":
			flush()
		}
	}
	flush()
	if len(out.Files) == 0 {
		out.Complete = false
		return out, errors.New("LCOV report contains no source files")
	}
	for _, v := range out.Files {
		if v.LinesFound < 0 || v.LinesHit < 0 || v.LinesHit > v.LinesFound {
			return CoverageResult{Format: "lcov", Complete: false, Reasons: []string{"inconsistent LCOV line counters"}}, errors.New("inconsistent LCOV line counters")
		}
		out.LinesFound += v.LinesFound
		out.LinesHit += v.LinesHit
	}
	if out.LinesFound > 0 {
		out.LineCoverage = float64(out.LinesHit) / float64(out.LinesFound)
	}
	return out, nil
}

func parseInt64(s string) (int64, error) { return strconv.ParseInt(strings.TrimSpace(s), 10, 64) }

func parseCoverageXML(data []byte, format string) (CoverageResult, error) {
	out := CoverageResult{Format: format, Complete: true, Files: map[string]FileCoverage{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var currentName string
	var packageName string
	var current FileCoverage
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return CoverageResult{Format: format, Complete: false}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "package" {
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						packageName = a.Value
					}
				}
			}
			if t.Name.Local == "class" || t.Name.Local == "file" {
				currentName = ""
				current = FileCoverage{}
				var fallback string
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "filename", "sourcefilename":
						currentName = a.Value
					case "name":
						fallback = a.Value
					}
				}
				if currentName == "" {
					currentName = fallback
				}
				if t.Name.Local == "class" && packageName != "" && !strings.ContainsAny(currentName, "/\\") {
					currentName = filepath.ToSlash(strings.ReplaceAll(packageName, ".", "/")) + "/" + currentName
				}
			}
			if t.Name.Local == "line" && currentName != "" {
				var missed, covered, hits, coveredInstr int64
				hasMissed, hasCovered, hasHits, hasMI, hasCI := false, false, false, false, false
				for _, a := range t.Attr {
					var n int64
					var parseErr error
					switch a.Name.Local {
					case "hits":
						n, parseErr = parseInt64(a.Value)
						hits = n
						hasHits = true
					case "missed":
						n, parseErr = parseInt64(a.Value)
						missed = n
						hasMissed = true
					case "covered":
						n, parseErr = parseInt64(a.Value)
						covered = n
						hasCovered = true
					case "mi":
						n, parseErr = parseInt64(a.Value)
						hasMI = true
					case "ci":
						n, parseErr = parseInt64(a.Value)
						coveredInstr = n
						hasCI = true
					}
					if parseErr != nil || n < 0 {
						return CoverageResult{Format: format, Complete: false, Reasons: []string{"invalid XML coverage counter"}}, errors.New("invalid XML coverage counter")
					}
				}
				switch {
				case hasMI || hasCI:
					if !hasMI || !hasCI {
						return CoverageResult{Format: format, Complete: false, Reasons: []string{"incomplete JaCoCo line counters"}}, errors.New("incomplete JaCoCo line counters")
					}
					current.LinesFound++
					if coveredInstr > 0 {
						current.LinesHit++
					}
				case hasMissed || hasCovered:
					if !hasMissed || !hasCovered {
						return CoverageResult{Format: format, Complete: false, Reasons: []string{"incomplete XML line counters"}}, errors.New("incomplete XML line counters")
					}
					current.LinesFound += missed + covered
					if covered > 0 {
						current.LinesHit++
					}
				case hasHits:
					current.LinesFound++
					if hits > 0 {
						current.LinesHit++
					}
				default:
					return CoverageResult{Format: format, Complete: false, Reasons: []string{"unsupported XML line counter"}}, errors.New("unsupported XML line counter")
				}
			}
		case xml.EndElement:
			if (t.Name.Local == "class" || t.Name.Local == "file") && currentName != "" {
				key := filepath.ToSlash(currentName)
				old := out.Files[key]
				old.LinesFound += current.LinesFound
				old.LinesHit += current.LinesHit
				out.Files[key] = old
				currentName = ""
				current = FileCoverage{}
			} else if t.Name.Local == "package" {
				packageName = ""
			}
		}
	}
	if len(out.Files) == 0 {
		out.Complete = false
		return out, errors.New("coverage XML contains no file line data")
	}
	for _, v := range out.Files {
		out.LinesFound += v.LinesFound
		out.LinesHit += v.LinesHit
	}
	if out.LinesFound > 0 {
		out.LineCoverage = float64(out.LinesHit) / float64(out.LinesFound)
	}
	return out, nil
}
