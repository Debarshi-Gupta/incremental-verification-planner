package cli

import "github.com/ivp/ivp/internal/domain"

// exitCodeForOutcome maps an execution or audit outcome to the documented CLI
// exit code (LLD 11.2):
//
//	0  required checks for the selected mode passed
//	1  one or more executed tests/checks failed
//	2  invalid user/repository configuration
//	3  unsafe/unsupported environment, or forced to full with no fallback
//	4  API/auth/policy failure; the safe fallback was printed but not executed
//	5  stale snapshot/plan; nothing unsafe was executed
//	6  adapter, report-parser or infrastructure error
//	7  audit detected a confirmed or unresolved possible miss
//	8  user cancelled execution
//
// Codes 2, 3 and 4 are produced by the command paths that detect invalid
// input, an unsupported environment and an API/auth/policy failure; they are
// returned as exitError values by those paths. The outcome-driven codes are
// decided here so the table has exactly one implementation.
//
// cancelled distinguishes a cancelled run (8) from any other infrastructure
// failure (6): both share the INFRA_FAILURE outcome.
func exitCodeForOutcome(outcome domain.ExecutionOutcome, cancelled bool) int {
	switch outcome {
	case domain.OutcomeTargetedPass, domain.OutcomeFullVerificationPass:
		return 0
	case domain.OutcomeTargetedFail, domain.OutcomeFullVerificationFail:
		return 1
	case domain.OutcomeStale:
		return 5
	case domain.OutcomeAuditMiss, domain.OutcomeAuditPossibleMiss:
		return 7
	case domain.OutcomeInfraFailure:
		if cancelled {
			return 8
		}
		return 6
	default:
		// An unknown outcome is never reported as a pass.
		return 6
	}
}
