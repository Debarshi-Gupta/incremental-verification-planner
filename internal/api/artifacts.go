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
			if errors.As(err, &maxErr) || strings.Contains(err.Error(), "size limit") {
				status, code, message = http.StatusRequestEntityTooLarge, "ARTIFACT_TOO_LARGE", "artifact exceeds the configured size limit"
			} else if strings.Contains(err.Error(), "execution") {
				status, code, message = http.StatusNotFound, "EXECUTION_NOT_FOUND", "execution is unavailable to this tenant"
			} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status, code, message = http.StatusRequestTimeout, "ARTIFACT_UPLOAD_INTERRUPTED", "artifact upload was interrupted"
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
			status = http.StatusNotFound
			writeError(w, r, &APIError{Status: http.StatusNotFound, Code: "ARTIFACT_NOT_FOUND", Message: "artifact is unavailable to this tenant"})
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
