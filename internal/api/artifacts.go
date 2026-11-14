package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/security"
)

type ArtifactInfo struct {
	ID        uuid.UUID `json:"id"`
	MediaType string    `json:"media_type"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	Sanitized bool      `json:"sanitized"`
	ExpiresAt string    `json:"expires_at"`
}

// Artifact retrieval errors. The service layer maps storage and metadata
// failures onto these so the HTTP layer can answer without disclosing
// whether an artifact exists in another tenant.
var (
	// ErrArtifactUnavailable marks an artifact the authenticated tenant may
	// not retrieve: absent, expired, unverified, or owned by another tenant.
	// It is answered with a tenant-neutral 404 so artifact existence is
	// never disclosed across tenants.
	ErrArtifactUnavailable = errors.New("artifact is unavailable to this tenant")
	// ErrArtifactStorageUnavailable marks a dependency that could not serve
	// the request. Artifact storage is optional for plan correctness
	// (LLD 1.3), so the API degrades with a retryable 503 instead of
	// pretending the artifact does not exist.
	ErrArtifactStorageUnavailable = errors.New("artifact storage is unavailable")
	// ErrArtifactTooLarge marks an artifact beyond the configured bound.
	ErrArtifactTooLarge = errors.New("artifact exceeds the configured size limit")
	// ErrArtifactExecutionUnavailable marks an execution that is not
	// available to the authenticated tenant.
	ErrArtifactExecutionUnavailable = errors.New("execution is unavailable to this tenant")
)

type ArtifactService interface {
	Upload(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, io.Reader) (ArtifactInfo, error)
	Open(context.Context, uuid.UUID, uuid.UUID) (ArtifactInfo, io.ReadCloser, error)
}

func uploadArtifact(opts Options, slots chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started, status := time.Now(), http.StatusInternalServerError
		defer func() {
			if opts.Metrics != nil {
				opts.Metrics.RecordAPI("/artifacts.upload", status, time.Since(started).Seconds())
			}
		}()
		if !artifactRole(w, r, security.RoleExecutor) {
			status = http.StatusForbidden
			return
		}
		if opts.Artifacts == nil {
			status = http.StatusServiceUnavailable
			writeError(w, r, &APIError{Status: http.StatusServiceUnavailable, Code: "ARTIFACT_STORAGE_UNAVAILABLE", Message: "artifact storage is unavailable", Retryable: true})
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-r.Context().Done():
			status = http.StatusRequestTimeout
			writeError(w, r, &APIError{Status: http.StatusRequestTimeout, Code: "ARTIFACT_UPLOAD_INTERRUPTED", Message: "artifact upload was interrupted", Retryable: true})
			return
		}
		maxBytes := opts.MaxArtifactBytes
		if maxBytes <= 0 {
			maxBytes = 16 << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		executionID, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			status = http.StatusBadRequest
			writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "INVALID_RESOURCE_ID", Message: "execution id must be a UUID"})
			return
		}
		principal, _ := security.PrincipalFromContext(r.Context())
		tenantID, err := uuid.Parse(principal.TenantID)
		if err != nil {
			status = http.StatusForbidden
			writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "INVALID_TENANT", Message: "tenant membership is invalid"})
			return
		}
		actorID, err := uuid.Parse(principal.UserID)
		if err != nil {
			status = http.StatusForbidden
			writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "INVALID_ACTOR", Message: "user membership is invalid"})
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !allowedArtifactMediaType(strings.ToLower(mediaType)) {
			status = http.StatusUnsupportedMediaType
			writeError(w, r, &APIError{Status: http.StatusUnsupportedMediaType, Code: "ARTIFACT_MEDIA_TYPE_UNSUPPORTED", Message: "only JSON and XML test reports are accepted"})
			return
		}
		info, err := opts.Artifacts.Upload(r.Context(), tenantID, actorID, executionID, mediaType, r.Body)
		if err != nil {
			status = http.StatusInternalServerError
			code, message := "ARTIFACT_UPLOAD_FAILED", "artifact could not be stored"
			var maxErr *http.MaxBytesError
			switch {
			case errors.As(err, &maxErr) || errors.Is(err, ErrArtifactTooLarge):
				status, code, message = http.StatusRequestEntityTooLarge, "ARTIFACT_TOO_LARGE", "artifact exceeds the configured size limit"
			case errors.Is(err, ErrArtifactExecutionUnavailable):
				status, code, message = http.StatusNotFound, "EXECUTION_NOT_FOUND", "execution is unavailable to this tenant"
			case errors.Is(err, ErrArtifactStorageUnavailable):
				status, code, message = http.StatusServiceUnavailable, "ARTIFACT_STORAGE_UNAVAILABLE", "artifact storage is unavailable"
			case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
				status, code, message = http.StatusRequestTimeout, "ARTIFACT_UPLOAD_INTERRUPTED", "artifact upload was interrupted"
			case strings.Contains(err.Error(), "size limit"):
				status, code, message = http.StatusRequestEntityTooLarge, "ARTIFACT_TOO_LARGE", "artifact exceeds the configured size limit"
			case strings.Contains(err.Error(), "execution"):
				status, code, message = http.StatusNotFound, "EXECUTION_NOT_FOUND", "execution is unavailable to this tenant"
			}
			writeError(w, r, &APIError{Status: status, Code: code, Message: message, Retryable: status >= 500 || status == http.StatusRequestTimeout})
			return
		}
		status = http.StatusCreated
		writeJSON(w, http.StatusCreated, info)
	}
}

func downloadArtifact(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started, status := time.Now(), http.StatusInternalServerError
		defer func() {
			if opts.Metrics != nil {
				opts.Metrics.RecordAPI("/artifacts.get", status, time.Since(started).Seconds())
			}
		}()
		if !artifactRole(w, r, security.RoleReader, security.RoleExecutor, security.RoleMaintainer, security.RoleAuditor, security.RoleAdmin) {
			status = http.StatusForbidden
			return
		}
		if opts.Artifacts == nil {
			status = http.StatusServiceUnavailable
			writeError(w, r, &APIError{Status: http.StatusServiceUnavailable, Code: "ARTIFACT_STORAGE_UNAVAILABLE", Message: "artifact storage is unavailable", Retryable: true})
			return
		}
		artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			status = http.StatusBadRequest
			writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "INVALID_RESOURCE_ID", Message: "artifact id must be a UUID"})
			return
		}
		principal, _ := security.PrincipalFromContext(r.Context())
		tenantID, err := uuid.Parse(principal.TenantID)
		if err != nil {
			status = http.StatusForbidden
			writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "INVALID_TENANT", Message: "tenant membership is invalid"})
			return
		}
		info, body, err := opts.Artifacts.Open(r.Context(), tenantID, artifactID)
		if err != nil {
			switch {
			case errors.Is(err, ErrArtifactStorageUnavailable):
				status = http.StatusServiceUnavailable
				writeError(w, r, &APIError{Status: status, Code: "ARTIFACT_STORAGE_UNAVAILABLE", Message: "artifact storage is unavailable", Retryable: true})
			case errors.Is(err, ErrArtifactUnavailable):
				status = http.StatusNotFound
				writeError(w, r, &APIError{Status: status, Code: "ARTIFACT_NOT_FOUND", Message: "artifact is unavailable to this tenant"})
			default:
				status = http.StatusInternalServerError
				writeError(w, r, toAPIError(err))
			}
			return
		}
		defer body.Close()
		status = http.StatusOK
		extension := ".json"
		if info.MediaType == "application/xml" || info.MediaType == "text/xml" {
			extension = ".xml"
		}
		w.Header().Set("Content-Type", info.MediaType)
		w.Header().Set("Content-Length", strconv.FormatInt(info.SizeBytes, 10))
		w.Header().Set("Content-Disposition", `attachment; filename="ivp-artifact-`+artifactID.String()+extension+`"`)
		w.Header().Set("ETag", `"`+info.SHA256+`"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, body); err != nil && opts.Metrics != nil {
			opts.Metrics.ArtifactStoreError("download")
		}
	}
}

func artifactRole(w http.ResponseWriter, r *http.Request, roles ...security.Role) bool {
	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, &APIError{Status: http.StatusUnauthorized, Code: "UNAUTHENTICATED", Message: "authentication required"})
		return false
	}
	if !security.HasRole(principal.Role, roles...) {
		writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "FORBIDDEN", Message: "the assigned tenant role does not allow this operation"})
		return false
	}
	return true
}

func allowedArtifactMediaType(mediaType string) bool {
	switch mediaType {
	case "application/json", "application/xml", "text/xml":
		return true
	default:
		return false
	}
}
