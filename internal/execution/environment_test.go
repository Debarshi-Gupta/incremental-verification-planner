package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

type stubAdapter struct {
	id      string
	version string
	err     error
}

func (s *stubAdapter) ID() string { return s.id }

func (s *stubAdapter) Detect(context.Context, string) (adapters.Detection, error) {
	return adapters.Detection{}, nil
}

func (s *stubAdapter) Discover(context.Context, adapters.DiscoverRequest) (adapters.DiscoveryResult, error) {
	return adapters.DiscoveryResult{}, nil
}

func (s *stubAdapter) Materialize(context.Context, adapters.MaterializeRequest) (adapters.ExecutionCommand, error) {
	return adapters.ExecutionCommand{}, nil
}

func (s *stubAdapter) ParseResults(context.Context, adapters.ParseRequest) (adapters.ParsedResults, error) {
	return adapters.ParsedResults{}, nil
}

func (s *stubAdapter) ParseCoverage(context.Context, adapters.CoverageRequest) (adapters.CoverageResult, error) {
	return adapters.CoverageResult{}, nil
}

func (s *stubAdapter) Capabilities() adapters.Capabilities { return adapters.Capabilities{} }

func (s *stubAdapter) Version(context.Context, string) (string, error) { return s.version, s.err }

func (s *stubAdapter) ValidateConfig(adapters.AdapterConfig) error { return nil }

func TestValidateEnvironmentDetectsPlanMismatch(t *testing.T) {
	plan := domain.VerificationPlan{ID: uuid.New(), Status: domain.PlanReady, EnvironmentFingerprint: "sha256:planned"}
	if err := ValidateEnvironment(plan, "sha256:planned"); err != nil {
		t.Fatalf("an unchanged environment fingerprint must be accepted: %v", err)
	}
	err := ValidateEnvironment(plan, "sha256:other")
	if err == nil {
		t.Fatal("a changed environment fingerprint must be rejected")
	}
	if !errors.Is(err, ErrEnvironmentMismatch) {
		t.Fatalf("the mismatch must be reported as an environment mismatch: %v", err)
	}
	if err := ValidateEnvironment(plan, ""); err != nil {
		t.Fatalf("an unknown environment fingerprint must not be treated as a difference: %v", err)
	}
	unknown := domain.VerificationPlan{ID: uuid.New(), Status: domain.PlanReady}
	if err := ValidateEnvironment(unknown, "sha256:other"); err != nil {
		t.Fatalf("a plan without an environment fingerprint must not be rejected: %v", err)
	}
}

func TestValidatePlanRejectsChangedExecutionEnvironment(t *testing.T) {
	now := time.Now().UTC()
	plan := domain.VerificationPlan{ID: uuid.New(), Status: domain.PlanReady, WorktreeHash: "tree-a", PolicyVersion: 1,
		AlgorithmVersion: "algo-v1", EnvironmentFingerprint: "sha256:planned", ExpiresAt: now.Add(time.Minute),
		SelectedUnits: []domain.PlanUnit{{StableKey: "tests/unit", TargetRef: "tests/unit"}}}
	valid := PlanRevalidation{CurrentWorktreeHash: "tree-a", PolicyVersion: 1, AlgorithmVersion: "algo-v1", Now: now,
		KnownTargetRefs: map[string]bool{"tests/unit": true}, EnvironmentFingerprint: "sha256:planned"}
	if err := ValidatePlan(plan, valid); err != nil {
		t.Fatal(err)
	}
	valid.EnvironmentFingerprint = "sha256:changed"
	err := ValidatePlan(plan, valid)
	if err == nil {
		t.Fatal("a changed execution environment fingerprint must invalidate the plan")
	}
	if !errors.Is(err, ErrEnvironmentMismatch) {
		t.Fatalf("the invalidation must be reported as an environment mismatch: %v", err)
	}
}

func TestAdapterToolchainDigestMatchesCanonicalIndexerDigest(t *testing.T) {
	root := t.TempDir()
	digest, err := AdapterToolchainDigest(context.Background(), root, []adapters.Adapter{&stubAdapter{id: "pytest", version: "8.2"}})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := domain.CanonicalDigest(map[string]string{"pytest": "8.2"})
	if err != nil {
		t.Fatal(err)
	}
	if digest != expected {
		t.Fatalf("the recomputed toolchain digest %q differs from the canonical digest %q", digest, expected)
	}
	if _, err := AdapterToolchainDigest(context.Background(), root, []adapters.Adapter{&stubAdapter{id: "pytest", err: errors.New("tool missing")}}); err == nil {
		t.Fatal("an undetectable adapter version must not produce a digest")
	}
	if _, err := AdapterToolchainDigest(context.Background(), root, nil); err == nil {
		t.Fatal("a workspace without adapters must not produce a digest")
	}
}
