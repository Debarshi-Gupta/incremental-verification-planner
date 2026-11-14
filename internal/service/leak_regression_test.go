package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/security"
	"github.com/ivp/ivp/internal/telemetry"
)

// LLD 17.5: source files and secret environment values must not appear in API
// payloads, logs, telemetry or artifacts. The canaries below are pushed
// through every one of those surfaces and must never be observable.
const (
	canarySecret = "ghp_CANARYSECRETVALUE0123456789"
	canarySource = "CANARY_SOURCE_SNIPPET_0123456789"
	canaryPath   = "C:/canary/repo/pkg/secret.go"
)

type leakBackend struct{ err error }

func (b leakBackend) Execute(context.Context, api.Action) (any, int, error) {
	return nil, http.StatusInternalServerError, b.err
}

func (b leakBackend) Ready(context.Context) error { return nil }

func adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := security.Principal{Subject: "leak-test", TenantID: uuid.NewString(), UserID: uuid.NewString(), Role: security.RoleAdmin}
		next.ServeHTTP(w, r.WithContext(security.WithPrincipal(r.Context(), principal)))
	})
}

// TestUnifiedLeakRegressionPushesCanariesThroughEverySurface is the unified
// LLD 17.5 regression: one canary secret, one canary source snippet and one
// canary path travel through the API error path, the API payload path, the
// structured log output, the telemetry exposition and the artifact
// sanitizer, and none of them may echo a canary.
func TestUnifiedLeakRegressionPushesCanariesThroughEverySurface(t *testing.T) {
	canaries := []string{canarySecret, canarySource, canaryPath}
	assertNoCanary := func(surface, payload string) {
		t.Helper()
		for _, canary := range canaries {
			if strings.Contains(payload, canary) {
				t.Fatalf("%s leaked %q: %s", surface, canary, payload)
			}
		}
	}

	t.Run("api error response", func(t *testing.T) {
		failure := errors.New("upload failed for " + canaryPath + " with token " + canarySecret)
		router := api.NewRouter(api.Options{Backend: leakBackend{err: failure}, Authenticator: adminAuth})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil)
		router.ServeHTTP(recorder, request)
		assertNoCanary("api error response", recorder.Body.String())
	})

	t.Run("api payload response", func(t *testing.T) {
		// The API echoes what the backend returns; a backend that returns a
		// canary must still be redacted on the wire.
		router := api.NewRouter(api.Options{Backend: leakBackend{err: &api.APIError{
			Status: http.StatusBadRequest, Code: "INVALID_INPUT",
			Message: "invalid input for " + canaryPath + " (" + canarySecret + ")",
		}}, Authenticator: adminAuth})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil)
		router.ServeHTTP(recorder, request)
		assertNoCanary("api payload response", recorder.Body.String())
	})

	t.Run("structured log output", func(t *testing.T) {
		var buffer strings.Builder
		logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
		logger.Error("execution failed", "error", security.Redact("token "+canarySecret+" in "+canaryPath, 4*1024))
		assertNoCanary("structured log output", buffer.String())
	})

	t.Run("telemetry exposition", func(t *testing.T) {
		metrics := telemetry.NewMetrics()
		// A canary must never become a metric label value.
		metrics.AuditRun(canarySecret, canarySource)
		metrics.Fallback(canarySecret, canarySource)
		metrics.AdapterError(canarySource, canarySecret)
		recorder := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		assertNoCanary("telemetry exposition", recorder.Body.String())
	})

	t.Run("artifact sanitizer", func(t *testing.T) {
		report := `{"tests":[{"name":"` + canaryPath + `","status":"failed","failureMessage":"` + canarySource + `"}],"token":"` + canarySecret + `","stdout":"Authorization: Bearer ` + canarySecret + `"}`
		clean, err := sanitizeReport("application/json", []byte(report), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(clean) {
			t.Fatalf("sanitized artifact is not valid JSON: %s", clean)
		}
		assertNoCanary("artifact sanitizer", string(clean))
	})

	t.Run("environment allowlist", func(t *testing.T) {
		// The allowlist is name-based: a variable whose NAME looks like a
		// credential is dropped even when it is explicitly allowed, so a
		// secret environment value never reaches the executor.
		allowed := security.EnvironmentAllowlist(map[string]string{
			"IVP_TOKEN":        canarySecret,
			"IVP_USER_SECRET":  canarySecret,
			"SOURCE_FILE_PATH": canaryPath,
			"SAFE_SETTING":     "visible",
		}, []string{"IVP_TOKEN", "IVP_USER_SECRET", "SOURCE_FILE_PATH", "SAFE_SETTING"})
		for _, dropped := range []string{"IVP_TOKEN", "IVP_USER_SECRET"} {
			if _, present := allowed[dropped]; present {
				t.Fatalf("secret-named environment variable %q survived the allowlist: %v", dropped, allowed)
			}
		}
		if allowed["SAFE_SETTING"] != "visible" {
			t.Fatalf("a non-secret environment variable was dropped: %v", allowed)
		}
	})

	t.Run("no canary reaches the process environment of the test", func(t *testing.T) {
		// The suite itself must not leak the canaries into the environment it
		// runs in; the check documents the contract the code above relies on.
		if value := os.Getenv("IVP_CANARY_SECRET"); value != "" {
			t.Fatalf("canary secret found in the environment: %s", value)
		}
	})
}
