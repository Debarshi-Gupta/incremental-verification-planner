package config

import "strings"

// ReadinessIssues reports configuration omissions that prevent safe full-suite
// execution or reduce readiness. An empty argv is allowed for configuration
// parsing so a user can run `ivp doctor` and receive this actionable report.
func (c Config) ReadinessIssues() []string {
	var issues []string
	if len(c.Execution.FullSuiteArgv) == 0 {
		issues = append(issues, "execution.full_suite_argv is empty; FULL_REQUIRED and offline fallback cannot execute")
	}
	if strings.TrimSpace(c.Server.Endpoint) == "" {
		issues = append(issues, "server.endpoint is empty; planning requires a reachable IVP API or a trusted local policy snapshot")
	}
	if strings.TrimSpace(c.Repository.Slug) == "" {
		issues = append(issues, "repository.slug is empty; repository identity cannot be resolved")
	}
	return issues
}

func (c Config) FullSuiteArgv() []string {
	return append([]string(nil), c.Execution.FullSuiteArgv...)
}
