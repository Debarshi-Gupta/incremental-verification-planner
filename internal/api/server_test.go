package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
)

type fakeBackend struct {
	calls    int
	action   Action
	response any
	status   int
	err      error
}

func (f *fakeBackend) Execute(_ context.Context, a Action) (any, int, error) {
	f.calls++
	f.action = a
	return f.response, f.status, f.err
}
func (f *fakeBackend) Ready(context.Context) error { return nil }

type fakeIdempotencyStore struct {
	status   int
	response []byte
	replayed bool
}

type fakeArtifactService struct {
	uploaded []byte
	info     ArtifactInfo
	err      error
}

func (f *fakeArtifactService) Upload(_ context.Context, _, _, _ uuid.UUID, mediaType string, body io.Reader) (ArtifactInfo, error) {
	if f.err != nil {
		return ArtifactInfo{}, f.err
	}
	var err error
	f.uploaded, err = io.ReadAll(body)
	if err != nil {
		return ArtifactInfo{}, err
	}
	if f.info.ID == uuid.Nil {
		f.info.ID = uuid.New()
	}
	f.info.MediaType = mediaType
	f.info.SizeBytes = int64(len(f.uploaded))
	f.info.SHA256 = strings.Repeat("a", 64)
	f.info.Sanitized = true
	f.info.ExpiresAt = "2026-12-01T00:00:00Z"
	return f.info, nil
}

func (f *fakeArtifactService) Open(context.Context, uuid.UUID, uuid.UUID) (ArtifactInfo, io.ReadCloser, error) {
	if f.err != nil {
		return ArtifactInfo{}, nil, f.err
	}
	return f.info, io.NopCloser(strings.NewReader(string(f.uploaded))), nil
}

func (f *fakeIdempotencyStore) RunIdempotent(_ context.Context, _ uuid.UUID, _, _, _ string, _ persistence.IdempotentOperation) (int, []byte, bool, error) {
	return f.status, f.response, f.replayed, nil
}

func fakeAuth(role security.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := security.Principal{Subject: "sub", TenantID: uuid.NewString(), UserID: uuid.NewString(), Role: role}
			next.ServeHTTP(w, r.WithContext(security.WithPrincipal(r.Context(), p)))
		})
	}
}

func TestRouterRoleChecksBeforeBackend(t *testing.T) {
	backend := &fakeBackend{response: map[string]string{"ok": "yes"}}
	h := NewRouter(Options{Backend: backend, Authenticator: fakeAuth(security.RoleReader)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/00000000-0000-4000-8000-000000000001/plans", strings.NewReader(`{"snapshot_id":"00000000-0000-4000-8000-000000000002","base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","mode":"FAST_FEEDBACK","changed_files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden || backend.calls != 0 {
		t.Fatalf("code=%d backend calls=%d body=%s", resp.Code, backend.calls, resp.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "FORBIDDEN" || body.RequestID == "" {
		t.Fatalf("unexpected error body: %+v", body)
	}
}

func TestIdempotentReplayPreservesStoredHTTPStatus(t *testing.T) {
	backend := &fakeBackend{}
	store := &fakeIdempotencyStore{status: http.StatusCreated, response: []byte(`{"execution_id":"00000000-0000-4000-8000-000000000003"}`), replayed: true}
	h := NewRouter(Options{Backend: backend, Authenticator: fakeAuth(security.RoleExecutor), Idempotency: store})
	body := `{"worktree_hash":"sha256:` + strings.Repeat("a", 64) + `","environment_digest":"sha256:` + strings.Repeat("b", 64) + `","full_suite":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plans/00000000-0000-4000-8000-000000000001/executions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "run-123")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated || resp.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay status=%d replay-header=%q body=%s", resp.Code, resp.Header().Get("Idempotency-Replayed"), resp.Body.String())
	}
	if backend.calls != 0 {
		t.Fatalf("replayed request called backend %d times", backend.calls)
	}
}

func TestPlanRequestRejectsUnsafeChangedPathAndUnknownField(t *testing.T) {
	unsafe := `{"snapshot_id":"00000000-0000-4000-8000-000000000002","base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","mode":"FAST_FEEDBACK","changed_files":[{"path":"../secret","status":"MODIFIED"}]}`
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(unsafe)), 1<<20, "plans.create"); err == nil {
		t.Fatal("unsafe changed path accepted")
	}
	unknown := `{"slug":"x","canonical_url":"https://example.test/repo","shell_command":"rm -rf"}`
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(unknown)), 1<<20, "repositories.create"); err == nil {
		t.Fatal("unknown shell command field accepted")
	}
}

func TestSnapshotRejectsSourceContentMetadata(t *testing.T) {
	body := `{"snapshot":{"id":"00000000-0000-4000-8000-000000000001","repository_id":"00000000-0000-4000-8000-000000000002","commit_sha":"abc","tree_hash":"tree"},"nodes":[{"stable_key":"file:a","node_type":"SOURCE_FILE","path":"a.go","attributes":{"source_contents":"package secret"}}]}`
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), 1<<20, "snapshots.create"); err == nil {
		t.Fatal("source content metadata accepted")
	}
}

func TestResultSummaryRejectsFreeFormText(t *testing.T) {
	unsafe := `{"outcome":"TARGETED_PASS","completed_at":"2026-10-27T00:00:00Z","summary":{"adapter_version":"v1 Authorization: Bearer abcdefghijkl"}}`
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(unsafe)), 1<<20, "executions.result"); err == nil {
		t.Fatal("free-form result text was accepted")
	}
	good := `{"outcome":"TARGETED_PASS","completed_at":"2026-10-27T00:00:00Z","summary":{"passed":1,"total":1,"adapter_version":"pytest-8.2"}}`
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(good)), 1<<20, "executions.result"); err != nil {
		t.Fatalf("safe structured summary rejected: %v", err)
	}
}

func TestAuditRequestRequiresClassifierEvidence(t *testing.T) {
	fingerprint := audit.Fingerprint{
		SourceTreeHash: strings.Repeat("a", 40), DependencyDigest: strings.Repeat("b", 64),
		BuildConfigDigest: strings.Repeat("c", 64), ToolchainDigest: strings.Repeat("d", 64),
		TestConfigDigest: strings.Repeat("e", 64), EnvironmentClass: "linux-ci", RequiredServicesDigest: strings.Repeat("f", 64),
	}
	payload := AuditRequest{
		FullExecutionID: "00000000-0000-4000-8000-000000000004", Adapter: "pytest", RuleScope: "unit",
		SelectedOutcome: domain.OutcomeTargetedPass, FullOutcome: domain.OutcomeFullVerificationFail,
		SelectedKeys: []string{"suite::selected"}, Failures: []audit.Failure{{TestUnitKey: "suite::omitted", Reproduced: true, RelatedToChange: true}},
		SelectedFingerprint: fingerprint, FullFingerprint: fingerprint,
	}
	good, _ := json.Marshal(payload)
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(good))), 1<<20, "audits.submit"); err != nil {
		t.Fatalf("complete audit classifier evidence rejected: %v", err)
	}
	missing := `{"full_execution_id":"00000000-0000-4000-8000-000000000004"}`
	if _, _, err := readPayload(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(missing)), 1<<20, "audits.submit"); err == nil {
		t.Fatal("audit request without comparison evidence was accepted")
	}
}

func TestHealthReadyDoesNotRequireAuth(t *testing.T) {
	h := NewRouter(Options{Backend: &fakeBackend{}})
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("readiness status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestArtifactUploadAndTenantScopedDownloadRoutes(t *testing.T) {
	artifacts := &fakeArtifactService{}
	executionID, artifactID := uuid.New(), uuid.New()
	artifacts.info.ID = artifactID
	h := NewRouter(Options{Backend: &fakeBackend{}, Artifacts: artifacts, Authenticator: fakeAuth(security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+executionID.String()+"/artifacts", strings.NewReader(`{"tests":[{"status":"passed"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("artifact upload status=%d body=%s", response.Code, response.Body.String())
	}
	if string(artifacts.uploaded) != `{"tests":[{"status":"passed"}]}` {
		t.Fatalf("upload service received unexpected content %q", artifacts.uploaded)
	}
	h = NewRouter(Options{Backend: &fakeBackend{}, Artifacts: artifacts, Authenticator: fakeAuth(security.RoleReader)})
	request = httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+artifactID.String(), nil)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"tests":[{"status":"passed"}]}` || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("artifact download status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	readerRouter := NewRouter(Options{Backend: &fakeBackend{}, Artifacts: artifacts, Authenticator: fakeAuth(security.RoleReader)})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+executionID.String()+"/artifacts", strings.NewReader(`{"tests":[{"status":"passed"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	readerRouter.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("reader upload status=%d body=%s", response.Code, response.Body.String())
	}
	largeRouter := NewRouter(Options{Backend: &fakeBackend{}, Artifacts: artifacts, Authenticator: fakeAuth(security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 4})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+executionID.String()+"/artifacts", strings.NewReader(`{"tests":"large"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	largeRouter.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized artifact status=%d body=%s", response.Code, response.Body.String())
	}
}
