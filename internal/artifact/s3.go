package artifact

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var bucketPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{1,61}[A-Za-z0-9]$`)

// S3Config configures a path-style S3-compatible endpoint such as AWS S3,
// MinIO, or Ceph RGW. Use HTTPS except for local development endpoints.
type S3Config struct {
	Endpoint      string
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	SessionToken  string
	AllowInsecure bool
	Client        *http.Client
}

type S3Store struct {
	endpoint     *url.URL
	region       string
	bucket       string
	accessKey    string
	secretKey    string
	sessionToken string
	client       *http.Client
}

func NewS3Store(cfg S3Config) (*S3Store, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("S3 endpoint must be an absolute URL without credentials, query, or fragment")
	}
	if u.Scheme != "https" && !(cfg.AllowInsecure && u.Scheme == "http") {
		return nil, errors.New("S3 endpoint must use HTTPS outside explicitly enabled local development")
	}
	if !bucketPattern.MatchString(cfg.Bucket) || cfg.Region == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("S3 bucket, region, access key, and secret key are required")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := *u
	return &S3Store{endpoint: &endpoint, region: cfg.Region, bucket: cfg.Bucket, accessKey: cfg.AccessKey, secretKey: cfg.SecretKey, sessionToken: cfg.SessionToken, client: client}, nil
}

func (s *S3Store) objectURL(key string) (*url.URL, error) {
	if s == nil || s.endpoint == nil {
		return nil, errors.New("S3 artifact store is unavailable")
	}
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	u := *s.endpoint
	base := strings.TrimSuffix(u.EscapedPath(), "/")
	escaped := base + "/" + url.PathEscape(s.bucket)
	for _, part := range strings.Split(key, "/") {
		escaped += "/" + url.PathEscape(part)
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, err
	}
	u.Path = decoded
	u.RawPath = escaped
	return &u, nil
}

func (s *S3Store) Put(ctx context.Context, key, mediaType string, src io.Reader, maxBytes int64) (Metadata, error) {
	var meta Metadata
	if src == nil || maxBytes <= 0 || maxBytes > MaxAllowedBytes {
		return meta, errors.New("artifact source and size bound between 1 byte and 64 MiB are required")
	}
	data, err := readBounded(ctx, src, maxBytes)
	if err != nil {
		return meta, err
	}
	hash := sha256.Sum256(data)
	request, err := s.request(ctx, http.MethodPut, key, data)
	if err != nil {
		return meta, err
	}
	if mediaType != "" {
		request.Header.Set("Content-Type", mediaType)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return meta, fmt.Errorf("S3 object upload failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return meta, fmt.Errorf("S3 object upload returned HTTP %d", response.StatusCode)
	}
	return Metadata{Key: key, MediaType: mediaType, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}, nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	request, err := s.request(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("S3 object retrieval failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("S3 object retrieval returned HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	request, err := s.request(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("S3 object deletion failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("S3 object deletion returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *S3Store) request(ctx context.Context, method, key string, body []byte) (*http.Request, error) {
	u, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(hash[:])
	now := time.Now().UTC()
	amzDate, date := now.Format("20060102T150405Z"), now.Format("20060102")
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)
	canonicalHeaders := "host:" + strings.ToLower(u.Host) + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	if s.sessionToken != "" {
		req.Header.Set("x-amz-security-token", s.sessionToken)
		canonicalHeaders += "x-amz-security-token:" + strings.TrimSpace(s.sessionToken) + "\n"
		signedHeaders += ";x-amz-security-token"
	}
	canonicalURI := u.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := method + "\n" + canonicalURI + "\n" + u.Query().Encode() + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	scope := date + "/" + s.region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	kDate := hmacSHA256([]byte("AWS4"+s.secretKey), date)
	kRegion := hmacSHA256(kDate, s.region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	credential := s.accessKey + "/" + scope
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+credential+", SignedHeaders="+signedHeaders+", Signature="+signature)
	return req, nil
}

func hmacSHA256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = io.WriteString(h, value)
	return h.Sum(nil)
}

func readBounded(ctx context.Context, src io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > MaxAllowedBytes {
		return nil, errors.New("artifact size bound must be between 1 byte and 64 MiB")
	}
	var data bytes.Buffer
	_, err := io.Copy(&data, &contextReader{ctx: ctx, r: io.LimitReader(src, maxBytes+1)})
	if err != nil {
		return nil, err
	}
	if int64(data.Len()) > maxBytes {
		return nil, errors.New("artifact exceeds configured size limit")
	}
	return data.Bytes(), nil
}
