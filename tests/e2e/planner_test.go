//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/domain"
)

func TestIncompleteIndexRequiresFullVerification(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	repositoryID, snapshotID := uuid.New(), uuid.New()
	snapshot := domain.RepositorySnapshot{
		ID: snapshotID, RepositoryID: repositoryID, CommitSHA: "head", BaseSHA: "base",
		TreeHash: "tree", WorktreeHash: "worktree", GraphVersion: 1,
		EvidenceGeneratedAt: now, IsComplete: false,
	}
	request := domain.PlanRequest{
		RepositoryID: repositoryID, SnapshotID: snapshotID, BaseSHA: "base", HeadSHA: "head",
		CurrentWorktreeHash: "worktree", Mode: domain.ModeFastFeedback, Now: now,
		PolicyVersion: 1, PolicySnapshotTrusted: true, AdapterSupported: true, AdapterDiscoveryOK: false,
	}
	evidence := analysis.EvidenceSnapshot{
		Snapshot:      snapshot,
		ChangedFiles:  []domain.ChangedFile{{Path: "src/main.go", Status: domain.ChangeModified}},
		GraphComplete: false, DiscoveryReliable: false, PolicyTrusted: true,
	}
	policy := domain.Policy{Version: 1, PolicyAvailable: true, FullSuiteTargetRef: "configured-full-suite"}

	plan, err := analysis.CreatePlan(request, evidence, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFullRequired {
		t.Fatalf("incomplete evidence produced %s instead of FULL_REQUIRED", plan.Status)
	}
	if len(plan.SelectedUnits) != 1 || plan.SelectedUnits[0].StableKey != "FULL_SUITE" || plan.SelectedUnits[0].TargetRef != "configured-full-suite" {
		t.Fatalf("configured full-suite fallback missing: %+v", plan.SelectedUnits)
	}
}
