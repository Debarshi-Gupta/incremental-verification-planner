package persistence

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validArtifactMetadataFixture() ArtifactMetadata {
	tenantID, artifactID, executionID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	return ArtifactMetadata{
		ID: artifactID, TenantID: tenantID, ExecutionID: executionID,
		ObjectKey: tenantID.String() + "/" + artifactID.String(), MediaType: "application/json",
		SizeBytes: 10, SHA256: strings.Repeat("a", 64), Sanitized: true,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func TestArtifactMetadataRequiresTenantKeyAndSafeBoundedContent(t *testing.T) {
	valid := validArtifactMetadataFixture()
	if err := validateArtifactMetadata(valid); err != nil {
		t.Fatalf("valid artifact metadata rejected: %v", err)
	}
	tests := map[string]func(*ArtifactMetadata){
		"foreign object key":     func(v *ArtifactMetadata) { v.ObjectKey = uuid.NewString() + "/" + v.ID.String() },
		"unsupported media type": func(v *ArtifactMetadata) { v.MediaType = "text/plain" },
		"oversize object":        func(v *ArtifactMetadata) { v.SizeBytes = 64<<20 + 1 },
		"invalid digest":         func(v *ArtifactMetadata) { v.SHA256 = "not-a-digest" },
		"unsanitized object":     func(v *ArtifactMetadata) { v.Sanitized = false },
		"nonpositive retention":  func(v *ArtifactMetadata) { v.ExpiresAt = v.CreatedAt },
		"unbounded retention":    func(v *ArtifactMetadata) { v.ExpiresAt = v.CreatedAt.Add(366 * 24 * time.Hour) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			item := valid
			mutate(&item)
			if err := validateArtifactMetadata(item); err == nil {
				t.Fatal("invalid artifact metadata accepted")
			}
		})
	}
}
