package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/artifact"
	"github.com/ivp/ivp/internal/persistence"
)

type memoryArtifactStore struct {
	objects       map[string][]byte
	wrongMetadata bool
}

func (s *memoryArtifactStore) Put(_ context.Context, key, media string, r io.Reader, limit int64) (artifact.Metadata, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return artifact.Metadata{}, err
	}
	if int64(len(data)) > limit {
		return artifact.Metadata{}, errors.New("artifact exceeds configured size limit")
	}
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[key] = data
	hash := sha256.Sum256(data)
	meta := artifact.Metadata{Key: key, MediaType: media, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	if s.wrongMetadata {
		meta.Size++
	}
	return meta, nil
}

func (s *memoryArtifactStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (s *memoryArtifactStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

type memoryArtifactRepository struct {
	rows  map[uuid.UUID]persistence.ArtifactMetadata
	write error
}

func (r *memoryArtifactRepository) SaveArtifact(_ context.Context, _ uuid.UUID, row persistence.ArtifactMetadata) error {
	if r.write != nil {
		return r.write
	}
	if r.rows == nil {
		r.rows = map[uuid.UUID]persistence.ArtifactMetadata{}
	}
	r.rows[row.ID] = row
	return nil
}

func (r *memoryArtifactRepository) GetArtifact(_ context.Context, tenant, id uuid.UUID) (persistence.ArtifactMetadata, error) {
	row, ok := r.rows[id]
	if !ok || row.TenantID != tenant {
		return persistence.ArtifactMetadata{}, errors.New("artifact not found")
	}
	return row, nil
}

func TestArtifactManagerSanitizesAndScopesUploadAndRetrieval(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	tenant, user, execution := uuid.New(), uuid.New(), uuid.New()
	repo, store := &memoryArtifactRepository{}, &memoryArtifactStore{}
	manager := &ArtifactManager{DB: repo, Store: store, MaxBytes: 1024, Retention: 24 * time.Hour, Now: func() time.Time { return now }}
	info, err := manager.Upload(context.Background(), tenant, user, execution, "application/json", strings.NewReader(`{"password":"secretvalue","passed":2}`))
	if err != nil {
		t.Fatal(err)
	}
	row := repo.rows[info.ID]
	stored := string(store.objects[row.ObjectKey])
	if strings.Contains(stored, "secretvalue") || !strings.Contains(stored, "[REDACTED]") || !row.Sanitized || row.TenantID != tenant || row.ExecutionID != execution {
		t.Fatalf("stored report or metadata was not safely scoped/redacted: content=%q row=%+v", stored, row)
	}
	got, body, err := manager.Open(context.Background(), tenant, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || got.ID != info.ID || string(data) != stored {
		t.Fatalf("retrieved info=%+v content=%q err=%v", got, data, err)
	}
	corrupted := []byte(stored)
	corrupted[0] ^= 1
	store.objects[row.ObjectKey] = corrupted
	_, body, err = manager.Open(context.Background(), tenant, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("artifact with a mismatched SHA-256 was served without an integrity error")
	}
	_ = body.Close()
	row.ObjectKey = artifact.TenantKey(uuid.New(), info.ID)
	repo.rows[info.ID] = row
	if _, body, err := manager.Open(context.Background(), tenant, info.ID); err == nil {
		if body != nil {
			_ = body.Close()
		}
		t.Fatal("artifact metadata pointing outside its tenant namespace was accepted")
	}
	if _, body, err := manager.Open(context.Background(), uuid.New(), info.ID); err == nil {
		if body != nil {
			_ = body.Close()
		}
		t.Fatal("cross-tenant artifact retrieval unexpectedly succeeded")
	}
}

func TestArtifactManagerRejectsOversizeAndDeletesBlobIfMetadataWriteFails(t *testing.T) {
	tenant, user, execution := uuid.New(), uuid.New(), uuid.New()
	store := &memoryArtifactStore{}
	manager := &ArtifactManager{DB: &memoryArtifactRepository{}, Store: store, MaxBytes: 8, Retention: time.Hour}
	if _, err := manager.Upload(context.Background(), tenant, user, execution, "application/json", strings.NewReader("123456789")); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	repo := &memoryArtifactRepository{write: errors.New("database unavailable")}
	manager.DB = repo
	if _, err := manager.Upload(context.Background(), tenant, user, execution, "application/json", strings.NewReader(`{"tests":1}`)); err == nil {
		t.Fatal("metadata write failure was ignored")
	}
	if len(store.objects) != 0 {
		t.Fatalf("orphaned artifact object remains after metadata failure: %v", store.objects)
	}
}

func TestArtifactManagerDeletesObjectWhenStoreMetadataIsInconsistent(t *testing.T) {
	store := &memoryArtifactStore{wrongMetadata: true}
	manager := &ArtifactManager{DB: &memoryArtifactRepository{}, Store: store, MaxBytes: 1024}
	if _, err := manager.Upload(context.Background(), uuid.New(), uuid.New(), uuid.New(), "application/json", strings.NewReader(`{"tests":1}`)); err == nil {
		t.Fatal("artifact store metadata mismatch was accepted")
	}
	if len(store.objects) != 0 {
		t.Fatalf("object with inconsistent storage metadata was left behind: %v", store.objects)
	}
}

func TestReportSanitizerRemovesSourcePathsFreeformFailureTextAndSecrets(t *testing.T) {
	jsonReport := `{"tests":[{"name":"\u001b[31mC:\\repo\\pkg\\test.go\u001b[0m","status":"failed","failureMessage":"source leaked here"}],"password":"secretvalue","stdout":"Authorization: Bearer abcdefghijkl"}`
	cleanJSON, err := sanitizeReport("application/json", []byte(jsonReport), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(cleanJSON) || strings.Contains(string(cleanJSON), "secretvalue") || strings.Contains(string(cleanJSON), "source leaked") || strings.Contains(string(cleanJSON), `C:\\repo`) || strings.Contains(string(cleanJSON), "abcdefghijkl") || strings.ContainsRune(string(cleanJSON), '\x1b') {
		t.Fatalf("JSON report was not sanitized: %s", cleanJSON)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cleanJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["password"] != "[REDACTED]" {
		t.Fatalf("credential field was not retained in redacted form: %v", decoded)
	}

	xmlReport := `<testsuite tests="1"><testcase name="suite.test" file="C:\repo\src\test.go"><failure message="source detail">source snippet and /work/project/file.go</failure><system-out>password=secretvalue</system-out></testcase></testsuite>`
	cleanXML, err := sanitizeReport("application/xml", []byte(xmlReport), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xml.NewDecoder(bytes.NewReader(cleanXML)).Token(); err != nil {
		t.Fatalf("sanitized XML is invalid: %v", err)
	}
	for _, forbidden := range []string{"source detail", "source snippet", "secretvalue", `C:\repo`, "/work/project"} {
		if strings.Contains(string(cleanXML), forbidden) {
			t.Fatalf("sanitized XML contains %q: %s", forbidden, cleanXML)
		}
	}
}
