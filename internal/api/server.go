package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/audit"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
	"github.com/ivp/ivp/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Backend interface {
	Execute(context.Context, Action) (any, int, error)
	Ready(context.Context) error
}

type IdempotencyStore interface {
	RunIdempotent(context.Context, uuid.UUID, string, string, string, persistence.IdempotentOperation) (int, []byte, bool, error)
}

type Options struct {
	Backend            Backend
	Artifacts          ArtifactService
	Authenticator      func(http.Handler) http.Handler
	Metrics            *telemetry.Metrics
	Tracing            trace.Tracer
	Idempotency        IdempotencyStore
	MaxBodyBytes       int64
	MaxArtifactBytes   int64
	RateLimitPerSecond float64
	RateLimitBurst     int
}

type Action struct {
	Operation      string
	Route          string
	Principal      security.Principal
	ResourceID     string
	Scope          string
	Query          map[string][]string
	Payload        any
	RequestID      string
	IdempotencyKey string
}

type APIError struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
	Details   any
}

func (e *APIError) Error() string { return e.Message }

type ErrorResponse struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
	TraceID   string    `json:"trace_id,omitempty"`
}
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
}

type RepositoryRequest struct {
	Slug          string `json:"slug"`
	CanonicalURL  string `json:"canonical_url"`
	DefaultBranch string `json:"default_branch"`
}
type SnapshotRequest struct {
	Snapshot  domain.RepositorySnapshot    `json:"snapshot"`
	Nodes     []domain.GraphNode           `json:"nodes"`
	Edges     []domain.GraphEdge           `json:"edges"`
	TestUnits []domain.TestUnit            `json:"test_units"`
	Coverage  []domain.CoverageObservation `json:"coverage"`
}
type ClientCapabilities struct {
	OS              string            `json:"os"`
	Arch            string            `json:"arch"`
	Adapters        []string          `json:"adapters"`
	AdapterVersions map[string]string `json:"adapter_versions"`
}
type ExecutionContext struct {
	CI               bool   `json:"ci"`
	ToolchainDigest  string `json:"toolchain_digest"`
	EnvironmentClass string `json:"environment_class"`
}
type PlanRequest struct {
	RequestID          string               `json:"request_id"`
	SnapshotID         string               `json:"snapshot_id"`
	BaseSHA            string               `json:"base_sha"`
	HeadSHA            string               `json:"head_sha"`
	WorktreeHash       string               `json:"worktree_hash"`
	ChangedFiles       []domain.ChangedFile `json:"changed_files"`
	Mode               domain.PlanMode      `json:"mode"`
	ClientCapabilities ClientCapabilities   `json:"client_capabilities"`
	ExecutionContext   ExecutionContext     `json:"execution_context"`
}
type ExecutionStartRequest struct {
	WorktreeHash      string `json:"worktree_hash"`
	EnvironmentDigest string `json:"environment_digest"`
	FullSuite         bool   `json:"full_suite"`
}
type ExecutionSummary struct {
	Passed               int            `json:"passed"`
	Failed               int            `json:"failed"`
	Skipped              int            `json:"skipped"`
	Total                int            `json:"total"`
	FailureClasses       map[string]int `json:"failure_classes,omitempty"`
	AdapterVersion       string         `json:"adapter_version,omitempty"`
	ArgvTemplateIDs      []string       `json:"argv_template_ids,omitempty"`
	EscalationPerformed  bool           `json:"escalation_performed"`
	OutputHashes         []string       `json:"output_hashes,omitempty"`
	SanitizedExcerptRefs []string       `json:"sanitized_excerpt_refs,omitempty"`
}
type UnitResultSummary struct {
	FailureClass string `json:"failure_class,omitempty"`
	RetryCount   int    `json:"retry_count,omitempty"`
}
type ExecutionUnitResult struct {
	TestUnitKey        string            `json:"test_unit_key"`
	Attempt            int               `json:"attempt"`
	Status             string            `json:"status"`
	DurationMillis     int64             `json:"duration_ms"`
	FailureFingerprint string            `json:"failure_fingerprint,omitempty"`
	ReportHash         string            `json:"report_hash,omitempty"`
	Summary            UnitResultSummary `json:"summary"`
}
type ResultRequest struct {
	Outcome       domain.ExecutionOutcome `json:"outcome"`
	ExitCode      *int                    `json:"exit_code,omitempty"`
	CompletedAt   time.Time               `json:"completed_at"`
	ElapsedMillis int64                   `json:"elapsed_ms"`
	ComputeMillis int64                   `json:"compute_ms"`
	Summary       ExecutionSummary        `json:"summary"`
	UnitResults   []ExecutionUnitResult   `json:"unit_results,omitempty"`
}
type AuditRequest struct {
	FullExecutionID     string                  `json:"full_execution_id"`
	FailureOutputHash   string                  `json:"failure_output_hash,omitempty"`
	Adapter             string                  `json:"adapter"`
	RuleScope           string                  `json:"rule_scope"`
	SelectedOutcome     domain.ExecutionOutcome `json:"selected_outcome"`
	FullOutcome         domain.ExecutionOutcome `json:"full_outcome"`
	SelectedKeys        []string                `json:"selected_keys"`
	Failures            []audit.Failure         `json:"failures"`
	SelectedFingerprint audit.Fingerprint       `json:"selected_fingerprint"`
	FullFingerprint     audit.Fingerprint       `json:"full_fingerprint"`
}
type PolicyRequest struct {
	Policy     json.RawMessage `json:"policy"`
	PolicyHash string          `json:"policy_hash"`
}
type QuarantineRequest struct {
	ScopeKey   string     `json:"scope_key"`
	ReasonCode string     `json:"reason_code"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}
type AcknowledgeRequest struct {
	Reason string `json:"reason"`
}

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	shaPattern       = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	digestPattern    = regexp.MustCompile(`^(?:sha256:)?[0-9a-fA-F]{64}$`)
	versionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/-]{0,127}$`)
	unitKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9._:/@#-]{1,512}$`)
	classPattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,63}$`)
	refPattern       = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
	scopePattern     = regexp.MustCompile(`^[A-Za-z0-9._/*:-]{1,200}$`)
)

func NewRouter(opts Options) http.Handler {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 24 << 20
	}
	if opts.RateLimitPerSecond <= 0 {
		opts.RateLimitPerSecond = 25
	}
	if opts.RateLimitBurst <= 0 {
		opts.RateLimitBurst = 50
	}
	r := chi.NewRouter()
	limiter := newLimiter(opts.RateLimitPerSecond, opts.RateLimitBurst, 4096)
	r.Use(requestContextMiddleware)
	r.Use(rateLimitMiddleware(limiter))
	r.Use(bodyLimitMiddleware(opts.MaxBodyBytes))
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
	})
	r.Get("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if opts.Backend == nil {
			writeError(w, r, &APIError{Status: 503, Code: "NOT_READY", Message: "service is not ready", Retryable: true})
			return
		}
		if err := opts.Backend.Ready(r.Context()); err != nil {
			writeError(w, r, &APIError{Status: 503, Code: "NOT_READY", Message: "service dependencies are not ready", Retryable: true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if opts.Metrics != nil {
		r.Handle("/metrics", opts.Metrics.Handler())
	}
	// chi's Route callback keeps authentication out of liveness, readiness and
	// Prometheus exposition while requiring it on every tenant API route.
	r.Route("/api/v1", func(api chi.Router) {
		artifactUploadSlots := make(chan struct{}, 2)
		if opts.Authenticator != nil {
			api.Use(opts.Authenticator)
		} else {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					writeError(w, r, &APIError{Status: 503, Code: "AUTH_UNAVAILABLE", Message: "authentication is not configured", Retryable: false})
				})
			})
		}
		api.Get("/me", dispatch(opts, "identity.me", 200, "", nil, false))
		api.Post("/repositories", dispatch(opts, "repositories.create", 201, "", []security.Role{security.RoleMaintainer}, false))
		api.Get("/repositories", dispatch(opts, "repositories.list", 200, "", []security.Role{security.RoleReader}, false))
		api.Get("/repositories/{id}", dispatch(opts, "repositories.get", 200, "id", []security.Role{security.RoleReader}, false))
		api.Post("/repositories/{id}/snapshots", dispatch(opts, "snapshots.create", 201, "id", []security.Role{security.RoleExecutor}, true))
		api.Get("/repositories/{id}/policy", dispatch(opts, "policy.get", 200, "id", []security.Role{security.RoleReader, security.RoleExecutor}, false))
		api.Put("/repositories/{id}/policy", dispatch(opts, "policy.update", 200, "id", []security.Role{security.RoleMaintainer}, false))
		api.Post("/repositories/{id}/plans", dispatch(opts, "plans.create", 201, "id", []security.Role{security.RoleExecutor}, false))
		api.Get("/plans/{id}", dispatch(opts, "plans.get", 200, "id", []security.Role{security.RoleReader, security.RoleExecutor}, false))
		api.Post("/plans/{id}/executions", dispatch(opts, "executions.start", 201, "id", []security.Role{security.RoleExecutor}, true))
		api.Post("/executions/{id}/results", dispatch(opts, "executions.result", 200, "id", []security.Role{security.RoleExecutor}, true))
		api.Post("/executions/{id}/artifacts", uploadArtifact(opts, artifactUploadSlots))
		api.Get("/executions/{id}", dispatch(opts, "executions.get", 200, "id", []security.Role{security.RoleReader, security.RoleExecutor}, false))
		api.Get("/artifacts/{id}", downloadArtifact(opts))
		api.Post("/executions/{id}/audits", dispatch(opts, "audits.submit", 201, "id", []security.Role{security.RoleExecutor, security.RoleMaintainer, security.RoleAuditor}, true))
		api.Get("/repositories/{id}/metrics", dispatch(opts, "metrics.repository", 200, "id", []security.Role{security.RoleMaintainer, security.RoleAuditor}, false))
		api.Post("/repositories/{id}/quarantine", dispatch(opts, "quarantine.create", 201, "id", []security.Role{security.RoleMaintainer}, false))
		api.Post("/repositories/{id}/quarantine/{scope}/acknowledge", dispatch(opts, "quarantine.acknowledge", 200, "id", []security.Role{security.RoleMaintainer}, false))
	})
	return r
}

func dispatch(opts Options, operation string, defaultStatus int, idParam string, roles []security.Role, idempotent bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		status := http.StatusInternalServerError
		defer func() {
			if opts.Metrics != nil {
				opts.Metrics.RecordAPI(operation, status, time.Since(started).Seconds())
			}
		}()
		ctx := r.Context()
		if opts.Tracing != nil {
			ctx, span := opts.Tracing.Start(ctx, "ivp.api."+operation, trace.WithAttributes(telemetry.SafeAttributes(map[string]string{"ivp.operation": operation, "http.request.method": r.Method})...))
			defer span.End()
			r = r.WithContext(ctx)
		}
		principal, ok := security.PrincipalFromContext(r.Context())
		if !ok {
			status = http.StatusUnauthorized
			writeError(w, r, &APIError{Status: status, Code: "UNAUTHENTICATED", Message: "authentication required"})
			return
		}
		if len(roles) > 0 && !security.HasRole(principal.Role, roles...) {
			status = http.StatusForbidden
			writeError(w, r, &APIError{Status: status, Code: "FORBIDDEN", Message: "the assigned tenant role does not allow this operation"})
			return
		}
		resourceID := ""
		if idParam != "" {
			resourceID = chi.URLParam(r, idParam)
			if _, err := uuid.Parse(resourceID); err != nil {
				status = http.StatusBadRequest
				writeError(w, r, &APIError{Status: status, Code: "INVALID_RESOURCE_ID", Message: "resource id must be a UUID"})
				return
			}
		}
		scope := chi.URLParam(r, "scope")
		if scope != "" && !scopePattern.MatchString(scope) {
			status = http.StatusBadRequest
			writeError(w, r, &APIError{Status: status, Code: "INVALID_SCOPE", Message: "quarantine scope is invalid"})
			return
		}
		if operation == "repositories.list" || operation == "metrics.repository" {
			if err := validatePagination(operation, r.URL.Query()); err != nil {
				status = http.StatusBadRequest
				writeError(w, r, toAPIError(err))
				return
			}
		}
		var payload any
		var raw []byte
		if needsBody(operation) {
			var err error
			payload, raw, err = readPayload(r, opts.MaxBodyBytes, operation)
			if err != nil {
				status = errorStatus(err)
				writeError(w, r, toAPIError(err))
				return
			}
		}
		if snapshot, ok := payload.(SnapshotRequest); ok {
			repositoryID, _ := uuid.Parse(resourceID)
			if snapshot.Snapshot.RepositoryID != repositoryID {
				status = http.StatusBadRequest
				writeError(w, r, &APIError{Status: status, Code: "SNAPSHOT_REPOSITORY_MISMATCH", Message: "snapshot repository does not match the requested repository"})
				return
			}
		}
		if request, ok := payload.(AuditRequest); ok && !security.HasRole(principal.Role, security.RoleAuditor) {
			for _, failure := range request.Failures {
				if failure.ReviewedNotAttributable {
					status = http.StatusForbidden
					writeError(w, r, &APIError{Status: status, Code: "AUDITOR_REQUIRED", Message: "only an auditor may mark an omitted failure as reviewed and unrelated to the change"})
					return
				}
			}
		}
		if opts.Backend == nil {
			status = http.StatusServiceUnavailable
			writeError(w, r, &APIError{Status: status, Code: "BACKEND_UNAVAILABLE", Message: "API service is not configured", Retryable: true})
			return
		}
		action := Action{Operation: operation, Route: operation, Principal: principal, ResourceID: resourceID, Scope: scope, Query: r.URL.Query(), Payload: payload, RequestID: requestID(r.Context()), IdempotencyKey: r.Header.Get("Idempotency-Key")}
		var response any
		if idempotent {
			if action.IdempotencyKey == "" || len(action.IdempotencyKey) > 200 {
				status = http.StatusBadRequest
				writeError(w, r, &APIError{Status: status, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "a valid Idempotency-Key header is required"})
				return
			}
			if opts.Idempotency == nil {
				status = http.StatusServiceUnavailable
				writeError(w, r, &APIError{Status: status, Code: "IDEMPOTENCY_UNAVAILABLE", Message: "idempotent writes are unavailable", Retryable: true})
				return
			}
			tenantID, err := uuid.Parse(principal.TenantID)
			if err != nil {
				status = http.StatusForbidden
				writeError(w, r, &APIError{Status: status, Code: "INVALID_TENANT", Message: "tenant membership is invalid"})
				return
			}
			endpoint := operation + ":" + resourceID
			var replayed bool
			status, raw, replayed, err = opts.Idempotency.RunIdempotent(r.Context(), tenantID, endpoint, action.IdempotencyKey, persistence.HashRequest(raw), func() (int, []byte, error) {
				value, code, callErr := opts.Backend.Execute(r.Context(), action)
				if callErr != nil {
					return 0, nil, callErr
				}
				if code == 0 {
					code = defaultStatus
				}
				body, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					return 0, nil, marshalErr
				}
				body, marshalErr = sanitizeJSON(body)
				return code, body, marshalErr
			})
			if err != nil {
				apiErr := toAPIError(err)
				status = apiErr.Status
				writeError(w, r, apiErr)
				return
			}
			if !json.Valid(raw) {
				status = http.StatusInternalServerError
				writeError(w, r, &APIError{Status: status, Code: "INVALID_BACKEND_RESPONSE", Message: "API operation returned an invalid response"})
				return
			}
			w.Header().Set("Idempotency-Replayed", strconv.FormatBool(replayed))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(raw)
			return
		}
		response, code, callErr := opts.Backend.Execute(r.Context(), action)
		if callErr != nil {
			apiErr := toAPIError(callErr)
			status = apiErr.Status
			writeError(w, r, apiErr)
			return
		}
		status = code
		if status == 0 {
			status = defaultStatus
		}
		body, err := json.Marshal(response)
		if err != nil {
			status = http.StatusInternalServerError
			writeError(w, r, &APIError{Status: status, Code: "RESPONSE_ENCODING_FAILED", Message: "API operation could not be encoded"})
			return
		}
		body, err = sanitizeJSON(body)
		if err != nil {
			status = http.StatusInternalServerError
			writeError(w, r, &APIError{Status: status, Code: "RESPONSE_ENCODING_FAILED", Message: "API operation could not be encoded"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func needsBody(operation string) bool {
	switch operation {
	case "repositories.create", "snapshots.create", "policy.update", "plans.create", "executions.start", "executions.result", "audits.submit", "quarantine.create", "quarantine.acknowledge":
		return true
	default:
		return false
	}
}

func validAPIOOutcome(outcome domain.ExecutionOutcome) bool {
	switch outcome {
	case domain.OutcomeTargetedPass, domain.OutcomeFullVerificationPass, domain.OutcomeTargetedFail, domain.OutcomeFullVerificationFail, domain.OutcomeInfraFailure, domain.OutcomeStale:
		return true
	default:
		return false
	}
}

func validAuditExecutionOutcome(outcome domain.ExecutionOutcome) bool {
	switch outcome {
	case domain.OutcomeTargetedPass, domain.OutcomeFullVerificationPass, domain.OutcomeTargetedFail, domain.OutcomeFullVerificationFail:
		return true
	default:
		return false
	}
}

func validFullAuditOutcome(outcome domain.ExecutionOutcome) bool {
	return outcome == domain.OutcomeFullVerificationPass || outcome == domain.OutcomeFullVerificationFail
}

func validAuditFingerprint(f audit.Fingerprint) bool {
	return shaPattern.MatchString(f.SourceTreeHash) &&
		digestPattern.MatchString(f.DependencyDigest) &&
		digestPattern.MatchString(f.BuildConfigDigest) &&
		digestPattern.MatchString(f.ToolchainDigest) &&
		digestPattern.MatchString(f.TestConfigDigest) &&
		versionPattern.MatchString(f.EnvironmentClass) &&
		digestPattern.MatchString(f.RequiredServicesDigest)
}

func validUnitStatus(status string) bool {
	switch status {
	case "PASSED", "FAILED", "SKIPPED", "ERROR", "CANCELLED", "RETRIED":
		return true
	default:
		return false
	}
}

func validateSafeSummary(summary ExecutionSummary, units []ExecutionUnitResult) error {
	if summary.AdapterVersion != "" && !versionPattern.MatchString(summary.AdapterVersion) {
		return errors.New("adapter_version must be a short version identifier")
	}
	if len(summary.ArgvTemplateIDs) > 100 || len(summary.OutputHashes) > 100 || len(summary.SanitizedExcerptRefs) > 100 {
		return errors.New("result summary list exceeds the request limit")
	}
	for _, id := range summary.ArgvTemplateIDs {
		if !refPattern.MatchString(id) {
			return errors.New("argv_template_ids must contain opaque template identifiers")
		}
	}
	for _, hash := range summary.OutputHashes {
		if !digestPattern.MatchString(hash) {
			return errors.New("output_hashes must contain SHA-256 digests")
		}
	}
	for _, ref := range summary.SanitizedExcerptRefs {
		if !refPattern.MatchString(ref) {
			return errors.New("sanitized_excerpt_refs must contain opaque artifact references")
		}
	}
	for class, count := range summary.FailureClasses {
		if !classPattern.MatchString(class) || count < 0 {
			return errors.New("failure_classes contains invalid classifications")
		}
	}
	for _, unit := range units {
		if !unitKeyPattern.MatchString(unit.TestUnitKey) || (unit.FailureFingerprint != "" && !refPattern.MatchString(unit.FailureFingerprint)) || (unit.ReportHash != "" && !digestPattern.MatchString(unit.ReportHash)) || (unit.Summary.FailureClass != "" && !classPattern.MatchString(unit.Summary.FailureClass)) {
			return errors.New("unit result contains invalid identifiers or hashes")
		}
		if unit.Attempt > 100 || unit.Summary.RetryCount > 100 {
			return errors.New("unit result retry count is excessive")
		}
	}
	return nil
}

func readPayload(r *http.Request, max int64, operation string) (any, []byte, error) {
	if r.Body == nil {
		return nil, nil, validationError("REQUEST_BODY_REQUIRED", "request body is required")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, validationError("REQUEST_BODY_INVALID", "request body is invalid or too large")
	}
	if len(raw) == 0 {
		return nil, nil, validationError("REQUEST_BODY_REQUIRED", "request body is required")
	}
	if max > 0 && int64(len(raw)) > max {
		return nil, nil, validationError("REQUEST_BODY_TOO_LARGE", "request body exceeds the configured limit")
	}
	decode := func(out any) error {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(out); err != nil {
			return err
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return errors.New("multiple JSON values are not allowed")
			}
			return err
		}
		return nil
	}
	var value any
	switch operation {
	case "repositories.create":
		var v RepositoryRequest
		if err = decode(&v); err == nil && (v.Slug == "" || v.CanonicalURL == "") {
			err = errors.New("slug and canonical_url are required")
		}
		value = v
	case "snapshots.create":
		var v SnapshotRequest
		if err = decode(&v); err == nil {
			if v.Snapshot.ID == uuid.Nil || v.Snapshot.RepositoryID == uuid.Nil || v.Snapshot.CommitSHA == "" || v.Snapshot.TreeHash == "" {
				err = errors.New("snapshot metadata is incomplete")
			}
			if err == nil {
				if v.Snapshot.CommitSHA == "" {
					err = errors.New("commit_sha required")
				}
				for _, n := range v.Nodes {
					if n.Path != "" {
						if _, e := security.NormalizeRepoPath(n.Path); e != nil {
							err = fmt.Errorf("invalid graph node path: %w", e)
							break
						}
					}
					if unsafeMetadata(n.Attributes) {
						err = errors.New("graph attributes contain prohibited source, command, environment, or secret fields")
						break
					}
				}
				for _, u := range v.TestUnits {
					if unsafeMetadata(u.Metadata) {
						err = errors.New("test metadata contains prohibited source, command, environment, or secret fields")
						break
					}
				}
			}
		}
		value = v
	case "policy.update":
		var v PolicyRequest
		if err = decode(&v); err == nil {
			if len(v.Policy) == 0 || !json.Valid(v.Policy) || v.PolicyHash == "" {
				err = errors.New("policy and policy_hash are required")
			}
		}
		value = v
	case "plans.create":
		var v PlanRequest
		if err = decode(&v); err == nil {
			if !shaPattern.MatchString(v.BaseSHA) || !shaPattern.MatchString(v.HeadSHA) {
				err = errors.New("base_sha and head_sha must be full Git object ids")
			}
			if !digestPattern.MatchString(v.WorktreeHash) {
				err = errors.New("worktree_hash must be a SHA-256 digest")
			}
			if _, e := uuid.Parse(v.SnapshotID); e != nil {
				err = errors.New("snapshot_id must be a UUID")
			}
			switch v.Mode {
			case domain.ModeFull, domain.ModeObserve, domain.ModeFastFeedback, domain.ModeRiskBasedCI:
			default:
				err = errors.New("mode is not supported")
			}
			if v.RequestID != "" {
				if _, e := uuid.Parse(v.RequestID); e != nil {
					err = errors.New("request_id must be a UUID")
				}
			}
			if len(v.ChangedFiles) == 0 || len(v.ChangedFiles) > 1000 {
				err = errors.New("changed_files must contain between 1 and 1000 entries")
			}
			if len(v.ClientCapabilities.Adapters) > 7 || len(v.ClientCapabilities.AdapterVersions) > 7 {
				err = errors.New("client capabilities exceed supported adapter limits")
			}
			if v.ClientCapabilities.OS != "linux" && v.ClientCapabilities.OS != "darwin" && v.ClientCapabilities.OS != "windows" {
				err = errors.New("client operating system must be linux, darwin, or windows")
			}
			if v.ClientCapabilities.Arch != "amd64" && v.ClientCapabilities.Arch != "arm64" {
				err = errors.New("client architecture must be amd64 or arm64")
			}
			adapters := map[string]bool{"gradle": true, "maven": true, "bazel": true, "nx": true, "pytest": true, "jest": true, "generic": true}
			seenAdapters := map[string]bool{}
			for _, adapter := range v.ClientCapabilities.Adapters {
				if !adapters[adapter] || seenAdapters[adapter] {
					err = errors.New("client capabilities contain an unknown or duplicate adapter")
					break
				}
				seenAdapters[adapter] = true
			}
			for adapter, version := range v.ClientCapabilities.AdapterVersions {
				if !adapters[adapter] || !versionPattern.MatchString(version) {
					err = errors.New("adapter_versions contains an invalid adapter or version")
					break
				}
			}
			for i := range v.ChangedFiles {
				f := &v.ChangedFiles[i]
				if f.Path == "" {
					err = errors.New("changed file path is required")
					break
				}
				normalized, pathErr := security.NormalizeRepoPath(f.Path)
				if pathErr != nil {
					err = errors.New("changed file path is unsafe")
					break
				}
				f.Path = normalized
				if f.OldPath != "" {
					old, oldErr := security.NormalizeRepoPath(f.OldPath)
					if oldErr != nil {
						err = errors.New("renamed file old_path is unsafe")
						break
					}
					f.OldPath = old
				}
				if (f.OldHash != "" && !digestPattern.MatchString(f.OldHash)) || (f.NewHash != "" && !digestPattern.MatchString(f.NewHash)) {
					err = errors.New("changed file hashes must be SHA-256 digests")
					break
				}
				if changedErr := domain.ValidateChangedFile(*f); changedErr != nil {
					err = errors.New("changed file metadata is invalid")
					break
				}
			}
		}
		value = v
	case "executions.start":
		var v ExecutionStartRequest
		if err = decode(&v); err == nil && (v.EnvironmentDigest == "" || v.WorktreeHash == "") {
			err = errors.New("worktree_hash and environment_digest are required")
		}
		value = v
	case "executions.result":
		var v ResultRequest
		if err = decode(&v); err == nil {
			if !validAPIOOutcome(v.Outcome) || v.ElapsedMillis < 0 || v.ComputeMillis < 0 || v.Summary.Passed < 0 || v.Summary.Failed < 0 || v.Summary.Skipped < 0 || v.Summary.Total < 0 || v.Summary.Passed+v.Summary.Failed+v.Summary.Skipped > v.Summary.Total {
				err = errors.New("execution result counts or outcome are invalid")
			}
			if err == nil {
				err = validateSafeSummary(v.Summary, v.UnitResults)
			}
			if len(v.UnitResults) > 100000 {
				err = errors.New("unit_results exceeds the request limit")
			}
			for _, u := range v.UnitResults {
				if u.TestUnitKey == "" || u.Attempt < 1 || u.DurationMillis < 0 || !validUnitStatus(u.Status) || u.Summary.RetryCount < 0 {
					err = errors.New("unit result fields are invalid")
					break
				}
			}
		}
		if err == nil {
			summary, _ := json.Marshal(v.Summary)
			units := make([]persistence.UnitResult, len(v.UnitResults))
			for i, u := range v.UnitResults {
				unitSummary, _ := json.Marshal(u.Summary)
				units[i] = persistence.UnitResult{TestUnitKey: u.TestUnitKey, Attempt: u.Attempt, Status: u.Status, DurationMillis: u.DurationMillis, FailureFingerprint: u.FailureFingerprint, ReportHash: u.ReportHash, Summary: unitSummary}
			}
			value = persistence.ResultSubmission{Outcome: v.Outcome, ExitCode: v.ExitCode, CompletedAt: v.CompletedAt, ElapsedMillis: v.ElapsedMillis, ComputeMillis: v.ComputeMillis, Summary: summary, UnitResults: units}
		} else {
			value = v
		}
	case "audits.submit":
		var v AuditRequest
		if err = decode(&v); err == nil {
			if _, e := uuid.Parse(v.FullExecutionID); e != nil {
				err = errors.New("full_execution_id must be a UUID")
			}
			if v.FailureOutputHash != "" && !digestPattern.MatchString(v.FailureOutputHash) {
				err = errors.New("failure_output_hash must be a SHA-256 digest")
			}
			if !map[string]bool{"gradle": true, "maven": true, "bazel": true, "nx": true, "pytest": true, "jest": true, "generic": true}[v.Adapter] || v.RuleScope == "" || !scopePattern.MatchString(v.RuleScope) {
				err = errors.New("adapter and valid rule_scope are required")
			}
			if !validAuditExecutionOutcome(v.SelectedOutcome) || !validFullAuditOutcome(v.FullOutcome) {
				err = errors.New("selected_outcome and full_outcome must be completed verification outcomes")
			}
			if len(v.SelectedKeys) > 100000 || len(v.Failures) > 100000 {
				err = errors.New("audit unit lists exceed the request limit")
			}
			selectedSeen := map[string]bool{}
			for _, key := range v.SelectedKeys {
				if !unitKeyPattern.MatchString(key) {
					err = errors.New("selected_keys contains an invalid stable key")
					break
				}
				if selectedSeen[key] {
					err = errors.New("selected_keys contains a duplicate stable key")
					break
				}
				selectedSeen[key] = true
			}
			failureSeen := map[string]bool{}
			for _, failure := range v.Failures {
				if !unitKeyPattern.MatchString(failure.TestUnitKey) {
					err = errors.New("failures contains an invalid test unit key")
					break
				}
				if failureSeen[failure.TestUnitKey] {
					err = errors.New("failures contains a duplicate test unit key")
					break
				}
				if failure.ReviewedNotAttributable && failure.RelatedToChange {
					err = errors.New("reviewed unrelated failures cannot be marked as related to the change")
					break
				}
				failureSeen[failure.TestUnitKey] = true
			}
			if !validAuditFingerprint(v.SelectedFingerprint) || !validAuditFingerprint(v.FullFingerprint) {
				err = errors.New("selected and full fingerprints must be complete")
			}
		}
		value = v
	case "quarantine.create":
		var v QuarantineRequest
		if err = decode(&v); err == nil {
			if v.ScopeKey == "" || !scopePattern.MatchString(v.ScopeKey) || v.ReasonCode == "" {
				err = errors.New("valid scope_key and reason_code are required")
			}
		}
		value = v
	case "quarantine.acknowledge":
		var v AcknowledgeRequest
		if err = decode(&v); err == nil && v.Reason == "" {
			err = errors.New("remediation reason is required")
		}
		value = v
	default:
		return nil, nil, validationError("ROUTE_NOT_SUPPORTED", "request route is not supported")
	}
	if err != nil {
		return nil, nil, validationError("REQUEST_INVALID", err.Error())
	}
	return value, raw, nil
}

func validatePagination(operation string, query map[string][]string) error {
	limit := 50
	if operation == "metrics.repository" {
		limit = 30
	}
	if values := query["limit"]; len(values) > 0 {
		if len(values) != 1 {
			return validationError("PAGINATION_INVALID", "limit must appear once")
		}
		parsed, err := strconv.Atoi(values[0])
		max := 200
		if operation == "metrics.repository" {
			max = 100
		}
		if err != nil || parsed < 1 || parsed > max {
			return validationError("PAGINATION_INVALID", "limit is outside the allowed range")
		}
		limit = parsed
	}
	_ = limit
	key := "cursor"
	if operation == "metrics.repository" {
		key = "cursor"
	}
	if values := query[key]; len(values) > 0 {
		if len(values) != 1 || values[0] == "" || len(values[0]) > 128 {
			return validationError("PAGINATION_INVALID", "cursor is invalid")
		}
		if operation == "repositories.list" {
			if _, err := uuid.Parse(values[0]); err != nil {
				return validationError("PAGINATION_INVALID", "repository cursor must be a UUID")
			}
		} else {
			if _, err := time.Parse("2006-01-02", values[0]); err != nil {
				return validationError("PAGINATION_INVALID", "metrics cursor must be a YYYY-MM-DD date")
			}
		}
	}
	return nil
}

func unsafeMetadata(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return true
	}
	var decoded any
	if json.Unmarshal(encoded, &decoded) != nil {
		return true
	}
	return hasForbiddenKey(decoded)
}
func hasForbiddenKey(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
			switch normalized {
			case "source", "sourcecontent", "sourcecontents", "stdout", "stderr", "environment", "env", "command", "argv", "shell", "token", "secret", "password", "credential", "authorization", "privatekey", "apikey":
				return true
			}
			if hasForbiddenKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasForbiddenKey(child) {
				return true
			}
		}
	}
	return false
}

func sanitizeJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(sanitizeValue(value))
}
func sanitizeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || lower == "source" || lower == "stdout" || lower == "stderr" || lower == "environment" || lower == "command" || lower == "argv" {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = sanitizeValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeValue(item)
		}
		return out
	case string:
		return security.Redact(v, 16*1024)
	default:
		return value
	}
}

func validationError(code, message string) error {
	return &APIError{Status: http.StatusBadRequest, Code: code, Message: security.Redact(message, 300)}
}
func errorStatus(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusBadRequest
}
func toAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 0 {
			apiErr.Status = http.StatusInternalServerError
		}
		if apiErr.Code == "" {
			apiErr.Code = "INTERNAL_ERROR"
		}
		return apiErr
	}
	if errors.Is(err, persistence.ErrIdempotencyConflict) {
		return &APIError{Status: http.StatusConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was already used for a different request"}
	}
	return &APIError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "request could not be completed", Retryable: true}
}

func writeError(w http.ResponseWriter, r *http.Request, e *APIError) {
	status := e.Status
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	message := security.Redact(e.Message, 300)
	details := any(nil)
	if e.Details != nil {
		encoded, err := json.Marshal(e.Details)
		if err == nil {
			clean, err := sanitizeJSON(encoded)
			if err == nil {
				_ = json.Unmarshal(clean, &details)
			}
		}
	}
	response := ErrorResponse{Error: ErrorBody{Code: e.Code, Message: message, Retryable: e.Retryable, Details: details}, RequestID: requestID(r.Context())}
	if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.IsValid() {
		response.TraceID = sc.TraceID().String()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type requestIDKey struct{}

func requestContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if !requestIDPattern.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, requestIDKey{}, id)))
	})
}
func requestID(ctx context.Context) string { v, _ := ctx.Value(requestIDKey{}).(string); return v }
func bodyLimitMiddleware(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

type bucket struct {
	tokens float64
	last   time.Time
}
type limiter struct {
	mu      sync.Mutex
	entries map[string]bucket
	rate    float64
	burst   float64
	max     int
}

func newLimiter(rate float64, burst, max int) *limiter {
	return &limiter{entries: make(map[string]bucket), rate: rate, burst: float64(burst), max: max}
}
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= l.max {
			for k := range l.entries {
				delete(l.entries, k)
				break
			}
		}
		b = bucket{tokens: l.burst, last: now}
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		l.entries[key] = b
		return false
	}
	b.tokens--
	l.entries[key] = b
	return true
}
func rateLimitMiddleware(l *limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			if !l.allow(host, time.Now()) {
				writeError(w, r, &APIError{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "request rate limit exceeded", Retryable: true})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
