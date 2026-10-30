package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/artifact"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
	"github.com/ivp/ivp/internal/telemetry"
)

const DefaultArtifactRetention = 90 * 24 * time.Hour

type ArtifactManager struct {
	DB        ArtifactRepository
	Store     artifact.Store
	MaxBytes  int64
	Retention time.Duration
	Now       func() time.Time
	Metrics   *telemetry.Metrics
}

type ArtifactRepository interface {
	SaveArtifact(context.Context, uuid.UUID, persistence.ArtifactMetadata) error
	GetArtifact(context.Context, uuid.UUID, uuid.UUID) (persistence.ArtifactMetadata, error)
}

var (
	artifactSecretKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|credential|api[_-]?key|private[_-]?key|authorization)`)
	artifactStripKey  = regexp.MustCompile(`(?i)^(source|sourcecode|snippet|stack|stacktrace|stdout|stderr|output|log|logs|command|cwd|file|filepath|path|environment|env|failuremessage|message|location)$`)
	artifactSafeKey   = regexp.MustCompile(`(?i)^(tests|testresults|testcases|results|suites|stats|totals|total|passed|failed|skipped|success|numpassedtests|numfailedtests|numskippedtests|numtotaltests|numpassedtestsuites|numfailedtestsuites|numtotaltestsuites|name|fullname|title|testname|package|action|elapsed|duration|durationmillis|starttime|endtime|startedat|finishedat|status|assertionresults|assertions|assertioncount|passes|fails|pending|errorcount)$`)
	windowsPath       = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s"'<>]+`)
	unixPath          = regexp.MustCompile(`(^|[\s"'=])/(?:[A-Za-z0-9._-]+/)+[A-Za-z0-9._-]+`)
	xmlSafeAttribute  = map[string]struct{}{"name": {}, "classname": {}, "time": {}, "tests": {}, "failures": {}, "errors": {}, "skipped": {}, "timestamp": {}, "assertions": {}, "hostname": {}, "package": {}, "version": {}, "id": {}}
	xmlSafeText       = map[string]struct{}{"name": {}, "classname": {}, "time": {}, "tests": {}, "failures": {}, "errors": {}, "skipped": {}, "timestamp": {}, "assertions": {}, "hostname": {}, "package": {}, "version": {}, "count": {}, "duration": {}}
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (m *ArtifactManager) defaults() (int64, time.Duration, time.Time, error) {
	if m == nil || m.DB == nil || m.Store == nil {
		return 0, 0, time.Time{}, errors.New("artifact storage is unavailable")
	}
	maxBytes := m.MaxBytes
	if maxBytes <= 0 {
		maxBytes = artifact.DefaultMaxBytes
	}
	if maxBytes > artifact.MaxAllowedBytes {
		return 0, 0, time.Time{}, errors.New("artifact size limit cannot exceed 64 MiB")
	}
	retention := m.Retention
	if retention <= 0 {
		retention = DefaultArtifactRetention
	}
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now().UTC()
	}
	if _, err := artifact.Expiry(now, retention); err != nil {
		return 0, 0, time.Time{}, err
	}
	return maxBytes, retention, now, nil
}

func (m *ArtifactManager) Upload(ctx context.Context, tenantID, actorID, executionID uuid.UUID, mediaType string, src io.Reader) (api.ArtifactInfo, error) {
	var info api.ArtifactInfo
	maxBytes, retention, now, err := m.defaults()
	if err != nil {
		return info, err
	}
	if tenantID == uuid.Nil || executionID == uuid.Nil || src == nil {
		return info, errors.New("tenant, execution, and artifact content are required")
	}
	mediaType, _, err = mime.ParseMediaType(mediaType)
	if err != nil {
		return info, errors.New("artifact media type is invalid")
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "application/json" && mediaType != "application/xml" && mediaType != "text/xml" {
		return info, errors.New("only JSON and XML test reports can be stored")
	}
	// Artifacts are intentionally text report formats. Sanitization is applied
	// server-side before bytes reach either storage backend.
	raw, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		return info, err
	}
	if int64(len(raw)) > maxBytes {
		return info, errors.New("artifact exceeds configured size limit")
	}
	if len(raw) == 0 {
		return info, errors.New("empty report artifacts are not stored")
	}
	cleanBytes, err := sanitizeReport(mediaType, raw, maxBytes)
	if err != nil {
		return info, err
	}
	clean := string(cleanBytes)
	if clean == "" {
		return info, errors.New("artifact is empty after sanitization")
	}
	artifactID, err := artifact.NewID()
	if err != nil {
		return info, fmt.Errorf("generate artifact id: %w", err)
	}
	key := artifact.TenantKey(tenantID, artifactID)
	object, err := m.Store.Put(ctx, key, mediaType, bytes.NewBufferString(clean), maxBytes)
	if err != nil {
		if m.Metrics != nil {
			m.Metrics.ArtifactStoreError("upload")
		}
		return info, err
	}
	expectedHash := sha256.Sum256(cleanBytes)
	if object.Key != key || object.MediaType != mediaType || object.Size != int64(len(cleanBytes)) || !strings.EqualFold(object.SHA256, hex.EncodeToString(expectedHash[:])) {
		_ = m.Store.Delete(context.Background(), key)
		return info, errors.New("artifact store returned inconsistent object metadata")
	}
	expiresAt, err := artifact.Expiry(now, retention)
	if err != nil {
		_ = m.Store.Delete(context.Background(), key)
		return info, err
	}
	row := persistence.ArtifactMetadata{ID: artifactID, TenantID: tenantID, ExecutionID: executionID, ObjectKey: key,
		MediaType: object.MediaType, SizeBytes: object.Size, SHA256: object.SHA256, Sanitized: true, ExpiresAt: expiresAt, CreatedAt: now}
	if err := m.DB.SaveArtifact(ctx, actorID, row); err != nil {
		_ = m.Store.Delete(context.Background(), key)
		return info, err
	}
	if m.Metrics != nil {
		m.Metrics.ArtifactUploaded(mediaType, row.SizeBytes)
	}
	return api.ArtifactInfo{ID: artifactID, MediaType: row.MediaType, SizeBytes: row.SizeBytes, SHA256: row.SHA256, Sanitized: true, ExpiresAt: expiresAt.Format(time.RFC3339Nano)}, nil
}

func sanitizeReport(mediaType string, raw []byte, maxBytes int64) ([]byte, error) {
	switch mediaType {
	case "application/json":
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("artifact is not a valid JSON test report")
		}
		object, ok := value.(map[string]any)
		if !ok || !looksLikeJSONTestReport(object) {
			return nil, errors.New("JSON artifact must contain recognized test-report fields")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("artifact contains trailing JSON data")
		}
		clean, _ := sanitizeJSONValue(value, "")
		data, err := json.Marshal(clean)
		if err != nil {
			return nil, errors.New("artifact JSON report could not be sanitized")
		}
		if int64(len(data)) > maxBytes {
			return nil, errors.New("sanitized artifact exceeds configured size limit")
		}
		return data, nil
	case "application/xml", "text/xml":
		return sanitizeXMLReport(raw, maxBytes)
	default:
		return nil, errors.New("only JSON and XML test reports can be stored")
	}
}

func looksLikeJSONTestReport(report map[string]any) bool {
	known := map[string]struct{}{"tests": {}, "testresults": {}, "testcases": {}, "results": {}, "suites": {}, "stats": {}, "totals": {}, "total": {}, "passed": {}, "failed": {}, "skipped": {}, "errors": {}, "numtotaltests": {}, "numpassedtests": {}, "numfailedtests": {}, "testsuites": {}, "test-suites": {}, "summary": {}}
	for key := range report {
		if _, ok := known[strings.ToLower(key)]; ok {
			return true
		}
	}
	return false
}

func sanitizeJSONValue(value any, key string) (any, bool) {
	if artifactStripKey.MatchString(key) {
		return nil, false
	}
	if artifactSecretKey.MatchString(key) {
		return "[REDACTED]", true
	}
	if key != "" && !artifactSafeKey.MatchString(key) {
		return nil, false
	}
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for childKey, child := range typed {
			if sanitized, keep := sanitizeJSONValue(child, childKey); keep {
				clean[childKey] = sanitized
			}
		}
		return clean, true
	case []any:
		clean := make([]any, 0, len(typed))
		for _, child := range typed {
			if sanitized, keep := sanitizeJSONValue(child, ""); keep {
				clean = append(clean, sanitized)
			}
		}
		return clean, true
	case string:
		text := security.Redact(typed, len(typed)+256)
		text = windowsPath.ReplaceAllString(text, "[PATH]")
		text = unixPath.ReplaceAllString(text, "$1[PATH]")
		return text, true
	default:
		return value, true
	}
}

func sanitizeXMLReport(raw []byte, maxBytes int64) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	type frame struct {
		name string
		skip bool
	}
	stack := make([]frame, 0, 16)
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("artifact is not a well-formed XML test report")
		}
		switch item := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(item.Name.Local)
			if len(stack) == 0 {
				rootSeen = true
				if name != "testsuite" && name != "testsuites" && name != "test-results" && name != "tests" {
					return nil, errors.New("XML artifact root is not a recognized test-report element")
				}
			}
			skip := name == "failure" || name == "error" || name == "system-out" || name == "system-err" || name == "source" || name == "sourcecode" || name == "snippet" || name == "stack" || name == "stacktrace" || name == "stdout" || name == "stderr" || name == "output" || name == "log" || name == "logs" || name == "properties" || name == "property"
			if _, ok := xmlSafeText[name]; !ok {
				skip = true
			}
			if len(stack) > 0 && stack[len(stack)-1].skip {
				skip = true
			}
			stack = append(stack, frame{name: name, skip: skip})
			attrs := item.Attr[:0]
			for _, attr := range item.Attr {
				attrName := strings.ToLower(attr.Name.Local)
				if _, ok := xmlSafeAttribute[attrName]; !ok {
					continue
				}
				if artifactSecretKey.MatchString(attrName) {
					attr.Value = "[REDACTED]"
				} else {
					attr.Value = sanitizeText(attr.Value, 4096)
				}
				attrs = append(attrs, attr)
			}
			item.Attr = attrs
			if err := encoder.EncodeToken(item); err != nil {
				return nil, errors.New("sanitized XML artifact could not be encoded")
			}
		case xml.EndElement:
			if err := encoder.EncodeToken(item); err != nil {
				return nil, errors.New("sanitized XML artifact could not be encoded")
			}
			if len(stack) == 0 {
				return nil, errors.New("artifact XML structure is invalid")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 && !stack[len(stack)-1].skip {
				text := sanitizeText(string(item), int(maxBytes))
				if err := encoder.EncodeToken(xml.CharData([]byte(text))); err != nil {
					return nil, errors.New("sanitized XML artifact could not be encoded")
				}
			}
		case xml.Directive, xml.ProcInst, xml.Comment:
			// Do not preserve declarations, processing instructions, or comments.
		}
		if int64(output.Len()) > maxBytes {
			return nil, errors.New("sanitized artifact exceeds configured size limit")
		}
	}
	if !rootSeen || len(stack) != 0 {
		return nil, errors.New("artifact XML document is incomplete")
	}
	if err := encoder.Flush(); err != nil {
		return nil, errors.New("sanitized XML artifact could not be flushed")
	}
	if int64(output.Len()) > maxBytes {
		return nil, errors.New("sanitized artifact exceeds configured size limit")
	}
	return output.Bytes(), nil
}

func sanitizeText(value string, maxBytes int) string {
	text := security.Redact(value, maxBytes)
	text = windowsPath.ReplaceAllString(text, "[PATH]")
	return unixPath.ReplaceAllString(text, "$1[PATH]")
}

func (m *ArtifactManager) Open(ctx context.Context, tenantID, artifactID uuid.UUID) (api.ArtifactInfo, io.ReadCloser, error) {
	var info api.ArtifactInfo
	maxBytes, _, now, err := m.defaults()
	if err != nil {
		return info, nil, err
	}
	if tenantID == uuid.Nil || artifactID == uuid.Nil {
		return info, nil, errors.New("tenant and artifact identifiers are required")
	}
	row, err := m.DB.GetArtifact(ctx, tenantID, artifactID)
	if err != nil {
		return info, nil, err
	}
	if row.ObjectKey != artifact.TenantKey(tenantID, artifactID) || !row.Sanitized || !row.ExpiresAt.After(now) || row.SizeBytes <= 0 || row.SizeBytes > maxBytes || !sha256Pattern.MatchString(strings.ToLower(row.SHA256)) || (row.MediaType != "application/json" && row.MediaType != "application/xml" && row.MediaType != "text/xml") {
		return info, nil, errors.New("artifact is unavailable")
	}
	reader, err := m.Store.Open(ctx, row.ObjectKey)
	if err != nil {
		if m.Metrics != nil {
			m.Metrics.ArtifactStoreError("download")
		}
		return info, nil, err
	}
	verified := &integrityReadCloser{source: reader, expectedSize: row.SizeBytes, expectedSHA256: strings.ToLower(row.SHA256), hasher: sha256.New()}
	return api.ArtifactInfo{ID: row.ID, MediaType: row.MediaType, SizeBytes: row.SizeBytes, SHA256: row.SHA256, Sanitized: row.Sanitized, ExpiresAt: row.ExpiresAt.Format(time.RFC3339Nano)}, verified, nil
}

type integrityReadCloser struct {
	source         io.ReadCloser
	expectedSize   int64
	expectedSHA256 string
	read           int64
	hasher         hash.Hash
	verified       bool
	failed         error
}

func (r *integrityReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.failed != nil {
		return 0, r.failed
	}
	if r.verified {
		return 0, io.EOF
	}
	if r.read == r.expectedSize {
		var probe [1]byte
		n, err := r.source.Read(probe[:])
		if n > 0 {
			r.failed = errors.New("stored artifact exceeds recorded size")
			return 0, r.failed
		}
		if err == io.EOF {
			return 0, r.verify()
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	remaining := r.expectedSize - r.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.source.Read(p)
	if n > 0 {
		r.read += int64(n)
		_, _ = r.hasher.Write(p[:n])
	}
	if err == io.EOF {
		if r.read != r.expectedSize {
			r.failed = io.ErrUnexpectedEOF
			return n, r.failed
		}
		if verifyErr := r.verify(); verifyErr != nil {
			return n, verifyErr
		}
		return n, io.EOF
	}
	return n, err
}

func (r *integrityReadCloser) verify() error {
	if r.read != r.expectedSize {
		r.failed = io.ErrUnexpectedEOF
		return r.failed
	}
	if hex.EncodeToString(r.hasher.Sum(nil)) != r.expectedSHA256 {
		r.failed = errors.New("stored artifact checksum does not match metadata")
		return r.failed
	}
	r.verified = true
	return io.EOF
}

func (r *integrityReadCloser) Close() error { return r.source.Close() }

func (m *ArtifactManager) DeleteExpiredObject(ctx context.Context, key string) error {
	if m == nil || m.Store == nil {
		return errors.New("artifact storage is unavailable")
	}
	err := m.Store.Delete(ctx, key)
	if err != nil && m.Metrics != nil {
		m.Metrics.ArtifactStoreError("delete")
	}
	return err
}

var _ api.ArtifactService = (*ArtifactManager)(nil)
