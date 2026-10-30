package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
)

type apiClient struct {
	endpoint, token, tenant string
	http                    *http.Client
	githubOIDC              bool
	oidcAudience            string
}

type trustedPolicy struct {
	Policy  domain.Policy
	Version int64
	Hash    string
}

func (c *apiClient) getTrustedPolicy(ctx context.Context, repositoryID uuid.UUID) (trustedPolicy, error) {
	var envelope struct {
		Policy            json.RawMessage `json:"policy"`
		Version           int64           `json:"version"`
		PolicyHash        string          `json:"policy_hash"`
		Trusted           bool            `json:"trusted"`
		SignatureAlgorithm string          `json:"signature_algorithm"`
		SigningKeyID      string          `json:"signing_key_id"`
		Signature         string          `json:"signature"`
	}
	path := "/api/v1/repositories/" + repositoryID.String() + "/policy"
	if err := c.request(ctx, http.MethodGet, path, nil, "", &envelope); err != nil {
		return trustedPolicy{}, err
	}
	if envelope.Version < 1 || len(envelope.Policy) == 0 || !envelope.Trusted || envelope.SignatureAlgorithm != "Ed25519" {
		return trustedPolicy{}, exitError{4, errors.New("effective policy is not signed and trusted; using the full-suite fallback")}
	}
	actualHash, err := security.CanonicalJSONHash(envelope.Policy)
	if err != nil || !strings.EqualFold(actualHash, envelope.PolicyHash) {
		return trustedPolicy{}, exitError{4, errors.New("effective policy content does not match its signed hash")}
	}
	publicKey := strings.TrimSpace(os.Getenv("IVP_POLICY_VERIFY_PUBLIC_KEY"))
	if publicKey == "" {
		return trustedPolicy{}, exitError{4, errors.New("IVP_POLICY_VERIFY_PUBLIC_KEY is not configured; using the full-suite fallback")}
	}
	if err := security.VerifyPolicyEnvelope(publicKey, repositoryID, envelope.Version, envelope.PolicyHash, envelope.SigningKeyID, envelope.Signature); err != nil {
		return trustedPolicy{}, exitError{4, fmt.Errorf("effective policy signature is invalid: %w", err)}
	}
	var policy domain.Policy
	if err := json.Unmarshal(envelope.Policy, &policy); err != nil {
		return trustedPolicy{}, exitError{4, errors.New("signed effective policy has an invalid schema")}
	}
	policy.Version = envelope.Version
	return trustedPolicy{Policy: policy, Version: envelope.Version, Hash: strings.ToLower(envelope.PolicyHash)}, nil
}

func newAPIClient(cfg config.Config) (*apiClient, error) {
	endpointValue := strings.TrimSpace(os.Getenv("IVP_SERVER_ENDPOINT"))
	if endpointValue == "" {
		endpointValue = cfg.Server.Endpoint
	}
	endpoint := strings.TrimRight(strings.TrimSpace(endpointValue), "/")
	if endpoint == "" {
		return nil, exitError{4, errors.New("server.endpoint is not configured")}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, exitError{2, errors.New("server.endpoint must be an absolute HTTP(S) URL")}
	}
	if u.Scheme != "https" && !strings.EqualFold(u.Hostname(), "localhost") && !strings.EqualFold(u.Hostname(), "127.0.0.1") {
		return nil, exitError{2, errors.New("server.endpoint must use HTTPS outside localhost")}
	}
	timeout := time.Duration(cfg.Server.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	token := strings.TrimSpace(os.Getenv("IVP_TOKEN"))
	githubOIDC := false
	audience := strings.TrimSpace(os.Getenv("IVP_OIDC_AUDIENCE"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("IVP_CI_ID_TOKEN"))
		if token == "" {
			token = strings.TrimSpace(os.Getenv("GITLAB_OIDC_TOKEN"))
		}
		if token == "" {
			token = strings.TrimSpace(os.Getenv("CI_JOB_JWT_V2"))
		}
		if token == "" && os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "" {
			var tokenErr error
			token, tokenErr = requestGitHubOIDCToken(context.Background(), timeout, audience)
			if tokenErr != nil {
				return nil, exitError{4, tokenErr}
			}
			githubOIDC = true
		}
	}
	tenant := strings.TrimSpace(os.Getenv("IVP_TENANT_ID"))
	if token == "" || tenant == "" {
		return nil, exitError{4, errors.New("IVP_TOKEN or a CI OIDC identity token and IVP_TENANT_ID are required for API access")}
	}
	if _, err := uuid.Parse(tenant); err != nil {
		return nil, exitError{2, errors.New("IVP_TENANT_ID must be a UUID")}
	}
	return &apiClient{endpoint: endpoint, token: token, tenant: tenant, http: &http.Client{Timeout: timeout}, githubOIDC: githubOIDC, oidcAudience: audience}, nil
}

func (c *apiClient) request(ctx context.Context, method, path string, body any, idempotency string, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		var reader io.Reader
		if data != nil {
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
		if err != nil {
			return exitError{4, err}
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Tenant-ID", c.tenant)
		req.Header.Set("X-Request-ID", uuid.NewString())
		req.Header.Set("Authorization", "Bearer "+c.token)
		if data != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotency != "" {
			req.Header.Set("Idempotency-Key", idempotency)
		}
		response, err := c.http.Do(req)
		if err != nil {
			last = err
			if attempt < 3 {
				if e := waitRetry(ctx, attempt, "", 0); e != nil {
					return exitError{4, e}
				}
				continue
			}
			return exitError{4, errors.New("IVP API is unavailable; use the configured full-suite fallback")}
		}
		limit := int64(64 << 20)
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		response.Body.Close()
		if readErr != nil {
			return exitError{4, errors.New("IVP API response could not be read")}
		}
		if response.StatusCode == http.StatusUnauthorized && c.githubOIDC && attempt == 0 {
			if refreshErr := c.refreshGitHubOIDCToken(ctx); refreshErr != nil {
				return exitError{4, refreshErr}
			}
			continue
		}
		if int64(len(raw)) > limit {
			return exitError{4, errors.New("IVP API response exceeded the client limit")}
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			last = fmt.Errorf("API returned %d", response.StatusCode)
			if attempt < 3 {
				if e := waitRetry(ctx, attempt, response.Header.Get("Retry-After"), response.StatusCode); e != nil {
					return exitError{4, e}
				}
				continue
			}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return apiResponseError(response.StatusCode, raw)
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return exitError{6, errors.New("IVP API returned an invalid response")}
			}
		}
		return nil
	}
	if last == nil {
		last = errors.New("IVP API request failed")
	}
	return exitError{4, errors.New("IVP API request failed after bounded retries; use the configured full-suite fallback")}
}

func (c *apiClient) uploadArtifact(ctx context.Context, executionID uuid.UUID, mediaType string, content []byte) (api.ArtifactInfo, error) {
	var info api.ArtifactInfo
	if executionID == uuid.Nil || len(content) == 0 || int64(len(content)) > 16<<20 {
		return info, exitError{6, errors.New("artifact execution id or report size is invalid")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/v1/executions/"+executionID.String()+"/artifacts", bytes.NewReader(content))
	if err != nil {
		return info, exitError{4, err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mediaType)
	req.Header.Set("X-Tenant-ID", c.tenant)
	req.Header.Set("X-Request-ID", uuid.NewString())
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		return info, exitError{4, errors.New("sanitized report upload failed; local execution result remains queued")}
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return info, exitError{4, errors.New("sanitized report upload response could not be read")}
	}
	if response.StatusCode == http.StatusUnauthorized && c.githubOIDC {
		if err := c.refreshGitHubOIDCToken(ctx); err != nil {
			return info, exitError{4, err}
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/v1/executions/"+executionID.String()+"/artifacts", bytes.NewReader(content))
		if err != nil {
			return info, exitError{4, err}
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", mediaType)
		req.Header.Set("X-Tenant-ID", c.tenant)
		req.Header.Set("X-Request-ID", uuid.NewString())
		req.Header.Set("Authorization", "Bearer "+c.token)
		response, err = c.http.Do(req)
		if err != nil {
			return info, exitError{4, errors.New("sanitized report upload failed; local execution result remains queued")}
		}
		defer response.Body.Close()
		raw, readErr = io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return info, exitError{4, errors.New("sanitized report upload response could not be read")}
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return info, apiResponseError(response.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &info); err != nil || info.ID == uuid.Nil || !info.Sanitized {
		return api.ArtifactInfo{}, exitError{6, errors.New("IVP API did not confirm a sanitized report artifact")}
	}
	return info, nil
}

func (c *apiClient) refreshGitHubOIDCToken(ctx context.Context) error {
	token, err := requestGitHubOIDCToken(ctx, c.http.Timeout, c.oidcAudience)
	if err != nil {
		return err
	}
	c.token = token
	return nil
}

func requestGitHubOIDCToken(ctx context.Context, timeout time.Duration, audience string) (string, error) {
	endpoint := strings.TrimSpace(os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"))
	requestToken := strings.TrimSpace(os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"))
	if endpoint == "" || requestToken == "" || audience == "" {
		return "", errors.New("GitHub CI identity token exchange requires ACTIONS_ID_TOKEN_REQUEST_URL, ACTIONS_ID_TOKEN_REQUEST_TOKEN, and IVP_OIDC_AUDIENCE")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("GitHub CI identity token endpoint is invalid")
	}
	query := u.Query()
	query.Set("audience", audience)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("GitHub CI identity token request could not be created")
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("GitHub CI identity token exchange failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", errors.New("GitHub CI identity token exchange was rejected")
	}
	var payload struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(data, &payload) != nil || strings.TrimSpace(payload.Value) == "" || strings.ContainsAny(payload.Value, "\r\n") {
		return "", errors.New("GitHub CI identity token response is invalid")
	}
	return strings.TrimSpace(payload.Value), nil
}

func waitRetry(ctx context.Context, attempt int, retryAfter string, status int) error {
	delay := time.Duration(1<<attempt) * 200 * time.Millisecond
	if n, err := strconv.Atoi(retryAfter); err == nil && n > 0 && n < 10 {
		delay = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(retryAfter); err == nil {
		if until := time.Until(t); until > 0 && until < 10*time.Second {
			delay = until
		}
	}
	if status != 0 && status != 429 && status < 500 {
		return nil
	}
	maxJitter := big.NewInt(int64(delay/3 + 1))
	if n, err := rand.Int(rand.Reader, maxJitter); err == nil {
		delay += time.Duration(n.Int64())
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func apiResponseError(status int, raw []byte) error {
	var response struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &response)
	message := strings.TrimSpace(response.Error.Message)
	if message == "" {
		message = http.StatusText(status)
	}
	message = security.Redact(message, 240)
	code := 4
	if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
		code = 2
	}
	if status == http.StatusConflict {
		code = 5
	}
	return exitError{code, fmt.Errorf("IVP API %s: %s", response.Error.Code, message)}
}

func (c *apiClient) repository(ctx context.Context, slug string) (domain.Repository, error) {
	if raw := strings.TrimSpace(os.Getenv("IVP_REPOSITORY_ID")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return domain.Repository{}, exitError{2, errors.New("IVP_REPOSITORY_ID must be a UUID")}
		}
		var repo domain.Repository
		if err := c.request(ctx, http.MethodGet, "/api/v1/repositories/"+id.String(), nil, "", &repo); err != nil {
			return repo, err
		}
		return repo, nil
	}
	cursor := ""
	for page := 0; page < 20; page++ {
		path := "/api/v1/repositories?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var result struct {
			Items      []domain.Repository `json:"items"`
			NextCursor string              `json:"next_cursor"`
		}
		if err := c.request(ctx, http.MethodGet, path, nil, "", &result); err != nil {
			return domain.Repository{}, err
		}
		for _, repo := range result.Items {
			if repo.Slug == slug {
				return repo, nil
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return domain.Repository{}, exitError{4, errors.New("repository is not registered in the selected tenant; ask a maintainer to register it and set IVP_REPOSITORY_ID")}
}

func newIdempotencyKey() string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return uuid.NewString()
	}
	return hex.EncodeToString(raw[:])
}
