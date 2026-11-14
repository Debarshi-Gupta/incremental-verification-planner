package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
)

// workflowBackend captures the aggregate result submission that the API layer
// forwards to the application service. The database is not part of this
// workflow: the test asserts the API contract and the artifact sanitization.
type workflowBackend struct {
	results []persistence.ResultSubmission
}

func (b *workflowBackend) Execute(_ context.Context, action api.Action) (any, int, error) {
	switch action.Operation {
	case "executions.result":
		submission, ok := action.Payload.(persistence.ResultSubmission)
		if !ok {
			return nil, 0, &api.APIError{Status: http.StatusBadRequest, Code: "REQUEST_INVALID", Message: "unexpected result payload"}
		}
		b.results = append(b.results, submission)
		return map[string]any{"execution_id": action.ResourceID, "outcome": submission.Outcome, "accepted": true}, http.StatusOK, nil
	default:
		return nil, 0, &api.APIError{Status: http.StatusNotFound, Code: "ROUTE_NOT_SUPPORTED", Message: "route is not supported"}
	}
}

func (b *workflowBackend) Ready(context.Context) error { return nil }

// workflowIdempotency executes the operation directly and reports it as a
// first, non-replayed write.
type workflowIdempotency struct{}

func (workflowIdempotency) RunIdempotent(_ context.Context, _ uuid.UUID, _, _, _ string, operation persistence.IdempotentOperation) (int, []byte, bool, error) {
	status, body, err := operation()
	return status, body, false, err
}

func workflowAuthenticator(tenant string, role security.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := security.Principal{Subject: "sub", TenantID: tenant, UserID: uuid.NewString(), Role: role}
			next.ServeHTTP(w, r.WithContext(security.WithPrincipal(r.Context(), principal)))
		})
	}
}

// TestEndToEndReportParsingResultSubmissionAndArtifactUpload runs the complete
// report workflow: a native test report is parsed, the aggregate result is
// submitted through the API, and the sanitized report artifact is uploaded
// through the API into the artifact service (LLD 3.2).
func TestEndToEndReportParsingResultSubmissionAndArtifactUpload(t *testing.T) {
	root := t.TempDir()
	report := `<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="unit" tests="2" failures="1" errors="0" time="0.03">
  <testcase name="test_ok" classname="pkg.test" time="0.01"/>
  <testcase name="test_bad" classname="pkg.test" time="0.02"><failure message="assertion failed">secret stack trace C:\repo\pkg\test.py</failure></testcase>
</testsuite>`
	if err := os.WriteFile(filepath.Join(root, "report.xml"), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	// Step 1: parse the native report into the canonical result model.
	parsed, err := adapters.ParseReports(context.Background(), adapters.ParseRequest{Root: root, ReportPaths: []string{"report.xml"}, ExitCode: 1})
	if err != nil {
		t.Fatalf("the native report must parse: %v", err)
	}
	if !parsed.Complete || !parsed.HasReliableReport || parsed.Tests != 2 || parsed.Failed != 1 || parsed.Passed != 1 {
		t.Fatalf("unexpected parsed report: %+v", parsed)
	}
	// Step 2: aggregate the parsed cases into the result submission.
	unitResults := make([]api.ExecutionUnitResult, 0, len(parsed.Cases))
	for _, testCase := range parsed.Cases {
		status := "PASSED"
		if strings.EqualFold(testCase.Status, "failed") {
			status = "FAILED"
		}
		unitResults = append(unitResults, api.ExecutionUnitResult{TestUnitKey: testCase.ClassName + "::" + testCase.Name,
			Attempt: 1, Status: status, DurationMillis: testCase.Duration.Milliseconds(),
			FailureFingerprint: strings.Repeat("a", 40), Summary: api.UnitResultSummary{FailureClass: "FAILED"}})
	}
	summary := api.ExecutionSummary{Passed: parsed.Passed, Failed: parsed.Failed, Skipped: parsed.Skipped, Total: parsed.Tests,
		FailureClasses: map[string]int{"FAILED": parsed.Failed}, AdapterVersion: "pytest-8.2"}
	payload := api.ResultRequest{Outcome: domain.OutcomeTargetedFail, CompletedAt: time.Now().UTC(), ElapsedMillis: 30, ComputeMillis: 30,
		Summary: summary, UnitResults: unitResults}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	backend := &workflowBackend{}
	repository := &memoryArtifactRepository{}
	store := &memoryArtifactStore{}
	manager := &ArtifactManager{DB: repository, Store: store, MaxBytes: 4096, Retention: time.Hour}
	tenant := uuid.NewString()
	router := api.NewRouter(api.Options{Backend: backend, Artifacts: manager, Authenticator: workflowAuthenticator(tenant, security.RoleExecutor),
		Idempotency: workflowIdempotency{}, MaxBodyBytes: 1 << 20, MaxArtifactBytes: 4096})
	executionID := uuid.New()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+executionID.String()+"/results", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "result-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("the aggregate result submission failed: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(backend.results) != 1 {
		t.Fatalf("the service must receive exactly one aggregate result: %d", len(backend.results))
	}
	submission := backend.results[0]
	if submission.Outcome != domain.OutcomeTargetedFail {
		t.Fatalf("the submitted outcome was not preserved: %s", submission.Outcome)
	}
	if len(submission.UnitResults) != 2 {
		t.Fatalf("the aggregate result must carry every parsed test case: %+v", submission.UnitResults)
	}
	var submittedSummary map[string]any
	if err := json.Unmarshal(submission.Summary, &submittedSummary); err != nil {
		t.Fatal(err)
	}
	if submittedSummary["total"] != float64(2) || submittedSummary["failed"] != float64(1) || submittedSummary["passed"] != float64(1) {
		t.Fatalf("the aggregate summary was not submitted: %+v", submittedSummary)
	}

	// Step 3: upload the sanitized report artifact for the same execution.
	artifactRequest := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+executionID.String()+"/artifacts", strings.NewReader(report))
	artifactRequest.Header.Set("Content-Type", "application/xml")
	artifactResponse := httptest.NewRecorder()
	router.ServeHTTP(artifactResponse, artifactRequest)
	if artifactResponse.Code != http.StatusCreated {
		t.Fatalf("the artifact upload failed: status=%d body=%s", artifactResponse.Code, artifactResponse.Body.String())
	}
	var info api.ArtifactInfo
	if err := json.Unmarshal(artifactResponse.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if !info.Sanitized || info.SizeBytes <= 0 {
		t.Fatalf("the uploaded artifact must be sanitized and non-empty: %+v", info)
	}
	row, ok := repository.rows[info.ID]
	if !ok {
		t.Fatal("the artifact metadata was not stored")
	}
	if row.ExecutionID != executionID {
		t.Fatalf("the artifact must belong to the execution: %+v", row)
	}
	stored := string(store.objects[row.ObjectKey])
	for _, forbidden := range []string{"secret stack trace", `C:\repo`, "assertion failed"} {
		if strings.Contains(stored, forbidden) {
			t.Fatalf("the stored artifact still contains %q: %s", forbidden, stored)
		}
	}
	// Step 4: the sanitized artifact is downloadable by the same tenant.
	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+info.ID.String(), nil)
	downloadResponse := httptest.NewRecorder()
	router.ServeHTTP(downloadResponse, downloadRequest)
	if downloadResponse.Code != http.StatusOK {
		t.Fatalf("the sanitized artifact download failed: status=%d body=%s", downloadResponse.Code, downloadResponse.Body.String())
	}
	if downloadResponse.Body.String() != stored {
		t.Fatalf("the downloaded artifact differs from the stored one: %q", downloadResponse.Body.String())
	}
}
