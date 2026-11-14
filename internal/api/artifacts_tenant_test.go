package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/artifact"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
	"github.com/ivp/ivp/internal/service"
	"github.com/jackc/pgx/v5"
)

// tenantArtifactRepository is an in-memory stand-in for the PostgreSQL
// artifacts table. It reproduces the tenant-scoped read the real repository
// performs (`SELECT ... FROM artifacts WHERE tenant_id=$1 AND id=$2`, see
// internal/persistence/artifacts.go:76-84) and the tenant-owned execution
// check performed on write (internal/persistence/artifacts.go:38-45).
type tenantArtifactRepository struct {
	rows       map[uuid.UUID]persistence.ArtifactMetadata
	executions map[uuid.UUID]uuid.UUID
}

func (r *tenantArtifactRepository) SaveArtifact(_ context.Context, _ uuid.UUID, row persistence.ArtifactMetadata) error {
	if owner, ok := r.executions[row.ExecutionID]; !ok || owner != row.TenantID {
		return errors.New("artifact execution does not belong to this tenant")
	}
	if r.rows == nil {
		r.rows = map[uuid.UUID]persistence.ArtifactMetadata{}
	}
	r.rows[row.ID] = row
	return nil
}

func (r *tenantArtifactRepository) GetArtifact(_ context.Context, tenantID, artifactID uuid.UUID) (persistence.ArtifactMetadata, error) {
	row, ok := r.rows[artifactID]
	if !ok || row.TenantID != tenantID {
		return persistence.ArtifactMetadata{}, pgx.ErrNoRows
	}
	return row, nil
}

// unavailableStore is an object store whose backend is down. It models an
// S3 or filesystem outage so the retrieval endpoint can be proven to degrade
// honestly instead of leaking or panicking.
type unavailableStore struct{ err error }

func (s unavailableStore) Put(context.Context, string, string, io.Reader, int64) (artifact.Metadata, error) {
	return artifact.Metadata{}, s.err
}

func (s unavailableStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, s.err
}

func (s unavailableStore) Delete(context.Context, string) error { return s.err }

// tenantAuthenticator authenticates every request as a member of the given
// tenant with the given role, so one router can serve several tenants.
func tenantAuthenticator(tenantID uuid.UUID, role security.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := security.Principal{Subject: "sub", TenantID: tenantID.String(), UserID: uuid.NewString(), Role: role}
			next.ServeHTTP(w, r.WithContext(security.WithPrincipal(r.Context(), p)))
		})
	}
}

// uploadThroughAPI drives the real upload handler for a tenant and returns
// the created artifact identifier.
func uploadThroughAPI(t *testing.T, h http.Handler, executionID uuid.UUID) uuid.UUID {
	t.Helper()
	report := `{"tests":[{"name":"suite.test","status":"passed"}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+executionID.String()+"/artifacts", strings.NewReader(report))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("artifact upload status=%d body=%s", response.Code, response.Body.String())
	}
	var info api.ArtifactInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode artifact info: %v", err)
	}
	if info.ID == uuid.Nil {
		t.Fatal("artifact upload returned no artifact id")
	}
	return info.ID
}

func decodeError(t *testing.T, response *httptest.ResponseRecorder) api.ErrorResponse {
	t.Helper()
	var body api.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response %q: %v", response.Body.String(), err)
	}
	return body
}

// newFileStore returns a real file-backed artifact store plus its root
// directory so a test can inspect the on-disk tenant namespaces.
func newFileStore(t *testing.T) (*artifact.FileStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := artifact.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

// storedObjects lists every stored object file below the store root.
func storedObjects(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store root: %v", err)
	}
	return files
}

// TestArtifactDownloadIsDeniedAcrossTenants proves that the artifact
// retrieval endpoint enforces tenant isolation: a tenant that knows another
// tenant's artifact id receives the same "unavailable" answer as for an
// unknown id, and no artifact bytes or metadata are disclosed.
func TestArtifactDownloadIsDeniedAcrossTenants(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	execution := uuid.New()
	repo := &tenantArtifactRepository{executions: map[uuid.UUID]uuid.UUID{execution: tenantA}}
	store, root := newFileStore(t)
	manager := &service.ArtifactManager{DB: repo, Store: store}

	owner := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: manager, Authenticator: tenantAuthenticator(tenantA, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	artifactID := uploadThroughAPI(t, owner, execution)

	// The owning tenant's own download succeeds, proving the artifact exists
	// and is reachable for its own tenant.
	ownRequest := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+artifactID.String(), nil)
	ownResponse := httptest.NewRecorder()
	owner.ServeHTTP(ownResponse, ownRequest)
	if ownResponse.Code != http.StatusOK {
		t.Fatalf("owner download status=%d body=%s", ownResponse.Code, ownResponse.Body.String())
	}
	if !strings.Contains(ownResponse.Body.String(), "suite.test") {
		t.Fatalf("owner download did not return the stored report: %s", ownResponse.Body.String())
	}

	// A second tenant that guesses the artifact id must be refused.
	other := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: manager, Authenticator: tenantAuthenticator(tenantB, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	crossRequest := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+artifactID.String(), nil)
	crossResponse := httptest.NewRecorder()
	other.ServeHTTP(crossResponse, crossRequest)
	if crossResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant download status=%d body=%s", crossResponse.Code, crossResponse.Body.String())
	}
	if strings.Contains(crossResponse.Body.String(), "suite.test") {
		t.Fatalf("cross-tenant download leaked artifact content: %s", crossResponse.Body.String())
	}
	if crossResponse.Header().Get("ETag") != "" || crossResponse.Header().Get("Content-Disposition") != "" || crossResponse.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("cross-tenant download disclosed artifact metadata headers: %v", crossResponse.Header())
	}
	crossBody := decodeError(t, crossResponse)
	if crossBody.Error.Code != "ARTIFACT_NOT_FOUND" {
		t.Fatalf("cross-tenant download error code = %q, want ARTIFACT_NOT_FOUND", crossBody.Error.Code)
	}

	// An unknown id must be indistinguishable from another tenant's id so
	// the endpoint cannot be used as an existence oracle.
	unknownRequest := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+uuid.NewString(), nil)
	unknownResponse := httptest.NewRecorder()
	other.ServeHTTP(unknownResponse, unknownRequest)
	if unknownResponse.Code != crossResponse.Code {
		t.Fatalf("unknown artifact status=%d, cross-tenant status=%d: responses must be indistinguishable", unknownResponse.Code, crossResponse.Code)
	}
	unknownBody := decodeError(t, unknownResponse)
	if unknownBody.Error.Code != crossBody.Error.Code || unknownBody.Error.Message != crossBody.Error.Message {
		t.Fatalf("unknown artifact error %+v differs from cross-tenant error %+v", unknownBody.Error, crossBody.Error)
	}

	// The owning tenant's object lives only under the owning tenant's
	// namespace on disk; the second tenant never gained a copy of it.
	owned := filepath.Join(root, tenantA.String(), artifactID.String())
	if _, err := os.Stat(owned); err != nil {
		t.Fatalf("owning tenant's object is missing at %s: %v", owned, err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, tenantB.String())); err == nil && len(entries) != 0 {
		t.Fatalf("second tenant's namespace holds %d objects", len(entries))
	}
	if files := storedObjects(t, root); len(files) != 1 {
		t.Fatalf("store holds %d objects, want 1: %v", len(files), files)
	}
}

// TestArtifactDownloadIgnoresClientSuppliedTenant proves that the tenant
// used for artifact scoping comes from the authenticated membership and not
// from anything the caller supplies in the request.
func TestArtifactDownloadIgnoresClientSuppliedTenant(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	execution := uuid.New()
	repo := &tenantArtifactRepository{executions: map[uuid.UUID]uuid.UUID{execution: tenantA}}
	store, _ := newFileStore(t)
	manager := &service.ArtifactManager{DB: repo, Store: store}

	owner := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: manager, Authenticator: tenantAuthenticator(tenantA, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	artifactID := uploadThroughAPI(t, owner, execution)

	other := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: manager, Authenticator: tenantAuthenticator(tenantB, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+artifactID.String()+"?tenant_id="+tenantA.String()+"&tenant="+tenantA.String(), nil)
	request.Header.Set("X-Tenant-ID", tenantA.String())
	response := httptest.NewRecorder()
	other.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("download with a spoofed tenant status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "suite.test") {
		t.Fatalf("download with a spoofed tenant leaked artifact content: %s", response.Body.String())
	}
}

// TestArtifactUploadRejectsAnotherTenantsExecution proves that a tenant
// cannot attach an artifact to an execution owned by a different tenant, and
// that the rejected upload leaves neither metadata nor a stored object.
func TestArtifactUploadRejectsAnotherTenantsExecution(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	execution := uuid.New()
	repo := &tenantArtifactRepository{executions: map[uuid.UUID]uuid.UUID{execution: tenantA}}
	store, root := newFileStore(t)
	manager := &service.ArtifactManager{DB: repo, Store: store}

	other := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: manager, Authenticator: tenantAuthenticator(tenantB, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+execution.String()+"/artifacts", strings.NewReader(`{"tests":[{"status":"passed"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	other.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant upload status=%d body=%s", response.Code, response.Body.String())
	}
	body := decodeError(t, response)
	if body.Error.Code != "EXECUTION_NOT_FOUND" {
		t.Fatalf("cross-tenant upload error code = %q, want EXECUTION_NOT_FOUND", body.Error.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("cross-tenant upload persisted %d artifact rows, want 0", len(repo.rows))
	}
	if files := storedObjects(t, root); len(files) != 0 {
		t.Fatalf("cross-tenant upload left %d stored objects, want 0: %v", len(files), files)
	}
}

// TestArtifactRetrievalDegradesHonestlyWhenStoreIsUnavailable proves that
// the retrieval endpoint reports an object store outage as a retryable
// unavailability instead of panicking, pretending the artifact is missing, or
// leaking content.
func TestArtifactRetrievalDegradesHonestlyWhenStoreIsUnavailable(t *testing.T) {
	tenant := uuid.New()
	execution := uuid.New()
	repo := &tenantArtifactRepository{executions: map[uuid.UUID]uuid.UUID{execution: tenant}}
	store, _ := newFileStore(t)
	owner := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: &service.ArtifactManager{DB: repo, Store: store}, Authenticator: tenantAuthenticator(tenant, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	artifactID := uploadThroughAPI(t, owner, execution)

	broken := api.NewRouter(api.Options{Backend: stubBackend{}, Artifacts: &service.ArtifactManager{DB: repo, Store: unavailableStore{err: artifact.ErrUnavailable}}, Authenticator: tenantAuthenticator(tenant, security.RoleExecutor), MaxBodyBytes: 1 << 20, MaxArtifactBytes: 1024})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+artifactID.String(), nil)
	response := httptest.NewRecorder()
	broken.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("store outage download status=%d body=%s", response.Code, response.Body.String())
	}
	body := decodeError(t, response)
	if body.Error.Code != "ARTIFACT_STORAGE_UNAVAILABLE" {
		t.Fatalf("store outage download error code = %q, want ARTIFACT_STORAGE_UNAVAILABLE", body.Error.Code)
	}
	if !body.Error.Retryable {
		t.Fatal("store outage download error is not marked retryable")
	}
	if strings.Contains(response.Body.String(), "suite.test") {
		t.Fatalf("store outage download leaked artifact content: %s", response.Body.String())
	}

	// A router without an artifact service must also fail closed with a
	// clear, retryable error rather than a panic or a silent empty body.
	absent := api.NewRouter(api.Options{Backend: stubBackend{}, Authenticator: tenantAuthenticator(tenant, security.RoleExecutor)})
	request = httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+artifactID.String(), nil)
	response = httptest.NewRecorder()
	absent.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("absent artifact service download status=%d body=%s", response.Code, response.Body.String())
	}
	absentBody := decodeError(t, response)
	if absentBody.Error.Code != "ARTIFACT_STORAGE_UNAVAILABLE" || !absentBody.Error.Retryable {
		t.Fatalf("absent artifact service error = %+v", absentBody.Error)
	}
}

// stubBackend satisfies api.Backend for routes that are not exercised here.
type stubBackend struct{}

func (stubBackend) Execute(context.Context, api.Action) (any, int, error) {
	return map[string]any{"ok": true}, http.StatusOK, nil
}

func (stubBackend) Ready(context.Context) error { return nil }
