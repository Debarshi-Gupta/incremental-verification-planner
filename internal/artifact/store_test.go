package artifact

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestFileStoreBoundsTenantObjectsAndRejectsTraversal(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222"
	meta, err := store.Put(context.Background(), key, "text/plain", strings.NewReader("report"), 16)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Size != 6 || meta.SHA256 == "" {
		t.Fatalf("unexpected object metadata: %+v", meta)
	}
	r, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(data) != "report" {
		t.Fatalf("object read=%q err=%v", data, err)
	}
	if _, err := store.Put(context.Background(), key, "text/plain", strings.NewReader("overwrite"), 32); err == nil {
		t.Fatal("duplicate object key unexpectedly overwritten")
	}
	if _, err := store.Put(context.Background(), "../escape", "text/plain", strings.NewReader("x"), 4); err == nil {
		t.Fatal("path traversal object key accepted")
	}
	if _, err := store.Put(context.Background(), "11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222", "text/plain", strings.NewReader("too large"), 4); err == nil {
		t.Fatal("over-limit object accepted")
	}
	if _, err := store.Put(context.Background(), key, "text/plain", strings.NewReader("x"), MaxAllowedBytes+1); err == nil {
		t.Fatal("unbounded caller-supplied artifact limit accepted")
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
}

func TestS3CompatibleStoreSignsAndRoundTripsBoundedObjects(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test-access/") {
			return mockResponse(r, http.StatusUnauthorized, []byte("missing signature")), nil
		}
		if r.Header.Get("x-amz-security-token") != "session-token" {
			return mockResponse(r, http.StatusUnauthorized, []byte("missing session token")), nil
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			objects[r.URL.Path] = data
			return mockResponse(r, http.StatusOK, nil), nil
		case http.MethodGet:
			data, ok := objects[r.URL.Path]
			if !ok {
				return mockResponse(r, http.StatusNotFound, nil), nil
			}
			return mockResponse(r, http.StatusOK, data), nil
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			return mockResponse(r, http.StatusNoContent, nil), nil
		default:
			return mockResponse(r, http.StatusMethodNotAllowed, nil), nil
		}
	})
	store, err := NewS3Store(S3Config{Endpoint: "http://s3.example.test", Region: "us-east-1", Bucket: "ivp-test", AccessKey: "test-access", SecretKey: "test-secret", SessionToken: "session-token", AllowInsecure: true, Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	key := "11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222"
	meta, err := store.Put(context.Background(), key, "application/json", strings.NewReader(`{"ok":true}`), 32)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Size != 11 || meta.SHA256 == "" {
		t.Fatalf("unexpected S3 metadata: %+v", meta)
	}
	r, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("S3 object read=%q err=%v", data, err)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), key, "text/plain", strings.NewReader("too large"), 3); err == nil {
		t.Fatal("S3 store accepted object larger than bound")
	}
	if _, err := store.Put(context.Background(), key, "text/plain", strings.NewReader("x"), MaxAllowedBytes+1); err == nil {
		t.Fatal("S3 store accepted an unbounded caller-supplied size limit")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockResponse(request *http.Request, status int, data []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: request}
}
