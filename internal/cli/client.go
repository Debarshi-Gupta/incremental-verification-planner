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
	"github.com/ivp/ivp/internal/config"
	"github.com/ivp/ivp/internal/domain"
	"github.com/ivp/ivp/internal/security"
)

type apiClient struct {
	endpoint, token, tenant string
	http                    *http.Client
}

func newAPIClient(cfg config.Config) (*apiClient, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Server.Endpoint), "/")
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
	token := strings.TrimSpace(os.Getenv("IVP_TOKEN"))
	tenant := strings.TrimSpace(os.Getenv("IVP_TENANT_ID"))
	if token == "" || tenant == "" {
		return nil, exitError{4, errors.New("IVP_TOKEN and IVP_TENANT_ID are required for API access")}
	}
	if _, err := uuid.Parse(tenant); err != nil {
		return nil, exitError{2, errors.New("IVP_TENANT_ID must be a UUID")}
	}
	timeout := time.Duration(cfg.Server.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &apiClient{endpoint: endpoint, token: token, tenant: tenant, http: &http.Client{Timeout: timeout}}, nil
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
