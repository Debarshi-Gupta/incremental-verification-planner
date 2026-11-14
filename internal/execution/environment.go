package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/domain"
)

// ErrEnvironmentMismatch reports that the execution environment fingerprint
// differs from the fingerprint the plan was created for (LLD 9.4). A selective
// run must not continue in that state: the executor escalates to the
// authoritative full-suite fallback instead.
var ErrEnvironmentMismatch = errors.New("execution environment fingerprint differs from the plan fingerprint")

// AdapterToolchainDigest detects the version of every adapter in the current
// workspace and returns the canonical toolchain digest. It is the execution
// side counterpart of the digest the indexer records in a snapshot, so both
// sides can be compared directly.
func AdapterToolchainDigest(ctx context.Context, root string, available []adapters.Adapter) (string, error) {
	versions := map[string]string{}
	for _, adapter := range available {
		if adapter == nil {
			continue
		}
		version, err := adapter.Version(ctx, root)
		if err != nil {
			return "", fmt.Errorf("detect adapter %q version: %w", adapter.ID(), err)
		}
		versions[adapter.ID()] = version
	}
	if len(versions) == 0 {
		return "", errors.New("no adapter version could be detected")
	}
	return domain.CanonicalDigest(versions)
}

// ValidateEnvironment compares a recomputed execution environment fingerprint
// with the fingerprint the plan was planned for. An unknown fingerprint on
// either side is not treated as a difference: IVP never claims a comparison it
// could not perform.
func ValidateEnvironment(plan domain.VerificationPlan, recomputed string) error {
	planned := strings.TrimSpace(plan.EnvironmentFingerprint)
	observed := strings.TrimSpace(recomputed)
	if planned == "" || observed == "" {
		return nil
	}
	if planned == observed {
		return nil
	}
	return fmt.Errorf("%w: the execution environment fingerprint %q differs from the planned fingerprint %q", ErrEnvironmentMismatch, observed, planned)
}
