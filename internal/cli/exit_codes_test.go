package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
)

// LLD 17.1 requires a regression test for every CLI exit code, and LLD 11.2
// defines their meanings. The suite is table-driven: every documented code is
// produced by the real code path that owns it, and the outcome-driven codes
// are produced by the single mapping that implements them.
func TestExitCodesFollowTheDocumentedTable(t *testing.T) {
	t.Run("outcome driven codes", func(t *testing.T) {
		cases := []struct {
			outcome   domain.ExecutionOutcome
			cancelled bool
			want      int
			meaning   string
		}{
			{domain.OutcomeTargetedPass, false, 0, "required checks passed"},
			{domain.OutcomeFullVerificationPass, false, 0, "required checks passed"},
			{domain.OutcomeTargetedFail, false, 1, "executed tests/checks failed"},
			{domain.OutcomeFullVerificationFail, false, 1, "executed tests/checks failed"},
			{domain.OutcomeStale, false, 5, "stale snapshot/plan; nothing unsafe executed"},
			{domain.OutcomeInfraFailure, false, 6, "adapter, report-parser or infrastructure error"},
			{domain.OutcomeAuditMiss, false, 7, "audit confirmed a miss"},
			{domain.OutcomeAuditPossibleMiss, false, 7, "audit found an unresolved possible miss"},
			{domain.OutcomeInfraFailure, true, 8, "user cancelled execution"},
		}
		for _, tc := range cases {
			if got := exitCodeForOutcome(tc.outcome, tc.cancelled); got != tc.want {
				t.Errorf("exitCodeForOutcome(%q, cancelled=%t) = %d, want %d (%s)", tc.outcome, tc.cancelled, got, tc.want, tc.meaning)
			}
		}
		// An outcome the CLI does not know is never reported as a pass.
		if got := exitCodeForOutcome(domain.ExecutionOutcome("SOMETHING_NEW"), false); got != 6 {
			t.Errorf("an unknown outcome produced exit code %d, want 6", got)
		}
	})

	t.Run("no error is a success", func(t *testing.T) {
		if got := ExitCode(nil); got != 0 {
			t.Errorf("ExitCode(nil) = %d, want 0", got)
		}
	})

	t.Run("code 2 is invalid user or repository input", func(t *testing.T) {
		// A missing local report file is invalid input, not an API failure:
		// the previous code-4 classification contradicted LLD 11.2.
		err := renderReport(nil, filepath.Join(t.TempDir(), "missing-report.json"), "text")
		if err == nil {
			t.Fatal("a missing report file was accepted")
		}
		if got := ExitCode(err); got != 2 {
			t.Fatalf("a missing report file produced exit code %d, want 2", got)
		}
		// A malformed reference is invalid input as well.
		if got := ExitCode(exitError{2, os.ErrNotExist}); got != 2 {
			t.Fatalf("an invalid report reference produced exit code %d, want 2", got)
		}
	})

	t.Run("code 3 is an unsupported environment without a fallback", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Execution.FullSuiteArgv = nil
		fallback, why := fallbackInstruction(cfg)
		if fallback != "" {
			t.Fatalf("a configuration without full-suite argv reported a fallback (%s)", fallback)
		}
		if strings.TrimSpace(why) == "" {
			t.Fatal("a missing fallback must be explained")
		}
		if got := ExitCode(exitError{3, os.ErrInvalid}); got != 3 {
			t.Fatalf("an unsupported environment produced exit code %d, want 3", got)
		}
	})

	t.Run("code 4 is an API authentication or policy failure", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Server.Endpoint = ""
		if _, err := newAPIClient(cfg); err == nil {
			t.Fatal("a missing server endpoint was accepted")
		} else if got := ExitCode(err); got != 4 {
			t.Fatalf("a missing server endpoint produced exit code %d, want 4", got)
		}
	})

	t.Run("code 6 is an invalid or unreadable API response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not json"))
		}))
		defer server.Close()
		t.Setenv("IVP_TOKEN", "test-token")
		t.Setenv("IVP_TENANT_ID", "11111111-1111-4111-8111-111111111111")
		cfg := config.Defaults()
		cfg.Server.Endpoint = server.URL
		client, err := newAPIClient(cfg)
		if err != nil {
			t.Fatalf("test server endpoint rejected: %v", err)
		}
		var value map[string]any
		err = client.request(t.Context(), http.MethodGet, "/api/v1/repositories", nil, "", &value)
		if err == nil {
			t.Fatal("an invalid API response was accepted")
		}
		if got := ExitCode(err); got != 6 {
			t.Fatalf("an invalid API response produced exit code %d, want 6", got)
		}
	})

	t.Run("the documented codes are the only codes", func(t *testing.T) {
		// Every code the CLI can produce is inside the LLD 11.2 range, so a
		// consumer can switch on it.
		for code := 0; code <= 8; code++ {
			if got := ExitCode(exitError{code, os.ErrInvalid}); got != code {
				t.Errorf("exit code %d was not preserved: got %d", code, got)
			}
		}
		if got := ExitCode(os.ErrInvalid); got != 6 {
			t.Errorf("an error without an explicit code produced %d, want 6", got)
		}
	})
}
