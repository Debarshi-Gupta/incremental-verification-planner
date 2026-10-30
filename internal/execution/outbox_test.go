package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/domain"
)

func TestOutboxPersistsBeforeUploadAndRetriesWithoutExecution(t *testing.T) {
	root := t.TempDir()
	outbox, err := NewOutbox(root, 1<<20, 10)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{ID: uuid.New(), Status: StatusCompleted, Outcome: domain.OutcomeTargetedPass, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), StdoutHash: "sha256-a", StdoutExcerpt: "safe summary"}
	path, err := outbox.PersistThenUpload(context.Background(), uuid.New(), result, func(context.Context, LocalReport) error { return errors.New("offline") })
	if err == nil || path == "" {
		t.Fatalf("expected persisted report after upload failure, path=%q err=%v", path, err)
	}
	called := 0
	uploaded, err := outbox.RetryUploads(context.Background(), func(_ context.Context, report LocalReport) error {
		called++
		if report.IdempotencyKey != result.ID.String() {
			t.Errorf("idempotency key changed: %q", report.IdempotencyKey)
		}
		if report.Outcome != domain.OutcomeTargetedPass {
			t.Errorf("targeted pass semantics changed: %s", report.Outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded != 1 || called != 1 {
		t.Fatalf("retried %d reports with %d uploads", uploaded, called)
	}
	if uploaded, err = outbox.RetryUploads(context.Background(), func(context.Context, LocalReport) error { t.Fatal("deleted report uploaded again"); return nil }); err != nil || uploaded != 0 {
		t.Fatalf("report was not removed after upload: count=%d err=%v", uploaded, err)
	}
}

func TestOfflinePolicyRequiresTrustedUnexpiredCompatibleSnapshot(t *testing.T) {
	now := time.Now().UTC()
	valid := OfflinePolicy{Trusted: true, ExpiresAt: now.Add(time.Minute), AlgorithmVersion: "v1", ExpectedAlgorithmVersion: "v1"}
	if err := ValidateOfflinePolicy(valid, now); err != nil {
		t.Fatal(err)
	}
	valid.Trusted = false
	if err := ValidateOfflinePolicy(valid, now); err == nil {
		t.Fatal("untrusted offline policy accepted")
	}
	valid.Trusted = true
	valid.ExpiresAt = now.Add(-time.Second)
	if err := ValidateOfflinePolicy(valid, now); err == nil {
		t.Fatal("expired offline policy accepted")
	}
	valid.ExpiresAt = now.Add(time.Minute)
	valid.AlgorithmVersion = "old"
	if err := ValidateOfflinePolicy(valid, now); err == nil {
		t.Fatal("incompatible policy accepted")
	}
}
